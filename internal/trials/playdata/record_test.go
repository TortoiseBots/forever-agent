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
// proven item ids and its bot name, the ledger's equip and learn rows, the samples' bot combat
// windows, and the events' casts of this bot only.
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
	writeTestFile(t, dir, "samples.jsonl", `{"unit":"bot","t":1,"in_combat":true}
{"unit":"bot","t":2,"in_combat":true}
{"unit":"bot","t":3,"in_combat":true}
{"unit":"bot","t":4,"in_combat":false}
{"unit":"bot","t":10,"in_combat":true}
{"unit":"bot","t":11,"in_combat":true}
{"unit":"player","t":1,"in_combat":true}
{"unit":"player","t":30,"in_combat":true}
{"unit":"player","t":31,"in_combat":true}
{"unit":"player","t":32,"in_combat":true}
{"unit":"player","t":33,"in_combat":true}
`)

	rec := LoadRep(dir, trial, fixture())
	if rec.Bot != "Bheal" || rec.Class != "priest" || rec.Spec != "holy" || rec.Role != "healer" || rec.Level != 60 {
		t.Fatalf("record head %+v", rec)
	}
	// The setup's bot_items read-back is the final equipment state: the ledger's 333 is history
	// (and the other bot's 444, the refused 555 and the partial last line 777 stay out).
	if !rec.EquippedKnown || len(rec.Equipped) != 2 {
		t.Fatalf("equipped %+v (known %v)", rec.Equipped, rec.EquippedKnown)
	}
	for i, id := range []int64{111, 222} {
		if rec.Equipped[i].Entry != id {
			t.Fatalf("equipped %d = %d, want %d", i, rec.Equipped[i].Entry, id)
		}
	}
	// The marker-kit read-back is not a full build: the talents are known (partial) only.
	if !rec.TalentsKnown || rec.TalentsComplete || len(rec.Talents) != 2 || rec.Talents[0] != 14751 || rec.Talents[1] != 15237 {
		t.Fatalf("talents %v (known %v, complete %v)", rec.Talents, rec.TalentsKnown, rec.TalentsComplete)
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
	// Combat time is the bot's own in_combat windows, not the repeat's clock: [1,3] and [10,11].
	// The player rows outside those windows (t 30..33) prove the unit filter: without it a third
	// window appears and the active time doubles.
	if !rec.ActiveKnown || len(rec.Combat) != 2 || rec.ActiveS != 3 {
		t.Fatalf("combat %+v (known %v): active %.1f, want [1,3] and [10,11] = 3 s", rec.Combat, rec.ActiveKnown, rec.ActiveS)
	}
	if rec.Combat[0] != (CombatWindow{From: 1, To: 3}) || rec.Combat[1] != (CombatWindow{From: 10, To: 11}) {
		t.Fatalf("combat windows %+v", rec.Combat)
	}
}

// TestLoadRepCompleteTalents: a bot_talents row that declares the full learned set is complete, even
// when it lists nothing.
func TestLoadRepCompleteTalents(t *testing.T) {
	dir := t.TempDir()
	trial := map[string]any{"bot": map[string]any{"class": "priest", "spec": "holy", "level": float64(60)}}
	writeTestFile(t, dir, "ledger.jsonl", `{"kind":"bot_talents","bot":"Bheal","spec":"holy","complete":true,"reply":"14751=known"}`)
	rec := LoadRep(dir, trial, fixture())
	if !rec.TalentsKnown || !rec.TalentsComplete || len(rec.Talents) != 1 || rec.Talents[0] != 14751 {
		t.Fatalf("talents %v (known %v, complete %v)", rec.Talents, rec.TalentsKnown, rec.TalentsComplete)
	}
	writeTestFile(t, dir, "ledger.jsonl", `{"kind":"bot_talents","bot":"Bheal","spec":"holy","complete":true,"reply":""}`)
	rec = LoadRep(dir, trial, fixture())
	if !rec.TalentsKnown || !rec.TalentsComplete || len(rec.Talents) != 0 {
		t.Fatalf("an empty complete read: %v (known %v, complete %v)", rec.Talents, rec.TalentsKnown, rec.TalentsComplete)
	}
}

// TestLoadRepEquipReplay: without a bot_items read-back the ledger's equips are replayed in order,
// the later piece replacing the earlier one in the same slot, so a replaced item never fails a bot
// whose final equipment is right.
func TestLoadRepEquipReplay(t *testing.T) {
	dir := t.TempDir()
	trial := map[string]any{"bot": map[string]any{"class": "priest", "spec": "holy", "level": float64(60)}}
	writeTestFile(t, dir, "ledger.jsonl", `{"kind":"equip","bot":"Bheal","item":1,"reply":"equipped "}
{"kind":"equip","bot":"Bheal","item":2,"reply":"equipped "}
{"kind":"equip","bot":"Bheal","item":10,"reply":"equipped "}
{"kind":"equip","bot":"Bheal","item":1,"reply":"equipped "}
{"kind":"equip","bot":"Bheal","item":3,"reply":"refused cannot be equipped"}`)
	rec := LoadRep(dir, trial, fixture())
	if !rec.EquippedKnown {
		t.Fatal("equips prove no equipment")
	}
	// 1 and 2 are both Head: 2 replaces 1, then 1 replaces 2; 10 (Waist) stays.
	want := []int64{1, 10}
	if len(rec.Equipped) != len(want) {
		t.Fatalf("equipped %+v, want %v", rec.Equipped, want)
	}
	for i, id := range want {
		if rec.Equipped[i].Entry != id {
			t.Fatalf("equipped %d = %d, want %d", i, rec.Equipped[i].Entry, id)
		}
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
	rec := LoadRep(dir, trial, fixture())
	if rec.EquippedKnown || rec.TalentsKnown || rec.ActiveKnown {
		t.Fatalf("known flags without files: %+v", rec)
	}
	if rec.Bot != "" {
		t.Fatalf("an unnamed trial bot is %q, want empty", rec.Bot)
	}
	if rec.Class != "warrior" || rec.Spec != "protection" || rec.ActiveS != 0 {
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
	rec := LoadRep(dir, trial, fixture())
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
