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
	mu    sync.Mutex
	st    serveclient.Status
	units []serveclient.Unit
	gm    []string
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

func (f *fakePatrolServe) Units(context.Context, serveclient.UnitsParams) ([]serveclient.Unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]serveclient.Unit(nil), f.units...), nil
}

func (f *fakePatrolServe) Party(context.Context) (serveclient.Party, error) {
	return serveclient.Party{}, nil
}

func (f *fakePatrolServe) GM(_ context.Context, text string) (serveclient.Command, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gm = append(f.gm, text)
	if rest, ok := strings.CutPrefix(text, ".go xyz "); ok {
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
