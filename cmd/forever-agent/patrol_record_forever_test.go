package main

// patrol-record --forever: the 128-bit target rule (a mob whose target is
// the agent's guid stops the run, and a guid that only shares its low 64 bits
// does not), the unchanged stance allow-list, and the file's 32-hex guid side
// columns.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/patrols"
)

// fakePatrolServe answers a serve slot for patrol-record: status in map 700,
// fixed units, party solo. A `.go xyz` moves the status.
type fakePatrolServe struct {
	mu        sync.Mutex
	st        serveclient.Status
	units     []serveclient.Unit
	gm        []string
	gmCtxErr  []error
	refuse    map[string]string
	noMove    bool
	unitReads []serveclient.UnitsParams
}

func (f *fakePatrolServe) Status(context.Context) (serveclient.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, nil
}

func (f *fakePatrolServe) Unit(_ context.Context, guid, name string) (serveclient.Unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.units {
		if string(u.GUID) == guid || (name != "" && strings.EqualFold(u.Name, name)) {
			return u, nil
		}
	}
	return serveclient.Unit{}, &serveclient.Error{Method: "unit", Code: -32003, Message: "no such unit"}
}

func (f *fakePatrolServe) Units(_ context.Context, p serveclient.UnitsParams) ([]serveclient.Unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unitReads = append(f.unitReads, p)
	return append([]serveclient.Unit(nil), f.units...), nil
}

func (f *fakePatrolServe) Party(context.Context) (serveclient.Party, error) {
	return serveclient.Party{}, nil
}

func (f *fakePatrolServe) GM(ctx context.Context, text string) (serveclient.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, text)
	f.gmCtxErr = append(f.gmCtxErr, ctx.Err())
	if msg := f.refuse[text]; msg != "" {
		return serveclient.Command{Sent: text, Lines: []string{msg}, Complete: true}, nil
	}
	if rest, ok := strings.CutPrefix(text, ".go xyz "); ok && !f.noMove {
		var x, y, z float32
		var m uint32
		if _, err := fmt.Sscan(rest, &x, &y, &z, &m); err == nil {
			f.st.X, f.st.Y, f.st.Z, f.st.Map = x, y, z, m
		}
	}
	return serveclient.Command{Sent: text, Complete: true}, nil
}

// patrolForeverEnv is the injected environment: no sleeps, the fake slot, and
// a --z that skips the navmesh.
func patrolForeverEnv(f *fakePatrolServe) patrolRecordEnv {
	env := patrolRecordEnvDefault()
	env.now = time.Now
	env.sleep = func(context.Context, time.Duration) error { return nil }
	env.openForever = func(string) (patrolForeverClient, error) { return f, nil }
	return env
}

// patrolForeverRun runs the command against the fake and returns its output.
func patrolForeverRun(t *testing.T, f *fakePatrolServe, extra ...string) (code int, out, errOut string, path string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "patrols.json")
	args := append([]string{"slot", "--forever", "--map", "700", "--area", "0,0,30", "--z", "0",
		"--minutes", "1", "--sample-ms", "500", "--out", path}, extra...)
	var o, e bytes.Buffer
	code = patrolRecordCmdEnv(context.Background(), args, &o, &e, patrolForeverEnv(f))
	return code, o.String(), e.String(), path
}

func TestPatrolRecordForeverStopsWhenTargeted(t *testing.T) {
	own := "0x0000000000000001F1300000000000AA"
	// The decoy shares the agent's low 64 bits only: a uint64 compare would
	// stop here, the full 128-bit compare must not.
	decoy := serveclient.Unit{GUID: "0x0000000000000002F1300000000000BA", Type: "creature",
		Name: "Wanderer", Entry: 100, X: 0, Y: 0, Target: "0x0000000000000000F1300000000000AA"}
	solo := &fakePatrolServe{
		st:    serveclient.Status{Character: "Testchar", GUID: serveclient.GUID(own), State: "online", Map: 700},
		units: []serveclient.Unit{{GUID: serveclient.GUID(own), Type: "player", Name: "Testchar"}, decoy},
	}
	code, out, errOut, path := patrolForeverRun(t, solo)
	if code != exitOK {
		t.Fatalf("a low-64-only match stopped the run: code %d out %q err %q", code, out, errOut)
	}
	f, err := patrols.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Static) != 1 {
		t.Fatalf("static rows %d, want 1 (%+v)", len(f.Static), f.Static)
	}
	if f.Static[0].GUID != string(decoy.GUID) {
		t.Fatalf("static guid %q, want the 32-hex text %q (the key's 16-hex form was kept)", f.Static[0].GUID, decoy.GUID)
	}
	// The units read asks for far more than serve's default 100, so a hostile unit is not cut off the list.
	solo.mu.Lock()
	if len(solo.unitReads) == 0 || solo.unitReads[0].Limit != patrolForeverUnitsLimit {
		t.Fatalf("units params %+v, want the explicit limit", solo.unitReads)
	}
	solo.mu.Unlock()

	// The real thing: the mob targets the agent's full 128-bit guid.
	ambusher := serveclient.Unit{GUID: "0x0000000000000003F1300000000000C1", Type: "creature",
		Name: "Ambusher", Entry: 200, X: 1, Y: 1, Target: serveclient.GUID(own)}
	real := &fakePatrolServe{
		st:    serveclient.Status{Character: "Testchar", GUID: serveclient.GUID(own), State: "online", Map: 700},
		units: []serveclient.Unit{{GUID: serveclient.GUID(own), Type: "player", Name: "Testchar"}, ambusher},
	}
	code, out, errOut, path = patrolForeverRun(t, real)
	if code != exitPatrolRecordStopped {
		t.Fatalf("a mob targeting the agent: code %d, want %d (out %q err %q)", code, exitPatrolRecordStopped, out, errOut)
	}
	if !strings.Contains(errOut, "targeted by Ambusher") {
		t.Fatalf("no targeted stop: %q", errOut)
	}
	f, err = patrols.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.Stopped, "targeted by Ambusher") {
		t.Fatalf("file stopped %q", f.Stopped)
	}

	// The stance lines are still the allow-list's, in order.
	want := []string{".gm on", ".gm visible off", ".go xyz 0 0 0 700", ".gm visible on"}
	got := append([]string(nil), real.gm...)
	if len(got) != len(want) {
		t.Fatalf("GM lines %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GM line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPatrolRecordForeverRefusesADeadSlot pins the world gate through
// serveclient: a slot still reconnecting is refused (15), not recorded.
func TestPatrolRecordForeverRefusesADeadSlot(t *testing.T) {
	f := &fakePatrolServe{
		st: serveclient.Status{Character: "Testchar", GUID: "0x0000000000000001F1300000000000AA", State: "reconnecting", Map: 700},
	}
	code, _, errOut, path := patrolForeverRun(t, f)
	if code != exitSlotRefused {
		t.Fatalf("code %d, want %d (err %q)", code, exitSlotRefused, errOut)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused recording wrote a file")
	}
}

// patrolForeverRunEnv is patrolForeverRun with a caller's env and context (to cancel it mid-loop).
func patrolForeverRunEnv(t *testing.T, ctx context.Context, env patrolRecordEnv, extra ...string) (code int, out, errOut string, path string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "patrols.json")
	args := append([]string{"slot", "--forever", "--map", "700", "--area", "0,0,30", "--z", "0",
		"--minutes", "1", "--sample-ms", "500", "--out", path}, extra...)
	var o, e bytes.Buffer
	code = patrolRecordCmdEnv(ctx, args, &o, &e, env)
	return code, o.String(), e.String(), path
}

// TestPatrolRecordForeverRestoresVisibilityAfterInterrupt is the Ctrl-C case: the loop's ctx is cancelled, but the
// `.gm visible on` restore still runs, on a fresh context, so the GM character is not left invisible.
func TestPatrolRecordForeverRestoresVisibilityAfterInterrupt(t *testing.T) {
	own := "0x0000000000000001F1300000000000AA"
	f := &fakePatrolServe{
		st:    serveclient.Status{Character: "Testchar", GUID: serveclient.GUID(own), State: "online", Map: 700},
		units: []serveclient.Unit{{GUID: serveclient.GUID(own), Type: "player", Name: "Testchar"}},
	}
	env := patrolForeverEnv(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := false
	env.sleep = func(c context.Context, d time.Duration) error {
		if !cancelled {
			cancelled = true
			cancel()
			return c.Err()
		}
		return c.Err()
	}
	code, _, errOut, path := patrolForeverRunEnv(t, ctx, env)
	if code != exitPatrolRecordStopped {
		t.Fatalf("interrupted recording: code %d err %q", code, errOut)
	}
	if !strings.Contains(errOut, "interrupted") {
		t.Fatalf("the stop reason is not the interrupt: %q", errOut)
	}
	f.mu.Lock()
	got, ctxErrs := append([]string(nil), f.gm...), append([]error(nil), f.gmCtxErr...)
	f.mu.Unlock()
	if len(got) != 4 || got[3] != ".gm visible on" {
		t.Fatalf("GM lines %q, want the restore last", got)
	}
	if ctxErrs[3] != nil {
		t.Fatalf("the restore used the cancelled context: %v", ctxErrs[3])
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no file written: %v", err)
	}
}

// TestPatrolRecordForeverRestoreRefusalIsAnError: a refusal line in the `.gm visible on` reply is reported, not
// treated as a success.
func TestPatrolRecordForeverRestoreRefusalIsAnError(t *testing.T) {
	own := "0x0000000000000001F1300000000000AA"
	f := &fakePatrolServe{
		st:     serveclient.Status{Character: "Testchar", GUID: serveclient.GUID(own), State: "online", Map: 700},
		units:  []serveclient.Unit{{GUID: serveclient.GUID(own), Type: "player", Name: "Testchar"}},
		refuse: map[string]string{".gm visible on": "Incorrect syntax near '.'"},
	}
	code, _, errOut, _ := patrolForeverRun(t, f)
	if code != exitOK {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if !strings.Contains(errOut, "the server refused") || !strings.Contains(errOut, "Incorrect syntax") {
		t.Fatalf("the refusal was not reported: %q", errOut)
	}
}

// TestPatrolRecordForeverWaitsForTheLanding: `.go xyz` acking is not the teleport: the wait needs the status on
// the map near the area centre, so a refused or ignored teleport is a refusal, not a recording of elsewhere.
func TestPatrolRecordForeverWaitsForTheLanding(t *testing.T) {
	own := "0x0000000000000001F1300000000000AA"
	f := &fakePatrolServe{
		st:     serveclient.Status{Character: "Testchar", GUID: serveclient.GUID(own), State: "online", Map: 700, X: 900, Y: 900},
		units:  []serveclient.Unit{{GUID: serveclient.GUID(own), Type: "player", Name: "Testchar"}},
		noMove: true,
	}
	env := patrolForeverEnv(f)
	// A clock that advances on every sleep, so the bounded wait ends without real time.
	now := time.Now()
	env.now = func() time.Time { return now }
	env.sleep = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	code, _, errOut, path := patrolForeverRunEnv(t, context.Background(), env)
	if code != exitSlotRefused {
		t.Fatalf("an unlanded teleport: code %d, want %d (%q)", code, exitSlotRefused, errOut)
	}
	if !strings.Contains(errOut, "did not reach map 700") {
		t.Fatalf("the refusal does not name the landing: %q", errOut)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused landing wrote a recording")
	}
}
