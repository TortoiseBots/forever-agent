package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/planexport"
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

// TestPlanExportForeverZulGurubLeg0: the raid's remapped leg 0 is exported as the module's plan file, with the ForeverCore pack,
// radius and boss rows; a selection past the remapped leg and a raid without one are refused, and write no file.
func TestPlanExportForeverZulGurubLeg0(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runExport("--forever", "--dir", "../../dungeons", "--last-leg", "high_priestess_jeklik",
		"--out", dir+"/", "zul_gurub")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	file := filepath.Join(dir, "plan_zul_gurub.sql")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := planexport.ParseForever(string(data))
	if err != nil || len(plans) != 1 {
		t.Fatalf("parse: %v (%d plans)", err, len(plans))
	}
	if len(plans[0].Rows.Legs) != 1 || plans[0].Rows.Legs[0].Name != "high_priestess_jeklik" {
		t.Fatalf("legs = %+v; want only high_priestess_jeklik", plans[0].Rows.Legs)
	}
	if len(plans[0].Bosses) != 1 || plans[0].Bosses[0].Entry != 14517 {
		t.Fatalf("boss rows = %+v; want 14517", plans[0].Bosses)
	}
	packed := map[uint32]int{}
	for _, u := range plans[0].Units {
		if u.Role == planexport.UnitPack {
			packed[u.Entry]++
		}
	}
	if packed[11371] < 2 {
		t.Fatalf("pack unit rows by entry = %v; want the ForeverCore serpent 11371 in pull group 0", packed)
	}
	if !strings.Contains(out, "zul_gurub: map 309, 1 legs") || !strings.Contains(out, "wrote "+file) {
		t.Fatalf("stdout %q", out)
	}

	// Leg 1 is not remapped: the selection is refused by name and writes nothing.
	past := t.TempDir()
	code, _, errs = runExport("--forever", "--dir", "../../dungeons", "--last-leg", "high_priest_venoxis",
		"--out", past+"/", "zul_gurub")
	if code != exitUsage || !strings.Contains(errs, "high_priest_venoxis") || !strings.Contains(errs, "forever_checked") {
		t.Fatalf("--last-leg high_priest_venoxis: exit %d: %s", code, errs)
	}
	if fs, _ := filepath.Glob(filepath.Join(past, "*")); len(fs) != 0 {
		t.Fatalf("a file was written for a refused selection: %v", fs)
	}

	// A raid plan needs the selection.
	none := t.TempDir()
	code, _, errs = runExport("--forever", "--dir", "../../dungeons", "--out", none+"/", "zul_gurub")
	if code != exitUsage || !strings.Contains(errs, "--last-leg") {
		t.Fatalf("no selection: exit %d: %s", code, errs)
	}

	// The report says the same, and writes nothing.
	code, repOut, _ := runExport("--forever", "--report", "--dir", "../../dungeons", "--last-leg", "high_priestess_jeklik", "zul_gurub")
	if code != exitOK || !strings.Contains(repOut, "plan=zul_gurub map=309 status=exported legs=1") {
		t.Fatalf("report: %d %q", code, repOut)
	}

	// The flag is the Forever mode's: the run-leader export writes every leg.
	code, _, errs = runExport("--dir", "../../dungeons", "--stamp", "20261126000000", "--last-leg", "high_priestess_jeklik", "--out", t.TempDir(), "zul_gurub")
	if code != exitUsage || !strings.Contains(errs, "--last-leg is the --forever mode's") {
		t.Fatalf("run-leader --last-leg: exit %d: %s", code, errs)
	}
}
