package rough

// Translated from tests/test_rough.py: the CardTest cases, which use only report.write_card and
// trl.scenario.validate (the live-runner classes of that file are not ported here).

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// rghTrialFile is tl.load_trial()'s fixture (test_live.TRIAL).
const rghTrialFile = "_fixtures/smoke-priest-tank.json"

// rghCard writes the RUN.json and the per-rep setup.json documents of a card run, then returns
// the card.md report.write_card wrote for it.
func rghCard(t *testing.T, steps map[string][]int64) string {
	t.Helper()
	tmp := t.TempDir()
	writeJSON(t, filepath.Join(tmp, "RUN.json"), map[string]any{
		"label": "x", "server": "s", "slots": []any{"1", "2"}, "args": map[string]any{"repeats": int64(0)},
	})
	tids := make([]string, 0, len(steps))
	for tid := range steps {
		tids = append(tids, tid)
	}
	sort.Strings(tids)
	runTrials := make([]map[string]any, 0, len(tids))
	for _, tid := range tids {
		for i, step := range steps[tid] {
			dir := filepath.Join(tmp, tid, "rep"+strconv.Itoa(i+1))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, filepath.Join(dir, "setup.json"), map[string]any{
				"ok": true, "checks": []any{},
				"difficulty": map[string]any{
					"step": step, "level_delta": 1 - step, "count": int64(2), "refill": true,
				},
			})
		}
		runTrials = append(runTrials, map[string]any{"trial": tid, "verdicts": []any{}})
	}
	report.WriteCard(tmp, runTrials)
	data, err := os.ReadFile(filepath.Join(tmp, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCardTest_CardNotesTheStepPerRepAndALinePerTrialBelowStep0(t *testing.T) {
	text := rghCard(t, map[string][]int64{"easy": {0, 0}, "eased": {0, 1, 2}})
	if !strings.Contains(text, "eased rep2: setup ok") {
		t.Fatalf("card does not note eased rep2:\n%s", text)
	}
	if !strings.Contains(text, "difficulty step 1") {
		t.Fatalf("card does not note step 1:\n%s", text)
	}
	if !strings.Contains(text, "difficulty step 2") {
		t.Fatalf("card does not note step 2:\n%s", text)
	}
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		if strings.Contains(ln, "ran easier than written") {
			lines = append(lines, ln)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want one 'ran easier than written' line, got %d:\n%s", len(lines), text)
	}
	if !strings.HasPrefix(lines[0], "- eased:") {
		t.Fatalf("ladder line is not a per-trial line: %s", lines[0])
	}
	if !strings.Contains(lines[0], "rep3 step 2 (level_delta -1") {
		t.Fatalf("ladder line does not list rep3's step: %s", lines[0])
	}
	if strings.Contains(text, "- easy: ran easier") {
		t.Fatalf("the all-step-0 trial got a ladder line:\n%s", text)
	}
}

func TestCardTest_CardWithAllRepsAtStep0HasNoLadderLine(t *testing.T) {
	text := rghCard(t, map[string][]int64{"easy": {0, 0}})
	if !strings.Contains(text, "difficulty step 0") {
		t.Fatalf("card does not note step 0:\n%s", text)
	}
	if strings.Contains(text, "ran easier than written") {
		t.Fatalf("all-step-0 run got a ladder line:\n%s", text)
	}
}

func rghSmokeTrial(t *testing.T) map[string]any {
	t.Helper()
	trial, err := scenario.LoadTrial(rghTrialFile)
	if err != nil {
		t.Fatalf("load %s: %v", rghTrialFile, err)
	}
	return trial
}

func TestCardTest_ScenarioValidationAcceptsAdaptive(t *testing.T) {
	for _, value := range []bool{true, false} {
		trial := rghSmokeTrial(t)
		obj(t, trial["situation"])["adaptive"] = value
		var errs []string
		for _, e := range scenario.Validate(trial) {
			if strings.Contains(e, "adaptive") || strings.Contains(strings.ToLower(e), "unknown") {
				errs = append(errs, e)
			}
		}
		if len(errs) != 0 {
			t.Fatalf("adaptive=%v: %v", value, errs)
		}
	}
	trial := rghSmokeTrial(t)
	obj(t, trial["situation"])["adaptive"] = "yes"
	var errs []string
	for _, e := range scenario.Validate(trial) {
		if strings.Contains(e, "adaptive") {
			errs = append(errs, e)
		}
	}
	if len(errs) == 0 {
		t.Fatalf("adaptive: \"yes\" was accepted")
	}
}

func TestCardTest_EveryTrialFileWithAdaptiveStillValidates(t *testing.T) {
	for _, path := range scenario.TrialFiles() {
		trial, err := scenario.LoadTrial(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		for _, e := range scenario.Validate(trial) {
			if strings.Contains(e, "adaptive") {
				t.Fatalf("%s: %s", path, e)
			}
		}
	}
}
