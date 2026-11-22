package world

// The method frame (spec 4.1): the act state the item, NPC, quest and loot methods share across calls, the step
// runner, the NPC reach check, the wait bound and the event helpers. A new session (a reconnect) starts with an
// empty state. Preconditions (bad parameters, an unknown or distant unit, the wrong NPC flags) are RPC errors raised
// before any packet goes out; once a packet was sent a method answers a result, with ok false and the failure event
// when the server or the act refused. Only a stop (the caller's cancel, a dead link) stays an error.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The wait a method's wait_ms bounds: the default when it is omitted, and the most a caller may ask for.
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

// Last is the ring's current sequence.
func (l *EventLog) Last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// methodCtx bounds one method call: wait_ms 0 is the default, a negative value or one over the maximum is an error.
func methodCtx(ctx context.Context, waitMS, defMS, maxMS int) (context.Context, context.CancelFunc, error) {
	if waitMS < 0 || waitMS > maxMS {
		return nil, nil, fmt.Errorf("wait_ms %d is not 0 (default) to %d", waitMS, maxMS)
	}
	if waitMS == 0 {
		waitMS = defMS
	}
	c, cancel := context.WithTimeout(ctx, time.Duration(waitMS)*time.Millisecond)
	return c, cancel, nil
}

// methodNPC finds the unit a method addresses: it must be in the world model and within reach (methods never walk).
// A different unit than the shared state's current NPC starts the state over the way an npc step does (act.go
// actNPC): the vendor list, the menus and the quest list belong to the NPC they were read from.
func (s *session) methodNPC(guid GUID) (*objects.Object, error) {
	if wire.GUID(guid).IsZero() {
		return nil, errors.New("guid is required")
	}
	if s.world == nil {
		return nil, fmt.Errorf("no unit %s in the world model", guid)
	}
	unit, ok := s.world.Object(wire.GUID(guid))
	if !ok || !unit.Info.HasUnit {
		return nil, fmt.Errorf("no unit %s in the world model", guid)
	}
	nx, ny, nz := unit.Position(s.transport().Now())
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return nil, fmt.Errorf("the NPC is %.1f yd away (walk_to it first)", dist)
	}
	a := s.methodState()
	if !a.hasNPC || a.npc == nil || a.npc.GUID != unit.GUID {
		a.hasMenu, a.quests = false, nil
		a.vendor, a.hasVendor = nil, false
		a.trainer, a.hasTrainer = nil, false
		a.taxi, a.hasTaxi = nil, false
		s.actMenu, s.actQuest = nil, nil
	}
	a.npc, a.npcEntry, a.hasNPC = unit, unit.Info.Entry, true
	return unit, nil
}

// methodStopped reports whether a step's error is a stop the method must return as an error (the caller gave up,
// the link died, the method's own wait_ms ran out) instead of a refusal it reports in its result. Running out of
// wait_ms after a packet went out is not a refusal: the server may still complete the request, so reporting it as a
// failure would invite a retry that buys or sells twice.
func (s *session) methodStopped(parent context.Context, err error) bool {
	return parent.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || s.linkDead() != nil
}

// methodStep runs one act step on a shared state. refusal is the act's error text when the step failed (the
// method reports it as ok false); err is a stop.
func (s *session) methodStep(parent, ctx context.Context, a *actState, verb string, args map[string]string) (lines []string, refusal string, err error) {
	lines, stepErr := s.runMethodStep(ctx, a, verb, args)
	lines = append([]string(nil), lines...)
	if stepErr == nil {
		return lines, "", nil
	}
	if s.methodStopped(parent, stepErr) {
		if parent.Err() == nil && errors.Is(stepErr, context.DeadlineExceeded) {
			return lines, "", fmt.Errorf("wait_ms ran out before the server answered; the outcome is unknown, so read the inventory and the event log before retrying: %w", stepErr)
		}
		return lines, "", stepErr
	}
	return lines, stepErr.Error(), nil
}

// firstEventSince is the first event-log row above since whose kind is one of kinds, or nil.
func (s *session) firstEventSince(since uint64, kinds ...string) *Event {
	log := s.EventLog()
	for {
		rows, next, _, more := log.Since(since, EventPageMax)
		for i := range rows {
			for _, k := range kinds {
				if rows[i].Kind == k {
					e := rows[i]
					return &e
				}
			}
		}
		if !more {
			return nil
		}
		since = next
	}
}

// methodEvent appends the event a method writes where no packet handler emits one (a vendor or quest result) and
// returns the stored row.
func (s *session) methodEvent(kind string, source wire.GUID, data map[string]any, text string) *Event {
	e := s.event(Event{
		Kind:       kind,
		Source:     s.eventName("", source),
		SourceGUID: eventGUID(source),
		Text:       text,
		Data:       data,
	})
	return &e
}
