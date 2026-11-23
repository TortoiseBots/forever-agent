package scenario

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
)

// TestMatcherReadsTheRawSystemLine pins the G1 gotcha: a system event's text
// is colour-stripped and loses |r, so matchers read data.raw instead.
func TestMatcherReadsTheRawSystemLine(t *testing.T) {
	e := events.Event{
		Kind: events.KindSystem,
		Text: "TBM:RUN|zulfarrakunning|76/247|use_on|done",
		Data: map[string]any{"raw": "TBM:RUN|zulfarrak|running|76/247|use_on|done"},
	}
	if got := eventLine(e); got != "TBM:RUN|zulfarrak|running|76/247|use_on|done" {
		t.Fatalf("line %q", got)
	}
	m, err := Matcher{Kind: "system", Match: `^TBM:RUN\|zulfarrak\|running\|`}.compile(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.matches(e) {
		t.Fatal("raw system line does not match")
	}
	plain := events.Event{Kind: events.KindSystem, Text: "hello"}
	if got := eventLine(plain); got != "hello" {
		t.Fatalf("line %q", got)
	}
}

// TestMatcherReadsThePartyState pins that a party row's packet state is matchable: a live invite names the bot in
// its text and says "invite" only in data, and the quest scenario has no bot name to match.
func TestMatcherReadsThePartyState(t *testing.T) {
	m, err := Matcher{Kind: "party", Match: "invite"}.compile(nil)
	if err != nil {
		t.Fatal(err)
	}
	invite := events.Event{Kind: events.KindParty, Source: "Botname", Text: "Botname", Data: map[string]any{"state": "invite"}}
	if !m.matches(invite) {
		t.Fatalf("line %q does not match", eventLine(invite))
	}
	roster := events.Event{Kind: events.KindParty, Text: "Botname", Data: map[string]any{"state": "roster"}}
	if m.matches(roster) {
		t.Fatalf("line %q matches an invite", eventLine(roster))
	}
}

// TestMatcherAddonPrefixAndChannel pins addon matching on prefix and channel.
func TestMatcherAddonPrefixAndChannel(t *testing.T) {
	e := addonEvent("S2C_GROUPS_LIST_UPDATE;start", "TW_LFG", "guild")
	yes, err := Matcher{Kind: "addon", Prefix: "TW_LFG", Channel: "guild", Match: "."}.compile(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !yes.matches(e) {
		t.Fatal("TW_LFG/guild does not match")
	}
	for _, m := range []Matcher{
		{Kind: "addon", Prefix: "TBM", Match: "."},
		{Kind: "addon", Prefix: "TW_LFG", Channel: "party", Match: "."},
		{Kind: "system", Prefix: "TW_LFG", Match: "."},
	} {
		c, err := m.compile(nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.matches(e) {
			t.Fatalf("%+v matches", m)
		}
	}
}
