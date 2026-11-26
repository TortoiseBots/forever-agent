package live

// items.go: the items a trial gives a hire are taken back again.
//
// `.tb additem` fills the hire's bags; a killed run used to leave them full,
// and the next run's additem then failed with "bags full". A recovery mark (a
// file under the state dir, so it survives a killed run) is written only
// AFTER a give is confirmed by an `ItemScan` read-back, and it names the
// server, the bot, the exact item id, the count the bags really gained and
// the carried count the hire had before (the baseline: bags plus worn). The
// teardown and the next setup take back at most that count, through the
// module's own `.tb removeitem`, and never reduce the carried count below
// the baseline, so the number of items the hire had before the trial is
// preserved. A clear is proved by an `ItemScan` read-back (the count back at
// the baseline); an item that cannot be proved gone stays in the mark and
// the caller says so. A kit piece that was equipped is unequipped first
// through `.bot item <bot> unequip 255 <slot>` (read from `.bot inv <bot>`)
// and then removed from bags. Bag copies are removed first so unequipping
// has free bag slots. A full bag that stays full is the setup row's SKIPPED
// needs=bot_bag_space, never a silent failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pjerra/forever-agent/internal/tbm"
)

// givenItem is one confirmed give: Count is the bag increase the read-back
// proved, Base the carried count (bags plus worn) before the first give.
// Worn is set once the piece was equipped.
type givenItem struct {
	Server string `json:"server"`
	Bot    string `json:"bot"`
	Item   uint32 `json:"item"`
	Count  uint32 `json:"count"`
	Base   uint32 `json:"base"`
	Worn   bool   `json:"worn,omitempty"`
}

// sameGive reports whether two marks name the same server, bot and item.
func sameGive(a givenItem, server, bot string, item uint32) bool {
	return strings.EqualFold(a.Server, server) && strings.EqualFold(a.Bot, bot) && a.Item == item
}

// recoveryMarks is the recovery mark file of one player's trials.
type recoveryMarks struct {
	path string
	mu   sync.Mutex
	mem  []givenItem // used when path is empty
}

var recoveryMu sync.Mutex // one process, one file: serialise read-modify-write

// RecoveryPathFor is the recovery mark file of a player's trials on one
// server: <state dir>/items-recovery-<server>-<player>.json, beside the
// arena locks. The server is in the name: the same character name on two
// servers must not share marks.
func RecoveryPathFor(server, player string) string {
	safe := func(in string) string {
		return strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == 0 || r == ':' {
				return '_'
			}
			return r
		}, in)
	}
	return filepath.Join(ArenaStateDir(), "items-recovery-"+safe(server)+"-"+safe(player)+".json")
}

func (m *recoveryMarks) load() []givenItem {
	if m.path == "" {
		return append([]givenItem(nil), m.mem...)
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return nil
	}
	var out []givenItem
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func (m *recoveryMarks) store(list []givenItem) error {
	if m.path == "" {
		m.mem = append([]givenItem(nil), list...)
		return nil
	}
	if len(list) == 0 {
		if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	raw, _ := json.Marshal(list)
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// add records one confirmed give; a later give of the same item adds to the
// count and keeps the first baseline.
func (m *recoveryMarks) add(g givenItem) error {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for i := range list {
		if sameGive(list[i], g.Server, g.Bot, g.Item) {
			list[i].Count += g.Count
			return m.store(list)
		}
	}
	return m.store(append(list, g))
}

// forBot lists the marked gives of one bot on one server.
func (m *recoveryMarks) forBot(server, bot string) []givenItem {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []givenItem
	for _, g := range m.load() {
		if strings.EqualFold(g.Server, server) && strings.EqualFold(g.Bot, bot) {
			out = append(out, g)
		}
	}
	return out
}

// drop removes one proved-cleared give from the mark.
func (m *recoveryMarks) drop(g givenItem) {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var keep []givenItem
	for _, e := range m.load() {
		if !sameGive(e, g.Server, g.Bot, g.Item) {
			keep = append(keep, e)
		}
	}
	_ = m.store(keep)
}

// setWorn flags one marked item as equipped (out of the bags).
func (m *recoveryMarks) setWorn(server, bot string, item uint32) {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for i := range list {
		if sameGive(list[i], server, bot, item) {
			list[i].Worn = true
		}
	}
	_ = m.store(list)
}

// reduce subtracts n cleared copies from one mark, dropping it when none remain.
func (m *recoveryMarks) reduce(g givenItem, n uint32) {
	if n == 0 {
		return
	}
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	var keep []givenItem
	for _, e := range list {
		if !sameGive(e, g.Server, g.Bot, g.Item) {
			keep = append(keep, e)
			continue
		}
		if e.Count > n {
			e.Count -= n
			keep = append(keep, e)
		}
	}
	_ = m.store(keep)
}

func (p *Provisioner) marks() *recoveryMarks {
	p.markOnce.Do(func() { p.recovery = &recoveryMarks{path: p.RecoveryPath} })
	return p.recovery
}

// carriedCount reads how many of item the bot carries: the `ItemScan`
// inv, which is the core's GetItemCount over equipment, bags and the
// reagent bank. ok is false when it cannot be read.
func (p *Provisioner) carriedCount(ctx context.Context, bot string, item uint32) (int, bool) {
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	rep, err := p.TB.ItemScan(tctx, bot, item)
	if err != nil {
		return 0, false
	}
	for _, r := range rep.Results {
		if r.Item == item && r.Exists && r.HasInv {
			return r.Inv, true
		}
	}
	return 0, false
}

// giveItem sends one `.tb additem` and marks it only once the bags prove it:
// the carried count is read before, and again after whatever the reply said
// (a lost reply may still have applied). The mark carries the gained count
// and the baseline. A give whose bags did not grow is never marked, so a
// clear can never remove items the trial did not give. Without a readable
// baseline nothing is given.
func (p *Provisioner) giveItem(ctx context.Context, bot string, item uint32, count uint32) ([]string, error) {
	if count == 0 {
		count = 1
	}
	base, known := p.carriedCount(ctx, bot, item)
	if !known {
		return nil, fmt.Errorf("nothing given: the bag count of item %d on %s is unreadable (UNVERIFIED baseline for the recovery mark)", item, bot)
	}
	tctx, cancel := p.ctxOf(ctx)
	lines, err := p.TB.FreeTextAddItem(tctx, bot, item, count)
	cancel()
	after, ok := p.carriedCount(ctx, bot, item)
	if ok && after > base {
		gained := uint32(after - base)
		if merr := p.marks().add(givenItem{Server: p.Server, Bot: bot, Item: item, Count: gained, Base: uint32(base)}); merr != nil {
			return lines, fmt.Errorf("recovery mark not written for %d given item(s) %d: %w", gained, item, merr)
		}
		if err != nil {
			return lines, err
		}
		if gained < count {
			return lines, fmt.Errorf("only %d of %d item %d reached the bags", gained, count, item)
		}
		return lines, nil
	}
	if err != nil {
		return lines, err
	}
	if !ok {
		return lines, fmt.Errorf("the give of item %d on %s is UNVERIFIED: the bag count is unreadable afterwards", item, bot)
	}
	return lines, fmt.Errorf("additem answered but the bags show no new item %d on %s (%d before, %d after)", item, bot, base, after)
}

// ClearGiven takes back what earlier gives left on bot: first it removes
// non-worn bag items with `.tb removeitem` (freeing bag space for unequipping),
// then it reads `.bot inv <bot>` and for any remaining or worn marked items it
// unequips only trial-added copies above the baseline via
// `.bot item <bot> unequip 255 <slot>` and removes them with `.tb removeitem`.
// Each removal is verified by an `ItemScan` read-back and never reduces the
// carried count (bags plus worn) below the pre-trial baseline. An item that cannot be read,
// unequipped, or proved removed stays marked and fails closed (`allOK=false`).
func (p *Provisioner) ClearGiven(ctx context.Context, bot string) (string, bool) {
	gives := p.marks().forBot(p.Server, bot)
	if len(gives) == 0 {
		return "no items to clear", true
	}
	allOK := true
	cleared := 0
	var notes []string
	var deferred []givenItem

	// Pass 1: remove non-worn marked items from the bags first so bag slots
	// open up before any worn kit pieces are unequipped.
	for _, g := range gives {
		if g.Worn {
			deferred = append(deferred, g)
			continue
		}
		inv, known := p.carriedCount(ctx, bot, g.Item)
		if !known {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (carried count unreadable)", g.Item))
			continue
		}
		extra := inv - int(g.Base)
		if extra > int(g.Count) {
			extra = int(g.Count)
		}
		if extra <= 0 {
			p.marks().drop(g) // back at the pre-trial baseline (used up or already gone)
			cleared++
			continue
		}
		removed, note, verified := p.removeFromBags(ctx, bot, g.Item, extra, inv)
		if removed > 0 {
			p.marks().reduce(g, uint32(removed))
		}
		if !verified {
			// The mark was never worn: only bag copies above the baseline may
			// be taken back, so equipment is never touched on its behalf.
			allOK = false
			notes = append(notes, note)
			continue
		}
		p.marks().drop(g)
		cleared++
	}

	if len(deferred) == 0 {
		return formatClearSummary(cleared, notes), allOK
	}

	// Pass 2: read `.bot inv <bot>`, remove any bag-held excess of deferred marks first,
	// then unequip and remove one trial-added worn copy at a time.
	binv, invErr := p.botInventory(ctx, bot)
	for _, g := range deferred {
		inv, known := p.carriedCount(ctx, bot, g.Item)
		if !known {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (carried count unreadable)", g.Item))
			continue
		}
		if invErr != nil {
			extra := inv - int(g.Base)
			if extra > int(g.Count) {
				extra = int(g.Count)
			}
			allOK = false
			if extra > 0 {
				notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (cannot read .bot inv to clear %d: %v)", g.Item, extra, invErr))
			} else {
				notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (cannot read .bot inv for worn mark: %v)", g.Item, invErr))
			}
			continue
		}
		eqSlots := equippedSlotsFor(binv, g.Item)
		bagHeld := bagCountFor(binv, g.Item)
		totalCarried := inv
		if sum := bagHeld + len(eqSlots); sum > totalCarried {
			totalCarried = sum
		}
		extra := totalCarried - int(g.Base)
		if extra > int(g.Count) {
			extra = int(g.Count)
		}
		if extra <= 0 {
			p.marks().drop(g)
			cleared++
			continue
		}
		removed := 0
		// The mark keeps the carried baseline (bags plus worn) and the gained
		// count, so at most `extra` copies are ever taken. Bag copies go first:
		// they are the copies a give just added; equipment is touched only for
		// the rest, because a worn copy of the entry may predate the trial.
		fromBags := bagHeld
		if fromBags > extra {
			fromBags = extra
		}
		if fromBags > 0 {
			n, note, ok := p.removeFromBags(ctx, bot, g.Item, fromBags, inv)
			if n > 0 {
				p.marks().reduce(g, uint32(n))
				removed += n
			}
			if !ok {
				allOK = false
				notes = append(notes, note)
				continue
			}
		}
		rem := extra - removed
		bagHeldNow := bagHeld - fromBags
		if rem > 0 {
			if len(eqSlots) == 0 {
				allOK = false
				notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (%d above baseline not found in bags or equipment)", g.Item, rem))
				continue
			}
			toUnequip := rem
			if len(eqSlots) < toUnequip {
				toUnequip = len(eqSlots)
			}
			unequipFailed := false
			// Unequip from highest slot index downward so lower-slot pre-existing copies stay worn.
			for i := range toUnequip {
				slot := eqSlots[len(eqSlots)-1-i]
				carriedBefore, okBefore := p.carriedCount(ctx, bot, g.Item)
				if !okBefore {
					allOK = false
					notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (carried count before unequipping slot %d unreadable)", g.Item, slot))
					unequipFailed = true
					break
				}
				if uerr := p.unequipBotSlot(ctx, bot, slot, g.Item); uerr != nil {
					allOK = false
					notes = append(notes, fmt.Sprintf("item %d: unequip slot %d failed: %v", g.Item, slot, uerr))
					unequipFailed = true
					break
				}
				// The unequip must have put exactly one copy into the bags: the
				// carried count alone cannot prove it (it already counted the
				// worn copy), so the bags are read through `.bot inv`. A silent
				// refusal (bags full) would otherwise let removeitem take a copy
				// the bot had before the trial.
				afterBinv, berr := p.botInventory(ctx, bot)
				bagNow := bagCountFor(afterBinv, g.Item)
				if berr != nil || bagNow != bagHeldNow+1 {
					allOK = false
					why := ""
					if berr != nil {
						why = ": " + berr.Error()
					}
					notes = append(notes, fmt.Sprintf("item %d: unequip slot %d not proven (bags %d before, %d after%s)",
						g.Item, slot, bagHeldNow, bagNow, why))
					unequipFailed = true
					break
				}
				n, note, ok := p.removeFromBags(ctx, bot, g.Item, 1, carriedBefore)
				if n > 0 {
					p.marks().reduce(g, uint32(n))
					removed += n
					bagHeldNow = bagNow - n
				}
				if !ok {
					allOK = false
					notes = append(notes, note)
					unequipFailed = true
					break
				}
			}
			if unequipFailed {
				continue
			}
			if rem > toUnequip {
				allOK = false
				notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (%d marked item(s) still unaccounted)", g.Item, rem-toUnequip))
				continue
			}
		}
		after, ok := p.carriedCount(ctx, bot, g.Item)
		if !ok || after > totalCarried-extra {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (carried %d before, %d after removing %d)", g.Item, totalCarried, after, extra))
			continue
		}
		p.marks().drop(g)
		cleared++
	}
	return formatClearSummary(cleared, notes), allOK
}

func formatClearSummary(cleared int, notes []string) string {
	var parts []string
	if cleared > 0 {
		parts = append(parts, fmt.Sprintf("cleared %d marked item(s)", cleared))
	}
	parts = append(parts, notes...)
	if len(parts) == 0 {
		parts = append(parts, "no items to clear")
	}
	return strings.Join(parts, "; ")
}

func (p *Provisioner) removeFromBags(ctx context.Context, bot string, item uint32, count int, before int) (int, string, bool) {
	tctx, cancel := p.ctxOf(ctx)
	res, lines, rerr := p.TB.FreeTextRemoveItem(tctx, bot, item, uint32(count))
	cancel()
	p.Ledger.Add(map[string]any{"kind": "removeitem", "bot": bot, "item_id": item, "count": count,
		"removed": res.Removed, "left": res.Left, "reply": firstLines(lines)})
	if rerr != nil {
		return 0, fmt.Sprintf("item %d: remove refused: %s", item, tbErrText(rerr, lines)), false
	}
	after, ok := p.carriedCount(ctx, bot, item)
	if !ok || after > before-count {
		removed := 0
		if ok && before > after {
			removed = before - after
		}
		return removed, fmt.Sprintf("item %d: UNVERIFIED (carried %d before, %d after removing %d)", item, before, after, count), false
	}
	return count, "", true
}

func (p *Provisioner) botInventory(ctx context.Context, bot string) (tbm.Inventory, error) {
	if p.Client == nil {
		return tbm.Inventory{}, fmt.Errorf("no client for .bot inv")
	}
	tctx, cancel := p.ctxOf(ctx)
	res, err := p.Client.botLine(tctx, ".bot inv "+bot, 5000)
	cancel()
	if err != nil {
		return tbm.Inventory{}, err
	}
	inv := tbm.ParseInventory(res.Lines)
	if inv.Err != nil {
		return tbm.Inventory{}, fmt.Errorf("%s: %s", inv.Err.Code, inv.Err.Message)
	}
	if !inv.Complete {
		return tbm.Inventory{}, fmt.Errorf("incomplete .bot inv reply")
	}
	return inv, nil
}

func (p *Provisioner) unequipBotSlot(ctx context.Context, bot string, slot int, item uint32) error {
	if p.Client == nil {
		return fmt.Errorf("no client for .bot item unequip")
	}
	tctx, cancel := p.ctxOf(ctx)
	res, err := p.Client.botLine(tctx, fmt.Sprintf(".bot item %s unequip 255 %d", bot, slot), 5000)
	cancel()
	p.Ledger.Add(map[string]any{"kind": "unequip", "bot": bot, "bag": 255, "slot": slot, "item_id": item, "reply": firstLines(res.Lines)})
	if err != nil {
		return err
	}
	for _, l := range res.Lines {
		if e, ok := tbm.ParseErr(l); ok {
			return fmt.Errorf("%s: %s", e.Code, e.Message)
		}
		if ack, ok := tbm.ParseAck(l); ok && ack.Intent == "item unequip" {
			return nil
		}
	}
	return fmt.Errorf("no ACTION_ACK for .bot item %s unequip 255 %d", bot, slot)
}

func equippedSlotsFor(inv tbm.Inventory, item uint32) []int {
	var slots []int
	for slot, e := range inv.Equipped {
		if e == item {
			slots = append(slots, slot)
		}
	}
	sort.Ints(slots)
	return slots
}

func bagCountFor(inv tbm.Inventory, item uint32) int {
	n := 0
	for _, it := range inv.Items {
		if it.Entry == item {
			n += it.Count
		}
	}
	return n
}

// isBagsFull reports the module's "bags full" refusal.
func isBagsFull(err error, lines []string) bool {
	text := strings.ToLower(strings.Join(lines, "\n"))
	if err != nil {
		text += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(text, "bags full") || strings.Contains(text, "bags are full")
}

// itemsGiveRow is the bot_items row for a refused additem: a full bag that
// clearing did not free is SKIPPED with the reason, any other refusal is a
// plain failed row.
func itemsGiveRow(bot, item string, ids any, err error, lines []string, clearEvidence string, clearOK bool) map[string]any {
	if isBagsFull(err, lines) {
		return rowOf("bot_items", false, ids, fmt.Sprintf(
			"SKIPPED needs=bot_bag_space: .tb additem %s %s: bags full (clearing leftover items: %s)", bot, item, clearEvidence))
	}
	return rowOf("bot_items", false, ids, ".tb additem "+bot+" "+item+": "+tbErrText(err, lines))
}
