package live

// a4_cleanup_test.go: the recovery marks of the items a trial gives are
// evidence, and only evidence, for taking a copy back. A copy with no record
// behind it is never removed or unequipped: a missing baseline is not a floor
// of zero. Every scenario here failed before the cleanup rewrite.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// markRec is one record of the recovery mark file as it is on disk.
type markRec struct {
	Server string `json:"server"`
	Bot    string `json:"bot"`
	Item   uint32 `json:"item"`
	Grant  uint64 `json:"grant"`
	Base   uint32 `json:"base"`
	Intent uint32 `json:"intent"`
	Count  uint32 `json:"count"`
	Worn   bool   `json:"worn"`
}

// readMarks reads the recovery mark file (nil when it does not exist).
func readMarks(t *testing.T, path string) []markRec {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the recovery marks: %v", err)
	}
	var out []markRec
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the recovery mark file is not a JSON list: %v\n%s", err, raw)
	}
	return out
}

func writeMarks(t *testing.T, path string, list []markRec) {
	t.Helper()
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A hire's own stack with no recovery mark is never touched: the setup gives
// the trial's stack on top of it.
func TestUnmarkedTrialItemsAreNeverCleared(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.set(1708, 20) // the hire's own Sweet Nectar, no mark names it
	spec := BotSpec{Class: "priest", Level: 40, Items: []TrialItem{{Name: "Sweet Nectar", Count: 20}}}
	row := prov.ensureBotGear(context.Background(), spec, "Bheal")
	if row == nil || !rowOK(row) {
		t.Fatalf("setup must not fail on an unmarked stack: %v", row)
	}
	if got := bags.seen(".tb removeitem"); len(got) != 0 {
		t.Fatalf("no copy may be removed without a mark, saw %v", got)
	}
	if got := bags.count(1708); got != 40 {
		t.Fatalf("the hire's 20 and the trial's 20 must both be there, bags=%d", got)
	}
	marks := readMarks(t, prov.RecoveryPath)
	if len(marks) != 1 || marks[0].Item != 1708 || marks[0].Base != 20 || marks[0].Count != 20 || marks[0].Grant == 0 {
		t.Fatalf("the give must record the fresh baseline 20 and the proved 20: %+v", marks)
	}
}

// A recovery mark file that cannot be read or parsed fails the clear closed:
// nothing is removed and the setup gives nothing.
func TestCorruptRecoveryMarksFailClosed(t *testing.T) {
	spec := BotSpec{Class: "priest", Level: 40, Items: []TrialItem{{Name: "Sweet Nectar", Count: 20}}}
	for name, breakIt := range map[string]func(t *testing.T, path string){
		"malformed": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{not a list"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable": func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			prov, _, bags := bagProv(t)
			bags.set(1708, 20)
			breakIt(t, prov.RecoveryPath)
			ev, ok := prov.ClearGiven(context.Background(), "Bheal")
			if ok || !strings.Contains(ev, "recovery marks") {
				t.Fatalf("an unreadable mark file must fail the clear closed: ok=%v %q", ok, ev)
			}
			if got := bags.count(1708); got != 20 {
				t.Fatalf("nothing may be removed on an unreadable mark file, bags=%d", got)
			}
			row := prov.ensureBotGear(context.Background(), spec, "Bheal")
			if row == nil || rowOK(row) {
				t.Fatalf("the setup must fail closed on an unreadable mark file: %v", row)
			}
			if got := bags.seen(".tb additem"); len(got) != 0 {
				t.Fatalf("the setup must not give into bags it cannot certify: %v", got)
			}
			if got := bags.count(1708); got != 20 {
				t.Fatalf("the hire's stack must stay, bags=%d", got)
			}
		})
	}
}

// The copies a proved clear gives back are retired from the marks: a hired
// character that acquires items between trials keeps them, and its next give
// reads the holdings fresh.
func TestSettledGrantIsRetiredAndBetweenRunCopiesStay(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.set(1708, 5)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	if ev, ok := prov.ClearGiven(context.Background(), "Bheal"); !ok {
		t.Fatalf("the clear must be proved: %q", ev)
	}
	if got := bags.count(1708); got != 5 {
		t.Fatalf("the hire's own 5 must stay, bags=%d", got)
	}
	if marks := readMarks(t, prov.RecoveryPath); len(marks) != 0 {
		t.Fatalf("a settled grant must be retired, marks: %+v", marks)
	}
	// Between runs the hire acquires two more copies of the trial item.
	bags.set(1708, 7)
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("a retired grant must leave the next clear nothing to do: %q", ev)
	}
	if got := bags.count(1708); got != 7 {
		t.Fatalf("the two copies acquired between runs must stay, bags=%d", got)
	}
	// The next setup's give records the fresh holding as its baseline.
	spec := BotSpec{Class: "priest", Level: 40, Items: []TrialItem{{Name: "Sweet Nectar", Count: 20}}}
	if row := prov.ensureBotGear(context.Background(), spec, "Bheal"); row == nil || !rowOK(row) {
		t.Fatalf("the next setup must give on top of the 7: %v", row)
	}
	if got := bags.count(1708); got != 27 {
		t.Fatalf("the 7 must stay and the trial's 20 go on top, bags=%d", got)
	}
	marks := readMarks(t, prov.RecoveryPath)
	if len(marks) != 1 || marks[0].Base != 7 || marks[0].Count != 20 {
		t.Fatalf("the new grant must record the fresh baseline 7: %+v", marks)
	}
}

// The removal budget is proved in the step that removes: when the carried
// count drops between the clear's first read and the removal, the clear
// re-reads it and never crosses the baseline.
func TestRemovalNeverCrossesTheBaseline(t *testing.T) {
	prov, s, bags := bagProv(t)
	bags.set(1708, 5)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	inner := s.tbFn
	dropped := false
	s.tbFn = func(line string) []string {
		out := inner(line)
		// The hire uses one copy right after the clear's first count read.
		if !dropped && strings.HasPrefix(line, ".tb itemscan") && bags.count(1708) == 25 {
			dropped = true
			bags.set(1708, 24)
		}
		return out
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("the clear must be proved: %q", ev)
	}
	if got := bags.count(1708); got != 5 {
		t.Fatalf("the removal must stop at the hire's baseline 5, bags=%d", got)
	}
}

// The unequip names only a slot the hire's own inventory proves holds the
// trial item; a stale itemscan candidate slot with unrelated gear is not
// touched.
func TestWornKitUnequipsOnlyAProvenSlot(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.itemSlot[9434] = 4 // the itemscan's candidate slot
	bags.setWorn(4, 5555)   // an unrelated cloak sits there
	bags.setWorn(10, 9434)  // the hire's own copy of the piece
	if _, err := prov.giveItem(context.Background(), "Bheal", 9434, 1); err != nil {
		t.Fatal(err)
	}
	bags.set(9434, 0)
	bags.setWorn(6, 9434) // the trial's copy went to the other ring slot
	prov.marks().setWorn("test2", "Bheal", 9434)

	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("a proven slot must clear: %q", ev)
	}
	if got := bags.wornAt(4); got != 5555 {
		t.Fatalf("the unrelated cloak in slot 4 must stay worn, slot4=%d", got)
	}
	if got := bags.count(5555); got != 0 {
		t.Fatalf("the unrelated cloak must not reach the bags, bags=%d", got)
	}
	if got := bags.wornAt(10); got != 9434 {
		t.Fatalf("the hire's own copy must stay worn, slot10=%d", got)
	}
	if got := bags.wornAt(6); got != 0 {
		t.Fatalf("the trial's worn copy must be gone, slot6=%d", got)
	}
	if got := bags.count(9434); got != 0 {
		t.Fatalf("the trial's copy must be destroyed, bags=%d", got)
	}
	if marks := readMarks(t, prov.RecoveryPath); len(marks) != 0 {
		t.Fatalf("the settled grant must be retired: %+v", marks)
	}
}

// A truncated `.bot inv` reply cannot prove which item holds a slot, so the
// worn copy stays marked and the clear fails closed.
func TestWornKitNotTouchedWhenTheSlotCannotBeProved(t *testing.T) {
	prov, s, bags := bagProv(t)
	bags.itemSlot[9434] = 4
	if _, err := prov.giveItem(context.Background(), "Bheal", 9434, 1); err != nil {
		t.Fatal(err)
	}
	bags.set(9434, 0)
	bags.setWorn(4, 9434)
	prov.marks().setWorn("test2", "Bheal", 9434)
	s.fake.mu.Lock()
	s.fake.botFn = func(line string) []string {
		if strings.HasPrefix(line, ".bot inv") {
			// The head only: no INV_EQ, no INV_END.
			return []string{"TBM:INV_BEGIN|Bheal|0|16|16"}
		}
		return bags.botAnswer(line)
	}
	s.fake.mu.Unlock()

	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") {
		t.Fatalf("an unprovable slot must fail the clear closed: ok=%v %q", ok, ev)
	}
	if got := bags.wornAt(4); got != 9434 {
		t.Fatalf("the worn copy must stay, slot4=%d", got)
	}
	if marks := readMarks(t, prov.RecoveryPath); len(marks) != 1 || marks[0].Count == 0 {
		t.Fatalf("the worn grant must stay marked: %+v", marks)
	}
}

// The baseline and the grant intent are on disk before the additem is sent,
// and the read-back turns the intent into a confirmed count.
func TestGiveWritesBaselineAndIntentBeforeAddItem(t *testing.T) {
	prov, s, bags := bagProv(t)
	bags.set(1708, 5)
	inner := s.tbFn
	var atAdd []markRec
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb additem Bheal 1708") {
			atAdd = readMarks(t, prov.RecoveryPath)
		}
		return inner(line)
	}
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	if len(atAdd) != 1 || atAdd[0].Item != 1708 || atAdd[0].Base != 5 || atAdd[0].Intent != 20 ||
		atAdd[0].Count != 0 || atAdd[0].Grant == 0 {
		t.Fatalf("the baseline and intent must be recorded before additem: %+v", atAdd)
	}
	after := readMarks(t, prov.RecoveryPath)
	if len(after) != 1 || after[0].Count != 20 {
		t.Fatalf("the read-back must confirm the give: %+v", after)
	}
}

// A give whose read-back never confirmed it is never cleared: the copies
// above its baseline are not proved to be the trial's, so the setup fails
// closed. Once the count is back at the baseline the intent is retired.
func TestUnconfirmedGiveIntentIsNeverCleared(t *testing.T) {
	prov, _, bags := bagProv(t)
	writeMarks(t, prov.RecoveryPath, []markRec{
		{Server: "test2", Bot: "Bheal", Item: 1708, Grant: 7, Base: 20, Intent: 20},
	})
	bags.set(1708, 40) // the additem applied, its read-back never confirmed it
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") {
		t.Fatalf("an unconfirmed give must fail the clear closed: ok=%v %q", ok, ev)
	}
	if got := bags.count(1708); got != 40 {
		t.Fatalf("nothing may be removed for an unconfirmed give, bags=%d", got)
	}
	if marks := readMarks(t, prov.RecoveryPath); len(marks) != 1 {
		t.Fatalf("the unconfirmed grant must stay marked: %+v", marks)
	}
	spec := BotSpec{Class: "priest", Level: 40, Items: []TrialItem{{Name: "Sweet Nectar", Count: 20}}}
	if row := prov.ensureBotGear(context.Background(), spec, "Bheal"); row == nil || rowOK(row) {
		t.Fatalf("the setup must not give into bags an unconfirmed give may have filled: %v", row)
	}
	if got := bags.seen(".tb additem"); len(got) != 0 {
		t.Fatalf("the setup must give nothing: %v", got)
	}
	// The hire used the copies: the count is back at the recorded baseline and
	// the intent has nothing to reclaim.
	bags.set(1708, 20)
	ev, ok = prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("the intent must retire at its baseline: %q", ev)
	}
	if marks := readMarks(t, prov.RecoveryPath); len(marks) != 0 {
		t.Fatalf("the settled intent must be retired: %+v", marks)
	}
	if got := bags.count(1708); got != 20 {
		t.Fatalf("the hire's 20 must stay, bags=%d", got)
	}
}
