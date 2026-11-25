package edge

// Tests for the round-2 Innervate trial (priest-holy-low-mana-innervate-ally): the staging that
// lets the fight produce a real opportunity, and the three verdicts its low-mana rule must be able
// to reach. The round-1 trial (priest-holy-low-mana-innervate) stays as the Python bench defines
// it; this one adds the target's own mana to the rule, so a cast on a full-mana target or on the
// druid itself is not a pass, and a fight where no ally dropped low enough is INCONCLUSIVE instead
// of a FAIL decided from an empty opportunity.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// TestEdgeInnervateAllyTrialRules pins the staging and the rule wiring of the round-2 trial: a
// level 60 Holy priest hire at 15 percent mana as `bot`, one Balance druid as `extra1` with no gear
// block (the Forever overlay carries no druid kit, and a gear block would make the extra's setup row
// SKIPPED), a 90 s fight, and the target's own mana under 35 percent at the cast as the pass line.
func TestEdgeInnervateAllyTrialRules(t *testing.T) {
	const tid = "priest-holy-low-mana-innervate-ally"
	trial, err := scenario.LoadTrial("edge/" + tid + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", tid, err)
	}
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("%s: %v", tid, errs)
	}
	bot := obj(t, trial["bot"])
	assertEqual(t, "priest", pyjson.Str(bot["class"]))
	assertEqual(t, "holy", pyjson.Str(bot["spec"]))
	assertEqual(t, "healer", pyjson.Str(bot["role"]))
	if got := num(t, bot["mana_pct"]); got != 15 {
		t.Fatalf("%s: bot mana_pct %v, want 15 (under the 25 percent Innervate threshold)", tid, got)
	}
	extras := list(t, trial["extra_bots"])
	if len(extras) != 1 {
		t.Fatalf("%s: %d extra_bots, want 1", tid, len(extras))
	}
	druid := obj(t, extras[0])
	assertEqual(t, "druid", pyjson.Str(druid["class"]))
	assertEqual(t, "balance", pyjson.Str(druid["spec"]))
	assertEqual(t, "dps", pyjson.Str(druid["role"]))
	// No gear block: the overlay has no druid kit, and the extra's presence must not hinge on one.
	if gear := druid["gear"]; pyjson.Truthy(gear) {
		t.Fatalf("%s: the druid extra carries gear %v; the overlay has no druid kit, so the setup row would be SKIPPED", tid, gear)
	}
	if dur := num(t, trial["duration_s"]); dur < 90 {
		t.Fatalf("%s: duration_s %v, want at least 90 s (five seconds of low mana plus a channelled Innervate)", tid, dur)
	}
	if mv := num(t, trial["min_valid_s"]); mv < 60 {
		t.Fatalf("%s: min_valid_s %v, want at least 60 s", tid, mv)
	}

	opp := obj(t, trial["opportunity"])
	assertEqual(t, "$bot", pyjson.Str(opp["unit"]))
	assertEqual(t, 35.0, num(t, opp["power_lt"]))

	low := etRuleByID(t, trial, "DRU-BAL-INNERVATE-LOWMANA-TARGET")
	check := obj(t, low["check"])
	where := obj(t, check["where"])
	assertEqual(t, "count", pyjson.Str(check["metric"]))
	assertEqual(t, "cast", pyjson.Str(where["kind"]))
	assertEqual(t, "$extra1", pyjson.Str(where["src"]))
	assertEqual(t, "$bot", pyjson.Str(where["dst"]))
	assertEqual(t, "Innervate", pyjson.Str(where["spell"]))
	assertEqual(t, 35.0, num(t, where["dst_power_lt"]))
	assertEqual(t, ">= 1", pyjson.Str(low["pass_if"]))
	assertEqual(t, 5.0, num(t, low["min_opportunity_s"]))

	notSelf := etRuleByID(t, trial, "DRU-BAL-INNERVATE-NOT-SELF-TARGET")
	nw := obj(t, obj(t, notSelf["check"])["where"])
	assertEqual(t, "$extra1", pyjson.Str(nw["src"]))
	assertEqual(t, "$extra1", pyjson.Str(nw["dst"]))
	assertEqual(t, "== 0", pyjson.Str(notSelf["pass_if"]))

	acted := etRuleByID(t, trial, "PRI-LOWMANA-ACTED")
	aw := obj(t, obj(t, acted["check"])["where"])
	assertEqual(t, "$bot", pyjson.Str(aw["src"]))
	assertEqual(t, ">= 3", pyjson.Str(acted["pass_if"]))
	assertEqual(t, 5.0, num(t, acted["min_opportunity_s"]))
}

// innervateSamples is the round-2 trial's 1 Hz fight: the bot's mana bar is whatever the case
// gives (power over a 1000 pool, so 200 is 20 percent and 900 is 90 percent).
func innervateSamples(secs int, botPower int64) []any {
	return etSeries(secs, etSeriesOpts{BotPower: etConstPower(botPower)})
}

// TestEdgeInnervateAllyVerdicts walks the low-mana rule through the three verdicts it must reach,
// on the same trial, from recordings only: a cast on a low ally PASSes, a cast on someone else
// FAILs while an ally was low, and a fight where no ally dropped under the limit is INCONCLUSIVE.
func TestEdgeInnervateAllyVerdicts(t *testing.T) {
	const tid = "priest-holy-low-mana-innervate-ally"
	const rid = "DRU-BAL-INNERVATE-LOWMANA-TARGET"
	const secs = 90
	cases := []struct {
		name     string
		power    int64
		events   []any
		verdict  string
		wantText string
	}{
		{
			name:  "innervate on the 20 percent healer",
			power: 200,
			events: []any{
				etEvent(t, 20, "cast", etExtra1, etBot, "Innervate", 0),
			},
			verdict: "PASS",
		},
		{
			name:  "innervate on the warrior while the healer sits at 20 percent",
			power: 200,
			events: []any{
				etEvent(t, 20, "cast", etExtra1, etPlayer, "Innervate", 0),
			},
			verdict: "FAIL",
		},
		{
			name:  "no low ally: the healer never drops under 35 percent",
			power: 900,
			events: []any{
				etEvent(t, 20, "cast", etExtra1, etBot, "Innervate", 0),
			},
			verdict:  "INCONCLUSIVE",
			wantText: "opportunity 0 s < 5 s",
		},
		{
			name:  "no cast at all while the healer sits at 20 percent",
			power: 200,
			events: []any{
				etEvent(t, 20, "cast", etBot, etPlayer, "Flash Heal", 500),
			},
			verdict: "FAIL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := etRules(t, tid, tc.events, innervateSamples(secs, tc.power), secs, nil)
			row := rules[rid]
			if got := pyjson.Str(row["verdict"]); got != tc.verdict {
				t.Fatalf("verdict %s, want %s (%v)", got, tc.verdict, row["evidence"])
			}
			if tc.wantText != "" && pyjson.Str(row["reason"]) != tc.wantText {
				t.Fatalf("reason %q, want %q", pyjson.Str(row["reason"]), tc.wantText)
			}
		})
	}
}

// TestEdgeInnervateAllySelfCastFails: an Innervate the druid aims at itself never satisfies the
// low-mana rule (its dst is not the ally) and trips the not-self companion rule.
func TestEdgeInnervateAllySelfCastFails(t *testing.T) {
	const tid = "priest-holy-low-mana-innervate-ally"
	const secs = 90
	events := []any{etEvent(t, 20, "cast", etExtra1, etExtra1, "Innervate", 0)}
	rules := etRules(t, tid, events, innervateSamples(secs, 200), secs, nil)
	if got := pyjson.Str(rules["DRU-BAL-INNERVATE-LOWMANA-TARGET"]["verdict"]); got != "FAIL" {
		t.Fatalf("the low-mana rule is %s on a self-cast, want FAIL", got)
	}
	if got := pyjson.Str(rules["DRU-BAL-INNERVATE-NOT-SELF-TARGET"]["verdict"]); got != "FAIL" {
		t.Fatalf("the not-self rule is %s on a self-cast, want FAIL", got)
	}
}

// TestEdgeInnervateAllyHealerActedPrecondition: the priest's own cast count is the evidence the
// fight happened; with the healer low but silent it FAILs, and the same guard keeps a fight with no
// opportunity from failing it.
func TestEdgeInnervateAllyHealerActedPrecondition(t *testing.T) {
	const tid = "priest-holy-low-mana-innervate-ally"
	const rid = "PRI-LOWMANA-ACTED"
	const secs = 90
	var casts []any
	for i := range 3 {
		casts = append(casts, etEvent(t, 10+float64(i)*5, "cast", etBot, etPlayer, "Flash Heal", 500))
	}
	if got := etVerdict(t, tid, rid, casts, innervateSamples(secs, 200), secs, nil); got != "PASS" {
		t.Fatalf("three healer casts: %s, want PASS", got)
	}
	if got := etVerdict(t, tid, rid, casts[:2], innervateSamples(secs, 200), secs, nil); got != "FAIL" {
		t.Fatalf("two healer casts: %s, want FAIL", got)
	}
	if got := etVerdict(t, tid, rid, nil, innervateSamples(secs, 900), secs, nil); got != "INCONCLUSIVE" {
		t.Fatalf("no casts and no low mana: %s, want INCONCLUSIVE", got)
	}
}
