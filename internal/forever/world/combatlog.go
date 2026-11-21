package world

// The session's combat log: one ring of rows for the fights the client's own packets describe. Decision 2 of the
// trials plan puts it here, in the world session and not in serve, so the serve `events` method, `forever
// dungeon-watch` and the trials runner all read the same log: the row shape is the trials runner's raw row
// (`seq kind time mono_ms source{guid,name} target{guid,name} spell_id amount`, plus `reason` on a spell_fail), so
// the Go normaliser and trials.py read one file.
//
// The ring is fed beside each model apply in the session's packet dispatch (one row per packet effect, never per
// packet), and it survives a reconnect: it belongs to the session, so a row's seq never goes back.

import (
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// CombatRingSize is how many rows the ring keeps. Ten rows a second for five minutes of a fight is 3,000; the
	// bound is what a reader that stopped for longer than that loses, and the page it then gets starts at the
	// oldest row the ring still holds.
	CombatRingSize = 4096
	// CombatPageMax is the most rows one read answers.
	CombatPageMax = 1000
)

// CombatActor is one side of a combat row: a guid as `0x` and 32 hex digits (the module's own GUID text with the
// prefix), and the name this client knows for the unit, empty when it knows none.
type CombatActor struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
}

// CombatRow is one row of the combat log. Kind is one of spell_start, spell_go, spell_fail, melee, spell_damage,
// periodic, heal, aura_applied, aura_removed, death, and the core-gains round's interrupt, dispel, absorb, miss,
// resist, immune, instakill and environmental. Time is the wall clock with milliseconds and its offset; MonoMS is
// the process's steady axis (events.MonoMS), the one a client cannot step.
type CombatRow struct {
	Seq     int         `json:"seq"`
	Kind    string      `json:"kind"`
	Time    string      `json:"time"`
	MonoMS  int64       `json:"mono_ms"`
	Source  CombatActor `json:"source"`
	Target  CombatActor `json:"target"`
	SpellID int32       `json:"spell_id"`
	Amount  int32       `json:"amount"`
	// Reason is set on a spell_fail (the core's SpellCastResult in words, combat.SpellFailureName), on a melee row
	// that did not land normally (the victim state, combat.VictimStateName), on a miss or resist, and on an
	// environmental row (the damage type, combat.EnvironmentalName).
	Reason string `json:"reason,omitempty"`
	// InterruptedSpellID is an interrupt row's victim spell: the cast the source stopped. SpellID on that row is
	// the interrupting spell (Kick), so a trial can match the response by spell and still name what was stopped.
	InterruptedSpellID int32 `json:"interrupted_spell_id,omitempty"`
	// AbsorbedSpellID is an absorb row's incoming spell (the shield's is SpellID): the damage the aura stopped.
	AbsorbedSpellID int32 `json:"absorbed_spell_id,omitempty"`
	// Dispelled is a dispel row's list of auras the spell removed.
	Dispelled []CombatDispel `json:"dispelled,omitempty"`
	// Sample is the advanced combat log's unit block (health and power) when the packet carried one. The unit it
	// describes is the packet's own: the attacker on a melee row, the target on a heal, periodic, absorb or
	// environmental one (the spell-damage packet's block is all zeros in this core and is left out). It is attached
	// to that event's row, so a fight's health and mana curves need no polling.
	Sample *CombatSample `json:"sample,omitempty"`
}

// CombatDispel is one aura a dispel row removed.
type CombatDispel struct {
	SpellID int32 `json:"spell_id"`
	Harmful bool  `json:"harmful,omitempty"`
}

// CombatSample is SpellCastLogData as a row carries it (CombatLogPacketsCommon.cpp:150-172): the unit's health, and
// its current power from the first SpellLogPowerData entry (the server puts the unit's own power bar first,
// CombatLogPacketsCommon.cpp:35-45).
type CombatSample struct {
	Health    int64 `json:"health"`
	Power     int32 `json:"power"`
	PowerType int8  `json:"power_type"`
}

// CombatLog is the session's ring of combat rows: appended by the session's dispatch (the session goroutine), read
// by anything that pages it, so every method takes the lock.
type CombatLog struct {
	mu sync.Mutex
	// rows is the ring: below CombatRingSize it grows, then it is rewritten in place and first is the oldest slot.
	rows  []CombatRow
	first int
	seq   int
	// changed is closed and replaced by every Append, so a reader that waits outside the ring's lock (serve's
	// `events`) wakes on the next row instead of polling.
	changed chan struct{}
}

// Append adds one row and assigns its seq: the ring's own counter, which only grows, so a reconnecting client never
// reads a seq back.
func (l *CombatLog) Append(row CombatRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	row.Seq = l.seq
	if len(l.rows) < CombatRingSize {
		l.rows = append(l.rows, row)
	} else {
		l.rows[l.first] = row
		l.first = (l.first + 1) % CombatRingSize
	}
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
}

// Since answers the rows above seq, oldest first, at most limit (CombatPageMax when limit is 0 or above it). next is
// the seq to pass next time: the last row's, or seq itself when nothing was above it. oldest is the lowest seq still
// held (0 for an empty ring). dropped is how many rows fell out of the ring between seq and the oldest row it still
// holds; a reader that sees it above zero knows the page has a hole.
func (l *CombatLog) Since(seq, limit int) (rows []CombatRow, next, oldest, dropped int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sinceLocked(seq, limit)
}

// SinceChanged is Since with the channel the next Append closes, read in the same lock: a row appended between the
// page and the wait is therefore either in the page or ends the wait, never missed. A waiter passes the channel back
// and waits on it outside the lock.
func (l *CombatLog) SinceChanged(seq, limit int) (rows []CombatRow, next, oldest, dropped int, changed <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, next, oldest, dropped = l.sinceLocked(seq, limit)
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
	return rows, next, oldest, dropped, l.changed
}

// sinceLocked is Since's body; the caller holds l.mu.
func (l *CombatLog) sinceLocked(seq, limit int) (rows []CombatRow, next, oldest, dropped int) {
	limit = min(limit, CombatPageMax)
	if limit <= 0 {
		limit = CombatPageMax
	}
	n := len(l.rows)
	if n == 0 {
		return nil, seq, 0, 0
	}
	oldest = l.seq - n + 1
	if seq < oldest-1 {
		dropped = oldest - 1 - seq
	}
	start := seq + 1
	if start < oldest {
		start = oldest
	}
	if start > l.seq {
		return nil, seq, oldest, dropped
	}
	count := l.seq - start + 1
	if count > limit {
		count = limit
	}
	rows = make([]CombatRow, count)
	base := start - oldest
	for i := range count {
		k := base + i
		if n < CombatRingSize {
			rows[i] = l.rows[k]
			continue
		}
		rows[i] = l.rows[(l.first+k)%CombatRingSize]
	}
	return rows, start + count - 1, oldest, dropped
}

// Len is how many rows the ring holds.
func (l *CombatLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.rows)
}

// newCombatRow starts one row the way the session's dispatch writes it: the wall clock and the steady axis of the
// moment, and the spell and amount the packet named. A zero guid is the empty value of that side; nothing here
// writes "none".
func newCombatRow(kind string, now time.Time, spellID, amount int32) CombatRow {
	return CombatRow{
		Kind:    kind,
		Time:    now.Format("2006-01-02T15:04:05.000-07:00"),
		MonoMS:  events.MonoMS(),
		SpellID: spellID,
		Amount:  amount,
	}
}

// CombatLog is the session's combat log, the ring its dispatch feeds. A serving daemon hands the ring of the
// session a drop replaced to the new one, so a row's seq only grows.
func (s *session) CombatLog() *CombatLog {
	if s.combat == nil {
		s.combat = &CombatLog{}
	}
	return s.combat
}

// combatRow appends one row with both sides named as this client knows them, and the module's own failure reason on
// a spell_fail.
func (s *session) combatRow(kind string, now time.Time, spellID, amount int32, src, dst wire.GUID, reason string) {
	s.combatRowLog(kind, now, spellID, amount, src, dst, reason, nil)
}

// combatRowLog is combatRow with the packet's advanced-log unit block, when it carried one: the row then also
// carries the source unit's health and power.
func (s *session) combatRowLog(kind string, now time.Time, spellID, amount int32, src, dst wire.GUID, reason string, log *combat.CastLogData) {
	s.combatRowExtras(kind, now, spellID, amount, src, dst, reason, combatRowExtra{Log: log})
}

// combatRowExtra is what one row carries beyond the base shape: the advanced-log sample and the kind-specific
// fields the core-gains round added.
type combatRowExtra struct {
	Log                *combat.CastLogData
	InterruptedSpellID int32
	AbsorbedSpellID    int32
	Dispelled          []CombatDispel
}

// combatRowExtras appends one row from the base shape plus its extras.
func (s *session) combatRowExtras(kind string, now time.Time, spellID, amount int32, src, dst wire.GUID, reason string, extra combatRowExtra) {
	row := newCombatRow(kind, now, spellID, amount)
	row.Source = s.combatActor(src)
	row.Target = s.combatActor(dst)
	row.Reason = reason
	row.InterruptedSpellID = extra.InterruptedSpellID
	row.AbsorbedSpellID = extra.AbsorbedSpellID
	row.Dispelled = extra.Dispelled
	row.Sample = combatSample(extra.Log)
	s.CombatLog().Append(row)
}

// combatSample turns SpellCastLogData into the row's sample, or nil when the packet carried none. A block whose
// health is zero is not a sample: SMSG_SPELL_NON_MELEE_DAMAGE_LOG always carries the full form to an advanced client
// but this core never initializes its LogData (Unit.cpp:5813-5848 has no Initialize), so its fields are zeros; a
// live unit cannot cast at zero health, and a death row's own evidence is the damage amount.
func combatSample(log *combat.CastLogData) *CombatSample {
	if log == nil || log.Health <= 0 {
		return nil
	}
	sample := &CombatSample{Health: log.Health}
	if len(log.Power) > 0 {
		sample.PowerType = log.Power[0].Type
		sample.Power = log.Power[0].Amount
	}
	return sample
}

// combatActor is one side of a row: the guid's text and the best name the model has (a player's name, else the
// creature's name by entry, else nothing).
func (s *session) combatActor(g wire.GUID) CombatActor {
	if g.IsZero() {
		return CombatActor{}
	}
	name := s.world.NameOf(g)
	if name == "" {
		if o, ok := s.world.Object(g); ok {
			name = s.world.Name(o)
		}
	}
	return CombatActor{GUID: g.String(), Name: name}
}

// combatCast writes a spell_start or a spell_go: a go is one row per target the cast hit (the packet's own list),
// and the cast's single target when the list is empty.
func (s *session) combatCast(kind string, now time.Time, c *combat.SpellCast) {
	caster := c.CasterUnit
	if caster.IsZero() {
		caster = c.Caster
	}
	if kind == "spell_start" {
		s.combatRow(kind, now, c.SpellID, 0, caster, c.Target.Unit, "")
		return
	}
	if len(c.HitTargets) == 0 {
		s.combatRow(kind, now, c.SpellID, 0, caster, c.Target.Unit, "")
		return
	}
	for _, t := range c.HitTargets {
		s.combatRow(kind, now, c.SpellID, 0, caster, t, "")
	}
}

// combatDeaths writes one death row per unit the values merge just took to dead. The merge that carries health zero
// does not name the blow that did it, so the row's source is empty and its target is the unit that died; the killing
// blow is the damage row right before it.
func (s *session) combatDeaths(now time.Time) {
	for _, g := range s.world.Deaths() {
		s.combatRow("death", now, 0, 0, wire.EmptyGUID, g, "")
	}
}
