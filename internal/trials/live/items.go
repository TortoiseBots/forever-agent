package live

// items.go: the items a trial gives a hire are taken back again.
//
// One rule: never remove or unequip a copy that is not proved to be one the
// trial added. The proof is a grant record, a file under the state dir so it
// survives a killed run. The record is written BEFORE the `.tb additem`: it
// names the server, the bot, the item id, its own grant serial, the carried
// count read right before the give (the baseline: bags plus worn plus the
// reagent bank) and the count the give asked for. A read-back that proves the
// count grew turns the intent into a confirmed count; a give whose read-back
// did not grow the count retires its own record. An id with no record is
// never touched: no record means no baseline and no grant, so nothing proves
// a copy there is the trial's.
//
// A record is scoped to one grant and retired when its copies are back. No
// baseline outlives its grant, so a hire that acquires copies between trials
// keeps them: the next give reads the holdings fresh, and they become its own
// baseline. A clear takes back at most the copies the grant proved it added
// (an unconfirmed grant's intent is never cleared: no copy is proved), never
// lowers the carried count below that grant's baseline, and re-reads the count
// in the very step that removes, one copy at a time: a count that changed
// between reads can never be crossed by a stale budget. The count is always
// the `ItemScan` inv (worn plus bag copies plus the reagent bank) and never a
// parsed `.bot inv` reply, which the live server truncated (2026-11-26); only
// the slot-occupant check reads `.bot inv`, and only a complete reply proves
// anything.
//
// A worn kit piece is unequipped only after a complete `.bot inv` reply proves
// that the item sits in an equipment slot, through `.bot item <bot> unequip
// 255 <slot>`; a slot that cannot be proved holding the item is never touched.
// Bag copies are removed first so unequipping has free bag slots. A full bag
// that stays full is the setup row's SKIPPED needs=bot_bag_space, never a
// silent failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/tbm"
)

// givenItem is one grant: Count is the increase a read-back proved, Base the
// carried count read right before the additem, Intent the count it asked for.
// Count 0 with a grant serial is a give whose read-back never confirmed it:
// no copy is proved, so nothing of it may be cleared. Grant 0 marks a record
// of an older build (which wrote only confirmed gives); a grant-less record
// with Count 0 is a kept baseline of an older build and is dropped on load,
// because it proves no outstanding copy.
type givenItem struct {
	Server string `json:"server"`
	Bot    string `json:"bot"`
	Item   uint32 `json:"item"`
	Grant  uint64 `json:"grant,omitempty"`
	Base   uint32 `json:"base"`
	Intent uint32 `json:"intent,omitempty"`
	Count  uint32 `json:"count"`
	Worn   bool   `json:"worn,omitempty"`
}

// sameGive reports whether two records name the same server, bot and item.
func sameGive(a givenItem, server, bot string, item uint32) bool {
	return strings.EqualFold(a.Server, server) && strings.EqualFold(a.Bot, bot) && a.Item == item
}

// sameRecord reports whether two records are the same stored record.
func sameRecord(a, b givenItem) bool {
	return strings.EqualFold(a.Server, b.Server) && strings.EqualFold(a.Bot, b.Bot) &&
		a.Item == b.Item && a.Grant == b.Grant && a.Base == b.Base &&
		a.Intent == b.Intent && a.Count == b.Count
}

// recoveryMarks is the recovery mark file of one player's trials.
type recoveryMarks struct {
	path string
	mu   sync.Mutex
	mem  []givenItem // used when path is empty
}

var recoveryMu sync.Mutex // one process, one file: serialise read-modify-write

var grantSeq atomic.Uint64

// nextGrant is a serial unique in the process: a later grant of the same item
// never shares a record with an earlier one.
func nextGrant() uint64 {
	return uint64(time.Now().UnixNano())<<8 | (grantSeq.Add(1) & 0xff)
}

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

// load reads the mark file. A missing file is no marks; an unreadable file or
// one that is not a valid list is an error, never an empty list: an empty list
// would read as "nothing was ever given" and authorize clearing.
func (m *recoveryMarks) load() ([]givenItem, error) {
	if m.path == "" {
		return append([]givenItem(nil), m.mem...), nil
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", m.path, err)
	}
	var out []givenItem
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", m.path, err)
	}
	kept := out[:0]
	for i, g := range out {
		if g.Item == 0 || strings.TrimSpace(g.Server) == "" || strings.TrimSpace(g.Bot) == "" {
			return nil, fmt.Errorf("%s: record %d names no server, bot or item", m.path, i+1)
		}
		// A grant-less zero-count record is a kept baseline of an older
		// build: it proves no outstanding copy and must never act as a
		// floor another id is cleared against.
		if g.Grant == 0 && g.Count == 0 {
			continue
		}
		kept = append(kept, g)
	}
	return kept, nil
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

// begin records a grant before its additem is sent and returns it with its
// grant serial; nothing about the give is proved yet.
func (m *recoveryMarks) begin(g givenItem) (givenItem, error) {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := m.load()
	if err != nil {
		return g, err
	}
	if g.Grant == 0 {
		g.Grant = nextGrant()
	}
	g.Count, g.Worn = 0, false
	list = append(list, g)
	if err := m.store(list); err != nil {
		return g, err
	}
	return g, nil
}

// confirm records the copies a read-back proved the grant added.
func (m *recoveryMarks) confirm(grant uint64, gained uint32) error {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := m.load()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].Grant == grant {
			list[i].Count = gained
			return m.store(list)
		}
	}
	return fmt.Errorf("grant %d is not in the recovery marks", grant)
}

// retireAll removes the settled records, in one write. A record that changed
// since it was read is left in place.
func (m *recoveryMarks) retireAll(settled []givenItem) error {
	if len(settled) == 0 {
		return nil
	}
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := m.load()
	if err != nil {
		return err
	}
	keep := list[:0]
	for _, e := range list {
		drop := false
		for _, s := range settled {
			if sameRecord(e, s) {
				drop = true
				break
			}
		}
		if !drop {
			keep = append(keep, e)
		}
	}
	return m.store(keep)
}

// setWorn flags one marked item as equipped (out of the bags).
func (m *recoveryMarks) setWorn(server, bot string, item uint32) error {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := m.load()
	if err != nil {
		return err
	}
	for i := range list {
		if sameGive(list[i], server, bot, item) {
			list[i].Worn = true
		}
	}
	return m.store(list)
}

// forBot lists the marks of one bot on one server. It is the read-only view;
// a clear uses forBotErr and fails closed on an unreadable file.
func (m *recoveryMarks) forBot(server, bot string) []givenItem {
	list, _ := m.forBotErr(server, bot)
	return list
}

// forBotErr is forBot with the file error: a clear must not read an
// unreadable mark file as "nothing to clear".
func (m *recoveryMarks) forBotErr(server, bot string) ([]givenItem, error) {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := m.load()
	if err != nil {
		return nil, err
	}
	var out []givenItem
	for _, g := range list {
		if strings.EqualFold(g.Server, server) && strings.EqualFold(g.Bot, bot) {
			out = append(out, g)
		}
	}
	return out, nil
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

// carriedCountNote reads how many of item the bot carries: the `ItemScan`
// inv, which is the core's GetItemCount over equipment, bags and the reagent
// bank (never the bank storage). The note names the failed read.
func (p *Provisioner) carriedCountNote(ctx context.Context, bot string, item uint32) (int, string, bool) {
	r, note, ok := p.scanItem(ctx, bot, item)
	if !ok {
		return 0, note, false
	}
	return r.Inv, "", true
}

// carriedCount is carriedCountNote without its note.
func (p *Provisioner) carriedCount(ctx context.Context, bot string, item uint32) (int, bool) {
	n, _, ok := p.carriedCountNote(ctx, bot, item)
	return n, ok
}

// giveItem sends one `.tb additem` and records it: the grant record with the
// baseline and the intent is on disk before the send, and a read-back that
// proves the count grew turns it into a confirmed grant. A give whose count
// did not grow retires its record, so a clear can never remove what it did not
// add. Without a readable baseline nothing is given and no record is written.
func (p *Provisioner) giveItem(ctx context.Context, bot string, item uint32, count uint32) ([]string, error) {
	if count == 0 {
		count = 1
	}
	base, known := p.carriedCount(ctx, bot, item)
	if !known {
		return nil, fmt.Errorf("nothing given: the carried count of item %d on %s is unreadable (no baseline for the recovery mark)", item, bot)
	}
	intent, merr := p.marks().begin(givenItem{Server: p.Server, Bot: bot, Item: item, Base: uint32(base), Intent: count})
	if merr != nil {
		return nil, fmt.Errorf("nothing given: the recovery mark of item %d on %s was not written before additem: %w", item, bot, merr)
	}
	tctx, cancel := p.ctxOf(ctx)
	lines, err := p.TB.FreeTextAddItem(tctx, bot, item, count)
	cancel()
	after, ok := p.carriedCount(ctx, bot, item)
	if ok && after > base {
		gained := uint32(after - base)
		if cerr := p.marks().confirm(intent.Grant, gained); cerr != nil {
			return lines, fmt.Errorf("recovery mark not confirmed for %d given item(s) %d: %w", gained, item, cerr)
		}
		if err != nil {
			return lines, err
		}
		if gained < count {
			return lines, fmt.Errorf("only %d of %d item %d reached the bags", gained, count, item)
		}
		return lines, nil
	}
	if ok {
		// The count did not grow above the baseline read before the give, so
		// this give added nothing: its record proves nothing and is retired.
		if rerr := p.marks().retireAll([]givenItem{intent}); rerr != nil {
			return lines, fmt.Errorf("the recovery mark of the give of item %d on %s was not retired: %w", item, bot, rerr)
		}
	}
	if err != nil {
		return lines, err
	}
	if !ok {
		return lines, fmt.Errorf("the give of item %d on %s is UNVERIFIED: the carried count is unreadable afterwards", item, bot)
	}
	return lines, fmt.Errorf("additem answered but the hire carries no more item %d on %s (%d before, %d after)", item, bot, base, after)
}

// ClearGiven takes back what earlier grants left on bot, and only that: each
// grant record is proved by an `ItemScan` read-back, capped at the copies its
// give proved and floored at the carried count read before that give. An
// unconfirmed grant's intent is never cleared (no copy is proved), and an
// unmarked id is never touched. A settled grant is retired; an unconfirmed
// grant whose count is back at its baseline is retired with it. The evidence
// is one line; ok is false when any grant could not be proved handled (its
// record stays for the next run).
func (p *Provisioner) ClearGiven(ctx context.Context, bot string) (string, bool) {
	marks, err := p.marks().forBotErr(p.Server, bot)
	if err != nil {
		return "recovery marks unreadable: " + err.Error(), false
	}
	if len(marks) == 0 {
		return "no items to clear", true
	}
	groups := grantGroups(marks)
	allOK := true
	var notes []string
	cleared := 0
	for i := range groups {
		g := &groups[i]
		if !p.prepareGrantGroup(ctx, bot, g) {
			allOK = false
			notes = append(notes, g.note)
		}
	}
	// Bag copies of every item go first: they free the slots an unequip
	// needs, and their count is the part of a grant that is in the bags.
	for i := range groups {
		g := &groups[i]
		if !g.ok {
			continue
		}
		if !p.clearGrantBags(ctx, bot, g) {
			allOK = false
			notes = append(notes, g.note)
		}
	}
	// A worn grant then takes its remaining copies off the equipment, one at
	// a time, each slot proved holding the item first.
	for i := range groups {
		g := &groups[i]
		if !g.ok || !g.worn {
			continue
		}
		if !p.clearGrantWorn(ctx, bot, g) {
			allOK = false
			notes = append(notes, g.note)
		}
	}
	var settled []givenItem
	for i := range groups {
		g := &groups[i]
		cleared += g.removed
		if !g.ok {
			continue
		}
		inv, note, ok := p.carriedCountNote(ctx, bot, g.item)
		if !ok {
			allOK = false
			notes = append(notes, note)
			continue
		}
		switch {
		case inv <= g.floor:
			// Back at the pre-grant count: every copy the grant added is gone.
			settled = append(settled, g.records...)
		case g.removed >= g.cap:
			// Every copy the grant proved it added is back; anything above the
			// floor left is not this grant's, so the record is settled and the
			// hire keeps those copies.
			settled = append(settled, g.records...)
		case !g.worn:
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (%d above the baseline not in the bags; a non-worn grant never touches equipment)", g.item, inv-g.floor))
		default:
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (%d above the baseline not proved removed)", g.item, inv-g.floor))
		}
	}
	if err := p.marks().retireAll(settled); err != nil {
		allOK = false
		notes = append(notes, "recovery marks not retired: "+err.Error())
	}
	return formatClearSummary(cleared, notes), allOK
}

// grantGroup is the cleanup view of one item id's grants.
type grantGroup struct {
	item    uint32
	floor   int         // the lowest baseline: the carried count before the first grant
	cap     int         // copies the confirmed grants proved they added
	removed int         // copies taken back so far
	worn    bool        // a grant was equipped
	ok      bool        // still clearable (a failed read leaves it false)
	note    string      // why it is not ok
	records []givenItem // the confirmed records to retire when settled
}

// grantGroups groups the marks of one bot by item, newest baseline first. A
// group holding an unconfirmed grant is prepared (and may be retired or
// failed) before its confirmed grants are cleared.
func grantGroups(marks []givenItem) []grantGroup {
	byItem := map[uint32]*grantGroup{}
	var ids []uint32
	for _, g := range marks {
		grp := byItem[g.Item]
		if grp == nil {
			grp = &grantGroup{item: g.Item, floor: int(g.Base), ok: true}
			byItem[g.Item] = grp
			ids = append(ids, g.Item)
		}
		if int(g.Base) < grp.floor {
			grp.floor = int(g.Base)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]grantGroup, 0, len(ids))
	for _, id := range ids {
		out = append(out, *byItem[id])
	}
	return out
}

// prepareGrantGroup settles the unconfirmed grants of one item: a grant whose
// read-back never proved a copy may have left copies, but nothing proves they
// are the trial's, so the group fails closed and nothing is cleared. A grant
// whose carried count is back at its own baseline added nothing that remains
// and is retired. The confirmed grants then form the group's budget.
func (p *Provisioner) prepareGrantGroup(ctx context.Context, bot string, g *grantGroup) bool {
	// The group's records are rebuilt from the marks of this item.
	all, err := p.marks().forBotErr(p.Server, bot)
	if err != nil {
		g.ok = false
		g.note = "recovery marks unreadable: " + err.Error()
		return false
	}
	var unconfirmed, confirmed []givenItem
	for _, r := range all {
		if r.Item != g.item {
			continue
		}
		if r.Grant != 0 && r.Count == 0 {
			unconfirmed = append(unconfirmed, r)
			continue
		}
		confirmed = append(confirmed, r)
	}
	if len(unconfirmed) > 0 {
		inv, note, ok := p.carriedCountNote(ctx, bot, g.item)
		if !ok {
			g.ok = false
			g.note = note
			return false
		}
		for _, r := range unconfirmed {
			if inv > int(r.Base) {
				g.ok = false
				g.note = fmt.Sprintf("item %d: UNVERIFIED (a give whose read-back never confirmed it left %d copy/copies above its recorded baseline; nothing is cleared)", g.item, inv-int(r.Base))
				return false
			}
		}
		if err := p.marks().retireAll(unconfirmed); err != nil {
			g.ok = false
			g.note = "recovery marks not retired: " + err.Error()
			return false
		}
	}
	g.records = confirmed
	g.cap = 0
	g.worn = false
	g.floor = 0
	first := true
	for _, r := range confirmed {
		g.cap += int(r.Count)
		g.worn = g.worn || r.Worn
		if first || int(r.Base) < g.floor {
			g.floor = int(r.Base)
			first = false
		}
	}
	if g.cap == 0 {
		g.records = nil
	}
	return true
}

// clearGrantBags removes one grant group's bag copies, up to its proved cap
// and never below its floor. Each removal re-reads the count first. A module
// removal of zero means the bags hold no copy (the rest of the grant is worn)
// and is not an error.
func (p *Provisioner) clearGrantBags(ctx context.Context, bot string, g *grantGroup) bool {
	for g.removed < g.cap {
		inv, note, ok := p.carriedCountNote(ctx, bot, g.item)
		if !ok {
			g.ok = false
			g.note = note
			return false
		}
		if inv <= g.floor {
			return true
		}
		n, note, ok := p.removeOneAbove(ctx, bot, g.item, g.floor)
		if !ok {
			g.ok = false
			g.note = note
			return false
		}
		if n == 0 {
			return true
		}
		g.removed += n
	}
	return true
}

// clearGrantWorn unequips and destroys a worn grant's remaining copies, one
// at a time. The slot is read from a complete `.bot inv` reply and must hold
// the item before the unequip is sent; a slot that cannot be proved is never
// touched.
func (p *Provisioner) clearGrantWorn(ctx context.Context, bot string, g *grantGroup) bool {
	for g.removed < g.cap {
		inv, note, ok := p.carriedCountNote(ctx, bot, g.item)
		if !ok {
			g.ok = false
			g.note = note
			return false
		}
		if inv <= g.floor {
			return true
		}
		scan, note, ok := p.scanItem(ctx, bot, g.item)
		if !ok {
			g.ok = false
			g.note = note
			return false
		}
		if !scan.CanEquip || !scan.HasSlot {
			why := scan.Why
			if why == "" {
				why = "the item is not equippable"
			}
			g.ok = false
			g.note = fmt.Sprintf("item %d: UNVERIFIED (%d above the baseline not in the bags: %s)", g.item, inv-g.floor, why)
			return false
		}
		slot, note, ok := p.provenEquipSlot(ctx, bot, g.item, scan.Slot)
		if !ok {
			g.ok = false
			g.note = note
			return false
		}
		if err := p.unequipBotSlot(ctx, bot, slot, g.item); err != nil {
			g.ok = false
			g.note = fmt.Sprintf("item %d: unequip slot %d failed: %v", g.item, slot, err)
			return false
		}
		n, note, ok := p.removeOneAbove(ctx, bot, g.item, g.floor)
		if !ok {
			g.ok = false
			g.note = note
			return false
		}
		if n != 1 {
			g.ok = false
			g.note = fmt.Sprintf("item %d: unequip slot %d did not put a copy into the bags", g.item, slot)
			return false
		}
		g.removed += n
	}
	return true
}

// removeOneAbove takes at most one copy of item out of the bags. The carried
// count is re-read here, in the same step that removes: when it is already at
// or below floor nothing may be removed, so a budget measured against an
// earlier read can never cross the baseline. The module is asked for exactly
// one copy and the read-back must show exactly that, never a count below the
// floor.
func (p *Provisioner) removeOneAbove(ctx context.Context, bot string, item uint32, floor int) (int, string, bool) {
	before, known := p.carriedCount(ctx, bot, item)
	if !known {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (carried count unreadable before removing)", item), false
	}
	if before <= floor {
		return 0, "", true
	}
	tctx, cancel := p.ctxOf(ctx)
	res, lines, rerr := p.TB.FreeTextRemoveItem(tctx, bot, item, 1)
	cancel()
	p.Ledger.Add(map[string]any{"kind": "removeitem", "bot": bot, "item_id": item, "count": 1,
		"removed": res.Removed, "left": res.Left, "reply": firstLines(lines)})
	if rerr != nil {
		return 0, fmt.Sprintf("item %d: remove refused: %s", item, tbErrText(rerr, lines)), false
	}
	after, ok := p.carriedCount(ctx, bot, item)
	if !ok {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (carried count unreadable after removing)", item), false
	}
	if res.Removed == 0 {
		if after != before {
			return 0, fmt.Sprintf("item %d: UNVERIFIED (the module removed none but the carried count moved %d -> %d)", item, before, after), false
		}
		return 0, "", true
	}
	if res.Removed != 1 || after != before-1 {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (carried %d before, %d after the module removed %d of 1)", item, before, after, res.Removed), false
	}
	if after < floor {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (the carried count %d fell below the baseline %d)", item, after, floor), false
	}
	return 1, "", true
}

// provenEquipSlot reads the hire's inventory and returns an equipment slot
// that holds item. preferred is the itemscan's candidate, used when it holds
// the item; otherwise the first occupied slot that does. Only a complete
// `.bot inv` reply proves an occupant, so a truncated reply proves nothing and
// the unequip is refused.
func (p *Provisioner) provenEquipSlot(ctx context.Context, bot string, item uint32, preferred int) (int, string, bool) {
	if p.Client == nil {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (no client for .bot inv)", item), false
	}
	tctx, cancel := p.ctxOf(ctx)
	res, err := p.Client.botLine(tctx, ".bot inv "+bot, 5000)
	cancel()
	p.Ledger.Add(map[string]any{"kind": "bot_inv", "bot": bot, "complete": res.Complete, "reply": firstLines(res.Lines)})
	if err != nil {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (.bot inv %s: %v)", item, bot, err), false
	}
	inv := tbm.ParseInventory(res.Lines)
	if inv.Err != nil {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (.bot inv %s: %s: %s)", item, bot, inv.Err.Code, inv.Err.Message), false
	}
	if !inv.Complete {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (.bot inv %s: the reply is truncated, no INV_END)", item, bot), false
	}
	if inv.Bot != "" && !strings.EqualFold(inv.Bot, bot) {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (.bot inv answered for %q, asked for %q)", item, inv.Bot, bot), false
	}
	if inv.Equipped[preferred] == item {
		return preferred, "", true
	}
	for slot := range 19 {
		if inv.Equipped[slot] == item {
			return slot, "", true
		}
	}
	return 0, fmt.Sprintf("item %d: UNVERIFIED (no equipment slot is proved to hold it)", item), false
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
