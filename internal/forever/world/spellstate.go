package world

// The session's spell state (ticket #93 round R6): GCD, spell/category cooldowns, login spell history, cast history,
// combo points, and cooldown/combo events. Owned by the session goroutine like the spellbook.

import (
	"sort"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	minGCDMS = 750
	maxGCDMS = 1500
	// dupFailureWindow deduplicates an interrupt's paired SMSG_SPELL_FAILURE + SMSG_SPELL_FAILED_OTHER.
	dupFailureWindow = 250 * time.Millisecond
)

type gcdState struct {
	start      time.Time
	total      time.Duration
	spellID    int32
	castID     wire.GUID
	castActive bool
	unknownAt  time.Time
}

type lastCast struct {
	spell int32
	at    time.Time
}

type lastFailure struct {
	spell  int32
	reason int32
	at     time.Time
}

type pendingHistoryCooldown struct {
	spellID  int32
	end      time.Time
	category uint32
	// emit, when set, is the login cooldown_start event held until the self identity exists.
	emit *pendingCooldownEvent
}

type pendingCooldownEvent struct {
	spellID  uint32
	ms       uint32
	category uint16
	onHold   bool
}

type spellState struct {
	gcd            gcdState
	lastCast       *lastCast
	lastFailure    *lastFailure
	failures       uint64
	comboKnown     bool
	comboPoints    uint8
	pendingHistory []pendingHistoryCooldown
}

func (s *session) spellState() *spellState {
	if s.spellSt == nil {
		s.spellSt = &spellState{}
	}
	return s.spellSt
}

func clampGCDMS(ms uint16) time.Duration {
	if ms == 0 {
		return 0
	}
	v := int(ms)
	if v < minGCDMS {
		v = minGCDMS
	} else if v > maxGCDMS {
		v = maxGCDMS
	}
	return time.Duration(v) * time.Millisecond
}

// onOwnSpellStart starts the GCD on the player's own SMSG_SPELL_START when the table row has gcd_category != 0 and
// gcd_ms > 0. An unknown spell records no GCD and marks gcd_known false until the next known cast or 1.5 s passes.
func (s *session) onOwnSpellStart(v *combat.SpellCast, now time.Time) {
	st := s.spellState()
	sp, ok := s.spellTable().Lookup(uint32(v.SpellID))
	if !ok {
		st.gcd = gcdState{spellID: v.SpellID, castID: v.CastID, castActive: true, unknownAt: now}
		return
	}
	st.gcd.unknownAt = time.Time{}
	if sp.GCDCategory != 0 && sp.GCDMS > 0 {
		st.gcd = gcdState{
			start:      now,
			total:      clampGCDMS(sp.GCDMS),
			spellID:    v.SpellID,
			castID:     v.CastID,
			castActive: true,
		}
	}
}

// onOwnSpellGo records the completed cast and starts its spell and category cooldowns per D17 (or puts a
// CooldownOnEvent spell on hold until SMSG_COOLDOWN_EVENT).
func (s *session) onOwnSpellGo(v *combat.SpellCast, now time.Time) {
	st := s.spellState()
	st.lastCast = &lastCast{spell: v.SpellID, at: now}
	if st.gcd.castActive && (st.gcd.castID == v.CastID || st.gcd.spellID == v.SpellID) {
		st.gcd.castActive = false
	}
	sp, ok := s.spellTable().Lookup(uint32(v.SpellID))
	if !ok || sp.Passive() {
		return
	}
	if sp.CooldownOnEvent() {
		s.world.StartCooldown(s.self, v.SpellID, time.Time{}, 0)
		return
	}
	ownMS, catMS := sp.RecoveryMS, sp.CategoryRecoveryMS
	if ownMS == 0 && catMS == 0 {
		return
	}
	durMS := ownMS
	if durMS == 0 {
		durMS = catMS
	}
	s.startSpellCooldowns(v.SpellID, now, ownMS, catMS, sp.Category)
	s.emitCooldownStart(uint32(v.SpellID), durMS, sp.Category, "cast", false)
}

func (s *session) startSpellCooldowns(spellID int32, now time.Time, ownMS, catMS uint32, category uint16) {
	durMS := ownMS
	if durMS == 0 {
		durMS = catMS
	}
	ownEnd := now.Add(time.Duration(durMS) * time.Millisecond)
	if category != 0 && catMS > 0 {
		catEnd := now.Add(time.Duration(catMS) * time.Millisecond)
		if ownEnd.Equal(catEnd) {
			s.world.StartCooldown(s.self, spellID, ownEnd, uint32(category))
		} else {
			s.world.StartCooldown(s.self, spellID, ownEnd, 0)
			s.world.StartCooldown(s.self, 0, catEnd, uint32(category))
		}
		return
	}
	s.world.StartCooldown(s.self, spellID, ownEnd, 0)
}

func (s *session) emitCooldownStart(spellID uint32, ms uint32, category uint16, source string, onHold bool) {
	data := map[string]any{"ms": ms, "source": source}
	if category != 0 {
		data["category"] = category
	}
	if onHold {
		data["on_hold"] = true
	}
	s.event(Event{
		Kind:       events.KindCooldownStart,
		SourceGUID: eventGUID(s.self),
		SpellID:    spellID,
		Text:       s.spellName(int32(spellID)),
		Data:       data,
	})
}

// onOwnSpellFailure records a failure of the player's own cast and ends the GCD if the failure belongs to the cast
// that started it.
func (s *session) onOwnSpellFailure(core uint32, v combat.SpellFailure, now time.Time) {
	st := s.spellState()
	dup := core == wire.SMSGSpellFailedOther && st.lastFailure != nil &&
		st.lastFailure.spell == v.SpellID && st.lastFailure.reason == v.Reason &&
		now.Sub(st.lastFailure.at) < dupFailureWindow
	if !dup {
		st.failures++
	}
	st.lastFailure = &lastFailure{spell: v.SpellID, reason: v.Reason, at: now}
	if st.gcd.castActive && (st.gcd.castID == v.CastID || st.gcd.spellID == v.SpellID) {
		st.gcd = gcdState{}
	}
}

// onSpellCooldown records SMSG_SPELL_COOLDOWN and emits cooldown_start for self.
func (s *session) onSpellCooldown(v combat.Cooldowns, now time.Time) {
	s.world.ApplyCooldowns(v, now)
	if !s.selfSeen || v.Caster != s.self {
		return
	}
	for _, cd := range v.List {
		var cat uint16
		if sp, ok := s.spellTable().Lookup(cd.SpellID); ok {
			cat = sp.Category
		}
		s.emitCooldownStart(cd.SpellID, cd.ForcedCooldown, cat, "server", cd.ForcedCooldown == 0)
	}
}

// onCooldownEvent starts the spell's cooldown from the table when known (D14), or clears an on-hold entry when not.
func (s *session) onCooldownEvent(v combat.CooldownEvent, now time.Time) {
	if v.Pet {
		return
	}
	if sp, ok := s.spellTable().Lookup(uint32(v.SpellID)); ok {
		durMS := sp.RecoveryMS
		if durMS == 0 {
			durMS = sp.CategoryRecoveryMS
		}
		if durMS > 0 {
			s.startSpellCooldowns(v.SpellID, now, sp.RecoveryMS, sp.CategoryRecoveryMS, sp.Category)
			s.emitCooldownStart(uint32(v.SpellID), durMS, sp.Category, "event", false)
			return
		}
	}
	s.clearSpellCooldown(v.SpellID, now)
}

func (s *session) clearSpellCooldown(spellID int32, now time.Time) {
	if !s.selfSeen || s.world == nil {
		return
	}
	var cat uint16
	if sp, ok := s.spellTable().Lookup(uint32(spellID)); ok {
		cat = sp.Category
	}
	wasRunning := false
	if cs, ok := s.world.Combat(s.self); ok {
		for _, cd := range cs.Cooldowns {
			if cd.SpellID == spellID && cd.Active(now) {
				wasRunning = true
				break
			}
		}
		if !wasRunning && cat != 0 {
			if end, ok := cs.CategoryCooldowns[uint32(cat)]; ok && now.Before(end) {
				wasRunning = true
			}
		}
	}
	s.world.ClearCooldown(s.self, spellID, now)
	if cat != 0 {
		s.world.ClearCategoryCooldown(s.self, uint32(cat))
	}
	if wasRunning {
		s.event(Event{
			Kind:       events.KindCooldownClear,
			SourceGUID: eventGUID(s.self),
			SpellID:    uint32(spellID),
			Text:       s.spellName(spellID),
		})
	}
}

// onSpellHistory seeds login cooldowns from SMSG_SEND_SPELL_HISTORY. If the self object is not in the world model
// yet (SendInitialPacketsBeforeAddToMap arrives before ThisIsYou), entries are queued until applyUpdate creates it.
func (s *session) onSpellHistory(v combat.SpellHistory, now time.Time) {
	for _, e := range v.Entries {
		if e.OnHold {
			s.applyOrQueueHistoryCooldown(int32(e.SpellID), time.Time{}, 0,
				&pendingCooldownEvent{spellID: e.SpellID, category: uint16(e.Category), onHold: true})
			continue
		}
		ownMS := int64(e.RecoveryTime)
		if e.RecoveryStartOffset != nil {
			ownMS -= int64(*e.RecoveryStartOffset)
		}
		catMS := int64(e.CategoryRecoveryTime)
		if e.CategoryStartOffset != nil {
			catMS -= int64(*e.CategoryStartOffset)
		}
		if ownMS <= 0 && catMS <= 0 {
			continue
		}
		if e.Category != 0 && catMS > 0 {
			s.applyOrQueueHistoryCooldown(0, now.Add(time.Duration(catMS)*time.Millisecond), e.Category, nil)
		}
		durMS := ownMS
		if durMS <= 0 {
			durMS = catMS
		}
		if durMS > 0 {
			s.applyOrQueueHistoryCooldown(int32(e.SpellID), now.Add(time.Duration(durMS)*time.Millisecond), 0,
				&pendingCooldownEvent{spellID: e.SpellID, ms: uint32(durMS), category: uint16(e.Category)})
		}
	}
}

func (s *session) applyOrQueueHistoryCooldown(spellID int32, end time.Time, category uint32, ev *pendingCooldownEvent) {
	if s.selfSeen && s.world != nil {
		if _, ok := s.world.Object(s.self); ok {
			s.world.StartCooldown(s.self, spellID, end, category)
			s.emitPendingCooldown(ev)
			return
		}
	}
	st := s.spellState()
	st.pendingHistory = append(st.pendingHistory, pendingHistoryCooldown{
		spellID: spellID, end: end, category: category, emit: ev,
	})
}

func (s *session) emitPendingCooldown(ev *pendingCooldownEvent) {
	if ev != nil {
		s.emitCooldownStart(ev.spellID, ev.ms, ev.category, "login", ev.onHold)
	}
}

func (s *session) flushPendingHistory() {
	if !s.selfSeen || s.world == nil || s.spellSt == nil || len(s.spellSt.pendingHistory) == 0 {
		return
	}
	if _, ok := s.world.Object(s.self); !ok {
		return
	}
	pending := s.spellSt.pendingHistory
	s.spellSt.pendingHistory = nil
	for _, p := range pending {
		s.world.StartCooldown(s.self, p.spellID, p.end, p.category)
		s.emitPendingCooldown(p.emit)
	}
}

// expireCooldowns drains expired spell and category cooldowns from the world model and emits cooldown_end events.
func (s *session) expireCooldowns(now time.Time) {
	if !s.selfSeen || s.world == nil {
		return
	}
	for _, id := range s.world.ExpireCooldowns(s.self, now) {
		s.event(Event{
			Kind:       events.KindCooldownEnd,
			SourceGUID: eventGUID(s.self),
			SpellID:    uint32(id),
			Text:       s.spellName(id),
		})
	}
}

func (s *session) gcdState(now time.Time) (total, left time.Duration, known bool) {
	known = s.spellTable().Loaded()
	if s.spellSt == nil {
		return 0, 0, known
	}
	st := s.spellSt
	if !st.gcd.unknownAt.IsZero() {
		if now.Sub(st.gcd.unknownAt) < maxGCDMS*time.Millisecond {
			known = false
		} else {
			st.gcd.unknownAt = time.Time{}
		}
	}
	if st.gcd.total > 0 {
		rem := st.gcd.start.Add(st.gcd.total).Sub(now)
		if rem > 0 {
			return st.gcd.total, rem, known
		}
	}
	return 0, 0, known
}

func (s *session) comboPoints() (uint8, wire.GUID) {
	if !s.selfSeen || s.world == nil {
		return 0, wire.EmptyGUID
	}
	o, ok := s.world.Object(s.self)
	if !ok {
		return 0, wire.EmptyGUID
	}
	pts := o.Info.PowerOf(4)
	if pts <= 0 {
		return 0, wire.EmptyGUID
	}
	if pts > 255 {
		pts = 255
	}
	return uint8(pts), o.Info.ComboTarget
}

func (s *session) observeComboPoints(valuesUpdate bool) {
	if !s.selfSeen || s.world == nil {
		return
	}
	pts, target := s.comboPoints()
	st := s.spellState()
	if !st.comboKnown {
		st.comboKnown = true
		st.comboPoints = pts
		if !valuesUpdate {
			return
		}
	}
	if pts == st.comboPoints {
		return
	}
	was := st.comboPoints
	st.comboPoints = pts
	data := map[string]any{"points": pts, "was": was}
	tg := ""
	if pts > 0 && !target.IsZero() {
		tg = eventGUID(target)
		data["target"] = tg
	}
	s.event(Event{
		Kind:       events.KindComboPoints,
		SourceGUID: eventGUID(s.self),
		TargetGUID: tg,
		Data:       data,
	})
}

func (s *session) activeCooldowns(now time.Time) []ServeCooldown {
	s.expireCooldowns(now)
	out := []ServeCooldown{}
	if !s.selfSeen || s.world == nil {
		return out
	}
	cs, ok := s.world.Combat(s.self)
	if !ok {
		return out
	}
	type entry struct {
		row    ServeCooldown
		onHold bool
	}
	best := make(map[int32]entry, len(cs.Cooldowns))
	for _, cd := range cs.Cooldowns {
		if !cd.Active(now) {
			continue
		}
		var rem int64
		onHold := cd.EndsAt.IsZero()
		if !onHold {
			rem = cd.EndsAt.Sub(now).Milliseconds()
		}
		var cat uint16
		if sp, ok := s.spellTable().Lookup(uint32(cd.SpellID)); ok {
			cat = sp.Category
		}
		best[cd.SpellID] = entry{
			row: ServeCooldown{
				SpellID:     cd.SpellID,
				Name:        s.spellName(cd.SpellID),
				RemainingMS: rem,
				Category:    cat,
			},
			onHold: onHold,
		}
	}
	for _, id := range s.spellbook().ids() {
		sp, ok := s.spellTable().Lookup(uint32(id))
		if !ok || sp.Category == 0 {
			continue
		}
		catEnd, hasCat := cs.CategoryCooldowns[uint32(sp.Category)]
		if !hasCat || !now.Before(catEnd) {
			continue
		}
		rem := catEnd.Sub(now).Milliseconds()
		cur, exists := best[id]
		if !exists || (!cur.onHold && rem > cur.row.RemainingMS) {
			best[id] = entry{
				row: ServeCooldown{
					SpellID:     id,
					Name:        s.spellName(id),
					RemainingMS: rem,
					Category:    sp.Category,
				},
			}
		}
	}
	for _, e := range best {
		out = append(out, e.row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SpellID < out[j].SpellID })
	return out
}
