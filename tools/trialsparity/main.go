// Command trialsparity proves the Go trials scorer gives the same report cards as the Python
// bench on real recorded runs.
//
// For every run folder under --runs it copies the folder twice, deletes every file either
// scorer generates (so a scorer that writes nothing cannot compare equal to one that did),
// scores one copy with the Python tool from --python-root and the other with internal/trials,
// and compares:
//
//	(a) every file in the two trees, in both directions (a file only one side wrote differs)
//	(b) the result class: scored / FAIL / INVALID / error
//	(c) that each expected output exists and was written by this run
//
// By default it also deletes the normalised events (events.jsonl) in every repeat that has a
// raw agent trace, so BOTH scorers normalise from the recorded raw input instead of reusing
// the events the run recorded. A run without raw input falls back to comparing the recorded
// normalised files, and is counted separately in the summary.
//
// It exits non-zero on any difference and prints the first differing run with a short diff.
// The run folders are only read: every score writes into a temporary copy.
//
//	go run ./tools/trialsparity --runs DIR --python-root DIR [--verbose]
//
// A folder still being written (no RUN.json, or a RUN.json newer than 10 minutes) is skipped.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

func main() {
	runs := flag.String("runs", "", "directory whose child folders are run folders")
	pythonRoot := flag.String("python-root", "", "checkout of tortoise-trials (read only; holds trials.py)")
	python := flag.String("python", "python3", "python interpreter")
	limit := flag.Int("limit", 0, "score at most this many runs (0 = all)")
	verbose := flag.Bool("verbose", false, "print the generated files deleted in each copy")
	flag.Parse()
	if *runs == "" || *pythonRoot == "" {
		fmt.Fprintln(os.Stderr, "usage: trialsparity --runs DIR --python-root DIR [--python CMD] [--limit N] [--verbose]")
		os.Exit(2)
	}
	entries, err := os.ReadDir(*runs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trialsparity: %v\n", err)
		os.Exit(2)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(*runs, entry.Name()))
		}
	}
	sort.Strings(dirs)
	compared, equal, skipped, rawRuns := 0, 0, 0, 0
	failures := 0
	for _, dir := range dirs {
		if *limit > 0 && compared >= *limit {
			break
		}
		reason := skipReason(dir)
		if reason != "" {
			fmt.Printf("skip %s: %s\n", filepath.Base(dir), reason)
			skipped++
			continue
		}
		compared++
		diff, raw, err := compareRun(dir, *pythonRoot, *python, *verbose)
		if raw {
			rawRuns++
		}
		if err != nil {
			failures++
			fmt.Printf("ERROR %s: %v\n", filepath.Base(dir), err)
			continue
		}
		if diff != "" {
			failures++
			fmt.Printf("DIFF %s\n%s", filepath.Base(dir), diff)
		} else {
			equal++
			fmt.Printf("same %s%s\n", filepath.Base(dir), modeSuffix(raw))
		}
	}
	fmt.Printf("compared %d runs, equal %d, differ %d, skipped %d, from raw input %d (pre-normalised %d)\n",
		compared, equal, failures, skipped, rawRuns, compared-rawRuns)
	if failures > 0 {
		os.Exit(1)
	}
}

func modeSuffix(raw bool) string {
	if raw {
		return " (raw input)"
	}
	return " (pre-normalised)"
}

// skipReason is "" for a finished run and the reason a folder is not scored.
func skipReason(dir string) string {
	info, err := os.Stat(filepath.Join(dir, "RUN.json"))
	if err != nil {
		return "no RUN.json"
	}
	if time.Since(info.ModTime()) < 10*time.Minute {
		return "RUN.json newer than 10 minutes (still being written)"
	}
	return ""
}

func compareRun(dir, pythonRoot, python string, verbose bool) (string, bool, error) {
	temp, err := os.MkdirTemp("", "trialsparity-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(temp)
	pyDir := filepath.Join(temp, "py")
	goDir := filepath.Join(temp, "go")
	if err := copyTree(dir, pyDir); err != nil {
		return "", false, fmt.Errorf("copy for python: %w", err)
	}
	if err := copyTree(dir, goDir); err != nil {
		return "", false, fmt.Errorf("copy for go: %w", err)
	}
	deleted, raw, err := deleteGenerated(pyDir)
	if err != nil {
		return "", false, fmt.Errorf("delete generated files: %w", err)
	}
	if _, _, err := deleteGenerated(goDir); err != nil {
		return "", false, fmt.Errorf("delete generated files (go copy): %w", err)
	}
	if verbose {
		fmt.Printf("delete %s:\n", filepath.Base(dir))
		for _, rel := range deleted {
			fmt.Printf("  %s\n", rel)
		}
	}

	cmd := exec.Command(python, filepath.Join(pythonRoot, "trials.py"), "score", pyDir)
	cmd.Dir = pythonRoot
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	pyCode := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return "", raw, fmt.Errorf("python score: %v: %s", err, errBuf.String())
		}
		pyCode = exitErr.ExitCode()
	}

	runTrials, goErr := score.ScoreRunE(goDir)

	pyClass := pythonClass(pyDir, pyCode)
	goClass := goClass(runTrials, goErr)
	var diff strings.Builder
	if pyClass != goClass {
		fmt.Fprintf(&diff, "  result class differs: python=%s go=%s", pyClass, goClass)
		if pyClass == "error" {
			fmt.Fprintf(&diff, " (python exit %d: %s)", pyCode, firstLine(errBuf.String()))
		}
		if goClass == "error" {
			fmt.Fprintf(&diff, " (go error: %v)", goErr)
		}
		diff.WriteString("\n")
	}
	if classDiff := compareTrees(pyDir, goDir); classDiff != "" {
		diff.WriteString(classDiff)
	}
	if pyClass != "error" && goClass != "error" {
		if missing := expectedOutputs(pyDir, raw); missing != "" {
			diff.WriteString(missing)
		}
	}
	return diff.String(), raw, nil
}

// pythonClass maps the Python scorer's exit and card to scored / fail / invalid / error.
// cmd_score exits 1 for a FAIL and 3 for an INVALID; an uncaught exception also exits 1 but
// writes no card, so the card is what separates a crash from a scored run.
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

// goClass is the Go CLI's result: an error is the Python crash class, otherwise the same
// FAIL/INVALID counts cmd_score uses for its exit code.
func goClass(runTrials []map[string]any, err error) string {
	if err != nil {
		return "error"
	}
	anyFail, anyInvalid := false, false
	for _, trial := range runTrials {
		verdicts, _ := pyjson.List(trial["verdicts"])
		for _, rowVal := range verdicts {
			row, ok := pyjson.Object(rowVal)
			if !ok {
				continue
			}
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

// compareTrees compares every regular file in both copies. A file only one side wrote is a
// difference, and so is any content difference.
func compareTrees(pyDir, goDir string) string {
	pyFiles, err := manifest(pyDir)
	if err != nil {
		return fmt.Sprintf("  manifest python: %v\n", err)
	}
	goFiles, err := manifest(goDir)
	if err != nil {
		return fmt.Sprintf("  manifest go: %v\n", err)
	}
	var rels []string
	for rel := range pyFiles {
		rels = append(rels, rel)
	}
	for rel := range goFiles {
		if _, ok := pyFiles[rel]; !ok {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	for _, rel := range rels {
		pyPath, inPy := pyFiles[rel]
		goPath, inGo := goFiles[rel]
		switch {
		case !inGo:
			return fmt.Sprintf("  %s only in the python copy\n", rel)
		case !inPy:
			return fmt.Sprintf("  %s only in the go copy\n", rel)
		}
		py, err := os.ReadFile(pyPath)
		if err != nil {
			return fmt.Sprintf("  read python %s: %v\n", rel, err)
		}
		goData, err := os.ReadFile(goPath)
		if err != nil {
			return fmt.Sprintf("  read go %s: %v\n", rel, err)
		}
		if !bytes.Equal(py, goData) {
			return fmt.Sprintf("  %s differs:\n%s", rel, shortDiff(string(py), string(goData)))
		}
	}
	return ""
}

// manifest maps every regular file below root to its path, relative to root.
func manifest(root string) (map[string]string, error) {
	out := map[string]string{}
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
		out[rel] = path
		return nil
	})
	return out, err
}

// generatedName reports a file either scorer writes. events.jsonl is generated only when the
// repeat has its raw trace: without it, it is the recorded input.
func generatedName(path string) bool {
	switch filepath.Base(path) {
	case "card.json", "card.md", "trial.json", "verdicts.json", "events_summary.json":
		return true
	case "events.jsonl":
		return fileExists(filepath.Join(filepath.Dir(path), "raw-ta.jsonl"))
	}
	return false
}

// deleteGenerated removes every scorer output from a copy and reports the deleted relative
// paths plus whether the copy was put into raw-input mode (some events.jsonl was removed).
func deleteGenerated(root string) ([]string, bool, error) {
	var deleted []string
	raw := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if filepath.Base(path) == "raw-ta.jsonl" {
			raw = true
		}
		if !generatedName(path) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		deleted = append(deleted, rel)
		return os.Remove(path)
	})
	sort.Strings(deleted)
	return deleted, raw, err
}

// expectedOutputs checks that every output the scorers must write exists after the run.
func expectedOutputs(root string, raw bool) string {
	runDoc, err := pyjson.UnmarshalFile(filepath.Join(root, "RUN.json"))
	if err != nil {
		return ""
	}
	run := pyjson.ObjectOrEmpty(runDoc)
	trials, _ := pyjson.List(run["trials"])
	var want []string
	want = append(want, "card.json", "card.md")
	for _, tidVal := range trials {
		tid := pyjson.Str(tidVal)
		tdir := filepath.Join(root, tid)
		if info, err := os.Stat(tdir); err != nil || !info.IsDir() {
			continue
		}
		want = append(want, filepath.Join(tid, "trial.json"))
		for _, rep := range repDirs(tdir) {
			rel, _ := filepath.Rel(root, rep)
			want = append(want, filepath.Join(rel, "verdicts.json"))
			if raw && fileExists(filepath.Join(rep, "raw-ta.jsonl")) {
				want = append(want, filepath.Join(rel, "events.jsonl"),
					filepath.Join(rel, "samples.jsonl"), filepath.Join(rel, "events_summary.json"))
			}
		}
	}
	var missing []string
	for _, rel := range want {
		if !fileExists(filepath.Join(root, rel)) {
			missing = append(missing, rel)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	sort.Strings(missing)
	return fmt.Sprintf("  expected output missing after the run: %s\n", strings.Join(missing, ", "))
}

func repDirs(trialDir string) []string {
	entries, err := os.ReadDir(trialDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "rep") {
			out = append(out, filepath.Join(trialDir, entry.Name()))
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// shortDiff prints the first differing lines of a line diff (a simple LCS-free walk: the first
// line that differs, then up to 8 lines of each side).
func shortDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	var b strings.Builder
	start := 0
	for start < len(wantLines) && start < len(gotLines) && wantLines[start] == gotLines[start] {
		start++
	}
	from := start - 2
	if from < 0 {
		from = 0
	}
	end := start + 8
	for _, side := range []struct {
		name  string
		lines []string
	}{{"python", wantLines}, {"go", gotLines}} {
		fmt.Fprintf(&b, "  --- %s (from line %d) ---\n", side.name, from+1)
		last := end
		if last > len(side.lines) {
			last = len(side.lines)
		}
		for i := from; i < last; i++ {
			fmt.Fprintf(&b, "  %s\n", side.lines[i])
		}
	}
	return b.String()
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
