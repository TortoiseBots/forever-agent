package driver

// The deathrecap tests: the parser reads the core's text built from PR #380's
// own format strings (GetLastDeathLines in DeathRecap.cpp), never a capture,
// and the hook asks once per death, attaches the timeline, and stops asking
// when the server has no command or no recording. Small: the format, the two
// stops, a hostile reply.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// deathRecapRef builds the reference reply from PR #380's format strings: the
// header, one frame line per sampled frame, one cast line per cast before the
// death.
func deathRecapRef(t *testing.T) []string { return deathRecapFor(t, "7", "Watchheal") }

// deathRecapFor is the reference reply for recap id and victim.
func deathRecapFor(t *testing.T, id, victim string) []string {
	t.Helper()
	return []string{
		"Recap #" + id + " of " + victim + ": map 389 instance 1, 3 frames, 2 events, offsets in seconds from the death.",
		"-9.0s  pos 100.0 200.0 50.0  hp 800/1000 (80%)",
		"-4.5s  pos 101.5 200.2 50.0  hp 400/1000 (40%)",
		"-0.1s  pos 102.0 200.0 50.0  hp 0/1000 (0%)",
		"-6.2s  Gor the Troll casts Fireball on " + victim,
		"-2.1s  Gor the Troll casts Shadow Bolt on " + victim,
	}
}

func TestParseDeathRecapRef(t *testing.T) {
	lines, err := ParseDeathRecap(deathRecapRef(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lines) != 6 {
		t.Fatalf("%d lines, want the header, 3 frames and 2 casts: %+v", len(lines), lines)
	}
	if lines[0].Kind != DeathRecapHeader || !strings.HasPrefix(lines[0].Header, "Recap #7 of Watchheal:") {
		t.Fatalf("header %+v", lines[0])
	}
	f := lines[2]
	if f.Kind != DeathRecapFrame || f.OffsetS != -4.5 || f.X != 101.5 || f.Y != 200.2 || f.Z != 50 || f.HP != 400 || f.MaxHP != 1000 || f.HPPct != 40 {
		t.Fatalf("frame %+v", f)
	}
	c := lines[4]
	if c.Kind != DeathRecapCast || c.OffsetS != -6.2 || c.Caster != "Gor the Troll" || c.Spell != "Fireball" || c.Target != "Watchheal" {
		t.Fatalf("cast %+v", c)
	}
	if got := lines[1].String(); got != "-9.0s  pos 100.0 200.0 50.0  hp 800/1000 (80%)" {
		t.Fatalf("frame text %q", got)
	}
}

func TestParseDeathRecapNoCasts(t *testing.T) {
	lines, err := ParseDeathRecap([]string{
		"Recap #1 of Tank: map 389 instance 1, 1 frames, 0 events, offsets in seconds from the death.",
		"  no casts recorded",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lines) != 2 || lines[1].Kind != DeathRecapNoCasts || lines[1].String() != "no casts recorded" {
		t.Fatalf("no-casts %+v", lines)
	}
}

func TestParseDeathRecapNoRecording(t *testing.T) {
	if _, err := ParseDeathRecap([]string{"No death recording for Watchheal."}); err != ErrDeathRecapNone {
		t.Fatalf("no recording: %v", err)
	}
}

func TestParseDeathRecapGarbled(t *testing.T) {
	for _, tc := range [][]string{
		{},
		{"something else entirely"},
		{"No player with that name."},
		{"Only players record deaths."},
		{"a No death recording for Tank. in the middle", "x"},
	} {
		if _, err := ParseDeathRecap(tc); err == nil {
			t.Fatalf("%q parsed", tc)
		}
	}
	many := make([]string, MaxDeathRecapLines+1)
	many[0] = "Recap #1 of Tank: map 389 instance 1, 1 frames, 0 events, offsets in seconds from the death."
	for i := 1; i < len(many); i++ {
		many[i] = "  no casts recorded"
	}
	if _, err := ParseDeathRecap(many); err == nil {
		t.Fatalf("%d lines parsed, want the bound", len(many))
	}
}

// deathWatch starts a watch over the one-group plan with a scripted module.
func deathWatch(t *testing.T) (*Run, *Fake) {
	t.Helper()
	cfg := watchConfig()
	cfg.Poll = 2 * time.Second
	run, f, _ := newWatch(t, cfg)
	return run, f
}

func TestWatchDeathRecapAttaches(t *testing.T) {
	run, f := deathWatch(t)
	f.DeathRecapLines = deathRecapFor(t, "7", "Tank")
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Unit: 0x100}},
	})
	res := run.Watch(context.Background())
	w := res.Summary.Watch
	if w == nil || len(w.DeathLog) != 1 {
		t.Fatalf("death log %+v", w)
	}
	d := w.DeathLog[0]
	if len(d.Recap) == 0 || !strings.HasPrefix(d.Recap[0], "Recap #7 of Tank:") {
		t.Fatalf("recap %+v", d.Recap)
	}
	if len(d.Recap) != 6 {
		t.Fatalf("%d recap lines, want the parsed timeline", len(d.Recap))
	}
	// One request per death, even with later events.
	var asks int
	for _, a := range f.Acted() {
		if a.Kind == ActionDeathRecap {
			asks++
			if a.Who != "Tank" {
				t.Fatalf("asked for %q, want the dead bot's name", a.Who)
			}
		}
	}
	if asks != 1 {
		t.Fatalf("%d deathrecap actions, want one", asks)
	}
}

func TestWatchDeathRecapNoRecordingAsksEachDeath(t *testing.T) {
	run, f := deathWatch(t)
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}, {After: 12 * time.Second, Who: "Watchheal"}},
	})
	res := run.Watch(context.Background())
	var asks int
	for _, a := range f.Acted() {
		if a.Kind == ActionDeathRecap {
			asks++
		}
	}
	if asks != 2 {
		t.Fatalf("%d deathrecap actions, want one per death (none recorded)", asks)
	}
	for i, d := range res.Summary.Watch.DeathLog {
		if len(d.Recap) != 1 || d.Recap[0] != "no recording" {
			t.Fatalf("death %d recap %+v, want the no-recording note", i, d.Recap)
		}
	}
}

func TestWatchDeathRecapMissingCommandStops(t *testing.T) {
	run, f := deathWatch(t)
	f.DeathRecapMissing = true
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}, {After: 12 * time.Second, Who: "Watchheal"}},
	})
	run.Watch(context.Background())
	var asks int
	for _, a := range f.Acted() {
		if a.Kind == ActionDeathRecap {
			asks++
		}
	}
	if asks != 1 {
		t.Fatalf("%d deathrecap actions after the unknown-command stop, want one", asks)
	}
}

func TestParseDeathRecapSkipsForeignLines(t *testing.T) {
	ref := deathRecapRef(t)
	reply := append([]string{"[Server] Restart in 10 minutes."}, ref[:3]...)
	reply = append(reply, "Some other GM output", ref[3], ref[4], ref[5], "trailing announcement")
	lines, err := ParseDeathRecap(reply)
	if err != nil {
		t.Fatalf("a foreign system line dropped the recap: %v", err)
	}
	if len(lines) != 6 || lines[0].Victim != "Watchheal" || lines[0].ID != "7" {
		t.Fatalf("lines %+v, want the six recap lines with the header's id and victim", lines)
	}
}

func TestParseDeathRecapNoRecordingIsTheExactFirstLine(t *testing.T) {
	// A prefix inside another line, or after the header, is no "no recording".
	for _, reply := range [][]string{
		{"Recap #1 of Tank: map 1 instance 1, 0 frames, 0 events, x.", "No death recording for Tank."},
		{"No death recording for Tank. And more text"},
		{"Announcement: No death recording for Tank."},
	} {
		if _, err := ParseDeathRecap(reply); err == ErrDeathRecapNone {
			t.Fatalf("%q read as no recording", reply)
		}
	}
	if _, err := ParseDeathRecap([]string{"", "  No death recording for Tank.  "}); err != ErrDeathRecapNone {
		t.Fatalf("the exact line: %v", err)
	}
}

func TestParseDeathRecapCastsAreUnambiguous(t *testing.T) {
	head := "Recap #1 of Tank: map 389 instance 1, 0 frames, 3 events, offsets in seconds from the death."
	lines, err := ParseDeathRecap([]string{
		head,
		"-3.0s  Lord Casts casts Frost Nova on Tank",
		"-2.0s  Gor casts Strike on Weakness on Tank",
		"-1.0s  Gor casts  on Tank",
	})
	if err != nil || len(lines) != 4 {
		t.Fatalf("parse %v %+v", err, lines)
	}
	for i, want := range [][3]string{
		{"Lord Casts", "Frost Nova", "Tank"},
		{"Gor", "Strike on Weakness", "Tank"},
		{"Gor", "", "Tank"},
	} {
		c := lines[i+1]
		if c.Kind != DeathRecapCast || c.Caster != want[0] || c.Spell != want[1] || c.Target != want[2] {
			t.Fatalf("cast %d %+v, want %v", i, c, want)
		}
	}
}

func TestParseDeathRecapSignedOffsets(t *testing.T) {
	lines, err := ParseDeathRecap([]string{
		"Recap #1 of Tank: map 389 instance 1, 2 frames, 0 events, offsets in seconds from the death.",
		"+0.0s  pos 1.0 -2.0 3.0  hp 0/10 (0%)",
		"-12.3s  pos -1.0 2.0 -3.0  hp 10/10 (100%)",
	})
	if err != nil || len(lines) != 3 || lines[1].OffsetS != 0 || lines[2].OffsetS != -12.3 || lines[2].X != -1 || lines[2].Z != -3 {
		t.Fatalf("parse %v %+v", err, lines)
	}
}
