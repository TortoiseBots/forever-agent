package tbcmd

import (
	"context"
	"errors"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// actionCase is one setup call: the line it must send, the end kind and the answer.
type actionCase struct {
	name string
	call func(c *Client) (ActionReport, error)
	line string
	end  string
}

// TestActionsSendTypedLines: every setup command builds its one line from typed arguments, requires its own end
// marker and reads only a reply that ended. A reply of another command, a count that disagrees with the lines and a
// line the layer does not read are refused; the module's error line is the named refusal.
func TestActionsSendTypedLines(t *testing.T) {
	cases := []actionCase{
		{"tele", func(c *Client) (ActionReport, error) {
			return c.Tele(context.Background(), "Tbdpsone", 44, -8913, -136.04, 82.5)
		}, ".tb tele Tbdpsone 44 -8913.0 -136.0 82.5", "tele-end"},
		{"tele party", func(c *Client) (ActionReport, error) {
			return c.TeleParty(context.Background(), 0, 1.25, 2, -3)
		}, ".tb tele party 0 1.2 2.0 -3.0", "tele-end"},
		{"tele to", func(c *Client) (ActionReport, error) {
			return c.TeleTo(context.Background(), "Lead", "Tbtank")
		}, ".tb tele Lead to Tbtank", "tele-end"},
		{"enter", func(c *Client) (ActionReport, error) {
			return c.EnterParty(context.Background())
		}, ".tb enter party", "enter-end"},
		{"revive", func(c *Client) (ActionReport, error) {
			return c.Revive(context.Background(), "Tbdpstwo")
		}, ".tb revive Tbdpstwo", "revive-end"},
		{"revive party", func(c *Client) (ActionReport, error) {
			return c.Revive(context.Background(), "party")
		}, ".tb revive party", "revive-end"},
		{"instance leave", func(c *Client) (ActionReport, error) {
			return c.InstanceLeave(context.Background(), "Tbtank")
		}, ".tb instance Tbtank leave", "instance-end"},
		{"instance reset", func(c *Client) (ActionReport, error) {
			return c.InstanceReset(context.Background(), "Tbtank")
		}, ".tb instance Tbtank reset", "instance-end"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
				if line != tc.line {
					t.Fatalf("line %q, want %q", line, tc.line)
				}
				return reply(o, tc.end+" count=0 #T"), nil
			}}
			rep, err := tc.call(NewClient(r))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if rep.Kind != tc.end || rep.Count != 0 || rep.Verb == "" {
				t.Fatalf("report %+v", rep)
			}
		})
	}
}

// TestActionRepliesAreStrict: only the command's own end line, tokened, with a count that matches the acknowledged
// lines, is a report; the module's error line is a named refusal.
func TestActionRepliesAreStrict(t *testing.T) {
	call := func(raws ...string) (ActionReport, error) {
		return NewClient(oneReply(raws...)).Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
	}
	if _, err := call("tele-end count=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("count over the lines: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-end #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a count-less end: err %v, want ErrBadReport", err)
	}
	if _, err := call("enter-end count=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("another command's end: err %v, want ErrBadReport", err)
	}
	if _, err := call("order bot=Tbdpsone status=ok #T", "tele-end count=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("another kind's record line: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele tgtentry=426 #T", "tele-end count=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a record line without target=: err %v, want ErrBadReport", err)
	}
	// The error line is the module's refusal, with its code.
	errRunner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, "error code=no-such-bot cmd=tele #T"), nil
	}}
	rep, err := NewClient(errRunner).Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "no-such-bot" || rep.Verb != "" {
		t.Fatalf("refusal: %v (report %+v)", err, rep)
	}
}

// TestActionArgumentsAreChecked: a target that is not a name, the party keyword where a name belongs, and a
// position that is not a number are refused before anything is sent.
func TestActionArgumentsAreChecked(t *testing.T) {
	r := &fakeRunner{script: func(string, command.Options) (*command.Reply, error) {
		t.Fatal("a refused argument was sent")
		return nil, nil
	}}
	c := NewClient(r)
	if _, err := c.Tele(context.Background(), "party", 0, 1, 2, 3); err == nil {
		t.Fatal("Tele accepted the party keyword")
	}
	if _, err := c.TeleTo(context.Background(), "Lead", "party"); err == nil {
		t.Fatal("TeleTo accepted the party keyword as a destination")
	}
	if _, err := c.Tele(context.Background(), "Tbwaytoolongname", 0, 1, 2, 3); err == nil {
		t.Fatal("Tele accepted a name over the 12-byte limit")
	}
	if _, err := c.TeleParty(context.Background(), 0, 1e6, 0, 0); err == nil {
		t.Fatal("TeleParty accepted a position off the map")
	}
	if len(r.seen) != 0 {
		t.Fatalf("%d line(s) sent: %v", len(r.seen), r.seen)
	}
}

// TestActionPartyTargets: a setup answer may name the target it resolved before its end line, as the module does
// for a party command (`.tb tele party` answers target=party:commander:<name>). The label is typed, not text: the
// party form, its commander, or the character named. A label of another shape is refused.
func TestActionPartyTargets(t *testing.T) {
	cases := []struct {
		line string
		want ActionTarget
	}{
		{"tele target=party:commander:Watchcmd #T", ActionTarget{Raw: "party:commander:Watchcmd", Party: true, Commander: "Watchcmd", Name: "Watchcmd"}},
		{"tele target=party:Watchtank #T", ActionTarget{Raw: "party:Watchtank", Party: true, Name: "Watchtank"}},
		{"tele target=Watchtank #T", ActionTarget{Raw: "Watchtank", Name: "Watchtank"}},
		{"tele target=party:commander:Watch%5Fcmd #T", ActionTarget{Raw: "party:commander:Watch_cmd", Party: true, Commander: "Watch_cmd", Name: "Watch_cmd"}},
	}
	for _, c := range cases {
		rep, err := NewClient(oneReply(c.line, "tele-end count=1 #T")).Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if rep.Count != 1 || len(rep.Targets) != 1 || rep.Targets[0] != c.want {
			t.Fatalf("%q: report %+v", c.line, rep)
		}
	}
	// A party command's answer carries the party's commander: the bot the module runs, which is not this character.
	rep, err := NewClient(oneReply("tele target=party:commander:Watchcmd #T", "tele-end count=1 #T")).
		TeleParty(context.Background(), 389, 1, 2, 3)
	if err != nil || len(rep.Targets) != 1 || !rep.Targets[0].Party || rep.Targets[0].Commander != "Watchcmd" {
		t.Fatalf("party teleport: %v %+v", err, rep)
	}
	// The answer that only acknowledges is still a whole answer.
	if rep, err := NewClient(oneReply("tele-end count=0 #T")).TeleParty(context.Background(), 389, 1, 2, 3); err != nil || len(rep.Targets) != 0 {
		t.Fatalf("acknowledgement: %v %+v", err, rep)
	}
	for _, bad := range []string{
		"tele target=party:commander: #T",
		"tele target=party::Watchcmd #T",
		"tele target=party:Watchcmd:extra #T",
		"tele target= #T",
		"tele target=party #T", // the command's keyword, never a character named Party
	} {
		if _, err := NewClient(oneReply(bad, "tele-end count=1 #T")).Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3); !errors.Is(err, ErrBadValue) {
			t.Fatalf("%q: err %v, want ErrBadValue", bad, err)
		}
	}
}

// TestActionContinuationLines: a target record may be split over <verb>+ continuation lines like any record. A
// continuation joins the base line before it (and a target may sit in the continuation); a continuation before any
// base line, or one repeating a key, is malformed rather than a second target.
func TestActionContinuationLines(t *testing.T) {
	call := func(raws ...string) (ActionReport, error) {
		return NewClient(oneReply(raws...)).Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
	}
	// The merge: the base line and its continuation are one target record.
	rep, err := call("tele #T", "tele+ target=party:Watchtank #T", "tele-end count=2 #T")
	if err != nil {
		t.Fatalf("a split target record: %v", err)
	}
	if len(rep.Targets) != 1 || !rep.Targets[0].Party || rep.Targets[0].Name != "Watchtank" {
		t.Fatalf("split target: %+v", rep)
	}
	if _, err := call("tele+ target=party:Watchtank #T", "tele-end count=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an orphan continuation: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele target=party:Watchtank #T", "tele+ target=party:Watchcmd #T", "tele-end count=2 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a continuation repeating target=: err %v, want ErrBadReport", err)
	}
}
