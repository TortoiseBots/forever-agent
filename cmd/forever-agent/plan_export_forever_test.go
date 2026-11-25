package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanExportForeverRagefire(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runExport("--forever", "--dir", "../../dungeons", "--out", dir+"/", "ragefire_chasm")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	file := filepath.Join(dir, "plan_ragefire_chasm.sql")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, want := range []string{"ai_dungeon_step_unit", "ai_dungeon_boss", "'pack'", "'boss'", "'patrol'"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("the SQL lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(sql), "guid") {
		t.Fatal("the SQL mentions a guid")
	}
	if !strings.Contains(out, "ragefire_chasm: map 389, 9 legs, 73 steps (51 pull, 4 boss, 18 move), 93 pack units, 103 unit rows, 4 boss rows") {
		t.Fatalf("stdout %q", out)
	}
	if !strings.Contains(out, "wrote "+file) {
		t.Fatalf("stdout %q", out)
	}
	if !strings.Contains(errs, "--forever without --mmaps") {
		t.Fatalf("stderr lacks the no-mmaps note: %q", errs)
	}
	// A second export to the same name is refused unless --force.
	if code, _, errs := runExport("--forever", "--dir", "../../dungeons", "--out", dir+"/", "ragefire_chasm"); code != exitUsage || !strings.Contains(errs, "exists") {
		t.Fatalf("second export: %d %q", code, errs)
	}
	if code, _, errs := runExport("--forever", "--dir", "../../dungeons", "--out", dir+"/", "--force", "ragefire_chasm"); code != exitOK {
		t.Fatalf("--force: %d %q", code, errs)
	}
	// The name has no date: --stamp, the run-leader export's, is refused, so the output is byte-stable.
	if code, _, errs := runExport("--forever", "--dir", "../../dungeons", "--stamp", "20261120000000", "--out", t.TempDir()+"/", "ragefire_chasm"); code != exitUsage || !strings.Contains(errs, "plan_<plan>.sql") {
		t.Fatalf("--stamp: %d %q", code, errs)
	}
}

func TestPlanExportForeverReport(t *testing.T) {
	code, out, errs := runExport("--forever", "--report", "--dir", "../../dungeons", "ragefire_chasm", "uldaman")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "plan=ragefire_chasm map=389 status=exported legs=9 steps=73 pulls=51 moves=18 bosses=4 units=103 boss_rows=4") {
		t.Fatalf("stdout %q", out)
	}
	for _, want := range []string{"dropped legs[3].skip", "entries 11834", `dropped avoid[0] (plan, box "lava basin of the trogg cave")`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the report lacks %q: %q", want, out)
		}
	}
	if !strings.Contains(out, "plan=uldaman map=70 status=refused") || !strings.Contains(out, "refused legs[1].boss.loot_item") {
		t.Fatalf("stdout %q", out)
	}
	// Report mode writes nothing, so --out is a usage error.
	if code, _, errs := runExport("--forever", "--report", "--out", t.TempDir(), "ragefire_chasm"); code != exitUsage || !strings.Contains(errs, "--report writes nothing") {
		t.Fatalf("--report --out: %d %q", code, errs)
	}
}

func TestPlanExportForeverRefusesABadField(t *testing.T) {
	dir := t.TempDir()
	plans := t.TempDir()
	plan := `{"version":1,"name":"gossipcave","status":"live-checked","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":1,"y":0,"z":0},"steps":[{"gossip":{"npc":7607,"near":{"x":12,"y":0,"z":0},"text":"blow","by":"tank"}}]}]}`
	if err := os.WriteFile(filepath.Join(plans, "gossipcave.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := runExport("--forever", "--dir", plans, "--out", dir+"/", "gossipcave")
	if code != exitUsage || !strings.Contains(errs, "legs[0].steps[0].gossip") {
		t.Fatalf("exit %d: %q", code, errs)
	}
	if fs, _ := filepath.Glob(filepath.Join(dir, "*")); len(fs) != 0 {
		t.Fatalf("a file was written for a refused plan: %v", fs)
	}
	// The report names the same field and writes nothing.
	code, out, _ := runExport("--forever", "--report", "--dir", plans, "gossipcave")
	if code != exitOK || !strings.Contains(out, "status=refused") || !strings.Contains(out, "refused legs[0].steps[0].gossip") {
		t.Fatalf("report: %d %q", code, out)
	}
}
