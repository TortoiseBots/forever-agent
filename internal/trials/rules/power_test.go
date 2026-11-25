package rules

// Tests for the power keys: the target's own bar in a where clause (dst_power_lt) and the unit's
// bar in the trial's opportunity block (power_lt/power_gt). Both read samples.jsonl's power and
// max_power, the same fields src_power_lt has always read.

import "testing"

func innervateRow(t float64, src, dst string) Row {
	return Row{"kind": "cast", "spell": "Innervate", "spell_id": int64(29166),
		"src": "Dru", "src_guid": src, "dst": "Pri", "dst_guid": dst, "t": t}
}

// TestMatchesDstPowerLt pins the target-side mana gate: the cast matches only while the target's
// own bar reads under the limit, and a target with no bar or no sample never matches (an unknown
// bar is not a low one).
func TestMatchesDstPowerLt(t *testing.T) {
	samples := []Row{
		{"t": 10.0, "guid": "0x02", "power": 20.0, "max_power": 100.0},
		{"t": 20.0, "guid": "0x02", "power": 80.0, "max_power": 100.0},
		{"t": 10.0, "guid": "0x03", "power": 0.0, "max_power": 0.0}, // no mana pool
	}
	byGUID := SampleRows(samples)
	cases := []struct {
		name  string
		row   Row
		where Row
		want  bool
	}{
		{"under the limit", innervateRow(10.5, "0x03", "0x02"), Row{"kind": "cast", "dst_power_lt": 35.0}, true},
		{"over the limit", innervateRow(20.5, "0x03", "0x02"), Row{"kind": "cast", "dst_power_lt": 35.0}, false},
		{"exactly the limit is not under it", Row{"kind": "cast", "dst_guid": "0x02", "t": 10.0},
			Row{"kind": "cast", "dst_power_lt": 20.0}, false},
		{"no target sample", innervateRow(40.0, "0x03", "0x02"), Row{"kind": "cast", "dst_power_lt": 35.0}, false},
		{"target has no bar", innervateRow(10.5, "0x02", "0x03"), Row{"kind": "cast", "dst_power_lt": 35.0}, false},
		{"no dst_power_lt never filters", innervateRow(20.5, "0x03", "0x02"), Row{"kind": "cast"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.row, tc.where, byGUID, &Ctx{}, nil, nil); got != tc.want {
				t.Fatalf("match %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMatchesDstPowerLtThresholdAlias: the key reads the same $critical/$low/$medium aliases
// src_power_lt does, so a rule can follow the server's configured thresholds.
func TestMatchesDstPowerLtThresholdAlias(t *testing.T) {
	byGUID := SampleRows([]Row{{"t": 10.0, "guid": "0x02", "power": 20.0, "max_power": 100.0}})
	ctx := &Ctx{Thresholds: map[string]any{"low": 50.0}}
	row := innervateRow(10.5, "0x03", "0x02")
	if !Matches(row, Row{"kind": "cast", "dst_power_lt": "$low"}, byGUID, ctx, nil, nil) {
		t.Fatal("20%% mana does not pass $low (50)")
	}
	ctx.Thresholds["low"] = 10.0
	if Matches(row, Row{"kind": "cast", "dst_power_lt": "$low"}, byGUID, ctx, nil, nil) {
		t.Fatal("20%% mana passes $low (10)")
	}
}

// TestOpportunityPowerSeconds pins the opportunity block's power keys: the window is the time the
// unit's own bar read under (or over) the limit, and a sample without a bar earns nothing.
func TestOpportunityPowerSeconds(t *testing.T) {
	guids := map[string]any{"bot": "0x02"}
	ctx := &Ctx{Guids: guids}
	trial := func(block map[string]any) map[string]any { return map[string]any{"opportunity": block} }
	// Each interval is judged by the sample that closes it: 0..10 s at 20 % mana, 10..20 s at 80 %.
	samples := []Row{
		{"t": 0.0, "guid": "0x02", "power": 20.0, "max_power": 100.0},
		{"t": 10.0, "guid": "0x02", "power": 20.0, "max_power": 100.0},
		{"t": 20.0, "guid": "0x02", "power": 80.0, "max_power": 100.0},
	}
	if got := OpportunitySeconds(trial(map[string]any{"unit": "$bot", "power_lt": 35.0}), samples, nil, ctx); got != 10 {
		t.Fatalf("power_lt window %.1f s, want 10", got)
	}
	if got := OpportunitySeconds(trial(map[string]any{"unit": "$bot", "power_lt": 50.0}), samples, nil, ctx); got != 10 {
		t.Fatalf("the 80 %% sample must end the window: %.1f s, want 10", got)
	}
	if got := OpportunitySeconds(trial(map[string]any{"unit": "$bot", "power_gt": 50.0}), samples, nil, ctx); got != 10 {
		t.Fatalf("power_gt window %.1f s, want the second half's 10", got)
	}
	// An hp-only block reads no power: the mana bar is untouched by it.
	if got := OpportunitySeconds(trial(map[string]any{"unit": "$bot", "hp_lt": 50.0}), samples, nil, ctx); got != 0 {
		t.Fatalf("hp_lt on samples without hp: %.1f s, want 0", got)
	}
	// A sample with no bar (max_power 0) holds no opportunity, so the window counts nothing.
	bare := []Row{
		{"t": 0.0, "guid": "0x02", "power": 30.0, "max_power": 100.0},
		{"t": 10.0, "guid": "0x02", "power": 0.0, "max_power": 0.0},
	}
	if got := OpportunitySeconds(trial(map[string]any{"unit": "$bot", "power_lt": 35.0}), bare, nil, ctx); got != 0 {
		t.Fatalf("a barless sample earned %.1f s, want 0", got)
	}
	// The excluded overlap is cut from the window, as for every other key.
	excluded := [][2]float64{{2.0, 4.0}}
	if got := OpportunitySeconds(trial(map[string]any{"unit": "$bot", "power_lt": 35.0}), samples, excluded, ctx); got != 8 {
		t.Fatalf("excluded window: %.1f s, want 8", got)
	}
}
