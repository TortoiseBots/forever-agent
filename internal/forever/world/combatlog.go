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
// periodic, heal, aura_applied, aura_removed and death. Time is the wall clock with milliseconds and its offset;
// MonoMS is the process's steady axis (events.MonoMS), the one a client cannot step.
type CombatRow struct {
	Seq     int         `json:"seq"`
	Kind    string      `json:"kind"`
	Time    string      `json:"time"`
	MonoMS  int64       `json:"mono_ms"`
	Source  CombatActor `json:"source"`
	Target  CombatActor `json:"target"`
	SpellID int32       `json:"spell_id"`
	Amount  int32       `json:"amount"`
	// Reason is set on a spell_fail only: the core's SpellCastResult in words (combat.SpellFailureName).
	Reason string `json:"reason,omitempty"`
	// SpellName is the server's own name for SpellID (CORE-GAINS-3): the
	// session fills it when it pages the log, from its SpellName record
	// cache. It is empty when the server holds no name or the fetch failed;
	// the row's spell_id is the fallback, as today.
	SpellName string `json:"spell_name,omitempty"`
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
	row := newCombatRow(kind, now, spellID, amount)
	row.Source = s.combatActor(src)
	row.Target = s.combatActor(dst)
	row.Reason = reason
	s.CombatLog().Append(row)
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
