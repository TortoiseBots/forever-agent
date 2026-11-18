package differential

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

func bigValue(t *testing.T, literal string) any {
	t.Helper()
	doc, err := pyjson.Unmarshal([]byte(literal))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func latencyRule(id string, trigger, response map[string]any, within float64, passIf string, extra map[string]any) map[string]any {
	check := map[string]any{
		"metric": "latency", "trigger": trigger, "response": response,
		"within_s": within,
	}
	for k, v := range extra {
		check[k] = v
	}
	return map[string]any{"id": id, "check": check, "pass_if": passIf,
		"source": "differential", "desc": id}
}

func runCases(t *testing.T) []runCase {
	castTrigger := map[string]any{"kind": "cast", "spell_id": int64(17)}
	bigGUID := "17379390989916241921"
	guidThreshold := "9223372036854775808"
	return []runCase{
		{
			// B1: two raw rows differ only in crit, so they survive the raw dedupe and become
			// two identical cast rows at t=5. Python compares rows by identity: each trigger is
			// the other's cooldown responder, both are skipped, INCONCLUSIVE. Content equality
			// would keep them and PASS.
			name: "b1_twin_rows_are_two_rows",
			trial: newTrial(latencyRule("B1", castTrigger, castTrigger, 2, ">= 1",
				map[string]any{"debounce_responder_s": int64(10)})),
			reps: []repCase{{raw: []any{
				rawCast(1, "2026-01-01T00:00:05", 17, false),
				rawCast(2, "2026-01-01T00:00:05", 17, true),
			}}},
		},
		{
			// B3: a decimal GUID at or above 2^63 becomes Python's 16-digit hex form.
			name:  "b3_guid_above_int64",
			trial: newTrial(metricRule("B3", "count", map[string]any{"where": map[string]any{"kind": "cast", "spell_id": int64(17)}}, ">= 1")),
			reps: []repCase{{
				setup: filledSetup(map[string]any{"guids": map[string]any{"player": bigGUID, "bot": "0x2"}}),
				raw: []any{map[string]any{
					"seq": int64(1), "time": "2026-01-01T00:00:05", "kind": "spell_go", "spell_id": int64(17),
					"source": map[string]any{"name": "Bot", "guid": "0x2"},
					"target": map[string]any{"name": "Player", "guid": bigGUID},
				}},
			}},
		},
		{
			// B3 boundary: 2^63 exactly is still a valid 64-bit guid pattern after hex/zero pad.
			name:  "b3_guid_exactly_2_63",
			trial: newTrial(metricRule("B3", "count", map[string]any{"where": map[string]any{"kind": "cast", "spell_id": int64(17)}}, ">= 1")),
			reps: []repCase{{
				setup: filledSetup(map[string]any{"guids": map[string]any{"player": guidThreshold, "bot": "0x2"}}),
				raw: []any{map[string]any{
					"seq": int64(1), "time": "2026-01-01T00:00:05", "kind": "spell_go", "spell_id": int64(17),
					"source": map[string]any{"name": "Bot", "guid": "0x2"},
					"target": map[string]any{"name": "Player", "guid": guidThreshold},
				}},
			}},
		},
		{
			// B5: a null line in events.jsonl makes the Python raise AttributeError; it must
			// never be dropped and scored.
			name:        "b5_events_null_line",
			trial:       newTrial(metricRule("B5", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps:        []repCase{{eventsText: []string{"null"}}},
			errorPython: "AttributeError: 'NoneType' object has no attribute 'get'",
			errorGo:     "events.jsonl:1: expected a JSON object, got null",
			errorStage:  "load",
		},
		{
			// With a raw trace the Python normalises over a null events line (its condition
			// ignores non-dict rows), so the bad recorded events file is never read.
			name:  "b5_events_null_with_raw_normalises",
			trial: newTrial(metricRule("B5", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{
				eventsText: []string{"null"},
				raw:        []any{rawCast(1, "2026-01-01T00:00:05", 17, false)},
			}},
		},
		{
			// But a seq row before the null line keeps the Python from normalising, and the
			// null row then raises: both sides must fail.
			name:        "b5_events_seq_then_null_with_raw_fails",
			errorPython: "AttributeError: 'NoneType' object has no attribute 'get'",
			errorGo:     "events.jsonl:2: expected a JSON object, got null",
			errorStage:  "load",
			trial:       newTrial(metricRule("B5", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{
				eventsText: []string{`{"t": 1, "kind": "cast", "seq": 1}`, "null"},
				raw:        []any{rawCast(1, "2026-01-01T00:00:05", 17, false)},
			}},
		},
		{
			name:        "b5_marks_number_line",
			errorPython: "AttributeError: 'int' object has no attribute 'get'",
			errorGo:     "marks.jsonl:1: expected a JSON object, got a number",
			errorStage:  "load",
			trial:       newTrial(metricRule("B5", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps:        []repCase{{marksText: []string{"123"}}},
		},
		{
			name:        "b5_samples_null_line",
			errorPython: "AttributeError: 'NoneType' object has no attribute 'get'",
			errorGo:     "samples.jsonl:1: expected a JSON object, got null",
			errorStage:  "load",
			trial:       newTrial(metricRule("B5", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps:        []repCase{{samplesText: []string{"null"}}},
		},
		{
			// B6: a list seq is unhashable in Python's dedupe key.
			name:        "b6_seq_list_unhashable",
			errorPython: "TypeError: unhashable type: 'list'",
			errorGo:     "unhashable seq a list",
			errorStage:  "load",
			trial:       newTrial(metricRule("B6", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{raw: []any{map[string]any{
				"seq": []any{int64(1)}, "time": "2026-01-01T00:00:05", "kind": "spell_go", "spell_id": int64(17),
				"source": map[string]any{"name": "Bot", "guid": "0x2"},
				"target": map[string]any{"name": "Player", "guid": "0x1"},
			}}}},
		},
		{
			// B4: trailing bytes after the snapshot's first JSON value are a JSONDecodeError.
			name:        "b4_snapshot_trailing_data",
			trialText:   `{"id":"t1","min_valid_s":0,"rules":[]} 123`,
			reps:        []repCase{{}},
			errorPython: "Extra data",
			errorGo:     "trailing data after the first JSON value",
			errorStage:  "load",
		},
		{
			// B4: an integer outside int64 keeps arbitrary precision (spell id and name).
			name:  "b4_big_spell_id_precision",
			trial: newTrial(metricRule("B4", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{raw: []any{map[string]any{
				"seq": int64(1), "time": "2026-01-01T00:00:05", "kind": "spell_go",
				"spell_id": bigValue(t, "123456789012345678901234567890"),
				"source":   map[string]any{"name": "Bot", "guid": "0x2"},
				"target":   map[string]any{"name": "Player", "guid": "0x1"},
			}}}},
		},
		{
			// Pre-scored repeats with float k/n: Python sums int(5.0) == 5 and keeps the raw
			// 5.0/10.0 in the detail rows.
			name: "edge_prescore_float_k_n",
			trial: newTrial(map[string]any{"id": "P", "pool": true, "pass_if": ">= 0.5",
				"source": "differential", "desc": "P",
				"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}),
			reps: []repCase{
				{noSetup: true, noMarks: true, verdicts: map[string]any{"rep": "rep1",
					"rules": []any{map[string]any{"id": "P", "verdict": "PASS",
						"value": 0.5, "k": 5.0, "n": 10.0}}}},
				{noSetup: true, noMarks: true, verdicts: map[string]any{"rep": "rep2",
					"rules": []any{map[string]any{"id": "P", "verdict": "PASS",
						"value": 0.5, "k": 1.0, "n": 2.0}}}},
			},
		},
		{
			name:  "edge_empty_events",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps:  []repCase{{eventsText: []string{}}},
		},
		{
			name:  "edge_one_event",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast", "spell_id": int64(17)}}, "== 1")),
			reps:  []repCase{{events: []any{castRow(1, 17)}}},
		},
		{
			// A tie below the pass line: 1 of 2 triggers answered is exactly 0.5.
			name: "edge_latency_exactly_half",
			trial: newTrial(latencyRule("T", map[string]any{"kind": "cast", "spell_id": int64(17)},
				map[string]any{"kind": "cast", "spell_id": int64(100)}, 2, ">= 0.5", nil)),
			reps: []repCase{{events: []any{
				castRow(1, 17), castRow(2, 100), castRow(5, 17),
			}}},
		},
		{
			name:  "edge_count_boundary_equal",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, "== 2")),
			reps:  []repCase{{events: []any{castRow(1, 17), castRow(2, 17)}}},
		},
		{
			name:  "edge_negative_times",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps:  []repCase{{noMarks: true, events: []any{castRow(-5, 17), castRow(2, 17)}}},
		},
		{
			// Identical raw rows with the same seq dedupe to one normalised event.
			name:  "edge_duplicate_raw_rows",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, "== 1")),
			reps:  []repCase{{raw: []any{rawCast(7, "2026-01-01T00:00:05", 17, false), rawCast(7, "2026-01-01T00:00:05", 17, false)}}},
		},
		{
			// int 5 and str "5" have different raw content: two rows survive, both seq int 5.
			name:  "edge_seq_int_vs_string",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, "== 2")),
			reps: []repCase{{raw: []any{
				rawCast(5, "2026-01-01T00:00:05", 17, false),
				func() map[string]any {
					row := rawCast(0, "2026-01-01T00:00:05", 17, false)
					row["seq"] = "5"
					return row
				}(),
			}}},
		},
		{
			name:  "edge_unicode_names",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{events: []any{func() map[string]any {
				row := castRow(1, 17)
				row["src"] = "Grüße"
				return row
			}()}}},
		},
		{
			name:  "edge_majority_of_three_reps",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{
				{events: []any{castRow(1, 17)}},
				{eventsText: []string{}},
				{events: []any{castRow(2, 17)}},
			},
		},
		{
			name:  "edge_no_rules",
			trial: newTrial(),
			reps:  []repCase{{events: []any{castRow(1, 17)}}},
		},
		{
			// Rule-level min_n 0 must not mask check.min_n (Python: 0 or 2 -> 2).
			name: "edge_rule_min_n_zero_masks_check",
			trial: newTrial(func() map[string]any {
				rule := metricRule("M", "count", map[string]any{
					"where": map[string]any{"kind": "cast"}, "min_n": int64(2),
				}, ">= 1")
				rule["min_n"] = int64(0)
				return rule
			}()),
			reps: []repCase{{events: []any{castRow(1, 17)}}},
		},
		{
			// min_alive_s 0 is falsy in Python: the 10 s floor still applies.
			name: "edge_rate_min_alive_zero_means_ten",
			trial: newTrial(metricRule("R", "rate", map[string]any{
				"where": map[string]any{"kind": "cast"}, "per": "mob_alive", "min_alive_s": int64(0),
			}, ">= 0")),
			reps: []repCase{{
				samples: []any{
					map[string]any{"t": 1.0, "unit": "mob:0xabc", "hp": int64(10)},
					map[string]any{"t": 2.0, "unit": "mob:0xabc", "hp": int64(10)},
					map[string]any{"t": 3.0, "unit": "mob:0xabc", "hp": int64(10)},
				},
				events: []any{castRow(1, 17)},
			}},
		},
		{
			// The `first` metric stores the row's own t: an integer stays 1 in the JSON.
			name: "edge_first_integer_t",
			trial: newTrial(metricRule("F", "first", map[string]any{
				"where": map[string]any{"kind": "cast", "spell_id": int64(17)},
			}, "<= 2")),
			reps: []repCase{{events: []any{func() map[string]any {
				row := castRow(1, 17)
				row["t"] = int64(1)
				return row
			}()}}},
		},
		{
			// B1: Python's json.loads accepts the NaN token; float(NaN) makes the
			// min_valid check false without an error, so both sides score the rep.
			name:      "b1_nan_token_in_snapshot",
			trialText: `{"id":"t1","min_valid_s":NaN,"rules":[{"id":"R","check":{"metric":"count","where":{"kind":"cast"}},"pass_if":">= 1","source":"differential","desc":"R"}]}`,
			reps:      []repCase{{events: []any{castRow(1, 17)}}},
		},
		{
			// B1: a NaN the Python json.dumps sees in verdicts.json (before trial.json)
			// raises allow_nan=False; Go returns the same class instead of panicking.
			name:  "b1_nan_value_in_prescore_errors",
			trial: newTrial(metricRule("R", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{noSetup: true, noMarks: true,
				verdictsText: `{"rep":"rep1","rules":[{"id":"R","verdict":"PASS","value":NaN,"k":1,"n":1}]}`}},
			errorPython: "Out of range float values are not JSON compliant",
			errorGo:     "not allowed with allow_nan=False",
			errorStage:  "write",
		},
		{
			// C2: +Infinity reaching trial.json: Python raises ValueError, Go returns an
			// error (ScoreTrialE/ScoreRunE), never a panic.
			name:  "c2_inf_value_in_prescore_errors",
			trial: newTrial(metricRule("R", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{noSetup: true, noMarks: true,
				verdictsText: `{"rep":"rep1","rules":[{"id":"R","verdict":"PASS","value":1e999,"k":1,"n":1}]}`}},
			errorPython: "Out of range float values are not JSON compliant",
			errorGo:     "not allowed with allow_nan=False",
			errorStage:  "write",
		},
		{
			// B2: rep-level int(min_n) is unguarded in Python; "abc" raises.
			name: "b2_rule_min_n_string_raises",
			trial: newTrial(func() map[string]any {
				rule := metricRule("R", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")
				rule["min_n"] = "abc"
				return rule
			}()),
			reps:        []repCase{{events: []any{castRow(1, 17)}}},
			errorPython: "invalid literal for int() with base 10: 'abc'",
			errorGo:     "min_n: pyjson: invalid literal for int()",
			errorStage:  "evaluate",
		},
		{
			// B2: the pooled path catches the same ValueError and uses 0.
			name: "b2_pooled_min_n_string_guarded",
			trial: newTrial(func() map[string]any {
				rule := metricRule("R", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")
				rule["pool"] = true
				rule["min_n"] = "abc"
				return rule
			}()),
			reps: []repCase{{events: []any{castRow(1, 17)}}},
		},
		{
			// B3: min_valid_s is float(...) with no guard.
			name:        "b3_min_valid_string_raises",
			trialText:   `{"id":"t1","min_valid_s":"abc","rules":[]}`,
			reps:        []repCase{{events: []any{castRow(1, 17)}}},
			errorPython: "could not convert string to float: 'abc'",
			errorGo:     "min_valid_s:",
			errorStage:  "evaluate",
		},
		{
			// B4: int(inf) raises OverflowError, which neither int() guard catches.
			name:  "b4_prescore_n_inf_raises",
			trial: newTrial(metricRule("R", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{noSetup: true, noMarks: true,
				verdictsText: `{"rep":"rep1","rules":[{"id":"R","verdict":"PASS","value":0.5,"k":1,"n":1e999}]}`}},
			errorPython: "OverflowError: cannot convert float infinity to integer",
			errorGo:     "cannot convert float infinity to integer",
			errorStage:  "pool",
		},
		{
			// B4: min_reps catches only TypeError/ValueError; int(inf) escapes.
			name:        "b4_min_reps_inf_raises",
			trialText:   `{"id":"t1","min_valid_s":0,"rules":[{"id":"R","check":{"metric":"count","where":{"kind":"cast"}},"pass_if":">= 1","source":"differential","desc":"R","min_reps":1e999}]}`,
			reps:        []repCase{{events: []any{castRow(1, 17)}}},
			errorPython: "OverflowError: cannot convert float infinity to integer",
			errorGo:     "min_reps",
			errorStage:  "pool",
		},
		{
			// B4: an inf in a raw row fails the normaliser's allow_nan=False dump before
			// its guarded int(seq) (Go order matches: the dump happens first).
			name:  "b4_raw_seq_inf_write_raises",
			trial: newTrial(metricRule("R", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")),
			reps: []repCase{{rawText: []string{
				`{"seq":1e999,"time":"2026-01-01T00:00:05","kind":"spell_go","spell_id":17,"source":{"name":"Bot","guid":"0x2"},"target":{"name":"Player","guid":"0x1"}}`,
			}}},
			errorPython: "Out of range float values are not JSON compliant",
			errorGo:     "not allowed with allow_nan=False",
			errorStage:  "normalise",
		},
		{
			// B4: min_reps beyond int64 is still a Python int: INCONCLUSIVE, not an
			// overflow. Go compares the exact digits.
			name:      "b4_min_reps_beyond_int64",
			trialText: `{"id":"t1","min_valid_s":0,"rules":[{"id":"R","check":{"metric":"count","where":{"kind":"cast"}},"pass_if":">= 1","source":"differential","desc":"R","min_reps":123456789012345678901234567890}]}`,
			reps:      []repCase{{events: []any{castRow(1, 17)}}},
		},
		{
			// B4: pooled min_n beyond int64 compares as a Python int: INCONCLUSIVE, not
			// an overflow.
			name: "b4_pooled_min_n_beyond_int64",
			trial: newTrial(func() map[string]any {
				rule := metricRule("R", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, ">= 1")
				rule["pool"] = true
				rule["min_n"] = bigValue(t, "123456789012345678901234567890")
				return rule
			}()),
			reps: []repCase{{events: []any{castRow(1, 17)}}},
		},
		{
			// B4: Python's int(text) / int(text, 16) underscore and sign rules.
			name:  "b4_norm_guid_edges",
			trial: newTrial(),
			reps: []repCase{{raw: []any{
				map[string]any{"seq": int64(1), "time": "2026-01-01T00:00:05", "kind": "spell_go", "spell_id": int64(17),
					"source": map[string]any{"name": "Bot", "guid": "0x-5"},
					"target": map[string]any{"name": "Player", "guid": "0x1"}},
				map[string]any{"seq": int64(2), "time": "2026-01-01T00:00:06", "kind": "spell_go", "spell_id": int64(17),
					"source": map[string]any{"name": "Bot", "guid": "1_0"},
					"target": map[string]any{"name": "Player", "guid": "0x1"}},
				map[string]any{"seq": int64(3), "time": "2026-01-01T00:00:07", "kind": "spell_go", "spell_id": int64(17),
					"source": map[string]any{"name": "Bot", "guid": "0x1_f"},
					"target": map[string]any{"name": "Player", "guid": "0x1"}},
				map[string]any{"seq": int64(4), "time": "2026-01-01T00:00:08", "kind": "spell_go", "spell_id": int64(17),
					"source": map[string]any{"name": "Bot", "guid": "0x+5"},
					"target": map[string]any{"name": "Player", "guid": "0x1"}},
			}}},
		},
		{
			// C3: a spell id beyond int64 in a where filter: Python's set holds the
			// arbitrary-precision int and matches; Go keys SpellIDs by int() digits.
			name: "c3_spell_id_beyond_int64",
			trial: newTrial(metricRule("C3", "count", map[string]any{
				"where": map[string]any{"kind": "cast", "spell_id": bigValue(t, "123456789012345678901234567890")},
			}, ">= 1")),
			reps: []repCase{{events: []any{func() map[string]any {
				row := castRow(1, 17)
				row["spell_id"] = bigValue(t, "123456789012345678901234567890")
				return row
			}()}}},
		},
		{
			name:  "edge_excluded_span",
			trial: newTrial(metricRule("E", "count", map[string]any{"where": map[string]any{"kind": "cast"}}, "== 1")),
			reps: []repCase{{events: []any{
				map[string]any{"kind": "excluded", "from": 1.0, "to": 3.0, "t": 1.0},
				castRow(2, 17), castRow(4, 17),
			}}},
		},
	}
}

func ruleCases(t *testing.T) []ruleCase {
	emptyCtx := map[string]any{"trial": map[string]any{}, "guids": map[string]any{}, "thresholds": map[string]any{}}
	walkCtx := func(report map[string]any) map[string]any {
		return map[string]any{"trial": map[string]any{}, "guids": map[string]any{},
			"thresholds": map[string]any{}, "walk_report": report}
	}
	walkRule := func() map[string]any {
		return map[string]any{"id": "W", "check": map[string]any{"metric": "walk_metric", "name": "walk_to_target"},
			"pass_if": "<= 0.5", "source": "differential", "desc": "W"}
	}
	return []ruleCase{
		{name: "b2_walk_n_float", rule: walkRule(), ctx: walkCtx(map[string]any{
			"walk_to_target": map[string]any{"k": int64(2), "n": 5.0}}), rows: []any{}, samples: []any{}, marks: []any{}},
		{name: "b2_walk_n_truncates", rule: walkRule(), ctx: walkCtx(map[string]any{
			"walk_to_target": map[string]any{"k": int64(2), "n": 5.5}}), rows: []any{}, samples: []any{}, marks: []any{}},
		{name: "b2_walk_n_string", rule: walkRule(), ctx: walkCtx(map[string]any{
			"walk_to_target": map[string]any{"k": int64(2), "n": "5"}}), rows: []any{}, samples: []any{}, marks: []any{}},
		{name: "b2_walk_mean_null_raises", rule: walkRule(), ctx: walkCtx(map[string]any{
			"walk_to_target": map[string]any{"n": 5.0, "mean": nil}}), rows: []any{}, samples: []any{}, marks: []any{},
			errorPython: "float() argument must be a string or a real number, not 'NoneType'", errorGo: "mean None is not a number"},
		{name: "b2_walk_mean_string_raises", rule: walkRule(), ctx: walkCtx(map[string]any{
			"walk_to_target": map[string]any{"n": 5.0, "mean": "x"}}), rows: []any{}, samples: []any{}, marks: []any{},
			errorPython: "could not convert string to float: 'x'", errorGo: "mean x is not a number"},
		{name: "b2_walk_k_null_raises", rule: walkRule(), ctx: walkCtx(map[string]any{
			"walk_to_target": map[string]any{"n": 5.0, "k": nil}}), rows: []any{}, samples: []any{}, marks: []any{},
			errorPython: "float() argument must be a string or a real number, not 'NoneType'", errorGo: "k None is not a number"},
		{name: "b2_walk_missing_n", rule: walkRule(), ctx: walkCtx(map[string]any{
			"walk_to_target": map[string]any{"k": int64(2)}}), rows: []any{}, samples: []any{}, marks: []any{}},
		{
			// The missing alias is "" in Python, so a row with no src_guid matches $player.
			name: "b2b_lower_of_nil_is_empty",
			rule: map[string]any{"id": "L", "check": map[string]any{"metric": "count",
				"where": map[string]any{"kind": "cast", "src": "$player"}},
				"pass_if": ">= 1", "source": "differential", "desc": "L"},
			rows: []any{func() map[string]any {
				row := castRow(1, 17)
				row["src_guid"] = nil
				return row
			}()},
			samples: []any{}, marks: []any{},
			ctx: map[string]any{"trial": map[string]any{}, "guids": map[string]any{}, "thresholds": map[string]any{}},
		},
		{
			// float("0") == 0.0: a truthy string zero disables the 10 s floor.
			name: "edge_rate_min_alive_string_zero",
			rule: metricRule("R", "rate", map[string]any{
				"where": map[string]any{"kind": "cast"}, "per": "mob_alive", "min_alive_s": "0",
			}, ">= 0"),
			rows: []any{castRow(1, 17)},
			samples: []any{
				map[string]any{"t": 1.0, "unit": "mob:0xabc", "hp": int64(10)},
				map[string]any{"t": 2.0, "unit": "mob:0xabc", "hp": int64(10)},
				map[string]any{"t": 3.0, "unit": "mob:0xabc", "hp": int64(10)},
			},
			marks: []any{}, ctx: emptyCtx,
		},
		{
			name: "edge_unknown_metric",
			rule: map[string]any{"id": "U", "check": map[string]any{"metric": "frobnicate"},
				"pass_if": ">= 1", "source": "differential", "desc": "U"},
			rows: []any{}, samples: []any{}, marks: []any{}, ctx: emptyCtx,
		},
		{
			name: "edge_latency_no_triggers",
			rule: latencyRule("N", map[string]any{"kind": "cast", "spell_id": int64(17)},
				map[string]any{"kind": "cast", "spell_id": int64(100)}, 2, ">= 1", nil),
			rows: []any{}, samples: []any{}, marks: []any{}, ctx: emptyCtx,
		},
		{
			// C1: int(5e18) is the int 5000000000000000000, not a big integer.
			name: "c1_walk_n_5e18",
			rule: walkRule(), ctx: walkCtx(map[string]any{
				"walk_to_target": map[string]any{"k": int64(1), "n": 5e18}}),
			rows: []any{}, samples: []any{}, marks: []any{},
		},
		{
			// B3: latency within_s is float(...) with no try/except around it.
			name: "b3_latency_within_s_string_raises",
			rule: func() map[string]any {
				rule := latencyRule("L", map[string]any{"kind": "cast", "spell_id": int64(17)},
					map[string]any{"kind": "cast", "spell_id": int64(100)}, 0, ">= 1", nil)
				pyjson.ObjectOrEmpty(rule["check"])["within_s"] = "abc"
				return rule
			}(),
			rows: []any{castRow(1, 17)}, samples: []any{}, marks: []any{}, ctx: emptyCtx,
			errorPython: "could not convert string to float: 'abc'",
			errorGo:     "could not convert string to float",
		},
		{
			name: "edge_first_no_match",
			rule: map[string]any{"id": "F", "check": map[string]any{"metric": "first",
				"where": map[string]any{"kind": "cast", "spell_id": int64(17)}},
				"pass_if": "> 0", "source": "differential", "desc": "F"},
			rows: []any{}, samples: []any{}, marks: []any{}, ctx: emptyCtx,
		},
	}
}

func TestDifferentialRunCases(t *testing.T) {
	root := referenceRoot(t)
	for _, c := range runCases(t) {
		t.Run(c.name, func(t *testing.T) {
			runScoreCase(t, root, c)
		})
	}
}

func TestDifferentialRuleCases(t *testing.T) {
	root := referenceRoot(t)
	for _, c := range ruleCases(t) {
		t.Run(c.name, func(t *testing.T) {
			runRuleCase(t, root, c)
		})
	}
}
