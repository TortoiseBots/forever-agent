package live

// items.go: the items a trial gives a hire are taken back again.
//
// `.tb additem` fills the hire's bags; a killed run used to leave them full,
// and the next run's additem then failed with "bags full". A recovery mark (a
// file under the state dir, so it survives a killed run) is written only
// AFTER a give is confirmed by an `ItemScan` read-back, and it names the
// server, the bot, the exact item id, the count the bags really gained and
// the bag count the hire had before (the baseline). The teardown and the
// next setup take back at most that count, through the module's own
// `.tb removeitem`, and never more than the bags hold above the baseline, so
// an item the hire carried before the trial is never removed. A clear is
// proved by an `ItemScan` read-back (the item gone from the bags); an item
// that cannot be proved gone stays in the mark and the caller says so. A kit
// piece that was equipped is not in the bags and the module has no unequip
// command: its mark is kept as worn and reported separately, never as
// cleared. A full bag that stays full is the setup row's SKIPPED
// needs=bot_bag_space, never a silent failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// givenItem is one confirmed give: Count is the bag increase the read-back
// proved, Base the bag count before the first give. Worn is set once the
// piece was equipped (it then leaves the bags and cannot be taken back).
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

// bagCount reads how many of item the bot carries in its bags (not
// equipped): the `ItemScan` inv. ok is false when it cannot be read.
func (p *Provisioner) bagCount(ctx context.Context, bot string, item uint32) (int, bool) {
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
	base, known := p.bagCount(ctx, bot, item)
	if !known {
		return nil, fmt.Errorf("nothing given: the bag count of item %d on %s is unreadable (UNVERIFIED baseline for the recovery mark)", item, bot)
	}
	tctx, cancel := p.ctxOf(ctx)
	lines, err := p.TB.FreeTextAddItem(tctx, bot, item, count)
	cancel()
	after, ok := p.bagCount(ctx, bot, item)
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

// ClearGiven takes back what earlier gives left in bot's bags: for each
// marked item the bags are read, at most the marked count (and never more
// than the bags hold above the baseline) is removed with `.tb removeitem`,
// and an `ItemScan` read-back must show the item gone from the bags. It
// returns a one-line evidence text and whether every marked item is proved
// handled. An item that cannot be read or removed stays marked; an equipped
// kit piece stays marked as worn and is named in the evidence, never counted
// as cleared.
func (p *Provisioner) ClearGiven(ctx context.Context, bot string) (string, bool) {
	gives := p.marks().forBot(p.Server, bot)
	if len(gives) == 0 {
		return "no items to clear", true
	}
	allOK := true
	cleared := 0
	var notes, wornIDs []string
	for _, g := range gives {
		inv, known := p.bagCount(ctx, bot, g.Item)
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
			if g.Worn {
				wornIDs = append(wornIDs, itoa(int(g.Item)))
				continue
			}
			p.marks().drop(g) // not in the bags any more (used up)
			cleared++
			continue
		}
		tctx, cancel := p.ctxOf(ctx)
		res, lines, rerr := p.TB.FreeTextRemoveItem(tctx, bot, g.Item, uint32(extra))
		cancel()
		p.Ledger.Add(map[string]any{"kind": "removeitem", "bot": bot, "item_id": g.Item, "count": extra,
			"removed": res.Removed, "left": res.Left, "reply": firstLines(lines)})
		if rerr != nil {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: remove refused: %s", g.Item, tbErrText(rerr, lines)))
			continue
		}
		after, ok := p.bagCount(ctx, bot, g.Item)
		if !ok || after > inv-extra {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (carried %d before, %d after removing %d)", g.Item, inv, after, extra))
			continue
		}
		if g.Worn {
			wornIDs = append(wornIDs, itoa(int(g.Item)))
			continue
		}
		p.marks().drop(g)
		cleared++
	}
	var parts []string
	if cleared > 0 {
		parts = append(parts, fmt.Sprintf("cleared %d marked item(s) from the bags", cleared))
	}
	if len(wornIDs) > 0 {
		parts = append(parts, "still worn by the hire, not removable (no unequip in the module): item "+strings.Join(wornIDs, ", "))
	}
	parts = append(parts, notes...)
	if len(parts) == 0 {
		parts = append(parts, "no items to clear")
	}
	return strings.Join(parts, "; "), allOK
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
