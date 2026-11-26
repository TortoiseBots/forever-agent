package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

func levelProv(s *setupFake) *Provisioner {
	return &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner),
		Guard:  NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"}),
		Ledger: NewLedger(""), Player: "Atank"}
}

func partyN(s *setupFake, n int, names ...string) {
	for i := 0; i < n; i++ {
		s.scriptParty(names...)
	}
}

// Defect 2: on the live client player unit rows carry name "". The level
// comes from the bot's own row by guid (guid from the party roster), never 0
// from a nameless row.
func TestBotLevelReadByGuidFromNamelessRow(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{
		{"guid": "0x0000000000000003", "name": "", "type": "player", "level": 12},
		{"guid": "0x0000000000000002", "name": "", "type": "player", "level": 40},
	}
	partyN(s, 6, "Atank", "Bheal")
	s.scriptTB(".tb level Bheal 40", "TortoiseBots: Bheal is level 40.")
	row := levelProv(s).ensureLevel(context.Background(), BotSpec{Level: 40}, "Bheal")
	if row == nil || !rowOK(row) {
		t.Fatalf("level must read 40 by guid: %v", row)
	}
}

// A level that cannot be read is UNVERIFIED, never a 0 and never ok.
func TestBotLevelUnreadableIsUnverified(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{{"guid": "0x0000000000000009", "name": "", "type": "player", "level": 40}}
	partyN(s, 6, "Atank", "Bheal")
	s.scriptTB(".tb level Bheal 40", "TortoiseBots: Bheal is level 40.")
	row := levelProv(s).ensureLevel(context.Background(), BotSpec{Level: 40}, "Bheal")
	if row == nil || rowOK(row) {
		t.Fatalf("unreadable level must fail the row: %v", row)
	}
	if !strings.Contains(rowEvidence(row), "UNVERIFIED") {
		t.Fatalf("evidence must say UNVERIFIED: %q", rowEvidence(row))
	}
	if v, _ := row["value"].(int); v == 0 && row["value"] != "unknown" {
		t.Fatalf("value must be unknown, not 0: %v", row["value"])
	}
}

// Defect 3: the arena quiet check must not count the tester itself, its own
// pet or the hired bots of this trial; a stranger still makes it not quiet.
func TestArenaQuietIgnoresTesterPetAndHires(t *testing.T) {
	f := newFakeServe(t)
	f.units = []map[string]any{
		{"guid": "0x0000000000000001", "name": "", "type": "active player", "distance": 0},
		{"guid": "0x0000000000000002", "name": "", "type": "player", "distance": 3},
		{"guid": "0x0000000000000004", "name": "", "type": "player", "distance": 4},
		{"guid": "0xF140000000000005", "name": "", "type": "pet", "distance": 2},
	}
	rep := &Repeat{Client: NewClient(f.rpc()), Guids: Guids{Player: "0x0000000000000001",
		Bot: "0x0000000000000002", Extras: []string{"0x0000000000000004"}, Pet: "0xF140000000000005"}}
	tr := &tankRun{r: rep}
	if !tr.quiet(context.Background()) {
		t.Fatal("the tester, its pet and the hires must not break the quiet check")
	}
	// The tester known only by guid (row type not "active player").
	f.units[0]["type"] = "player"
	if !tr.quiet(context.Background()) {
		t.Fatal("the tester's own guid must not count")
	}
	f.units = append(f.units, map[string]any{"guid": "0x0000000000000099", "name": "", "type": "player", "distance": 20})
	if tr.quiet(context.Background()) {
		t.Fatal("a stranger must still make the arena not quiet")
	}
}

// stanceFake is a player whose stance aura 71 appears only after `.learn 71`
// (the spell was unknown), like the live rerun's character.
func stanceFake(t *testing.T) (*PlayerSetup, *fakeServe) {
	t.Helper()
	f := newFakeServe(t)
	var mu sync.Mutex
	learned := false
	f.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001", "character": "Atank", "level": 40, "mono_ms": 1000}
	f.unitsFn = func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		auras := []any{}
		if learned {
			auras = append(auras, map[string]any{"spell_id": 71})
		}
		return []map[string]any{{"guid": "0x0000000000000001", "name": "", "type": "active player", "level": 40, "auras": auras}}
	}
	f.hook = func(method string, params map[string]any) {
		if method == "gm" && strings.HasPrefix(strAny(params["text"]), ".learn") {
			mu.Lock()
			learned = true
			mu.Unlock()
		}
	}
	client := NewClient(f.rpc())
	ps := &PlayerSetup{Client: client, TB: tbcmd.NewClient(NewTBRunner(client)),
		Guard:  NewGuard(client, GuardOptions{PlayerGUID: "0x0000000000000001"}),
		Ledger: NewLedger(""), Player: "Atank", Timeout: 1500 * time.Millisecond, Poll: 20 * time.Millisecond}
	return ps, f
}

func stanceTrial() map[string]any {
	return map[string]any{"player": map[string]any{"level": float64(40), "stance": float64(71)}}
}

// Defect 5: slow aura reads before the .learn must not use up the step's
// budget: the cast, the .learn and the second cast each get their own wait.
func TestStanceWaitsAreNotOneSharedBudget(t *testing.T) {
	ps, _ := stanceFake(t)
	// Each aura read is slow enough that three failed casts (4 reads each)
	// pass the 1.5 s the old code gave the whole step.
	orig := ps.Poll
	ps.Poll = 150 * time.Millisecond // 3 casts x 4 reads x 150 ms = 1.8 s > 1.5 s
	defer func() { ps.Poll = orig }()
	row, err := ps.ensureStance(context.Background(), stanceTrial())
	if err != nil {
		t.Fatal(err)
	}
	if !rowOK(row) {
		t.Fatalf("stance must be held after .learn despite 1.8 s of earlier waits: %v", rowEvidence(row))
	}
}

// A cast that really hangs stays a typed timeout, not a "refused".
func TestStanceRealTimeoutIsTyped(t *testing.T) {
	ps, f := stanceFake(t)
	ps.Timeout = 150 * time.Millisecond
	prev := f.hook
	f.hook = func(method string, params map[string]any) {
		if method == "cast" {
			time.Sleep(600 * time.Millisecond)
		}
		prev(method, params)
	}
	cctx, cancel := ps.ctxOf(context.Background())
	defer cancel()
	err := ps.timedOut("cast", ps.Client.Cast(cctx, 71, "0x0000000000000001"))
	var to *OpTimeout
	if !errors.As(err, &to) || to.Op != "cast" {
		t.Fatalf("a hung cast must be an *OpTimeout, got %v", err)
	}
	row, _ := ps.ensureStance(context.Background(), stanceTrial())
	if rowOK(row) && !strings.Contains(rowEvidence(row), "after .learn") {
		t.Fatalf("unexpected row %v", row)
	}
	if strings.Contains(rowEvidence(row), "refused") {
		t.Fatalf("a timeout must not be reported as refused: %q", rowEvidence(row))
	}
}

func TestMain(m *testing.M) {
	// The recovery marks and arena locks live under the state dir: keep every
	// test off the real one.
	dir, err := os.MkdirTemp("", "trials-state-")
	if err == nil {
		os.Setenv("XDG_STATE_HOME", dir)
		defer os.RemoveAll(dir)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// bagModel is a hire's bags as the real module shows them: itemscan reports
// the bag count, `.tb additem <bot> <id> [n]` adds n (bags full past cap),
// `.tb removeitem <bot> <id> [n]` takes out n (all when no n) and answers the
// real record shape. The old negative-count additem is NOT a removal here
// (the real module answers a usage line that names no bot, so the typed
// layer sees nothing): a clear that still used it changes nothing and its
// read-back fails.
type bagModel struct {
	mu       sync.Mutex
	inv      map[uint32]int
	worn     map[int]uint32
	itemSlot map[uint32]int
	cap      int
	bagSlots int
	lines    []string
}

func newBagModel() *bagModel {
	return &bagModel{inv: map[uint32]int{}, worn: map[int]uint32{}, itemSlot: map[uint32]int{}, cap: 1000}
}

func (b *bagModel) count(item uint32) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inv[item]
}

func (b *bagModel) wornCountLocked(item uint32) int {
	n := 0
	for _, e := range b.worn {
		if e == item {
			n++
		}
	}
	return n
}

func (b *bagModel) wornAt(slot int) uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.worn[slot]
}

func (b *bagModel) setWorn(slot int, item uint32) {
	b.mu.Lock()
	if item == 0 {
		delete(b.worn, slot)
	} else {
		b.worn[slot] = item
	}
	b.mu.Unlock()
}

func (b *bagModel) usedBagSlotsLocked() int {
	n := 0
	for _, c := range b.inv {
		if c > 0 {
			n++
		}
	}
	return n
}

func (b *bagModel) set(item uint32, n int) {
	b.mu.Lock()
	if n == 0 {
		delete(b.inv, item)
	} else {
		b.inv[item] = n
	}
	b.mu.Unlock()
}

func (b *bagModel) seen(prefix string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range b.lines {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// answer is a setupFake.tbFn: nil for a line the model does not own.
func (b *bagModel) answer(line string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := strings.Fields(line)
	if len(f) < 3 || f[0] != ".tb" {
		return fastTypedError(line)
	}
	num := func(i int) (int, bool) {
		if i >= len(f) {
			return 0, false
		}
		n, err := strconv.Atoi(f[i])
		return n, err == nil
	}
	switch f[1] {
	case "itemscan":
		b.lines = append(b.lines, line)
		out := []string{"itemscan-begin #T"}
		canEquip := 0
		for _, w := range f[3:] {
			id, err := strconv.Atoi(w)
			if err != nil {
				continue
			}
			// The core's itemscan inv is Player::GetItemCount over
			// equipment, bags and the reagent bank, so worn copies count.
			total := b.inv[uint32(id)] + b.wornCountLocked(uint32(id))
			if slot, ok := b.itemSlot[uint32(id)]; ok {
				canEquip++
				out = append(out, fmt.Sprintf("itemscan bot=%s item=%d exists=yes name=Item%%20%d class=4:1 inv=%d req=0 quality=3 ilvl=40 canequip=yes slot=%d why= #T", f[2], id, id, total, slot))
			} else {
				out = append(out, fmt.Sprintf("itemscan bot=%s item=%d exists=yes name=Item%%20%d class=0:5 inv=%d req=0 quality=1 ilvl=5 canequip=no why= #T", f[2], id, id, total))
			}
		}
		return append(out, fmt.Sprintf("itemscan-end bot=%s count=%d records=%d exist=%d canequip=%d #T", f[2], len(out)-1, len(out)-1, len(out)-1, canEquip))
	case "additem":
		b.lines = append(b.lines, line)
		id, ok1 := num(3)
		n, ok2 := num(4)
		if !ok2 {
			n = 1
		}
		if !ok1 || n < 1 {
			return []string{} // usage refusal: names no bot, nothing happens
		}
		if b.inv[uint32(id)]+n > b.cap || (b.bagSlots > 0 && b.inv[uint32(id)] == 0 && b.usedBagSlotsLocked() >= b.bagSlots) {
			return []string{"TortoiseBots: " + f[2] + ": bags full, the item was not stored."}
		}
		b.inv[uint32(id)] += n
		return []string{fmt.Sprintf("TortoiseBots: %s now has %d of item %d.", f[2], b.inv[uint32(id)], id)}
	case "removeitem":
		b.lines = append(b.lines, line)
		id, ok1 := num(3)
		if !ok1 {
			return []string{}
		}
		n, ok2 := num(4)
		if !ok2 || n > b.inv[uint32(id)] {
			n = b.inv[uint32(id)]
		}
		b.inv[uint32(id)] -= n
		if b.inv[uint32(id)] == 0 {
			delete(b.inv, uint32(id))
		}
		return []string{fmt.Sprintf("removeitem bot=%s item=%d removed=%d left=%d", f[2], id, n, b.inv[uint32(id)])}
	case "equip":
		b.lines = append(b.lines, line)
		id, ok1 := num(3)
		if !ok1 || b.inv[uint32(id)] < 1 {
			return []string{"equip-begin #T", fmt.Sprintf("equip bot=%s item=%d result=failed why=not_found #T", f[2], id), fmt.Sprintf("equip-end bot=%s count=1 records=1 #T", f[2])}
		}
		slot, hasSlot := b.itemSlot[uint32(id)]
		if !hasSlot {
			slot = int(id) % 19
		}
		prev := b.worn[slot]
		b.worn[slot] = uint32(id)
		b.inv[uint32(id)]--
		if b.inv[uint32(id)] == 0 {
			delete(b.inv, uint32(id))
		}
		if prev != 0 {
			b.inv[prev]++
		}
		return []string{"equip-begin #T", fmt.Sprintf("equip bot=%s item=%d result=equipped slot=%d why= #T", f[2], id, slot), fmt.Sprintf("equip-end bot=%s count=1 records=1 #T", f[2])}
	}
	return fastTypedError(line)
}

func (b *bagModel) botAnswer(line string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	f := strings.Fields(line)
	if len(f) >= 3 && f[0] == ".bot" && f[1] == "inv" {
		bot := f[2]
		free := 16
		total := 16
		if b.bagSlots > 0 {
			total = b.bagSlots
			free = b.bagSlots - b.usedBagSlotsLocked()
			if free < 0 {
				free = 0
			}
		}
		lines := []string{fmt.Sprintf("TBM:INV_BEGIN|%s|0|%d|%d", bot, free, total)}
		for slot := 0; slot < 19; slot++ {
			if e := b.worn[slot]; e != 0 {
				lines = append(lines, fmt.Sprintf("TBM:INV_EQ|%d|%d|Item %d", slot, e, e))
			}
		}
		idx := 0
		for e, c := range b.inv {
			if c > 0 {
				lines = append(lines, fmt.Sprintf("TBM:INV_ITEM|255|%d|%d|%d|", 23+idx, e, c))
				idx++
			}
		}
		lines = append(lines, fmt.Sprintf("TBM:INV_END|%s", bot))
		return lines
	}
	if len(f) == 6 && f[0] == ".bot" && f[1] == "item" && f[3] == "unequip" {
		bot := f[2]
		bag, err1 := strconv.Atoi(f[4])
		slot, err2 := strconv.Atoi(f[5])
		if err1 != nil || err2 != nil || bag != 255 {
			return []string{"TBM:ACTION_ERR|item unequip|bad-slot|invalid slot"}
		}
		item := b.worn[slot]
		if item == 0 {
			return []string{"TBM:ACTION_ERR|item unequip|empty|nothing equipped there"}
		}
		if b.bagSlots > 0 && b.inv[item] == 0 && b.usedBagSlotsLocked() >= b.bagSlots {
			return []string{"TBM:ACTION_ERR|item unequip|bags-full|bags full"}
		}
		delete(b.worn, slot)
		b.inv[item]++
		return []string{fmt.Sprintf("TBM:ACTION_ACK|item unequip|bot:%s|1|%d", bot, item)}
	}
	return nil
}

func bagProv(t *testing.T) (*Provisioner, *setupFake, *bagModel) {
	t.Helper()
	s, f := newSetupFake(t)
	bags := newBagModel()
	s.tbFn = bags.answer
	f.botFn = bags.botAnswer
	prov := levelProv(s)
	prov.Server = "test2"
	prov.RecoveryPath = filepath.Join(t.TempDir(), "mark.json")
	return prov, s, bags
}

// Finding 2/3: what the trial gave is taken back with the module's
// `.tb removeitem` (never the negative additem), the mark is written only
// after the give was read back, and the clear is proved by an itemscan.
func TestClearGivenUsesRemoveItemAndReadsBack(t *testing.T) {
	prov, _, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	got := prov.marks().forBot("test2", "Bheal")
	if len(got) != 1 || got[0].Item != 1708 || got[0].Count != 20 || got[0].Server != "test2" {
		t.Fatalf("mark after a confirmed give: %+v", got)
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok || bags.count(1708) != 0 {
		t.Fatalf("clear must be proved and empty the bags: ok=%v bags=%d %s", ok, bags.count(1708), ev)
	}
	if got := bags.seen(".tb removeitem Bheal 1708 20"); len(got) != 1 {
		t.Fatalf("the removal must be `.tb removeitem Bheal 1708 20`, saw %v", bags.lines)
	}
	for _, l := range bags.seen(".tb additem") {
		if strings.Contains(l, " -") {
			t.Fatalf("the negative-count additem must never be sent: %s", l)
		}
	}
	if got := prov.marks().forBot("test2", "Bheal"); len(got) != 0 {
		t.Fatalf("the mark must be dropped after a proved clear: %v", got)
	}
}

// A refused give (bags full) is never marked: a later clear must not remove
// the items the hire already carried.
func TestRefusedGiveWritesNoMarkAndClearKeepsOldStack(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.set(1708, 5) // the hire already carried 5
	bags.cap = 10     // 20 more do not fit
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err == nil {
		t.Fatal("a refused give must be an error")
	}
	if got := prov.marks().forBot("test2", "Bheal"); len(got) != 0 {
		t.Fatalf("a refused give must write no mark: %+v", got)
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok || bags.count(1708) != 5 {
		t.Fatalf("the hire's own 5 items must stay: ok=%v bags=%d %s", ok, bags.count(1708), ev)
	}
	if len(bags.seen(".tb removeitem")) != 0 {
		t.Fatalf("nothing may be removed without a mark: %v", bags.lines)
	}
}

// A clear removes at most what the trial gave on top of the baseline.
func TestClearNeverRemovesWhatTheHireHadBefore(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.set(1708, 5)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	if _, ok := prov.ClearGiven(context.Background(), "Bheal"); !ok {
		t.Fatal("clear must be proved")
	}
	if bags.count(1708) != 5 {
		t.Fatalf("the hire's own 5 must remain, bags=%d", bags.count(1708))
	}
	// The hire used 3 of the gift: only the 17 still above the baseline go.
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	bags.set(1708, 22) // 25 - 3 used
	if _, ok := prov.ClearGiven(context.Background(), "Bheal"); !ok {
		t.Fatal("clear must be proved")
	}
	if bags.count(1708) != 5 {
		t.Fatalf("clear must stop at the baseline, bags=%d", bags.count(1708))
	}
}

// A removal the read-back does not show stays marked and is reported.
func TestClearGivenUnprovedStaysMarked(t *testing.T) {
	prov, s, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	// The module acknowledges the removal but the bags keep the items.
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb removeitem") {
			return []string{"removeitem bot=Bheal item=1708 removed=20 left=0"}
		}
		return bags.answer(line)
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") {
		t.Fatalf("an unproved clear must not read ok: ok=%v %s", ok, ev)
	}
	if len(prov.marks().forBot("test2", "Bheal")) != 1 {
		t.Fatal("the mark must stay for the next run")
	}
}

// A fake that still accepts the old negative additem as a removal must not
// make the clear pass: the agent never sends that form, so a fake that only
// understands it leaves the bags full and the clear reads UNVERIFIED.
func TestClearFailsAgainstAFakeThatOnlyKnowsTheOldForm(t *testing.T) {
	prov, s, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb additem Bheal 1708 -") {
			bags.set(1708, 0)
			return []string{"TortoiseBots: Bheal now has 0 of item 1708."}
		}
		if strings.HasPrefix(line, ".tb removeitem") {
			return []string{}
		}
		return bags.answer(line)
	}
	if _, ok := prov.ClearGiven(context.Background(), "Bheal"); ok {
		t.Fatal("a clear that needs the old negative additem form must not pass")
	}
}

// Marks name the server: the same character on another server is not touched.
func TestMarksAreScopedToTheServer(t *testing.T) {
	prov, s, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	other := levelProv(s)
	other.Server = "test3"
	other.RecoveryPath = prov.RecoveryPath
	if got := other.marks().forBot("test3", "Bheal"); len(got) != 0 {
		t.Fatalf("a mark of test2 must not show on test3: %+v", got)
	}
	if _, ok := other.ClearGiven(context.Background(), "Bheal"); !ok || bags.count(1708) != 20 {
		t.Fatalf("test3's clear must leave test2's items alone (bags=%d)", bags.count(1708))
	}
	if RecoveryPathFor("test2", "Atank") == RecoveryPathFor("test3", "Atank") {
		t.Fatal("the recovery file path must carry the server")
	}
}

// An equipped kit piece whose `.bot inv` cannot be read (or whose unequip
// is refused) fails closed UNVERIFIED and keeps its mark on disk.
func TestWornKitPieceIsNotClaimedCleared(t *testing.T) {
	prov, s, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 9434, 1); err != nil {
		t.Fatal(err)
	}
	bags.set(9434, 0)
	bags.setWorn(4, 9434) // equipped: out of the bags
	prov.marks().setWorn("test2", "Bheal", 9434)
	s.fake.mu.Lock()
	s.fake.botFn = func(line string) []string { return []string{} } // unreadable .bot inv
	s.fake.mu.Unlock()
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") || strings.Contains(ev, "cleared 1") {
		t.Fatalf("an unverified worn clear must fail closed UNVERIFIED: ok=%v %q", ok, ev)
	}
	if len(prov.marks().forBot("test2", "Bheal")) != 1 {
		t.Fatal("the worn mark must stay")
	}
}

// A killed run's mark (a file) is cleaned by the next run before it gives
// new items.
func TestNextRunClearsAKilledRunsMark(t *testing.T) {
	first, s, bags := bagProv(t)
	if _, err := first.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	// the run is killed here: a brand new Provisioner reads the file.
	next := levelProv(s)
	next.Server = "test2"
	next.RecoveryPath = first.RecoveryPath
	got := next.marks().forBot("test2", "Bheal")
	if len(got) != 1 || got[0].Item != 1708 || got[0].Count != 20 {
		t.Fatalf("the killed run's mark must be on disk: %v", got)
	}
	if _, ok := next.ClearGiven(context.Background(), "Bheal"); !ok || bags.count(1708) != 0 {
		t.Fatalf("the next run must clear the killed run's items (bags=%d)", bags.count(1708))
	}
}

// "bags full" that clearing does not free: SKIPPED with the reason, never a
// silent failure and never ok.
func TestBagsFullIsSkippedWithReason(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.cap = 0
	spec := BotSpec{Class: "priest", Level: 40, Items: []TrialItem{{Name: "Sweet Nectar", Count: 20}}}
	row := prov.ensureBotGear(context.Background(), spec, "Bheal")
	if row == nil || rowOK(row) {
		t.Fatalf("a refused give must not be ok: %v", row)
	}
	if skippedIn(rowEvidence(row)) != "bot_bag_space" {
		t.Fatalf("bags full must be SKIPPED needs=bot_bag_space with a reason, got %q", rowEvidence(row))
	}
}

// The trial's teardown clears what it gave while the hire is still online.
func TestTrialTeardownClearsGivenItems(t *testing.T) {
	prov, s, bags := bagProv(t)
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	var seen []string
	inner := s.tbFn
	s.tbFn = func(line string) []string {
		seen = append(seen, line)
		return inner(line)
	}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2"}
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", RecoveryPath: prov.RecoveryPath, TrialID: "t"}
	r.tbRunner = s.runner
	r.trialTeardown(context.Background(), rep, "t")
	idx := func(prefix string) int {
		for i, l := range seen {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	rm, logout := idx(".tb removeitem Bheal 1708 20"), idx(".tb logout")
	if rm < 0 || (logout >= 0 && rm > logout) {
		t.Fatalf("items must be cleared before the logout: %v", seen)
	}
	if bags.count(1708) != 0 || len(prov.marks().forBot("test2", "Bheal")) != 0 {
		t.Fatal("the bags and the mark must be empty after the teardown clear")
	}
}

// Item (1) of #278: teardown unequips and destroys only the worn items this
// trial added (leaving pre-existing worn/bag items untouched) and frees bag
// space first so the next trial's equip does not hit bags full.
func TestClearGivenUnequipsAndDestroysOnlyTrialAddedWornItems(t *testing.T) {
	prov, _, bags := bagProv(t)
	bags.set(9999, 1)      // pre-existing bag item (not given by trial, must stay)
	bags.setWorn(14, 5555) // pre-existing cloak (must stay)
	bags.setWorn(10, 6666) // pre-existing ring (duplicate entry also equipped in slot 11 below)
	bags.itemSlot[7720] = 0
	bags.itemSlot[9434] = 4
	bags.itemSlot[6666] = 11

	// Trial gives a consumable (1708) and kit items (7720, 9434, 6666) and equips them.
	if _, err := prov.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []uint32{7720, 9434, 6666} {
		if _, err := prov.giveItem(context.Background(), "Bheal", entry, 1); err != nil {
			t.Fatal(err)
		}
		if eq, err := prov.TB.Equip(context.Background(), "Bheal", entry, true); err != nil || eq.Result != "equipped" {
			t.Fatalf("equip %d failed: %v %+v", entry, err, eq)
		}
		prov.marks().setWorn("test2", "Bheal", entry)
	}
	// Now enforce tight 2-slot bag capacity: 9999 (untouchable) + 1708 (trial bag item) occupy
	// all 2 slots, so unequip would fail bags-full unless ClearGiven removes 1708 from bags
	// before unequipping and destroys each unequipped piece before the next unequip.
	bags.bagSlots = 2

	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("ClearGiven must unequip and clear worn trial items: ok=%v %s", ok, ev)
	}
	if bags.count(9999) != 1 || bags.wornAt(14) != 5555 || bags.wornAt(10) != 6666 {
		t.Fatalf("pre-existing items touched: bag9999=%d slot14=%d slot10=%d", bags.count(9999), bags.wornAt(14), bags.wornAt(10))
	}
	if bags.wornAt(0) != 0 || bags.wornAt(4) != 0 || bags.wornAt(11) != 0 {
		t.Fatalf("trial-added worn items still equipped: slot0=%d slot4=%d slot11=%d", bags.wornAt(0), bags.wornAt(4), bags.wornAt(11))
	}
	if bags.count(1708) != 0 || bags.count(7720) != 0 || bags.count(9434) != 0 || bags.count(6666) != 0 {
		t.Fatalf("trial-added items still in bags: 1708=%d 7720=%d 9434=%d 6666=%d",
			bags.count(1708), bags.count(7720), bags.count(9434), bags.count(6666))
	}
	if left := prov.marks().forBot("test2", "Bheal"); len(left) != 0 {
		t.Fatalf("marks must be cleared after unequip+remove: %+v", left)
	}
	// Next trial's give+equip must fit in the freed bag slot without bags-full.
	if _, err := prov.giveItem(context.Background(), "Bheal", 7720, 1); err != nil {
		t.Fatalf("next trial give hit bags full: %v", err)
	}
}
