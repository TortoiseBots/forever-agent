package botcmd

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// TestParseReplyNeedsPrefix proves a line is a reply only with the module's prefix, and that a refusal's code is
// kept even when the published list does not know it.
func TestParseReplyNeedsPrefix(t *testing.T) {
	for _, line := range []string{
		"add Botone: ok queued for login",
		"tortoisebots: add Botone: ok queued for login",
		"",
		Prefix + "who: 2 bot(s)",
		Prefix + "who: Botone level 12 Warrior map 389 follow engine",
		Prefix + "add Botone okay",
	} {
		if r, ok := ParseReply(line); ok {
			t.Errorf("%q parsed as %+v; want no reply", line, r)
		}
	}

	got, ok := ParseReply(Prefix + "attack Bottwo: refused target-dead: your selection is dead")
	want := Reply{Verb: "attack", Bot: "Bottwo", Code: "target-dead", Text: "your selection is dead"}
	if !ok || got != want {
		t.Fatalf("a refusal parsed %+v (%v), want %+v", got, ok, want)
	}
	if got, ok := ParseReply(Prefix + "follow Botone: refused made-up-code: something"); !ok || got.OK || got.Code != "made-up-code" {
		t.Fatalf("an unknown code parsed %+v (%v); an unknown code is a refusal, never an ok", got, ok)
	}
	if got, ok := ParseReply(Prefix + "stay Botone: ok"); !ok || !got.OK || got.Text != "" {
		t.Fatalf("an ok with no text parsed %+v (%v)", got, ok)
	}
}

// TestClassifyMatchesVerbAndBot proves the line taken is the one for the verb and the bot sent, never a neighbour's.
func TestClassifyMatchesVerbAndBot(t *testing.T) {
	rep := &command.Reply{RawText: []string{
		"TortoiseBots: attack Bottwo: refused no-selection: nothing is selected",
		"TortoiseBots: follow Botone: ok following you",
		"TortoiseBots: stay Botone: ok staying where it is",
		"TortoiseBots: follow Bottwo: refused not-in-group: it is not in your group",
		"a friendly system message",
	}}
	want := Reply{Verb: "follow", Bot: "Botone", OK: true, Text: "following you"}
	if got, err := Classify(rep, VerbFollow, "Botone"); err != nil || got != want {
		t.Fatalf("Classify = %+v, %v; want %+v", got, err, want)
	}
	if got, err := Classify(rep, VerbFollow, "botone"); err != nil || got.Bot != "Botone" {
		t.Fatalf("Classify on a lower-case name = %+v, %v; names are compared case-insensitively", got, err)
	}
	if got, err := Classify(rep, VerbFollow, "Botfour"); !errors.Is(err, ErrNoReply) {
		t.Fatalf("Classify for a bot the reply does not name = %+v, %v; want ErrNoReply", got, err)
	}
	if got, err := Classify(rep, VerbAdd, "Botone"); !errors.Is(err, ErrNoReply) {
		t.Fatalf("Classify for another verb = %+v, %v; want ErrNoReply", got, err)
	}
	if _, err := Classify(nil, VerbFollow, "Botone"); !errors.Is(err, ErrNoReply) {
		t.Fatalf("Classify on no reply = %v; want ErrNoReply", err)
	}
}

// TestModuleAbsentIsError proves the core's unknown-command answer is never read as an ok.
func TestModuleAbsentIsError(t *testing.T) {
	for _, line := range []string{"There is no such command", "There is no such command."} {
		rep := &command.Reply{Raw: []string{line}, RawText: []string{line}}
		if _, err := Classify(rep, VerbAdd, "Botone"); !errors.Is(err, ErrNoModule) {
			t.Fatalf("Classify on %q = %v; want ErrNoModule", line, err)
		}
		if r, ok := ParseReply(line); ok {
			t.Fatalf("%q parsed as %+v; the core's line is not a reply", line, r)
		}
	}
	ref := &command.Reply{Refusal: &command.RefusalError{Err: command.ErrUnknownCommand, Text: "Command 'bot' does not exist"}}
	if _, err := Classify(ref, VerbWho, ""); !errors.Is(err, ErrNoModule) {
		t.Fatalf("Classify on the collector's unknown-command refusal = %v; want ErrNoModule", err)
	}
}

// TestLineClosedVocabulary proves only the ten verbs and their stated shapes reach the wire.
func TestLineClosedVocabulary(t *testing.T) {
	for _, c := range []struct {
		v        Verb
		bot, arg string
		want     string
		bad      bool
	}{
		{v: VerbAdd, bot: "botone", want: ".bot add Botone"},
		{v: VerbSummon, bot: "BOTTWO", want: ".bot summon Bottwo"},
		{v: VerbWho, want: ".bot who"},
		{v: VerbHelp, want: ".bot help"},
		{v: Verb("run"), bot: "Botone", bad: true},
		{v: Verb(""), bot: "Botone", bad: true},
		{v: VerbAdd, bad: true},
		{v: VerbAdd, bot: `Bot\one`, bad: true},
		{v: VerbAdd, bot: "Bot\none", bad: true},
		{v: VerbFollow, bot: "Botone", arg: "now", bad: true},
		{v: VerbWho, bot: "Botone", bad: true},
	} {
		got, err := Line(c.v, c.bot, c.arg)
		if c.bad {
			if err == nil {
				t.Errorf("Line(%q, %q, %q) = %q; want an error", c.v, c.bot, c.arg, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Line(%q, %q, %q) = %q, %v; want %q", c.v, c.bot, c.arg, got, err, c.want)
		}
	}
}

// TestWhoRows proves the who answer is the header's count plus one well-formed row per bot, and nothing else.
func TestWhoRows(t *testing.T) {
	lines := []string{
		"a friendly system message",
		"TortoiseBots: who: 2 bot(s)",
		"TortoiseBots: who: Botone level 12 Warrior map 389 follow engine",
		"TortoiseBots: who: Bottwo level 13 Priest map 1 stay spike",
	}
	want := []WhoRow{
		{Bot: "Botone", Level: 12, Class: "Warrior", Map: 389, Move: "follow", Mode: "engine"},
		{Bot: "Bottwo", Level: 13, Class: "Priest", Map: 1, Move: "stay", Mode: "spike"},
	}
	got, err := ParseWho(lines)
	if err != nil {
		t.Fatalf("ParseWho: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ParseWho returned %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, err := ParseWho(lines[:3]); err == nil {
		t.Fatal("a header of 2 with one row parsed; the count is the contract")
	}
	bad := []string{lines[1], "TortoiseBots: who: Botone level 12 Warrior map 389 wander engine"}
	if _, err := ParseWho(bad); err == nil {
		t.Fatal("move=wander parsed; the row's move word is closed")
	}
	if _, err := ParseWho(lines[2:]); err == nil {
		t.Fatal("rows without a header parsed; the count is the contract")
	}
}

// TestRefusalWithoutBot proves a refusal that names no bot (what the module prints for usage, unknown-verb and
// players-off) parses as a refusal and answers the verb asked, and that an ok without a bot is no reply.
func TestRefusalWithoutBot(t *testing.T) {
	r, ok := ParseReply("TortoiseBots: add: refused players-off: player commands are off (TortoiseBots.Player.Commands = 0)")
	if !ok || r.OK || r.Verb != "add" || r.Bot != "" || r.Code != "players-off" {
		t.Fatalf("bot-less refusal parsed %+v, %v", r, ok)
	}
	if _, ok := ParseReply("TortoiseBots: add: ok queued"); ok {
		t.Fatal("an ok that names no bot parsed as a reply")
	}
	rep := &command.Reply{RawText: []string{"TortoiseBots: bot: refused players-off: player commands are off"}}
	got, err := Classify(rep, VerbFollow, "Botone")
	if err != nil || got.Code != "players-off" || !errors.Is(got.Err(), ErrRefused) {
		t.Fatalf("Classify on the empty-verb refusal = %+v, %v", got, err)
	}
	other := &command.Reply{RawText: []string{"TortoiseBots: stay: refused usage: x"}}
	if _, err := Classify(other, VerbFollow, "Botone"); !errors.Is(err, ErrNoReply) {
		t.Fatalf("another verb's bot-less refusal was taken: %v", err)
	}
	if _, err := ParseWho([]string{"TortoiseBots: who: refused usage: .bot who [all]"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("who refused = %v; want ErrRefused", err)
	}
}

type fakeRunner struct {
	lines []string
	opts  command.Options
}

func (f *fakeRunner) Run(_ context.Context, _ string, o command.Options) (*command.Reply, error) {
	f.opts = o
	return &command.Reply{RawText: f.lines}, nil
}

// TestWhoLargeRoster proves Who asks the collector for every row of a long roster (the header and 12 bots).
func TestWhoLargeRoster(t *testing.T) {
	lines := []string{"TortoiseBots: who: 12 bot(s)"}
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf("TortoiseBots: who: Bot%c level 10 Warrior map 1 follow engine", 'a'+i))
	}
	f := &fakeRunner{lines: lines}
	rows, err := (&Client{run: f}).Who(context.Background())
	if err != nil || len(rows) != 12 {
		t.Fatalf("Who = %d rows, %v", len(rows), err)
	}
	if f.opts.MaxLines < 13 {
		t.Fatalf("Who collected up to %d lines; a 12-bot roster needs 13", f.opts.MaxLines)
	}
}
