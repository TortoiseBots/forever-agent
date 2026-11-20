package tbcmd

// The run commands' typed reads: the module's own record shapes, every optional key, the event ring, the refusals
// the decoder owes for a bad value or a bad shape, and the lines the calls build from their typed arguments.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const runStartRecord = "run lead=Tankbot plan=ragefire_chasm state=running step=0 steps=84 leg=0 legname=tunnel " +
	"kind=move phase=begin note=started pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=0 ev=1 #T"

const runPausedRecord = "run lead=Tankbot plan=ragefire_chasm state=paused step=17 steps=84 leg=2 legname=cave " +
	"kind=pull phase=wait note=refused%3A%20revive%20party pull=6 pulls=51 boss=2732:dead;2733:alive " +
	"deaths=2 wipes=1 pauses=1 elapsed=312 why=wipe future_key=a%23b #T"

const runDoneRecord = "run lead=Moff plan=other state=done step=9 steps=9 phase=hold note=done pull=1 pulls=1 " +
	"boss= deaths=0 wipes=0 pauses=0 elapsed=44 ev=3 #T"

func TestRunStartEveryKey(t *testing.T) {
	r := oneReply("run-begin #T", runStartRecord, "run-end count=1 records=1 verb=start #T")
	rep, err := NewClient(r).RunStart(context.Background(), "Tankbot", "ragefire_chasm")
	if err != nil {
		t.Fatalf("RunStart: %v", err)
	}
	if want := ".tb run start Tankbot ragefire_chasm"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if len(rep.Runs) != 1 {
		t.Fatalf("%d run(s)", len(rep.Runs))
	}
	got := rep.Runs[0]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Lead", got.Lead, "Tankbot"},
		{"Plan", got.Plan, "ragefire_chasm"},
		{"State", got.State, "running"},
		{"Step", got.Step, 0},
		{"Steps", got.Steps, 84},
		{"Leg", got.Leg, 0},
		{"HasLeg", got.HasLeg, true},
		{"LegName", got.LegName, "tunnel"},
		{"Kind", got.Kind, "move"},
		{"HasKind", got.HasKind, true},
		{"Phase", got.Phase, "begin"},
		{"Note", got.Note, "started"},
		{"Pull", got.Pull, 0},
		{"Pulls", got.Pulls, 51},
		{"Bosses", len(got.Bosses), 0},
		{"Deaths", got.Deaths, 0},
		{"Wipes", got.Wipes, 0},
		{"Pauses", got.Pauses, 0},
		{"Elapsed", got.Elapsed, 0},
		{"HasWhy", got.HasWhy, false},
		{"Ev", got.Ev, 1},
		{"HasEv", got.HasEv, true},
		{"Verb", rep.Verb, "start"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestRunStatusAllEveryKey(t *testing.T) {
	r := oneReply("run-begin #T", runPausedRecord, runDoneRecord,
		"run-end count=2 records=2 verb=status runs=2 ticks=10 tick_avg_us=5 tick_max_us=9 facts_reads=12 "+
			"facts_avg_us=2 facts_max_us=4 facts_unreadable=0 #T")
	rep, err := NewClient(r).RunStatus(context.Background(), "all")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if want := ".tb run status all"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if len(rep.Runs) != 2 {
		t.Fatalf("%d run(s)", len(rep.Runs))
	}
	p := rep.Runs[0]
	if p.State != "paused" || p.Step != 17 || p.Leg != 2 || p.LegName != "cave" || p.Kind != "pull" {
		t.Fatalf("paused record: %+v", p)
	}
	if !p.HasWhy || p.Why != "wipe" || p.Note != "refused: revive party" || p.Extra["future_key"] != "a#b" {
		t.Fatalf("paused why/note/extra: %+v", p)
	}
	if len(p.Bosses) != 2 || p.Bosses[0] != (RunBoss{Entry: 2732, Alive: false, State: "dead"}) ||
		p.Bosses[1] != (RunBoss{Entry: 2733, Alive: true, State: "alive"}) {
		t.Fatalf("bosses: %+v", p.Bosses)
	}
	d := rep.Runs[1]
	if d.HasLeg || d.HasKind || d.HasWhy {
		t.Fatalf("past the last step the leg/kind/why keys must be absent: %+v", d)
	}
	if rep.End["tick_max_us"] != "9" || rep.End["facts_unreadable"] != "0" {
		t.Fatalf("end line: %+v", rep.End)
	}
}

func TestRunEventsEveryKey(t *testing.T) {
	r := oneReply("run-begin #T",
		"event seq=30 t=309 kind=fight-end step=17 leg=2 why= #T",
		"event seq=31 t=312 kind=stop step=17 leg=2 why=stopped%3A%20by%20.tb%20run%20stop #T",
		"run-end count=2 records=2 verb=events lead=Tankbot last=31 #T")
	rep, err := NewClient(r).RunEvents(context.Background(), "Tankbot", 29, true)
	if err != nil {
		t.Fatalf("RunEvents: %v", err)
	}
	if want := ".tb run events Tankbot since=29"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if rep.Lead != "Tankbot" || !rep.HasLast || rep.Last != 31 || len(rep.Events) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	ev := rep.Events[1]
	if ev.Seq != 31 || ev.T != 312 || ev.Kind != "stop" || !ev.HasStep || ev.Step != 17 || !ev.HasLeg || ev.Leg != 2 {
		t.Fatalf("event: %+v", ev)
	}
	if ev.Why != "stopped: by .tb run stop" || ev.HasUnit {
		t.Fatalf("event why/unit: %+v", ev)
	}
}

func TestRunEventsWithoutSince(t *testing.T) {
	r := oneReply("run-begin #T", "event seq=1 t=0 kind=start why= #T",
		"run-end count=1 records=1 verb=events lead=Tankbot last=1 #T")
	if _, err := NewClient(r).RunEvents(context.Background(), "Tankbot", 0, false); err != nil {
		t.Fatalf("RunEvents: %v", err)
	}
	if want := ".tb run events Tankbot"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
}

func TestRunStopReasonWords(t *testing.T) {
	r := oneReply("run-begin #T", runStartRecord,
		"run-end count=1 records=1 verb=stop pending=no stayed=5 #T")
	rep, err := NewClient(r).RunStop(context.Background(), "Tankbot", "watchdog no new event")
	if err != nil {
		t.Fatalf("RunStop: %v", err)
	}
	if want := ".tb run stop Tankbot watchdog no new event"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if !rep.HasPending || rep.Pending || rep.End["stayed"] != "5" {
		t.Fatalf("stop end: pending=%v/%v end=%v", rep.Pending, rep.HasPending, rep.End)
	}
}

func TestRunPauseAndResumeLines(t *testing.T) {
	r := oneReply("run-begin #T", runStartRecord, "run-end count=1 records=1 verb=pause #T")
	if _, err := NewClient(r).RunPause(context.Background(), "Tankbot"); err != nil {
		t.Fatalf("RunPause: %v", err)
	}
	r = oneReply("run-begin #T", runStartRecord, "run-end count=1 records=1 verb=resume #T")
	if _, err := NewClient(r).RunResume(context.Background(), "Tankbot"); err != nil {
		t.Fatalf("RunResume: %v", err)
	}
	if r.seen[0] != ".tb run resume Tankbot" {
		t.Fatalf("line %q", r.seen[0])
	}
}

func TestRunTargetMismatchAndRefusals(t *testing.T) {
	end := "run-end count=1 records=1 verb=start #T"
	tests := []struct {
		name string
		raws []string
	}{
		{"other leader", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "lead=Tankbot", "lead=Moff"), end}},
		{"bad state", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "state=running", "state=gone"), end}},
		{"bad phase", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "phase=begin", "phase=lunch"), end}},
		{"bad kind shape", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "kind=move", "kind=Move!"), end}},
		{"bad boss state", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "boss=", "boss=2732:gone"), end}},
		{"bad boss shape", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "boss=", "boss=2732"), end}},
		{"missing note", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, " note=started", ""), end}},
		{"missing boss", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, " boss=", ""), end}},
		{"two records for one leader", []string{"run-begin #T", runStartRecord,
			strings.ReplaceAll(runStartRecord, "step=0", "step=1"), "run-end count=2 records=2 verb=start #T"}},
		{"wrong verb", []string{"run-begin #T", runStartRecord, "run-end count=1 records=1 verb=status #T"}},
		{"bad count", []string{"run-begin #T", runStartRecord, "run-end count=2 records=1 verb=start #T"}},
		{"bad records count", []string{"run-begin #T", runStartRecord, "run-end count=1 records=2 verb=start #T"}},
		{"bad percent", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "note=started", "note=bad%2"), end}},
		{"repeated key", []string{"run-begin #T", strings.ReplaceAll(runStartRecord, "note=started", "note=a note=b"), end}},
		{"two start records", []string{"run-begin #T", runStartRecord, runDoneRecord,
			"run-end count=2 records=2 verb=start #T"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(oneReply(tc.raws...)).RunStart(context.Background(), "Tankbot", "ragefire_chasm")
			if err == nil {
				t.Fatal("want an error")
			}
			var ce *CommandError
			if errors.As(err, &ce) {
				t.Fatalf("a bad run record read as the module's refusal %v", ce)
			}
			if !errors.Is(err, ErrBadReport) && !errors.Is(err, ErrBadValue) {
				t.Fatalf("err %v, want ErrBadReport or ErrBadValue", err)
			}
		})
	}
}

func TestRunEventRefusals(t *testing.T) {
	end := "run-end count=1 records=1 verb=events lead=Tankbot last=1 #T"
	tests := []struct {
		name string
		line string
	}{
		{"unknown kind", "event seq=1 t=0 kind=lunch step=1 leg=1 why= #T"},
		{"zero seq", "event seq=0 t=0 kind=start why= #T"},
		{"missing why", "event seq=1 t=0 kind=start step=1 leg=1 #T"},
		{"bad step", "event seq=1 t=0 kind=start step=x leg=1 why= #T"},
		{"zero unit", "event seq=1 t=0 kind=death unit=0 why=Tankbot #T"},
		{"bad unit", "event seq=1 t=0 kind=death unit=abc why=Tankbot #T"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(oneReply("run-begin #T", tc.line, end)).RunEvents(context.Background(), "Tankbot", 0, false)
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, ErrBadReport) && !errors.Is(err, ErrBadValue) {
				t.Fatalf("err %v, want ErrBadReport or ErrBadValue", err)
			}
		})
	}
	// Events out of sequence and a last below the newest one are refused too.
	_, err := NewClient(oneReply("run-begin #T",
		"event seq=2 t=1 kind=step why= #T", "event seq=1 t=2 kind=step why= #T",
		"run-end count=2 records=2 verb=events lead=Tankbot last=2 #T")).RunEvents(context.Background(), "Tankbot", 0, false)
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("out-of-sequence err %v", err)
	}
	_, err = NewClient(oneReply("run-begin #T",
		"event seq=2 t=1 kind=step why= #T", "run-end count=1 records=1 verb=events lead=Tankbot last=1 #T")).
		RunEvents(context.Background(), "Tankbot", 0, false)
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("last-below-newest err %v", err)
	}
}

func TestRunArguments(t *testing.T) {
	c := NewClient(oneReply("run-begin #T", "run-end count=0 records=0 verb=status #T"))
	if _, err := c.RunStatus(context.Background(), "B@d!"); err == nil {
		t.Fatal("a bad leader name was sent")
	}
	if _, err := c.RunEvents(context.Background(), "Tankbot", -1, true); err == nil {
		t.Fatal("a negative since was sent")
	}
	if _, err := NewClient(oneReply()).RunStart(context.Background(), "Tankbot", "bad plan!"); err == nil {
		t.Fatal("a bad plan name was sent")
	}
	if _, err := c.RunStop(context.Background(), "Tankbot", strings.Repeat("xx ", 17)); err == nil {
		t.Fatal("a stop reason of 17 words was sent")
	}
}

func TestRunStatusEmptyAll(t *testing.T) {
	rep, err := NewClient(oneReply("run-begin #T", "run-end count=0 records=0 verb=status runs=0 #T")).
		RunStatus(context.Background(), "all")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if len(rep.Runs) != 0 {
		t.Fatalf("%d run(s)", len(rep.Runs))
	}
}
