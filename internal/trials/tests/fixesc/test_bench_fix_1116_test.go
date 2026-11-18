package fixesc

// Translated from tests/test_bench_fix_1116.py (bench round 1116): potion carryover
// scoring and the cross-rep item-cooldown gap.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

const (
	fix1116Player = "0x0000000000000001"
	fix1116Bot    = "0x0000000000000002"
	fix1116Mob    = "0xf13000000000abcd"
	fix1116TID    = "priest-holy-low-mana-potion"
)

func fix1116LoadTrial(t *testing.T) map[string]any {
	t.Helper()
	trial, err := scenario.LoadTrial("edge/" + fix1116TID + ".json")
	if err != nil {
		t.Fatalf("load trial %s: %v", fix1116TID, err)
	}
	return trial
}

// fix1116Wall is Python's datetime.fromtimestamp(base+offset, utc).isoformat(
// timespec="milliseconds").
func fix1116Wall(base, offset float64) string {
	at := time.Unix(0, int64((base+offset)*1e9)).UTC()
	return at.Format("2006-01-02T15:04:05.000-07:00")
}

func fix1116Event(t float64) map[string]any {
	return map[string]any{
		"t": t, "wall": nil, "kind": "cast", "src": "bot", "src_guid": fix1116Bot,
		"dst": "bot", "dst_guid": fix1116Bot, "spell": "Restore Mana", "spell_id": int64(11903),
		"dst_hp_pct": int64(60), "origin": "ta",
	}
}

// fix1116WriteRep writes one repeat dir the way the rc1116 run recorded it.
func fix1116WriteRep(t *testing.T, trialDir, name string, base float64, potions []float64) string {
	t.Helper()
	rep := filepath.Join(trialDir, name)
	writeJSON(t, filepath.Join(rep, "setup.json"), map[string]any{
		"ok":     true,
		"guids":  map[string]any{"player": fix1116Player, "bot": fix1116Bot, "mobs": []any{fix1116Mob}},
		"config": map[string]any{},
	})
	writeJSONL(t, filepath.Join(rep, "marks.jsonl"), []any{
		map[string]any{"kind": "fight_start", "wall": fix1116Wall(base, 0)},
		map[string]any{"kind": "fight_end", "wall": fix1116Wall(base, 200), "ended": "duration"},
	})
	var rows []any
	for _, p := range potions {
		rows = append(rows, fix1116Event(p))
	}
	writeJSONL(t, filepath.Join(rep, "events.jsonl"), rows)
	writeJSONL(t, filepath.Join(rep, "samples.jsonl"), nil)
	return rep
}

// fix1116ScoreReps scores a synthetic run whose repeats carry the given potion times and
// returns the first rule's verdict doc (Python score_reps returns verdicts[0]).
func fix1116ScoreReps(t *testing.T, potionsPerRep [][]float64, spacing *float64) map[string]any {
	t.Helper()
	// the rc1116 wall spacings (fight_start to fight_start): uniform 220 s puts a
	// carried first potion back inside the old cooldown span by fractions of a
	// second, while the run's real 219.6-222.4 s spacings land it just past expiry.
	gaps := []float64{220.0, 221.5, 219.587, 222.392, 220.598}
	tdir := filepath.Join(t.TempDir(), "run", fix1116TID)
	base := 1_800_000_000.0
	for i, pots := range potionsPerRep {
		if i > 0 {
			if spacing != nil {
				base += *spacing
			} else {
				idx := i - 1
				if idx >= len(gaps) {
					idx = len(gaps) - 1
				}
				base += gaps[idx]
			}
		}
		fix1116WriteRep(t, tdir, "rep"+itoa1116(i+1), base, pots)
	}
	trial := fix1116LoadTrial(t)
	out := score.ScoreTrial(tdir, trial)
	verdicts := list(t, out["verdicts"])
	if len(verdicts) == 0 {
		t.Fatalf("no verdicts in %v", out)
	}
	return obj(t, verdicts[0])
}

func itoa1116(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// detailByRep indexes a rule verdict's detail rows by rep name.
func fix1116DetailByRep(t *testing.T, verdict map[string]any) map[string]map[string]any {
	t.Helper()
	byRep := map[string]map[string]any{}
	for _, rowVal := range list(t, verdict["detail"]) {
		row := obj(t, rowVal)
		byRep[fix1116Str(row["rep"])] = row
	}
	return byRep
}

func fix1116Str(v any) string {
	s, _ := v.(string)
	return s
}

func TestPotionCarryoverTest_RecordedValuesRescoreToFourInconclusiveTwoScored(t *testing.T) {
	pots := [][]float64{{-0.29, 119.77}, {19.89, 139.93}, {38.51, 158.59},
		{59.08, 179.13}, {76.78}, {-0.32, 119.81}}
	got := fix1116ScoreReps(t, pots, nil)
	byRep := fix1116DetailByRep(t, got)
	assertEqual(t, "PASS", byRep["rep1"]["verdict"])
	assertEqual(t, "PASS", byRep["rep6"]["verdict"])
	for _, rep := range []string{"rep2", "rep3", "rep4", "rep5"} {
		if v := byRep[rep]["verdict"]; !jsonEqual(v, "INCONCLUSIVE") {
			t.Fatalf("%s verdict = %v, want INCONCLUSIVE", rep, v)
		}
		if r := byRep[rep]["reason"]; !jsonEqual(r, "item cooldown from rep N-1") {
			t.Fatalf("%s reason = %v, want item cooldown from rep N-1", rep, r)
		}
		assertEqual(t, int64(0), byRep[rep]["k"])
		assertEqual(t, int64(0), byRep[rep]["n"])
	}
}

func TestPotionCarryoverTest_AFirstPotionPastTheGraceScoresNormally(t *testing.T) {
	// 150 s after the previous rep's last potion: the bot had a free window.
	spacing := 260.0
	got := fix1116ScoreReps(t, [][]float64{{10}, {10}}, &spacing)
	assertEqual(t, "PASS", fix1116DetailByRep(t, got)["rep2"]["verdict"])
}

func TestPotionCarryoverTest_NoPotionInAPartiallyCoveredWindowIsStillAMiss(t *testing.T) {
	// a rep with NO potion still scores the +inf path (unchanged bench-1112 shape).
	got := fix1116ScoreReps(t, [][]float64{{10}, {}}, nil)
	assertEqual(t, "FAIL", fix1116DetailByRep(t, got)["rep2"]["verdict"])
}

func TestPotionCarryoverTest_AFirstHitInsideTheOldCooldownCannotHideTheCarryover(t *testing.T) {
	// rep1's last potion is late (t 200 on a 220 s rep), so rep2's cooldown span is
	// [-20, 100]. rep2's first potion at t 5 sits INSIDE that span (impossible, or a
	// proc row) and was `ordered[0]` before the fix: the carryover branch was skipped
	// and the real first potion (t 100.5, 120.5 s after rep1's last) scored 100.5 -> FAIL.
	got := fix1116ScoreReps(t, [][]float64{{200}, {5, 100.5}}, nil)
	rep2 := fix1116DetailByRep(t, got)["rep2"]
	assertEqual(t, "INCONCLUSIVE", rep2["verdict"])
	assertEqual(t, "item cooldown from rep N-1", rep2["reason"])
}

func TestPotionMinRepGapTest_TheTrialCarriesADocumentedMinRepGap(t *testing.T) {
	trial := fix1116LoadTrial(t)
	sit := obj(t, trial["situation"])
	assertEqual(t, int64(130), sit["min_rep_gap_s"])
	assertEqual(t, []any{}, errs1116(scenario.Validate(trial)))
	bad := cloneRow(trial)
	badSit := cloneRow(sit)
	badSit["min_rep_gap_s"] = int64(-5)
	bad["situation"] = badSit
	found := false
	for _, e := range scenario.Validate(bad) {
		if strings.Contains(e, "min_rep_gap_s") {
			found = true
		}
	}
	if !found {
		t.Fatalf("validate did not flag min_rep_gap_s: %v", scenario.Validate(bad))
	}
}

func errs1116(errs []string) []any {
	out := make([]any, 0, len(errs))
	for _, e := range errs {
		out = append(out, e)
	}
	return out
}
