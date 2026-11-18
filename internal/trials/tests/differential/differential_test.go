// Package differential pins the Go scorers against the Python reference on small hand-made
// inputs: the cases recorded runs do not cover (row identity, float counts, huge GUIDs,
// non-object JSONL lines, null means, ...).
//
// The Go side runs in-process. The Python side runs only when TRIALS_PYTHON_ROOT points at a
// checkout of the reference (the pinned tortoise-trials tree); without it the test is SKIPPED,
// so CI stays green without Python. TRIALS_PYTHON names the interpreter (default python3).
//
//	TRIALS_PYTHON_ROOT=~/wt-trials-ref go test ./internal/trials/tests/differential
//
// Two kinds of case:
//
//	run  — a whole run folder (RUN.json, trial snapshot, reps) scored by both CLIs; every
//	       generated file is compared byte for byte, plus the result class
//	rule — one rules.evaluate / rules.Evaluate call with hand-made rows, comparing verdict,
//	       value (int vs float included), k, n and whether the call raises.
package differential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

func referenceRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("TRIALS_PYTHON_ROOT")
	if root == "" {
		t.Skip("TRIALS_PYTHON_ROOT not set: differential parity needs the Python reference")
	}
	if _, err := os.Stat(filepath.Join(root, "trials.py")); err != nil {
		t.Skipf("TRIALS_PYTHON_ROOT=%s has no trials.py", root)
	}
	return root
}

func pythonBin() string {
	if v := os.Getenv("TRIALS_PYTHON"); v != "" {
		return v
	}
	return "python3"
}

// --- run cases ------------------------------------------------------------------------

type repCase struct {
	noSetup     bool
	setup       map[string]any
	noMarks     bool
	marks       []any
	marksText   []string
	samples     []any
	samplesText []string
	events      []any
	eventsText  []string
	raw         []any
	rawText     []string
}

type runCase struct {
	name      string
	trial     map[string]any
	trialText string
	run       map[string]any
	reps      []repCase
}

func defaultSetup() map[string]any {
	return map[string]any{
		"ok":    true,
		"guids": map[string]any{"player": "0x1", "bot": "0x2"},
		"config": map[string]any{
			"critical": int64(20), "low": int64(50), "medium": int64(70),
		},
	}
}

func defaultMarks() []any {
	return []any{
		map[string]any{"kind": "fight_start", "wall": "2026-01-01T00:00:00"},
		map[string]any{"kind": "fight_end", "wall": "2026-01-01T00:00:10", "ended": "boss dead"},
	}
}

func filledSetup(extra map[string]any) map[string]any {
	setup := defaultSetup()
	for k, v := range extra {
		setup[k] = v
	}
	return setup
}

func newTrial(ruleDocs ...any) map[string]any {
	return map[string]any{"id": "t1", "min_valid_s": int64(0), "rules": ruleDocs}
}

func metricRule(id, metric string, check map[string]any, passIf string) map[string]any {
	full := map[string]any{"metric": metric}
	for k, v := range check {
		full[k] = v
	}
	return map[string]any{"id": id, "check": full, "pass_if": passIf,
		"source": "differential", "desc": id}
}

func castRow(t float64, spellID int64) map[string]any {
	return map[string]any{
		"t": t, "kind": "cast", "spell_id": spellID,
		"src_guid": "0x2", "dst_guid": "0x1", "src": "Bot", "dst": "Player",
		"src_unit": "$bot", "dst_unit": "$player", "src_role": "bot", "dst_role": "player",
	}
}

func rawCast(seq int64, time string, spellID int64, crit bool) map[string]any {
	return map[string]any{
		"seq": seq, "time": time, "kind": "spell_go", "spell_id": spellID, "crit": crit,
		"source": map[string]any{"name": "Bot", "guid": "0x2"},
		"target": map[string]any{"name": "Player", "guid": "0x1"},
	}
}

func writeJSONFile(t *testing.T, path string, doc any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pyjson.Dump(path, doc); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeLines(t *testing.T, path string, rows []any, text []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, line := range text {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	for _, row := range rows {
		if err := pyjson.DumpLine(&buf, row); err != nil {
			t.Fatalf("encode %s: %v", path, err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCase(t *testing.T, root string, c runCase) {
	t.Helper()
	run := map[string]any{"label": "differential", "trials": []any{"t1"}, "trial_snapshots": true}
	for k, v := range c.run {
		run[k] = v
	}
	writeJSONFile(t, filepath.Join(root, "RUN.json"), run)
	snap := filepath.Join(root, "t1", "trial.effective.json")
	if c.trialText != "" {
		if err := os.MkdirAll(filepath.Dir(snap), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snap, []byte(c.trialText), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		writeJSONFile(t, snap, c.trial)
	}
	if len(c.reps) == 0 {
		c.reps = []repCase{{}}
	}
	for i, rep := range c.reps {
		dir := filepath.Join(root, "t1", fmt.Sprintf("rep%d", i+1))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if !rep.noSetup {
			setup := rep.setup
			if setup == nil {
				setup = defaultSetup()
			}
			writeJSONFile(t, filepath.Join(dir, "setup.json"), setup)
		}
		if !rep.noMarks {
			marks := rep.marks
			if marks == nil && rep.marksText == nil {
				marks = defaultMarks()
			}
			writeLines(t, filepath.Join(dir, "marks.jsonl"), marks, rep.marksText)
		}
		writeLines(t, filepath.Join(dir, "samples.jsonl"), rep.samples, rep.samplesText)
		if rep.events != nil || rep.eventsText != nil {
			writeLines(t, filepath.Join(dir, "events.jsonl"), rep.events, rep.eventsText)
		}
		if rep.raw != nil || rep.rawText != nil {
			writeLines(t, filepath.Join(dir, "raw-ta.jsonl"), rep.raw, rep.rawText)
		}
	}
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// pythonClass is the Python scorer's class: cmd_score exits 1 for a FAIL and 3 for an
// INVALID; an uncaught exception also exits 1 but writes no card.
func pythonClass(dir string, code int) string {
	if code != 0 && code != 1 && code != 3 {
		return "error"
	}
	if _, err := os.Stat(filepath.Join(dir, "card.json")); err != nil {
		return "error"
	}
	switch code {
	case 1:
		return "fail"
	case 3:
		return "invalid"
	}
	return "scored"
}

// goClass is the Go CLI's class: an error, else the same FAIL/INVALID counts the CLI uses.
func goClass(dir string, runTrials []map[string]any, goErr error) string {
	if goErr != nil {
		return "error"
	}
	if _, err := os.Stat(filepath.Join(dir, "card.json")); err != nil {
		return "error"
	}
	anyFail, anyInvalid := false, false
	for _, trial := range runTrials {
		verdicts, _ := pyjson.List(trial["verdicts"])
		for _, rowVal := range verdicts {
			row := pyjson.ObjectOrEmpty(rowVal)
			switch pyjson.Str(row["verdict"]) {
			case "FAIL":
				anyFail = true
			case "INVALID":
				anyInvalid = true
			}
		}
	}
	switch {
	case anyFail:
		return "fail"
	case anyInvalid:
		return "invalid"
	}
	return "scored"
}

func runPythonScore(t *testing.T, root, dir string) (int, string) {
	t.Helper()
	cmd := exec.Command(pythonBin(), filepath.Join(root, "trials.py"), "score", dir)
	cmd.Dir = root
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("python score: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return code, errBuf.String()
}

// compareTree reports the first file only one side wrote or whose bytes differ.
func compareTree(t *testing.T, pyDir, goDir string) string {
	t.Helper()
	files := map[string][]string{}
	for _, root := range []string{pyDir, goDir} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files[rel] = append(files[rel], path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var rels []string
	for rel, paths := range files {
		if len(paths) != 2 {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	if len(rels) > 0 {
		rel := rels[0]
		side := "python"
		if len(files[rel]) == 1 && strings.HasPrefix(files[rel][0], goDir) {
			side = "go"
		}
		return fmt.Sprintf("%s exists only in the %s copy", rel, side)
	}
	for _, rel := range sortedKeys(files) {
		py, err := os.ReadFile(files[rel][0])
		if err != nil {
			t.Fatal(err)
		}
		goData, err := os.ReadFile(files[rel][1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(py, goData) {
			return fmt.Sprintf("%s differs:\n--- python ---\n%s\n--- go ---\n%s", rel, py, goData)
		}
	}
	return ""
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// verdictFields is the trial-level [verdict, value, k, n] of each rule, for a readable
// difference when the trees differ.
func verdictFields(t *testing.T, dir string) map[string]string {
	t.Helper()
	doc, err := pyjson.UnmarshalFile(filepath.Join(dir, "t1", "trial.json"))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	verdicts, _ := pyjson.List(pyjson.ObjectOrEmpty(doc)["verdicts"])
	for _, rowVal := range verdicts {
		row := pyjson.ObjectOrEmpty(rowVal)
		value, _ := pyjson.MarshalLine(row["value"])
		out[pyjson.Str(row["id"])] = fmt.Sprintf("verdict=%s value=%s k=%s n=%s",
			pyjson.Str(row["verdict"]), value, pyjson.Str(row["k"]), pyjson.Str(row["n"]))
	}
	return out
}

func runScoreCase(t *testing.T, root string, c runCase) {
	t.Helper()
	input := t.TempDir()
	writeCase(t, input, c)
	temp := t.TempDir()
	pyDir := filepath.Join(temp, "py")
	goDir := filepath.Join(temp, "go")
	if err := copyTree(input, pyDir); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(input, goDir); err != nil {
		t.Fatal(err)
	}
	code, stderr := runPythonScore(t, root, pyDir)
	goTrials, goErr := score.ScoreRunE(goDir)
	pyClass := pythonClass(pyDir, code)
	goClass := goClass(goDir, goTrials, goErr)
	if pyClass != goClass {
		t.Fatalf("result class: python=%s (exit %d, %s), go=%s (%v)",
			pyClass, code, firstLine(stderr), goClass, goErr)
	}
	if diff := compareTree(t, pyDir, goDir); diff != "" {
		t.Fatalf("python class %s vs go class %s\n%s", pyClass, goClass, diff)
	}
	if pyClass != "error" {
		pyFields := verdictFields(t, pyDir)
		goFields := map[string]string{}
		for _, trial := range goTrials {
			verdicts, _ := pyjson.List(trial["verdicts"])
			for _, rowVal := range verdicts {
				row := pyjson.ObjectOrEmpty(rowVal)
				value, _ := pyjson.MarshalLine(row["value"])
				goFields[pyjson.Str(row["id"])] = fmt.Sprintf("verdict=%s value=%s k=%s n=%s",
					pyjson.Str(row["verdict"]), value, pyjson.Str(row["k"]), pyjson.Str(row["n"]))
			}
		}
		for id, py := range pyFields {
			if goFields[id] != py {
				t.Errorf("rule %s: python %s, go %s", id, py, goFields[id])
			}
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// --- rule cases -----------------------------------------------------------------------

type ruleCase struct {
	name    string
	rule    map[string]any
	rows    []any
	samples []any
	marks   []any
	ctx     map[string]any
}

const ruleScript = `
import json, sys
sys.path.insert(0, ".")
from trl import rules
doc = json.load(sys.stdin)
try:
    r = rules.evaluate(doc["rule"], doc["rows"], doc["samples"], doc["marks"], doc["ctx"])
    print(json.dumps({"ok": True, "result": {
        "verdict": r.get("verdict"), "value": r.get("value"),
        "k": r.get("k"), "n": r.get("n"),
        "reason": r.get("reason"), "no_match": bool(r.get("no_match"))}}, allow_nan=False))
except Exception as exc:
    print(json.dumps({"ok": False, "error": type(exc).__name__}))
`

func runRuleCase(t *testing.T, root string, c ruleCase) {
	t.Helper()
	payload := map[string]any{"rule": c.rule, "rows": c.rows, "samples": c.samples,
		"marks": c.marks, "ctx": c.ctx}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pythonBin(), "-c", ruleScript)
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(body)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("python rules.evaluate: %v: %s", err, errBuf.String())
	}
	pyDoc, err := pyjson.Unmarshal(out.Bytes())
	if err != nil {
		t.Fatalf("python output %q: %v", out.String(), err)
	}
	py := pyjson.ObjectOrEmpty(pyDoc)
	pyOK := pyjson.Truthy(py["ok"])
	pyResult := pyjson.ObjectOrEmpty(py["result"])

	doc, err := pyjson.Unmarshal(body)
	if err != nil {
		t.Fatal(err)
	}
	docObj := pyjson.ObjectOrEmpty(doc)
	ctx := ctxFromDoc(pyjson.ObjectOrEmpty(docObj["ctx"]))
	res := rules.Evaluate(
		pyjson.ObjectOrEmpty(docObj["rule"]),
		listRows(pyjson.ObjectOrEmpty(docObj["rule"]), docObj, "rows"),
		listRows(nil, docObj, "samples"),
		listRows(nil, docObj, "marks"), ctx)
	if pyOK != (res.Err == nil) {
		t.Fatalf("raise class: python ok=%v (%s), go err=%v", pyOK, pyjson.Str(py["error"]), res.Err)
	}
	if !pyOK {
		return
	}
	// The bench omits k/n when it means 0 (walk_metric, unknown metric): compare the
	// scorer's own defaults, not the absence of the key.
	pyValue, _ := pyjson.MarshalLine(pyResult["value"])
	goValue, _ := pyjson.MarshalLine(res.Value)
	if got, want := fmt.Sprintf("%s/%s/%d/%d", pyjson.Str(pyResult["verdict"]), pyValue,
		resultInt(pyResult, "k"), resultInt(pyResult, "n")),
		fmt.Sprintf("%s/%s/%d/%d", res.Verdict, goValue, res.K, res.N); got != want {
		t.Fatalf("verdict/value/k/n: python %s, go %s", got, want)
	}
}

func resultInt(doc map[string]any, key string) int64 {
	if n, ok := pyjson.Int64(doc[key]); ok {
		return n
	}
	return 0
}

func ctxFromDoc(doc map[string]any) *rules.Ctx {
	ctx := &rules.Ctx{}
	ctx.Trial = pyjson.ObjectOrEmpty(doc["trial"])
	ctx.Guids = pyjson.ObjectOrEmpty(doc["guids"])
	ctx.WalkReport = pyjson.ObjectOrEmpty(doc["walk_report"])
	if th, ok := pyjson.Object(doc["thresholds"]); ok {
		ctx.Thresholds = th
	}
	ctx.ThresholdsAssumed = pyjson.Truthy(doc["thresholds_assumed"])
	if w, ok := pyjson.List(doc["window"]); ok && len(w) == 2 {
		lo, okLo := pyjson.Float(w[0])
		hi, okHi := pyjson.Float(w[1])
		if okLo && okHi {
			ctx.Window = &[2]float64{lo, hi}
		}
	}
	return ctx
}

func listRows(_ map[string]any, doc map[string]any, key string) []rules.Row {
	items, _ := pyjson.List(doc[key])
	out := make([]rules.Row, 0, len(items))
	for _, item := range items {
		out = append(out, pyjson.ObjectOrEmpty(item))
	}
	return out
}
