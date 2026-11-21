package bughunt

// The registry's names and order: every rule's name is pinned here, so a deletion (rule 14's, once) or a rename
// that changes counts.json, ticket titles and baseline diffs is a test failure, not a quiet report change.

import (
	"reflect"
	"testing"
)

// pinnedRuleNames are the checker's names, verbatim. A rule's number keys them.
var pinnedRuleNames = map[int]string{
	1:  "idle in combat >10 s",
	2:  "healer not healing <40% ally >4 s",
	3:  "tank without aggro: add on non-tank >5 s",
	4:  "standing in damaging AoE >3 s",
	5:  "stuck >30 s while group moves",
	6:  "eat/drink while group in combat",
	8:  "buff flip-flop (>3 casts / 60 s / target)",
	9:  "fall/drown death",
	10: "power waste at 100% in combat >30 s",
	11: "cast started but nothing lands while mana rises >10 s (cast-cancel signal)",
	13: "healer under 30% mana out of combat >20 s, no drink 15 s, group not moving",
	14: "member >40 yd from the group centroid >20 s in combat",
	15: "missed decurse: removable debuff >8 s with an able dispeller alive, in range, not casting",
	16: "trash tactics: must-interrupt cast completed with an interrupter free",
	17: "trash tactics: crowd control tried on an immune mob",
	18: "trash tactics: listed dispellable debuff left on too long",
	19: "trash tactics: lower-priority mob killed before a higher one",
}

// TestRuleNamesPinned pins every name: rule 14 lost its row once and rule 14 still reads ruleNames[14].
func TestRuleNamesPinned(t *testing.T) {
	for n, want := range pinnedRuleNames {
		if got := ruleNames[n]; got != want {
			t.Errorf("rule %d name %q, want %q", n, got, want)
		}
	}
	for n := range ruleNames {
		if _, ok := pinnedRuleNames[n]; !ok {
			t.Errorf("rule %d has a name no test pins: %q", n, ruleNames[n])
		}
	}
}

// TestRegistryRulesNamed checks every registered rule carries a non-empty name from the table and that its number
// agrees with that name's row.
func TestRegistryRulesNamed(t *testing.T) {
	tac := tacticsFile()
	all := append(FrameRules(), EventRules()...)
	all = append(all, TacticsRules(tac)...)
	for _, rule := range all {
		if rule.Name == "" {
			t.Errorf("rule %d is registered with an empty name", rule.Number)
		}
		if want := ruleNames[rule.Number]; rule.Name != want {
			t.Errorf("rule %d name %q, want %q", rule.Number, rule.Name, want)
		}
		if rule.Run == nil {
			t.Errorf("rule %d has no judge", rule.Number)
		}
	}
	// The four groups' own orders are the order the command sorts, so pin them too.
	wantFrame := []int{3, 5, 6, 9, 10, 13, 14}
	wantEvent := []int{1, 2, 4, 8, 11, 15}
	wantTactics := []int{16, 17, 18, 19}
	if got := ruleNumbers(FrameRules()); !reflect.DeepEqual(got, wantFrame) {
		t.Errorf("frame rules %v, want %v", got, wantFrame)
	}
	if got := ruleNumbers(EventRules()); !reflect.DeepEqual(got, wantEvent) {
		t.Errorf("event rules %v, want %v", got, wantEvent)
	}
	if got := ruleNumbers(TacticsRules(tac)); !reflect.DeepEqual(got, wantTactics) {
		t.Errorf("tactics rules %v, want %v", got, wantTactics)
	}
}

// ruleNumbers is the rules' numbers in registry order.
func ruleNumbers(rules []Rule) []int {
	out := make([]int, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.Number)
	}
	return out
}
