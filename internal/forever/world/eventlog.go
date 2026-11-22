package world

// The session's event log: one ring of typed events for the whole session, the stream serve's event_log answers. It
// is fed by the dispatch beside the models (a chat line, a system notice, a party packet, the combat rows, loot,
// gossip, an add-on message and the link's own state), so a consumer that wants one ordered view of what happened
// reads one seq axis instead of joining chat_log with the combat ring. The row shape is internal/events.Event's JSON
// (seq, time, mono_ms, kind, source, source_guid, target, target_guid, spell_id, amount, crit, party, text, data),
// with a GUID as the 0x plus 32 hex digits text this client prints everywhere.
//
// The ring outlives a reconnect like the combat ring does: a serving daemon hands the dropped session's log to the
// session that replaces it, so a reader's seq never goes back and the history before the drop stays readable.

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// EventRingSize is how many events the ring keeps. A session that chats, fights, loots and follows for an hour
	// stays well inside it; what falls out is the bound a reader that stopped for longer than that loses.
	EventRingSize = 20000
	// EventPageMax is the most events one read answers.
	EventPageMax = 1000
)

// Event is one row of the session event log. The keys are internal/events.Event's, so an old consumer's decoder
// reads this log too; Time is the wall clock and MonoMS the steady axis (events.MonoMS), the one a client cannot
// step. SourceGUID and TargetGUID are empty for a zero GUID and otherwise the 0x text.
type Event struct {
	Seq        uint64         `json:"seq"`
	Time       time.Time      `json:"time"`
	MonoMS     int64          `json:"mono_ms"`
	Kind       string         `json:"kind"`
	Source     string         `json:"source,omitempty"`
	SourceGUID string         `json:"source_guid,omitempty"`
	Target     string         `json:"target,omitempty"`
	TargetGUID string         `json:"target_guid,omitempty"`
	SpellID    uint32         `json:"spell_id,omitempty"`
	Amount     int64          `json:"amount,omitempty"`
	Crit       bool           `json:"crit,omitempty"`
	Party      bool           `json:"party,omitempty"`
	Text       string         `json:"text,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// EventLog is the session's ring of events: appended by the session goroutine, read by event_log, so every method
// takes the lock.
type EventLog struct {
	mu sync.Mutex
	// rows is the ring: below EventRingSize it grows, then it is rewritten in place and first is the oldest slot.
	rows  []Event
	first int
	seq   uint64
	// changed is closed and replaced by every Append, so a long-poll waits outside the lock and wakes on the next
	// event instead of polling.
	changed chan struct{}
	// epoch names this ring's life: a random id made with the ring, so a reader that kept its cursor across a daemon
	// restart (a new ring, its seq starting again at 1) can see that the log it is reading is a different one.
	epoch string
}

// EventRead is one read of the ring: the page, the signal the next Append closes, the ring's epoch, and reset, which
// says the reader's cursor was above the ring's seq (the ring is a new life, a restart) so the page starts at the
// beginning instead of silently staying empty.
type EventRead struct {
	Rows    []Event
	Next    uint64
	Dropped int
	More    bool
	Reset   bool
	Epoch   string
	Changed <-chan struct{}
}

// Append adds one event and assigns its seq: the ring's own counter, which only grows, so a reconnecting client
// never reads a seq back.
func (l *EventLog) Append(e Event) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if len(l.rows) < EventRingSize {
		l.rows = append(l.rows, e)
	} else {
		l.rows[l.first] = e
		l.first = (l.first + 1) % EventRingSize
	}
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	return e
}

// Since answers the events above seq, oldest first, at most limit (EventPageMax when limit is 0 or above it). next
// is the seq to pass next time: the last event's, or seq itself when nothing was above it. dropped is how many
// events fell out of the ring between seq and the oldest it still holds; more says another page follows.
func (l *EventLog) Since(seq uint64, limit int) (rows []Event, next uint64, dropped int, more bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, next, dropped, more, _ = l.sinceLocked(seq, limit)
	return rows, next, dropped, more
}

// Read is Since with the channel the next Append closes, the epoch and the reset flag, all read in the same lock: an
// event appended between the page and the wait is therefore either in the page or ends the wait, never missed.
func (l *EventLog) Read(seq uint64, limit int) EventRead {
	l.mu.Lock()
	defer l.mu.Unlock()
	var r EventRead
	r.Rows, r.Next, r.Dropped, r.More, r.Reset = l.sinceLocked(seq, limit)
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
	if l.epoch == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err == nil {
			l.epoch = hex.EncodeToString(b[:])
		} else {
			l.epoch = "0"
		}
	}
	r.Epoch, r.Changed = l.epoch, l.changed
	return r
}

// sinceLocked is Since's body; the caller holds l.mu. A seq above the ring's own is a cursor from another life of
// the log (the daemon restarted and the seq began again at 1): it is read as 0 and reset is true, so the reader
// is told rather than left with an empty page until the new seq passes its cursor.
func (l *EventLog) sinceLocked(seq uint64, limit int) (rows []Event, next uint64, dropped int, more bool, reset bool) {
	limit = min(limit, EventPageMax)
	if limit <= 0 {
		limit = EventPageMax
	}
	if seq > l.seq {
		seq, reset = 0, true
	}
	n := len(l.rows)
	if n == 0 {
		return nil, seq, 0, false, reset
	}
	oldest := l.seq - uint64(n) + 1
	if seq+1 < oldest {
		dropped = int(oldest - 1 - seq)
	}
	start := seq + 1
	if start < oldest {
		start = oldest
	}
	if start > l.seq {
		return nil, seq, dropped, false, reset
	}
	count := int(l.seq - start + 1)
	if count > limit {
		count, more = limit, true
	}
	rows = make([]Event, count)
	base := int(start - oldest)
	for i := range count {
		k := base + i
		if n == EventRingSize {
			k = (l.first + k) % EventRingSize
		}
		rows[i] = l.rows[k]
	}
	return rows, start + uint64(count) - 1, dropped, more, reset
}

// Len is how many events the ring holds.
func (l *EventLog) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.rows)
}

// EventLog is the session's event log. A serving daemon hands the ring of the session a drop replaced to the new
// one, so an event's seq only grows.
//
// Every session built by newSession owns its ring from construction, because event_log reads it from an RPC goroutine
// while the session goroutine appends; the nil fallback is for a session assembled by hand (a test's struct literal),
// which has no concurrent reader.
func (s *session) EventLog() *EventLog {
	if s.evlog == nil {
		s.evlog = &EventLog{}
	}
	return s.evlog
}

// event appends one event, stamped with the moment it is written.
func (s *session) event(e Event) Event {
	if e.Time.IsZero() {
		e.Time = s.Now()
	}
	if e.MonoMS == 0 {
		e.MonoMS = events.MonoMS()
	}
	return s.EventLog().Append(e)
}

// eventGUID is the 0x text of a guid for an event field, or "" for the zero GUID.
func eventGUID(g wire.GUID) string {
	if g.IsZero() {
		return ""
	}
	return GUID(g).String()
}

// eventName is the name an event prints for a chat participant: the packet's own name when it has one, this
// character's own name for itself, the model's cached player name, the roster's name, else nothing (an event never
// prints a guid's kind as a name).
func (s *session) eventName(packed string, g wire.GUID) string {
	if packed != "" {
		return chat.SanitiseName(packed)
	}
	if g.IsZero() {
		return ""
	}
	if s.selfSeen && g == s.self {
		return s.report.Character.Name
	}
	if name := s.world.NameOf(g); name != "" {
		return chat.SanitiseName(name)
	}
	if s.party != nil && s.party.RosterValid {
		if m, ok := s.party.Members[g]; ok && m.Name != "" {
			return chat.SanitiseName(m.Name)
		}
	}
	return ""
}

// eventParty says whether a guid is this character or a party member, the flag the old daemon's rows carry.
func (s *session) eventParty(g wire.GUID) bool {
	if g.IsZero() {
		return false
	}
	if s.selfSeen && g == s.self {
		return true
	}
	if s.party == nil || !s.party.RosterValid {
		return false
	}
	_, ok := s.party.Members[g]
	return ok
}

// eventChat appends one chat or notice line: kind chat, whisper or system by its type, with the sender and its text.
func (s *session) eventChat(line chat.Line) {
	kind := events.KindChat
	switch line.Type {
	case chat.TypeSystem:
		kind = events.KindSystem
	case chat.TypeWhisper, chat.TypeWhisperInform:
		kind = events.KindWhisper
	}
	data := map[string]any{"type": chat.TypeName(line.Type)}
	if line.Channel != "" {
		data["channel"] = line.Channel
	}
	if line.Prefix != "" {
		data["prefix"] = line.Prefix
	}
	s.event(Event{
		Kind:       kind,
		Source:     s.eventName(line.SenderName, line.SenderGUID),
		SourceGUID: eventGUID(line.SenderGUID),
		Target:     s.eventName(line.TargetName, line.TargetGUID),
		TargetGUID: eventGUID(line.TargetGUID),
		Text:       line.Text,
		Party:      s.eventParty(line.SenderGUID) || s.eventParty(line.TargetGUID),
		Data:       data,
	})
}

// eventCombat copies one combat row into the log: the same kind and sides, with the row's spell and amount, so the
// two rings never disagree about a blow.
func (s *session) eventCombat(row CombatRow, now time.Time, src, dst wire.GUID) {
	e := Event{
		Kind:       row.Kind,
		Time:       now,
		MonoMS:     row.MonoMS,
		Source:     row.Source.Name,
		SourceGUID: row.Source.GUID,
		Target:     row.Target.Name,
		TargetGUID: row.Target.GUID,
		SpellID:    uint32(max(row.SpellID, 0)),
		Amount:     int64(row.Amount),
		Party:      s.eventParty(src) || s.eventParty(dst),
	}
	if row.Reason != "" {
		e.Data = map[string]any{"reason": row.Reason}
	}
	if row.Kind == "knockback" || row.Kind == "knockback_land" {
		if e.Data == nil {
			e.Data = map[string]any{}
		}
		if row.Pos != nil {
			e.Data["pos"] = map[string]any{"x": row.Pos.X, "y": row.Pos.Y, "z": row.Pos.Z}
		}
		e.Data["self"] = row.Self
		e.Data["speed_xy"] = row.HorzSpeed
		e.Data["speed_z"] = row.VertSpeed
		if row.Simulated {
			e.Data["simulated"] = true
		}
		if row.Kind == "knockback_land" {
			e.Data["air_ms"] = row.AirMS
			e.Data["drop"] = row.Drop
		}
	}
	s.event(e)
}

// eventSession appends a session state change: in_world, map_change, disconnected or reconnected.
func (s *session) eventSession(state string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["state"] = state
	s.event(Event{Kind: events.KindSession, Data: data})
}

// eventPartyPacket appends one party packet: its state and the names or counts the packet carried.
func (s *session) eventPartyPacket(state string, data map[string]any, src wire.GUID, text string) {
	if data == nil {
		data = map[string]any{}
	}
	data["state"] = state
	s.event(Event{
		Kind:       events.KindParty,
		Source:     s.eventName("", src),
		SourceGUID: eventGUID(src),
		Text:       text,
		Party:      true,
		Data:       data,
	})
}

// eventRaidMarkSingle appends one raid target update: the icon, the unit that now carries it and who set it. The
// row kind is the shared events.KindRaidMark, so a reader that already reads the turtle-agent session's raid_mark
// rows reads this log's too.
func (s *session) eventRaidMarkSingle(u party.MarkUpdate) {
	s.event(Event{
		Kind:       events.KindRaidMark,
		Source:     s.eventName("", u.ChangedBy),
		SourceGUID: eventGUID(u.ChangedBy),
		Target:     s.eventName("", u.Target),
		TargetGUID: eventGUID(u.Target),
		Text:       party.MarkName(u.Mark),
		Data:       map[string]any{"state": "single", "symbol": int(u.Mark)},
	})
}

// eventRaidMarkList appends the whole icon list a list request answered: every slot the server sent, its target
// empty for an unset one.
func (s *session) eventRaidMarkList(l party.MarkList) {
	marks := make([]map[string]any, 0, len(l.Marks))
	for _, e := range l.Marks {
		marks = append(marks, map[string]any{"symbol": int(e.Mark), "target": eventGUID(e.Target)})
	}
	s.event(Event{Kind: events.KindRaidMark, Data: map[string]any{"state": "list", "marks": marks}})
}

// eventAddon appends one delivered add-on message.
func (s *session) eventAddon(m addonMessageEvent) {
	s.event(Event{
		Kind:       events.KindAddon,
		Source:     s.eventName(m.senderName, m.sender),
		SourceGUID: eventGUID(m.sender),
		Text:       m.text,
		Data:       map[string]any{"prefix": m.prefix, "channel": m.channel},
	})
}

// eventLoot appends one loot packet: the window opening, or a roll starting, voting, winning or all passing.
func (s *session) eventLoot(kind string, lootGUID wire.GUID, data map[string]any) {
	s.event(Event{
		Kind:       kind,
		Source:     s.eventName("", lootGUID),
		SourceGUID: eventGUID(lootGUID),
		Party:      s.eventParty(lootGUID),
		Data:       data,
	})
}

// eventGossip appends one gossip packet: the menu with its id and option count, or its completion.
func (s *session) eventGossip(kind string, unit wire.GUID, data map[string]any) {
	s.event(Event{
		Kind:       kind,
		Source:     s.eventName("", unit),
		SourceGUID: eventGUID(unit),
		Data:       data,
	})
}

// addonMessageEvent is what an add-on delivery reports to the event log: the sender as the packet named it, the
// prefix and the text.
type addonMessageEvent struct {
	senderName string
	sender     wire.GUID
	prefix     string
	channel    string
	text       string
}
