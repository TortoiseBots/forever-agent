// Command trialsparity proves the Go trials scorer gives the same report cards as the Python
// bench on real recorded runs.
//
// For every run folder under --runs it copies the folder twice, scores one copy with the
// Python tool from --python-root and the other with internal/trials, and diffs:
//
//	(a) the JSON report card (card.json)
//	(b) the text report (card.md)
//
// It exits non-zero on any difference and prints the first differing run with a short diff.
// The run folders are only read: every score writes into a temporary copy.
//
//	go run ./tools/trialsparity --runs DIR --python-root DIR
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

	"github.com/pjerra/forever-agent/internal/trials/score"
)

func main() {
	runs := flag.String("runs", "", "directory whose child folders are run folders")
	pythonRoot := flag.String("python-root", "", "checkout of tortoise-trials (read only; holds trials.py)")
	python := flag.String("python", "python3", "python interpreter")
	limit := flag.Int("limit", 0, "score at most this many runs (0 = all)")
	flag.Parse()
	if *runs == "" || *pythonRoot == "" {
		fmt.Fprintln(os.Stderr, "usage: trialsparity --runs DIR --python-root DIR [--python CMD] [--limit N]")
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
	compared, equal, skipped := 0, 0, 0
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
		diff, err := compareRun(dir, *pythonRoot, *python)
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
			fmt.Printf("same %s\n", filepath.Base(dir))
		}
	}
	fmt.Printf("compared %d runs, equal %d, differ %d, skipped %d\n", compared, equal, failures, skipped)
	if failures > 0 {
		os.Exit(1)
	}
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

func compareRun(dir, pythonRoot, python string) (string, error) {
	temp, err := os.MkdirTemp("", "trialsparity-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	pyDir := filepath.Join(temp, "py")
	goDir := filepath.Join(temp, "go")
	if err := copyTree(dir, pyDir); err != nil {
		return "", fmt.Errorf("copy for python: %w", err)
	}
	if err := copyTree(dir, goDir); err != nil {
		return "", fmt.Errorf("copy for go: %w", err)
	}
	cmd := exec.Command(python, filepath.Join(pythonRoot, "trials.py"), "score", pyDir)
	cmd.Dir = pythonRoot
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// cmd_score exits 1 for a FAIL and 3 for an INVALID: both are scored runs.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || (exitErr.ExitCode() != 1 && exitErr.ExitCode() != 3) {
			return "", fmt.Errorf("python score failed: %v: %s", err, errBuf.String())
		}
	}
	score.ScoreRun(goDir)
	var diff strings.Builder
	for _, name := range []string{"card.json", "card.md"} {
		py, err := os.ReadFile(filepath.Join(pyDir, name))
		if err != nil {
			return "", fmt.Errorf("python %s: %w", name, err)
		}
		goCard, err := os.ReadFile(filepath.Join(goDir, name))
		if err != nil {
			return "", fmt.Errorf("go %s: %w", name, err)
		}
		if !bytes.Equal(py, goCard) {
			diff.WriteString(fmt.Sprintf("  %s differs:\n", name))
			diff.WriteString(shortDiff(string(py), string(goCard)))
			break
		}
	}
	// The per-repeat verdicts and the trial verdicts are what the card is built from: a
	// difference there explains a card difference, so report it too.
	if diff.Len() == 0 {
		for _, pattern := range []string{"*/rep*/verdicts.json", "*/trial.json"} {
			matches, _ := filepath.Glob(filepath.Join(pyDir, pattern))
			sort.Strings(matches)
			for _, pyPath := range matches {
				rel, err := filepath.Rel(pyDir, pyPath)
				if err != nil {
					continue
				}
				py, err := os.ReadFile(pyPath)
				if err != nil {
					continue
				}
				goPath := filepath.Join(goDir, rel)
				goData, err := os.ReadFile(goPath)
				if err != nil {
					diff.WriteString(fmt.Sprintf("  %s missing in the go copy\n", rel))
					break
				}
				if !bytes.Equal(py, goData) {
					diff.WriteString(fmt.Sprintf("  %s differs:\n", rel))
					diff.WriteString(shortDiff(string(py), string(goData)))
					break
				}
			}
			if diff.Len() > 0 {
				break
			}
		}
	}
	return diff.String(), nil
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
