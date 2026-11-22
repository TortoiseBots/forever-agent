package driver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
)

// TestGoldenOwnerAnswers proves the fake answers the contract's states with the contract's refusals, on the virtual
// clock, and that every line it recorded is a line botcmd.Line builds.
func TestGoldenOwnerAnswers(t *testing.T) {
	start := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	clock := NewVClock(start)
	o := &GoldenOwner{Clock: clock, SelfMap: 389, Bots: []*OwnerBot{
		{Name: "Botone", InParty: true, Map: 389, Level: 12, Class: "Warrior", Move: "follow", Mode: "engine"},
		{Name: "Botthree", Level: 12, Class: "Warrior"},
		{Name: "Bottwo", Online: true, InParty: true, Map: 1, Level: 13, Class: "Priest", Move: "stay", Mode: "engine"},
		{Name: "Botfour", Online: true, InParty: true, Map: 389, Dead: true},
		{Name: "Botfive", Online: true, InParty: true, Map: 1, SummonCooldownUntil: start.Add(300 * time.Second)},
	}}
	ctx := context.Background()

	if r, err := o.Add(ctx, "botthree"); err != nil || !r.OK || r.Bot != "Botthree" {
		t.Fatalf("add an offline bot = %+v, %v; want ok for Botthree", r, err)
	}
	if r, err := o.Add(ctx, "Botthree"); !errors.Is(err, botcmd.ErrRefused) || r.Code != "already-online" {
		t.Fatalf("add it twice = %+v, %v; want refused already-online", r, err)
	}
	if r, err := o.Add(ctx, "Botnine"); !errors.Is(err, botcmd.ErrRefused) || r.Code != "not-found" {
		t.Fatalf("add a character the table lacks = %+v, %v; want refused not-found", r, err)
	}
	if r, err := o.Follow(ctx, "Botfour"); !errors.Is(err, botcmd.ErrRefused) || r.Code != "dead" {
		t.Fatalf("follow a dead bot = %+v, %v; want refused dead", r, err)
	}
	if r, err := o.Summon(ctx, "Botfive"); !errors.Is(err, botcmd.ErrRefused) || r.Code != "on-cooldown" {
		t.Fatalf("summon during the cooldown = %+v, %v; want refused on-cooldown", r, err)
	}
	clock.Advance(300 * time.Second)
	if r, err := o.Summon(ctx, "Botfive"); err != nil || !r.OK {
		t.Fatalf("summon after the cooldown = %+v, %v", r, err)
	}
	if r, err := o.Summon(ctx, "Botfive"); !errors.Is(err, botcmd.ErrRefused) || r.Code != "already-on-map" {
		t.Fatalf("summon a bot already on the map = %+v, %v; want refused already-on-map", r, err)
	}
	if r, err := o.Who(ctx); err != nil || !r.OK || r.Text != "4 bot(s)" {
		t.Fatalf("who = %+v, %v; want ok with 4 bot(s)", r, err)
	}

	before := len(o.Lines())
	if _, err := o.Follow(ctx, "Bad1"); err == nil {
		t.Fatal("a name the contract cannot carry must be refused")
	}
	if len(o.Lines()) != before {
		t.Fatal("a refused name was recorded; a line that Line cannot build never reaches the module")
	}

	for _, line := range o.Lines() {
		words := strings.Fields(line)
		if len(words) < 2 || words[0] != ".bot" {
			t.Fatalf("recorded %q; the port sends .bot lines only", line)
		}
		v, ok := botcmd.ParseVerb(words[1])
		if !ok {
			t.Fatalf("recorded %q; %q is not a v1 verb", line, words[1])
		}
		bot := ""
		if len(words) > 2 {
			bot = words[2]
		}
		if built, err := botcmd.Line(v, bot, ""); err != nil || built != line {
			t.Fatalf("recorded %q; Line builds %q, %v", line, built, err)
		}
	}
}
