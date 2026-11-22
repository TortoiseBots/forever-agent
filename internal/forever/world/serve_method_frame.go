package world

// The D2 method frame (spec 4.1): the act state the item, NPC, quest and loot methods share across calls, the
// NPC reach check, the step runner, the wait bound, and the event-log helpers every later round uses (Last,
// firstEventSince, methodEvent). A new session (a reconnect) starts with an empty state.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The wait bounds of one method call: wait_ms 0 takes the default, a negative or over-max value is refused.
const (
	methodWaitDefaultMS = 15000
	methodWaitMaxMS     = 60000
)

// ServeInvItem is one row of the inventory/equipment methods: TA objects.Item's JSON names plus the container
// byte and the placement flags. Bag is 255 for the character's own slots and 30-33 for a bag.
type ServeInvItem struct {
	Bag           int    `json:"bag"`
	Slot          int    `json:"slot"`
	GUID          GUID   `json:"guid"`
	ItemEntry     int32  `json:"item_entry"`
	Count         uint32 `json:"count"`
	Durability    uint32 `json:"durability,omitempty"`
	MaxDurability uint32 `json:"max_durability,omitempty"`
	Placed        bool   `json:"placed"`
	Unconfirmed   bool   `json:"unconfirmed,omitempty"`
	Name          string `json:"name,omitempty"`
	Quality       *int8  `json:"quality,omitempty"`
}

// methodState is the shared act state, created on first use.
func (s *session) methodState() *actState {
	if s.methodAct == nil {
		s.methodAct = &actState{}
	}
	return s.methodAct
}

// methodNPC resolves a unit for a method and enforces the interaction reach. Methods never walk: an out-of-reach
// NPC is an error that names the walk. A different GUID than the state's current NPC resets the state the way an
// act `npc` step does, so a vendor list or a menu read for the old NPC cannot answer for the new one.
func (s *session) methodNPC(guid GUID) (*objects.Object, error) {
	if s.world == nil {
		return nil, fmt.Errorf("no unit %s in the world model", guid)
	}
	obj, ok := s.world.Object(wire.GUID(guid))
	if !ok || !obj.Info.HasUnit {
		return nil, fmt.Errorf("no unit %s in the world model", guid)
	}
	now := s.transport().Now()
	x, y, z := obj.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), x, y, z); dist > actInteractReach {
		return nil, fmt.Errorf("the NPC is %.1f yd away (walk_to it first)", dist)
	}
	a := s.methodState()
	if a.npc == nil || a.npc.GUID != obj.GUID {
		a.npc, a.npcEntry, a.hasNPC = obj, obj.Info.Entry, true
		a.hasMenu = false
		a.menuNPC = false
		a.quests = nil
		a.vendor, a.hasVendor = nil, false
		a.trainer, a.hasTrainer = nil, false
		a.taxi, a.hasTaxi = nil, false
		s.actMenu, s.actQuest = nil, nil
	}
	return obj, nil
}

// runMethodStep runs one act step on a shared state and returns the lines it printed. The step's Raw form is the
// canonical verb plus sorted k=v pairs, so a refusal prints the same line a script would.
func (s *session) runMethodStep(ctx context.Context, a *actState, verb string, args map[string]string) ([]string, error) {
	if a == nil {
		a = s.methodState()
	}
	a.lines, a.dropped = nil, 0
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var raw strings.Builder
	raw.WriteString(verb)
	for _, k := range keys {
		raw.WriteByte(' ')
		raw.WriteString(k)
		raw.WriteByte('=')
		raw.WriteString(args[k])
	}
	if err := s.actStep(ctx, a, ActStep{Verb: verb, Args: args, Raw: raw.String()}); err != nil {
		return a.lines, err
	}
	return a.lines, nil
}

// methodCtx bounds one method wait: 0 takes defMS, a negative or over-max value is refused.
func methodCtx(ctx context.Context, waitMS, defMS, maxMS int) (context.Context, context.CancelFunc, error) {
	if waitMS < 0 {
		return nil, nil, fmt.Errorf("wait_ms must not be negative")
	}
	wait := waitMS
	if wait == 0 {
		wait = defMS
	}
	if wait > maxMS {
		return nil, nil, fmt.Errorf("wait_ms %d is over the %d ms maximum", waitMS, maxMS)
	}
	c, cancel := context.WithTimeout(ctx, time.Duration(wait)*time.Millisecond)
	return c, cancel, nil
}

// Last is the ring's current sequence.
func (l *EventLog) Last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// firstEventSince is the first row above since whose kind is one of kinds.
func (s *session) firstEventSince(since uint64, kinds ...string) *Event {
	rows, _, _, _ := s.EventLog().Since(since, EventPageMax)
	for i := range rows {
		for _, kind := range kinds {
			if rows[i].Kind == kind {
				e := rows[i]
				return &e
			}
		}
	}
	return nil
}

// methodEvent appends an event the method itself emits and returns the stored row.
func (s *session) methodEvent(kind string, source wire.GUID, data map[string]any, text string) *Event {
	e := s.event(Event{Kind: kind, SourceGUID: eventGUID(source), Data: data, Text: text})
	return &e
}
