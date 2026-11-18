package score

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/rules"
)

// TestRepOrderScoresRep10AfterRep9 is the Python RepOrderTest: score_trial calls score_rep
// once per rep in numeric order (rep10 after rep9, never between rep1 and rep2). The Python
// test patches score_rep; here the scorer is the parameter scoreTrialWith takes, so the
// calls themselves are observed, without setup.json or pre-scored verdicts.
func TestRepOrderScoresRep10AfterRep9(t *testing.T) {
	tdir := t.TempDir()
	want := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		name := "rep" + strconv.Itoa(i)
		if err := os.MkdirAll(filepath.Join(tdir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		want = append(want, name)
	}
	var seen []string
	fake := func(repDir string, _ Trial, _ []rules.PriorEvent) (map[string]any, error) {
		base := filepath.Base(repDir)
		seen = append(seen, base)
		return map[string]any{"rep": base, "rules": []any{}}, nil
	}
	if _, err := scoreTrialWith(tdir, map[string]any{"id": "t", "rules": []any{}}, fake); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(want) {
		t.Fatalf("scored %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("rep order = %v, want %v", seen, want)
		}
	}
}
