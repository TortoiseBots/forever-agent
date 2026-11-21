// Package follow is the small follow loop a ForeverCore session runs behind one other character: it keeps the
// client near the leader by walking to the navmesh point just behind them, and it ends when the leader is on another
// map or stays unseen for the loss window. It is deliberately not the 1.12 follower: no chat, no GM, no party
// orders, one leader and one distance rule.
//
// The loop ticks every Tick. Beyond Far it walks once to Near behind the leader (the walk is synchronous, so the
// next tick runs after the character arrives); inside Near it stops a walk that ended early; between the two it does
// nothing, which is the hysteresis that keeps a caught-up follower from twitching.
//
// A walk or a stop the session refuses, or that fails because the link is reconnecting, is retried: one failure does
// not end the follow. The loop keeps trying, one attempt per RetryGap and from the second attempt on toward the
// leader's own position instead of the point behind them (that point may have no route), until the failures have
// lasted Retry; only then does it end as failed. A success clears the count.
//
// Following yourself is refused at once. Two characters following each other cannot be told apart from one
// follower, which is a documented limit: each walks to the point behind the other until both stand inside Far of
// each other, where neither walks again, so the pair settles instead of chasing (TestFollowMutualPairSettles).
package follow

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// Vec3 is a world position.
type Vec3 struct {
	X, Y, Z float32
}

// Unit is one other character as the loop sees them: where they stand, which way they face and the map they are on.
type Unit struct {
	Name string
	Pos  Vec3
	// O is the orientation in radians in the core's convention: the angle of atan2(dy, dx), so +X is 0 and +Y is
	// pi/2, counter-clockwise (objects.OrientationTo, Position::GetAbsoluteAngle).
	O   float32
	Map uint32
}

// Game is what the loop needs from the session. Every method may block (the session posts to its own goroutine), so
// the loop never touches the session's state directly.
type Game interface {
	// Self is where this client stands.
	Self() Vec3
	// Unit names one character; false when the character is not in this client's world (unseen).
	Unit(name string) (Unit, bool)
	// Walk walks the client to to and returns when it has arrived, or with the walk's error (a refusal, a map
	// change, the caller's context ending).
	Walk(to Vec3) error
	// Stop stops the client's movement.
	Stop() error
	// Map is the map this client is on.
	Map() uint32
}

// MapEpoch is an optional Game method: a counter that changes whenever this client changes map, and does not change
// across a reconnect. The loop uses it to end as lost at once when the leader disappears after this client's own
// map change, instead of waiting out the loss window.
type MapEpoch interface {
	Epoch() uint64
}

// ZoneReader is an optional Game method: the zone another character is in as the client last learned it (the party
// roster's member state), without the character having to be in view. The loop ends as lost at once when a leader
// that went out of view is now in another zone than the one they were last seen in.
type ZoneReader interface {
	Zone(name string) (zone uint32, ok bool)
}

// Config is one follow loop's rule. The zero value means the defaults below; Near and Far are yards.
type Config struct {
	Lead string
	Near float64       // default NearDefault
	Far  float64       // default FarDefault
	Lost time.Duration // default LostDefault: unseen this long, the leader is lost
	Tick time.Duration // default TickDefault
	// Self is this client's own character name; a Lead equal to it (any case) is refused.
	Self string
	// Retry is how long walk or stop failures may go on before the loop gives up (default Lost); RetryGap is the
	// pause between two attempts (default RetryGapDefault, never below Tick).
	Retry    time.Duration
	RetryGap time.Duration
	// Now is the loop's clock; nil is time.Now. Tests shorten the loss window against the real clock.
	Now func() time.Time
}

// The defaults of Config.
const (
	NearDefault = 3.0
	FarDefault  = 8.0
	LostDefault = 30 * time.Second
	TickDefault = 250 * time.Millisecond
	// RetryGapDefault paces the retries of a refused walk: a route that has none is not planned four times a second.
	RetryGapDefault = time.Second
)

// The reasons a run ends, in Result.Reason.
const (
	// ReasonStopped: the caller's context ended.
	ReasonStopped = "stopped"
	// ReasonLost: the leader changed map or stayed unseen for the loss window.
	ReasonLost = "lost"
	// ReasonFailed: the client could not walk or stop (the link dropped, the map changed, a refusal).
	ReasonFailed = "failed"
)

// Result is what one run did.
type Result struct {
	Reason string
	Detail string
	Walks  int
	Stops  int
	// Retries counts the walk and stop attempts that failed and were tried again.
	Retries int
}

// withDefaults fills the zero fields.
func (c Config) withDefaults() Config {
	if c.Near <= 0 {
		c.Near = NearDefault
	}
	if c.Far <= c.Near {
		c.Far = FarDefault
		if c.Far <= c.Near {
			c.Far = c.Near + 1
		}
	}
	if c.Lost <= 0 {
		c.Lost = LostDefault
	}
	if c.Tick <= 0 {
		c.Tick = TickDefault
	}
	if c.Retry <= 0 {
		c.Retry = c.Lost
	}
	if c.RetryGap <= 0 {
		c.RetryGap = RetryGapDefault
	}
	if c.RetryGap < c.Tick {
		c.RetryGap = c.Tick
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// SameName says whether two character names are the same character (names are case-insensitive).
func SameName(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && strings.EqualFold(a, b)
}

// Run follows cfg.Lead until the caller's context ends, the leader is lost, or the walks keep failing. It never
// sends chat or a GM line: the only packets it causes are the session's own movement ones.
func Run(ctx context.Context, g Game, cfg Config) Result {
	cfg = cfg.withDefaults()
	var res Result
	if SameName(cfg.Lead, cfg.Self) {
		res.Reason = ReasonFailed
		res.Detail = fmt.Sprintf("%s is this character: a character cannot follow itself", cfg.Lead)
		return res
	}
	epoch, hasEpoch := g.(MapEpoch)
	zones, hasZones := g.(ZoneReader)
	var (
		lastSeen   time.Time
		everSeen   bool
		seenEpoch  uint64
		seenZone   uint32
		haveZone   bool
		failedFrom time.Time // the first failure of the current streak; zero when the last attempt worked
		walking    bool
	)
	timer := time.NewTimer(0) // the first tick runs at once
	defer timer.Stop()
	// fail records one failed walk or stop. It returns true when the loop ends: the caller's context ended (a stop,
	// not a failure) or the failures have lasted the retry window.
	fail := func(what string, err error) bool {
		if ctx.Err() != nil {
			res.Reason = ReasonStopped
			return true
		}
		now := cfg.Now()
		if failedFrom.IsZero() {
			failedFrom = now
		}
		if now.Sub(failedFrom) >= cfg.Retry {
			res.Reason = ReasonFailed
			res.Detail = fmt.Sprintf("%s: %v (still failing after %s)", what, err, cfg.Retry)
			return true
		}
		res.Retries++
		return false
	}
	for {
		select {
		case <-ctx.Done():
			res.Reason = ReasonStopped
			return res
		case <-timer.C:
		}
		delay := cfg.Tick
		now := cfg.Now()
		u, ok := g.Unit(cfg.Lead)
		switch {
		case !ok:
			if lastSeen.IsZero() {
				lastSeen = now
			}
			if everSeen && hasEpoch && epoch.Epoch() != seenEpoch {
				res.Reason = ReasonLost
				res.Detail = fmt.Sprintf("this client changed map after %s was last seen", cfg.Lead)
				return res
			}
			if everSeen && hasZones && haveZone {
				if z, known := zones.Zone(cfg.Lead); known && z != seenZone {
					res.Reason = ReasonLost
					res.Detail = fmt.Sprintf("%s went from zone %d to zone %d", cfg.Lead, seenZone, z)
					return res
				}
			}
			if now.Sub(lastSeen) >= cfg.Lost {
				res.Reason = ReasonLost
				res.Detail = fmt.Sprintf("%s was not seen for %s", cfg.Lead, cfg.Lost)
				return res
			}
		default:
			lastSeen, everSeen = now, true
			if hasEpoch {
				seenEpoch = epoch.Epoch()
			}
			if hasZones {
				seenZone, haveZone = zones.Zone(cfg.Lead)
			}
			if u.Map != g.Map() {
				res.Reason = ReasonLost
				res.Detail = fmt.Sprintf("%s is on map %d and this client on map %d", cfg.Lead, u.Map, g.Map())
				return res
			}
			d := Distance(g.Self(), u.Pos)
			if d <= cfg.Near {
				if walking {
					if err := g.Stop(); err != nil {
						if fail("stop", err) {
							return res
						}
						delay = cfg.RetryGap
					} else {
						failedFrom = time.Time{}
						res.Stops++
						walking = false
					}
				}
			} else if d > cfg.Far {
				to := Behind(u, cfg.Near)
				if !failedFrom.IsZero() {
					to = u.Pos // the point behind the leader was refused: go to the leader themselves
				}
				if err := g.Walk(to); err != nil {
					if fail("walk", err) {
						return res
					}
					delay = cfg.RetryGap
				} else {
					failedFrom = time.Time{}
					res.Walks++
					walking = false // the walk ended: it arrived
				}
			}
		}
		timer.Reset(delay)
	}
}

// Behind is the point Near yards behind the leader: opposite the leader's facing, at the leader's height. Facing is
// the core's: the unit vector of orientation o is (cos o, sin o), so a leader at (0, 10) facing +X (o = 0) has
// (-near, 10) behind them and facing +Y (o = pi/2) has (0, 10-near).
func Behind(u Unit, near float64) Vec3 {
	o := float64(u.O)
	return Vec3{
		X: u.Pos.X - float32(math.Cos(o)*near),
		Y: u.Pos.Y - float32(math.Sin(o)*near),
		Z: u.Pos.Z,
	}
}

// Distance is the yards between two positions, 3D.
func Distance(a, b Vec3) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
