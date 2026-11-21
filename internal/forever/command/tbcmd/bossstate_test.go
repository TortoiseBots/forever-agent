package tbcmd

// Hand-written samples of the core's boss-state answers. The reference copies of the core's format strings sit in
// bossstate.go beside the matcher: 'Boss id %u state is now set to %i (%s).' and 'Boss id %u state is %i (%s).'
// (trinity strings 5057/5058, 2017_99_99_18863_world.sql), sent by cs_instance.cpp:204 and :251 with the
// EncounterState number and its ToConstant word (NOT_STARTED 0, IN_PROGRESS 1, FAIL 2, DONE 3, SPECIAL 4,
// TO_BE_DECIDED 5; InstanceScript.h:68-76, enuminfo_InstanceScript.cpp:30-41).

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// bossReply is a tokenless core reply carrying server lines. Raw and RawText are both set the way the
// collector sets them (command.go: the raw text is the matching copy, Raw its visible form). An empty reply
// stands for the production runner's quiet window with nothing collected: Complete with End "quiet", the
// AllowEmpty success of `.debug boundary` and `.npc evade`.
func bossReply(lines ...string) *command.Reply {
	if len(lines) == 0 {
		return &command.Reply{Complete: true, End: "quiet"}
	}
	return &command.Reply{Raw: append([]string(nil), lines...), RawText: append([]string(nil), lines...), Complete: true, End: "quiet"}
}

// bossRunner answers every line with the same reply and records what it was sent.
func bossRunner(rep *command.Reply, sent *string) *fakeRunner {
	return &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		*sent = line
		if o.Token != "" {
			panic("bossstate: the core commands carry no token")
		}
		return rep, nil
	}}
}

func TestBossStateSetAndGet(t *testing.T) {
	// The set is read back with a get (the core prints the requested state even when the script
	// refuses the transition): the fake runner answers each line, the way the collector does.
	var sent []string
	run := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		sent = append(sent, line)
		switch line {
		case ".instance setbossstate 3 1":
			return bossReply("Boss id 3 state is now set to 1 (IN_PROGRESS)."), nil
		case ".instance getbossstate 3":
			return bossReply("Boss id 3 state is 1 (IN_PROGRESS)."), nil
		}
		t.Fatalf("unexpected line %q", line)
		return nil, nil
	}}
	c := NewClient(run)
	got, err := c.SetBossState(context.Background(), 3, 1, "")
	if err != nil {
		t.Fatalf("SetBossState: %v", err)
	}
	if len(sent) != 2 || sent[0] != ".instance setbossstate 3 1" || sent[1] != ".instance getbossstate 3" {
		t.Fatalf("lines %q", sent)
	}
	if got.ID != 3 || got.State != 1 || got.Name != "IN_PROGRESS" || !got.Set {
		t.Fatalf("report %+v", got)
	}

	var getSent string
	c = NewClient(bossRunner(bossReply("Boss id 3 state is 1 (IN_PROGRESS)."), &getSent))
	got, err = c.GetBossState(context.Background(), 3, "")
	if err != nil {
		t.Fatalf("GetBossState: %v", err)
	}
	if getSent != ".instance getbossstate 3" {
		t.Fatalf("line %q", getSent)
	}
	if got.ID != 3 || got.State != 1 || got.Name != "IN_PROGRESS" || got.Set {
		t.Fatalf("report %+v", got)
	}
}

// TestBossStateRefusedTransition: the set prints DONE but the get still reads IN_PROGRESS — the script
// refused (DONE-is-terminal and TO_BE_DECIDED rules in InstanceScript.cpp) — so the set is a typed error
// naming both, never a DONE report.
func TestBossStateRefusedTransition(t *testing.T) {
	run := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".instance setbossstate 0 3":
			return bossReply("Boss id 0 state is now set to 3 (DONE)."), nil
		case ".instance getbossstate 0":
			return bossReply("Boss id 0 state is 1 (IN_PROGRESS)."), nil
		}
		t.Fatalf("unexpected line %q", line)
		return nil, nil
	}}
	if _, err := NewClient(run).SetBossState(context.Background(), 0, 3, ""); err == nil {
		t.Fatal("refused transition reported DONE")
	} else if !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "IN_PROGRESS") {
		t.Fatalf("error %v names neither the refusal nor the real state", err)
	}
}

func TestBossStatePlayer(t *testing.T) {
	var sent string
	c := NewClient(bossRunner(bossReply("Boss id 0 state is 3 (DONE)."), &sent))
	got, err := c.GetBossState(context.Background(), 0, "Tbtank")
	if err != nil {
		t.Fatalf("GetBossState: %v", err)
	}
	if sent != ".instance getbossstate 0 Tbtank" {
		t.Fatalf("line %q", sent)
	}
	if got.ID != 0 || got.State != 3 || got.Name != "DONE" {
		t.Fatalf("report %+v", got)
	}
}

func TestBossStateStates(t *testing.T) {
	for _, c := range []struct {
		line  string
		state uint32
		name  string
	}{
		{"Boss id 1 state is 0 (NOT_STARTED).", 0, "NOT_STARTED"},
		{"Boss id 1 state is 2 (FAIL).", 2, "FAIL"},
		{"Boss id 1 state is 4 (SPECIAL).", 4, "SPECIAL"},
		{"Boss id 1 state is 5 (TO_BE_DECIDED).", 5, "TO_BE_DECIDED"},
	} {
		got, err := parseBossState(bossReply(c.line), false)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if got.State != c.state || got.Name != c.name {
			t.Fatalf("%q parsed as %+v", c.line, got)
		}
		if name, ok := BossStateName(c.state); !ok || name != c.name {
			t.Fatalf("BossStateName(%d) = %q, %v", c.state, name, ok)
		}
	}
	if _, ok := BossStateName(6); ok {
		t.Fatal("BossStateName(6) is known")
	}
}

func TestBossStateRefusals(t *testing.T) {
	// The state number is bounded before anything is sent.
	var sent string
	c := NewClient(bossRunner(bossReply("Boss id 1 state is now set to 6 (TO_BE_DECIDED)."), &sent))
	if _, err := c.SetBossState(context.Background(), 1, 6, ""); err == nil {
		t.Fatal("state 6 sent")
	}
	if sent != "" {
		t.Fatalf("nothing may be sent, sent %q", sent)
	}
	// A get answer to a set, and a set answer to a get, is another command's tail.
	if _, err := parseBossState(bossReply("Boss id 1 state is 1 (IN_PROGRESS)."), true); err == nil {
		t.Fatal("get line parsed as a set")
	}
	if _, err := parseBossState(bossReply("Boss id 1 state is now set to 1 (IN_PROGRESS)."), false); err == nil {
		t.Fatal("set line parsed as a get")
	}
	// The core's refusals and syntax lines are not states, nor is a second line in the window.
	for _, lines := range [][]string{
		{"Incorrect syntax."},
		{"The boss id is not valid."},
		{"Map is not a dungeon."},
		{"Map has no instance data."},
		{},
		{"Boss id 1 state is 1 (IN_PROGRESS).", "Boss id 2 state is 0 (NOT_STARTED)."},
		{"Boss id 1 state is 1 (IN_PROGRESS) extra."},
		{"Boss id 1 state is 1 (BOGUS)."},
		{"Boss id 1 state is 2 (IN_PROGRESS)."},
	} {
		if _, err := parseBossState(bossReply(lines...), false); err == nil {
			t.Fatalf("%q parsed", strings.Join(lines, "|"))
		}
	}
	// A wrong boss id in the answer is the wrong boss.
	c = NewClient(bossRunner(bossReply("Boss id 2 state is 1 (IN_PROGRESS)."), &sent))
	if _, err := c.GetBossState(context.Background(), 1, ""); err == nil {
		t.Fatal("boss 2 parsed for boss 1")
	}
}
