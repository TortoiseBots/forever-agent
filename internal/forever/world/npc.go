package world

// The NPC dispatch: the gossip and interaction packets of the act round (forever-world-r0). The death flow keeps
// its own SMSG_GOSSIP_MESSAGE case for the spirit healer (handleDeath); this handler serves the act runner and the
// later vendor, trainer, taxi and quest rounds.

import (
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// handleNPC answers the act round's NPC packets: the gossip menu (stored for the runner's wait), its completion,
// the option's NPC interaction and the interaction-open result. A packet this round's parser cannot consume exactly
// is counted (Report.ParseErrors) and merges nothing.
func (s *session) handleNPC(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGGossipMessage:
		m, err := npc.ParseGossipMessage(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.actMenu = &m
		// The spirit healer's flow (handleDeath) waits on its own copy of the menu.
		if s.death != nil {
			s.death.gossip = m
			s.death.hasGossip = true
		}
	case wire.SMSGQuestGiverQuestList:
		l, err := npc.ParseQuestGiverQuestList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.actQuest = &l
	case wire.SMSGGossipComplete:
		if _, err := npc.ParseGossipComplete(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
	case wire.SMSGGossipOptionNPCInteract:
		if _, err := npc.ParseGossipOptionInteraction(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
	case wire.SMSGNPCInteractionOpenResult:
		if _, err := npc.ParseInteractionOpenResult(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
	}
	return nil
}
