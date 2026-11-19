package command

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// scriptTransport is a Transport driven by a script and a fake clock: each fed line advances the clock, and the
// pump then advances it until the collector's own clock says it is done.
type scriptTransport struct {
	lines   []chat.Line
	step    time.Duration
	clock   *time.Time
	sent    []string
	sentAt  []time.Time
	sendErr error
	pumpErr error
}

func (t *scriptTransport) SendCommand(line string) error {
	if t.sendErr != nil {
		return t.sendErr
	}
	t.sent = append(t.sent, line)
	t.sentAt = append(t.sentAt, *t.clock)
	return nil
}

func (t *scriptTransport) Pump(ctx context.Context, feed func(chat.Line), done func() bool) error {
	if t.pumpErr != nil {
		return t.pumpErr
	}
	for _, l := range t.lines {
		*t.clock = t.clock.Add(t.step)
		feed(l)
		if done() {
			return nil
		}
	}
	for range 100000 {
		*t.clock = t.clock.Add(t.step)
		if done() {
			return nil
		}
	}
	return nil
}

func newScript(lines []chat.Line) (*scriptTransport, time.Time, func() time.Time) {
	start := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	clock := start
	t := &scriptTransport{lines: lines, step: 100 * time.Millisecond, clock: &clock}
	return t, start, func() time.Time { return clock }
}

func sys(text string) chat.Line { return chat.Line{Type: chat.TypeSystem, Text: text} }
func say(name, text string) chat.Line {
	return chat.Line{Type: chat.TypeSay, SenderName: name, Text: text}
}

func TestReplyLineParsing(t *testing.T) {
	cases := []struct {
		raw   string
		kind  string
		field map[string]string
		token string
		words int
	}{
		{raw: "state bot=Tbtank hp=612 resting=no #t1", kind: "state", field: map[string]string{"bot": "Tbtank", "hp": "612", "resting": "no"}, token: "t1"},
		{raw: "state-end count=1 resting=0 target=Tbtank #t1", kind: "state-end", field: map[string]string{"count": "1", "resting": "0", "target": "Tbtank"}, token: "t1"},
		{raw: "error=no-such-player cmd=state #t9", kind: "error", field: map[string]string{"code": "no-such-player", "cmd": "state"}, token: "t9"},
		{raw: "Module: 4 bots online", kind: "Module:", words: 3},
		{raw: "order bot=Tbtank verb=pull seq=41 status=queued #t7", kind: "order", field: map[string]string{"bot": "Tbtank", "verb": "pull", "seq": "41", "status": "queued"}, token: "t7"},
		{raw: "Command '.tb x' does not exist", kind: "Command", words: 5},
	}
	for _, c := range cases {
		ln := ParseReplyLine(c.raw)
		if ln.Kind != c.kind {
			t.Errorf("%q: kind %q, want %q", c.raw, ln.Kind, c.kind)
		}
		if ln.Token != c.token {
			t.Errorf("%q: token %q, want %q", c.raw, ln.Token, c.token)
		}
		for k, want := range c.field {
			if got, ok := ln.Get(k); !ok || got != want {
				t.Errorf("%q: field %s=%q (present %v), want %q", c.raw, k, got, ok, want)
			}
		}
		if c.words > 0 && len(ln.Words) != c.words {
			t.Errorf("%q: %d free word(s), want %d (%v)", c.raw, len(ln.Words), c.words, ln.Words)
		}
	}
}

func TestVisibleStripsColourAndLinks(t *testing.T) {
	cases := map[string]string{
		"|cffff0000red|r":                  "red",
		"a||b":                             "a||b",
		"a|b":                              "a|b",
		"|Hplayer:Tbtank|h[Tbtank]|h says": "[Tbtank] says",
		"|cffffff00|Hplayer:X|h[X]|h":      "[X]",
		"plain":                            "plain",
		"|n":                               "",
		"|cnothex":                         "|cnothex",
		"|Hunterminated":                   "unterminated",
	}
	for in, want := range cases {
		if got := Visible(in); got != want {
			t.Errorf("Visible(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokenCollector(t *testing.T) {
	lines := []chat.Line{
		sys("welcome SENTINEL-UNRELATED"),
		sys("state bot=Tbtank hp=612 #t1"),
		sys("someone else's state-end count=1 resting=0 #zzz"),
		sys("state bot=Tbdps1 hp=500 #t1"),
		sys("state-end count=2 resting=0 target=party:Tbtank #t1"),
	}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".tb state party", Options{Token: "t1", EndKind: "state-end", Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Complete || rep.End != "state-end" {
		t.Fatalf("reply: complete %v, end %q", rep.Complete, rep.End)
	}
	if len(rep.Raw) != 3 {
		t.Fatalf("%d collected line(s), want 3: %v", len(rep.Raw), rep.Raw)
	}
	if rep.Lines[0].Kind != "state" || rep.Lines[2].Kind != "state-end" {
		t.Fatalf("kinds: %v", rep.Lines)
	}
	if len(tr.sent) != 1 || tr.sent[0] != ".tb state party #t1" {
		t.Fatalf("sent %v", tr.sent)
	}
}

func TestTokenReplySplitOverMessages(t *testing.T) {
	// Each line arrives in its own read; the pump feeds them one at a time, as the session's packet loop does.
	lines := []chat.Line{
		sys("state bot=Tbtank hp=612 #t2"),
		sys("state bot=Tbdps1 hp=500 #t2"),
		sys("state-end count=2 resting=0 #t2"),
	}
	tr, _, now := newScript(lines)
	tr.step = 400 * time.Millisecond // a gap longer than the quiet window: a token reply ignores it
	rep, err := Run(context.Background(), tr, ".tb state", Options{Token: "t2", EndKind: "state-end", Quiet: 100 * time.Millisecond, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Raw) != 3 || !rep.Complete {
		t.Fatalf("%d line(s), complete %v", len(rep.Raw), rep.Complete)
	}
}

func TestQuietWindowEndsTokenlessReply(t *testing.T) {
	lines := []chat.Line{
		sys("Module: enabled"),
		sys("Module: 4 bot session(s)"),
	}
	tr, _, now := newScript(lines)
	tr.step = 100 * time.Millisecond
	rep, err := Run(context.Background(), tr, ".tb status", Options{Quiet: 500 * time.Millisecond, Deadline: 5 * time.Second, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.End != "quiet" || !rep.Complete || len(rep.Raw) != 2 {
		t.Fatalf("end %q, complete %v, %d line(s)", rep.End, rep.Complete, len(rep.Raw))
	}
}

func TestDeadlineEndsAnIncompleteTokenReply(t *testing.T) {
	lines := []chat.Line{sys("state bot=Tbtank hp=612 #t3")}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".tb state", Options{Token: "t3", EndKind: "state-end", Deadline: 1500 * time.Millisecond, Now: now})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err %v, want ErrIncomplete", err)
	}
	if rep == nil || len(rep.Raw) != 1 || rep.Complete {
		t.Fatalf("reply %+v", rep)
	}
}

func TestNoReply(t *testing.T) {
	tr, _, now := newScript(nil)
	rep, err := Run(context.Background(), tr, ".server info", Options{Deadline: 1 * time.Second, Now: now})
	if !errors.Is(err, ErrNoReply) {
		t.Fatalf("err %v, want ErrNoReply", err)
	}
	if rep == nil || len(rep.Raw) != 0 {
		t.Fatalf("reply %+v", rep)
	}
}

func TestSayEchoIsNoPermission(t *testing.T) {
	lines := []chat.Line{say("Tbtank", ".server info")}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".server info", Options{SelfName: "tbtank", Deadline: 2 * time.Second, Now: now})
	if !errors.Is(err, ErrNoPermission) {
		t.Fatalf("err %v, want ErrNoPermission", err)
	}
	if rep == nil || !rep.Echoed {
		t.Fatalf("reply %+v", rep)
	}
}

func TestRefusalTexts(t *testing.T) {
	cases := []struct {
		text string
		want error
	}{
		{"Command 'server info' does not exist", ErrUnknownCommand},
		{"Incorrect syntax.", ErrSyntax},
		{"Subcommand 'server nope' does not exist.", ErrSyntax},
		{"Command 'se' is ambiguous:", ErrSyntax},
		{"Possible subcommands:", ErrSyntax},
		{"Commands available to you:", ErrSyntax},
		{"Value 'abc' is not valid for type number.", ErrSyntax},
		{"Syntax: .server info", ErrSyntax},
	}
	for _, c := range cases {
		tr, _, now := newScript([]chat.Line{sys(c.text)})
		_, err := Run(context.Background(), tr, ".server info", Options{Deadline: 2 * time.Second, Now: now})
		if !errors.Is(err, c.want) {
			t.Errorf("%q: err %v, want %v", c.text, err, c.want)
		}
	}
}

func TestCollectorBounds(t *testing.T) {
	// The line cap: a token reply that never ends is stopped at MaxLines, not collected without bound.
	lines := make([]chat.Line, 0, MaxCollectedLines+10)
	for i := range MaxCollectedLines + 10 {
		lines = append(lines, sys("state bot=Tbtank i="+itoa(i)+" #t4"))
	}
	tr, _, now := newScript(lines)
	_, err := Run(context.Background(), tr, ".tb state", Options{Token: "t4", EndKind: "state-end", MaxLines: 8, Now: now})
	if !errors.Is(err, ErrReplyTooLong) {
		t.Fatalf("line cap: err %v, want ErrReplyTooLong", err)
	}
	// One line over the chat packet's text field.
	big := sys("state bot=Tbtank pad=" + strings.Repeat("x", MaxLineBytes) + " #t5")
	tr, _, now = newScript([]chat.Line{big})
	_, err = Run(context.Background(), tr, ".tb state", Options{Token: "t5", EndKind: "state-end", Now: now})
	if !errors.Is(err, ErrReplyTooLong) {
		t.Fatalf("line bound: err %v, want ErrReplyTooLong", err)
	}
	// The byte cap: many long lines and no end marker; past 64 KiB the run stops instead of growing.
	var many []chat.Line
	for range 20 {
		many = append(many, sys("state bot=Tbtank pad="+strings.Repeat("y", 4000)+" #t6"))
	}
	tr, _, now = newScript(many)
	_, err = Run(context.Background(), tr, ".tb state", Options{Token: "t6", EndKind: "state-end", Now: now})
	if !errors.Is(err, ErrReplyTooLong) {
		t.Fatalf("byte bound: err %v, want ErrReplyTooLong", err)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = digits[i%10]
		i /= 10
	}
	return string(b[p:])
}

// concurrentTransport fails if two pumps overlap: the queue must serialise whole runs.
type concurrentTransport struct {
	inside atomic.Int32
	peak   atomic.Int32
}

func (c *concurrentTransport) SendCommand(string) error { return nil }

func (c *concurrentTransport) Pump(ctx context.Context, feed func(chat.Line), done func() bool) error {
	n := c.inside.Add(1)
	for {
		peak := c.peak.Load()
		if n <= peak || c.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	c.inside.Add(-1)
	return nil
}

func TestSessionSerialisesRuns(t *testing.T) {
	conn := &concurrentTransport{}
	q := NewSession(conn)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = q.Run(context.Background(), ".server info", Options{Deadline: 100 * time.Millisecond, Quiet: 10 * time.Millisecond})
		}()
	}
	wg.Wait()
	if peak := conn.peak.Load(); peak != 1 {
		t.Fatalf("%d pumps overlapped, want 1", peak)
	}
}

func TestTokens(t *testing.T) {
	for range 64 {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidToken(tok) {
			t.Fatalf("NewToken gave %q, which ValidToken refuses", tok)
		}
	}
	for _, bad := range []string{"", "with space", "tab\t", strings.Repeat("a", 33), "hash#"} {
		if ValidToken(bad) {
			t.Errorf("ValidToken(%q) = true", bad)
		}
	}
}

func TestRunValidatesTheLine(t *testing.T) {
	tr, _, now := newScript(nil)
	if _, err := Run(context.Background(), tr, "plain chat", Options{Now: now}); err == nil {
		t.Fatal("Run accepted a line that is not a command")
	}
	if len(tr.sent) != 0 {
		t.Fatalf("a refused line was sent: %v", tr.sent)
	}
}

func TestSayEchoByGUID(t *testing.T) {
	// The live server fills SenderName only for a whisper: a self say line carries the GUID and no name.
	self := wire.GUID{Low: 0x0801180000000007, High: 0}
	echo := chat.Line{Type: chat.TypeSay, SenderGUID: self, Text: ".gm on"}
	tr, _, now := newScript([]chat.Line{echo})
	rep, err := Run(context.Background(), tr, ".gm on", Options{SelfGUID: self, Deadline: 2 * time.Second, Now: now})
	if !errors.Is(err, ErrNoPermission) || rep == nil || !rep.Echoed {
		t.Fatalf("err %v, reply %+v", err, rep)
	}
	// Another player saying the same text is not a refusal.
	other := chat.Line{Type: chat.TypeSay, SenderGUID: wire.GUID{Low: 5}, Text: ".gm on"}
	tr, _, now = newScript([]chat.Line{other})
	if _, err := Run(context.Background(), tr, ".gm on", Options{SelfGUID: self, Quiet: 200 * time.Millisecond, Deadline: 1 * time.Second, Now: now}); !errors.Is(err, ErrNoReply) {
		t.Fatalf("another player's line: err %v, want ErrNoReply", err)
	}
}

func TestRefusalLineDoesNotCutATokenlessBlock(t *testing.T) {
	// The core's usage block for a command matched with wrong arguments: a middle line is a syntax refusal, but
	// the block is one answer and must all land in the same run.
	lines := []chat.Line{
		sys("### USAGE: .gm ..."),
		sys("Possible subcommands:"),
		sys("|- gm on"),
		sys("Syntax: .gm on"),
	}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".gm on extra", Options{Quiet: 200 * time.Millisecond, Deadline: 2 * time.Second, Now: now})
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("err %v, want ErrSyntax", err)
	}
	if len(rep.Raw) != 4 {
		t.Fatalf("%d line(s), want the whole block: %v", len(rep.Raw), rep.Raw)
	}
	if rep.End != "quiet" {
		t.Fatalf("end %q", rep.End)
	}
}

func TestSayEchoAfterAFirstLine(t *testing.T) {
	// A notification that is not an answer arrives first; the say echo is still the proof, and it is seen even
	// though a line was already collected.
	lines := []chat.Line{
		sys("You are muted for 5 seconds."),
		say("Tbtank", ".gm"),
	}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".gm", Options{SelfName: "Tbtank", Quiet: time.Second, Deadline: 3 * time.Second, Now: now})
	if !errors.Is(err, ErrNoPermission) {
		t.Fatalf("err %v, want ErrNoPermission", err)
	}
	if rep == nil || !rep.Echoed || rep.End != "echo" {
		t.Fatalf("reply %+v", rep)
	}
}

func TestSayEchoByGUIDAfterAFirstLine(t *testing.T) {
	// The live server fills no sender name for a say: the GUID finds the echo after a collected line.
	self := wire.GUID{Low: 0x0801180000000007}
	lines := []chat.Line{
		sys("Global notify: an announcement"),
		{Type: chat.TypeSay, SenderGUID: self, Text: ".gm"},
	}
	tr, _, now := newScript(lines)
	if _, err := Run(context.Background(), tr, ".gm", Options{SelfGUID: self, Quiet: time.Second, Deadline: 3 * time.Second, Now: now}); !errors.Is(err, ErrNoPermission) {
		t.Fatalf("err %v, want ErrNoPermission", err)
	}
}

func TestLateEchoOfAnEarlierCommand(t *testing.T) {
	// The echo is not this run's line but an earlier command's; a self say of any command text is the
	// fall-through, and this client never says one as chat.
	lines := []chat.Line{say("Tbtank", ".gm on")}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".tb state", Options{SelfName: "Tbtank", Deadline: 2 * time.Second, Now: now})
	if !errors.Is(err, ErrNoPermission) || rep == nil || !rep.Echoed {
		t.Fatalf("err %v, reply %+v", err, rep)
	}
}

func TestTokenedRefusalBeforeTheTokenIsAccepted(t *testing.T) {
	// Through Session, which owns the quiet rule: an untagged refusal before any tokened line is this run's.
	lines := []chat.Line{sys("Command 'tb state' does not exist")}
	tr, _, now := newScript(lines)
	q := NewSession(tr)
	_, err := q.Run(context.Background(), ".tb state #t1", Options{Token: "t1", EndKind: "state-end", Deadline: 2 * time.Second, Now: now})
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("err %v, want ErrUnknownCommand", err)
	}
}

func TestTokenedRefusalAfterATokenedLineIsIgnored(t *testing.T) {
	// The tail of an earlier tokenless reply is unrelated traffic once this run has its own tokened line.
	lines := []chat.Line{
		sys("state bot=Tbtank hp=612 #t1"),
		sys("Syntax: .server info"),
		sys("state-end count=1 target=Tbtank #t1"),
	}
	tr, _, now := newScript(lines)
	q := NewSession(tr)
	rep, err := q.Run(context.Background(), ".tb state #t1", Options{Token: "t1", EndKind: "state-end", Deadline: 2 * time.Second, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Refusal != nil || len(rep.Raw) != 2 || !rep.Complete {
		t.Fatalf("reply %+v", rep)
	}
}

func TestDirectRunRefusesUntaggedRefusalForAToken(t *testing.T) {
	// Run without a Session has no way to know that no other command's tail is pending, so an untagged refusal
	// is not this run's; the run ends incomplete instead.
	lines := []chat.Line{sys("Syntax: .server info")}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".tb state #t1", Options{Token: "t1", EndKind: "state-end", Deadline: 300 * time.Millisecond, Now: now})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err %v, want ErrIncomplete", err)
	}
	if rep == nil || rep.Refusal != nil || len(rep.Raw) != 0 {
		t.Fatalf("reply %+v", rep)
	}
}

func TestSessionWaitsOutATokenlessTail(t *testing.T) {
	// A tokenless run ended by its line cap: its quiet window has not passed, so a tokened run must wait for it
	// before it is sent.
	start := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	clock := start
	now := func() time.Time { return clock }
	tr := &scriptTransport{clock: &clock, step: 100 * time.Millisecond, lines: []chat.Line{
		sys("Module: line one"),
		sys("Module: line two"),
	}}
	q := NewSession(tr)
	q.sleep = func(d time.Duration) { clock = clock.Add(d) }
	if _, err := q.Run(context.Background(), ".tb status", Options{Quiet: 500 * time.Millisecond, MaxLines: 2, Deadline: 5 * time.Second, Now: now}); err != nil {
		t.Fatalf("the tokenless run: %v", err)
	}
	if _, err := q.Run(context.Background(), ".tb state #t9", Options{Token: "t9", EndKind: "state-end", Deadline: 200 * time.Millisecond, Now: now}); err == nil {
		t.Fatalf("the tokened run succeeded without an end line")
	}
	if len(tr.sentAt) != 2 {
		t.Fatalf("sent %v", tr.sent)
	}
	if d := tr.sentAt[1].Sub(tr.sentAt[0]); d < 500*time.Millisecond {
		t.Fatalf("the tokened run was sent %s after the tokenless one, want at least its 500ms quiet window", d)
	}
}

func TestSessionCancelDuringTheTailWait(t *testing.T) {
	start := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	clock := start
	now := func() time.Time { return clock }
	tr := &scriptTransport{clock: &clock, step: 100 * time.Millisecond, lines: []chat.Line{sys("Module: line one")}}
	q := NewSession(tr)
	if _, err := q.Run(context.Background(), ".tb status", Options{Quiet: 500 * time.Millisecond, MaxLines: 1, Deadline: 5 * time.Second, Now: now}); err != nil {
		t.Fatalf("the tokenless run: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	q.sleep = func(time.Duration) { cancel() }
	if _, err := q.Run(ctx, ".tb state #t9", Options{Token: "t9", EndKind: "state-end", Deadline: time.Second, Now: now}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if len(tr.sent) != 1 {
		t.Fatalf("a cancelled run was sent: %v", tr.sent)
	}
}

func TestReplyLinesInPlayerChatAreNotCollected(t *testing.T) {
	// A reply-shaped line said, whispered or sent to the party by another player is not server text: it is not
	// collected and an end line among them does not end the run.
	lines := []chat.Line{
		{Type: chat.TypeSay, SenderName: "Other", Text: "state bot=X #t1"},
		{Type: chat.TypeWhisper, SenderName: "Other", Text: "state bot=X #t1"},
		{Type: chat.TypeParty, SenderName: "Other", Text: "state-end count=1 #t1"},
		sys("state bot=X #t1"),
		sys("state-end count=1 #t1"),
	}
	tr, _, now := newScript(lines)
	rep, err := Run(context.Background(), tr, ".tb state", Options{Token: "t1", EndKind: "state-end", Deadline: 2 * time.Second, Now: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Raw) != 2 || rep.Lines[1].Kind != "state-end" || !rep.Complete {
		t.Fatalf("reply %+v", rep)
	}
}

func TestDuplicateKeysOnOneLine(t *testing.T) {
	ln := ParseReplyLine("state bot=A bot=B hp=1 #t1")
	if ln.Bad == "" {
		t.Fatalf("a duplicate key was accepted: %+v", ln)
	}
	if got, _ := ln.Get("bot"); got != "A" {
		t.Fatalf("the first value was not kept: %q", got)
	}
}

func FuzzParseReplyLine(f *testing.F) {
	for _, seed := range []string{
		"state bot=A hp=1 #t1", "error code=x cmd=y #t1", "state-end count=1 #t1",
		"Command 'x' does not exist", "state bot=A bot=B #t1", "", "#t1", "state =x", "a=b=c #t1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		ln := ParseReplyLine(raw)
		if len(ln.Keys) != len(ln.Fields) {
			t.Fatalf("%q: %d key(s), %d field(s)", raw, len(ln.Keys), len(ln.Fields))
		}
		for _, k := range ln.Keys {
			if k == "" {
				t.Fatalf("%q: an empty key", raw)
			}
			if _, ok := ln.Fields[k]; !ok {
				t.Fatalf("%q: key %q has no field", raw, k)
			}
		}
		if ln.Token != "" && !ValidToken(ln.Token) {
			t.Fatalf("%q: token %q is invalid", raw, ln.Token)
		}
	})
}

func FuzzVisible(f *testing.F) {
	for _, seed := range []string{
		"plain", "|cffff0000red|r", "a||b", "|Hplayer:X|h[X]|h", "|Hunterminated", "|", "||", "|T1|t", "|n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := Visible(s)
		if len(out) > len(s) {
			t.Fatalf("Visible(%q) grew to %q", s, out)
		}
	})
}
