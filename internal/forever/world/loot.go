package world

// The loot model: what this session knows of the windows, money, group rolls and object uses the server sends, and
// the client's side of opening, voting, taking and releasing. The packet layouts live in internal/forever/loot; this
// file is the state and the flow. Everything runs on the session goroutine (the packet handler and the waiting
// command share it), so the fields need no lock, like the fight log.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	// ErrLootRefused is a window the server refused (SMSG_LOOT_RESPONSE with Acquired false).
	ErrLootRefused = errors.New("world: the server refused the loot window")
	// ErrNoLootWindow is an open the server never answered: a creature with nothing to loot is answered with
	// silence by HandleLootOpcode (LootHandler.cpp:213-259), so this is its normal refusal.
	ErrNoLootWindow = errors.New("world: the server sent no loot window")
)

// LootRefusal is the refusal's own reason (LootError, Loot.h:137-153).
type LootRefusal struct {
	Reason uint8
}

func (e *LootRefusal) Error() string {
	return fmt.Sprintf("%s: %s (reason %d)", ErrLootRefused, loot.ErrorName(e.Reason), e.Reason)
}

func (e *LootRefusal) Unwrap() error { return ErrLootRefused }

// Wait budgets. The window and its roll starts follow the open in the same server tick (Player::SendLoot sends the
// response, then Loot::OnLootOpened starts the rolls, Loot.cpp:10019-10046 and :857-905); the answers to a take or a
// release are immediate; the roll's own result arrives when it ends, which the start's RollTime bounds.
const (
	lootWindowWait    = 4 * time.Second
	lootRollStartWait = 1500 * time.Millisecond
	lootAnswerWait    = 4 * time.Second
	lootReleaseWait   = 3 * time.Second
	lootRollSlack     = 3 * time.Second
	// lootRollWaitMax caps the wait for a roll's result. The start's RollTimeMs is the server's choice (a uint32,
	// up to ~49 days) and nothing but the run's own deadline bounds it otherwise, so one absurd start would swallow
	// the whole run. The core's own roll timeout is one minute (LOOT_ROLL_TIMEOUT, Loot.h:34), so 90 s is the
	// longest a real roll can take; a server that asks for more is not waited out.
	lootRollWaitMax = 90 * time.Second
	// lootCleanupWait is the budget of the release a failed run still owes the server: a window it was granted
	// stays open on the server until the loot's own timeout otherwise. The cleanup runs on a context of its own,
	// because the run's deadline is often the very reason the run ended.
	lootCleanupWait = 10 * time.Second
	// lootReach is how close the character stands to open a window: inside the client's and the server's 5-yard
	// interaction range (ObjectDefines.h:24, GameObject::IsAtInteractDistance, GameObject.cpp:4405-4437).
	lootReach = 4.0
)

// rollKey identifies one roll: the loot it belongs to and the item's slot.
type rollKey struct {
	loot   wire.GUID
	listID uint8
}

// rollWatch is one started roll.
type rollWatch struct {
	start    loot.StartRoll
	votes    []loot.Roll
	won      *loot.RollWon
	passed   *loot.AllPassed
	complete *loot.RollsComplete
	voted    bool
	ours     uint8 // the vote this session sent, 0xFF when none
}

// finished reports whether the server said how the roll ended.
func (r *rollWatch) finished() bool {
	return r.won != nil || r.passed != nil || r.complete != nil
}

// lootWatch is the session's loot state.
type lootWatch struct {
	// window is the last window the server opened for this session, refusal the last one it refused.
	window  *loot.Response
	refusal *loot.Response
	// removed counts SMSG_LOOT_REMOVED for the open window and coinRemoved the money removals.
	removed     int
	coinRemoved int
	money       *loot.MoneyNotify
	releases    int
	// rolls is keyed by loot GUID and slot; order keeps them in arrival order for the report.
	rolls map[rollKey]*rollWatch
	order []rollKey
}

func newLootWatch() *lootWatch {
	return &lootWatch{rolls: make(map[rollKey]*rollWatch)}
}

// reset starts a fresh window: the old rolls belong to the old loot.
func (l *lootWatch) reset() {
	l.window = nil
	l.refusal = nil
	l.removed = 0
	l.coinRemoved = 0
	l.money = nil
	l.rolls = make(map[rollKey]*rollWatch)
	l.order = nil
}

// startedRolls returns the rolls started for a loot GUID.
func (l *lootWatch) startedRolls(lootGUID wire.GUID) []*rollWatch {
	out := make([]*rollWatch, 0, len(l.order))
	for _, key := range l.order {
		if key.loot == lootGUID {
			out = append(out, l.rolls[key])
		}
	}
	return out
}

// handleLoot applies one loot or game-object packet. A parse error ends the session: these packets are this
// round's subject, and a layout this client cannot read must not be guessed at.
func (s *session) handleLoot(p worldconn.Packet) error {
	switch p.Core {
	case wire.SMSGLootResponse:
		r, err := loot.ParseResponse(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_RESPONSE: %w", err)
		}
		if !r.Acquired {
			s.loot.refusal = r
			return nil
		}
		s.loot.window = r
		s.eventLoot(events.KindLootOpen, r.Loot, map[string]any{"coins": r.Coins, "items": len(r.Items), "method": r.Method})
	case wire.SMSGLootRemoved:
		r, err := loot.ParseRemoved(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_REMOVED: %w", err)
		}
		s.loot.removed++
		if s.loot.window != nil && r.Loot == s.loot.window.Loot {
			s.report.Loot.Removed = append(s.report.Loot.Removed, r.LootListID)
		}
	case wire.SMSGCoinRemoved:
		if _, err := loot.ParseCoinRemoved(p.Payload); err != nil {
			return fmt.Errorf("world: SMSG_COIN_REMOVED: %w", err)
		}
		s.loot.coinRemoved++
	case wire.SMSGLootMoneyNotify:
		r, err := loot.ParseMoneyNotify(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_MONEY_NOTIFY: %w", err)
		}
		s.loot.money = &r
		s.report.Loot.MoneyTaken = r.Money
		s.report.Loot.MoneyMod = r.MoneyMod
	case wire.SMSGLootRelease:
		r, err := loot.ParseRelease(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_RELEASE: %w", err)
		}
		s.loot.releases++
		// SMSG_LOOT_RELEASE names the window's OWNER, not the LootObject: DoLootRelease sends
		// `player->SendLootRelease(lguid)` with lguid = loot->GetOwnerGUID(), and Player::SendLootRelease puts that
		// guid in LootObj (LootHandler.cpp:269-283, Player.cpp:10102-10108). The LootObject GUID the window's own
		// Loot field carries is what CMSG_LOOT_ITEM and the removals name, never the release.
		if s.loot.window != nil && r.Loot == s.loot.window.Owner {
			s.report.Loot.Released = true
		}
	case wire.SMSGLootReleaseAll:
		// No payload: the server closed every window of this player (WorldSession::DoLootReleaseAll).
	case wire.SMSGLootList:
		if _, err := loot.ParseList(p.Payload); err != nil {
			return fmt.Errorf("world: SMSG_LOOT_LIST: %w", err)
		}
	case wire.SMSGStartLootRoll:
		r, err := loot.ParseStartRoll(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_START_LOOT_ROLL: %w", err)
		}
		key := rollKey{loot: r.Loot, listID: r.Item.LootListID}
		if _, exists := s.loot.rolls[key]; !exists {
			s.loot.rolls[key] = &rollWatch{start: r, ours: 0xFF}
			s.loot.order = append(s.loot.order, key)
			s.eventLoot(events.KindLootStart, r.Loot, map[string]any{"item": int32(r.Item.Instance.ItemID), "slot": r.Item.LootListID, "ms": r.RollTimeMs})
		}
	case wire.SMSGLootRoll:
		r, err := loot.ParseRoll(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_ROLL: %w", err)
		}
		key := rollKey{loot: r.Loot, listID: r.Item.LootListID}
		if roll := s.loot.rolls[key]; roll != nil {
			roll.votes = append(roll.votes, r)
			s.eventLoot(events.KindLootRoll, r.Loot, map[string]any{"item": int32(r.Item.Instance.ItemID), "slot": r.Item.LootListID, "player": eventGUID(r.Player), "number": r.Number, "type": r.RollType, "autopassed": r.Autopassed})
		}
	case wire.SMSGLootRollWon:
		r, err := loot.ParseRollWon(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_ROLL_WON: %w", err)
		}
		key := rollKey{loot: r.Loot, listID: r.Item.LootListID}
		if roll := s.loot.rolls[key]; roll != nil {
			roll.won = &r
			s.eventLoot(events.KindLootWon, r.Loot, map[string]any{"item": int32(r.Item.Instance.ItemID), "slot": r.Item.LootListID, "winner": eventGUID(r.Winner), "number": r.Number})
		}
	case wire.SMSGLootAllPassed:
		r, err := loot.ParseAllPassed(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_ALL_PASSED: %w", err)
		}
		key := rollKey{loot: r.Loot, listID: r.Item.LootListID}
		if roll := s.loot.rolls[key]; roll != nil {
			roll.passed = &r
			s.eventLoot(events.KindLootAllPassed, r.Loot, map[string]any{"item": int32(r.Item.Instance.ItemID), "slot": r.Item.LootListID})
		}
	case wire.SMSGLootRollsComplete:
		r, err := loot.ParseRollsComplete(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_LOOT_ROLLS_COMPLETE: %w", err)
		}
		key := rollKey{loot: r.Loot, listID: r.LootListID}
		if roll := s.loot.rolls[key]; roll != nil {
			roll.complete = &r
		}
	default:
		return nil
	}
	return nil
}

// isLootPacket reports the opcodes handleLoot owns.
func isLootPacket(core uint32) bool {
	switch core {
	case wire.SMSGLootResponse, wire.SMSGLootRemoved, wire.SMSGCoinRemoved, wire.SMSGLootMoneyNotify,
		wire.SMSGLootRelease, wire.SMSGLootReleaseAll, wire.SMSGLootList, wire.SMSGStartLootRoll,
		wire.SMSGLootRoll, wire.SMSGLootRollWon, wire.SMSGLootAllPassed, wire.SMSGLootRollsComplete:
		return true
	}
	return false
}

// lootWait answers server packets until ready is true or the budget runs out. A timeout is not an error: the
// caller decides what silence means.
func (s *session) lootWait(ctx context.Context, what string, d time.Duration, ready func() bool) (bool, error) {
	if ready() {
		return true, nil
	}
	end := time.Now().Add(d)
	for {
		left := time.Until(end)
		if left <= 0 {
			return false, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		ev, err := s.next(what, left)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return false, nil
			}
			return false, err
		}
		if err := s.handle(ctx, ev); err != nil {
			return false, err
		}
		if ready() {
			return true, nil
		}
	}
}

// gameObjectNow is the object as the model holds it now, zero when it is gone.
func (s *session) gameObjectNow(guid wire.GUID) objects.Info {
	if o, ok := s.world.Object(guid); ok {
		return o.Info
	}
	return objects.Info{}
}

// stateChangedFrom reports a game object whose state or flags moved since the snapshot.
func stateChangedFrom(before objects.Info, seen bool, now objects.Info) bool {
	if !seen || !now.HasGameObject {
		return false
	}
	return now.GOState != before.GOState || now.GOFlags != before.GOFlags
}

// lootTarget walks to target, opens its window and reads it. For a game object it sends the use and waits for the
// window or the state change; for a unit it sends CMSG_LOOT_UNIT. It does not take anything. The returned release
// must be deferred by the caller: it gives back whatever window the session holds when the run ends before its own
// release (see releaseLeftover), so it is a no-op only when no window is open or the run's own release closed it. It
// reads s.loot.window at call time, not here: a window may arrive for another owner than the target, after the
// window wait, or during a later wait, and every one of those exits still owes the server a release.
func (s *session) lootTarget(ctx context.Context, target *objects.Object) (release func(), err error) {
	release = func() { s.releaseLeftover(s.loot.window) }
	link := s.transport()
	r := &s.report.Loot
	r.Kind = "unit"
	if target.Info.HasGameObject || target.Type == objects.TypeGameObject {
		r.Kind = "game object"
	}
	r.Type = objects.TypeName(target.Type)
	r.Entry = target.Info.Entry
	r.Name = s.world.Name(target)
	now := link.Now()
	tx, ty, tz := target.Position(now)
	r.Distance = objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
	if s.mover != nil && r.Distance > lootReach {
		px, py, pz := approachPoint(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz, lootReach*0.8, 1)
		if err := s.mover.MoveTo(ctx, px, py, pz); err != nil && !errors.Is(err, context.Canceled) {
			s.opts.logf("world: the walk to the %s stopped: %v", r.Kind, err)
		}
	}

	before := s.gameObjectNow(target.GUID)
	seen := target.Info.HasGameObject
	s.loot.reset()

	// The window is answered for the owner the server itself names (the creature or the game object), never for
	// whatever GUID the client guessed at (DoLootRelease/Loot::Owner, LootHandler.cpp:269-275).
	found := func() bool {
		return s.loot.window != nil || s.loot.refusal != nil ||
			stateChangedFrom(before, seen, s.gameObjectNow(target.GUID))
	}

	if r.Kind == "game object" {
		if err := link.Send(wire.CMSGGameObjUse, loot.BuildGameObjUse(target.GUID)); err != nil {
			return release, fmt.Errorf("world: CMSG_GAME_OBJ_USE: %w", err)
		}
		// The real client follows the use with the report-use packet in the same tick; the core only runs the
		// object's OnReportUse and the use criteria (SpellHandler.cpp:218-229), so its answer is not waited for.
		_ = link.Send(wire.CMSGGameObjReportUse, loot.BuildGameObjReportUse(target.GUID))
	} else {
		if err := link.Send(wire.CMSGLootUnit, loot.BuildLootUnit(target.GUID)); err != nil {
			return release, fmt.Errorf("world: CMSG_LOOT_UNIT: %w", err)
		}
	}
	if _, err := s.lootWait(ctx, "the loot window", lootWindowWait, found); err != nil {
		return release, err
	}
	// The object's own state is reported whatever the window did, so a refused use still says whether the object
	// moved.
	after := s.gameObjectNow(target.GUID)
	r.StateBefore, r.FlagsBefore = before.GOState, before.GOFlags
	r.StateAfter, r.FlagsAfter = after.GOState, after.GOFlags
	r.StateKnown = seen && after.HasGameObject
	r.StateChanged = stateChangedFrom(before, seen, after)
	if s.loot.refusal != nil {
		if s.loot.refusal.Owner != target.GUID {
			return release, fmt.Errorf("world: the refusal names another object (%v)", s.loot.refusal.Owner)
		}
		r.Failure = s.loot.refusal.Failure
		return release, &LootRefusal{Reason: s.loot.refusal.Failure}
	}
	if s.loot.window != nil && s.loot.window.Owner != target.GUID {
		return release, fmt.Errorf("world: the window belongs to another object (%v)", s.loot.window.Owner)
	}
	if s.loot.window == nil {
		if r.StateChanged {
			return release, nil // a door or lever: the state change is the whole answer
		}
		return release, ErrNoLootWindow
	}
	r.Opened = true
	s.fillLootWindow(s.loot.window)
	return release, nil
}

// fillLootWindow copies the window into the report: the method, the threshold, the coins and one row per item.
func (s *session) fillLootWindow(w *loot.Response) {
	r := &s.report.Loot
	r.Method = w.Method
	r.Threshold = w.Threshold
	r.Coins = w.Coins
	r.Items = r.Items[:0]
	for _, it := range w.Items {
		row := LootItemRow{
			LootListID: it.LootListID,
			ItemID:     it.Instance.ItemID,
			Count:      it.Quantity,
			Slot:       it.UIType,
			Roll:       it.UIType == loot.SlotRollOngoing,
			Trade:      it.CanTradeToTapList,
		}
		r.Items = append(r.Items, row)
	}
	r.Currencies = len(w.Currencies)
}

// voteRolls waits for the roll starts that belong to the window, votes once on each when a vote was asked for, and
// then waits for the rolls' own results. It never votes need unless the caller asked for need: the caller's vote is
// a single choice made before the roll started.
func (s *session) voteRolls(ctx context.Context, w *loot.Response, hasVote bool, vote uint8) error {
	link := s.transport()
	rollItems := 0
	for _, it := range w.Items {
		if it.UIType == loot.SlotRollOngoing {
			rollItems++
		}
	}
	if rollItems == 0 {
		return nil
	}
	// The starts follow the window in the same tick; wait for as many as the window marked for a roll (bounded).
	_, err := s.lootWait(ctx, "the roll starts", lootRollStartWait, func() bool {
		return len(s.loot.startedRolls(w.Loot)) >= rollItems
	})
	if err != nil {
		return err
	}
	starts := s.loot.startedRolls(w.Loot)
	if len(starts) == 0 {
		s.opts.logf("loot-check: %d item(s) need a roll and the server started none", rollItems)
		return nil
	}
	for _, roll := range starts {
		if roll.voted {
			continue
		}
		roll.voted = true
		row := s.report.Loot.addRoll(roll.start)
		if !hasVote {
			s.opts.logf("loot-check: a roll started for item %d (slot %d, %d ms, valid votes 0x%02X); no vote was asked for",
				roll.start.Item.Instance.ItemID, roll.start.Item.LootListID, roll.start.RollTimeMs, roll.start.ValidRolls)
			continue
		}
		if !loot.VoteAllowed(roll.start.ValidRolls, vote) {
			s.opts.logf("loot-check: the roll for item %d does not allow %s (valid votes 0x%02X); not voting",
				roll.start.Item.Instance.ItemID, loot.RollName(vote), roll.start.ValidRolls)
			continue
		}
		if err := link.Send(wire.CMSGLootRoll, loot.BuildLootRoll(w.Loot, roll.start.Item.LootListID, vote)); err != nil {
			return fmt.Errorf("world: CMSG_LOOT_ROLL: %w", err)
		}
		roll.ours = vote
		row.Vote = vote
		s.report.Loot.Votes++
		s.opts.logf("loot-check: voted %s on item %d (slot %d)", loot.RollName(vote),
			roll.start.Item.Instance.ItemID, roll.start.Item.LootListID)
	}
	// Each roll ends with its own result at the latest when its time limit does (LootRoll::UpdateRoll,
	// Loot.cpp:568-578).
	limit := rollResultWait(starts)
	_, err = s.lootWait(ctx, "the roll results", limit, func() bool {
		for _, roll := range starts {
			if !roll.finished() {
				return false
			}
		}
		return true
	})
	if err != nil {
		return err
	}
	s.fillRollResults(w)
	return nil
}

// rollResultWait is how long the results of these rolls get: the longest start's own limit plus the slack, capped at
// lootRollWaitMax, or the start wait when the server named no limit at all.
func rollResultWait(starts []*rollWatch) time.Duration {
	var limit time.Duration
	for _, roll := range starts {
		if d := time.Duration(roll.start.RollTimeMs) * time.Millisecond; d > limit {
			limit = d
		}
	}
	if limit <= 0 {
		limit = lootRollStartWait
	}
	limit += lootRollSlack
	if limit > lootRollWaitMax {
		limit = lootRollWaitMax
	}
	return limit
}

// takeLoot takes the money and up to take items (0 means every item the window allows) and releases the window.
func (s *session) takeLoot(ctx context.Context, w *loot.Response, take int, money bool) error {
	link := s.transport()
	if money && w.Coins > 0 {
		if err := link.Send(wire.CMSGLootMoney, loot.BuildLootMoney()); err != nil {
			return fmt.Errorf("world: CMSG_LOOT_MONEY: %w", err)
		}
		before := s.loot.coinRemoved
		if _, err := s.lootWait(ctx, "the money answer", lootAnswerWait, func() bool {
			return s.loot.coinRemoved > before || s.loot.money != nil
		}); err != nil {
			return err
		}
		if s.loot.money == nil && s.loot.coinRemoved == before {
			s.opts.logf("loot-check: the server did not answer CMSG_LOOT_MONEY")
		}
	}
	// A roll-ongoing, locked or master item cannot be taken by this client; only allow-loot and owner slots can.
	requests := make([]loot.Request, 0, len(w.Items))
	for _, it := range w.Items {
		if take > 0 && len(requests) >= take {
			break
		}
		if it.UIType != loot.SlotAllowLoot && it.UIType != loot.SlotOwner {
			continue
		}
		requests = append(requests, loot.Request{Loot: w.Loot, LootListID: it.LootListID})
	}
	if len(requests) > 0 {
		if err := link.Send(wire.CMSGLootItem, loot.BuildLootItem(requests)); err != nil {
			return fmt.Errorf("world: CMSG_LOOT_ITEM: %w", err)
		}
		want := s.loot.removed + len(requests)
		if _, err := s.lootWait(ctx, "the item answer", lootAnswerWait, func() bool {
			return s.loot.removed >= want
		}); err != nil {
			return err
		}
		if s.loot.removed < want {
			s.opts.logf("loot-check: asked for %d item(s), the server removed %d", len(requests), want-s.loot.removed)
		}
	}
	return s.releaseLoot(ctx, w)
}

// releaseLoot sends CMSG_LOOT_RELEASE for the unit the window belongs to and waits for its confirmation.
func (s *session) releaseLoot(ctx context.Context, w *loot.Response) error {
	link := s.transport()
	if err := link.Send(wire.CMSGLootRelease, loot.BuildLootRelease(w.Owner)); err != nil {
		return fmt.Errorf("world: CMSG_LOOT_RELEASE: %w", err)
	}
	closed := func() bool {
		return s.loot.releases > 0 && s.report.Loot.Released
	}
	if _, err := s.lootWait(ctx, "the release answer", lootReleaseWait, closed); err != nil {
		return err
	}
	if !s.report.Loot.Released {
		return fmt.Errorf("world: the server did not confirm the loot release")
	}
	return nil
}

// releaseLeftover is the release a run owes the server when it ends after a window was accepted and before its own
// release: an expired run window, a parse error, a failed send. It writes on a context of its own (lootCleanupWait,
// never the run's), so a run whose deadline ended it can still give the window back, and its own failure is recorded
// on the report (ReleaseAttempted, ReleaseErr) instead of replacing the run's error. Only the server's confirmation
// of the release (SMSG_LOOT_RELEASE for this window) suppresses the write: a completed local send proves nothing
// about the server, so an unconfirmed normal release is written again here.
func (s *session) releaseLeftover(w *loot.Response) {
	r := &s.report.Loot
	if w == nil || r.Released {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), lootCleanupWait)
	defer cancel()
	r.ReleaseAttempted = true
	if err := s.sendOnCtx(cctx, sideInstance, wire.CMSGLootRelease, loot.BuildLootRelease(w.Owner)); err != nil {
		r.ReleaseErr = fmt.Sprintf("CMSG_LOOT_RELEASE: %v", err)
		s.opts.logf("loot: the window's cleanup release failed: %v", err)
		return
	}
	s.opts.logf("loot: the window's cleanup release sent")
	if _, err := s.lootWait(cctx, "the release answer", lootReleaseWait, func() bool { return r.Released }); err != nil {
		r.ReleaseErr = fmt.Sprintf("no answer to CMSG_LOOT_RELEASE: %v", err)
		s.opts.logf("loot: %s", r.ReleaseErr)
		return
	}
	if !r.Released {
		r.ReleaseErr = "the server did not confirm the cleanup release"
		s.opts.logf("loot: %s", r.ReleaseErr)
	}
}

// fillRollResults writes one row per ended roll: who won, with which number, or that everybody passed.
func (s *session) fillRollResults(w *loot.Response) {
	rows := s.report.Loot.Rolls
	for i := range rows {
		key := rollKey{loot: w.Loot, listID: rows[i].LootListID}
		roll := s.loot.rolls[key]
		if roll == nil {
			continue
		}
		if roll.won != nil {
			rows[i].Winner = "another player"
			if roll.won.Winner == s.self {
				rows[i].Winner = "you"
			}
			rows[i].Number = roll.won.Number
			rows[i].Vote = roll.won.RollType
			rows[i].Ended = true
		} else if roll.passed != nil {
			rows[i].Winner = "nobody"
			rows[i].AllPassed = true
			rows[i].Ended = true
		} else if roll.complete != nil {
			rows[i].Winner = "nobody"
			rows[i].Ended = true
		}
		for _, v := range roll.votes {
			if v.Autopassed {
				rows[i].Autopassed++
			}
			if v.OffSpec {
				rows[i].OffSpec++
			}
		}
		rows[i].VoteSeen = len(roll.votes)
		rows[i].OurVote = roll.ours
	}
}

// addRoll appends a roll row for a start and returns it.
func (r *LootReport) addRoll(start loot.StartRoll) *LootRollRow {
	row := LootRollRow{
		LootListID: start.Item.LootListID,
		ItemID:     start.Item.Instance.ItemID,
		Count:      start.Item.Quantity,
		ValidRolls: start.ValidRolls,
		TimeLimit:  start.RollTimeMs,
		OurVote:    0xFF,
	}
	r.Rolls = append(r.Rolls, row)
	return &r.Rolls[len(r.Rolls)-1]
}
