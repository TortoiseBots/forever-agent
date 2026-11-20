package tbcmd

import (
	"context"
	"errors"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// The reply lines in this file are the module's own shape: a begin marker, one record per character with its own
// status, and an end line that counts the lines and names the resolved target (`target=party:commander:<name>`).

// actionCase is one setup call: the line it must send, the end kind and the answer.
type actionCase struct {
	name  string
	call  func(c *Client) (ActionReport, error)
	line  string
	end   string
	lines []string
}

// TestActionsSendTypedLines: every setup command builds its one line from typed arguments, requires its own end
// marker and reads its reply's begin marker, per-bot records, counts and target label.
func TestActionsSendTypedLines(t *testing.T) {
	cases := []actionCase{
		{
			name: "tele",
			call: func(c *Client) (ActionReport, error) {
				return c.Tele(context.Background(), "Tbdpsone", 44, -8913, -136.04, 82.5)
			},
			line: ".tb tele Tbdpsone 44 -8913.0 -136.0 82.5",
			end:  "tele-end",
			lines: []string{
				"tele-begin #T",
				"tele bot=Tbdpsone status=started map=44 pos=-8913.0,-136.0,82.5 tag=SETUP #T",
				"tele-end count=1 records=1 target=Tbdpsone bots=1 started=1 refused=0 #T",
			},
		},
		{
			name: "tele party",
			call: func(c *Client) (ActionReport, error) {
				return c.TeleParty(context.Background(), 0, 1.25, 2, -3)
			},
			line: ".tb tele party 0 1.2 2.0 -3.0",
			end:  "tele-end",
			lines: []string{
				"tele-begin #T",
				"tele bot=Atank status=started map=0 pos=1.2,2.0,-3.0 tag=SETUP #T",
				"tele-end count=1 records=1 target=party:commander:Dcmd bots=1 started=1 refused=0 #T",
			},
		},
		{
			name: "tele to",
			call: func(c *Client) (ActionReport, error) {
				return c.TeleTo(context.Background(), "Lead", "Tbtank")
			},
			line: ".tb tele Lead to Tbtank",
			end:  "tele-end",
			lines: []string{
				"tele-begin #T",
				"tele bot=Lead status=started map=44 pos=10.0,20.0,30.0 tag=SETUP #T",
				"tele-end count=1 records=1 target=Lead bots=1 started=1 refused=0 #T",
			},
		},
		{
			name: "enter",
			call: func(c *Client) (ActionReport, error) {
				return c.EnterParty(context.Background(), 0)
			},
			line: ".tb enter party",
			end:  "enter-end",
			lines: []string{
				"enter-begin #T",
				"enter bot=Atank trigger=0 map=0 status=started inbox=no direct=no #T",
				"enter-end count=1 records=1 trigger=0 map=0 target=party:Atank bots=1 started=1 refused=0 #T",
			},
		},
		{
			name: "revive",
			call: func(c *Client) (ActionReport, error) {
				return c.Revive(context.Background(), "Tbdpstwo")
			},
			line: ".tb revive Tbdpstwo",
			end:  "revive-end",
			lines: []string{
				"revive-begin #T",
				"revive bot=Tbdpstwo tag=PRACTICE status=revived practice=yes #T",
				"revive-end count=1 records=1 tag=PRACTICE target=Tbdpstwo bots=1 revived=1 refused=0 #T",
			},
		},
		{
			name: "revive party",
			call: func(c *Client) (ActionReport, error) {
				return c.Revive(context.Background(), "party")
			},
			line: ".tb revive party",
			end:  "revive-end",
			lines: []string{
				"revive-begin #T",
				"revive bot=Atank tag=PRACTICE status=revived practice=yes #T",
				"revive bot=Bheal tag=PRACTICE status=revived practice=yes #T",
				"revive-end count=2 records=2 tag=PRACTICE target=party:Atank bots=2 revived=2 refused=0 #T",
			},
		},
		{
			name: "instance leave",
			call: func(c *Client) (ActionReport, error) {
				return c.InstanceLeave(context.Background(), "Tbtank")
			},
			line: ".tb instance Tbtank leave",
			end:  "instance-end",
			lines: []string{
				"instance-begin #T",
				"leave bot=Atank map=1 status=started #T",
				"instance-end count=1 records=1 verb=leave map=389 inst=7 bots=1 started=1 refused=0 #T",
			},
		},
		{
			name: "instance reset",
			call: func(c *Client) (ActionReport, error) {
				return c.InstanceReset(context.Background(), "Tbtank")
			},
			line: ".tb instance Tbtank reset",
			end:  "instance-end",
			lines: []string{
				"instance-begin #T",
				"reset map=389 inst=7 status=ok #T",
				"instance-end count=1 records=1 verb=reset leader=Atank instances=1 ok=1 failed=0 #T",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
				if line != tc.line {
					t.Fatalf("line %q, want %q", line, tc.line)
				}
				return reply(o, tc.lines...), nil
			}}
			rep, err := tc.call(NewClient(r))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if rep.Kind != tc.end || rep.Count == 0 || rep.Verb == "" {
				t.Fatalf("report %+v", rep)
			}
		})
	}
}

// TestEnterFallsBackToThePlanMap: `.tb enter party` from outside the entrance's box answers no-trigger; with the
// plan's map the call is retried once as `enter party map <id>` and the report is the real module's. Any other
// refusal is not retried.
func TestEnterFallsBackToThePlanMap(t *testing.T) {
	var seen []string
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		seen = append(seen, line)
		if len(seen) == 1 {
			return reply(o, "error code=no-trigger cmd=enter msg=no%20area%20trigger%20leads%20into%20that%20map #T"), nil
		}
		return reply(o, "enter-begin #T",
			"enter bot=Atank trigger=2230 map=389 status=started inbox=no direct=no #T",
			"enter-end count=1 records=1 trigger=2230 map=389 target=party:commander:Dcmd bots=1 started=1 refused=0 #T"), nil
	}}
	rep, err := NewClient(r).EnterParty(context.Background(), 389)
	if err != nil {
		t.Fatalf("EnterParty: %v", err)
	}
	want := []string{".tb enter party", ".tb enter party map 389"}
	if len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("lines %q, want %q", seen, want)
	}
	if len(rep.Targets) != 1 || rep.Targets[0].Commander != "Dcmd" {
		t.Fatalf("report %+v", rep)
	}

	// Another refusal is the module's answer and stands: no map retry.
	seen = nil
	r = &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		seen = append(seen, line)
		return reply(o, "error code=not-in-party cmd=enter #T"), nil
	}}
	_, err = NewClient(r).EnterParty(context.Background(), 389)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "not-in-party" || len(seen) != 1 {
		t.Fatalf("refusal %v after %q", err, seen)
	}

	// No map to fall back to: the bare form's no-trigger stays the answer.
	seen = nil
	r = &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		seen = append(seen, line)
		return reply(o, "error code=no-trigger cmd=enter #T"), nil
	}}
	_, err = NewClient(r).EnterParty(context.Background(), 0)
	if !errors.As(err, &ce) || ce.Code != "no-trigger" || len(seen) != 1 {
		t.Fatalf("no-trigger without a map: %v after %q", err, seen)
	}
}

// TestActionRefusalStaysAnError: the module's own error line and a per-bot refusal are both named refusals; a reply
// whose end line counts a refusal without a line that says so is one too.
func TestActionRefusalStaysAnError(t *testing.T) {
	cases := []struct {
		name string
		call func(c *Client) (ActionReport, error)
		code string
	}{
		{
			name: "error line",
			call: func(c *Client) (ActionReport, error) {
				return c.Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
			},
			code: "no-such-bot",
		},
		{
			name: "per-bot refusal",
			call: func(c *Client) (ActionReport, error) {
				return c.Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
			},
			code: "teleporting",
		},
		{
			name: "refusal without a code",
			call: func(c *Client) (ActionReport, error) {
				return c.EnterParty(context.Background(), 389)
			},
			code: "refused",
		},
		{
			name: "reset failure",
			call: func(c *Client) (ActionReport, error) {
				return c.InstanceReset(context.Background(), "Atank")
			},
			code: "other",
		},
	}
	lines := map[string][]string{
		"error line": {"error code=no-such-bot cmd=tele #T"},
		"per-bot refusal": {
			"tele-begin #T",
			"tele bot=Tbdpsone status=refused code=teleporting msg=the%20bot%20is%20being%20teleported #T",
			"tele-end count=1 records=1 target=Tbdpsone bots=1 started=0 refused=1 #T",
		},
		"refusal without a code": {
			"enter-begin #T",
			"enter bot=Atank trigger=0 map=389 status=refused msg=nobody%20entered #T",
			"enter-end count=1 records=1 trigger=0 map=389 target=party:Atank bots=1 started=0 refused=1 #T",
		},
		"reset failure": {
			"instance-begin #T",
			"reset map=389 inst=7 status=failed code=other msg=the%20group%20still%20names%20the%20instance #T",
			"instance-end count=1 records=1 verb=reset leader=Atank instances=1 ok=0 failed=1 #T",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call(NewClient(oneReply(lines[tc.name]...)))
			var ce *CommandError
			if !errors.As(err, &ce) || ce.Code != tc.code {
				t.Fatalf("error %v, want code %q", err, tc.code)
			}
		})
	}
}

// TestActionRepliesAreStrict: only the command's own begin and end markers, tokened, with counts that match the
// lines between them, are a report. The module's whole shape is required: an end-only acknowledgement, a reply
// missing records=, target=, a record's status= or the end line's outcome counts, and counts that disagree with the
// record lines or each other, are all ErrBadReport rather than a success nothing proved.
func TestActionRepliesAreStrict(t *testing.T) {
	call := func(raws ...string) (ActionReport, error) {
		return NewClient(oneReply(raws...)).Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
	}
	ok := "tele bot=Tbdpsone status=started #T"
	end := "tele-end count=1 records=1 target=Tbdpsone bots=1 started=1 refused=0 #T"
	if _, err := call("tele-begin #T", ok, end); err != nil {
		t.Fatalf("the module's whole report: %v", err)
	}
	if _, err := call("tele-begin #T", "tele-end count=1 records=1 target=Tbdpsone #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("count over the lines: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", ok, "tele-end #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a count-less end: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", ok, "enter-end count=1 records=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("another command's end: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", "order bot=Tbdpsone status=ok #T", end); !errors.Is(err, ErrBadReport) {
		t.Fatalf("another kind's record line: err %v, want ErrBadReport", err)
	}
	// The module always writes the character's name first; a record that names neither a character nor a target is
	// not a setup record this layer can read.
	if _, err := call("tele-begin #T", "tele status=started #T", end); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a record line without bot=: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", "tele target=Tbdpsone status=started #T", end); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a per-bot line with target= and no bot=: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", ok, "tele-end count=1 records=2 target=Tbdpsone bots=2 started=1 refused=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("records over the record lines: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin junk=1 #T", ok, end); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a begin marker with a field: err %v, want ErrBadReport", err)
	}

	// An end-only acknowledgement (an older module's count=0) proves no per-character success.
	if _, err := call("tele-end count=0 records=0 target=Tbdpsone bots=0 started=0 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an end-only acknowledgement: err %v, want ErrBadReport", err)
	}
	// A begin and an end with no records= or no target= is a truncated report, not an acknowledgement.
	if _, err := call("tele-begin #T", ok, "tele-end count=1 target=Tbdpsone bots=1 started=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an end without records=: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", ok, "tele-end count=1 records=1 bots=1 started=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an end without target=: err %v, want ErrBadReport", err)
	}
	// Each record says what the module did with its character; without status= it proves nothing.
	if _, err := call("tele-begin #T", "tele bot=Tbdpsone #T", end); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a record without status=: err %v, want ErrBadReport", err)
	}
	// The end line's own outcome counts are required, and they count the record lines.
	if _, err := call("tele-begin #T", ok, "tele-end count=1 records=1 target=Tbdpsone bots=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an end without started=: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", ok, "tele-end count=1 records=1 target=Tbdpsone bots=1 started=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an end without refused=: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", ok, "tele-end count=1 records=1 target=Tbdpsone bots=2 started=1 refused=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("bots= over the record lines: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", ok, "tele-end count=1 records=1 target=Tbdpsone bots=1 started=0 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("outcome counts short of the records: err %v, want ErrBadReport", err)
	}

	// The instance command's own report: the end names leave or reset, and each carries its own counts.
	instance := func(raws ...string) (ActionReport, error) {
		return NewClient(oneReply(raws...)).InstanceLeave(context.Background(), "Tbtank")
	}
	if _, err := instance("instance-begin #T", "leave bot=Atank map=1 status=started #T",
		"instance-end count=1 records=1 verb=info map=389 inst=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an instance info answer to a leave: err %v, want ErrBadReport", err)
	}
	if _, err := instance("instance-begin #T", "leave bot=Atank map=1 status=started #T",
		"instance-end count=1 records=1 map=389 inst=1 bots=1 started=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a leave end without verb=: err %v, want ErrBadReport", err)
	}
	if _, err := instance("instance-begin #T", "leave bot=Atank map=1 status=started #T",
		"instance-end count=1 records=1 verb=leave map=389 inst=1 bots=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a leave end without started=: err %v, want ErrBadReport", err)
	}
	if _, err := instance("instance-begin #T", "leave bot=Atank map=1 #T",
		"instance-end count=1 records=1 verb=leave map=389 inst=1 bots=1 started=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a leave record without status=: err %v, want ErrBadReport", err)
	}
	reset := func(raws ...string) (ActionReport, error) {
		return NewClient(oneReply(raws...)).InstanceReset(context.Background(), "Tbtank")
	}
	if _, err := reset("instance-begin #T", "reset map=389 inst=7 status=ok #T",
		"instance-end count=1 records=1 verb=reset leader=Atank instances=2 ok=1 failed=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("instances= over the record lines: err %v, want ErrBadReport", err)
	}
	if _, err := reset("instance-begin #T", "reset map=389 inst=7 status=ok #T",
		"instance-end count=1 records=1 verb=reset leader=Atank instances=1 failed=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a reset end without ok=: err %v, want ErrBadReport", err)
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

// TestActionPartyTargets: the end line names the target the module resolved, as the module writes it
// (`target=party:commander:<name>`). The label is typed, not text: the party form, its commander, or the character
// named. A label of another shape is refused, and a label that names another target than the one the command
// addressed is not this command's answer.
func TestActionPartyTargets(t *testing.T) {
	cases := []struct {
		target string
		want   ActionTarget
	}{
		{"target=party:commander:Watchcmd", ActionTarget{Raw: "party:commander:Watchcmd", Party: true, Commander: "Watchcmd", Name: "Watchcmd"}},
		{"target=party:Watchtank", ActionTarget{Raw: "party:Watchtank", Party: true, Name: "Watchtank"}},
		{"target=party:commander:Watch%5Fcmd", ActionTarget{Raw: "party:commander:Watch_cmd", Party: true, Commander: "Watch_cmd", Name: "Watch_cmd"}},
	}
	for _, c := range cases {
		// A party form's answer: the label is the party's leader or the commander it was bound to, the same party the
		// command addressed.
		rep, err := NewClient(oneReply("tele-begin #T",
			"tele bot=Watchtank status=started #T",
			"tele-end count=1 records=1 "+c.target+" bots=1 started=1 refused=0 #T")).
			TeleParty(context.Background(), 389, 1, 2, 3)
		if err != nil {
			t.Fatalf("%q: %v", c.target, err)
		}
		if rep.Count != 1 || len(rep.Targets) != 1 || rep.Targets[0] != c.want {
			t.Fatalf("%q: report %+v", c.target, rep)
		}
	}
	// One character's answer names that character, case-insensitively as the module spells it.
	rep, err := NewClient(oneReply("tele-begin #T",
		"tele bot=Tbdpsone status=started #T",
		"tele-end count=1 records=1 target=Tbdpsone bots=1 started=1 refused=0 #T")).
		Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
	if err != nil || len(rep.Targets) != 1 || rep.Targets[0].Name != "Tbdpsone" {
		t.Fatalf("single target: %v %+v", err, rep)
	}
	// The target is the one the command addressed: another character's answer is not a success for this call.
	if _, err := NewClient(oneReply("tele-begin #T",
		"tele bot=Tbdpsone status=started #T",
		"tele-end count=1 records=1 target=Otherguy bots=1 started=1 refused=0 #T")).
		Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3); !errors.Is(err, ErrBadReport) {
		t.Fatalf("another character's target: err %v, want ErrBadReport", err)
	}
	// A one-character command answered with the party's label is not that command's answer either.
	if _, err := NewClient(oneReply("tele-begin #T",
		"tele bot=Tbdpsone status=started #T",
		"tele-end count=1 records=1 target=party:Tbdpsone bots=1 started=1 refused=0 #T")).
		Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3); !errors.Is(err, ErrBadReport) {
		t.Fatalf("the party label for one character: err %v, want ErrBadReport", err)
	}
	for _, bad := range []string{
		"target=party:commander:",
		"target=party::Watchcmd",
		"target=party:Watchcmd:extra",
		"target=",
		"target=party", // the command's keyword, never a character named Party
	} {
		if _, err := NewClient(oneReply("tele-begin #T",
			"tele bot=Watchtank status=started #T",
			"tele-end count=1 records=1 "+bad+" bots=1 started=1 refused=0 #T")).
			TeleParty(context.Background(), 389, 1, 2, 3); !errors.Is(err, ErrBadValue) {
			t.Fatalf("%q: err %v, want ErrBadValue", bad, err)
		}
	}
}

// TestActionContinuationLines: a per-bot record may be split over <verb>+ continuation lines like any record. A
// continuation repeats the record's bot= as its first key; a continuation before any base line, or one repeating
// another key, is malformed rather than a second record.
func TestActionContinuationLines(t *testing.T) {
	call := func(raws ...string) (ActionReport, error) {
		return NewClient(oneReply(raws...)).Tele(context.Background(), "Tbdpsone", 0, 1, 2, 3)
	}
	rep, err := call("tele-begin #T", "tele bot=Tbdpsone #T",
		"tele+ bot=Tbdpsone status=started map=44 pos=-8913.0,-136.0,82.5 tag=SETUP #T",
		"tele-end count=2 records=1 target=Tbdpsone bots=1 started=1 refused=0 #T")
	if err != nil {
		t.Fatalf("a split record: %v", err)
	}
	if rep.Count != 2 || len(rep.Targets) != 1 || rep.Targets[0].Name != "Tbdpsone" {
		t.Fatalf("split record: %+v", rep)
	}
	if _, err := call("tele-begin #T", "tele+ bot=Tbdpsone status=started #T", "tele-end count=2 records=1 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an orphan continuation: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", "tele bot=Tbdpsone status=started #T",
		"tele+ bot=Tbdpsone status=started #T", "tele-end count=2 records=1 target=Tbdpsone bots=1 started=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a continuation repeating status=: err %v, want ErrBadReport", err)
	}
	if _, err := call("tele-begin #T", "tele bot=Tbdpsone status=started #T",
		"tele+ bot=Other status=revived #T", "tele-end count=2 records=1 target=Tbdpsone bots=1 started=1 refused=0 #T"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a continuation of another bot: err %v, want ErrBadReport", err)
	}
}
