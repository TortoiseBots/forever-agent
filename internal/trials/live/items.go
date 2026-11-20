package live

// items.go: the items a trial gives a hire are taken back again.
//
// `.tb additem` fills the hire's bags; a killed run used to leave them full,
// and the next run's additem then failed with "bags full". Every give is
// written to a recovery mark BEFORE it is sent (a file under the state dir,
// so it survives a killed run), the teardown clears what the trial gave, and
// the next setup clears any mark an earlier run left before it gives new
// items. A clear is proved by an `ItemScan` read-back; an item that cannot be
// proved gone stays in the mark and the caller says so. A full bag that stays
// full is the setup row's SKIPPED needs=bot_bag_space, never a silent failure.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// givenItem is one item a trial gave a bot (count as sent).
type givenItem struct {
	Bot   string `json:"bot"`
	Item  uint32 `json:"item"`
	Count uint32 `json:"count"`
}

// recoveryMarks is the recovery mark file of one player's trials.
type recoveryMarks struct {
	path string
	mu   sync.Mutex
	mem  []givenItem // used when path is empty
}

var recoveryMu sync.Mutex // one process, one file: serialise read-modify-write

// RecoveryPathFor is the recovery mark file of a player's trials:
// <state dir>/items-recovery-<player>.json, beside the arena locks.
func RecoveryPathFor(player string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, player)
	return filepath.Join(ArenaStateDir(), "items-recovery-"+safe+".json")
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

// add records one give before it is sent.
func (m *recoveryMarks) add(g givenItem) error {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.load()
	for i := range list {
		if list[i].Bot == g.Bot && list[i].Item == g.Item {
			list[i].Count += g.Count
			return m.store(list)
		}
	}
	return m.store(append(list, g))
}

// forBot lists the marked gives of one bot.
func (m *recoveryMarks) forBot(bot string) []givenItem {
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []givenItem
	for _, g := range m.load() {
		if strings.EqualFold(g.Bot, bot) {
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
		if !(strings.EqualFold(e.Bot, g.Bot) && e.Item == g.Item) {
			keep = append(keep, e)
		}
	}
	_ = m.store(keep)
}

func (p *Provisioner) marks() *recoveryMarks {
	p.markOnce.Do(func() { p.recovery = &recoveryMarks{path: p.RecoveryPath} })
	return p.recovery
}

// giveItem marks, then sends one `.tb additem`.
func (p *Provisioner) giveItem(ctx context.Context, bot string, item uint32, count uint32) ([]string, error) {
	if count == 0 {
		count = 1
	}
	if err := p.marks().add(givenItem{Bot: bot, Item: item, Count: count}); err != nil {
		return nil, fmt.Errorf("recovery mark not written, nothing given: %w", err)
	}
	tctx, cancel := p.ctxOf(ctx)
	defer cancel()
	return p.TB.FreeTextAddItem(tctx, bot, item, count)
}

// ClearGiven takes back what earlier gives left in bot's bags: for each
// marked item the carried count is read, that many (at most the count given)
// are removed, and an `ItemScan` read-back must show the count down. It
// returns a one-line evidence text and whether every marked item is proved
// cleared. An item that cannot be read or removed stays marked.
func (p *Provisioner) ClearGiven(ctx context.Context, bot string) (string, bool) {
	gives := p.marks().forBot(bot)
	if len(gives) == 0 {
		return "no items to clear", true
	}
	allOK := true
	var notes []string
	for _, g := range gives {
		tctx, cancel := p.ctxOf(ctx)
		rep, err := p.TB.ItemScan(tctx, bot, g.Item)
		cancel()
		inv, known := -1, false
		if err == nil {
			for _, r := range rep.Results {
				if r.Item == g.Item && r.HasInv {
					inv, known = r.Inv, true
				}
			}
		}
		if !known {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (carried count unreadable)", g.Item))
			continue
		}
		if inv == 0 {
			p.marks().drop(g)
			continue
		}
		n := g.Count
		if uint32(inv) < n {
			n = uint32(inv)
		}
		tctx, cancel = p.ctxOf(ctx)
		lines, rerr := p.TB.FreeTextRemoveItem(tctx, bot, g.Item, n)
		cancel()
		p.Ledger.Add(map[string]any{"kind": "delitem", "bot": bot, "item_id": g.Item, "count": n, "reply": firstLines(lines)})
		if rerr != nil {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: remove refused: %s", g.Item, tbErrText(rerr, lines)))
			continue
		}
		tctx, cancel = p.ctxOf(ctx)
		rep, err = p.TB.ItemScan(tctx, bot, g.Item)
		cancel()
		after, ok := -1, false
		if err == nil {
			for _, r := range rep.Results {
				if r.Item == g.Item && r.HasInv {
					after, ok = r.Inv, true
				}
			}
		}
		if !ok || after > inv-int(n) {
			allOK = false
			notes = append(notes, fmt.Sprintf("item %d: UNVERIFIED (carried %d before, %d after removing %d)", g.Item, inv, after, n))
			continue
		}
		p.marks().drop(g)
	}
	if allOK {
		return fmt.Sprintf("cleared %d marked item(s)", len(gives)), true
	}
	return strings.Join(notes, "; "), false
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
