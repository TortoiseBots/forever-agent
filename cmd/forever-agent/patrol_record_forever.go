package main

// patrol-record --forever: the same recording loop over a `forever serve`
// slot. The stance allow-list, the safety rules and the file format are the
// 1.12 command's; only the client differs (serveclient, 128-bit guids) and
// the navmesh floors are read from a v16 mmaps folder. The analysis key of a
// sample is the serve key (GUID.Key()), while the file carries the unit's
// full 32-hex guid.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/patrols"
	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/world"
)

// patrolForeverClient is the part of serveclient.Client patrol-record --forever
// uses; a test injects its own.
type patrolForeverClient interface {
	Status(ctx context.Context) (serveclient.Status, error)
	Unit(ctx context.Context, guid, name string) (serveclient.Unit, error)
	Units(ctx context.Context, p serveclient.UnitsParams) ([]serveclient.Unit, error)
	Party(ctx context.Context) (serveclient.Party, error)
	GM(ctx context.Context, text string) (serveclient.Command, error)
}

// patrolForeverOpen opens a serve slot; tests replace it.
var patrolForeverOpen = func(dir string) (patrolForeverClient, error) {
	c, err := serveclient.Open(dir)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// patrolRecordForeverFloors lists the surfaces at (x, y) on the v16 mesh.
func patrolRecordForeverFloors(mmaps string, mapID uint32, x, y float32) ([]float32, error) {
	pl := navmesh.NewForeverPlanner(mmaps, nil)
	defer pl.Close()
	fl, err := pl.Floors(mapID, x, y, -2000, 2000)
	if err != nil {
		return nil, err
	}
	out := make([]float32, 0, len(fl))
	for _, f := range fl {
		out = append(out, f.Z)
	}
	return out, nil
}

// patrolForeverErrCode maps a serveclient failure like the 1.12 loop maps its
// own: a JSON-RPC answer is an RPC error (16), anything else means the daemon
// cannot be reached (15).
func patrolForeverErrCode(err error) int {
	if _, ok := serveclient.CodeOf(err); ok {
		return exitRPCError
	}
	return exitSlotRefused
}

// patrolForeverSameGUID compares two serve guids by their full 128-bit text
// (canonical 32-hex), never by a 64-bit half: two guids can share one half.
func patrolForeverSameGUID(a, b serveclient.GUID) bool {
	if a == "" || b == "" {
		return false
	}
	return bughunt.CanonGUID(string(a)) == bughunt.CanonGUID(string(b))
}

// patrolForeverServer names the daemon for the file's source line.
func patrolForeverServer(dir string) string {
	if _, m, err := slotdir.Read(dir); err == nil && m.Server != "" {
		return m.Server
	}
	return "forever"
}

// patrolRecordForever is the --forever body of patrol-record: the group and
// world checks, the allow-listed stance lines, the teleport wait, the sample
// loop and the file, through one serve slot.
func patrolRecordForever(ctx context.Context, env patrolRecordEnv, o patrolRecordOptions, spawns []patrolRecordSpawn, stdout, stderr io.Writer) int {
	client, err := env.openForever(o.dir)
	if err != nil {
		var refused *slotOpenError
		if errors.As(err, &refused) {
			fmt.Fprintln(stderr, refused.msg)
			return refused.code
		}
		fmt.Fprintf(stderr, "patrol-record: %v\n", err)
		return exitSlotRefused
	}
	// Never record inside a group: the teleport would land in a bound raid's instance.
	party, err := client.Party(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patrol-record: party: %v\n", err)
		return patrolForeverErrCode(err)
	}
	if party.InGroup {
		fmt.Fprintln(stderr, "patrol-record: the slot agent is in a group; a recording goes into the GM's own instance, so leave the group first")
		return exitSlotRefused
	}
	st, err := client.Status(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patrol-record: status: %v\n", err)
		return patrolForeverErrCode(err)
	}
	if !patrolForeverLive(st) {
		fmt.Fprintf(stderr, "patrol-record: the slot agent is not a live character in the world (state %q, dead %v, ghost %v)\n",
			st.State, st.Dead, st.Ghost)
		return exitSlotRefused
	}
	if st.GUID == "" {
		fmt.Fprintln(stderr, "patrol-record: the serve slot's status names no guid")
		return exitSlotRefused
	}

	// Stance and teleport: the allow-list's three lines.
	for _, line := range []string{
		".gm on",
		".gm visible off",
		fmt.Sprintf(".go xyz %g %g %g %d", o.x, o.y, o.z, o.mapID),
	} {
		if err := patrolForeverGM(ctx, client, line); err != nil {
			fmt.Fprintf(stderr, "patrol-record: %s: %v\n", line, err)
			return patrolForeverErrCode(err)
		}
	}
	// The safety rule stops on a map mismatch, so wait for the teleport to land first.
	if code := patrolForeverWaitForMap(ctx, client, env, o, stderr); code != exitOK {
		return code
	}

	started := env.now()
	samples, stop, guidText := patrolForeverLoop(ctx, client, env, o)
	// Visibility is restored whatever ended the recording, including Ctrl-C: the loop's ctx is already cancelled
	// then, so the restore gets a fresh one of its own.
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := patrolForeverGM(restoreCtx, client, ".gm visible on"); err != nil {
		fmt.Fprintf(stderr, "patrol-record: .gm visible on: %v\n", err)
	}
	source := fmt.Sprintf("record %s %s", patrolForeverServer(o.dir), started.Format("2006-01-02"))
	return patrolRecordWrite(o, source, started, samples, stop, spawns, guidText, stdout, stderr)
}

// patrolForeverLive says whether a serve status is a live character in the
// world: the serve link is online (an older serve's empty state counts), the
// character is named and it is neither dead nor a ghost.
func patrolForeverLive(st serveclient.Status) bool {
	if st.Character == "" || (st.State != "" && st.State != "online") {
		return false
	}
	return !st.Dead && !st.Ghost
}

// patrolForeverGM sends one stance line, and only a line of the allow-list. The core's refusal lines are errors:
// a line that only looks accepted still leaves the character in the wrong stance (invisible, not a GM, elsewhere).
func patrolForeverGM(ctx context.Context, client patrolForeverClient, line string) error {
	if !patrolRecordAllowedLine(line) {
		return fmt.Errorf("line outside the allow-list: %q", line)
	}
	cmd, err := client.GM(ctx, line)
	if err != nil {
		return err
	}
	if ref := patrolForeverRefusal(cmd); ref != "" {
		return fmt.Errorf("the server refused: %s", ref)
	}
	return nil
}

// patrolForeverRefusal returns the core's refusal text in a GM answer, or "" when the answer carried none. The
// refusal phrases are the ones the plan scout's GM calls already check (core LANG_* error rows).
func patrolForeverRefusal(cmd serveclient.Command) string {
	for _, l := range cmd.Lines {
		low := strings.ToLower(l)
		for _, bad := range scoutGMErrorTexts {
			if strings.Contains(low, bad) {
				return strings.TrimSpace(l)
			}
		}
	}
	return ""
}

// patrolForeverLandYd is how near (x, y) the teleport must land before the recording starts: `.go xyz` acking says
// nothing about where the server put the character.
const patrolForeverLandYd = 10

// patrolForeverUnitsLimit keeps a busy area's units read from being cut at serve's default of 100: a hostile unit
// targeting the agent must not fall off the end of the list the safety rules scan.
const patrolForeverUnitsLimit = 5000

// patrolForeverWaitForMap polls status until the teleport shows map M near (o.x, o.y).
func patrolForeverWaitForMap(ctx context.Context, client patrolForeverClient, env patrolRecordEnv, o patrolRecordOptions, stderr io.Writer) int {
	deadline := env.now().Add(patrolRecordArriveWait)
	for {
		st, err := client.Status(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patrol-record: status: %v\n", err)
			return patrolForeverErrCode(err)
		}
		if !patrolForeverLive(st) {
			fmt.Fprintf(stderr, "patrol-record: the slot agent left the world (state %q, dead %v, ghost %v)\n",
				st.State, st.Dead, st.Ghost)
			return exitSlotRefused
		}
		if st.Map == o.mapID && patrolRecordDist2DSq(st.X, st.Y, o.x, o.y) <= patrolForeverLandYd*patrolForeverLandYd {
			return exitOK
		}
		if !env.now().Before(deadline) {
			fmt.Fprintf(stderr, "patrol-record: the agent did not reach map %d at %.0f,%.0f (it is on map %d at %.0f,%.0f) within %s\n",
				o.mapID, o.x, o.y, st.Map, st.X, st.Y, patrolRecordArriveWait)
			return exitSlotRefused
		}
		if err := env.sleep(ctx, time.Second); err != nil {
			fmt.Fprintf(stderr, "patrol-record: interrupted while waiting for map %d\n", o.mapID)
			return exitSlotRefused
		}
	}
}

// patrolForeverLoop samples until the recording ends or a safety rule stops
// it. It returns the samples, the stop reason ("" for a complete run) and the
// key -> 32-hex guid text map the file's rows are rewritten with.
func patrolForeverLoop(ctx context.Context, client patrolForeverClient, env patrolRecordEnv, o patrolRecordOptions) ([]patrols.Sample, string, map[string]string) {
	total := o.minutes * 60_000 / o.sampleMS
	if total < 1 {
		total = 1
	}
	tracked := map[uint64]bool{}
	guidText := map[string]string{}
	var samples []patrols.Sample
	for i := range total {
		st, err := client.Status(ctx)
		if err != nil {
			return samples, "lost the agent: " + err.Error(), guidText
		}
		switch {
		case st.Character == "" || (st.State != "" && st.State != "online"):
			return samples, fmt.Sprintf("lost world (%s)", st.State), guidText
		case st.Dead:
			return samples, "dead", guidText
		case st.Ghost:
			return samples, "ghost", guidText
		case st.Map != o.mapID:
			return samples, fmt.Sprintf("left map %d (now on map %d)", o.mapID, st.Map), guidText
		}
		own, err := client.Unit(ctx, string(st.GUID), "")
		if err != nil {
			return samples, "lost the agent: " + err.Error(), guidText
		}
		if own.InCombat {
			return samples, "in combat", guidText
		}
		units, err := client.Units(ctx, serveclient.UnitsParams{Within: o.r + patrolRecordNearYd, Limit: patrolForeverUnitsLimit})
		if err != nil {
			return samples, "lost the agent: " + err.Error(), guidText
		}
		for _, u := range units {
			if !patrolForeverSameGUID(u.GUID, st.GUID) && patrolForeverSameGUID(u.Target, st.GUID) {
				return samples, "targeted by " + patrolForeverUnitName(u), guidText
			}
		}
		now := env.now()
		for _, u := range units {
			if u.Type != "creature" || u.Dead {
				continue
			}
			if u.Entry <= 0 || (len(o.entries) > 0 && !patrolRecordHasEntry(o.entries, uint32(u.Entry))) {
				continue
			}
			key := u.GUID.Key()
			if !tracked[key] {
				if patrolRecordDist2DSq(u.X, u.Y, o.x, o.y) > float64(o.r)*float64(o.r) {
					continue
				}
				tracked[key] = true
			}
			guidText[world.GUID(key).String()] = string(u.GUID)
			samples = append(samples, patrols.Sample{
				T: now, GUID: world.GUID(key), Entry: uint32(u.Entry), Name: u.Name,
				X: u.X, Y: u.Y, Z: u.Z, Moving: u.Moving,
			})
		}
		if i+1 < total {
			if err := env.sleep(ctx, time.Duration(o.sampleMS)*time.Millisecond); err != nil {
				return samples, "interrupted", guidText
			}
		}
	}
	return samples, "", guidText
}

// patrolForeverUnitName is the unit's name for a stop reason, its entry when
// no name is known.
func patrolForeverUnitName(u serveclient.Unit) string {
	if u.Name != "" {
		return u.Name
	}
	return fmt.Sprintf("entry %d", u.Entry)
}

// patrolRecordUseGUIDText rewrites the file's guid strings from the analysis
// keys (16-hex world.GUID text) to the serve units' full 32-hex guid text, so
// a Forever recording names real guids in its side columns.
func patrolRecordUseGUIDText(file *patrols.File, text map[string]string) {
	swap := func(s string) string {
		if t, ok := text[s]; ok {
			return t
		}
		return s
	}
	for i := range file.Static {
		file.Static[i].GUID = swap(file.Static[i].GUID)
	}
	for i := range file.Wanderers {
		file.Wanderers[i].GUID = swap(file.Wanderers[i].GUID)
	}
	for i := range file.Patrols {
		p := &file.Patrols[i]
		for j, g := range p.GUIDs {
			p.GUIDs[j] = swap(g)
		}
		p.ID = swap(p.ID)
	}
}
