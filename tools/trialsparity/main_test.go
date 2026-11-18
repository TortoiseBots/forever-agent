package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// C4: the stale-output check compares against stamps taken BEFORE the delete. A file whose
// size and mtime are unchanged was not written by this run; a rewritten one was.
func TestExpectedOutputsUsesPreDeleteStamp(t *testing.T) {
	root := t.TempDir()
	rep := filepath.Join(root, "t1", "rep1")
	if err := os.MkdirAll(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("RUN.json", `{"trials": ["t1"]}`)
	write("card.json", "old card")
	write("card.md", "old card")
	write(filepath.Join("t1", "rep1", "verdicts.json"), "old verdicts")
	// trial.json is absent, so it can only appear by being written.
	before, err := stampTree(root)
	if err != nil {
		t.Fatal(err)
	}
	// The scorer rewrites verdicts.json and trial.json; card.* stay untouched.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(rep, "verdicts.json"), old, old); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("t1", "trial.json"), "new trial")

	problem := expectedOutputs(root, false, before)
	for _, want := range []string{"card.json (not written by this run)", "card.md (not written by this run)"} {
		if !strings.Contains(problem, want) {
			t.Errorf("problem = %q, want %q", problem, want)
		}
	}
	for _, notWant := range []string{"verdicts.json (not written by this run)", "trial.json (not written by this run)"} {
		if strings.Contains(problem, notWant) {
			t.Errorf("problem = %q, must not contain %q", problem, notWant)
		}
	}
}
