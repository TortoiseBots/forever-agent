package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writePlanListPlan writes one plan file into dir: a Turtle status and a Forever status that differ, so a column
// that prints the wrong one is caught.
func writePlanListPlan(t *testing.T, dir, name, status, forever string) {
	t.Helper()
	body := `{"version":1,"name":"` + name + `","status":"` + status + `","forever_status":"` + forever + `","map":1,"entrance":{"x":0,"y":0,"z":0},` +
		`"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPlanListShowsForeverStatus: one row per plan, sorted, with the status word and the forever status in their own
// columns (a plan's Turtle status and its Forever status are different facts and must not share a column).
func TestPlanListShowsForeverStatus(t *testing.T) {
	dir := t.TempDir()
	writePlanListPlan(t, dir, "beta", "live-checked", "n/a (Turtle custom content)")
	writePlanListPlan(t, dir, "alpha", "draft (not mapped, not live-checked)", "todo (not exported)")
	var out, errBuf bytes.Buffer
	if code := planListCmd([]string{"--dir", dir}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit %d: %s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and two rows:\n%s", out.String())
	}
	if !strings.HasPrefix(lines[0], "NAME\t") {
		t.Errorf("no header: %q", lines[0])
	}
	row := strings.Split(lines[1], "\t")
	if len(row) != 5 || row[0] != "alpha" || row[3] != "draft" || row[4] != "todo (not exported)" {
		t.Fatalf("alpha row: %q", row)
	}
	if b := strings.Split(lines[2], "\t"); len(b) != 5 || b[0] != "beta" || b[3] != "live-checked" || b[4] != "n/a (Turtle custom content)" {
		t.Errorf("beta row: %q", b)
	}

	out.Reset()
	if code := planListCmd([]string{"--dir", dir, "--json"}, &out, &errBuf); code != exitOK {
		t.Fatalf("--json exit %d: %s", code, errBuf.String())
	}
	var rows []planListInfo
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if len(rows) != 2 || rows[0].Name != "alpha" || rows[0].Status != "draft" || rows[0].ForeverStatus != "todo (not exported)" || rows[0].Kind != "dungeon" {
		t.Fatalf("%+v", rows)
	}
}

// TestPlanListOverTheRepositoryPlans: the list of the plans this repo carries is sorted, carries every plan, and shows
// the Turtle-custom ones as not applicable.
func TestPlanListOverTheRepositoryPlans(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := planListCmd([]string{"--dir", "../../dungeons"}, &out, &errBuf); code != exitOK {
		t.Fatalf("exit %d: %s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 41 {
		t.Fatalf("%d lines", len(lines))
	}
	var names []string
	forever := map[string]string{}
	for _, line := range lines[1:] {
		row := strings.Split(line, "\t")
		if len(row) != 5 {
			t.Fatalf("row: %q", line)
		}
		names = append(names, row[0])
		forever[row[0]] = row[4]
	}
	if !sort.StringsAreSorted(names) {
		t.Error("rows are not sorted")
	}
	if !strings.HasPrefix(forever["black_morass"], "n/a") || !strings.HasPrefix(forever["ragefire_chasm"], "exported") {
		t.Errorf("black_morass %q, ragefire_chasm %q", forever["black_morass"], forever["ragefire_chasm"])
	}
}
