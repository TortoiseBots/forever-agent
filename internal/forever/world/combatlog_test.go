package world

// The combat log's own tests: the ring's paging, its seq axis across a reconnect and its overflow rule. The rows the
// dispatch feeds are the trials runner's raw row shape; these tests use made-up names.

import (
	"encoding/json"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/combat"
)

func TestCombatLogSincePages(t *testing.T) {
	var log CombatLog
	for i := range 5 {
		log.Append(CombatRow{Kind: "melee", Amount: int32(i)})
	}
	rows, next, oldest, dropped := log.Since(0, 2)
	if len(rows) != 2 || next != 2 || oldest != 1 || dropped != 0 {
		t.Fatalf("first page %d rows next=%d oldest=%d dropped=%d", len(rows), next, oldest, dropped)
	}
	if rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("first page seqs %v", []int{rows[0].Seq, rows[1].Seq})
	}
	rows, next, _, _ = log.Since(next, 2)
	if len(rows) != 2 || next != 4 || rows[0].Seq != 3 {
		t.Fatalf("second page %d rows next=%d", len(rows), next)
	}
	rows, next, _, _ = log.Since(next, 2)
	if len(rows) != 1 || next != 5 || rows[0].Seq != 5 {
		t.Fatalf("last page %d rows next=%d", len(rows), next)
	}
	// Caught up: no rows and the same seq back, so a poller never re-reads one.
	rows, next, _, _ = log.Since(next, 2)
	if len(rows) != 0 || next != 5 {
		t.Fatalf("caught up: %d rows next=%d", len(rows), next)
	}
	// A reader ahead of the log gets nothing, never an error.
	if rows, next, _, _ := log.Since(99, 10); len(rows) != 0 || next != 99 {
		t.Fatalf("ahead: %d rows next=%d", len(rows), next)
	}
}

// TestCombatLogOverflow: the ring keeps the last CombatRingSize rows, and a reader that fell further behind learns
// how many rows it lost and continues from the oldest the ring still holds.
func TestCombatLogOverflow(t *testing.T) {
	var log CombatLog
	total := CombatRingSize + 100
	for range total {
		log.Append(CombatRow{Kind: "periodic"})
	}
	if n := log.Len(); n != CombatRingSize {
		t.Fatalf("ring holds %d rows, want %d", n, CombatRingSize)
	}
	rows, next, oldest, dropped := log.Since(0, CombatPageMax)
	if dropped != 100 {
		t.Fatalf("dropped %d, want 100", dropped)
	}
	if oldest != 101 {
		t.Fatalf("oldest %d, want 101", oldest)
	}
	if len(rows) != CombatPageMax || rows[0].Seq != 101 {
		t.Fatalf("page %d rows from seq %d", len(rows), rows[0].Seq)
	}
	if rows[len(rows)-1].Seq != next {
		t.Fatalf("next %d, last row %d", next, rows[len(rows)-1].Seq)
	}
	if next != 101+CombatPageMax-1 {
		t.Fatalf("next %d, want the page's last row %d", next, 101+CombatPageMax-1)
	}
	// The rest of the log comes in pages of at most CombatPageMax, and the last one ends at the newest row.
	for len(rows) > 0 {
		rows, next, _, dropped = log.Since(next, CombatPageMax)
		if dropped != 0 {
			t.Fatalf("a later page claims %d dropped", dropped)
		}
	}
	if next != total {
		t.Fatalf("the pages end at %d, want the newest row %d", next, total)
	}
}

// TestCombatLogSeqAcrossReconnect: a reconnect is not a new session's log. The ring lives on the session value, so a
// reader's seq from before the reconnect still points into it and no seq is reused.
func TestCombatLogSeqAcrossReconnect(t *testing.T) {
	var log CombatLog
	log.Append(CombatRow{Kind: "melee"})
	rows, next, _, _ := log.Since(0, 10)
	if len(rows) != 1 || next != 1 {
		t.Fatalf("before the reconnect: %d rows next=%d", len(rows), next)
	}
	// The link dropped and came back; the dispatch keeps writing to the same ring.
	log.Append(CombatRow{Kind: "heal"})
	rows, next, _, dropped := log.Since(next, 10)
	if len(rows) != 1 || rows[0].Seq != 2 || next != 2 || dropped != 0 {
		t.Fatalf("after the reconnect: %d rows next=%d dropped=%d", len(rows), next, dropped)
	}
}

// TestCombatRowShape: a row is the trials runner's raw row, keys and all, so trials.py and the Go normaliser read the
// same file.
func TestCombatRowShape(t *testing.T) {
	var log CombatLog
	log.Append(CombatRow{
		Kind: "spell_fail", SpellID: 5019, Reason: "line of sight",
		Source: CombatActor{GUID: "0x0000000000000011", Name: "Atank"},
		Target: CombatActor{},
	})
	rows, _, _, _ := log.Since(0, 1)
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	b, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"seq", "kind", "time", "mono_ms", "source", "target", "spell_id", "amount", "reason"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("row %s lacks %q", b, key)
		}
	}
	if src, ok := got["source"].(map[string]any); !ok || src["guid"] != "0x0000000000000011" || src["name"] != "Atank" {
		t.Fatalf("row %s source %v", b, got["source"])
	}
	// A kind without a reason carries no reason key: the row shape adds it on a spell_fail only.
	var log2 CombatLog
	log2.Append(CombatRow{Kind: "melee"})
	rows2, _, _, _ := log2.Since(0, 1)
	b2, err := json.Marshal(rows2[0])
	if err != nil {
		t.Fatal(err)
	}
	var got2 map[string]any
	if err := json.Unmarshal(b2, &got2); err != nil {
		t.Fatal(err)
	}
	if _, ok := got2["reason"]; ok {
		t.Fatalf("a melee row carries a reason: %s", b2)
	}
}

// TestCombatRowGains: the core-gains row fields and the advanced-log sample are written only when the packet had
// them, so a row from a basic log keeps its old shape.
func TestCombatRowGains(t *testing.T) {
	if combatSample(nil) != nil {
		t.Fatal("a packet without log data got a sample")
	}
	if combatSample(&combat.CastLogData{Power: []combat.LogPower{{Type: 0, Amount: 100}}}) != nil {
		t.Fatal("an uninitialized, zero-health block got a sample")
	}
	sample := combatSample(&combat.CastLogData{
		Health: 4321,
		Power:  []combat.LogPower{{Type: 1, Amount: 750, Cost: 10}},
	})
	if sample == nil || sample.Health != 4321 || sample.PowerType != 1 || sample.Power != 750 {
		t.Fatalf("sample %+v", sample)
	}

	row := CombatRow{
		Kind: "interrupt", SpellID: 1766, InterruptedSpellID: 9532,
		AbsorbedSpellID: 9, Dispelled: []CombatDispel{{SpellID: 17172, Harmful: true}}, Sample: sample,
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["interrupted_spell_id"] != float64(9532) || got["absorbed_spell_id"] != float64(9) {
		t.Fatalf("row %s", b)
	}
	list, _ := got["dispelled"].([]any)
	if len(list) != 1 {
		t.Fatalf("dispelled %v", got["dispelled"])
	}
	if first, _ := list[0].(map[string]any); first["spell_id"] != float64(17172) || first["harmful"] != true {
		t.Fatalf("dispelled entry %v", list[0])
	}
	if s, _ := got["sample"].(map[string]any); s["health"] != float64(4321) || s["power_type"] != float64(1) {
		t.Fatalf("sample %v", got["sample"])
	}

	plain, err := json.Marshal(CombatRow{Kind: "melee"})
	if err != nil {
		t.Fatal(err)
	}
	var plainMap map[string]any
	if err := json.Unmarshal(plain, &plainMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"interrupted_spell_id", "absorbed_spell_id", "dispelled", "sample"} {
		if _, ok := plainMap[key]; ok {
			t.Fatalf("a plain row carries %q: %s", key, plain)
		}
	}
}
