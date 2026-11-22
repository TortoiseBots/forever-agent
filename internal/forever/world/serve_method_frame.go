package world

// The D2 method frame (spec 4.1): the act state the item, NPC, quest and loot methods share across calls, the
// step runner, and EventLog.Last. The NPC reach check, the wait bound and the event helpers return with the round
// that first calls them. A new session (a reconnect) starts with an empty state.

import (
	"context"
	"sort"
	"strings"
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
