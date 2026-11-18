package rough

// Translated from tests/test_live1.py: the methods that exercise the scoring modules only
// (rules.opportunity_seconds, rules._spell_ids + rules.evaluate, scenario.validate,
// report._repeats_text + report.write_card).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

const (
	l1Player = "0x000000000000064c"
	l1Bot    = "0x000000000000064e"
	l1Extra  = "0x000000000000064f"

	l1Tid2Tank = "priest-holy-pws-def-arms-2tank"
)

func l1Trial(t *testing.T, tid string) map[string]any {
	t.Helper()
	trial, err := scenario.LoadTrial("healers/" + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	return trial
}

func l1Ctx(trial map[string]any) *rules.Ctx {
	return &rules.Ctx{
		Guids:      map[string]any{"player": l1Player, "bot": l1Bot, "extras": []any{l1Extra}},
		Thresholds: map[string]any{"critical": 20.0, "low": 50.0, "medium": 70.0},
		Trial:      trial,
	}
}

// The real rep: opportunity was 0 s because the player's in_combat was always false.
func TestScoringChainTest_OpportunityNeedsThePlayerInCombat(t *testing.T) {
	trial := l1Trial(t, l1Tid2Tank)
	samples := fixtureRows(t, "live1", "samples_2tank_player_fight.jsonl")
	if len(samples) == 0 {
		t.Fatal("no samples")
	}
	for _, s := range samples {
		if pyjson.Truthy(s["in_combat"]) {
			t.Fatalf("the fixture already has an in-combat sample: %v", s)
		}
	}
	ctx := l1Ctx(trial)
	if got := rules.OpportunitySeconds(trial, samples, nil, ctx); got != 0.0 {
		t.Fatalf("opportunity without in-combat: got %v, want 0", got)
	}
	// what the fixed sampler writes while the player fights: in_combat true from fight_start
	fixed := make([]rules.Row, 0, len(samples))
	for _, s := range samples {
		row := cloneRow(s)
		tv, _ := pyjson.Float(row["t"])
		row["in_combat"] = tv >= 8.4
		fixed = append(fixed, row)
	}
	if got := rules.OpportunitySeconds(trial, fixed, nil, ctx); got <= 0.0 {
		t.Fatalf("opportunity with in-combat: got %v, want > 0", got)
	}
}

func TestGovernorRescueTest_CapIsValidatedInTheTrialFile(t *testing.T) {
	trial := l1Trial(t, "priest-holy-pws-def-arms")
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("the trial does not validate: %v", errs)
	}
	sit := obj(t, trial["situation"])
	sit["max_rescues_per_min"] = int64(12)
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("max_rescues_per_min 12 was rejected: %v", errs)
	}
	sit["max_rescues_per_min"] = int64(0)
	found := false
	for _, e := range scenario.Validate(trial) {
		if strings.Contains(e, "max_rescues_per_min") {
			found = true
		}
	}
	if !found {
		t.Fatal("max_rescues_per_min 0 was accepted")
	}
}

// Bug 6 (verified, not a bug): the rule's Power Word: Shield ids and the cast rows agree.
func TestSpellIdsTest_EveryRealShieldCastCarriesAnIDTheRuleMatches(t *testing.T) {
	trial := l1Trial(t, l1Tid2Tank)
	rule := obj(t, list(t, trial["rules"])[0])
	casts := fixtureRows(t, "live1", "events_2tank_pws_casts.jsonl")
	if len(casts) != 3 {
		t.Fatalf("want 3 casts, got %d", len(casts))
	}
	where := obj(t, obj(t, rule["check"])["where"])
	ids := rules.SpellIDs(where)
	ranks := []int64{17, 592, 600, 3747, 6065, 6066, 10898, 10899, 10900, 10901}
	for _, rank := range ranks {
		if !ids[pyjson.Str(rank)] {
			t.Fatalf("the rule does not match shield rank %d", rank)
		}
	}
	for _, c := range casts {
		id, err := pyjson.PyInt(c["spell_id"])
		if err != nil || !ids[pyjson.Str(id)] {
			t.Fatalf("cast %v is not matched by the rule", c)
		}
	}
	samples := append(fixtureRows(t, "live1", "samples_2tank_bot_near_pws.jsonl"),
		fixtureRows(t, "live1", "samples_2tank_player_fight.jsonl")...)
	ctx := l1Ctx(trial)
	out := rules.Evaluate(rule, casts, samples, nil, ctx)
	// none went to the warrior: the shields were Tira's own and Steelwind's, all rank 6
	assertEqual(t, int64(0), out.Value)
	if len(out.Evidence) == 0 {
		t.Fatal("no evidence")
	}
	parts := strings.Split(out.Evidence[0], ",")
	if len(parts) < 2 || strings.TrimSpace(parts[1]) != "3 same-spell casts on other targets" {
		t.Fatalf("evidence: %q", out.Evidence[0])
	}
	dsts := map[string]bool{}
	for _, c := range casts {
		dsts[pyjson.Str(c["dst"])] = true
	}
	if len(dsts) != 2 || !dsts["Tira"] || !dsts["Steelwind"] {
		t.Fatalf("cast targets: %v", dsts)
	}
}

// Bug 5: the card printed the --repeats flag (0), not the repeats that ran.
func TestCardRepeatsTest_RepeatsThatRanAreCounted(t *testing.T) {
	run := obj(t, fixtureJSON(t, "live1", "RUN_2tank.json"))
	assertEqual(t, int64(0), obj(t, run["args"])["repeats"])
	tmp := t.TempDir()
	tid := l1Tid2Tank
	for _, rep := range []string{"rep1", "rep2"} {
		if err := os.MkdirAll(filepath.Join(tmp, tid, rep), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := report.RepeatsText(run, tmp, []map[string]any{{"trial": tid}}); got != "2" {
		t.Fatalf("repeats text: got %q, want \"2\"", got)
	}
	writeJSON(t, filepath.Join(tmp, "RUN.json"), run)
	if _, err := report.WriteCard(tmp, []map[string]any{{"trial": tid, "verdicts": []any{}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	card := string(data)
	if !strings.Contains(card, "repeats 2.") {
		t.Fatalf("card does not print the repeats that ran:\n%s", card)
	}
	if strings.Contains(card, "repeats 0") {
		t.Fatalf("card printed the --repeats flag:\n%s", card)
	}
}

func TestCardRepeatsTest_TheFlagIsShownWhenNothingRanYet(t *testing.T) {
	tmp := t.TempDir()
	run := map[string]any{"args": map[string]any{"repeats": int64(3)}}
	if got := report.RepeatsText(run, tmp, nil); got != "3" {
		t.Fatalf("repeats text: got %q, want \"3\"", got)
	}
}
