package live

// items.go: the items a trial gives a hire are taken back again.
//
// One rule: a copy is deleted only when the server's own answer names it, as
// the item instance it created, and only on the character it went to. The
// proof is a grant record, a file under the state dir so it survives a killed
// run: one line per copy the server named, holding the item's instance GUID,
// its id, and the character. Nothing else is ever deleted: not a copy the
// hire carried before the trial, not one that merged into the hire's own
// stack, not one the server did not name. An id with no grant is the hire's.
//
// A give is recorded at grant time: setup reads the item instances the hire
// holds (`ItemScan`'s instances= list), sends `.tb additem`, reads the list
// again, and records every GUID the give added. A give whose read-back is
// missing, or added no instance (a merged stack), is reported and records
// nothing: those copies are never the trial's to delete. The list is the
// server's answer and the only evidence used; counts are never taken as
// provenance, because a stack's count moves under the trial's feet (the live
// bench ate a Sweet Nectar between the read and the removal, 2026-11-27).
//
// The clear deletes each recorded GUID with `.tb removeitem <character> guid
// <guid>`, one copy at a time, only on the character the record names. A worn
// copy is unequipped first, through the equipment slot the instance list
// named, and the slot is read back before the unequip. A deletion that the
// module refuses, or whose read-back cannot prove the GUID gone, is reported
// and left in place: the clear never retries it, never deletes another copy
// in its place, and never restores a budget. A recorded GUID the hire no
// longer holds is retired: the copy is gone.

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

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/tbm"
)

// invReadWaitMS is the `.bot inv` collection window. The dump is one chat
// line per worn slot and per bag slot (up to ~100), and the collector ends a
// tokenless reply after a quiet window; five seconds cut a full bag's dump
// mid-reply on a loaded world, so the read is given the module's full dump
// time instead.
const invReadWaitMS = 15000

// ItemGrant is one item instance a trial gave a character: the GUID the
// server named for it, the item it is, and who received it. There is no
// count: the instance is the copy (or the stack) the give created, and the
// clear deletes exactly that instance.
type ItemGrant struct {
	Server    string `json:"server"`
	Character string `json:"character"`
	CharGUID  string `json:"char_guid,omitempty"`
	Item      uint32 `json:"item"`
	GUID      string `json:"guid"`
	Grant     uint64 `json:"grant,omitempty"`
}

// sameGrant reports whether two records are the same stored grant.
func sameGrant(a, b ItemGrant) bool {
	return strings.EqualFold(a.Server, b.Server) && strings.EqualFold(a.Character, b.Character) &&
		a.Item == b.Item && strings.EqualFold(a.GUID, b.GUID)
}

// grantFile is the item grants of one player's trials on one server.
type grantFile struct {
	path string
	mu   sync.Mutex
	mem  []ItemGrant // used when path is empty
}

var grantMu sync.Mutex // one process, one file: serialise read-modify-write

var grantSeq atomic.Uint64

// nextGrant is a serial unique in the process: two grants of the same item
// never share an identity beyond their own GUIDs.
func nextGrant() uint64 {
	return uint64(time.Now().UnixNano())<<8 | (grantSeq.Add(1) & 0xff)
}

// GrantPathFor is the item grant file of a player's trials on one server:
// <state dir>/items-grants-<server>-<player>.json, beside the arena locks.
// The server is in the name: the same character name on two servers must not
// share grants.
func GrantPathFor(server, player string) string {
	safe := func(in string) string {
		return strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == 0 || r == ':' {
				return '_'
			}
			return r
		}, in)
	}
	return filepath.Join(ArenaStateDir(), "items-grants-"+safe(server)+"-"+safe(player)+".json")
}

// load reads the grant file. A missing file is no grants; an unreadable file,
// or one that is not a valid list of grants, is an error, never an empty
// list: an empty list would read as "nothing was ever given" and a clear
// could not report the copies it leaves behind.
func (g *grantFile) load() ([]ItemGrant, error) {
	if g.path == "" {
		return append([]ItemGrant(nil), g.mem...), nil
	}
	raw, err := os.ReadFile(g.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", g.path, err)
	}
	var out []ItemGrant
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", g.path, err)
	}
	for i, rec := range out {
		if rec.Item == 0 || strings.TrimSpace(rec.Server) == "" || strings.TrimSpace(rec.Character) == "" {
			return nil, fmt.Errorf("%s: record %d names no server, character or item", g.path, i+1)
		}
		if guid, err := tbcmd.ItemGUID(rec.GUID); err != nil {
			return nil, fmt.Errorf("%s: record %d: %w", g.path, i+1, err)
		} else if guid != rec.GUID {
			out[i].GUID = guid
		}
	}
	return out, nil
}

func (g *grantFile) store(list []ItemGrant) error {
	if g.path == "" {
		g.mem = append([]ItemGrant(nil), list...)
		return nil
	}
	if len(list) == 0 {
		if err := os.Remove(g.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0o755); err != nil {
		return err
	}
	raw, _ := json.Marshal(list)
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.path)
}

// add records grants in one write; a record already stored (same server,
// character, item and GUID) is not written twice. A grant that does not name
// its server, character, item and 32-hex GUID is refused here: the file must
// never hold a record its own loader rejects.
func (g *grantFile) add(grants []ItemGrant) error {
	if len(grants) == 0 {
		return nil
	}
	for i, n := range grants {
		if strings.TrimSpace(n.Server) == "" || strings.TrimSpace(n.Character) == "" || n.Item == 0 {
			return fmt.Errorf("grant %d names no server, character or item", i+1)
		}
		if guid, err := tbcmd.ItemGUID(n.GUID); err != nil {
			return fmt.Errorf("grant %d: %w", i+1, err)
		} else {
			grants[i].GUID = guid
		}
	}
	grantMu.Lock()
	defer grantMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	list, err := g.load()
	if err != nil {
		return err
	}
	for _, n := range grants {
		known := false
		for _, have := range list {
			if sameGrant(have, n) {
				known = true
				break
			}
		}
		if !known {
			list = append(list, n)
		}
	}
	return g.store(list)
}

// retire removes the settled grants in one write. A record that changed since
// it was read is left in place.
func (g *grantFile) retire(settled []ItemGrant) error {
	if len(settled) == 0 {
		return nil
	}
	grantMu.Lock()
	defer grantMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	list, err := g.load()
	if err != nil {
		return err
	}
	keep := list[:0]
	for _, have := range list {
		drop := false
		for _, s := range settled {
			if sameGrant(have, s) {
				drop = true
				break
			}
		}
		if !drop {
			keep = append(keep, have)
		}
	}
	return g.store(keep)
}

// forCharacter lists one character's grants on one server. The clear must
// never read an unreadable file as "nothing to clear", so the error is
// returned, not swallowed.
func (g *grantFile) forCharacter(server, character string) ([]ItemGrant, error) {
	grantMu.Lock()
	defer grantMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	list, err := g.load()
	if err != nil {
		return nil, err
	}
	var out []ItemGrant
	for _, rec := range list {
		if strings.EqualFold(rec.Server, server) && strings.EqualFold(rec.Character, character) {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (p *Provisioner) grants() *grantFile {
	p.markOnce.Do(func() { p.grantLog = &grantFile{path: p.GrantPath} })
	return p.grantLog
}

// setCharGUID remembers a hire's character GUID once the setup read it, so
// every grant of that character records the character beyond its name.
func (p *Provisioner) setCharGUID(name, guid string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	if p.CharGUIDs == nil {
		p.CharGUIDs = map[string]string{}
	}
	g := normGUIDOf(guid)
	p.CharGUIDs[strings.ToLower(name)] = g
	if g != "" && p.OnHireGUID != nil {
		p.OnHireGUID(name, g)
	}
}

func (p *Provisioner) charGUID(name string) string {
	return p.CharGUIDs[strings.ToLower(name)]
}

// itemInstances reads the item instances the server says the character holds
// (the `ItemScan` record's instances= list): each copy's own GUID, its place
// and its count. ok is false when the answer cannot be read or names no
// instance list: the module cannot say which copies are there, so nothing may
// be recorded or deleted on that answer.
func (p *Provisioner) itemInstances(ctx context.Context, character string, item uint32) ([]tbcmd.ItemInstance, string, bool) {
	tctx, cancel := p.ctxOf(ctx)
	rep, err := p.TB.ItemScan(tctx, character, item)
	cancel()
	if err != nil {
		return nil, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s: %v)", item, character, err), false
	}
	for _, r := range rep.Results {
		if r.Item != item {
			continue
		}
		if !r.Exists {
			return nil, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s: the server holds no such item)", item, character), false
		}
		if !r.HasInstances {
			return nil, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s named no item instance list, so no copy's GUID is known)", item, character), false
		}
		return r.Instances, "", true
	}
	return nil, fmt.Sprintf("item %d: UNVERIFIED (itemscan %s held no record for it)", item, character), false
}

// giveItem sends one `.tb additem` and records every item instance the give
// created: the instances read after the give that were not there before. The
// grant is written before the next server call. Without a readable instance
// list nothing is given; a give whose read-back names no new instance (a
// merged stack, an unreadable answer) is reported and records nothing, so a
// clear can never delete a copy the server did not name.
func (p *Provisioner) giveItem(ctx context.Context, bot string, item uint32, count uint32) ([]string, error) {
	if count == 0 {
		count = 1
	}
	before, note, ok := p.itemInstances(ctx, bot, item)
	if !ok {
		return nil, fmt.Errorf("nothing given: %s", note)
	}
	have := map[string]bool{}
	for _, inst := range before {
		have[inst.GUID] = true
	}
	tctx, cancel := p.ctxOf(ctx)
	lines, err := p.TB.FreeTextAddItem(tctx, bot, item, count)
	cancel()
	after, note, ok := p.itemInstances(ctx, bot, item)
	if !ok {
		if err != nil {
			return lines, err
		}
		return lines, fmt.Errorf("the give of item %d on %s is UNVERIFIED: %s; its copies are not recorded and will not be deleted", item, bot, note)
	}
	var fresh []tbcmd.ItemInstance
	for _, inst := range after {
		if !have[inst.GUID] {
			fresh = append(fresh, inst)
		}
	}
	if len(fresh) == 0 {
		if err != nil {
			return lines, err
		}
		return lines, fmt.Errorf("the give of item %d on %s created no item instance of its own (merged into a copy the hire carried, or the server named none): nothing recorded, nothing will be deleted", item, bot)
	}
	grants := make([]ItemGrant, 0, len(fresh))
	for _, inst := range fresh {
		grants = append(grants, ItemGrant{Server: p.Server, Character: bot, CharGUID: p.charGUID(bot),
			Item: item, GUID: inst.GUID, Grant: nextGrant()})
	}
	if aerr := p.grants().add(grants); aerr != nil {
		return lines, fmt.Errorf("the give of %d item instance(s) %d on %s was not recorded: %w", len(grants), item, bot, aerr)
	}
	if err != nil {
		return lines, err
	}
	// A stack give names one instance; a give asking for several copies that
	// landed in several stacks names several. Fewer instances than the
	// copies asked for is fine when they hold every copy the read-back shows
	// (merges are not evidence either way); the give itself already failed
	// above when it created nothing.
	return lines, nil
}

// ClearGiven deletes every item instance a trial recorded for character, and
// only those: each grant is one GUID the server named at grant time, deleted
// on that exact character. A worn copy is unequipped first, in the slot the
// instance list proved. A recorded copy the character no longer holds is
// retired (it is gone). A deletion the module refuses, or one whose read-back
// cannot prove the GUID gone, is reported and left in place: it is not
// retried, no other copy is deleted in its place, and the grant stays for the
// next run. The evidence is one line; ok is false when any recorded copy
// could not be proved handled.
func (p *Provisioner) ClearGiven(ctx context.Context, character string) (string, bool) {
	grants, err := p.grants().forCharacter(p.Server, character)
	if err != nil {
		return "item grants unreadable: " + err.Error(), false
	}
	if len(grants) == 0 {
		return "no items to clear", true
	}
	// One pass per item: the server's instance list is read once for the ids
	// that batch, and every recorded GUID is deleted on its own.
	byItem := map[uint32][]ItemGrant{}
	var ids []uint32
	for _, g := range grants {
		if _, ok := byItem[g.Item]; !ok {
			ids = append(ids, g.Item)
		}
		byItem[g.Item] = append(byItem[g.Item], g)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	allOK := true
	var notes []string
	cleared := 0
	var settled []ItemGrant
	for _, item := range ids {
		itemGrants := byItem[item]
		instances, note, ok := p.itemInstances(ctx, character, item)
		if !ok {
			allOK = false
			notes = append(notes, note)
			continue
		}
		held := map[string]tbcmd.ItemInstance{}
		for _, inst := range instances {
			held[inst.GUID] = inst
		}
		for _, grant := range itemGrants {
			inst, present := held[grant.GUID]
			if !present {
				// The copy is gone: nothing to delete, the grant is settled.
				settled = append(settled, grant)
				continue
			}
			if inst.Worn {
				// A worn copy goes to the bags first, in the slot the server
				// proved it occupies; a slot that is refused is reported and
				// the copy stays worn.
				if err := p.unequipBotSlot(ctx, character, inst.Slot, item); err != nil {
					allOK = false
					notes = append(notes, fmt.Sprintf("item %d (item guid %s): unequip slot %d failed: %v; left worn",
						item, grant.GUID, inst.Slot, err))
					continue
				}
				now, note, ok := p.itemInstances(ctx, character, item)
				if !ok {
					allOK = false
					notes = append(notes, note+"; the unequipped copy is left in place")
					continue
				}
				moved := tbcmd.ItemInstance{}
				for _, cur := range now {
					if cur.GUID == grant.GUID {
						moved = cur
						break
					}
				}
				if moved.GUID == "" || moved.Worn {
					allOK = false
					notes = append(notes, fmt.Sprintf("item %d (item guid %s): unequip slot %d did not put the copy into the bags; left in place",
						item, grant.GUID, inst.Slot))
					continue
				}
			}
			res, _, rerr := p.TB.RemoveItemInstance(ctx, character, grant.GUID)
			if rerr != nil {
				allOK = false
				notes = append(notes, fmt.Sprintf("item %d (item guid %s): remove refused: %s; left in place", item, grant.GUID, tbErrText(rerr, nil)))
				continue
			}
			if res.Removed != 1 {
				allOK = false
				notes = append(notes, fmt.Sprintf("item %d (item guid %s): the module removed no copy; left in place", item, grant.GUID))
				continue
			}
			// The answer is not the proof: the instance list must no longer
			// hold the GUID. An unreadable read-back leaves the copy in place.
			now, note, ok := p.itemInstances(ctx, character, item)
			if !ok {
				allOK = false
				notes = append(notes, note+fmt.Sprintf("; item guid %s: the removal is UNVERIFIED and the copy is left in place", grant.GUID))
				continue
			}
			still := false
			for _, cur := range now {
				if cur.GUID == grant.GUID {
					still = true
					break
				}
			}
			if still {
				allOK = false
				notes = append(notes, fmt.Sprintf("item %d (item guid %s): UNVERIFIED (the module answered removed=1 but the copy is still held)", item, grant.GUID))
				continue
			}
			settled = append(settled, grant)
			cleared++
		}
	}
	if err := p.grants().retire(settled); err != nil {
		allOK = false
		notes = append(notes, "item grants not retired: "+err.Error())
	}
	return formatClearSummary(cleared, notes), allOK
}

// provenEquipSlot reads the hire's inventory and returns the equipment slot
// the instance list named when a complete `.bot inv` reply proves it holds
// item. Only a complete reply proves an occupant, so a truncated reply proves
// nothing and the unequip is refused.
func (p *Provisioner) provenEquipSlot(ctx context.Context, bot string, item uint32, slot int) (int, string, bool) {
	if p.Client == nil {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (no client for .bot inv)", item), false
	}
	var inv tbm.Inventory
	var note string
	for attempt := 1; attempt <= 2; attempt++ {
		tctx, cancel := p.ctxOf(ctx)
		// The inventory dump is many lines (every worn slot and every bag
		// slot): ask for the collector's full bound, or a full bag cuts the
		// reply before INV_END and no slot is ever proved. A read that ends
		// without its end marker is retried once (the module's dump can
		// outlast the quiet window on a loaded world), still fail-closed.
		res, err := p.Client.botLine(tctx, ".bot inv "+bot, invReadWaitMS, command.MaxCollectedLines)
		cancel()
		p.Ledger.Add(map[string]any{"kind": "bot_inv", "bot": bot, "complete": res.Complete, "reply": firstLines(res.Lines)})
		if err != nil {
			note = fmt.Sprintf("item %d: UNVERIFIED (.bot inv %s: %v)", item, bot, err)
			if attempt < 2 {
				continue
			}
			return 0, note, false
		}
		inv = tbm.ParseInventory(res.Lines)
		if inv.Err != nil {
			return 0, fmt.Sprintf("item %d: UNVERIFIED (.bot inv %s: %s: %s)", item, bot, inv.Err.Code, inv.Err.Message), false
		}
		if inv.Complete {
			note = ""
			break
		}
		note = fmt.Sprintf("item %d: UNVERIFIED (.bot inv %s: the reply is truncated, no INV_END)", item, bot)
	}
	if !inv.Complete {
		return 0, note, false
	}
	if inv.Bot != "" && !strings.EqualFold(inv.Bot, bot) {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (.bot inv answered for %q, asked for %q)", item, bot, inv.Bot), false
	}
	if slot < 0 || inv.Equipped[slot] != item {
		return 0, fmt.Sprintf("item %d: UNVERIFIED (the slot %d the instance list named is not proved to hold it)", item, slot), false
	}
	return slot, "", true
}

func (p *Provisioner) unequipBotSlot(ctx context.Context, bot string, slot int, item uint32) error {
	proved, note, ok := p.provenEquipSlot(ctx, bot, item, slot)
	if !ok {
		return fmt.Errorf("%s", note)
	}
	if p.Client == nil {
		return fmt.Errorf("no client for .bot item unequip")
	}
	tctx, cancel := p.ctxOf(ctx)
	res, err := p.Client.botLine(tctx, fmt.Sprintf(".bot item %s unequip 255 %d", bot, proved), 5000, 0)
	cancel()
	p.Ledger.Add(map[string]any{"kind": "unequip", "bot": bot, "bag": 255, "slot": proved, "item_id": item, "reply": firstLines(res.Lines)})
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
	return fmt.Errorf("no ACTION_ACK for .bot item %s unequip 255 %d", bot, proved)
}

// isBagsFull reports the module's "bags full" refusal.
func isBagsFull(err error, lines []string) bool {
	text := strings.ToLower(strings.Join(lines, "\n"))
	if err != nil {
		text += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(text, "bags full") || strings.Contains(text, "bags are full")
}

// noInstanceList reports the module's missing capability: `.tb itemscan`
// answered no instances= list, so no copy's GUID can be known. A server gap
// (needs=bot_items), never a defect of the trial.
func noInstanceList(evidence string) bool {
	return strings.Contains(evidence, "named no item instance list")
}

// itemsGiveRow is the bot_items row for a refused additem: a full bag that
// clearing did not free is SKIPPED with the reason, any other refusal is a
// plain failed row. A module that cannot name item instances is a server gap
// (SKIPPED needs=bot_items), never a trial failure.
func itemsGiveRow(bot, item string, ids any, err error, lines []string, clearEvidence string, clearOK bool) map[string]any {
	if isBagsFull(err, lines) {
		return rowOf("bot_items", false, ids, fmt.Sprintf(
			"SKIPPED needs=bot_bag_space: .tb additem %s %s: bags full (clearing recorded items: %s)", bot, item, clearEvidence))
	}
	text := tbErrText(err, lines)
	if noInstanceList(text) {
		return rowOf("bot_items", false, ids, "SKIPPED needs=bot_items: module/core gap — "+text)
	}
	return rowOf("bot_items", false, ids, ".tb additem "+bot+" "+item+": "+text)
}

func formatClearSummary(cleared int, notes []string) string {
	var parts []string
	if cleared > 0 {
		parts = append(parts, fmt.Sprintf("cleared %d recorded item instance(s)", cleared))
	}
	parts = append(parts, notes...)
	if len(parts) == 0 {
		parts = append(parts, "no items to clear")
	}
	return strings.Join(parts, "; ")
}
