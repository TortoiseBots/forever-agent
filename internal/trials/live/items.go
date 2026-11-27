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
// A give is recorded at grant time, from the give's own answer: setup sends
// `.tb additem` and the module answers `additem bot= item= created=<guid;...>`,
// naming every item instance that very give created on that character. Those
// GUIDs, and only those, are recorded. Inventory differences are never
// evidence: a copy that appears between two scans may have been looted,
// mailed or traded in, and a scan cannot say what created it. A give whose
// answer names no created instance (a module without the record, a merged
// stack, a refusal, a transport failure) records nothing and is reported:
// those copies are never the trial's to delete. Counts, entries and slots are
// never taken as provenance either.
//
// The clear deletes each recorded GUID with `.tb removeitem <character> guid
// <guid>`, one copy at a time, only on the character the record names. A worn
// copy is first moved to the bags with `.tb unequip <character> guid <guid>`,
// which the module checks against the instance in the slot at the moment of
// the move; nothing is ever unequipped by slot, because the slot can hold a
// replacement copy of the same item by then. A module that cannot unequip by
// instance leaves the worn copy worn, the grant pending, and the clear
// reports it. A deletion that the module refuses, or whose read-back cannot
// prove the GUID gone, is reported and left in place: the clear never
// retries it, never deletes another copy in its place, and never restores a
// budget. A recorded GUID the hire no longer holds is retired: the copy is
// gone.

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
)

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

// giveItem sends one `.tb additem` and records the item instances the
// module's answer says that give created (created=), on the character the
// answer names, before the next server call. The instance list is read first
// only as a capability check: a module that cannot list its copies by GUID
// can never prove a later deletion, so nothing is given. A give whose answer
// names no created instance records nothing and is reported; no scan, count
// or inventory difference ever stands in for that answer.
func (p *Provisioner) giveItem(ctx context.Context, bot string, item uint32, count uint32) ([]string, error) {
	if count == 0 {
		count = 1
	}
	if _, note, ok := p.itemInstances(ctx, bot, item); !ok {
		return nil, fmt.Errorf("nothing given: %s", note)
	}
	tctx, cancel := p.ctxOf(ctx)
	res, lines, err := p.TB.AddItemInstances(tctx, bot, item, count)
	cancel()
	if err != nil {
		// A refusal, a malformed record or a transport failure: whatever
		// the give may have done, no answer named its copies, so none is
		// recorded and none will be deleted.
		return lines, err
	}
	if !res.HasCreated {
		return lines, fmt.Errorf("the give of item %d on %s is UNVERIFIED: the additem answer named no created item instance list, so no copy's GUID is known; nothing recorded, nothing will be deleted", item, bot)
	}
	if len(res.Created) == 0 {
		return lines, fmt.Errorf("the give of item %d on %s created no item instance of its own (merged into a copy the hire carried): nothing recorded, nothing will be deleted", item, bot)
	}
	grants := make([]ItemGrant, 0, len(res.Created))
	for _, guid := range res.Created {
		grants = append(grants, ItemGrant{Server: p.Server, Character: bot, CharGUID: p.charGUID(bot),
			Item: item, GUID: guid, Grant: nextGrant()})
	}
	if aerr := p.grants().add(grants); aerr != nil {
		return lines, fmt.Errorf("the give of %d item instance(s) %d on %s was not recorded: %w", len(grants), item, bot, aerr)
	}
	return lines, nil
}

// ClearGiven deletes every item instance a trial recorded for character, and
// only those: each grant is one GUID the server named at grant time, deleted
// on that exact character. A worn copy is unequipped first, by its own GUID
// (the module checks the slot still holds that instance). A recorded copy the character no longer holds is
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
				// A worn copy goes to the bags first, by its own GUID: the
				// module checks the instance in the slot when it moves it, so
				// a replacement copy of the same item is never touched. A
				// module that refuses (or cannot unequip by instance) leaves
				// the copy worn and the grant pending.
				tctx, cancel := p.ctxOf(ctx)
				ures, _, uerr := p.TB.UnequipItemInstance(tctx, character, grant.GUID)
				cancel()
				p.Ledger.Add(map[string]any{"kind": "unequip", "bot": character, "item_id": item,
					"item_guid": grant.GUID, "moved": ures.Moved, "error": errText(uerr)})
				if uerr != nil {
					allOK = false
					notes = append(notes, fmt.Sprintf("item %d (item guid %s): unequip by instance refused: %s; left worn",
						item, grant.GUID, tbErrText(uerr, nil)))
					continue
				}
				if ures.Moved != 1 {
					allOK = false
					notes = append(notes, fmt.Sprintf("item %d (item guid %s): the module moved no copy out of its slot; left worn", item, grant.GUID))
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
					notes = append(notes, fmt.Sprintf("item %d (item guid %s): the unequip did not put the copy into the bags; left in place",
						item, grant.GUID))
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
	return strings.Contains(evidence, "named no item instance list") ||
		strings.Contains(evidence, "named no created item instance list")
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
