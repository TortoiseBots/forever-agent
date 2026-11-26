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
// the caller says so. The count is always the ItemScan `inv` (worn plus bag
// copies) and never a parsed `.bot inv` reply: that reply was truncated on
// the live server (2026-11-26), so a clear that depended on it could not see
// the worn copies and left a pool bot's bags full for every later trial.
// A kit piece that was equipped is unequipped through `.bot item <bot>
// unequip 255 <slot>` (the slot the ItemScan record names) and then removed
// from bags. Bag copies are removed first so unequipping has free bag slots.
// A full bag that stays full is the setup row's SKIPPED needs=bot_bag_space,
// never a silent failure.
//
// Setup also clears the trial's own known ids (consumables and kit pieces)
// before it gives them, fail-closed, so an earlier trial's leftovers cannot
// make this trial fail setup even when no mark names them.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
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

func (p *Provisioner) marks() *recoveryMarks {
	p.markOnce.Do(func() { p.recovery = &recoveryMarks{path: p.RecoveryPath} })
	return p.recovery
}

// scanItem reads one item's `ItemScan` record: its id, whether it exists, the
// carried count (the core's GetItemCount over equipment and bags) and the
// equipment slot the core would put it in (when equippable). ok is false when
// the reply cannot be read or does not hold the record.
func (p *Provisioner) scanItem(ctx context.Context, bot string, item uint32) (tbcmd.ItemScanResult, string, bool) {
	tctx, cancel := p.ctxOf(ctx)
	rep, err := p.TB.ItemScan(tctx, bot, item)
	cancel()
	if err != nil {
		return tbcmd.ItemScanResult{}, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s: %v)", item, bot, err), false
	}
	for _, r := range rep.Results {
		if r.Item != item {
			continue
		}
		if !r.Exists {
			// The server holds no such template: no copy can be carried, so
			// the count is 0 (a later exist read still fails a give of it).
			return r, "", true
		}
		if r.HasInv {
			return r, "", true
		}
		return tbcmd.ItemScanResult{}, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s named no carried count)", item, bot), false
	}
	return tbcmd.ItemScanResult{}, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s held no record for it)", item, bot), false
}

// carriedCount reads how many of item the bot carries: the `ItemScan`
// inv, which is the core's GetItemCount over equipment, bags and the reagent
// bank (never the bank storage). ok is false when it cannot be read.
func (p *Provisioner) carriedCount(ctx context.Context, bot string, item uint32) (int, bool) {
	r, _, ok := p.scanItem(ctx, bot, item)
	if !ok {
		return 0, false
	}
	return r.Inv, true
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

// ClearGiven takes back what earlier gives left on bot. Both the count and
// the unequip slot come from `.tb itemscan` (inv counts worn and bag copies
// together); the `.bot inv` reply is never read, so a truncated reply cannot
// hide an item. Non-worn marks are cleared first (their bag copies free bag
// slots), then worn marks unequip only trial-added copies above the baseline
// through `.bot item <bot> unequip 255 <slot>` and remove each one at once.
// Every step is proved by an `ItemScan` read-back and the carried count never
// goes below the pre-trial baseline. An item that cannot be read, unequipped
// or proved removed stays marked and fails closed (`allOK=false`).
func (p *Provisioner) ClearGiven(ctx context.Context, bot string) (string, bool) {
	gives := p.marks().forBot(p.Server, bot)
	if len(gives) == 0 {
		return "no items to clear", true
	}
	allOK := true
	cleared := 0
	var notes []string
	clear := func(g givenItem) {
		if note, ok := p.clearAboveBaseline(ctx, bot, g); !ok {
			allOK = false
			notes = append(notes, note)
			return
		}
		p.marks().drop(g)
		cleared++
	}
	// Non-worn marks first: their bag copies free slots for the unequips the
	// worn marks may need.
	for _, g := range gives {
		if !g.Worn {
			clear(g)
		}
	}
	for _, g := range gives {
		if g.Worn {
			clear(g)
		}
	}
	return formatClearSummary(cleared, notes), allOK
}

// clearAboveBaseline takes back one mark's gives: at most g.Count copies
// above g.Base. Bag copies go first through `.tb removeitem` (the copies a
// give just added); a worn mark then unequips the rest, one copy at a time,
// destroying each copy before the next unequip so it has a free bag slot. A
// non-worn mark never touches equipment.
func (p *Provisioner) clearAboveBaseline(ctx context.Context, bot string, g givenItem) (string, bool) {
	scan, note, ok := p.scanItem(ctx, bot, g.Item)
	if !ok {
		return note, false
	}
	extra := scan.Inv - int(g.Base)
	if extra > int(g.Count) {
		extra = int(g.Count)
	}
	if extra <= 0 {
		return "", true // back at the pre-trial baseline (used up or already gone)
	}
	removed, note, ok := p.emptyItemBags(ctx, bot, g.Item, extra)
	if !ok {
		return note, false
	}
	extra -= removed
	if extra <= 0 {
		return "", true
	}
	if !g.Worn {
		return fmt.Sprintf("item %d: UNVERIFIED (%d above baseline not in the bags; a non-worn mark never touches equipment)", g.Item, extra), false
	}
	if _, note, ok := p.clearWorn(ctx, bot, g.Item, int(g.Base)); !ok {
		return note, false
	}
	return "", true
}

// emptyItemBags removes what the bot's bags hold of item, up to cap copies
// (cap <= 0: no limit), proving each removal by the carried count. It stops
// when the bags and reagent bank hold no copy (the rest is worn) and
// returns how many copies left them.
func (p *Provisioner) emptyItemBags(ctx context.Context, bot string, item uint32, cap int) (int, string, bool) {
	removed := 0
	for range 8 {
		scan, note, ok := p.scanItem(ctx, bot, item)
		if !ok {
			return removed, note, false
		}
		if scan.Inv == 0 {
			return removed, "", true
		}
		ask := scan.Inv
		if cap > 0 {
			if left := cap - removed; left < ask {
				ask = left
			}
			if ask <= 0 {
				return removed, "", true
			}
		}
		n, note, ok := p.removeUpTo(ctx, bot, item, ask)
		if !ok {
			return removed, note, false
		}
		if n == 0 {
			return removed, "", true // no removable copy left: what remains is worn
		}
		removed += n
	}
	return removed, fmt.Sprintf("item %d: UNVERIFIED (bags still hold a copy after 8 removals)", item), false
}

// clearWorn unequips and destroys worn copies of item until the carried
// count reaches floor, one copy at a time: `.bot item <bot> unequip 255
// <slot>` (the slot the ItemScan record names) then `.tb removeitem`. A copy
// that cannot be unequipped or proved removed fails closed.
func (p *Provisioner) clearWorn(ctx context.Context, bot string, item uint32, floor int) (int, string, bool) {
	removed := 0
	for range 8 {
		scan, note, ok := p.scanItem(ctx, bot, item)
		if !ok {
			return removed, note, false
		}
		if scan.Inv <= floor {
			return removed, "", true
		}
		if !scan.CanEquip || !scan.HasSlot {
			why := scan.Why
			if why == "" {
				why = "the item is not equippable"
			}
			return removed, fmt.Sprintf("item %d: UNVERIFIED (%d above the baseline not in the bags: %s)", item, scan.Inv-floor, why), false
		}
		if err := p.unequipBotSlot(ctx, bot, scan.Slot, item); err != nil {
			return removed, fmt.Sprintf("item %d: unequip slot %d failed: %v", item, scan.Slot, err), false
		}
		n, note, ok := p.removeUpTo(ctx, bot, item, 1)
		if !ok {
			return removed, note, false
		}
		if n != 1 {
			return removed, fmt.Sprintf("item %d: unequip slot %d did not put a copy into the bags", item, scan.Slot), false
		}
		removed += n
	}
	return removed, fmt.Sprintf("item %d: UNVERIFIED (still above the baseline after 8 unequips)", item), false
}

// scanItemBatch reads one `ItemScan` for every id (in chunks of the module's
// 40-id limit) and returns the records by item id. ok is false when a reply
// cannot be read; a missing record is left for the caller to report.
func (p *Provisioner) scanItemBatch(ctx context.Context, bot string, ids []uint32) (map[uint32]tbcmd.ItemScanResult, string, bool) {
	out := make(map[uint32]tbcmd.ItemScanResult, len(ids))
	for start := 0; start < len(ids); start += 40 {
		chunk := ids[start:]
		if len(chunk) > 40 {
			chunk = chunk[:40]
		}
		tctx, cancel := p.ctxOf(ctx)
		rep, err := p.TB.ItemScan(tctx, bot, chunk...)
		cancel()
		if err != nil {
			return nil, fmt.Sprintf("itemscan %s: %v", bot, err), false
		}
		for _, r := range rep.Results {
			out[r.Item] = r
		}
	}
	return out, "", true
}

// ClearTrialItems removes every copy of the ids the trial gives (its
// consumables and its kit pieces) before the setup gives them again. An
// earlier trial can leave those ids in the hire's bags and equipment with no
// recovery mark naming them (a run killed between the give and the mark
// write); without this pass the next give hits "bags full" for an item the
// trial itself added. Counts come from `.tb itemscan`, never a `.bot inv`
// reply; one batched read tells which ids carry anything, bag copies of every
// such id go first (so the worn pieces have free slots), then worn copies
// come off one at a time. A count that cannot be read, unequipped or proved
// removed fails the setup row (fail-closed): the trial does not give into
// bags it cannot read.
func (p *Provisioner) ClearTrialItems(ctx context.Context, bot string, ids []uint32) (string, bool) {
	ids = uniqueItemIDs(ids)
	if len(ids) == 0 {
		return "no known trial items", true
	}
	records, note, ok := p.scanItemBatch(ctx, bot, ids)
	if !ok {
		return note, false
	}
	allOK := true
	var notes []string
	var toClear []uint32
	for _, id := range ids {
		r, found := records[id]
		if !found {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s held no record for it)", id, bot))
			continue
		}
		if !r.Exists || (r.HasInv && r.Inv == 0) {
			continue // no template, or nothing carried: no leftover of it
		}
		if !r.HasInv {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s named no carried count)", id, bot))
			continue
		}
		toClear = append(toClear, id)
	}
	cleared := 0
	bagFailed := map[uint32]bool{}
	for _, id := range toClear {
		n, note, ok := p.emptyItemBags(ctx, bot, id, 0)
		cleared += n
		if !ok {
			allOK = false
			bagFailed[id] = true
			notes = append(notes, note)
		}
	}
	for _, id := range toClear {
		if bagFailed[id] {
			continue
		}
		if r, note, ok := p.clearWorn(ctx, bot, id, 0); !ok {
			allOK = false
			notes = append(notes, note)
		} else {
			cleared += r
		}
	}
	if cleared == 0 && len(notes) == 0 {
		return "no trial items left over", true
	}
	var parts []string
	if cleared > 0 {
		parts = append(parts, fmt.Sprintf("cleared %d leftover item(s)", cleared))
	}
	parts = append(parts, notes...)
	return strings.Join(parts, "; "), allOK
}

// trialItemIDs is every item id a trial gives a hire: the consumable luggage
// and, when the spec asks for a gear kit, the kit's pieces.
func trialItemIDs(spec BotSpec) []uint32 {
	ids := itemIDs(spec.Items)
	if spec.Gear != "" {
		for _, it := range botGearKit(spec.Class, spec.Gear, spec.Level) {
			ids = append(ids, it.Entry)
		}
	}
	return uniqueItemIDs(ids)
}

// uniqueItemIDs drops id 0 and duplicates, sorted so the reads are stable.
func uniqueItemIDs(ids []uint32) []uint32 {
	seen := make(map[uint32]bool, len(ids))
	out := make([]uint32, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
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

// removeUpTo asks the module to take at most n copies of item out of the bags
// (`.tb removeitem <bot> <id> <n>`; the module takes only what the bags hold)
// and proves it with an `ItemScan`: the carried count must fall by exactly the
// number the module answered it removed.
func (p *Provisioner) removeUpTo(ctx context.Context, bot string, item uint32, n int) (int, string, bool) {
	if n <= 0 {
		return 0, "", true
	}
	before, known := p.carriedCount(ctx, bot, item)
	if !known {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (carried count unreadable before removing %d)", item, n), false
	}
	tctx, cancel := p.ctxOf(ctx)
	res, lines, rerr := p.TB.FreeTextRemoveItem(tctx, bot, item, uint32(n))
	cancel()
	p.Ledger.Add(map[string]any{"kind": "removeitem", "bot": bot, "item_id": item, "count": n,
		"removed": res.Removed, "left": res.Left, "reply": firstLines(lines)})
	if rerr != nil {
		return 0, fmt.Sprintf("item %d: remove refused: %s", item, tbErrText(rerr, lines)), false
	}
	after, ok := p.carriedCount(ctx, bot, item)
	if !ok || res.Removed > n || after != before-res.Removed {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (carried %d before, %d after the module removed %d of %d)",
			item, before, after, res.Removed, n), false
	}
	return res.Removed, "", true
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
