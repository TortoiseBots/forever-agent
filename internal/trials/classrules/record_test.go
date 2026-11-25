package classrules

// The record loader and the per-trial rows: what one repeat directory (setup.json, events.jsonl)
// and one trial definition (roles, extras) become, and the shape TrialRows hands the card.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

func writeJSON(t *testing.T, path string, doc any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pyjson.Dump(path, doc); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeJSONL(t *testing.T, path string, rows []any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, row := range rows {
		if err := pyjson.DumpLine(&b, row); err != nil {
			t.Fatalf("encode row for %s: %v", path, err)
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testTrial is the trial definition the loader reads roles from: a warrior tank player, a Holy
// priest healer and one Paladin healer extra.
func testTrial() map[string]any {
	return map[string]any{
		"id":     "t",
		"player": map[string]any{"class": "warrior", "forced_role": "tank"},
		"bot":    map[string]any{"class": "priest", "spec": "holy", "role": "healer"},
		"extra_bots": []any{
			map[string]any{"class": "paladin", "spec": "holy", "role": "healer", "level": int64(60)},
			map[string]any{"class": "druid", "spec": "balance", "role": "dps", "level": int64(60)},
		},
	}
}

func TestLoadRep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rep1")
	setup := map[string]any{
		"ok":          true,
		"bot_name":    "Bheal",
		"extra_names": []any{"Cheal"},
		"guids": map[string]any{
			"player": "0xAA", "bot": "0xBB", "extras": []any{"0xCC", "0xEE"},
		},
	}
	writeJSON(t, filepath.Join(dir, "setup.json"), setup)
	writeJSONL(t, filepath.Join(dir, "events.jsonl"), []any{
		map[string]any{"kind": "heal", "t": 10.0, "src_guid": "0xBB", "dst_guid": "0xAA", "amount": 250},
		map[string]any{"kind": "heal", "t": 11.0, "src_guid": "0xCC", "dst_guid": "0xBB", "amount": 120},
		map[string]any{"kind": "heal", "t": 12.0, "src_guid": "0xBB", "dst_guid": "0xDD", "amount": 40},
		map[string]any{"kind": "cast", "t": 13.0, "src_guid": "0xBB", "dst_guid": "0xAA", "spell": "Flash Heal"},
	})
	rec := LoadRep(dir, testTrial())
	if !rec.EventsKnown {
		t.Fatal("events known, the file is there")
	}
	if len(rec.Members) != 4 {
		t.Fatalf("%d members, want 4", len(rec.Members))
	}
	want := []struct{ unit, name, guid, role string }{
		{"$player", "", "0xaa", RoleTank},
		{"$bot", "Bheal", "0xbb", RoleHealer},
		{"$extra1", "Cheal", "0xcc", RoleHealer},
		{"$extra2", "", "0xee", RoleDPS},
	}
	for i, w := range want {
		m := rec.Members[i]
		if m.Unit != w.unit || m.Name != w.name || m.GUID != w.guid || m.Role != w.role {
			t.Fatalf("member %d is %+v, want %+v", i, m, w)
		}
	}
	if len(rec.Heals) != 3 {
		t.Fatalf("%d heals, want the three heal rows (the cast is not one)", len(rec.Heals))
	}
	if rec.Heals[0].Src != "$bot" || rec.Heals[0].Dst != "$player" || rec.Heals[0].Amount != 250 {
		t.Fatalf("heal 0 is %+v", rec.Heals[0])
	}
	if rec.Heals[1].Src != "$extra1" || rec.Heals[1].Dst != "$bot" {
		t.Fatalf("heal 1 is %+v", rec.Heals[1])
	}
	if rec.Heals[2].Src != "$bot" || rec.Heals[2].Dst != "" {
		t.Fatalf("heal 2 (a non-party target) is %+v", rec.Heals[2])
	}
}

func TestLoadRepWithoutEvents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rep1")
	writeJSON(t, filepath.Join(dir, "setup.json"), map[string]any{"ok": true, "guids": map[string]any{"player": "0xAA", "bot": "0xBB"}})
	rec := LoadRep(dir, testTrial())
	if rec.EventsKnown {
		t.Fatal("events known, the file is missing")
	}
	if row := healAssignmentRow(rec); row.Verdict != VerdictInconclusive {
		t.Fatalf("verdict %s, want INCONCLUSIVE", row.Verdict)
	}
}

// TestTrialRows: one row per repeat, in rep order, in the card's shape.
func TestTrialRows(t *testing.T) {
	dir := t.TempDir()
	for _, rep := range []string{"rep1", "rep2"} {
		repDir := filepath.Join(dir, rep)
		writeJSON(t, filepath.Join(repDir, "setup.json"), map[string]any{
			"ok": true, "bot_name": "Bheal",
			"guids": map[string]any{"player": "0xAA", "bot": "0xBB", "extras": []any{"0xCC", "0xEE"}},
		})
		var rows []any
		for i := range 20 {
			rows = append(rows, map[string]any{"kind": "heal", "t": float64(i), "src_guid": "0xBB", "dst_guid": "0xAA", "amount": 100})
		}
		rows = append(rows,
			map[string]any{"kind": "heal", "t": 30.0, "src_guid": "0xCC", "dst_guid": "0xAA", "amount": 35},
			map[string]any{"kind": "heal", "t": 31.0, "src_guid": "0xCC", "dst_guid": "0xAA", "amount": 35},
		)
		for i := range 6 {
			rows = append(rows, map[string]any{"kind": "heal", "t": float64(40 + i), "src_guid": "0xCC", "dst_guid": "0xEE", "amount": 50})
		}
		for i := range 3 {
			rows = append(rows, map[string]any{"kind": "heal", "t": float64(52 + i), "src_guid": "0xCC", "dst_guid": "0xBB", "amount": 50})
		}
		writeJSONL(t, filepath.Join(repDir, "events.jsonl"), rows)
	}
	rows := TrialRows(dir, testTrial())
	if len(rows) != 2 {
		t.Fatalf("%d rows, want one per repeat", len(rows))
	}
	for i, rowVal := range rows {
		row, _ := pyjson.Object(rowVal)
		wantRep := []string{"rep1", "rep2"}[i]
		if pyjson.Str(row["rep"]) != wantRep {
			t.Fatalf("row %d rep %q, want %q", i, pyjson.Str(row["rep"]), wantRep)
		}
		if pyjson.Str(row["id"]) != RuleHealAssignment {
			t.Fatalf("row %d id %q", i, pyjson.Str(row["id"]))
		}
		if pyjson.Str(row["verdict"]) != VerdictPass {
			t.Fatalf("row %d is %s, want PASS (%s)", i, pyjson.Str(row["verdict"]), pyjson.Str(row["reason"]))
		}
		if pyjson.Str(row["source"]) != "trials record" {
			t.Fatalf("row %d source %q", i, pyjson.Str(row["source"]))
		}
	}
}
