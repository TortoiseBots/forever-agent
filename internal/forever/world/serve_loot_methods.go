package world

// The D5 loot methods: loot, loot_take, loot_close, open_object, use_object, gameobjects and roll. Names and JSON
// follow turtle-agent's. The one thing that differs is that the loot window outlives the call that opened it: loot
// (or open_object, or use_object) keeps the window the server granted in s.methodLoot, loot_take takes from it
// without releasing it, and loot_close, a take that empties it, the next open, a teardown or a logout give it back.
//
// Packet layouts are the ones internal/forever/loot already pins against the core; this file only sequences them
// (CMSG_LOOT_UNIT LootPackets.cpp:48, CMSG_LOOT_ITEM :79, CMSG_LOOT_RELEASE :113, CMSG_LOOT_MONEY :118,
// CMSG_LOOT_ROLL :140, CMSG_GAME_OBJ_USE and CMSG_GAME_OBJ_REPORT_USE GameObjectPackets.cpp:21-29).
//
// Preconditions (bad parameters, an unknown or distant object, a live creature, a missing window) are RPC errors
// raised before the call's own packet. Once a packet went out the call answers a result: ok false with the failure
// event. A stop (the caller's cancel, a dead link, the call's own wait_ms) stays an error.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// useObjectWaitDefaultMS is use_object's default wait: most objects answer within a tick or say nothing at all.
	useObjectWaitDefaultMS = 4000
	// goFlagLocked is GO_FLAG_LOCKED (SharedDefines.h:3239): the object needs a key or a spell.
	goFlagLocked uint32 = 0x2
	// lootRequestMax is the most requests one CMSG_LOOT_ITEM may carry (LootPackets.h:93-97).
	lootRequestMax = 100
)

type serveLootParams struct {
	GUID   GUID   `json:"guid"`
	Name   string `json:"name,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

type serveLootTakeParams struct {
	Slot   *int `json:"slot,omitempty"`
	All    bool `json:"all,omitempty"`
	Money  bool `json:"money,omitempty"`
	WaitMS int  `json:"wait_ms,omitempty"`
}

type serveLootCloseParams struct {
	WaitMS int `json:"wait_ms,omitempty"`
}

type serveOpenObjectParams struct {
	GUID    GUID  `json:"guid"`
	SpellID int32 `json:"spell_id,omitempty"`
	Take    bool  `json:"take,omitempty"`
	WaitMS  int   `json:"wait_ms,omitempty"`
}

type serveUseObjectParams struct {
	GUID   GUID `json:"guid"`
	WaitMS int  `json:"wait_ms,omitempty"`
}

type serveGameObjectsParams struct {
	Within float32 `json:"within,omitempty"`
	Radius float32 `json:"radius,omitempty"`
	Entry  int32   `json:"entry,omitempty"`
	Name   string  `json:"name,omitempty"`
	Type   *int    `json:"type,omitempty"`
	Limit  int     `json:"limit,omitempty"`
}

type serveRollParams struct {
	Looted   GUID   `json:"looted,omitempty"`
	ItemSlot *int   `json:"item_slot,omitempty"`
	RollID   uint32 `json:"roll_id,omitempty"`
	Choice   string `json:"choice"`
}

// ServeLootItem is one row of a loot window: Slot is the LootListID the server numbered it with.
type ServeLootItem struct {
	Slot      uint8  `json:"slot"`
	ItemEntry int32  `json:"item_entry"`
	Count     uint32 `json:"count"`
	SlotType  string `json:"slot_type"`
}

// ServeLootWindow is a loot window as TA's LootWindow had it: GUID is the creature or game object the window
// belongs to, LootGUID the loot's own id.
type ServeLootWindow struct {
	GUID     GUID            `json:"guid"`
	LootGUID GUID            `json:"loot_guid"`
	Type     string          `json:"type"`
	Money    uint32          `json:"money"`
	Items    []ServeLootItem `json:"items"`
}

// ServeLootResult is loot's answer: the window's fields plus the call frame.
type ServeLootResult struct {
	Since uint64 `json:"since"`
	OK    bool   `json:"ok"`
	Event *Event `json:"event"`
	ServeLootWindow
}

// ServeLootTakeResult is loot_take's answer. Taken, Left and Skipped are slot numbers; Window is null once the
// take emptied the window and it was released.
type ServeLootTakeResult struct {
	Since   uint64           `json:"since"`
	OK      bool             `json:"ok"`
	Event   *Event           `json:"event"`
	Taken   []int            `json:"taken"`
	Left    []int            `json:"left,omitempty"`
	Skipped []int            `json:"skipped,omitempty"`
	Money   uint32           `json:"money,omitempty"`
	Window  *ServeLootWindow `json:"window"`
}

// ServeLootCloseResult is loot_close's answer. Closed is false when no window was held.
type ServeLootCloseResult struct {
	Since  uint64 `json:"since"`
	OK     bool   `json:"ok"`
	Closed bool   `json:"closed"`
	Event  *Event `json:"event"`
}

// ServeOpenObjectResult is open_object's answer (TA's LootedObject): Window is null for a door or lever and after
// a take that released it.
type ServeOpenObjectResult struct {
	Since  uint64           `json:"since"`
	OK     bool             `json:"ok"`
	Event  *Event           `json:"event"`
	Window *ServeLootWindow `json:"window"`
	Taken  []ServeLootItem  `json:"taken"`
	Money  uint32           `json:"money,omitempty"`
	Left   []ServeLootItem  `json:"left,omitempty"`
}

// ServeGameObject is one row of gameobjects (and use_object's object).
type ServeGameObject struct {
	GUID       GUID    `json:"guid"`
	Entry      int32   `json:"entry"`
	Name       string  `json:"name"`
	Type       int     `json:"type"`
	TypeName   string  `json:"type_name"`
	X          float32 `json:"x"`
	Y          float32 `json:"y"`
	Z          float32 `json:"z"`
	O          float32 `json:"o"`
	Distance   float32 `json:"distance"`
	Distance2D float32 `json:"distance_2d"`
	State      int     `json:"state"`
	Flags      uint32  `json:"flags"`
}

// ServeUseObjectResult is use_object's answer. Outcome is the answering event's kind, "state_changed" or "none".
// Ritual says the object is a summoning ritual (game object type 18): a use that answers "none" there is the
// ritual waiting for its other users, not a failed use (GameObject.cpp:3195-3235).
type ServeUseObjectResult struct {
	Since   uint64           `json:"since"`
	OK      bool             `json:"ok"`
	Object  ServeGameObject  `json:"object"`
	Ritual  bool             `json:"ritual,omitempty"`
	Outcome string           `json:"outcome"`
	Event   *Event           `json:"event"`
	Hint    string           `json:"hint,omitempty"`
	Window  *ServeLootWindow `json:"window,omitempty"`
}

// ServeRollResult is roll's answer. RollID is always 0: rolls are named by looted and item_slot here.
type ServeRollResult struct {
	RollID    int    `json:"roll_id"`
	Looted    GUID   `json:"looted"`
	ItemSlot  uint8  `json:"item_slot"`
	ItemEntry int32  `json:"item_entry"`
	Choice    string `json:"choice"`
}

// registerLootMethods wires the D5 methods into the method table.
func registerLootMethods(srv *rpc.Server, st *serveState) {
	srv.Register("loot", serveCallTyped(st, func(s *session, ctx context.Context, p serveLootParams) (ServeLootResult, error) {
		return s.serveLoot(ctx, p)
	}))
	srv.Register("loot_take", serveCallTyped(st, func(s *session, ctx context.Context, p serveLootTakeParams) (ServeLootTakeResult, error) {
		return s.serveLootTake(ctx, p)
	}))
	srv.Register("loot_close", serveCallTyped(st, func(s *session, ctx context.Context, p serveLootCloseParams) (ServeLootCloseResult, error) {
		return s.serveLootClose(ctx, p)
	}))
	srv.Register("open_object", serveCallTyped(st, func(s *session, ctx context.Context, p serveOpenObjectParams) (ServeOpenObjectResult, error) {
		return s.serveOpenObject(ctx, p)
	}))
	srv.Register("use_object", serveCallTyped(st, func(s *session, ctx context.Context, p serveUseObjectParams) (ServeUseObjectResult, error) {
		return s.serveUseObject(ctx, p)
	}))
	srv.Register("gameobjects", serveCallTyped(st, func(s *session, _ context.Context, p serveGameObjectsParams) ([]ServeGameObject, error) {
		return s.serveGameObjects(p)
	}))
	srv.Register("roll", serveCallTyped(st, func(s *session, _ context.Context, p serveRollParams) (ServeRollResult, error) {
		return s.serveRoll(p)
	}))
}

// ---- the held window ----

// holdWindow copies a window for the session to hold: the packet handler removes taken slots from the copy, so it
// must not share its item slice with the loot-check state.
func holdWindow(w *loot.Response) *loot.Response {
	cp := *w
	cp.Items = append([]loot.Item(nil), w.Items...)
	cp.Currencies = append([]loot.Currency(nil), w.Currencies...)
	return &cp
}

// takeableSlot reports whether this client may take the item now: only allow-loot and owner slots can be taken;
// a roll, a locked or a master slot cannot (the same rule takeLoot applies).
func takeableSlot(it loot.Item) bool {
	return it.UIType == loot.SlotAllowLoot || it.UIType == loot.SlotOwner
}

// windowItem finds a slot in a window.
func windowItem(w *loot.Response, slot uint8) (loot.Item, bool) {
	for _, it := range w.Items {
		if it.LootListID == slot {
			return it, true
		}
	}
	return loot.Item{}, false
}

func lootItemRow(it loot.Item) ServeLootItem {
	return ServeLootItem{Slot: it.LootListID, ItemEntry: it.Instance.ItemID, Count: it.Quantity, SlotType: loot.SlotName(it.UIType)}
}

// lootWindowView is a window as the methods answer it.
func (s *session) lootWindowView(w *loot.Response) ServeLootWindow {
	kind := "unit"
	if o, ok := s.world.Object(w.Owner); ok && (o.Info.HasGameObject || o.Type == objects.TypeGameObject) {
		kind = "game_object"
	}
	v := ServeLootWindow{GUID: GUID(w.Owner), LootGUID: GUID(w.Loot), Type: kind, Money: w.Coins, Items: make([]ServeLootItem, 0, len(w.Items))}
	for _, it := range w.Items {
		v.Items = append(v.Items, lootItemRow(it))
	}
	return v
}

// heldView is the held window as a pointer for a result, nil when none is held.
func (s *session) heldView() *ServeLootWindow {
	if s.methodLoot == nil {
		return nil
	}
	v := s.lootWindowView(s.methodLoot)
	return &v
}

// methodRelease sends CMSG_LOOT_RELEASE for the held window and waits for the server's SMSG_LOOT_RELEASE, which
// handleLoot answers by clearing the held window (LootHandler.cpp:261-283: the release names the window's owner).
func (s *session) methodRelease(ctx context.Context, w *loot.Response) error {
	if err := s.sendOnCtx(ctx, sideInstance, wire.CMSGLootRelease, loot.BuildLootRelease(w.Owner)); err != nil {
		return fmt.Errorf("world: CMSG_LOOT_RELEASE: %w", err)
	}
	if _, err := s.lootWait(ctx, "the release answer", lootReleaseWait, func() bool { return s.methodLoot != w }); err != nil {
		return err
	}
	if s.methodLoot == w {
		return errors.New("world: the server did not confirm the loot release")
	}
	return nil
}

// releaseHeld gives back the held window, if any. refusal is the failure text when the server did not confirm
// (the window stays held, so loot_close can try again); err is a stop.
func (s *session) releaseHeld(parent, ctx context.Context) (refusal string, err error) {
	w := s.methodLoot
	if w == nil {
		return "", nil
	}
	if err := s.methodRelease(ctx, w); err != nil {
		if s.methodStopped(parent, err) {
			return "", err
		}
		return err.Error(), nil
	}
	return "", nil
}

// lootFailed writes the failure event of a loot call.
func (s *session) lootFailed(owner wire.GUID, data map[string]any, text string) *Event {
	return s.methodEvent(events.KindLootError, owner, data, text)
}

// serveReleaseHeldLoot is the release teardown and logout owe the server for a window a method left open. It runs
// on a context of its own (a cancelled run still gives the window back), clears the held window whatever the answer
// and reports whether the server confirmed.
func (s *session) serveReleaseHeldLoot() (detail string, ok bool) {
	w := s.methodLoot
	if w == nil {
		return "", true
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), lootCleanupWait)
	defer cancel()
	err := s.methodRelease(cctx, w)
	s.methodLoot = nil
	if err != nil {
		return err.Error(), false
	}
	return "released the held loot window", true
}

// snapshotRolls and restoreRolls keep the open group rolls across an open: lootTarget starts a fresh window with
// loot.reset(), which forgets every roll, and a roll for another corpse may still be waiting for a vote.
type rollSnapshot struct {
	rolls map[rollKey]*rollWatch
	order []rollKey
}

func (s *session) snapshotRolls() rollSnapshot {
	return rollSnapshot{rolls: s.loot.rolls, order: s.loot.order}
}

func (s *session) restoreRolls(snap rollSnapshot) {
	for _, k := range snap.order {
		if _, ok := s.loot.rolls[k]; ok {
			continue
		}
		s.loot.rolls[k] = snap.rolls[k]
		s.loot.order = append(s.loot.order, k)
	}
}

// methodOpen opens the target's window with lootTarget on a fresh loot report, keeping the open rolls.
func (s *session) methodOpen(ctx context.Context, target *objects.Object) (func(), error) {
	snap := s.snapshotRolls()
	s.report.Loot = LootReport{}
	release, err := s.lootTarget(ctx, target)
	s.restoreRolls(snap)
	return release, err
}

// openFailure turns a lootTarget error into the refusal text the call reports as ok false, or returns the error
// itself when it is a stop or a protocol failure.
func openFailure(err error) (text string, data map[string]any, stop error) {
	var refusal *LootRefusal
	switch {
	case errors.As(err, &refusal):
		return err.Error(), map[string]any{"reason": refusal.Reason}, nil
	case errors.Is(err, ErrNoLootWindow):
		return err.Error(), map[string]any{}, nil
	}
	return "", nil, err
}

// ---- loot ----

func (s *session) serveLoot(parent context.Context, p serveLootParams) (ServeLootResult, error) {
	if wire.GUID(p.GUID).IsZero() && strings.TrimSpace(p.Name) == "" {
		if s.methodLoot == nil {
			return ServeLootResult{}, errors.New("no loot window is open")
		}
		return ServeLootResult{Since: s.EventLog().Last(), OK: true, ServeLootWindow: s.lootWindowView(s.methodLoot)}, nil
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeLootResult{}, err
	}
	defer cancel()
	target, err := s.serveUnitByName(p.GUID, p.Name)
	if err != nil {
		return ServeLootResult{}, err
	}
	if target.Info.HasUnit && !target.Info.HasGameObject && !(target.Dead || (target.Info.Health <= 0 && target.Info.MaxHealth > 0)) {
		return ServeLootResult{}, fmt.Errorf("unit %s is alive: only a corpse has loot", GUID(target.GUID))
	}
	res := ServeLootResult{Since: s.EventLog().Last(), ServeLootWindow: ServeLootWindow{GUID: GUID(target.GUID), Items: []ServeLootItem{}}}
	if refusal, err := s.releaseHeld(parent, ctx); err != nil {
		return res, err
	} else if refusal != "" {
		res.Event = s.lootFailed(target.GUID, map[string]any{}, "the held loot window could not be released first: "+refusal)
		return res, nil
	}
	release, err := s.methodOpen(ctx, target)
	if err != nil {
		release()
		text, data, stop := openFailure(err)
		if stop != nil {
			return res, sentTimeout(stop, res.Since)
		}
		res.Event = s.lootFailed(target.GUID, data, text)
		return res, nil
	}
	if s.loot.window == nil {
		res.Event = s.lootFailed(target.GUID, map[string]any{}, "no loot window opened")
		return res, nil
	}
	s.methodLoot = holdWindow(s.loot.window)
	res.OK = true
	res.ServeLootWindow = s.lootWindowView(s.methodLoot)
	res.Event = s.firstEventSince(res.Since, events.KindLootOpen)
	return res, nil
}

// ---- loot_take ----

// takeSlots sends CMSG_LOOT_ITEM for the requested slots of the held window w and, when money is true and the
// window has coins, CMSG_LOOT_MONEY, then waits for the server's removals. It does not release: the window stays
// open for the next take. w must be the held window (the packet handler removes taken slots from it).
//
// taken are the requested slots the server removed, left every slot still in the window, skipped the requested
// slots this client cannot take (a roll, a lock, a master slot, or not in the window).
func (s *session) takeSlots(ctx context.Context, w *loot.Response, ids []uint8, money bool) (taken, left, skipped []int, moneyTaken bool, err error) {
	link := s.transport()
	if money && w.Coins > 0 {
		before := s.loot.coinRemoved
		if err := link.Send(wire.CMSGLootMoney, loot.BuildLootMoney()); err != nil {
			return nil, nil, nil, false, fmt.Errorf("world: CMSG_LOOT_MONEY: %w", err)
		}
		if _, err := s.lootWait(ctx, "the money answer", lootAnswerWait, func() bool { return s.loot.coinRemoved > before }); err != nil {
			return nil, nil, nil, false, err
		}
		moneyTaken = s.loot.coinRemoved > before
	}
	requests := make([]loot.Request, 0, len(ids))
	asked := make([]uint8, 0, len(ids))
	for _, id := range ids {
		it, ok := windowItem(w, id)
		if !ok || !takeableSlot(it) {
			skipped = append(skipped, int(id))
			continue
		}
		requests = append(requests, loot.Request{Loot: w.Loot, LootListID: id})
		asked = append(asked, id)
	}
	if len(requests) > lootRequestMax {
		return nil, nil, nil, moneyTaken, fmt.Errorf("a take names %d slots, the server reads at most %d", len(requests), lootRequestMax)
	}
	if len(requests) > 0 {
		if err := link.Send(wire.CMSGLootItem, loot.BuildLootItem(requests)); err != nil {
			return nil, nil, nil, moneyTaken, fmt.Errorf("world: CMSG_LOOT_ITEM: %w", err)
		}
		gone := func() bool {
			for _, id := range asked {
				if _, in := windowItem(w, id); in {
					return false
				}
			}
			return true
		}
		if _, err := s.lootWait(ctx, "the item answer", lootAnswerWait, gone); err != nil {
			return nil, nil, nil, moneyTaken, err
		}
	}
	for _, id := range asked {
		if _, in := windowItem(w, id); !in {
			taken = append(taken, int(id))
		}
	}
	for _, it := range w.Items {
		left = append(left, int(it.LootListID))
	}
	return taken, left, skipped, moneyTaken, nil
}

// allTakeable is every slot of the window this client can take.
func allTakeable(w *loot.Response) []uint8 {
	ids := make([]uint8, 0, len(w.Items))
	for _, it := range w.Items {
		if takeableSlot(it) {
			ids = append(ids, it.LootListID)
		}
	}
	return ids
}

func (s *session) serveLootTake(parent context.Context, p serveLootTakeParams) (ServeLootTakeResult, error) {
	w := s.methodLoot
	if w == nil {
		return ServeLootTakeResult{}, errors.New("no loot window is open (loot comes first)")
	}
	if p.Slot == nil && !p.All && !p.Money {
		return ServeLootTakeResult{}, errors.New("loot_take needs slot, all or money")
	}
	if p.Slot != nil && p.All {
		return ServeLootTakeResult{}, errors.New("loot_take takes slot or all, not both")
	}
	var ids []uint8
	if p.Slot != nil {
		if *p.Slot < 0 || *p.Slot > 255 {
			return ServeLootTakeResult{}, fmt.Errorf("slot %d is not 0-255", *p.Slot)
		}
		if _, ok := windowItem(w, uint8(*p.Slot)); !ok {
			return ServeLootTakeResult{}, fmt.Errorf("the window has no slot %d (taken already?)", *p.Slot)
		}
		ids = []uint8{uint8(*p.Slot)}
	}
	if p.All {
		// Every slot, so the ones this client cannot take come back as skipped.
		for _, it := range w.Items {
			ids = append(ids, it.LootListID)
		}
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeLootTakeResult{}, err
	}
	defer cancel()
	res := ServeLootTakeResult{Since: s.EventLog().Last(), Taken: []int{}}
	coins := w.Coins
	wantMoney := p.Money || p.All
	taken, left, skipped, money, err := s.takeSlots(ctx, w, ids, wantMoney)
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	res.Taken, res.Left, res.Skipped = append(res.Taken, taken...), left, skipped
	if money {
		res.Money = coins
	}
	// ok: everything asked for was taken. Under all, slots this client cannot take are reported, not failures.
	res.OK = len(taken)+len(skipped) == len(ids) && (p.All || len(skipped) == 0) && (!wantMoney || coins == 0 || money)
	if res.OK {
		res.Event = s.methodEvent(events.KindLootTaken, w.Owner, map[string]any{"slots": res.Taken, "money": res.Money}, "")
	} else {
		res.Event = s.lootFailed(w.Owner, map[string]any{"slots": res.Taken, "skipped": res.Skipped}, lootTakeFailure(len(taken), len(ids), skipped, wantMoney && coins > 0 && !money))
	}
	// A window with nothing left to take is released, so the next loot call starts clean.
	if cur := s.methodLoot; cur != nil && len(cur.Items) == 0 && cur.Coins == 0 {
		refusal, rerr := s.releaseHeld(parent, ctx)
		if rerr != nil {
			return res, rerr
		}
		if refusal != "" {
			res.OK = false
			res.Event = s.lootFailed(w.Owner, map[string]any{}, "the emptied window could not be released: "+refusal)
		}
	}
	res.Window = s.heldView()
	return res, nil
}

// lootTakeFailure words what a take that did not finish missed.
func lootTakeFailure(got, asked int, skipped []int, moneyMissed bool) string {
	var parts []string
	if len(skipped) > 0 {
		parts = append(parts, fmt.Sprintf("slot(s) %v cannot be taken (a roll, a lock or a master slot)", skipped))
	}
	if missing := asked - len(skipped) - got; missing > 0 {
		parts = append(parts, fmt.Sprintf("the server did not remove %d slot(s) in time (a full bag?)", missing))
	}
	if moneyMissed {
		parts = append(parts, "the server did not take the money")
	}
	return strings.Join(parts, "; ")
}

// ---- loot_close ----

func (s *session) serveLootClose(parent context.Context, p serveLootCloseParams) (ServeLootCloseResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeLootCloseResult{}, err
	}
	defer cancel()
	res := ServeLootCloseResult{Since: s.EventLog().Last()}
	w := s.methodLoot
	if w == nil {
		res.OK = true
		return res, nil
	}
	refusal, err := s.releaseHeld(parent, ctx)
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if refusal != "" {
		res.Event = s.lootFailed(w.Owner, map[string]any{}, refusal)
		return res, nil
	}
	res.OK, res.Closed = true, true
	res.Event = s.methodEvent(events.KindLootClosed, w.Owner, map[string]any{}, "")
	return res, nil
}

// ---- open_object ----

// serveGameObjectTarget resolves a game object from the world model, before any packet.
func (s *session) serveGameObjectTarget(guid GUID) (*objects.Object, error) {
	if wire.GUID(guid).IsZero() {
		return nil, errors.New("guid is required")
	}
	o, ok := s.world.Object(wire.GUID(guid))
	if !ok || !(o.Info.HasGameObject || o.Type == objects.TypeGameObject) {
		return nil, fmt.Errorf("no game object %s in the world model", guid)
	}
	return o, nil
}

func (s *session) serveOpenObject(parent context.Context, p serveOpenObjectParams) (ServeOpenObjectResult, error) {
	if p.SpellID != 0 {
		return ServeOpenObjectResult{}, errors.New("objects open with CMSG_GAME_OBJ_USE on this server (spell_id is not accepted)")
	}
	obj, err := s.serveGameObjectTarget(p.GUID)
	if err != nil {
		return ServeOpenObjectResult{}, err
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeOpenObjectResult{}, err
	}
	defer cancel()
	res := ServeOpenObjectResult{Since: s.EventLog().Last(), Taken: []ServeLootItem{}}
	if refusal, err := s.releaseHeld(parent, ctx); err != nil {
		return res, err
	} else if refusal != "" {
		res.Event = s.lootFailed(obj.GUID, map[string]any{}, "the held loot window could not be released first: "+refusal)
		return res, nil
	}
	release, err := s.methodOpen(ctx, obj)
	if err != nil {
		release()
		text, data, stop := openFailure(err)
		if stop != nil {
			return res, sentTimeout(stop, res.Since)
		}
		res.Event = s.lootFailed(obj.GUID, data, text)
		return res, nil
	}
	if s.loot.window == nil {
		// A door or a lever: the state change is the whole answer.
		res.OK = true
		res.Event = s.methodEvent(events.KindGameObject, obj.GUID, map[string]any{"change": "state", "state": int(s.gameObjectNow(obj.GUID).GOState)}, "")
		return res, nil
	}
	s.methodLoot = holdWindow(s.loot.window)
	res.OK = true
	res.Event = s.firstEventSince(res.Since, events.KindLootOpen)
	if !p.Take {
		v := s.lootWindowView(s.methodLoot)
		res.Window = &v
		return res, nil
	}
	w := s.methodLoot
	coins := w.Coins
	rows := map[uint8]ServeLootItem{}
	for _, it := range w.Items {
		rows[it.LootListID] = lootItemRow(it)
	}
	taken, _, _, money, err := s.takeSlots(ctx, w, allTakeable(w), true)
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	for _, id := range taken {
		res.Taken = append(res.Taken, rows[uint8(id)])
	}
	if money {
		res.Money = coins
	}
	// Whatever could not be taken stays on the object: the window is released all the same.
	for _, it := range w.Items {
		res.Left = append(res.Left, lootItemRow(it))
	}
	if refusal, err := s.releaseHeld(parent, ctx); err != nil {
		return res, err
	} else if refusal != "" {
		res.OK = false
		res.Event = s.lootFailed(obj.GUID, map[string]any{}, "the window could not be released: "+refusal)
	}
	res.Window = s.heldView()
	return res, nil
}

// ---- use_object ----

func (s *session) serveUseObject(parent context.Context, p serveUseObjectParams) (ServeUseObjectResult, error) {
	obj, err := s.serveGameObjectTarget(p.GUID)
	if err != nil {
		return ServeUseObjectResult{}, err
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, useObjectWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeUseObjectResult{}, err
	}
	defer cancel()
	deadline, _ := ctx.Deadline()
	res := ServeUseObjectResult{Since: s.EventLog().Last(), Object: s.serveGameObjectRow(obj, s.Now()), Outcome: "none",
		Ritual: obj.Info.HasGameObject && obj.Info.GOType == goTypeRitual}
	if refusal, err := s.releaseHeld(parent, ctx); err != nil {
		return res, err
	} else if refusal != "" {
		res.Event = s.lootFailed(obj.GUID, map[string]any{}, "the held loot window could not be released first: "+refusal)
		return res, nil
	}
	snap := s.snapshotRolls()
	s.loot.reset()
	s.restoreRolls(snap)
	s.report.Loot = LootReport{}
	before, seen := s.gameObjectNow(obj.GUID), obj.Info.HasGameObject
	link := s.transport()
	if err := link.Send(wire.CMSGGameObjUse, loot.BuildGameObjUse(obj.GUID)); err != nil {
		return res, fmt.Errorf("world: CMSG_GAME_OBJ_USE: %w", err)
	}
	// The real client follows the use with the report-use packet in the same tick (SpellHandler.cpp:218-229). It
	// drives the object's OnReportUse and the use criteria, so a half-sent pair is reported, not hidden.
	if err := link.Send(wire.CMSGGameObjReportUse, loot.BuildGameObjReportUse(obj.GUID)); err != nil {
		return res, fmt.Errorf("world: CMSG_GAME_OBJ_REPORT_USE: %w; the use itself went out, so its outcome is unknown (event_log from %d shows what it did)", err, res.Since)
	}
	answered := func() bool {
		return s.loot.window != nil || s.loot.refusal != nil ||
			stateChangedFrom(before, seen, s.gameObjectNow(obj.GUID)) ||
			s.firstEventSince(res.Since, events.KindGossip, events.KindQuestList, events.KindGameObject) != nil
	}
	// The wait is the call's own budget, on the caller's context: running out of it is the answer "none", not an error.
	if _, err := s.lootWait(parent, "the object's answer", time.Until(deadline), answered); err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if now, ok := s.world.Object(obj.GUID); ok {
		res.Object = s.serveGameObjectRow(now, s.Now())
	}
	switch {
	case s.loot.window != nil:
		s.methodLoot = holdWindow(s.loot.window)
		v := s.lootWindowView(s.methodLoot)
		res.Window = &v
		res.Outcome, res.OK = events.KindLootOpen, true
		res.Event = s.firstEventSince(res.Since, events.KindLootOpen)
	case s.loot.refusal != nil:
		res.Outcome = events.KindLootError
		res.Event = s.lootFailed(obj.GUID, map[string]any{"reason": s.loot.refusal.Failure}, "loot refused: "+loot.ErrorName(s.loot.refusal.Failure))
	default:
		if ev := s.firstEventSince(res.Since, events.KindGossip, events.KindQuestList, events.KindGameObject); ev != nil {
			res.Outcome, res.OK, res.Event = ev.Kind, true, ev
		} else if stateChangedFrom(before, seen, s.gameObjectNow(obj.GUID)) {
			res.Outcome, res.OK = "state_changed", true
			res.Event = s.methodEvent(events.KindGameObject, obj.GUID, map[string]any{"change": "state", "state": int(s.gameObjectNow(obj.GUID).GOState)}, "")
		}
	}
	if !res.OK {
		res.Hint = s.useObjectHint(obj)
	}
	return res, nil
}

// useObjectHint says why an object may have answered nothing: too far to use (use_object never walks), or locked.
func (s *session) useObjectHint(obj *objects.Object) string {
	x, y, z := obj.Position(s.Now())
	if d := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), x, y, z); d > lootReach {
		return fmt.Sprintf("the object is %.1f yd away; use_object never walks (open_object does, or walk_to it first)", d)
	}
	if now := s.gameObjectNow(obj.GUID); now.HasGameObject && now.GOFlags&goFlagLocked != 0 {
		return "the object is locked (it needs a key or a spell)"
	}
	return ""
}

// ---- gameobjects ----

// goTypeRitual is GameobjectTypes' ritual (SharedDefines.h:3165-3215): index 18 of gameObjectTypeNames, the
// summoning ritual a use makes one caster of (GameObject.cpp:3138-3235).
const goTypeRitual int8 = 18

// gameObjectTypeNames is GameobjectTypes (SharedDefines.h:3165-3215), lower-cased.
var gameObjectTypeNames = []string{
	"door", "button", "questgiver", "chest", "binder", "generic", "trap", "chair", "spell_focus", "text", "goober",
	"transport", "areadamage", "camera", "map_object", "map_obj_transport", "duel_arbiter", "fishingnode", "ritual",
	"mailbox", "do_not_use", "guardpost", "spellcaster", "meetingstone", "flagstand", "fishinghole", "flagdrop",
	"mini_game", "do_not_use_2", "control_zone", "aura_generator", "dungeon_difficulty", "barber_chair",
	"destructible_building", "guild_bank", "trapdoor", "new_flag", "new_flag_drop", "garrison_building",
	"garrison_plot", "client_creature", "client_item", "capture_point", "phaseable_mo", "garrison_monument",
	"garrison_shipment", "garrison_monument_plaque", "item_forge", "ui_link", "keystone_receptacle",
}

func gameObjectTypeName(t int8) string {
	if t >= 0 && int(t) < len(gameObjectTypeNames) {
		return gameObjectTypeNames[t]
	}
	return fmt.Sprintf("type %d", t)
}

func (s *session) serveGameObjectRow(o *objects.Object, now time.Time) ServeGameObject {
	x, y, z := o.Position(now)
	sx, sy, sz := s.MoverX(), s.MoverY(), s.MoverZ()
	return ServeGameObject{
		GUID: GUID(o.GUID), Entry: o.Info.Entry, Name: s.world.Name(o),
		Type: int(o.Info.GOType), TypeName: gameObjectTypeName(o.Info.GOType),
		X: x, Y: y, Z: z, O: o.O,
		Distance: objects.Distance(sx, sy, sz, x, y, z), Distance2D: objects.Distance(sx, sy, 0, x, y, 0),
		State: int(o.Info.GOState), Flags: o.Info.GOFlags,
	}
}

func (s *session) serveGameObjects(p serveGameObjectsParams) ([]ServeGameObject, error) {
	if p.Limit < 0 {
		return nil, errors.New("gameobjects: limit must not be negative")
	}
	if p.Within < 0 || p.Radius < 0 {
		return nil, errors.New("gameobjects: within and radius must not be negative")
	}
	limit := p.Limit
	if limit == 0 {
		limit = 100
	}
	within := p.Within
	if within == 0 {
		within = p.Radius
	}
	pattern := strings.ToLower(p.Name)
	now := s.Now()
	// The capacity never follows limit: a caller's huge limit must not size an allocation.
	out := make([]ServeGameObject, 0, 16)
	for _, o := range s.world.GameObjects(s.MoverX(), s.MoverY(), s.MoverZ()) {
		row := s.serveGameObjectRow(o, now)
		if within > 0 && row.Distance > within {
			continue
		}
		if p.Entry != 0 && row.Entry != p.Entry {
			continue
		}
		if p.Type != nil && row.Type != *p.Type {
			continue
		}
		if pattern != "" && !strings.Contains(strings.ToLower(row.Name), pattern) {
			continue
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		a, b := wire.GUID(out[i].GUID), wire.GUID(out[j].GUID)
		if a.Low != b.Low {
			return a.Low < b.Low
		}
		return a.High < b.High
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---- roll ----

func (s *session) serveRoll(p serveRollParams) (ServeRollResult, error) {
	if p.RollID != 0 {
		return ServeRollResult{}, errors.New("rolls are named by looted and item_slot on this server (roll_id is not accepted)")
	}
	vote, ok, err := loot.VoteFromWord(strings.ToLower(strings.TrimSpace(p.Choice)))
	if err != nil {
		return ServeRollResult{}, err
	}
	if !ok {
		return ServeRollResult{}, errors.New("roll needs choice: need, greed or pass")
	}
	lootGUID := wire.GUID(p.Looted)
	if lootGUID.IsZero() != (p.ItemSlot == nil) {
		return ServeRollResult{}, errors.New("roll takes looted and item_slot together, or neither")
	}
	var key rollKey
	if p.ItemSlot != nil {
		if *p.ItemSlot < 0 || *p.ItemSlot > 255 {
			return ServeRollResult{}, fmt.Errorf("item_slot %d is not 0-255", *p.ItemSlot)
		}
		key = rollKey{loot: lootGUID, listID: uint8(*p.ItemSlot)}
		if s.loot.rolls[key] == nil {
			return ServeRollResult{}, fmt.Errorf("no roll for loot %s slot %d", lootGUID, key.listID)
		}
	} else {
		var open []rollKey
		for _, k := range s.loot.order {
			if r := s.loot.rolls[k]; r != nil && !r.finished() && !r.voted {
				open = append(open, k)
			}
		}
		switch len(open) {
		case 0:
			return ServeRollResult{}, errors.New("no roll is open")
		case 1:
			key = open[0]
		default:
			var names []string
			for _, k := range open {
				names = append(names, fmt.Sprintf("looted=%s item_slot=%d (item %d)", GUID(k.loot), k.listID, s.loot.rolls[k].start.Item.Instance.ItemID))
			}
			return ServeRollResult{}, fmt.Errorf("%d rolls are open, name one: %s", len(open), strings.Join(names, "; "))
		}
	}
	r := s.loot.rolls[key]
	if r.finished() {
		return ServeRollResult{}, errors.New("that roll has ended")
	}
	if r.voted {
		return ServeRollResult{}, errors.New("this client already voted on that roll")
	}
	if !loot.VoteAllowed(r.start.ValidRolls, vote) {
		return ServeRollResult{}, fmt.Errorf("the roll does not allow %s (valid votes 0x%02X)", loot.RollName(vote), r.start.ValidRolls)
	}
	if err := s.transport().Send(wire.CMSGLootRoll, loot.BuildLootRoll(key.loot, key.listID, vote)); err != nil {
		return ServeRollResult{}, fmt.Errorf("world: CMSG_LOOT_ROLL: %w", err)
	}
	r.voted, r.ours = true, vote
	return ServeRollResult{Looted: GUID(key.loot), ItemSlot: key.listID, ItemEntry: r.start.Item.Instance.ItemID, Choice: loot.RollName(vote)}, nil
}
