package world

// The daemon's small test-setup gaps (#99): `teleport`, `gm_evade` and `gm_respawn`. Each goes through the command
// channel (serveGMResult), so the core's own GM log carries the line and the GM guard runs first. The two unit hands
// are held to the same fail-closed boundary as gm_kill (serveGMUnit): the daemon must have been started with
// --allow-gm-units and the guid must be on the allowlist trial_units registered. `teleport` moves only this
// session's own character, like the raw gm path it wraps, so it needs no allowlist.
//
// Core references (ForeverCore at the pin): `.go xyz <x> <y> [z [map [o]]]` is HandleGoXYZCommand
// (cs_go.cpp:376-399); `.npc evade [why] [force]` is HandleNpcEvadeCommand (cs_npc.cpp:1140-1161: it needs a selected
// non-pet creature with AI and prints only a refusal); `.respawn` is HandleRespawnCommand (cs_misc.cpp:1964-1983:
// with an explicit selection it respawns that one creature only if it is dead; with none it respawns every dead
// creature around the player, which is why gm_respawn refuses a unit the client shows alive and always confirms the
// selection first).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveTeleportParams is teleport's call: the point, and the map when it is another one. The three coordinates are
// required: the core's two-coordinate form picks the terrain height itself, which a caller that wants a known
// height does not want, and a fall is only computed from a stated one.
type serveTeleportParams struct {
	X   *float64 `json:"x"`
	Y   *float64 `json:"y"`
	Z   *float64 `json:"z"`
	Map *int64   `json:"map,omitempty"`
	// RequireGround makes an unknown ground an error: the answer then says the character WAS moved, and where the
	// ground could not be checked.
	RequireGround bool `json:"require_ground,omitempty"`
}

// ServeTeleport is teleport's result: the line sent, where the client stands afterwards (the landing's height when
// a fall was flown), and the fall.
type ServeTeleport struct {
	Sent     string  `json:"sent"`
	Map      uint32  `json:"map"`
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	Z        float32 `json:"z"`
	Fell     bool    `json:"fell"`
	DropYd   float32 `json:"drop_yd,omitempty"`
	Seq      uint64  `json:"teleport_seq"`
	ZUnknown bool    `json:"z_unknown,omitempty"`
	// Ground says what was found under the point: "standing" (ground within a yard), "landed" (a fall was flown), or
	// "unknown" with GroundWhy (no navmesh, no floor in reach, a failed query, water, or a far teleport, which starts no
	// fall). An unknown ground means the height is the one stated, not a checked one.
	Ground    string `json:"ground"`
	GroundWhy string `json:"ground_why,omitempty"`
}

// serveTeleportBound limits a coordinate: the world's own coordinate range is far smaller, and a bound keeps a huge
// or malformed number out of the line.
const serveTeleportBound = 1e6

// registerGapMethods wires the #99 methods.
func registerGapMethods(srv *rpc.Server, st *serveState) {
	srv.Register("teleport", serveCallTyped(st, func(s *session, ctx context.Context, p serveTeleportParams) (ServeTeleport, error) {
		return s.serveTeleport(ctx, p)
	}))
	srv.Register("gm_evade", serveCallTyped(st, func(s *session, ctx context.Context, p serveUnitParams) (ServeGMEvade, error) {
		return s.serveGMEvade(ctx, p)
	}))
	srv.Register("gm_respawn", serveCallTyped(st, func(s *session, ctx context.Context, p serveUnitParams) (ServeCommand, error) {
		return s.serveGMRespawn(ctx, p)
	}))
}

// serveTeleportLine builds the `.go xyz` line, refusing anything the line could not carry.
func serveTeleportLine(p serveTeleportParams) (string, error) {
	if p.X == nil || p.Y == nil || p.Z == nil {
		return "", errors.New("teleport needs x, y and z")
	}
	for _, c := range []float64{*p.X, *p.Y, *p.Z} {
		if math.IsNaN(c) || math.IsInf(c, 0) || math.Abs(c) > serveTeleportBound {
			return "", fmt.Errorf("teleport: %v is not a coordinate", c)
		}
	}
	line := fmt.Sprintf(".go xyz %.2f %.2f %.2f", *p.X, *p.Y, *p.Z)
	if p.Map != nil {
		if *p.Map < 0 || *p.Map > math.MaxInt32 {
			return "", fmt.Errorf("teleport: map %d is not a map id", *p.Map)
		}
		line += fmt.Sprintf(" %d", *p.Map)
	}
	return line, nil
}

// serveTeleport puts the character at a point with the core's `.go xyz`, waits for the server's own move of it (the
// gm path's teleport confirmation), and, when the point is above the ground the navmesh knows, for the fall the
// client flies from there (fall.go) to land.
func (s *session) serveTeleport(ctx context.Context, p serveTeleportParams) (ServeTeleport, error) {
	line, err := serveTeleportLine(p)
	if err != nil {
		return ServeTeleport{}, err
	}
	falls := s.fallStarts
	s.ground = groundNote{}
	if _, err := s.serveGMResult(ctx, line); err != nil {
		return ServeTeleport{}, fmt.Errorf("teleport: %w", err)
	}
	out := ServeTeleport{Sent: line, Fell: s.fallStarts != falls}
	if s.fallActive() {
		if err := s.waitFallLanded(ctx); err != nil {
			return ServeTeleport{}, err
		}
	}
	out.Map = s.world.MapID()
	out.Seq = s.teleportSeq
	out.ZUnknown = s.zUnknown
	if s.mover != nil {
		x, y, z, _ := s.mover.Position()
		out.X, out.Y, out.Z = x, y, z
	} else if o, ok := s.world.Object(s.selfOrMover()); ok {
		out.X, out.Y, out.Z = o.X, o.Y, o.Z
	}
	if out.Fell {
		out.DropYd = float32(*p.Z) - out.Z
	}
	out.Ground, out.GroundWhy = s.ground.state, s.ground.why
	if out.Ground == "" {
		out.Ground, out.GroundWhy = "unknown", "no near teleport was seen (a far teleport starts no fall)"
	}
	if out.Ground == "unknown" {
		out.ZUnknown = true
		if p.RequireGround {
			return out, fmt.Errorf("teleport: the character was moved to the point, but the ground under it is unknown: %s", out.GroundWhy)
		}
	}
	return out, nil
}

// waitFallLanded waits, under the caller's context, until the fall in progress has landed: its computed landing plus
// fallLandWait.
func (s *session) waitFallLanded(ctx context.Context) error {
	f := s.flight
	if f == nil {
		return nil
	}
	end := f.land.Add(fallLandWait)
	for s.fallActive() {
		if !s.Now().Before(end) {
			return fallError(fallLandWait)
		}
		if err := s.collect(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// serveGMCreatureHand is what gm_evade and gm_respawn share: resolve the unit, hold it to the GM boundary, refuse a
// player, and select it with the server's echo confirmed, so the line acts on it and on nothing else.
func (s *session) serveGMCreatureHand(ctx context.Context, method string, p serveUnitParams) (*objects.Object, error) {
	o, err := s.serveUnitByName(p.GUID, p.Name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if err := s.serveGMUnit(o, method); err != nil {
		return nil, err
	}
	if o.Type == objects.TypePlayer || o.Type == objects.TypeActivePlayer || !o.Info.HasUnit {
		return nil, fmt.Errorf("%s: %s is not a creature", method, o.GUID)
	}
	return o, nil
}

// ServeGMEvade is gm_evade's result: the line's answer and what the server did to the creature. `.npc evade` prints
// nothing when it works (cs_npc.cpp:1140-1161), so the line's silence proves nothing about the creature: Effect says
// what the client's own view of it shows afterwards. "left_combat": it was in combat and is not any more. "none": it
// was not in combat, so there was nothing to leave (the call changed nothing the client can see). A creature that is
// still in combat after the settle wait is an error, never a result.
type ServeGMEvade struct {
	ServeCommand
	InCombatBefore bool   `json:"in_combat_before"`
	InCombatAfter  bool   `json:"in_combat_after"`
	Effect         string `json:"effect"`
}

// gmEvadeSettle bounds the wait for the creature's combat flag to clear after the line. A variable so the tests
// shorten it.
var gmEvadeSettle = 3 * time.Second

// serveGMEvade makes one creature leave combat, walk home and reset (`.npc evade`), and reports whether the server
// did it: the creature's in-combat flag before and after, read from the update stream.
func (s *session) serveGMEvade(ctx context.Context, p serveUnitParams) (ServeGMEvade, error) {
	o, err := s.serveGMCreatureHand(ctx, "gm_evade", p)
	if err != nil {
		return ServeGMEvade{}, err
	}
	if err := s.serveSelectConfirmed(ctx, o, serveGMSelectWait); err != nil {
		return ServeGMEvade{}, fmt.Errorf("gm_evade: %w", err)
	}
	out := ServeGMEvade{InCombatBefore: o.InCombat}
	guid := o.GUID
	cmd, err := s.serveGMResult(ctx, ".npc evade")
	if err != nil {
		return ServeGMEvade{}, err
	}
	out.ServeCommand = cmd
	inCombat := func() bool {
		if cur, ok := s.world.Object(guid); ok {
			return cur.InCombat
		}
		return false
	}
	if out.InCombatBefore {
		end := s.Now().Add(gmEvadeSettle)
		for inCombat() && s.Now().Before(end) {
			if err := s.collect(ctx, 50*time.Millisecond); err != nil {
				return ServeGMEvade{}, err
			}
		}
	}
	out.InCombatAfter = inCombat()
	switch {
	case out.InCombatBefore && out.InCombatAfter:
		return ServeGMEvade{}, fmt.Errorf("gm_evade: %s is still in combat %s after .npc evade (the core printed no refusal)", guid, gmEvadeSettle)
	case out.InCombatBefore:
		out.Effect = "left_combat"
	default:
		out.Effect = "none"
	}
	return out, nil
}

// serveGMRespawn brings one dead creature back (`.respawn` with it selected). A unit the client shows alive is
// refused before anything is sent.
func (s *session) serveGMRespawn(ctx context.Context, p serveUnitParams) (ServeCommand, error) {
	o, err := s.serveGMCreatureHand(ctx, "gm_respawn", p)
	if err != nil {
		return ServeCommand{}, err
	}
	if !o.Dead {
		return ServeCommand{}, fmt.Errorf("gm_respawn: %s is alive", o.GUID)
	}
	if err := s.serveSelectConfirmed(ctx, o, serveGMSelectWait); err != nil {
		return ServeCommand{}, fmt.Errorf("gm_respawn: %w", err)
	}
	return s.serveGMResult(ctx, ".respawn")
}
