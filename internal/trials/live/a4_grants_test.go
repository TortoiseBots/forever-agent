package live

// a4_grants_test.go: the item provenance of a trial is the GUID the server
// named for each copy it created, on the character it went to. A copy no
// grant names is never deleted; a deletion that cannot be proved is reported
// and left in place; a hire that failed its party identity check is still
// released. Every scenario here failed before the GUID rewrite.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// readGrants reads the item grant file (nil when it does not exist).
func readGrants(t *testing.T, path string) []ItemGrant {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the item grants: %v", err)
	}
	var out []ItemGrant
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the item grant file is not a JSON list: %v\n%s", err, raw)
	}
	return out
}

func writeGrants(t *testing.T, path string, list []ItemGrant) {
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

// bagGUIDs lists the fake server's instance GUIDs of an item.
func bagGUIDs(b *bagModel, item uint32) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, it := range b.items {
		if it.item == item {
			out = append(out, it.guid)
		}
	}
	return out
}

// Rule 1: every item a trial grants is recorded at grant time by the GUID the
// server named, together with the character it went to.
func TestGiveRecordsTheServerNamedGuidAndTheCharacter(t *testing.T) {
	prov, _, bags := bagProv(t)
	prov.setCharGUID("Bheal", "0x0000000000000002")
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	guids := bagGUIDs(bags, 1708)
	if len(guids) != 1 {
		t.Fatalf("the fake must hold one created stack, got %v", guids)
	}
	got, err := prov.grants().forCharacter("test2", "Bheal")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("one created instance must be one grant: %+v", got)
	}
	if got[0].GUID != guids[0] || got[0].Item != 1708 || got[0].Server != "test2" {
		t.Fatalf("the grant must name the server's instance %s: %+v", guids[0], got[0])
	}
	if !strings.EqualFold(got[0].Character, "Bheal") || got[0].CharGUID != "0x0000000000000002" {
		t.Fatalf("the grant must name the character it went to: %+v", got[0])
	}
	// The record is on disk: a killed run loses nothing.
	if disk := readGrants(t, prov.GrantPath); len(disk) != 1 || disk[0].GUID != guids[0] {
		t.Fatalf("the grant must be on disk, got %+v", disk)
	}
}

// Rule 1, the negative: a module that cannot name its copies gets no give at
// all. Nothing is given into a bag whose copies can never be proved.
func TestGiveWithoutAnInstanceListGivesNothing(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.noGuids = true
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err == nil || !strings.Contains(err.Error(), "instance list") {
		t.Fatalf("a give without a readable instance list must fail closed: %v", err)
	}
	if got := bags.seen(".tb additem"); len(got) != 0 {
		t.Fatalf("nothing may be given when no copy can be named: %v", got)
	}
	if got := readGrants(t, prov.GrantPath); len(got) != 0 {
		t.Fatalf("no grant may be written: %+v", got)
	}
	// The setup row names the module gap: SKIPPED needs=bot_items (a required
	// row that is SKIPPED makes the trial SKIPPED), never a trial failure.
	spec := BotSpec{Class: "priest", Level: 40, Items: []TrialItem{{Name: "Sweet Nectar", Count: 20}}}
	row := prov.ensureBotGear(context.Background(), spec, "Bheal")
	if row == nil || rowOK(row) {
		t.Fatalf("a module without item instances must fail the row: %v", row)
	}
	if skippedIn(rowEvidence(row)) != "bot_items" {
		t.Fatalf("the module gap must be SKIPPED needs=bot_items: %q", rowEvidence(row))
	}
}

// Rule 1, the negative: a give that merged into the hire's own stack creates
// no instance of its own, so nothing is recorded and the stack is never
// deleted later.
func TestMergedGiveRecordsNothingAndLeavesTheStack(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.set(1708, 5)
	bags.mergeAdds = true
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err == nil || !strings.Contains(err.Error(), "created no item instance") {
		t.Fatalf("a merged give must not record a copy: %v", err)
	}
	if got := readGrants(t, prov.GrantPath); len(got) != 0 {
		t.Fatalf("a merged give must write no grant: %+v", got)
	}
	if ev, ok := prov.ClearGiven(context.Background(), "Bheal"); !ok || !strings.Contains(ev, "no items to clear") {
		t.Fatalf("a clear with no grant must do nothing: ok=%v %q", ok, ev)
	}
	if got := bags.count(1708); got != 25 {
		t.Fatalf("the hire's merged stack must stay whole, bags=%d", got)
	}
}

// Rule 2: the clear deletes only the recorded instance on that exact
// character. The hire's own stack of the same item, holding no recorded GUID,
// survives.
func TestClearDeletesOnlyTheRecordedInstance(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.set(1708, 5) // the hire's own stack, no grant names it
	own := bagGUIDs(bags, 1708)[0]
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	granted := ""
	for _, g := range bagGUIDs(bags, 1708) {
		if g != own {
			granted = g
		}
	}
	if granted == "" {
		t.Fatal("the give must create a second instance")
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("the clear must be proved: %q", ev)
	}
	if got := bags.count(1708); got != 5 {
		t.Fatalf("only the granted 20 may go, bags=%d", got)
	}
	if ids := bagGUIDs(bags, 1708); len(ids) != 1 || ids[0] != own {
		t.Fatalf("the hire's own instance %s must stay, held %v", own, ids)
	}
	rm := bags.seen(".tb removeitem")
	if len(rm) != 1 || !strings.Contains(rm[0], granted) || !strings.Contains(rm[0], "guid") {
		t.Fatalf("exactly the recorded GUID may be removed, saw %v", rm)
	}
	if left := readGrants(t, prov.GrantPath); len(left) != 0 {
		t.Fatalf("the settled grant must be retired: %+v", left)
	}
}

// Rule 2: a grant is scoped to the character and the server it was made on.
func TestClearTouchesNoOtherCharacterOrServer(t *testing.T) {
	prov, s, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	if ev, ok := prov.ClearGiven(context.Background(), "Betty"); !ok || !strings.Contains(ev, "no items to clear") {
		t.Fatalf("another character's clear must do nothing: ok=%v %q", ok, ev)
	}
	other := levelProv(s)
	other.Server, other.GrantPath = "test3", prov.GrantPath
	if ev, ok := other.ClearGiven(context.Background(), "Bheal"); !ok || !strings.Contains(ev, "no items to clear") {
		t.Fatalf("another server's clear must do nothing: ok=%v %q", ok, ev)
	}
	if got := bags.count(1708); got != 20 {
		t.Fatalf("Bheal's granted copies must be untouched, bags=%d", got)
	}
	if GrantPathFor("test2", "Atank") == GrantPathFor("test3", "Atank") {
		t.Fatal("the grant file path must carry the server")
	}
}

// A grant file that cannot be read or parsed fails the clear closed: nothing
// is deleted and the setup gives nothing.
func TestCorruptGrantFileFailsClosed(t *testing.T) {
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
		"no guid": func(t *testing.T, path string) {
			writeGrants(t, path, []ItemGrant{{Server: "test2", Character: "Bheal", Item: 1708}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			prov, _, bags := bagProv(t)
			bags.set(1708, 20)
			breakIt(t, prov.GrantPath)
			ev, ok := prov.ClearGiven(context.Background(), "Bheal")
			if ok || !strings.Contains(ev, "item grants") {
				t.Fatalf("an unreadable grant file must fail the clear closed: ok=%v %q", ok, ev)
			}
			if got := bags.count(1708); got != 20 {
				t.Fatalf("nothing may be removed on an unreadable grant file, bags=%d", got)
			}
			row := prov.ensureBotGear(context.Background(), spec, "Bheal")
			if row == nil || rowOK(row) {
				t.Fatalf("the setup must fail closed on an unreadable grant file: %v", row)
			}
			if got := bags.seen(".tb additem"); len(got) != 0 {
				t.Fatalf("the setup must not give into bags it cannot certify: %v", got)
			}
		})
	}
}

// Rule 3: a deletion the module refuses is reported and left in place, and
// the clear does not retry it or take another copy in its place.
func TestFailedDeletionIsReportedLeftAndNotRetried(t *testing.T) {
	prov, _, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	bags.refuseRemoval = true
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "remove refused") {
		t.Fatalf("a refused deletion must be reported: ok=%v %q", ok, ev)
	}
	if got := bags.count(1708); got != 20 {
		t.Fatalf("the copy must stay in place, bags=%d", got)
	}
	if got := readGrants(t, prov.GrantPath); len(got) != 1 {
		t.Fatalf("the unproved grant must stay for the next run: %+v", got)
	}
	if got := bags.seen(".tb removeitem"); len(got) != 1 {
		t.Fatalf("a failed deletion is not retried: saw %d removal(s): %v", len(got), got)
	}
}

// Rule 3: an answer is not the proof. The module may say removed=1 while the
// instance list still holds the copy; the clear reports that and leaves it.
func TestDeletionTheReadBackDoesNotProveIsReportedAndLeft(t *testing.T) {
	prov, _, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	bags.ignoreRemoval = true
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") {
		t.Fatalf("an unproved deletion must not read ok: ok=%v %q", ok, ev)
	}
	if got := bags.count(1708); got != 20 {
		t.Fatalf("the copy must stay, bags=%d", got)
	}
	if got := readGrants(t, prov.GrantPath); len(got) != 1 {
		t.Fatalf("the grant must stay: %+v", got)
	}
}

// Rule 3: a read-back that cannot be made leaves the copy in place and is
// reported: the clear never assumes the deletion landed.
func TestUnreadableReadBackAfterDeletionLeavesTheCopy(t *testing.T) {
	prov, s, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	inner := s.tbFn
	calls := 0
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb itemscan") {
			calls++
			if calls > 1 { // the clear's first read passes; its post-removal read is unreadable
				return fastTypedError(line)
			}
		}
		return inner(line)
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") {
		t.Fatalf("an unreadable read-back must not read ok: ok=%v %q", ok, ev)
	}
	if got := bags.count(1708); got != 20 {
		t.Fatalf("nothing may be deleted on an unreadable list, bags=%d", got)
	}
	if got := readGrants(t, prov.GrantPath); len(got) != 1 {
		t.Fatalf("the grant must stay: %+v", got)
	}
}

// Rule 2 and the worn path: a recorded worn copy is unequipped in the slot
// the instance list named and then deleted by its own GUID; the hire's own
// worn piece is never touched.
func TestWornGrantIsUnequippedAndDeletedByIdentity(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.setWorn(14, 5555) // the hire's own cloak, no grant names it
	bags.itemSlot[7720] = 0
	if _, err := prov.giveItem(context.Background(), "Bheal", 7720, 1); err != nil {
		t.Fatal(err)
	}
	if eq, err := prov.TB.Equip(context.Background(), "Bheal", 7720, true); err != nil || eq.Result != "equipped" {
		t.Fatalf("equip failed: %v %+v", err, eq)
	}
	granted := readGrants(t, prov.GrantPath)
	if len(granted) != 1 {
		t.Fatalf("the worn piece's grant must exist: %+v", granted)
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("the worn grant must clear: %q", ev)
	}
	if got := bags.wornAt(0); got != 0 {
		t.Fatalf("the recorded piece must be off the body, slot0=%d", got)
	}
	if got := bags.wornAt(14); got != 5555 {
		t.Fatalf("the hire's own cloak must stay worn, slot14=%d", got)
	}
	if ids := bagGUIDs(bags, 7720); len(ids) != 0 {
		t.Fatalf("the recorded instance must be gone, held %v", ids)
	}
	// The unequip happened before the deletion, and the deletion names the
	// recorded GUID.
	unequip, removal := -1, -1
	for i, l := range bags.lines {
		if strings.HasPrefix(l, ".bot item Bheal unequip 255 0") && unequip < 0 {
			unequip = i
		}
		if strings.HasPrefix(l, ".tb removeitem Bheal guid ") && removal < 0 {
			removal = i
		}
	}
	if unequip < 0 || removal < 0 || unequip > removal {
		t.Fatalf("the worn copy must be unequipped before the removal: %v", bags.lines)
	}
	if !strings.Contains(bags.lines[removal], granted[0].GUID) {
		t.Fatalf("the removal must name the recorded GUID %s: %q", granted[0].GUID, bags.lines[removal])
	}
}

// A worn grant whose slot cannot be proved is left alone and reported.
func TestWornGrantWithNoProvedSlotIsLeftInPlace(t *testing.T) {
	prov, s, bags := bagProv(t)
	bags.itemSlot[7720] = 0
	if _, err := prov.giveItem(context.Background(), "Bheal", 7720, 1); err != nil {
		t.Fatal(err)
	}
	if eq, err := prov.TB.Equip(context.Background(), "Bheal", 7720, true); err != nil || eq.Result != "equipped" {
		t.Fatalf("equip failed: %v %+v", err, eq)
	}
	// `.bot inv` answers without INV_END: the slot is not proved.
	f := s.fake
	f.mu.Lock()
	f.botFn = func(line string) []string {
		if strings.HasPrefix(line, ".bot inv") {
			return []string{"TBM:INV_BEGIN|Bheal|0|16|16", "TBM:INV_EQ|0|7720|Item 7720"}
		}
		return nil
	}
	f.mu.Unlock()
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") {
		t.Fatalf("an unproved slot must not read ok: ok=%v %q", ok, ev)
	}
	if got := bags.wornAt(0); got != 7720 {
		t.Fatalf("nothing may be unequipped without a proved slot, slot0=%d", got)
	}
	if got := bags.seen(".bot item"); len(got) != 0 {
		t.Fatalf("no unequip may be sent: %v", got)
	}
	if got := readGrants(t, prov.GrantPath); len(got) != 1 {
		t.Fatalf("the grant must stay: %+v", got)
	}
}

// A killed run's grants (a file) are cleared by the next run.
func TestNextRunClearsAKilledRunsGrants(t *testing.T) {
	first, s, bags := bagProv(t)
	if _, err := first.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	next := levelProv(s)
	next.Server, next.GrantPath = "test2", first.GrantPath
	if got := readGrants(t, next.GrantPath); len(got) != 1 {
		t.Fatalf("the killed run's grant must be on disk: %+v", got)
	}
	if _, ok := next.ClearGiven(context.Background(), "Bheal"); !ok || bags.count(1708) != 0 {
		t.Fatalf("the next run must clear the killed run's copies (bags=%d)", bags.count(1708))
	}
}

// Rule 4: a hire that logged in and then failed its party identity check is
// still in the teardown list, keyed by its character, and is released.
func TestHireThatFailedPartyVerificationIsStillReleased(t *testing.T) {
	prov, s, _ := bagProv(t)
	// The login succeeded; the party roster never names the hire, so its
	// party guid is empty and the setup row fails.
	s.scriptTB(".tb login", "TortoiseBots: Bx is logging in.")
	rep := &Repeat{Client: s.runner.client, Ledger: NewLedger(""), TrialID: "t"}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2"}
	r.tbRunner = s.runner
	prov.Client, prov.TB, prov.Ledger = s.runner.client, tbcmd.NewClient(s.runner), r.Ledger
	prov.Pool = map[string][]string{"priest": {"Bx"}}
	prov.OnExtra = func(name string) { rep.noteHire(name, "") }
	prov.OnHireGUID = func(name, guid string) { rep.noteHire(name, guid) }
	trial := map[string]any{"extra_bots": []any{map[string]any{
		"class": "priest", "role": "healer", "level": float64(40)}}}
	s.scriptParty("Atank") // the hire is never in the roster
	var sent []string
	outer := s.tbFn
	s.tbFn = func(line string) []string {
		sent = append(sent, line)
		if strings.HasPrefix(line, ".tb logout") {
			return []string{"TortoiseBots: Bx is logging out."}
		}
		return outer(line)
	}
	_, rows, err := prov.ProvisionExtras(context.Background(), trial, map[string]any{"player": "0x0000000000000001"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, row := range rows {
		if strOf(row["id"]) == "extra_bots_present" && !rowOK(row) {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("the fake must fail the hire's party identity: %v", rows)
	}
	if names := acquiredHires(rep); len(names) != 1 || !strings.EqualFold(names[0].name, "Bx") {
		t.Fatalf("the failed hire must stay in the teardown list: %+v", names)
	}
	_ = r.trialTeardown(context.Background(), rep, "t")
	loggedOut := false
	for _, l := range sent {
		if strings.HasPrefix(l, ".tb logout Bx") {
			loggedOut = true
		}
	}
	if !loggedOut {
		t.Fatalf("the failed hire must still be logged out: %v", sent)
	}
}
