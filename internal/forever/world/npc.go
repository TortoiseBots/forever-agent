package world

// The NPC dispatch: the gossip and interaction packets of the act round (forever-world-r0). The death flow keeps
// its own SMSG_GOSSIP_MESSAGE case for the spirit healer (handleDeath); this handler serves the act runner and the
// later vendor, trainer, taxi and quest rounds.

import (
	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/quest"
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
		// SendGossipMenu starts the interaction on this unit (GossipDef.cpp:220-222): a bank opened elsewhere is no
		// longer the server's interaction (bank.go).
		s.bankInteractionAt(m.Unit)
		s.eventGossip(events.KindGossip, m.Unit, map[string]any{"menu_id": m.MenuID, "options": len(m.Options)})
		// The spirit healer's flow (handleDeath) waits on its own copy of the menu.
		if s.death != nil {
			s.death.gossip = m
			s.death.hasGossip = true
		}
	case wire.SMSGQuestGiverQuestList:
		l, err := quest.ParseQuestList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.actQuest = &l
		// SendQuestGiverQuestListMessage starts the interaction on the giver (GossipDef.cpp:420).
		s.bankInteractionAt(l.Giver)
	case wire.SMSGGossipComplete:
		if _, err := npc.ParseGossipComplete(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.eventGossip(events.KindGossipComplete, wire.EmptyGUID, nil)
	case wire.SMSGGossipOptionNPCInteract:
		g, err := npc.ParseGossipOptionInteraction(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// OnGossipSelect starts the interaction on the source unit and answers this packet when the option has no
		// NPC interaction id (Player.cpp:15434-15460); the open result above is the other answer.
		s.bankInteractionAt(g.GossipGUID)
	case wire.SMSGNPCInteractionOpenResult:
		r, err := npc.ParseInteractionOpenResult(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// Round R3: the vendor flow waits on it (vendor_act.go); it is the only answer the first
		// CMSG_LIST_INVENTORY gets (F2).
		s.vendor.open = &r
		// Issue #220: a banker interaction is the bank's open (bank.go): bank_list sends CMSG_BANKER_ACTIVATE and the
		// bank commands are refused until its answer opened the window.
		s.recordBankOpen(r)
	}
	return nil
}
