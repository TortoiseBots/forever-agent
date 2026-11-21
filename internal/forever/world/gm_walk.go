// The live run: follow the GM, sample the GM's own object position, watch whispers/say for marks.

package world

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/gmwalk"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// GMWalkOptions is `forever gm-walk`: follow the GM named Follow, sample the GM's own object position, and
// record the GM's mark words into world data. Nil means the session runs something else.
type GMWalkOptions struct {
	// Follow is the GM character to stay near. Required. The name is only the lookup key for the resolve
	// step: it never reaches the output (file names and the report carry the map id, never who walked).
	Follow string
	// MarksFrom limits which senders may mark: "gm" (only the followed GM, the default) or "any" (any sender's
	// whisper or nearby say). Empty is "gm".
	MarksFrom string
	// Sample bounds the position sample interval. Zero takes the default.
	Sample time.Duration
	// ToleranceYd is the thinning tolerance in yards. Zero takes the default.
	ToleranceYd float64
	// FollowYd is how close the agent stays to the GM in yards. Zero takes the default.
	FollowYd float32
}

// GMWalkReport is one gm-walk run: what was sampled and marked. It carries the map id, never a character name.
type GMWalkReport struct {
	Map       uint32    `json:"map"`
	Samples   int       `json:"samples"`
	Marks     int       `json:"marks"`
	Ignored   int       `json:"ignored"`
	Unknown   int       `json:"unknown"`
	Points    int       `json:"points"`
	Zones     int       `json:"zones"`
	Roads     int       `json:"roads"`
	Minutes   float64   `json:"minutes"`
	EndedWhy  string    `json:"ended"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// GMWalkResult is the run's report plus the data: the command writes the data file.
type GMWalkResult struct {
	Report GMWalkReport
	Data   gmwalk.WorldData
}

// GMWalk defaults.
const (
	GMWalkSample    = 2 * time.Second
	GMWalkTolerance = 1.0
	GMWalkFollowYd  = 6.0
	// GMWalkIdle ends the run when no sample moved the agent for this long: the GM stopped walking.
	GMWalkIdle = 90 * time.Second
)

// GMWalkMarkSources are the --marks-from values.
const (
	GMWalkFromGM  = "gm"
	GMWalkFromAny = "any"
)

func (o *GMWalkOptions) withDefaults() GMWalkOptions {
	out := *o
	if out.MarksFrom == "" {
		out.MarksFrom = GMWalkFromGM
	}
	if out.Sample <= 0 {
		out.Sample = GMWalkSample
	}
	if out.ToleranceYd <= 0 {
		out.ToleranceYd = GMWalkTolerance
	}
	if out.FollowYd <= 0 {
		out.FollowYd = GMWalkFollowYd
	}
	return out
}

// gmWalkTap counts what the mark feed did with each line: marks stored, ignored (parsed but no position or past
// a bound), unknown (not the GM's, not a mark word).
type gmWalkTap struct {
	marks   int
	ignored int
	unknown int
}

// gmWalkTap feeds one chat line into the recorder. Only whispers and nearby say lines are marks; the GM's own
// say echoes back what the agent hears nearby, and a whisper reaches the agent anywhere. With MarksFrom "gm"
// only the followed GM's lines count, matched by the line's sender GUID against the resolved GM guid: a stranger
// using the same words, or a line whose sender name merely matches the GM's (including a FakeSenderName system
// line), is unknown chatter, counted, never stored.
func (s *session) gmWalkTap(rec *gmwalk.Recorder, o *GMWalkOptions, tap *gmWalkTap, gm wire.GUID, line chat.Line) {
	if line.Type != chat.TypeWhisper && line.Type != chat.TypeSay {
		return
	}
	if line.FakeSenderName {
		tap.unknown++
		return
	}
	if o.MarksFrom != GMWalkFromAny && line.SenderGUID != gm {
		tap.unknown++
		return
	}
	m, err := gmwalk.ParseMark(line.Text)
	if err != nil {
		tap.unknown++
		return
	}
	pos, known := gmPosOf(s, gm)
	if err := rec.MarkAtGM(pos, known, m); err != nil {
		tap.ignored++
		return
	}
	tap.marks++
}

// gmPosOf is the GM's own object position for a mark: the resolved GM guid's world-model entry, if known.
// The second value is false when the GM is not in the model (out of range or the update has not arrived yet),
// and the mark is then ignored, never stamped at the agent's position.
func gmPosOf(s *session, gm wire.GUID) (gmwalk.Sample, bool) {
	if s == nil || s.world == nil {
		return gmwalk.Sample{}, false
	}
	obj, ok := s.world.Object(gm)
	if !ok {
		return gmwalk.Sample{}, false
	}
	return gmwalk.Sample{Map: s.world.MapID(), X: obj.X, Y: obj.Y, Z: obj.Z}, true
}

// gmWalkChase follows the GM until the caller's context ends: one ChaseTo per poll tick, re-aimed at the GM's
// latest model position. It returns when the context ends. Every sample is the GM's own object position from
// the world model: the road is the walk the GM walked, not the agent's chase path behind it. The pump runs
// while the agent chases, so marks land at the GM's position when the line arrived, not at a stale sample.
func (s *session) gmWalk(ctx context.Context) (*GMWalkResult, error) {
	raw := s.opts.GMWalk
	if raw == nil {
		return nil, fmt.Errorf("gm-walk: no options")
	}
	o := raw.withDefaults()
	// Resolve the GM from the names the client learned; the GM may log in after the agent.
	resolveDeadline := time.Now().Add(2 * time.Minute)
	var guid wire.GUID
	name, err := chat.NormaliseName(o.Follow)
	if err != nil {
		return nil, fmt.Errorf("gm-walk: --follow: %w", err)
	}
	if err := s.collect(ctx, s.settleFor()); err != nil {
		return nil, err
	}
	for {
		s.queryNames(ctx)
		if g, ok := s.world.PlayerByName(name); ok {
			guid = g
			break
		}
		if time.Now().After(resolveDeadline) {
			return nil, fmt.Errorf("gm-walk: no player named %s among the names the model knows", chat.SanitiseName(name))
		}
		if err := s.collect(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	s.opts.logf("gm-walk: following the GM (marks from %s, sample %s)", o.MarksFrom, o.Sample)

	rec := &gmwalk.Recorder{}
	tap := &gmWalkTap{}
	// The tap sees every chat line the pump handles: whispers and say lines from the GM become marks at the
	// GM's own object position.
	prevFeed := s.cmdFeed
	s.cmdFeed = func(line chat.Line) {
		if prevFeed != nil {
			prevFeed(line)
		}
		s.gmWalkTap(rec, &o, tap, guid, line)
	}
	defer func() { s.cmdFeed = prevFeed }()

	start := time.Now()
	lastMove := start
	var lastX, lastY, lastZ float32
	moved := false
	sampleAt := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			break
		}
		obj, ok := s.world.Object(guid)
		if !ok {
			// The GM left the model (out of range or offline): hold position and keep listening; the idle
			// bound below ends the run if nothing moves.
			s.opts.logf("gm-walk: the followed GM is not in the world model (holding position)")
		} else {
			gx, gy, gz := obj.X, obj.Y, obj.Z
			ax, ay, az, _ := s.mover.Position()
			if objects.Distance(ax, ay, az, gx, gy, gz) > o.FollowYd {
				goal := objects.ChaseGoal{
					Dest: func(time.Time) (float32, float32, float32) {
						if cur, ok := s.world.Object(guid); ok {
							return cur.X, cur.Y, cur.Z
						}
						return gx, gy, gz
					},
					Arrived: func(time.Time) bool {
						bx, by, bz, _ := s.mover.Position()
						var tx, ty, tz float32
						if cur, ok := s.world.Object(guid); ok {
							tx, ty, tz = cur.X, cur.Y, cur.Z
						} else {
							tx, ty, tz = gx, gy, gz
						}
						return objects.Distance(bx, by, bz, tx, ty, tz) <= o.FollowYd
					},
				}
				if err := s.mover.ChaseTo(ctx, goal); err != nil {
					if ctx.Err() != nil {
						break
					}
					// A failed chase (replan bound, map change) is retried next tick; stopping the whole
					// recording for one bad leg would lose the walk.
					s.opts.logf("gm-walk: chase: %v", err)
				}
			}
		}
		if err := s.collect(ctx, o.Sample); err != nil {
			if ctx.Err() != nil {
				break
			}
			return nil, err
		}
		now := time.Now()
		if now.Sub(sampleAt) >= o.Sample {
			sampleAt = now
			smp, ok := s.gmWalkSample(guid)
			if !ok {
				continue
			}
			rec.AddSample(smp)
			if !moved || dist3(smp.X, smp.Y, smp.Z, lastX, lastY, lastZ) > 0.5 {
				lastX, lastY, lastZ, moved = smp.X, smp.Y, smp.Z, true
				lastMove = now
			}
			if now.Sub(lastMove) > GMWalkIdle {
				s.opts.logf("gm-walk: no movement for %s; ending the run", GMWalkIdle)
				break
			}
		}
	}
	ended := time.Now()
	data, buildErr := rec.Build(o.ToleranceYd)
	res := &GMWalkResult{
		Data: data,
		Report: GMWalkReport{
			Map:       s.world.MapID(),
			Samples:   rec.Samples(),
			Marks:     tap.marks,
			Ignored:   tap.ignored + rec.Ignored(),
			Unknown:   tap.unknown,
			Points:    len(data.Points),
			Zones:     len(data.Zones),
			Roads:     len(data.Roads),
			Minutes:   ended.Sub(start).Minutes(),
			StartedAt: start,
			EndedAt:   ended,
		},
	}
	if ctx.Err() != nil {
		res.Report.EndedWhy = "deadline"
	} else {
		res.Report.EndedWhy = "idle"
	}
	if buildErr != nil {
		return res, buildErr
	}
	return res, nil
}

// gmWalkSample is one sample of the walk: the GM's own object position with the map id of the same update.
// It reports false while a transfer is open (the client stands between maps: pairing the new map with the
// old coordinates — or the reverse — would draw one stray road across it) and when the GM is not in the model
// (out of range or the update has not arrived yet): the caller skips the sample, never guesses. The pump runs
// on this same goroutine between the two reads, so the map and the object are one update's content.
func (s *session) gmWalkSample(gm wire.GUID) (gmwalk.Sample, bool) {
	if s == nil || s.world == nil || s.betweenMaps() {
		return gmwalk.Sample{}, false
	}
	return gmPosOf(s, gm)
}

func (s *session) settleFor() time.Duration {
	if s.opts.Settle > 0 {
		return s.opts.Settle
	}
	return 4 * time.Second
}

// dist3 is the 3D distance in yards between two points.
func dist3(ax, ay, az, bx, by, bz float32) float32 {
	dx, dy, dz := ax-bx, ay-by, az-bz
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}
