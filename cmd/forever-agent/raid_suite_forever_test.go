package main

// raid-suite --forever (SPEC #108 R4b): each entry names its leader, its Forever server and its slot; the slot is
// resolved and leased, the slot dir is built with `slot setup --forever`, the watch runs in this process and the lease
// is released however the watch ends. Only the two side effects are replaced here (the setup and the watch).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
)

// raidForeverSlots writes a slots file with one Forever server and slot 2 (mode 0600 in a 0700 dir) and returns its
// path. Every value is a placeholder: no host, account or character name is a real one.
func raidForeverSlots(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,
	 "servers":[{"name":"fv1","realmd":"example.invalid:9000","slots":[
	   {"id":"2","account":"TAGENTX","character":"Slotchar","port":18799,"settings_from":"` + settings + `"}]}],
	 "forever":{"fv1":{"rest":"http://example.invalid:9100","rpc":"http://example.invalid:9101","ca":"` +
		filepath.Join(dir, "ca.pem") + `","build":"0"}}}`
	path := filepath.Join(dir, "slots.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// raidForeverRig replaces the slot setup and the watch with fakes and records what each got. script answers the exit
// code of a watch and writes the summary it wants; watch, when set, replaces it and sees the watch's context.
type raidForeverRig struct {
	setups []string // the out dir of every slot setup
	args   [][]string
	script func(args []string, out string) int
	watch  func(ctx context.Context, args []string, out string) int
}

func newRaidForeverRig(t *testing.T) *raidForeverRig {
	t.Helper()
	r := &raidForeverRig{}
	prevSetup, prevWatch := raidSuiteSlotSetup, raidSuiteForeverWatch
	t.Cleanup(func() { raidSuiteSlotSetup, raidSuiteForeverWatch = prevSetup, prevWatch })
	raidSuiteSlotSetup = func(w *autoForeverWorld, binary string, f autoForeverSlot, out string) error {
		if f.rest == "" || f.rpc == "" || f.ca == "" {
			t.Errorf("the slot %s/%s resolved without its connection flags: %+v", f.server, f.slot, f)
		}
		r.setups = append(r.setups, out)
		return nil
	}
	raidSuiteForeverWatch = func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
		r.args = append(r.args, args)
		out := raidForeverFlag(args, "--out")
		if r.watch != nil {
			return r.watch(ctx, args, out)
		}
		if r.script == nil {
			writeSuiteSummary(t, out, suiteSummary(raidSuiteForeverUntil, time.Now(), 300))
			return exitOK
		}
		return r.script(args, out)
	}
	return r
}

// raidForeverFlag is one flag's value in a watch's arguments ("" when absent).
func raidForeverFlag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	return ""
}

// raidForeverRun runs raid-suite over one entry and returns the exit code and the report.
func raidForeverRun(t *testing.T, slotsPath string, entries ...raidSuiteEntry) (int, raidSuiteReport, string) {
	t.Helper()
	suite := writeSuiteFile(t, entries...)
	base := t.TempDir()
	out := filepath.Join(base, "suite-out")
	var stdout, stderr bytes.Buffer
	code := raidSuiteCmd(context.Background(), []string{
		"--forever", "--suite", suite, "--history", filepath.Join(base, "history"), "--out", out,
		"--slots", slotsPath, "--json",
	}, &stdout, &stderr)
	var rep raidSuiteReport
	if raw, err := os.ReadFile(filepath.Join(out, "suite.json")); err == nil {
		if err := json.Unmarshal(raw, &rep); err != nil {
			t.Fatalf("suite.json: %v", err)
		}
	}
	return code, rep, stderr.String()
}

// raidForeverEntry is one Forever suite entry for onyxias_lair (the tier table names it, so the preparation passes
// with no roster and no sql).
func raidForeverEntry() raidSuiteEntry {
	return raidSuiteEntry{Plan: "onyxias_lair", Dir: suiteDir, Leader: "Tankbot", Server: "fv1", Slot: "2"}
}

// TestRaidSuiteForeverRefusesSQL: a Forever entry places nothing, so its sql and hires are refused before any lease.
func TestRaidSuiteForeverRefusesSQL(t *testing.T) {
	newRaidForeverRig(t)
	slotsPath := raidForeverSlots(t)
	for _, c := range []struct {
		name   string
		mut    func(*raidSuiteEntry)
		reject string
	}{
		{"sql", func(e *raidSuiteEntry) { e.SQL = filepath.Join(t.TempDir(), "world.sql") }, "refuses sql and hires"},
		{"hires", func(e *raidSuiteEntry) { e.Hires = filepath.Join(t.TempDir(), "roster.json") }, "refuses sql and hires"},
		{"no leader", func(e *raidSuiteEntry) { e.Leader = "" }, "needs leader, server and slot"},
		{"no server", func(e *raidSuiteEntry) { e.Server = "" }, "needs leader, server and slot"},
		{"no slot", func(e *raidSuiteEntry) { e.Slot = "" }, "needs leader, server and slot"},
		{"slot 3", func(e *raidSuiteEntry) { e.Slot = "3" }, "slot 3 is never used"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := raidForeverEntry()
			c.mut(&e)
			code, _, stderr := raidForeverRun(t, slotsPath, e)
			if code != exitUsage {
				t.Fatalf("exit %d; want a usage error (stderr %q)", code, stderr)
			}
			if !strings.Contains(stderr, c.reject) {
				t.Fatalf("stderr = %q; want %q", stderr, c.reject)
			}
		})
	}
	// --dir names no leader, server or slot per raid: a --forever suite is a suite file's.
	var stdout, stderr bytes.Buffer
	if code := raidSuiteCmd(context.Background(), []string{"--forever", "--dir", suiteDir, "--history", t.TempDir(),
		"--out", filepath.Join(t.TempDir(), "out"), "--slots", slotsPath}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("--forever --dir: exit %d; want a usage error", code)
	} else if !strings.Contains(stderr.String(), "walks a suite file's raids") {
		t.Fatalf("stderr = %q; want the suite-file line", stderr.String())
	}
}

// TestRaidSuiteForeverArgs: the entry's plan, leader, server and slot reach the watch as autoForeverCommandArgs
// builds them: the plan file, the leader, the slots file's connection flags and account, the slot dir and the attempt's
// folder, and the slot character when the entry names none. No --until, --from or --place: a watch has no resume.
func TestRaidSuiteForeverArgs(t *testing.T) {
	rig := newRaidForeverRig(t)
	slotsPath := raidForeverSlots(t)
	code, rep, stderr := raidForeverRun(t, slotsPath, raidForeverEntry())
	if code != exitOK {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if len(rig.args) != 1 {
		t.Fatalf("watch calls = %d; want 1", len(rig.args))
	}
	args := rig.args[0]
	want := []string{
		"--plan", filepath.Join(suiteDir, "onyxias_lair.json"),
		"--leader", "Tankbot",
		"--rest", "http://example.invalid:9100",
		"--rpc", "http://example.invalid:9101",
	}
	got := strings.Join(args, " ")
	if !strings.HasPrefix(got, strings.Join(want, " ")+" ") {
		t.Fatalf("args = %q; want them to start with %q", got, strings.Join(want, " "))
	}
	if !strings.HasSuffix(raidForeverFlag(args, "--serve-dir"), "/slot") {
		t.Fatalf("--serve-dir = %q; want the attempt's slot dir", raidForeverFlag(args, "--serve-dir"))
	}
	if raidForeverFlag(args, "--account") != "TAGENTX" || raidForeverFlag(args, "--character") != "Slotchar" {
		t.Fatalf("account/character = %q/%q; want the slot's", raidForeverFlag(args, "--account"), raidForeverFlag(args, "--character"))
	}
	if out := raidForeverFlag(args, "--out"); out == "" || rep.Raids[0].Out != out || rep.Raids[0].Out != rig.setups[0] {
		t.Fatalf("--out = %q, report out %q, setup dir %q; want one attempt's folder", out, rep.Raids[0].Out, rig.setups[0])
	}
	for _, banned := range []string{"--until", "--from", "--place", "--reuse-instance", "--build"} {
		if slices.Contains(args, banned) {
			t.Fatalf("args carry %s: %q", banned, got)
		}
	}
	if rep.Raids[0].Until != raidSuiteForeverUntil {
		t.Fatalf("until = %q; want %q (the module's own run decides it)", rep.Raids[0].Until, raidSuiteForeverUntil)
	}
	if rep.Raids[0].Verdict != suiteNew {
		t.Fatalf("verdict = %q (%s); want a first walk to be new", rep.Raids[0].Verdict, rep.Raids[0].Reason)
	}
}

// TestRaidSuiteForeverLeaseReleased: the entry's lease is released after a good watch and after a failed one.
func TestRaidSuiteForeverLeaseReleased(t *testing.T) {
	for _, c := range []struct {
		name   string
		script func(args []string, out string) int
	}{
		{"a good watch", func(args []string, out string) int {
			writeSuiteSummary(t, out, suiteSummary(raidSuiteForeverUntil, time.Now(), 300))
			return exitOK
		}},
		{"a failed watch", func(args []string, out string) int { return exitRaidWalkFailed }},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newRaidForeverRig(t)
			rig.script = c.script
			slotsPath := raidForeverSlots(t)
			raidForeverRun(t, slotsPath, raidForeverEntry())
			if len(rig.args) != 1 {
				t.Fatalf("watch calls = %d; want 1", len(rig.args))
			}
			cfg, err := slots.Load(slotsPath)
			if err != nil {
				t.Fatal(err)
			}
			store := slots.NewStore(cfg, slotsPath)
			lease, err := store.Get("fv1", "2")
			if err != nil {
				t.Fatal(err)
			}
			if lease != nil {
				t.Fatalf("the lease is still held by %s after the watch", lease.Holder)
			}
			// The slot is free for the next attempt.
			if _, err := store.Acquire("fv1", "2", false, "raid-suite-next", autoForeverLeaseTTL); err != nil {
				t.Fatalf("the slot is not free after the watch: %v", err)
			}
		})
	}
}

// TestRaidSuiteForeverRefusedSlot: a slot the slots file does not hold is a refused raid, not a failed one, and nothing
// is set up or watched.
func TestRaidSuiteForeverRefusedSlot(t *testing.T) {
	rig := newRaidForeverRig(t)
	e := raidForeverEntry()
	e.Slot = "9"
	code, rep, stderr := raidForeverRun(t, raidForeverSlots(t), e)
	if code != exitOK {
		t.Fatalf("exit %d stderr %q; a refused raid is no failure", code, stderr)
	}
	if len(rep.Raids) != 1 || rep.Raids[0].Verdict != suiteRefused {
		t.Fatalf("report = %+v; want one refused raid", rep.Raids)
	}
	if len(rig.args) != 0 || len(rig.setups) != 0 {
		t.Fatalf("setup/watch ran for a refused slot: %v/%v", rig.setups, rig.args)
	}
}

// raidForeverLeaseRenew shortens the suite's renewal interval for one test.
func raidForeverLeaseRenew(t *testing.T, d time.Duration) {
	t.Helper()
	prev := raidSuiteLeaseRenew
	t.Cleanup(func() { raidSuiteLeaseRenew = prev })
	raidSuiteLeaseRenew = d
}

// raidForeverStore is a store over the same slots file, for reading the lease the suite holds.
func raidForeverStore(t *testing.T, slotsPath string) *slots.Store {
	t.Helper()
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	return slots.NewStore(cfg, slotsPath)
}

// TestRaidSuiteForeverLeaseRenewed: a watch that outlives the renewal interval has its lease renewed while it runs, so
// a two-hour watch never leaves its slot free after thirty minutes.
func TestRaidSuiteForeverLeaseRenewed(t *testing.T) {
	raidForeverLeaseRenew(t, 15*time.Millisecond)
	rig := newRaidForeverRig(t)
	slotsPath := raidForeverSlots(t)
	store := raidForeverStore(t, slotsPath)
	var renewed bool
	rig.watch = func(ctx context.Context, args []string, out string) int {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if l, err := store.Get("fv1", "2"); err == nil && l != nil && l.Renewed.After(l.Acquired) {
				renewed = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		writeSuiteSummary(t, out, suiteSummary(raidSuiteForeverUntil, time.Now(), 300))
		return exitOK
	}
	code, _, stderr := raidForeverRun(t, slotsPath, raidForeverEntry())
	if code != exitOK {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if !renewed {
		t.Fatal("the lease was never renewed while the watch ran")
	}
	if l, err := store.Get("fv1", "2"); err != nil || l != nil {
		t.Fatalf("the lease after the watch: %+v, %v; want released", l, err)
	}
}

// TestRaidSuiteForeverRenewalLostCancelsTheWatch: a lease that goes to another holder ends the watch — it may not keep
// a slot it no longer holds — the raid is failed with the renewal's error, and the other holder's lease is left alone.
func TestRaidSuiteForeverRenewalLostCancelsTheWatch(t *testing.T) {
	raidForeverLeaseRenew(t, 15*time.Millisecond)
	rig := newRaidForeverRig(t)
	slotsPath := raidForeverSlots(t)
	store := raidForeverStore(t, slotsPath)
	rig.watch = func(ctx context.Context, args []string, out string) int {
		go func() {
			// Another run takes the slot after the watch started: release ours, then acquire as a stranger.
			time.Sleep(10 * time.Millisecond)
			if l, err := store.Get("fv1", "2"); err == nil && l != nil {
				_, _ = store.Release("fv1", "2", l.Holder, l.LeaseID, false)
				_, _ = store.Acquire("fv1", "2", false, "raid-suite-other", autoForeverLeaseTTL)
			}
		}()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		return exitRaidWalkFailed
	}
	code, rep, stderr := raidForeverRun(t, slotsPath, raidForeverEntry())
	if code == exitOK {
		t.Fatalf("exit %d; a lost lease is a failure (stderr %q)", code, stderr)
	}
	if len(rep.Raids) != 1 || rep.Raids[0].Verdict != suiteFailed {
		t.Fatalf("report = %+v; want one failed raid (stderr %q)", rep.Raids, stderr)
	}
	if !strings.Contains(rep.Raids[0].Reason, "renew") {
		t.Fatalf("reason = %q; want the renewal's error", rep.Raids[0].Reason)
	}
	// The report says the raid failed: its exit code is not the watch's 0 (a watch that ran to a cancellation).
	if rep.Raids[0].ExitCode == nil || *rep.Raids[0].ExitCode == 0 {
		t.Fatalf("exit code = %v; want a non-zero code on the lease-loss path", rep.Raids[0].ExitCode)
	}
	l, err := store.Get("fv1", "2")
	if err != nil {
		t.Fatal(err)
	}
	if l == nil || l.Holder != "raid-suite-other" {
		t.Fatalf("the lease after the run = %+v; want the other holder's own", l)
	}
}

// TestRaidSuiteForeverTransientRenewFailureKeepsTheWatch: one renewal error that passes is not a lost lease. The watch
// keeps running, the next renewal succeeds, and the raid is judged as the good walk it was.
func TestRaidSuiteForeverTransientRenewFailureKeepsTheWatch(t *testing.T) {
	raidForeverLeaseRenew(t, 15*time.Millisecond)
	rig := newRaidForeverRig(t)
	slotsPath := raidForeverSlots(t)
	prev := raidSuiteRenewHeld
	t.Cleanup(func() { raidSuiteRenewHeld = prev })
	calls := 0
	raidSuiteRenewHeld = func(w *autoForeverWorld, f autoForeverSlot) error {
		calls++
		if calls == 1 {
			return errors.New("the store is busy")
		}
		return w.renewHeld(f)
	}
	rig.watch = func(ctx context.Context, args []string, out string) int {
		time.Sleep(60 * time.Millisecond) // several renewal ticks: the first fails, the rest pass
		if ctx.Err() != nil {
			return exitRaidWalkFailed
		}
		writeSuiteSummary(t, out, suiteSummary(raidSuiteForeverUntil, time.Now(), 300))
		return exitOK
	}
	code, rep, stderr := raidForeverRun(t, slotsPath, raidForeverEntry())
	if code != exitOK {
		t.Fatalf("exit %d stderr %q; want the watch to survive one renewal error", code, stderr)
	}
	if len(rep.Raids) != 1 || rep.Raids[0].Verdict == suiteFailed {
		t.Fatalf("report = %+v; want the walk judged, not failed (stderr %q)", rep.Raids, stderr)
	}
	if calls < 2 {
		t.Fatalf("renewal calls = %d; want the retry after the error", calls)
	}
}

// TestRaidSuiteForeverWatchPanicReleasesTheLease: a panic in the watch's goroutine is recovered, the lease is
// released on the way out, and the panic is raised again rather than hidden.
func TestRaidSuiteForeverWatchPanicReleasesTheLease(t *testing.T) {
	rig := newRaidForeverRig(t)
	slotsPath := raidForeverSlots(t)
	rig.watch = func(ctx context.Context, args []string, out string) int { panic("the watch exploded") }
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		raidForeverRun(t, slotsPath, raidForeverEntry())
	}()
	if recovered == nil {
		t.Fatal("the watch's panic was swallowed")
	}
	store := raidForeverStore(t, slotsPath)
	if l, err := store.Get("fv1", "2"); err != nil || l != nil {
		t.Fatalf("the lease after the panic = %+v, %v; want released", l, err)
	}
	if _, err := store.Acquire("fv1", "2", false, "raid-suite-next", autoForeverLeaseTTL); err != nil {
		t.Fatalf("the slot is not free after the panic: %v", err)
	}
}
