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

// fakeItem is one copy (one item instance) the fake server holds: its own
// GUID, the item it is and where it sits (a bag slot, or an equipment slot
// when worn).
type fakeItem struct {
	guid  string
	item  uint32
	bag   int
	slot  int
	count int
	worn  bool
}

// bagModel is a hire's bags as the real module shows them: itemscan reports
// the copies by their own GUIDs (instances=), `.tb additem <bot> <id> [n]`
// adds n (bags full past cap), `.tb removeitem <bot> guid <hex>` destroys one
// named instance and answers the real record shape, and `.tb equip` wears a
// copy without changing its GUID. The old negative-count additem is NOT a
// removal here.
type bagModel struct {
	mu       sync.Mutex
	inv      map[uint32]int
	worn     map[int]uint32
	itemSlot map[uint32]int
	cap      int
	bagSlots int
	lines    []string
	items    []*fakeItem
	nextGUID uint64
	// noGuids makes itemscan answer without an instances= list (a module that
	// cannot name its copies); mergeAdds makes an additem merge into the
	// hire's existing bag stack (no new instance); refuseRemoval makes the
	// guid removal answer a refusal; ignoreRemoval acknowledges one but
	// leaves the copy.
	noGuids       bool
	mergeAdds     bool
	refuseRemoval bool
	ignoreRemoval bool
	// noCreated makes additem answer only the human line, without the
	// `additem ... created=` record (a module that does not name what a give
	// created); refuseAdd refuses every additem; lootDuringAdd puts a second,
	// unrelated copy of the same item into the bags while the give runs (a
	// loot or a mail landing between two scans): it is never in created=;
	// noGuidUnequip refuses `.tb unequip <bot> guid` (a module that cannot
	// unequip by instance); swapBeforeUnequip makes a replacement copy of the
	// worn item take its slot (the recorded copy goes to the bags) the moment
	// any unequip command arrives, before the command acts.
	noCreated         bool
	refuseAdd         bool
	lootDuringAdd     bool
	noGuidUnequip     bool
	swapBeforeUnequip bool
}

// swapWornLocked replaces the worn copy in slot with a new copy of the same
// item and moves the old one into the bags. It returns the replacement.
func (b *bagModel) swapWornLocked(slot int) *fakeItem {
	for _, it := range b.items {
		if it.worn && it.slot == slot {
			it.worn = false
			it.bag = 255
			it.slot = b.freeBagSlotLocked()
			b.inv[it.item]++
			return b.newItemLocked(it.item, 255, slot, 1, true)
		}
	}
	return nil
}

func newBagModel() *bagModel {
	return &bagModel{inv: map[uint32]int{}, worn: map[int]uint32{}, itemSlot: map[uint32]int{}, cap: 1000}
}

// newItemLocked creates one instance with a deterministic, unique GUID.
func (b *bagModel) newItemLocked(item uint32, bag, slot, count int, worn bool) *fakeItem {
	b.nextGUID++
	it := &fakeItem{guid: fmt.Sprintf("%032x", 0x4000000000000000+b.nextGUID), item: item, bag: bag, slot: slot, count: count, worn: worn}
	b.items = append(b.items, it)
	return it
}

// freeBagSlotLocked is the first bag slot (23 upward, as the fake's
// `.bot inv` shows them) no instance holds.
func (b *bagModel) freeBagSlotLocked() int {
	used := map[int]bool{}
	for _, it := range b.items {
		if !it.worn {
			used[it.slot] = true
		}
	}
	for slot := 23; ; slot++ {
		if !used[slot] {
			return slot
		}
	}
}

// dropItemLocked forgets every instance of item.
func (b *bagModel) dropItemLocked(item uint32) {
	keep := b.items[:0]
	for _, it := range b.items {
		if it.item != item {
			keep = append(keep, it)
		}
	}
	b.items = keep
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
		keep := b.items[:0]
		for _, it := range b.items {
			if it.worn && it.slot == slot {
				continue
			}
			keep = append(keep, it)
		}
		b.items = keep
		b.newItemLocked(item, 255, slot, 1, true)
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
	b.dropItemLocked(item)
	if n == 0 {
		delete(b.inv, item)
	} else {
		b.inv[item] = n
		b.newItemLocked(item, 255, b.freeBagSlotLocked(), n, false)
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

// instanceListLocked renders the item's copies as the module's instances=
// value: `<guid>:<bag>:<slot>:<count>` separated by `;`.
func (b *bagModel) instanceListLocked(item uint32) string {
	var parts []string
	for _, it := range b.items {
		if it.item != item {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d:%d", it.guid, it.bag, it.slot, it.count))
	}
	return strings.Join(parts, ";")
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
			instances := ""
			if !b.noGuids {
				instances = " instances=" + b.instanceListLocked(uint32(id))
			}
			if slot, ok := b.itemSlot[uint32(id)]; ok {
				canEquip++
				out = append(out, fmt.Sprintf("itemscan bot=%s item=%d exists=yes name=Item%%20%d class=4:1 inv=%d req=0 quality=3 ilvl=40 canequip=yes slot=%d why=%s #T", f[2], id, id, total, slot, instances))
			} else {
				out = append(out, fmt.Sprintf("itemscan bot=%s item=%d exists=yes name=Item%%20%d class=0:5 inv=%d req=0 quality=1 ilvl=5 canequip=no why=%s #T", f[2], id, id, total, instances))
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
		item := uint32(id)
		if b.lootDuringAdd {
			// A foreign copy lands while the give runs: the bags hold it,
			// the give's own record never names it.
			b.newItemLocked(item, 255, b.freeBagSlotLocked(), 1, false)
			b.inv[item]++
		}
		if b.refuseAdd {
			return []string{"TortoiseBots: " + f[2] + ": the item was not stored (refused)."}
		}
		if b.inv[item]+n > b.cap || (b.bagSlots > 0 && b.inv[item] == 0 && b.usedBagSlotsLocked() >= b.bagSlots) {
			return []string{"TortoiseBots: " + f[2] + ": bags full, the item was not stored."}
		}
		merged := false
		if b.mergeAdds {
			for _, it := range b.items {
				if it.item == item && !it.worn {
					it.count += n
					merged = true
					break
				}
			}
		}
		created := ""
		if !merged {
			created = b.newItemLocked(item, 255, b.freeBagSlotLocked(), n, false).guid
		}
		b.inv[item] += n
		out := []string{fmt.Sprintf("TortoiseBots: %s now has %d of item %d.", f[2], b.inv[item], item)}
		if !b.noCreated {
			out = append(out, fmt.Sprintf("additem bot=%s item=%d created=%s", f[2], item, created))
		}
		return out
	case "unequip":
		b.lines = append(b.lines, line)
		if len(f) != 5 || f[3] != "guid" || b.noGuidUnequip {
			return []string{"TortoiseBots: usage: .tb unequip <character> guid <item guid>"}
		}
		want := strings.ToLower(f[4])
		for _, it := range b.items {
			if it.worn && b.swapBeforeUnequip {
				b.swapWornLocked(it.slot)
				b.swapBeforeUnequip = false
				break
			}
		}
		// The module checks the named instance in the slot at the moment of
		// the move: a copy that is not worn moves nothing.
		for _, it := range b.items {
			if it.guid != want {
				continue
			}
			if !it.worn {
				return []string{fmt.Sprintf("unequip bot=%s guid=%s moved=0", f[2], want)}
			}
			delete(b.worn, it.slot)
			b.inv[it.item]++
			it.worn = false
			it.bag = 255
			it.slot = b.freeBagSlotLocked()
			return []string{fmt.Sprintf("unequip bot=%s guid=%s moved=1", f[2], want)}
		}
		return []string{fmt.Sprintf("unequip bot=%s guid=%s moved=0", f[2], want)}
	case "removeitem":
		b.lines = append(b.lines, line)
		if len(f) > 3 && f[3] == "guid" {
			want := strings.ToLower(f[4])
			for i, it := range b.items {
				if it.guid != want {
					continue
				}
				if b.refuseRemoval {
					return []string{"TortoiseBots: item guid " + want + " is not in the bags."}
				}
				if !b.ignoreRemoval {
					if !it.worn {
						b.inv[it.item] -= it.count
						if b.inv[it.item] <= 0 {
							delete(b.inv, it.item)
						}
					}
					b.items = append(b.items[:i], b.items[i+1:]...)
				}
				return []string{fmt.Sprintf("removeitem bot=%s item=%d guid=%s removed=1 left=%d", f[2], it.item, want, b.inv[it.item])}
			}
			return []string{"TortoiseBots: item guid " + want + " is not held."}
		}
		return []string{}
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
		// The first bag instance moves to the slot; the copy keeps its GUID.
		var moved *fakeItem
		for _, it := range b.items {
			if it.item == uint32(id) && !it.worn {
				moved = it
				break
			}
		}
		prev := b.worn[slot]
		if prev != 0 {
			b.inv[prev]++
			b.newItemLocked(prev, 255, b.freeBagSlotLocked(), 1, false)
			var keep []*fakeItem
			for _, it := range b.items {
				if it.worn && it.slot == slot {
					continue
				}
				keep = append(keep, it)
			}
			b.items = keep
		}
		b.worn[slot] = uint32(id)
		b.inv[uint32(id)]--
		if b.inv[uint32(id)] == 0 {
			delete(b.inv, uint32(id))
		}
		if moved != nil {
			moved.worn = true
			moved.slot = slot
			moved.count = 1
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
		if b.swapBeforeUnequip {
			if s, err := strconv.Atoi(f[5]); err == nil {
				b.swapWornLocked(s)
			}
			b.swapBeforeUnequip = false
		}
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
		for _, it := range b.items {
			if it.worn && it.slot == slot && it.item == item {
				it.worn = false
				it.bag = 255
				it.slot = b.freeBagSlotLocked()
				break
			}
		}
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
	prov.GrantPath = filepath.Join(t.TempDir(), "grants.json")
	return prov, s, bags
}
