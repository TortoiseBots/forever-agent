package world

// The act runner's step parsing: one `--step 'verb k=v'` line becomes an ActStep, with the bad verb, the missing
// key and the bad duration refused.

import (
	"testing"
)

func TestParseActStep(t *testing.T) {
	step, err := ParseActStep("wait dur=2s")
	if err != nil {
		t.Fatalf("ParseActStep: %v", err)
	}
	if step.Verb != "wait" || step.Args["dur"] != "2s" {
		t.Fatalf("the step parsed as %+v", step)
	}
	gm, err := ParseActStep("gm line=.additem")
	if err != nil {
		t.Fatalf("ParseActStep: %v", err)
	}
	if gm.Verb != "gm" || gm.Args["line"] != ".additem" {
		t.Fatalf("the gm step parsed as %s %v", gm.Verb, gm.Args)
	}
	if _, err := ParseActStep("dance"); err == nil {
		t.Fatal("the parser accepted a bad verb")
	}
	if _, err := ParseActStep("npc entry=123"); err != nil {
		t.Fatalf("ParseActStep(npc entry=123): %v", err)
	}
	if _, err := ParseActStep("open extra"); err == nil {
		t.Fatal("the parser accepted a bare word after the verb")
	}
	if _, err := ParseActStep("wait dur=2s dur=3s"); err == nil {
		t.Fatal("the parser accepted a duplicate key")
	}
}

func TestActWaitParsesDurations(t *testing.T) {
	f := newDeathWorld(t)
	ctx := f.s.ctx
	if err := f.s.actStep(ctx, &actState{}, mustActStep(t, "wait dur=150ms")); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := f.s.opts.Logf; got == nil {
		t.Fatal("no log sink")
	}
	if len(f.lines) != 1 || f.lines[0] != "step 1 wait ok waited 150ms" {
		t.Fatalf("the wait line is %q", f.lines)
	}
	if err := f.s.actStep(ctx, &actState{}, mustActStep(t, "wait dur=forever")); err == nil {
		t.Fatal("the wait accepted a bad duration")
	}
	if err := f.s.actStep(ctx, &actState{}, mustActStep(t, "wait dur=11m")); err == nil {
		t.Fatal("the wait accepted a duration over 10m")
	}
}

func TestActGMNeedsALine(t *testing.T) {
	f := newDeathWorld(t)
	if err := f.s.actStep(f.s.ctx, &actState{}, mustActStep(t, "gm")); err == nil {
		t.Fatal("the gm step sent nothing with no line key")
	} else if got := err.Error(); got != "world: step \"gm\" needs line=" {
		t.Fatalf("the gm error is %q", got)
	}
}

func mustActStep(t *testing.T, line string) ActStep {
	t.Helper()
	step, err := ParseActStep(line)
	if err != nil {
		t.Fatalf("ParseActStep(%q): %v", line, err)
	}
	return step
}
