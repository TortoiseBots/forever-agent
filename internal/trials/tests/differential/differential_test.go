// Package differential pins the Go scorers against the Python reference on small hand-made
// inputs: the cases recorded runs do not cover (row identity, float counts, huge GUIDs,
// non-object JSONL lines, null means, ...).
//
// The Go side runs in-process. The Python side runs only when -python-root (or
// TRIALS_PYTHON_ROOT) points at a checkout of the reference (the pinned tortoise-trials tree);
// without it the test is SKIPPED, so CI stays green without Python. TRIALS_PYTHON names the
// interpreter (default python3).
//
//	TRIALS_PYTHON_ROOT=<local worktree> go test ./internal/trials/tests/differential
//	go test ./internal/trials/tests/differential -args -python-root <local worktree>
//
// Two kinds of case:
//
//	run  — a whole run folder (RUN.json, trial snapshot, reps) scored by both CLIs; every
//	       generated file is compared byte for byte, plus the result class
//	rule — one rules.evaluate / rules.Evaluate call with hand-made rows, comparing verdict,
//	       value (int vs float included), k, n, reason, no_match and whether the call raises;
//	       the payload is written by pyjson (not encoding/json, which writes 5.0 as 5) and
//	       Python must read back the same JSON types Go wrote.
package differential

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

var pythonRootFlag = flag.String("python-root", "",
	"checkout of the Python reference (else TRIALS_PYTHON_ROOT)")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func referenceRoot(t *testing.T) string {
	t.Helper()
	root := *pythonRootFlag
	if root == "" {
		root = os.Getenv("TRIALS_PYTHON_ROOT")
	}
	if root == "" {
		t.Skip("TRIALS_PYTHON_ROOT not set: differential parity needs the Python reference")
	}
	if _, err := os.Stat(filepath.Join(root, "trials.py")); err != nil {
		// A reference that was asked for but is absent must fail, not skip: a typo in
		// the path cannot turn the parity table green.
		t.Fatalf("reference %s has no trials.py: %v", root, err)
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
	verdictsText string
	noSetup      bool
	setup        map[string]any
	noMarks      bool
	marks        []any
	marksText    []string
	samples      []any
	samplesText  []string
	events       []any
	eventsText   []string
	raw          []any
	rawText      []string
	verdicts     map[string]any
}

type runCase struct {
	name      string
	trial     map[string]any
	trialText string
	run       map[string]any
	reps      []repCase
	// An error case names the stable part of each side's error, so "both errored" is not
	// enough: the table also pins WHY. errorStage says which stage (load / normalise /
	// evaluate / pool / write) was expected, for the failure message.
	errorPython string
	errorGo     string
	errorStage  string
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
		if rep.verdictsText != "" {
			if err := os.WriteFile(filepath.Join(dir, "verdicts.json"), []byte(rep.verdictsText), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if rep.verdicts != nil {
			writeJSONFile(t, filepath.Join(dir, "verdicts.json"), rep.verdicts)
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
// difference when the trees differ. trial.json has no top-level "value": the per-repeat
// values live in detail[*].value, so those are what is compared.
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
		out[pyjson.Str(row["id"])] = verdictSummary(row)
	}
	return out
}

// verdictSummary is the fields a run case compares for one rule row.
func verdictSummary(row map[string]any) string {
	var values []string
	details, _ := pyjson.List(row["detail"])
	for _, detVal := range details {
		value, _ := pyjson.MarshalLine(pyjson.ObjectOrEmpty(detVal)["value"])
		values = append(values, string(value))
	}
	return fmt.Sprintf("verdict=%s k=%s n=%s detail value=[%s]",
		pyjson.Str(row["verdict"]), pyjson.Str(row["k"]), pyjson.Str(row["n"]), strings.Join(values, ", "))
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
	if pyClass == "error" {
		// Both errored: the case must also say WHICH error, or any crash would pass.
		if c.errorPython == "" || c.errorGo == "" {
			t.Fatalf("error case has no expected messages (python=%q go=%q); stage %q; python stderr=%q go error=%v",
				c.errorPython, c.errorGo, c.errorStage, stderr, goErr)
		}
		if !strings.Contains(stderr, c.errorPython) {
			t.Fatalf("stage %s: python error %q does not contain %q", c.errorStage, stderr, c.errorPython)
		}
		if goErr == nil || !strings.Contains(goErr.Error(), c.errorGo) {
			t.Fatalf("stage %s: go error %v does not contain %q", c.errorStage, goErr, c.errorGo)
		}
		return
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
				goFields[pyjson.Str(row["id"])] = verdictSummary(row)
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
	// payloadText is the exact body sent to both sides instead of pyjson.Marshal of the
	// fields above: the only way to carry a number the writers refuse (Infinity, 1e999).
	payloadText string
	// An error case names the stable part of each side's error.
	errorPython string
	errorGo     string
}

const ruleScript = `
import json, sys
sys.path.insert(0, ".")
from trl import rules

def types_(v):
    if isinstance(v, dict):
        return {k: types_(x) for k, x in v.items()}
    if isinstance(v, list):
        return [types_(x) for x in v]
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, int):
        return "int"
    if isinstance(v, float):
        return "float"
    if isinstance(v, str):
        return "str"
    if v is None:
        return "null"
    return type(v).__name__

doc = json.load(sys.stdin)
try:
    r = rules.evaluate(doc["rule"], doc["rows"], doc["samples"], doc["marks"], doc["ctx"])
    print(json.dumps({"ok": True, "types": types_(doc), "result": {
        "verdict": r.get("verdict"), "value": r.get("value"),
        "k": r.get("k"), "n": r.get("n"),
        "reason": r.get("reason"), "no_match": bool(r.get("no_match"))}}, allow_nan=False))
except Exception as exc:
    print(json.dumps({"ok": False, "types": types_(doc), "error": type(exc).__name__, "msg": str(exc)}))
`

// payloadTypes mirrors ruleScript's types_ on the Go value, so a payload that reaches Python
// with a different JSON type than Go wrote (encoding/json would send 5 for float64(5.0), the
// exact bug this table exists to catch) fails the case.
func payloadTypes(v any) any {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "str"
	case int, int64, *big.Int:
		return "int"
	case float64:
		return "float"
	case []any:
		out := make([]any, len(val))
		for i := range val {
			out[i] = payloadTypes(val[i])
		}
		return out
	case []string:
		out := make([]any, len(val))
		for i := range val {
			out[i] = payloadTypes(val[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(val))
		for k := range val {
			out[k] = payloadTypes(val[k])
		}
		return out
	}
	return "unsupported"
}

func runRuleCase(t *testing.T, root string, c ruleCase) {
	t.Helper()
	payload := map[string]any{"rule": c.rule, "rows": c.rows, "samples": c.samples,
		"marks": c.marks, "ctx": c.ctx}
	var body []byte
	if c.payloadText != "" {
		// Only a raw body can carry a number the writers refuse (an infinity): json.dumps
		// with allow_nan=False cannot write one, but json.load reads it.
		body = []byte(c.payloadText)
	} else {
		var err error
		body, err = pyjson.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	doc, err := pyjson.Unmarshal(body)
	if err != nil {
		t.Fatal(err)
	}
	docObj := pyjson.ObjectOrEmpty(doc)
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
	// The expectation is the Go payload's own types, not the body decoded back: decoding
	// the body would agree with Python even when the marshaller already dropped the float
	// (encoding/json writes 5 for 5.0, which reads back as an int on both sides).
	var wantTree any = payloadTypes(docObj)
	if c.payloadText == "" {
		wantTree = payloadTypes(payload)
	}
	wantTypes, err := pyjson.MarshalLine(wantTree)
	if err != nil {
		t.Fatal(err)
	}
	pyTypes, err := pyjson.MarshalLine(py["types"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wantTypes, pyTypes) {
		t.Fatalf("payload types: go wrote %s, python read %s", wantTypes, pyTypes)
	}
	pyOK := pyjson.Truthy(py["ok"])
	pyResult := pyjson.ObjectOrEmpty(py["result"])

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
		if c.errorPython == "" || c.errorGo == "" {
			t.Fatalf("error case has no expected messages (python=%q go=%q); python msg=%q go error=%v",
				c.errorPython, c.errorGo, pyjson.Str(py["msg"]), res.Err)
		}
		msg := pyjson.Str(py["msg"])
		if !strings.Contains(msg, c.errorPython) {
			t.Fatalf("python error %q does not contain %q", msg, c.errorPython)
		}
		if res.Err == nil || !strings.Contains(res.Err.Error(), c.errorGo) {
			t.Fatalf("go error %v does not contain %q", res.Err, c.errorGo)
		}
		return
	}
	// The bench omits k/n when it means 0 (walk_metric, unknown metric): compare the
	// scorer's own defaults, not the absence of the key. A missing reason is None in
	// Python and "" in Go, so both bad halves are pinned here.
	pyValue, _ := pyjson.MarshalLine(pyResult["value"])
	goValue, _ := pyjson.MarshalLine(res.Value)
	pyReason := ""
	if raw := pyResult["reason"]; raw != nil {
		text, ok := raw.(string)
		if !ok {
			t.Fatalf("python reason %v is not a string", raw)
		}
		pyReason = text
	}
	if got, want := fmt.Sprintf("%s/%s/%d/%d/%v/%q", pyjson.Str(pyResult["verdict"]), pyValue,
		resultInt(pyResult, "k"), resultInt(pyResult, "n"),
		pyjson.Truthy(pyResult["no_match"]), pyReason),
		fmt.Sprintf("%s/%s/%d/%d/%v/%q", res.Verdict, goValue, res.K, res.N,
			res.NoMatch, res.Reason); got != want {
		t.Fatalf("verdict/value/k/n/no_match/reason: python %s, go %s", got, want)
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

// --- validate cases -------------------------------------------------------------------

// validateCase writes a directory of trial files and compares `trials.py validate`'s stdout
// and exit code with scenario.ValidateOutput.
type validateCase struct {
	name    string
	files   map[string]map[string]any
	raw     map[string]string
	want    []string
	notWant []string
}

func baseTrial(t *testing.T) map[string]any {
	t.Helper()
	doc, err := scenario.LoadTrial("_fixtures/smoke-priest-tank.json")
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func cloneTrial(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	body, err := pyjson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := pyjson.Unmarshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return pyjson.ObjectOrEmpty(copy)
}

func setRuleID(t *testing.T, trial map[string]any, id any) {
	t.Helper()
	rules, ok := pyjson.List(trial["rules"])
	if !ok || len(rules) == 0 {
		t.Fatal("base trial has no rules")
	}
	pyjson.ObjectOrEmpty(rules[0])["id"] = id
}

func validateCases(t *testing.T) []validateCase {
	with := func(id string, ruleID any) map[string]any {
		trial := cloneTrial(t, baseTrial(t))
		trial["id"] = id
		setRuleID(t, trial, ruleID)
		return trial
	}
	bad := cloneTrial(t, baseTrial(t))
	bad["id"] = "zbad"
	bad["rules"] = []any{"not-an-object"}
	return []validateCase{
		{
			// B5: trial-id prefix, %r quoting of a stem with a quote, and int 1 vs "1"
			// rule ids staying apart while int 1 duplicates int 1.
			name: "b5_messages_rule_id_keys",
			// The int key 1 must duplicate the int key 1 in alpha but not beta's "1",
			// and the stem with a quote must use Python's double-quote repr.
			want:    []string{"duplicate rule id 1 (also in alpha)", `trial id 'quote' != file stem "q'uote"`},
			notWant: []string{`duplicate rule id 1 (also in beta)`},
			files: map[string]map[string]any{
				"alpha.json":  with("alpha", int64(1)),
				"beta.json":   with("beta", "1"),
				"gamma.json":  with("gamma", int64(1)),
				"q'uote.json": with("quote", "unique-quote"),
				"wrong.json":  with("not-wrong", "unique-wrong"),
			},
		},
		{
			// A file named exactly ".json": Python's splitext keeps a leading-dot name as
			// the stem, so the mismatch names ".json"; filepath.Ext would make the stem ""
			// and Go would print the wrong stem.
			name:    "r3_dot_json_stem",
			want:    []string{`trial id 'dotfile' != file stem '.json'`},
			notWant: []string{`file stem ""`},
			files: map[string]map[string]any{
				".json": with("dotfile", "unique-dot"),
			},
		},
		{
			// B5: a rule that is not an object makes cmd_validate raise after the files
			// before it printed, exit 1.
			name: "b5_non_object_rule_raises",
			// The good file printed, then cmd_validate raised before printing bad.json.
			want:    []string{"good.json: good:"},
			notWant: []string{"zbad.json"},
			files: map[string]map[string]any{
				"good.json": with("good", "good-rule"),
				"zbad.json": bad,
			},
		},
	}
}

func runPythonValidate(t *testing.T, root string, paths []string) (string, string, int) {
	t.Helper()
	args := append([]string{filepath.Join(root, "trials.py"), "validate"}, paths...)
	cmd := exec.Command(pythonBin(), args...)
	cmd.Dir = root
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("python validate: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return out.String(), errBuf.String(), code
}

func runValidateCase(t *testing.T, root string, c validateCase) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, doc := range c.files {
		path := filepath.Join(dir, name)
		writeJSONFile(t, path, doc)
		paths = append(paths, path)
	}
	for name, text := range c.raw {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	pyOut, pyErr, pyCode := runPythonValidate(t, root, paths)
	goOut, goCode, err := scenario.ValidateOutput(paths)
	if pyCode != goCode {
		t.Fatalf("exit code: python=%d go=%d (go error %v)\n--- python ---\n%s\n--- python stderr ---\n%s\n--- go ---\n%s",
			pyCode, goCode, err, pyOut, pyErr, goOut)
	}
	if pyOut != goOut {
		t.Fatalf("stdout differs:\n--- python ---\n%s\n--- go ---\n%s", pyOut, goOut)
	}
	for _, want := range c.want {
		if !strings.Contains(pyOut, want) {
			t.Fatalf("stdout does not contain %q:\n%s\n--- python stderr ---\n%s", want, pyOut, pyErr)
		}
	}
	for _, notWant := range c.notWant {
		if strings.Contains(pyOut, notWant) {
			t.Fatalf("stdout unexpectedly contains %q:\n%s\n--- python stderr ---\n%s", notWant, pyOut, pyErr)
		}
	}
}

func TestDifferentialValidate(t *testing.T) {
	root := referenceRoot(t)
	for _, c := range validateCases(t) {
		t.Run(c.name, func(t *testing.T) {
			runValidateCase(t, root, c)
		})
	}
}
