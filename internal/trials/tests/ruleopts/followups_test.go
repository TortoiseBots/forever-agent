package ruleopts

// Translated from the Python bench's tests/test_followups.py. Only the cases that exercise the
// scoring modules are here; the run/teardown/pacing/ladder/situation cases need the live runner
// and are listed in the port report.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// PaladinHealLatencySituationTest: it hit the rescue cap on every rep (even the ladder's step 3),
// so it never scored. The ladder/mobs half of this class needs trl.live (situations, ladder).
func TestPaladinHealLatencySituationTest_BothHealLatencyTrialsMakeTheChance(t *testing.T) {
	load := func(name string) map[string]any {
		trial := scenario.FindTrialByID(name)
		if trial == nil {
			t.Fatalf("trial %s not found", name)
		}
		return trial
	}
	pal := load("paladin-holy-heal-latency")
	pri := load("priest-holy-heal-latency")
	if errs := scenario.Validate(pal); len(errs) != 0 {
		t.Fatalf("validate(paladin) = %v; want no errors", errs)
	}
	// P3b: the paladin keeps its lighter shape, and both make the trigger fall happen
	palSit := pyjson.ObjectOrEmpty(pal["situation"])
	mobs := pyjson.ObjectOrEmpty(palSit["mobs"])
	if !jsonEqual(int64(-4), mobs["level_delta"]) {
		t.Fatalf("paladin level_delta = %v; want -4", mobs["level_delta"])
	}
	for _, sit := range []map[string]any{palSit, pyjson.ObjectOrEmpty(pri["situation"])} {
		if !jsonEqual(int64(1), pyjson.ObjectOrEmpty(sit["mobs"])["count"]) {
			t.Fatalf("mobs count = %v; want 1", pyjson.ObjectOrEmpty(sit["mobs"])["count"])
		}
		if !jsonEqual([]any{int64(40), int64(80)}, sit["hp_band"]) {
			t.Fatalf("hp_band = %v; want [40 80]", sit["hp_band"])
		}
		if !jsonEqual(int64(45), sit["top_up_to_pct"]) {
			t.Fatalf("top_up_to_pct = %v; want 45", sit["top_up_to_pct"])
		}
		rescues, ok := pyjson.Float(sit["max_rescues_per_min"])
		if !ok || rescues < 12 {
			t.Fatalf("max_rescues_per_min = %v; want >= 12", sit["max_rescues_per_min"])
		}
	}
}

// MarkedHasCcTest: DEFAULT_CC_AURAS had Hunter's Mark and other non-CC ids and only rank 1 of
// Polymorph, so a sheep from a mage above level 19 was never seen as CC.
const fuMarked = "0xf130000000000099"

func fuHasCC(auraIDs []any) bool {
	samples := []rules.Row{{"t": 5.0, "guid": fuMarked, "auras": auraIDs}}
	byGUID := rules.SampleRows(samples)
	ctx := &rules.Ctx{Guids: map[string]any{"marked": fuMarked}}
	return rules.Matches(map[string]any{"t": 5.0}, map[string]any{"marked_has_cc": true},
		byGUID, ctx, map[string]any{}, nil)
}

func TestMarkedHasCcTest_ARank4PolymorphCountsAsCc(t *testing.T) {
	if !fuHasCC([]any{int64(12826)}) {
		t.Fatal("rank 4 Polymorph not seen as CC")
	}
}

func TestMarkedHasCcTest_EveryPolymorphRankCounts(t *testing.T) {
	for _, id := range []int64{118, 12824, 12825, 12826} {
		if !fuHasCC([]any{id}) {
			t.Fatalf("Polymorph %d not seen as CC", id)
		}
	}
}

func TestMarkedHasCcTest_HuntersMarkIsNotCc(t *testing.T) {
	// the failing case before the fix: 14325 (Hunter's Mark rank 4) was in the list
	if fuHasCC([]any{int64(14325)}) {
		t.Fatal("Hunter's Mark counted as CC")
	}
}

func TestMarkedHasCcTest_TheOldNonCcIDsAreGone(t *testing.T) {
	for _, id := range []int64{2108, 9035, 14325, 17826, 18651, 10956} {
		if rules.DefaultCCAuras[id] {
			t.Fatalf("%d is still in DEFAULT_CC_AURAS", id)
		}
	}
}

func TestMarkedHasCcTest_EveryIDIsACrowdControlSpellByNameAndEveryRankIsThere(t *testing.T) {
	names := []string{"Polymorph", "Sap", "Hibernate", "Freezing Trap Effect", "Shackle Undead", "Banish",
		"Seduction", "Gouge", "Scare Beast", "Repentance", "Sleep", "Blind"}
	doc, err := pyjson.Unmarshal(data.Read("spells.json"))
	if err != nil {
		t.Fatal(err)
	}
	byID := pyjson.ObjectOrEmpty(pyjson.ObjectOrEmpty(doc)["by_id"])
	have := map[string]map[string]bool{}
	for id := range rules.DefaultCCAuras {
		row := pyjson.ObjectOrEmpty(byID[pyjson.Str(id)])
		name := pyjson.Str(row["name"])
		matched := false
		for _, prefix := range names {
			if strings.HasPrefix(name, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%d: %q is not a crowd-control spell name", id, name)
		}
		if have[name] == nil {
			have[name] = map[string]bool{}
		}
		have[name][pyjson.Str(row["rank"])] = true
	}
	wantRanks := map[string]int{"Polymorph": 4, "Sap": 3, "Hibernate": 3, "Freezing Trap Effect": 3,
		"Shackle Undead": 3, "Banish": 2, "Gouge": 5, "Scare Beast": 3, "Sleep": 4}
	for name, ranks := range wantRanks {
		if len(have[name]) != ranks {
			t.Fatalf("%s has %d ranks; want %d", name, len(have[name]), ranks)
		}
	}
}
