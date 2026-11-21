package combat

// SMSG_AI_REACTION (CombatPackets.h:118-133, CombatPackets.cpp:81-87): the packed GUID of the unit that reacted,
// then the reaction as a uint32 (AI_REACTION_ALERT 0, FRIENDLY 1, HOSTILE 2, AFRAID 3, DESTROY 4;
// SharedDefines.h:6636-6642). HOSTILE is sent when a creature engages a victim (Unit.cpp:6242, HOSTILE) and ALERT
// when a stealthed player is noticed (CreatureAI.cpp:156). Nothing else is decoded: the packet carries no target,
// so the "who was noticed" is the watch record's own context (the pull it belongs to), not a field.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// AIReactionKind is one AiReaction value. Only ALERT and HOSTILE reach a client in play; the rest are named so a
// bug report quotes the server's own word, not a number.
type AIReactionKind uint32

// The reactions the server sends (SharedDefines.h:6636-6642).
const (
	AIReactionAlert    AIReactionKind = 0
	AIReactionFriendly AIReactionKind = 1
	AIReactionHostile  AIReactionKind = 2
	AIReactionAfraid   AIReactionKind = 3
	AIReactionDestroy  AIReactionKind = 4
)

// String names the reaction the way the server does.
func (k AIReactionKind) String() string {
	switch k {
	case AIReactionAlert:
		return "alert"
	case AIReactionFriendly:
		return "friendly"
	case AIReactionHostile:
		return "hostile"
	case AIReactionAfraid:
		return "afraid"
	case AIReactionDestroy:
		return "destroy"
	default:
		return fmt.Sprintf("unknown(%d)", uint32(k))
	}
}

// AIReaction is one SMSG_AI_REACTION: the unit that reacted, and how.
type AIReaction struct {
	Unit wire.GUID
	Kind AIReactionKind
}

// errAIReactionShort is a reaction packet that ended before its layout.
var errAIReactionShort = errors.New("combat: SMSG_AI_REACTION ended before its GUID and reaction")

// ParseAIReaction reads one SMSG_AI_REACTION payload: the packed GUID, then the uint32 reaction
// (CombatPackets.cpp:81-87).
func ParseAIReaction(p []byte) (AIReaction, error) {
	b := wire.NewReader(p)
	v := AIReaction{Unit: guid(b)}
	if b.Err() != nil {
		return AIReaction{}, errAIReactionShort
	}
	v.Kind = AIReactionKind(b.ReadU32())
	if err := b.Err(); err != nil {
		return AIReaction{}, errAIReactionShort
	}
	if err := exactly(b); err != nil {
		return AIReaction{}, err
	}
	return v, nil
}
