package playdata

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestFile writes one record file under dir.
func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadRep reads the runner's files into the record the rules judge: the bot block, the setup's
// proven item ids and its bot name, the ledger's equip and learn rows, the events' casts of this bot
// only, and the repeat's scored time.
func TestLoadRep(t *testing.T) {
	dir := t.TempDir()
	trial := map[string]any{"bot": map[string]any{
		"class": "priest", "spec": "holy", "role": "healer", "level": float64(60),
	}}
	writeTestFile(t, dir, "setup.json", `{
	  "bot_name": "Bheal",
	  "checks": [
	    {"id": "bot_items", "ok": true, "value": [111, 222], "evidence": "blue kit: 2 piece(s) scanned exists and equipped"},
	    {"id": "bot_spec", "ok": true, "value": "holy"}
	  ]
	}`)
	writeTestFile(t, dir, "ledger.jsonl", `{"kind":"equip","bot":"Bheal","item":333,"reply":"equipped "}
{"kind":"equip","bot":"Other","item":444,"reply":"equipped "}
{"kind":"equip","bot":"Bheal","item":555,"reply":"refused"}
{"kind":"bot_talents","bot":"Bheal","spec":"holy","reply":"15237=learned 14751=known"}
{"kind":"session","bot":"Bheal","item":666}
{"kind":"equip","bot":"Bheal","item":777,"reply":"equip`)
	writeTestFile(t, dir, "events.jsonl", `{"kind":"cast","src":"Bheal","spell":"Renew","t":1.5}
{"kind":"cast","src":"Bheal","spell":"Renew","t":4}
{"kind":"cast","src":"Other","spell":"Smite","t":5}
{"kind":"cast_start","src":"Bheal","spell":"Flash Heal","t":6}
{"kind":"cast","src":"Bheal","spell":"Flash Heal","t":7}
`)
	writeTestFile(t, dir, "verdicts.json", `{"scored_s": 42.5, "rules": []}`)

	rec := LoadRep(dir, trial)
	if rec.Bot != "Bheal" || rec.Class != "priest" || rec.Spec != "holy" || rec.Role != "healer" || rec.Level != 60 {
		t.Fatalf("record head %+v", rec)
	}
	if !rec.EquippedKnown || len(rec.Equipped) != 3 {
		t.Fatalf("equipped %+v (known %v)", rec.Equipped, rec.EquippedKnown)
	}
	// The refused 555, the other bot's 444 and the partial last line (777) stay out.
	wantIDs := []int64{111, 222, 333}
	for i, id := range wantIDs {
		if rec.Equipped[i].Entry != id {
			t.Fatalf("equipped %d = %d, want %d", i, rec.Equipped[i].Entry, id)
		}
	}
	if !rec.TalentsKnown || len(rec.Talents) != 2 || rec.Talents[0] != 14751 || rec.Talents[1] != 15237 {
		t.Fatalf("talents %v (known %v)", rec.Talents, rec.TalentsKnown)
	}
	if got := rec.Casts["Renew"]; got != 2 {
		t.Fatalf("Renew casts %d, want 2", got)
	}
	if got := rec.Casts["Flash Heal"]; got != 1 {
		t.Fatalf("Flash Heal casts %d, want 1 (cast_start is not a cast)", got)
	}
	if _, ok := rec.Casts["Smite"]; ok {
		t.Fatal("another unit's cast is in the bot's casts")
	}
	if rec.ActiveS != 42.5 {
		t.Fatalf("active %.1f, want the scored 42.5", rec.ActiveS)
	}
}

// TestLoadRepMissingFiles: a repeat without the files leaves the Known flags false (a rule reports the
// missing data) and takes the trial's own bot words.
func TestLoadRepMissingFiles(t *testing.T) {
	dir := t.TempDir()
	trial := map[string]any{
		"class": "warrior", "spec": "protection", "role": "tank", "duration_s": float64(60),
		"bot": map[string]any{"class": "warrior", "spec": "protection", "role": "tank", "level": float64(60)},
	}
	rec := LoadRep(dir, trial)
	if rec.EquippedKnown || rec.TalentsKnown {
		t.Fatalf("known flags without files: %+v", rec)
	}
	if rec.Class != "warrior" || rec.Spec != "protection" || rec.ActiveS != 60 {
		t.Fatalf("record %+v", rec)
	}
}

// TestLoadRepBotItemsSkipped: a bot_items check that is not ok proves nothing (a SKIPPED kit is no
// equipment read), and a bot_talents row with a refused reply proves no talents.
func TestLoadRepBotItemsSkipped(t *testing.T) {
	dir := t.TempDir()
	trial := map[string]any{"bot": map[string]any{"class": "druid", "spec": "balance", "level": float64(60)}}
	writeTestFile(t, dir, "setup.json", `{"bot_name":"Bdru","checks":[{"id":"bot_items","ok":false,"value":[111],"evidence":"SKIPPED"}]}`)
	writeTestFile(t, dir, "ledger.jsonl", `{"kind":"bot_talents","bot":"Bdru","spec":"balance","reply":"error: no such spell"}`)
	rec := LoadRep(dir, trial)
	if rec.EquippedKnown || len(rec.Equipped) != 0 {
		t.Fatalf("a failed bot_items check proved items: %+v", rec.Equipped)
	}
	if rec.TalentsKnown || len(rec.Talents) != 0 {
		t.Fatalf("a failed learn proved talents: %v", rec.Talents)
	}
}

// TestLearnedIDs pins the learn read-back parsing: only the ids the module proved learned or known.
func TestLearnedIDs(t *testing.T) {
	got := learnedIDs("15237=learned 14751=known 14901=refused 12=bogus")
	if len(got) != 2 || got[0] != 15237 || got[1] != 14751 {
		t.Fatalf("learned %v", got)
	}
	if ids := learnedIDs(""); len(ids) != 0 {
		t.Fatalf("empty reply: %v", ids)
	}
}

// TestRepDirsOrder keeps the card's rows in the runner's run order (rep10 after rep9).
func TestRepDirsOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"rep10", "rep2", "rep1", "other"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := repDirs(dir)
	if len(got) != 3 {
		t.Fatalf("dirs %v", got)
	}
	for i, want := range []string{"rep1", "rep2", "rep10"} {
		if filepath.Base(got[i]) != want {
			t.Fatalf("dir %d = %s, want %s", i, filepath.Base(got[i]), want)
		}
	}
}
