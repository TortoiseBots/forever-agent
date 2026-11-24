package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestLiqcheckNeedsPoints: no points is a usage error, not a run against nothing.
func TestLiqcheckNeedsPoints(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{t.TempDir()}, &out, &errb); rc != 2 {
		t.Fatalf("rc = %d, want the usage exit 2", rc)
	}
	if !strings.Contains(errb.String(), "usage: liqcheck") {
		t.Fatalf("stderr = %q, want the usage line", errb.String())
	}
}

// TestLiqcheckNeedsWholePoints: a half triple is refused before the planner is touched.
func TestLiqcheckNeedsWholePoints(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{t.TempDir(), "1", "2"}, &out, &errb); rc != 2 {
		t.Fatalf("rc = %d, want the usage exit 2", rc)
	}
}

// TestLiqcheckReportsAPlanError is the smoke test: an empty navmesh dir cannot plan the leg, so the command prints
// the leg's ERROR line and still exits 0 (a diagnostic run reports, it does not fail the shell).
func TestLiqcheckReportsAPlanError(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{t.TempDir(), "0", "0", "0", "1", "0", "0"}, &out, &errb); rc != 0 {
		t.Fatalf("rc = %d, want 0 (stderr %q)", rc, errb.String())
	}
	if !strings.Contains(out.String(), "0->1 nowater=false ERROR") {
		t.Fatalf("stdout = %q, want the unplannable leg's ERROR line", out.String())
	}
}
