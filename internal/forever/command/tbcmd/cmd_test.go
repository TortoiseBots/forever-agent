package tbcmd

// `.tb cmd` (rounds C5/C6): the line the call builds and the module's real answers, in the shapes of the module's
// TB-COMMANDS section 14 samples and its P3/P6 notes (character names replaced by Tanka and Healera).

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// cmdPullAccepted is the P3 notes' pull answer: one bot executes it, named in the result scope.
const (
	cmdPullAccepted = "cmd verb=pull status=accepted bots=1 result=bot%3ATanka%201 #T"
	guidTanka       = "0x0000000000000000000000000000BEEF"
	guidTankaBare   = "0000000000000000000000000000BEEF"
)

func TestCmdPullAnswer(t *testing.T) {
	r := oneReply("cmd-begin #T", cmdPullAccepted, "cmd-end count=1 records=1 verb=pull lead=Tanka bots=1 #T")
	rep, err := NewClient(r).Cmd(context.Background(), "pull", guidTanka)
	if err != nil {
		t.Fatalf("Cmd: %v", err)
	}
	if want := ".tb cmd pull " + guidTankaBare; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if rep.Verb != "pull" || rep.Status != "accepted" || rep.Lead != "Tanka" {
		t.Fatalf("report %+v", rep)
	}
	if rep.Bots != 1 || rep.Queued != 1 || rep.Refused != 0 || rep.Count != 1 {
		t.Fatalf("counts %+v", rep)
	}
	if rep.Result != "bot:Tanka 1" {
		t.Fatalf("result %q", rep.Result)
	}
	if len(rep.Results) != 1 {
		t.Fatalf("%d result(s)", len(rep.Results))
	}
	res := rep.Results[0]
	if res.Bot != "Tanka" || res.Verb != "pull" || res.Status != "queued" || res.HasSeq {
		t.Fatalf("result %+v", res)
	}
}

// TestMoveDirectFlag pins the fourth word: `move x y z [direct]` sends `direct` as its own argument word, and the
// party's answer is one whole-party record.
func TestMoveDirectFlag(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, "cmd-begin #T",
			"cmd verb=move status=accepted bots=5 result=party%205 #T",
			"cmd-end count=1 records=1 verb=move lead=Tanka bots=5 #T"), nil
	}}
	c := NewClient(r)
	rep, err := c.Cmd(context.Background(), "move", "1.5", "-2.5", "3.5", "direct")
	if err != nil {
		t.Fatalf("move direct: %v", err)
	}
	if want := ".tb cmd move 1.5 -2.5 3.5 direct"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	if len(rep.Results) != 0 || rep.Bots != 5 || rep.Result != "party 5" {
		t.Fatalf("a party-wide answer names no bot: %+v", rep)
	}
	if _, err := c.Cmd(context.Background(), "move", "1.5", "-2.5", "3.5"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if want := ".tb cmd move 1.5 -2.5 3.5"; r.seen[1] != want {
		t.Fatalf("line %q, want %q", r.seen[1], want)
	}
}

// TestTargetGuidStrips0x: the agent's model writes guids as `0x<32 hex>`; the module's grammar is the bare digits.
func TestTargetGuidStrips0x(t *testing.T) {
	for _, in := range []string{guidTanka, "0X" + guidTankaBare[:32]} {
		r := oneReply("cmd-begin #T",
			"cmd verb=focus status=accepted bots=1 result=bot%3ATanka%201 #T",
			"cmd-end count=1 records=1 verb=focus lead=Tanka bots=1 #T")
		if _, err := NewClient(r).Cmd(context.Background(), "focus", in); err != nil {
			t.Fatalf("focus %q: %v", in, err)
		}
		if want := ".tb cmd focus " + guidTankaBare; r.seen[0] != want {
			t.Fatalf("line for %q is %q, want %q", in, r.seen[0], want)
		}
	}
	// A shorter 0x word is not a guid and is sent as written (the module refuses it; the line builder never guesses).
	r := oneReply("error code=invalid-target cmd=cmd msg=usage #T")
	_, err := NewClient(r).Cmd(context.Background(), "attack", "0x1F")
	var ce *CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("attack: err %v, want the module's refusal", err)
	}
	if r.seen[0] != ".tb cmd attack 0x1F" {
		t.Fatalf("line %q", r.seen[0])
	}
}

// TestCmdRestAndRevive pins the argument-less verbs' lines.
func TestCmdRestAndRevive(t *testing.T) {
	for _, tc := range []struct {
		verb string
		args []string
		line string
	}{
		{"rest", nil, ".tb cmd rest"},
		{"revive", []string{"party"}, ".tb cmd revive party"},
		{"pullback", []string{guidTankaBare}, ".tb cmd pullback " + guidTankaBare},
	} {
		r := oneReply("cmd-begin #T",
			"cmd verb="+tc.verb+" status=accepted bots=1 result=party%201 #T",
			"cmd-end count=1 records=1 verb="+tc.verb+" lead=Tanka bots=1 #T")
		rep, err := NewClient(r).Cmd(context.Background(), tc.verb, tc.args...)
		if err != nil {
			t.Fatalf("%s: %v", tc.verb, err)
		}
		if r.seen[0] != tc.line {
			t.Fatalf("line %q, want %q", r.seen[0], tc.line)
		}
		if rep.Verb != tc.verb || rep.Status != "accepted" {
			t.Fatalf("%s report %+v", tc.verb, rep)
		}
	}
}

// TestCmdRefusal is the module's one-line error: a typed CommandError, as for every command, never a zero report.
func TestCmdRefusal(t *testing.T) {
	r := oneReply("error code=unreachable cmd=cmd msg=The%20way%20to%20that%20point%20crosses%20lava #T")
	_, err := NewClient(r).Cmd(context.Background(), "move", "1", "2", "3")
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "unreachable" || ce.Cmd != "cmd" ||
		ce.Msg != "The way to that point crosses lava" {
		t.Fatalf("err %v", err)
	}
}

// TestCmdTaskLoot is the P6 task answer: the bot and the target guid, and the end line's ok/refused counts.
func TestCmdTaskLoot(t *testing.T) {
	r := oneReply("cmd-begin #T",
		"cmd verb=loot bot=Tanka status=accepted target="+guidTankaBare+" #T",
		"cmd-end count=1 records=1 verb=loot ok=1 refused=0 #T")
	rep, err := NewClient(r).Cmd(context.Background(), "loot", "my", "Tanka")
	if err != nil {
		t.Fatalf("loot: %v", err)
	}
	if r.seen[0] != ".tb cmd loot my Tanka" {
		t.Fatalf("line %q", r.seen[0])
	}
	if rep.Status != "accepted" || rep.Bots != 1 || rep.Queued != 1 {
		t.Fatalf("report %+v", rep)
	}
	if len(rep.Results) != 1 {
		t.Fatalf("%d result(s)", len(rep.Results))
	}
	res := rep.Results[0]
	if res.Bot != "Tanka" || res.Status != "queued" || res.Target != guidTankaBare || !res.HasTarget {
		t.Fatalf("result %+v", res)
	}
}

// TestCmdLootMethod keeps the keys this model does not type in Fields, decoded.
func TestCmdLootMethod(t *testing.T) {
	r := oneReply("cmd-begin #T",
		"cmd verb=lootmethod status=ok method=ffa threshold=2 #T",
		"cmd-end count=1 records=1 verb=lootmethod ok=1 refused=0 #T")
	rep, err := NewClient(r).Cmd(context.Background(), "lootmethod", "ffa")
	if err != nil {
		t.Fatalf("lootmethod: %v", err)
	}
	if rep.Status != "ok" || len(rep.Results) != 0 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Fields["method"] != "ffa" || rep.Fields["threshold"] != "2" {
		t.Fatalf("fields %v", rep.Fields)
	}
}

// TestCmdState reads the task records: one per bound bot, with the async status the loot/open/use/gossip verbs
// report through.
func TestCmdState(t *testing.T) {
	r := oneReply("cmd-begin #T",
		"task bot=Tanka busy=no status=done verb=loot entry=6910 text=looted%203%20item%28s%29 #T",
		"task bot=Healera busy=yes status=running verb=gossip #T",
		"task bot=Rogua busy=no status=failed verb=loot text=Nothing%20is%20left%20to%20loot #T",
		"cmd-end count=3 records=3 verb=state #T")
	rep, err := NewClient(r).Cmd(context.Background(), "state", "Tanka")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if rep.Verb != "state" || rep.Count != 3 || len(rep.Results) != 3 {
		t.Fatalf("report %+v", rep)
	}
	tanka, healera, rogua := rep.Results[0], rep.Results[1], rep.Results[2]
	if tanka.Bot != "Tanka" || tanka.Status != "done" || tanka.Verb != "loot" || !tanka.HasMsg ||
		tanka.Msg != "looted 3 item(s)" || tanka.Extra["busy"] != "no" || tanka.Extra["entry"] != "6910" {
		t.Fatalf("Tanka %+v", tanka)
	}
	if healera.Status != "running" || healera.Extra["busy"] != "yes" || healera.HasMsg {
		t.Fatalf("Healera %+v", healera)
	}
	if rogua.Status != "failed" || rogua.Msg != "Nothing is left to loot" {
		t.Fatalf("Rogua %+v", rogua)
	}
}

// TestCmdBadReport refuses the three ways a reply can disagree with itself.
func TestCmdBadReport(t *testing.T) {
	for name, raws := range map[string][]string{
		"record verb": {
			"cmd-begin #T",
			"cmd verb=pull status=accepted bots=1 result=party%201 #T",
			"cmd-end count=1 records=1 verb=stay lead=Tanka bots=1 #T",
		},
		"count": {
			"cmd-begin #T",
			"cmd verb=stay status=accepted bots=1 result=party%201 #T",
			"cmd-end count=2 records=1 verb=stay lead=Tanka bots=1 #T",
		},
		"task status": {
			"cmd-begin #T",
			"task bot=Tanka busy=no status=sleeping #T",
			"cmd-end count=1 records=1 verb=state #T",
		},
	} {
		t.Run(name, func(t *testing.T) {
			verb := "stay"
			if name == "task status" {
				verb = "state"
			}
			_, err := NewClient(oneReply(raws...)).Cmd(context.Background(), verb)
			if !errors.Is(err, ErrBadReport) && !errors.Is(err, ErrBadValue) {
				t.Fatalf("err %v, want a bad report", err)
			}
		})
	}
}

// TestCmdWords refuses a word that could inject a token or a second word.
func TestCmdWords(t *testing.T) {
	for _, tc := range []struct{ verb, arg string }{
		{"", "1"},
		{"stay", "1 2"},
		{"stay", "#tok"},
		{"stay", "a=b"},
		{"stay", strings.Repeat("x", cmdMaxWord+1)},
	} {
		if _, err := CmdLine(tc.verb, []string{tc.arg}); err == nil {
			t.Fatalf("CmdLine(%q, %q) answered", tc.verb, tc.arg)
		}
	}
	// The object target's grammar characters reach the wire.
	if line, err := CmdLine("open", []string{"entry:6910@-120.5,40,-47"}); err != nil ||
		line != ".tb cmd open entry:6910@-120.5,40,-47" {
		t.Fatalf("open line %q: %v", line, err)
	}
}

// The fixtures below are built from the module's own reply formats, not from this package's reader:
//   - an order verb (P3), TbCmdOrders.cpp HandleCmd: rec("cmd").S("verb", verb).S("status", "accepted")
//     .N("bots", n).S("result", text); end("cmd").S("verb", verb).S("lead", leadName).N("bots", n);
//   - a task verb (P6), TbCmdTasks.cpp HandleLootOpenUse: rec("cmd") verb, bot, status=accepted, target; end verb,
//     ok=1, refused=0;
//   - lootmethod: rec verb, status=ok, method, threshold; end verb, ok=1, refused=0;
//   - state, HandleState: one rec("task") per bot (bot, busy, status, optional verb, entry, text) and end verb=state.

func cmdRec(fields string) string { return "cmd " + fields + " #T" }
func cmdEnd(fields string) string { return "cmd-end " + fields + " #T" }
func recordsOf(n int) string      { return "count=" + strconv.Itoa(n) + " records=" + strconv.Itoa(n) }
func orderAcceptedFor(verb string) []string {
	return []string{"cmd-begin #T",
		cmdRec("verb=" + verb + " status=accepted bots=1 result=party%201"),
		cmdEnd(recordsOf(1) + " verb=" + verb + " lead=Tanka bots=1")}
}

// TestCmdAnswerForAnotherVerbIsBad: the reply is bound to the verb sent. A complete, self-consistent answer for
// `rest` to a `move` is not the answer to the move.
func TestCmdAnswerForAnotherVerbIsBad(t *testing.T) {
	_, err := NewClient(oneReply(orderAcceptedFor("rest")...)).Cmd(context.Background(), "move", "1", "2", "3")
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("err %v, want a bad report", err)
	}
	// A state answer to a loot is the same.
	_, err = NewClient(oneReply("cmd-begin #T", cmdEnd(recordsOf(0)+" verb=state"))).Cmd(context.Background(), "loot", "my")
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("state for loot: err %v, want a bad report", err)
	}
}

// TestCmdOrderWithNoRecordIsBad: an order verb's answer holds its one cmd record; none is a bad report, as are
// two. A state answer may hold none.
func TestCmdOrderWithNoRecordIsBad(t *testing.T) {
	_, err := NewClient(oneReply("cmd-begin #T", cmdEnd(recordsOf(0)+" verb=stay lead=Tanka bots=1"))).Cmd(context.Background(), "stay")
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("zero records: err %v, want a bad report", err)
	}
	two := []string{"cmd-begin #T",
		cmdRec("verb=stay status=accepted bots=1 result=party%201"),
		cmdRec("verb=stay status=accepted bots=1 result=party%201"),
		cmdEnd(recordsOf(2) + " verb=stay lead=Tanka bots=1")}
	if _, err := NewClient(oneReply(two...)).Cmd(context.Background(), "stay"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("two records: err %v, want a bad report", err)
	}
	rep, err := NewClient(oneReply("cmd-begin #T", cmdEnd(recordsOf(0)+" verb=state"))).Cmd(context.Background(), "state")
	if err != nil || len(rep.Results) != 0 {
		t.Fatalf("an empty state answer: %v %+v", err, rep)
	}
}

// TestCmdRefusedAndUnknownStatus: `status=refused` is a typed refusal with the module's reason, never a success;
// a status word outside accepted/ok is a bad report.
func TestCmdRefusedAndUnknownStatus(t *testing.T) {
	refused := []string{"cmd-begin #T",
		cmdRec("verb=stay status=refused bots=1 result=no%20engine"),
		cmdEnd(recordsOf(1) + " verb=stay lead=Tanka bots=1")}
	_, err := NewClient(oneReply(refused...)).Cmd(context.Background(), "stay")
	var cr *CmdRefusedError
	if !errors.As(err, &cr) || cr.Verb != "stay" || cr.Reason != "no engine" {
		t.Fatalf("err %v, want a refusal carrying the reason", err)
	}
	for _, status := range []string{"queued", "failed", "", "ACCEPTED"} {
		raws := []string{"cmd-begin #T",
			cmdRec("verb=stay status=" + status + " bots=1 result=party%201"),
			cmdEnd(recordsOf(1) + " verb=stay lead=Tanka bots=1")}
		if _, err := NewClient(oneReply(raws...)).Cmd(context.Background(), "stay"); err == nil || errors.As(err, &cr) {
			t.Fatalf("status %q: err %v, want a bad report", status, err)
		}
	}
}

// TestCmdTalliesMustAgree: the record's bots= and the end line's bots=, and the end line's ok=, must say the
// same thing.
func TestCmdTalliesMustAgree(t *testing.T) {
	for name, raws := range map[string][]string{
		"bots": {"cmd-begin #T",
			cmdRec("verb=stay status=accepted bots=1 result=party%201"),
			cmdEnd(recordsOf(1) + " verb=stay lead=Tanka bots=5")},
		"ok": {"cmd-begin #T",
			cmdRec("verb=loot bot=Tanka status=accepted target=0000000000000000000000000000BEEF"),
			cmdEnd(recordsOf(1) + " verb=loot ok=0 refused=0")},
		"refused with an accepted record": {"cmd-begin #T",
			cmdRec("verb=loot bot=Tanka status=accepted target=0000000000000000000000000000BEEF"),
			cmdEnd(recordsOf(1) + " verb=loot ok=1 refused=1")},
	} {
		t.Run(name, func(t *testing.T) {
			verb := "stay"
			if strings.Contains(raws[1], "verb=loot") {
				verb = "loot"
			}
			if _, err := NewClient(oneReply(raws...)).Cmd(context.Background(), verb, argFor(verb)); !errors.Is(err, ErrBadReport) {
				t.Fatalf("err %v, want a bad report", err)
			}
		})
	}
}

func argFor(verb string) string {
	if verb == "loot" {
		return "my"
	}
	return "x"
}

// TestCmdStateEntryZero: `entry=0` is accepted (a task on a target with no entry), as is a record with none.
func TestCmdStateEntryZero(t *testing.T) {
	r := oneReply("cmd-begin #T",
		"task bot=Tanka busy=no status=done verb=loot entry=0 #T",
		"task bot=Healera busy=no status=none #T",
		cmdEnd(recordsOf(2)+" verb=state"))
	rep, err := NewClient(r).Cmd(context.Background(), "state")
	if err != nil {
		t.Fatalf("state with entry=0: %v", err)
	}
	if rep.Results[0].Extra["entry"] != "0" {
		t.Fatalf("entry %v", rep.Results[0].Extra)
	}
	if _, ok := rep.Results[1].Extra["entry"]; ok {
		t.Fatalf("a record with no entry carries one: %v", rep.Results[1].Extra)
	}
}

// TestCmdLineIsTheWireLine: the exported builder is what the caller shows as the line sent.
func TestCmdLineIsTheWireLine(t *testing.T) {
	line, err := CmdLine("attack", []string{guidTanka})
	if err != nil || line != ".tb cmd attack "+guidTankaBare {
		t.Fatalf("line %q: %v", line, err)
	}
}

// TestCmdStateOfARaidFitsTheCollector: a bare `state` answers one task record per bound bot (HandleState), and a raid
// is 40 of them. The call raises the collector's cap above the typed layer's floor of 16, so the answer reaches its end
// marker.
func TestCmdStateOfARaidFitsTheCollector(t *testing.T) {
	raws := []string{"cmd-begin #T"}
	for i := range 40 {
		raws = append(raws, "task bot=Bot"+strconv.Itoa(i)+" busy=no status=none #T")
	}
	raws = append(raws, cmdEnd(recordsOf(40)+" verb=state"))
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if len(raws) > o.MaxLines {
			return nil, command.ErrIncomplete
		}
		return reply(o, raws...), nil
	}}
	rep, err := NewClient(r).Cmd(context.Background(), "state")
	if err != nil || len(rep.Results) != 40 {
		t.Fatalf("state of 40 bots: %v, %d result(s)", err, len(rep.Results))
	}
}
