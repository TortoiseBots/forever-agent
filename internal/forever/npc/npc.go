package npc

// The NPC wire layer of the Forever (WoW Classic 1.60.1) client: the packets a player sends to talk to a creature
// (talk, select a gossip option, hello a quest giver, close) and the packets that carry the menu, its completion,
// the option's NPC interaction and the interaction-open result.
//
// Every layout is the server's own writer or reader; the file:line is cited at each field group. Nothing here
// touches the world model: the parsers return values and the callers decide what to merge. The gossip menu parse
// moved here from the death package in the act round (forever-world-r0), so one parser serves the spirit healer,
// vendors, trainers, taxi and quests; the death tests still pin every moved byte.

import (
	"errors"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// UNIT_NPC_FLAG bits (UnitDefines.h:320-321): the two flags the act runner reads to choose how to open an NPC.
const (
	// NPCFlagGossip is UNIT_NPC_FLAG_GOSSIP: the creature opens a gossip menu.
	NPCFlagGossip uint32 = 0x00000001
	// NPCFlagQuestGiver is UNIT_NPC_FLAG_QUESTGIVER: the creature answers a quest hello.
	NPCFlagQuestGiver uint32 = 0x00000002
	// NPCFlagSpiritHealer is UNIT_NPC_FLAG_SPIRIT_HEALER (UnitDefines.h:334), the flag the creature a ghost may
	// use has.
	NPCFlagSpiritHealer uint32 = 0x4000
	// NPCFlagVendor is UNIT_NPC_FLAG_VENDOR (UnitDefines.h:327): the creature's list is what CMSG_LIST_INVENTORY
	// asks for, and its rows are UNIT_NPC_FLAG_VENDOR_AMMO/FOOD/POISON/REAGENT shops too (0x100-0x800).
	NPCFlagVendor uint32 = 0x0080
	// NPCFlagVendorMasks is the four shop flags next to the generic one (UnitDefines.h:328-331): a vendor may have
	// only one of them, and it is still a vendor.
	NPCFlagVendorMasks uint32 = 0x0F00
	// NPCFlagRepair is UNIT_NPC_FLAG_REPAIR (UnitDefines.h:332): the creature repairs.
	NPCFlagRepair uint32 = 0x1000
	// NPCFlagTrainer is UNIT_NPC_FLAG_TRAINER (UnitDefines.h:324): the flag the core's list and buy handlers check.
	// The class (0x20) and profession (0x40) bits only say what the trainer teaches (UnitDefines.h:325-326).
	NPCFlagTrainer uint32 = 0x00000010
	// NPCFlagFlightmaster is UNIT_NPC_FLAG_FLIGHTMASTER (UnitDefines.h:333): the flag every taxi handler checks.
	NPCFlagFlightmaster uint32 = 0x00002000
	// NPCFlagBattlemaster is UNIT_NPC_FLAG_BATTLEMASTER (UnitDefines.h:340): the creature answers the battlemaster
	// hello with the battleground list.
	NPCFlagBattlemaster uint32 = 0x00100000
	// NPCFlagMailbox is UNIT_NPC_FLAG_MAILBOX (UnitDefines.h:346): the creature answers the mailbox packets.
	NPCFlagMailbox uint32 = 0x04000000
)

// IsVendor reports whether a creature's flags make it a vendor (any of the five vendor bits).
func IsVendor(flags uint32) bool { return flags&(NPCFlagVendor|NPCFlagVendorMasks) != 0 }

// IsRepairer reports whether a creature's flags make it a repairer.
func IsRepairer(flags uint32) bool { return flags&NPCFlagRepair != 0 }

// IsTrainer reports whether a creature's flags carry the trainer bit the core's list and buy handlers check
// (NPCHandler.cpp:99, :134). A trainer that only carries a class or profession bit answers neither packet.
func IsTrainer(flags uint32) bool { return flags&NPCFlagTrainer != 0 }

// IsFlightmaster reports whether a creature's flags carry the flight-master bit every taxi handler checks
// (TaxiHandler.cpp:35-76, :159-166).
func IsFlightmaster(flags uint32) bool { return flags&NPCFlagFlightmaster != 0 }

// IsBattlemaster reports whether a creature's flags make it a battlemaster (BattleGroundHandler.cpp
// HandleBattlemasterHelloOpcode checks the flag before answering the list).
func IsBattlemaster(flags uint32) bool { return flags&NPCFlagBattlemaster != 0 }
// IsMailbox reports whether a creature's flags make it a mailbox (MailHandler.cpp CanOpenMailBox).
func IsMailbox(flags uint32) bool { return flags&NPCFlagMailbox != 0 }

// InteractionMerchant is PlayerInteractionType::Merchant (DBCEnums.h:2236): the interaction SMSG_NPC_INTERACTION_OPEN_RESULT
// names when a vendor opens.
const InteractionMerchant int32 = 5

// GossipOptionNpc names the server's option kind (GossipDef.h:37, the GossipOptionNpc enum): the icon the menu row
// carries, which the act runner uses to pick an option by role.
const (
	GossipOptionNone         uint32 = 0
	GossipOptionVendor       uint32 = 1
	GossipOptionTaxinode     uint32 = 2
	GossipOptionTrainer      uint32 = 3
	GossipOptionSpiritHealer uint32 = 4
	GossipOptionBinder       uint32 = 5
	GossipOptionBanker       uint32 = 6
	GossipOptionPetition     uint32 = 7
	GossipOptionGuildTabard  uint32 = 8
	GossipOptionBattlemaster uint32 = 9
	GossipOptionAuctioneer   uint32 = 10
	GossipOptionTalentMaster uint32 = 11
	GossipOptionStablemaster uint32 = 12
	GossipOptionGuildBanker  uint32 = 14
	GossipOptionSpellclick   uint32 = 15
	GossipOptionMailbox      uint32 = 18
)

// Bounds for every count and length a packet declares. A count is server data: the parser refuses the packet when
// it exceeds the bound instead of allocating from it.
const (
	// maxGossipOptions bounds a gossip menu's option list. The server's menus hold a handful of rows; the count is a
	// uint32 from the packet.
	maxGossipOptions = 64
	// maxGossipTexts bounds a gossip menu's quest-text list (a quest giver's menu is one page of quests).
	maxGossipTexts = 64
	// maxTreasureItems bounds a gossip option's reward list (a treasure option lists a few items).
	maxTreasureItems = 32
	// maxGossipTextLen bounds the 12-bit Text and Confirm lengths of a gossip option (4095 on the wire).
	maxGossipTextLen = 1024
	// maxGossipFailureLen bounds the 8-bit FailureDescription length (255 on the wire is the field's own width).
	maxGossipFailureLen = 255
	// maxGossipTitleLen bounds the 9-bit quest title length (511 on the wire).
	maxGossipTitleLen = 255
	// maxPromotionCodeLen bounds CMSG_GOSSIP_SELECT_OPTION's 8-bit promotion code.
	maxPromotionCodeLen = 255
)

// errShort is a packet that ended before its layout did; wire.ErrShort is the usual cause.
var errShort = errors.New("npc: the packet ended before its layout")

// GossipOption is one option of SMSG_GOSSIP_MESSAGE (ClientGossipOptions, NPCPackets.cpp:42-72). Only the fields a
// gossip flow needs are kept; the option's own reward list is walked by its items' fixed bytes and its type
// bits (written into the option's bit block, see ParseGossipMessage).
type GossipOption struct {
	ID       int32
	NPC      uint32
	Flags    int8
	Cost     uint64
	Language uint32
	// Text is the option's label ("Return me to life."); Confirm is its pop-up box text.
	Text    string
	Confirm string
	// HasSpell/SpellID is the optional spell the option carries.
	HasSpell bool
	SpellID  int32
}

// GossipIdent is one identifier of a gossip menu that is not a GUID: the menu's own id and each option's id. They
// are the server's data (a menu id and an option id are built into the game), so a committed fixture must carry the
// generator's synthetic value: the audit parse reports both with their offsets, the generator rewrites them there,
// and the fixture test refuses a fixture whose values are not synthetic.
type GossipIdent struct {
	Name   string
	Offset int
	Value  int32
}

// GossipMessage is SMSG_GOSSIP_MESSAGE (GossipMessage::Write, NPCPackets.cpp:109-135): the menu a creature opened,
// its options and its quest text list.
type GossipMessage struct {
	Unit              wire.GUID
	MenuID            int32
	LfgDungeonsID     int32
	FriendshipFaction int32
	Options           []GossipOption
	Texts             int
	HasRandomText     bool
	RandomTextID      int32
	HasBroadcastText  bool
	BroadcastTextID   int32
}

// SpiritHealer returns the menu's SpiritHealer option, if it has one.
func (m GossipMessage) SpiritHealer() (GossipOption, bool) {
	for _, o := range m.Options {
		if o.NPC == GossipOptionSpiritHealer {
			return o, true
		}
	}
	return GossipOption{}, false
}

// OptionNPC returns the menu's first option of the given GossipOptionNpc kind, if it has one.
func (m GossipMessage) OptionNPC(kind uint32) (GossipOption, bool) {
	for _, o := range m.Options {
		if o.NPC == kind {
			return o, true
		}
	}
	return GossipOption{}, false
}
