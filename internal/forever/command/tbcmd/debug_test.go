package tbcmd

// Hand-written samples of the core's leash-and-evade answers. Reference copies of the core's format strings
// (copied here, not just cited): the boundary handlers print no line on success (CreatureAI::VisualizeBoundary
// returns 0, cs_debug.cpp:1318 prints only when the result is an error id; cs_npc.cpp HandleNpcEvadeCommand
// returns true with no line); the failure strings are 'VisualizeBoundary warning: No interior point of the
// creature's boundary could be found - check if you have mutually exclusive boundaries!',
// 'VisualizeBoundary error: Creature movement is unbounded' and 'VisualizeBoundary warning: Reached fail-safe
// flood boundary - check is your boundary is unbounded!' (LANG 11011-11013); the threat lines are '%s (%s) is not
// alive.%s', '%s (%s) does not threaten any units.', 'List of units threatened by %s (%s)',
// "   %u.   %s   (%s, SpawnID " UI64FMTD ")  - threat %f", 'End of threatened-by-me list.',
// 'Threat list of %s (%s, SpawnID " UI64FMTD "):', '%s (%s, SpawnID " UI64FMTD ") is not engaged, but still has a
// threat list? Well, here it is:', "   %u.   %s   (%s)  - threat %f%s%s", 'End of threat list.',
// '%s (%s, SpawnID " UI64FMTD ") is not currently engaged.',
// '%s (%s, SpawnID " UI64FMTD ") seems to be engaged, but does not have a threat list??',
// '%s (%s) is currently engaged. (This unit cannot have a threat list.)',
// '%s (%s) is not currently engaged. (This unit cannot have a threat list.)' (cs_debug.cpp:877-968); the combat
// lines are 'Combat refs: (Combat state: %d | Manager state: %d)', '[PvP] %s (SpawnID %u)' and
// '[PvE] %s (SpawnID %u)' (HandleDebugCombatListCommand).

import (
	"context"
	"testing"
)

func TestDebugBoundary(t *testing.T) {
	// Success prints nothing: the markers in the world are the answer.
	got, err := parseBoundary(bossReply())
	if err != nil || !got.Bounded || got.Failure != "" {
		t.Fatalf("empty %+v %v", got, err)
	}
	got, err = parseBoundary(bossReply("VisualizeBoundary error: Creature movement is unbounded"))
	if err != nil || got.Bounded || got.Failure != "unbounded" {
		t.Fatalf("unbounded %+v %v", got, err)
	}
	for text, failure := range map[string]string{
		"VisualizeBoundary warning: No interior point of the creature's boundary could be found - check if you have mutually exclusive boundaries!": "no-interior",
		"VisualizeBoundary warning: Reached fail-safe flood boundary - check is your boundary is unbounded!":                                        "failsafe",
	} {
		got, err = parseBoundary(bossReply(text))
		if err != nil || got.Bounded || got.Failure != failure {
			t.Fatalf("%q: %+v %v", text, got, err)
		}
	}
	for _, lines := range [][]string{
		{"something else entirely"},
		{"VisualizeBoundary error: Creature movement is unbounded", "VisualizeBoundary error: Creature movement is unbounded"},
	} {
		if _, err := parseBoundary(bossReply(lines...)); err == nil {
			t.Fatalf("%q parsed", lines)
		}
	}
	var sent string
	c := NewClient(bossRunner(bossReply(), &sent))
	if _, err := c.DebugBoundary(context.Background(), true, 60, true); err != nil {
		t.Fatalf("DebugBoundary: %v", err)
	}
	if sent != ".debug boundary fill 60" {
		t.Fatalf("line %q", sent)
	}
	c = NewClient(bossRunner(bossReply(), &sent))
	if _, err := c.DebugBoundary(context.Background(), false, 0, false); err != nil {
		t.Fatalf("DebugBoundary: %v", err)
	}
	if sent != ".debug boundary" {
		t.Fatalf("line %q", sent)
	}
}

func TestNpcEvade(t *testing.T) {
	got, err := parseEvade(bossReply())
	if err != nil || !got.Evaded {
		t.Fatalf("empty %+v %v", got, err)
	}
	// Any line at all is another command's tail: evade prints nothing on success.
	if _, err := parseEvade(bossReply("whatever")); err == nil {
		t.Fatal("a line parsed as an evade")
	}
	var sent string
	c := NewClient(bossRunner(bossReply(), &sent))
	if _, err := c.NpcEvade(context.Background(), "other", true); err != nil {
		t.Fatalf("NpcEvade: %v", err)
	}
	if sent != ".npc evade other force" {
		t.Fatalf("line %q", sent)
	}
	if _, err := c.NpcEvade(context.Background(), "two words", false); err == nil {
		t.Fatal("two-word reason sent")
	}
}

func TestDebugThreat(t *testing.T) {
	rep := bossReply(
		"List of units threatened by Broodlord (0xF130001234567890)",
		"   1.   Tbtank   (0x0700000001234567, SpawnID 0)  - threat 1234.500000",
		"End of threatened-by-me list.",
		"Threat list of Broodlord (0xF130001234567890, SpawnID 34567):",
		"   1.   Tbtank   (0x0700000001234567)  - threat 1234.500000 [TAUNT]",
		"   2.   Tbheal   (0x0700000001234568)  - threat 12.000000 [SUPPRESSED] [OFFLINE]",
		"End of threat list.",
	)
	got, err := parseDebugThreat(rep)
	if err != nil {
		t.Fatalf("parseDebugThreat: %v", err)
	}
	if got.Subject != "Broodlord" || !got.Engaged || !got.HasThreatList || !got.Alive {
		t.Fatalf("report %+v", got)
	}
	if len(got.ThreatenedByMe) != 1 || !got.ThreatenedByMe[0].HasSpawn || got.ThreatenedByMe[0].SpawnID != 0 {
		t.Fatalf("byme %+v", got.ThreatenedByMe)
	}
	if len(got.ThreatList) != 2 || len(got.ThreatList[0].Flags) != 1 || got.ThreatList[0].Flags[0] != "TAUNT" {
		t.Fatalf("list %+v", got.ThreatList)
	}
	if len(got.ThreatList[1].Flags) != 2 || got.ThreatList[1].Threat != 12 {
		t.Fatalf("list %+v", got.ThreatList)
	}
	if len(got.Raw) != 7 {
		t.Fatalf("raw kept %d lines", len(got.Raw))
	}
	// The odd shapes: not engaged but listed, not engaged and empty, engaged but empty, cannot-have, dead.
	for _, lines := range [][]string{
		{"Broodlord (0xF130001234567890, SpawnID 34567) is not engaged, but still has a threat list? Well, here it is:",
			"   1.   Tbtank   (0x0700000001234567)  - threat 5.000000",
			"End of threat list."},
		{"Broodlord (0xF130001234567890, SpawnID 34567) is not currently engaged."},
		{"Broodlord (0xF130001234567890, SpawnID 34567) seems to be engaged, but does not have a threat list??"},
		{"Broodlord (0xF130001234567890) is currently engaged. (This unit cannot have a threat list.)"},
		{"Broodlord (0xF130001234567890) is not currently engaged. (This unit cannot have a threat list.)"},
		{"Broodlord (0xF130001234567890) is not alive. (It is, however, engaged. Huh?)"},
		{"Broodlord (0xF130001234567890) does not threaten any units."},
	} {
		sub, err := parseDebugThreat(bossReply(lines...))
		if err != nil {
			t.Fatalf("%q: %v", lines, err)
		}
		if sub.Subject != "Broodlord" || len(sub.Raw) != len(lines) {
			t.Fatalf("%q: %+v", lines, sub)
		}
	}
	if _, err := parseDebugThreat(bossReply("some other line")); err == nil {
		t.Fatal("another line parsed as threat")
	}
	var sent string
	c := NewClient(bossRunner(rep, &sent))
	if _, err := c.DebugThreat(context.Background()); err != nil {
		t.Fatalf("DebugThreat: %v", err)
	}
	if sent != ".debug threat" {
		t.Fatalf("line %q", sent)
	}
}

func TestDebugCombat(t *testing.T) {
	rep := bossReply(
		"Combat refs: (Combat state: 1 | Manager state: 1)",
		"[PvE] Tbtank (SpawnID 0)",
		"[PvE] Broodlord (SpawnID 34567)",
	)
	got, err := parseDebugCombat(rep)
	if err != nil {
		t.Fatalf("parseDebugCombat: %v", err)
	}
	if !got.SubjectInCombat || !got.ManagerCombat || len(got.Refs) != 2 {
		t.Fatalf("report %+v", got)
	}
	if got.Refs[0].PvP || got.Refs[0].Name != "Tbtank" || got.Refs[0].SpawnID != 0 {
		t.Fatalf("refs %+v", got.Refs)
	}
	empty, err := parseDebugCombat(bossReply("Combat refs: (Combat state: 0 | Manager state: 0)"))
	if err != nil || len(empty.Refs) != 0 || empty.SubjectInCombat {
		t.Fatalf("empty %+v %v", empty, err)
	}
	for _, lines := range [][]string{
		{},
		{"[PvE] Tbtank (SpawnID 0)"},
		{"Combat refs: (Combat state: 1 | Manager state: 1)", "gibberish"},
		{"Combat refs: (Combat state: 1 | Manager state: 1)",
			"Combat refs: (Combat state: 1 | Manager state: 1)"},
	} {
		if _, err := parseDebugCombat(bossReply(lines...)); err == nil {
			t.Fatalf("%q parsed", lines)
		}
	}
	var sent string
	c := NewClient(bossRunner(bossReply(
		"Combat refs: (Combat state: 0 | Manager state: 0)",
	), &sent))
	if _, err := c.DebugCombat(context.Background()); err != nil {
		t.Fatalf("DebugCombat: %v", err)
	}
	if sent != ".debug combat" {
		t.Fatalf("line %q", sent)
	}
}
