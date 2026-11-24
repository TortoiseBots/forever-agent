package wire

import "fmt"

// UnknownOpcode is the core's UNKNOWN_OPCODE marker (Opcodes.h:47).
const UnknownOpcode uint32 = 0xBADD

// Client and server core opcodes this client uses. The numbers are the core's OpcodeClient/OpcodeServer values
// (Opcodes.h), i.e. what the packet log records; the wire numbers are computed by OpcodeTable.
const (
	CMSGAuthSession             uint32 = 0x440001
	CMSGAuthContinuedSession    uint32 = 0x440003
	CMSGEnterEncryptedModeAck   uint32 = 0x440005
	CMSGPing                    uint32 = 0x440006
	CMSGLogDisconnect           uint32 = 0x440007
	CMSGQueuedMessagesEnd       uint32 = 0x44000A
	CMSGEnumCharacters          uint32 = 0x430014
	CMSGPlayerLogin             uint32 = 0x430016
	CMSGLoadingScreenNotify     uint32 = 0x430024
	CMSGMoveInitActiveMoverDone uint32 = 0x410064
	CMSGTimeSyncResponse        uint32 = 0x41005C
	CMSGLogoutRequest           uint32 = 0x3E0072

	// The movement the client sends (Opcodes.h:619-700) and the name lookups of round 3.
	CMSGMoveStartForward       uint32 = 0x410000
	CMSGMoveStartBackward      uint32 = 0x410001
	CMSGMoveStop               uint32 = 0x410002
	CMSGMoveStartStrafeLeft    uint32 = 0x410003
	CMSGMoveStopStrafe         uint32 = 0x410005
	CMSGMoveJump               uint32 = 0x410006
	CMSGMoveStartTurnLeft      uint32 = 0x410008
	CMSGMoveStopTurn           uint32 = 0x41000A
	CMSGMoveSetRunMode         uint32 = 0x41000E
	CMSGMoveSetWalkMode        uint32 = 0x41000F
	CMSGMoveFallLand           uint32 = 0x410017
	CMSGMoveStartSwim          uint32 = 0x410018
	CMSGMoveStopSwim           uint32 = 0x410019
	CMSGMoveSetFacing          uint32 = 0x410027
	CMSGMoveHeartbeat          uint32 = 0x41002E
	CMSGMoveSetFacingHeartbeat uint32 = 0x41007F
	CMSGMoveTeleportAck        uint32 = 0x410016
	// The acknowledgements a real client sends for a movement change the server pushed (MovementHandler.cpp:662-733
	// and :777-784): the speed changes the release gives the ghost, and its water walk.
	CMSGMoveForceRunSpeedChangeAck  uint32 = 0x410029
	CMSGMoveForceSwimSpeedChangeAck uint32 = 0x41002B
	CMSGMoveWaterWalkAck            uint32 = 0x41003B
	// CMSG_MOVE_KNOCK_BACK_ACK 0x410030 (Opcodes.h:644): the ack the mover sends for SMSG_MOVE_KNOCK_BACK
	// (HandleMoveKnockBackAck, MovementHandler.cpp:760-776).
	CMSGMoveKnockBackAck uint32 = 0x410030
	// The death flow's client packets (Opcodes.h:252, 413, 777-778, 837, 846, 891, 981).
	CMSGRepopRequest      uint32 = 0x3E00C8
	CMSGReclaimCorpse     uint32 = 0x3E0076
	CMSGResurrectResponse uint32 = 0x4300AF
	// CMSG_SUMMON_RESPONSE 0x430096 (Opcodes.h:994): the answer to a summon offer, the summoner's packed GUID and
	// the Accept bit (MovementPackets.cpp:1079-1083).
	CMSGSummonResponse       uint32 = 0x430096
	CMSGQueryCorpseLocation  uint32 = 0x43008C
	CMSGQueryCorpseTransport uint32 = 0x43008D
	CMSGTalkToGossip         uint32 = 0x3E0027
	CMSGGossipSelectOption   uint32 = 0x3E0029
	CMSGQuestGiverHello      uint32 = 0x3E002B
	CMSGSpiritHealerActivate uint32 = 0x3E0045
	CMSGCloseInteraction     uint32 = 0x3E0028
	CMSGQueryPlayerNames     uint32 = 0x440010
	CMSGQueryCreature        uint32 = 0x3D0141
	// CMSG_QUERY_GAME_OBJECT 0x3D0142 (Opcodes.h:781): the uint32 entry then the packed GUID
	// (QueryGameObject::Read, QueryPackets.cpp:344-348).
	CMSGQueryGameObject uint32 = 0x3D0142
	CMSGDBQueryBulk     uint32 = 0x430010

	// The combat the client sends (Opcodes.h:66-67, 168-179, 954).
	CMSGSetSelection uint32 = 0x3E00CA
	CMSGAttackSwing  uint32 = 0x3D011C
	CMSGAttackStop   uint32 = 0x3D011D
	CMSGCastSpell    uint32 = 0x3D0174
	CMSGCancelCast   uint32 = 0x3D0177
	// CMSGCancelMountAura is the empty packet that cancels the character's mount aura (Opcodes.h:173,
	// SpellHandler.cpp:338 RemoveAurasByType(SPELL_AURA_MOUNTED)).
	CMSGCancelMountAura uint32 = 0x3D0153

	SMSGAuthChallenge        uint32 = 0x4C0000
	SMSGEnterEncryptedMode   uint32 = 0x4C0004
	SMSGCompressedPacket     uint32 = 0x4C000E
	SMSGAuthResponse         uint32 = 0x450001
	SMSGEnumCharactersResult uint32 = 0x450018
	SMSGConnectTo            uint32 = 0x4C0008
	SMSGResumeComms          uint32 = 0x4C0006
	SMSGLoginVerifyWorld     uint32 = 0x45002F
	SMSGTimeSyncRequest      uint32 = 0x5E0000
	SMSGMoveSetActiveMover   uint32 = 0x5E0003
	SMSGPong                 uint32 = 0x4C0009
	SMSGLogoutResponse       uint32 = 0x45012F
	SMSGCharacterLoginFailed uint32 = 0x4501B0
	SMSGLogoutComplete       uint32 = 0x450130

	// Chat and party (round 5): the client packets (Opcodes.h:194-236, 541, 738-740, 869, 914) and the
	// server packets it answers (Opcodes.h:1340-1353, 1436, 1463, 1610-1614, 2068-2075, 2161, 2414).
	CMSGChatMessageSay                 uint32 = 0x2C0023
	CMSGChatMessageYell                uint32 = 0x2C0025
	CMSGChatMessageParty               uint32 = 0x2C0026
	CMSGChatMessageWhisper             uint32 = 0x2C0008
	CMSGChatMessageEmote               uint32 = 0x2C0024
	CMSGChatMessageGuild               uint32 = 0x2C000D
	CMSGChatMessageOfficer             uint32 = 0x2C000E
	CMSGChatMessageRaid                uint32 = 0x2C0027
	CMSGChatMessageRaidWarning         uint32 = 0x2C0029
	CMSGChatMessageInstanceChat        uint32 = 0x2C0028
	CMSGChatMessageChannel             uint32 = 0x2C0007
	CMSGChatJoinChannel                uint32 = 0x2C0000
	CMSGChatLeaveChannel               uint32 = 0x2C0001
	CMSGChatChannelList                uint32 = 0x2C0011
	CMSGChatAddonMessage               uint32 = 0x2C002A
	CMSGChatAddonMessageTargeted       uint32 = 0x2C002B
	CMSGChatRegisterAddonPrefixes      uint32 = 0x2C0005
	CMSGChatUnregisterAllAddonPrefixes uint32 = 0x2C0006
	CMSGPartyInvite                    uint32 = 0x430030
	CMSGPartyInviteResponse            uint32 = 0x430032
	CMSGPartyUninvite                  uint32 = 0x430073
	CMSGSetLootMethod                  uint32 = 0x430074
	CMSGLeaveGroup                     uint32 = 0x430075
	CMSGSetPartyLeader                 uint32 = 0x430076
	CMSGChangeSubGroup                 uint32 = 0x430078
	CMSGSwapSubGroups                  uint32 = 0x430079
	CMSGConvertRaid                    uint32 = 0x43007A
	CMSGSetAssistantLeader             uint32 = 0x43007B
	CMSGUpdateRaidTarget               uint32 = 0x43007C
	CMSGRequestPartyMemberStats        uint32 = 0x43007F
	// The guild invite flow this client sends (Opcodes.h:40, 429, 439). The accept carries the invite's packed
	// guild GUID (GuildPackets.cpp:159-162); the decline and the leave carry no body (GuildHandler.cpp:82, :125-129).
	CMSGGuildAcceptInvite      uint32 = 0x430029
	CMSGGuildDeclineInvitation uint32 = 0x43002A
	CMSGGuildLeave             uint32 = 0x2E0003

	SMSGChat                       uint32 = 0x4A0001
	SMSGChatPlayerAmbiguous        uint32 = 0x4A0004
	SMSGChatRestricted             uint32 = 0x4A0007
	SMSGChatPlayerNotFound         uint32 = 0x4A000D
	SMSGChatServerMessage          uint32 = 0x4A001C
	SMSGChannelNotify              uint32 = 0x4A0017
	SMSGChannelNotifyJoined        uint32 = 0x4A0019
	SMSGChannelNotifyLeft          uint32 = 0x4A001A
	SMSGChannelList                uint32 = 0x4A001B
	SMSGPrintNotification          uint32 = 0x45006E
	SMSGMotd                       uint32 = 0x4A0003
	SMSGEmote                      uint32 = 0x450273
	SMSGTextEmote                  uint32 = 0x450126
	SMSGPartyInvite                uint32 = 0x450061
	SMSGPartyUpdate                uint32 = 0x450097
	SMSGPartyMemberFullState       uint32 = 0x450203
	SMSGPartyMemberPartialState    uint32 = 0x450202
	SMSGGroupDecline               uint32 = 0x45023B
	SMSGGroupUninvite              uint32 = 0x45023D
	SMSGGroupDestroyed             uint32 = 0x45023E
	SMSGGroupNewLeader             uint32 = 0x4500D6
	SMSGSendRaidTargetUpdateAll    uint32 = 0x4500D7
	SMSGSendRaidTargetUpdateSingle uint32 = 0x4500D8
	SMSGPartyCommandResult         uint32 = 0x450240
	SMSGReadyCheckStarted          uint32 = 0x450099
	SMSGReadyCheckResponse         uint32 = 0x45009A
	SMSGReadyCheckCompleted        uint32 = 0x45009B
	// The guild invite flow the server sends (Opcodes.h:1625, 1634, 1646; GuildPackets.cpp:146-155, :186-214,
	// :254-269).
	SMSGGuildInvite            uint32 = 0x510012
	SMSGGuildEventPlayerJoined uint32 = 0x510032
	SMSGGuildCommandResult     uint32 = 0x510002

	// Object and movement traffic of round 3 (Opcodes.h:1919-1998, 2185, 2174).
	SMSGUpdateObject       uint32 = 0x5C0000
	SMSGOnMonsterMove      uint32 = 0x5E0002
	SMSGMoveUpdate         uint32 = 0x5E000E
	SMSGMoveUpdateTeleport uint32 = 0x5E000F
	// SMSG_MOVE_UPDATE_KNOCK_BACK 0x5E0010 and SMSG_MOVE_KNOCK_BACK 0x5E0033 (Opcodes.h:1915, 2005): the
	// knockback the acked mover broadcasts and the one the server sends to the mover itself.
	SMSGMoveUpdateKnockBack      uint32 = 0x5E0010
	SMSGMoveKnockBack            uint32 = 0x5E0033
	SMSGMoveTeleport             uint32 = 0x5E0034
	SMSGMoveRoot                 uint32 = 0x5E0029
	SMSGMoveUnroot               uint32 = 0x5E002A
	SMSGNewWorld                 uint32 = 0x45002B
	SMSGTransferPending          uint32 = 0x450071
	SMSGQueryPlayerNamesResponse uint32 = 0x640027
	SMSGCreatureQueryResponse    uint32 = 0x490006
	// SMSG_QUERY_GAME_OBJECT_RESPONSE 0x490007 (Opcodes.h:2175): the entry, the GUID, the allow bit, the
	// stats size and the stats block (QueryPackets.cpp:350-388).
	SMSGGameObjectQueryResponse uint32 = 0x490007
	SMSGDBReply                 uint32 = 0x490000

	// Combat, spells, auras, cooldowns and experience (Opcodes.h:1177-1178, 1296, 1363-1364, 1403, 1475, 1864,
	// 2156, 2365-2367, 2379).
	SMSGAttackStart          uint32 = 0x4B0017
	SMSGAttackStop           uint32 = 0x4B0018
	SMSGCancelCombat         uint32 = 0x4B0025
	SMSGAttackSwingError     uint32 = 0x4B0026
	SMSGAttackSwingLandedLog uint32 = 0x4B0027
	SMSGAttackerStateUpdate  uint32 = 0x4B002C
	SMSGSpellStart           uint32 = 0x67002D
	SMSGSpellGo              uint32 = 0x67002C
	SMSGCastFailed           uint32 = 0x67004A
	SMSGSpellFailure         uint32 = 0x670046
	SMSGSpellFailedOther     uint32 = 0x670048
	SMSGSpellNonMeleeDamage  uint32 = 0x670020
	SMSGSpellHealLog         uint32 = 0x67000A
	SMSGSpellPeriodicAuraLog uint32 = 0x670008
	SMSGSpellEnergizeLog     uint32 = 0x670009
	SMSGAuraUpdate           uint32 = 0x670011
	SMSGSpellCooldown        uint32 = 0x670005
	SMSGCooldownEvent        uint32 = 0x450165
	// SMSG_AI_REACTION 0x450161 (CombatPackets.h:124-133): the unit that reacted and the reaction as a
	// uint32; HOSTILE marks the pull moment (Unit.cpp:6242).
	SMSGAIReaction      uint32 = 0x450161
	SMSGClearCooldown   uint32 = 0x450166
	SMSGClearCooldowns  uint32 = 0x670015
	SMSGPowerUpdate     uint32 = 0x45017E
	SMSGSpellDelayed    uint32 = 0x670032
	SMSGLogXPGain       uint32 = 0x450190
	SMSGDeathReleaseLoc uint32 = 0x45017F
	// The core-gains round (Opcodes.h:922, 1262, 1466-1472, 1675, 2162, 2361-2384, 2415-2417): the advanced
	// combat log flag, the pushed threat lists, the encounter packets and the combat-log kinds this round decodes.
	CMSGSetAdvancedCombatLogging uint32 = 0x3D0193
	SMSGThreatUpdate             uint32 = 0x450186
	SMSGHighestThreatUpdate      uint32 = 0x450185
	SMSGThreatRemove             uint32 = 0x450187
	SMSGThreatClear              uint32 = 0x450188
	SMSGEncounterStart           uint32 = 0x450228
	SMSGEncounterEnd             uint32 = 0x450229
	SMSGBossKill                 uint32 = 0x4B002B
	SMSGSpellInterruptLog        uint32 = 0x67000D
	SMSGSpellDispellLog          uint32 = 0x670007
	SMSGSpellMissLog             uint32 = 0x670034
	SMSGSpellInstakillLog        uint32 = 0x670021
	SMSGEnvironmentalDamageLog   uint32 = 0x67000E
	SMSGSpellAbsorbLog           uint32 = 0x67000C
	SMSGProcResist               uint32 = 0x450205
	SMSGSpellOrDamageImmune      uint32 = 0x67001D
	// The rest of the death flow (Opcodes.h:27, 228, 1948-1976, 377, 430, 1318): the graveyard, the corpse, a
	// resurrection offer, the spirit healer's menu and the movement state a ghost is given.
	SMSGResurrectRequest uint32 = 0x450012
	// SMSG_SUMMON_REQUEST 0x4501CC (Opcodes.h:2406): a summoning portal offers the player the summon
	// (MovementPackets.cpp:1100-1110).
	SMSGSummonRequest        uint32 = 0x4501CC
	SMSGCorpseLocation       uint32 = 0x4500F7
	SMSGCorpseTransportQuery uint32 = 0x4501BE
	SMSGCorpseReclaimDelay   uint32 = 0x4501F4
	SMSGPreRessurect         uint32 = 0x450211
	SMSGStandStateUpdate     uint32 = 0x4501C8
	SMSGGossipMessage        uint32 = 0x650018
	// The act round's NPC answers (Opcodes.h:1602, 1604, 2058): the menu's completion, the option's NPC
	// interaction, and the interaction-open result.
	SMSGGossipComplete           uint32 = 0x650017
	SMSGGossipOptionNPCInteract  uint32 = 0x650028
	SMSGNPCInteractionOpenResult uint32 = 0x45031A
	// A quest-only NPC's answer to the quest hello (Opcodes.h:2200): its quest list.
	SMSGQuestGiverQuestList uint32 = 0x65001A
	SMSGMoveSetRunSpeed     uint32 = 0x5E0020
	SMSGMoveSetSwimSpeed    uint32 = 0x5E0022
	SMSGMoveSetWaterWalk    uint32 = 0x5E002B
	SMSGMoveSetLandWalk     uint32 = 0x5E002E
	SMSGSendKnownSpells     uint32 = 0x670019
	SMSGSendSpellHistory    uint32 = 0x67001A
	// The spell book changes of ticket #93 round R5 (Opcodes.h:1810, 2407, 2450; SpellPackets.cpp:506-530,
	// 607-618): a learned spell (a higher rank carries the one it supersedes), a superseded pair, and unlearns.
	SMSGLearnedSpells    uint32 = 0x670040
	SMSGSupercededSpells uint32 = 0x67003F
	SMSGUnlearnedSpells  uint32 = 0x670041
	SMSGLevelUpInfo      uint32 = 0x450194
	SMSGSpellExecuteLog  uint32 = 0x670033
	// The mount answers (Opcodes.h:1444, 1891; SpellPackets.cpp:1156-1165): MountResult is one int32, 0 for a
	// mount that went through. DismountResult shares the shape but this core never sends it
	// (Opcodes.cpp:1520 STATUS_UNHANDLED): the field update is what ends a dismount.
	SMSGMountResult    uint32 = 0x45000F
	SMSGDismountResult uint32 = 0x450010
)

// Loot and game objects (Opcodes.h:357-358, 579-583, 1499-1500, 1805, 1865-1874, 2395):
// CMSG_LOOT_UNIT and its four requests, CMSG_GAME_OBJ_USE with the report the real client follows it with, and the
// window, money, roll and object-state answers.
const (
	CMSGLootUnit         uint32 = 0x3D00CB
	CMSGLootMoney        uint32 = 0x3D00CC
	CMSGLootItem         uint32 = 0x3D00CD
	CMSGLootRelease      uint32 = 0x3D00D1
	CMSGLootRoll         uint32 = 0x3D00D2
	CMSGGameObjUse       uint32 = 0x3E0089
	CMSGGameObjReportUse uint32 = 0x3E008A

	SMSGLootResponse      uint32 = 0x4500BE
	SMSGLootRemoved       uint32 = 0x4500BF
	SMSGCoinRemoved       uint32 = 0x4500C0
	SMSGLootReleaseAll    uint32 = 0x4500C3
	SMSGLootRelease       uint32 = 0x4500C4
	SMSGLootMoneyNotify   uint32 = 0x4500C5
	SMSGStartLootRoll     uint32 = 0x4500C6
	SMSGLootRoll          uint32 = 0x4500C7
	SMSGLootRollsComplete uint32 = 0x4500C9
	SMSGLootAllPassed     uint32 = 0x4500CA
	SMSGLootRollWon       uint32 = 0x4500CB
	SMSGItemPushResult    uint32 = 0x4500CC
	SMSGLootList          uint32 = 0x4501EB

	SMSGGameObjectCustomAnim uint32 = 0x450068
	SMSGGameObjectDespawn    uint32 = 0x450069

	// The item actions of round R2 (Opcodes.h:88-95, 323, 732, 984, 1000-1001, 1051, 1793): the client's
	// inventory moves and the two answers that carry an item gain and a refusal.
	CMSGAutoEquipItem          uint32 = 0x400004
	CMSGAutoStoreBagItem       uint32 = 0x400005
	CMSGSwapItem               uint32 = 0x400006
	CMSGSwapInvItem            uint32 = 0x400007
	CMSGSplitItem              uint32 = 0x400008
	CMSGAutoEquipItemSlot      uint32 = 0x400009
	CMSGDestroyItem            uint32 = 0x3D016A
	CMSGUseItem                uint32 = 0x3D016F
	CMSGOpenItem               uint32 = 0x3D0211
	SMSGInventoryChangeFailure uint32 = 0x680005
)

// The pets of round P1 (Opcodes.h:756, 770-771, 2090, 2107, 2113, 2116 at the pinned core): the control bar, the
// mode, the pet's cast failure and action feedback, and the three client packets. Each GUID in them is packed.
const (
	SMSGPetSpellsMessage  uint32 = 0x670014
	SMSGPetMode           uint32 = 0x45001F
	SMSGPetCastFailed     uint32 = 0x67004B
	SMSGPetActionFeedback uint32 = 0x4501F3
	CMSGPetAction         uint32 = 0x3E001F
	CMSGPetSpellAutocast  uint32 = 0x3E0024
	CMSGPetStopAttack     uint32 = 0x3E0020
)

// The vendor of round R3 (Opcodes.h:147-148, 555, 844, 906-907, 1270-1271, 2300, 2483): the six client packets and
// the four answers. LIST_INVENTORY is the NPC hello (Hello::Read), which is also how a vendor without the gossip
// flag is opened.
const (
	CMSGListInventory    uint32 = 0x3E0036
	CMSGBuyItem          uint32 = 0x3E0039
	CMSGSellItem         uint32 = 0x3E0037
	CMSGSellAllJunkItems uint32 = 0x3E0038
	CMSGBuyBackItem      uint32 = 0x3E003A
	CMSGRepairItem       uint32 = 0x3E0087
	SMSGVendorInventory  uint32 = 0x45005C
	SMSGSellResponse     uint32 = 0x450170
	SMSGBuySucceeded     uint32 = 0x450171
	SMSGBuyFailed        uint32 = 0x450172
)

// The quests of round R5 (Opcodes.h:805-814, 2195-2205, 2214-2217): the giver's client packets (HELLO is R0's)
// and the answers, credits and log notices.
const (
	CMSGQuestGiverStatusQuery      uint32 = 0x3E0031
	CMSGQuestGiverQueryQuest       uint32 = 0x3E002C
	CMSGQuestGiverAcceptQuest      uint32 = 0x3E002D
	CMSGQuestGiverCompleteQuest    uint32 = 0x3E002E
	CMSGQuestGiverRequestReward    uint32 = 0x3E0030
	CMSGQuestGiverChooseReward     uint32 = 0x3E002F
	CMSGQuestGiverCloseQuest       uint32 = 0x3E00EC
	CMSGQuestLogRemoveQuest        uint32 = 0x3E00D0
	SMSGQuestGiverStatus           uint32 = 0x65001B
	SMSGQuestGiverQuestDetails     uint32 = 0x650012
	SMSGQuestGiverRequestItems     uint32 = 0x650013
	SMSGQuestGiverOfferReward      uint32 = 0x650014
	SMSGQuestGiverQuestComplete    uint32 = 0x650003
	SMSGQuestGiverInvalidQuest     uint32 = 0x650005
	SMSGQuestGiverQuestFailed      uint32 = 0x650006
	SMSGQuestUpdateAddCredit       uint32 = 0x65000C
	SMSGQuestUpdateAddCreditSimple uint32 = 0x65000D
	SMSGQuestUpdateComplete        uint32 = 0x650009
	SMSGQuestLogFull               uint32 = 0x650007
)

// The trainer of round R4 (Opcodes.h:1019-1020, 1886, 2430-2431): the list ask (an NPC hello), the purchase, and
// the list and its refusal. A purchase's success answer is SMSG_LEARNED_SPELLS, declared above with the spell book
// (round R5, #93), which is the packet any learn arrives on.
const (
	CMSGTrainerList     uint32 = 0x3E0043
	CMSGTrainerBuySpell uint32 = 0x3E0044
	SMSGTrainerList     uint32 = 0x45018A
	SMSGTrainerBuyFail  uint32 = 0x45018B
)

// The mail of round W-106 (Opcodes.h:586-592, 786, 910, 1876-1877, 1954, 2057): the client's mailbox packets (every
// one names the mailbox except the delete) and the server's list, command result, next-mail answer and new-mail
// notice. The mailbox itself is opened client-side: no use packet precedes the list ask (the core only checks the
// interaction range in CanOpenMailBox, MailHandler.cpp:39-63).
const (
	CMSGMailGetList             uint32 = 0x3E00D8
	CMSGMailTakeMoney           uint32 = 0x3E00D9
	CMSGMailTakeItem            uint32 = 0x3E00DA
	CMSGQueryNextMailTime       uint32 = 0x3E00DB
	CMSGMailMarkAsRead          uint32 = 0x3E00DC
	CMSGMailDelete              uint32 = 0x3D00E7
	CMSGMailReturnToSender      uint32 = 0x430081
	CMSGSendMail                uint32 = 0x430026
	SMSGMailCommandResult       uint32 = 0x4500E4
	SMSGNotifyReceivedMail      uint32 = 0x4500E5
	SMSGMailListResult          uint32 = 0x450200
	SMSGMailQueryNextTimeResult uint32 = 0x450201
)

// The auction house of issue #106 part 2 (Opcodes.h:70-85, 1179-1195): the client's hello, browse, bucket listing, own
// listing, post, bid, cancel and commodity quote and purchase, and the server's hello, command result, lists, quote
// and notifications. The modern bucket protocol: the favourite, replicate and bidded-list packets are not built.
const (
	CMSGAuctionHelloRequest               uint32 = 0x3E005F
	CMSGAuctionSellItem                   uint32 = 0x3E0060
	CMSGAuctionRemoveItem                 uint32 = 0x3E0061
	CMSGAuctionPlaceBid                   uint32 = 0x3E0063
	CMSGAuctionBrowseQuery                uint32 = 0x3E0064
	CMSGAuctionListItemsByBucketKey       uint32 = 0x3E0065
	CMSGAuctionListOwnedItems             uint32 = 0x3E0067
	CMSGAuctionGetCommodityQuote          uint32 = 0x3E006A
	CMSGAuctionConfirmCommoditiesPurchase uint32 = 0x3E006B
	CMSGAuctionSellCommodity              uint32 = 0x3E006D
	SMSGAuctionHelloResponse              uint32 = 0x450199
	SMSGAuctionCommandResult              uint32 = 0x45019B
	SMSGAuctionWonNotification            uint32 = 0x45019C
	SMSGAuctionOutbidNotification         uint32 = 0x45019D
	SMSGAuctionClosedNotification         uint32 = 0x45019E
	SMSGAuctionOwnerBidNotification       uint32 = 0x45019F
	SMSGAuctionListBucketsResult          uint32 = 0x4502F5
	SMSGAuctionListItemsResult            uint32 = 0x4502F6
	SMSGAuctionListOwnedItemsResult       uint32 = 0x4502F8
	SMSGAuctionGetCommodityQuoteResult    uint32 = 0x4502FA
)

// The flight paths of round R6 (Opcodes.h:49, 348, 1006-1008, 1141, 2052, 2350, 2413): the node status ask, the
// node list ask (and the enable a client sends when it meets a flight master), the flight, and the answers.
const (
	CMSGTaxiNodeStatusQuery     uint32 = 0x3E003E
	CMSGEnableTaxiNode          uint32 = 0x3E003F
	CMSGTaxiQueryAvailableNodes uint32 = 0x3E0040
	CMSGActivateTaxi            uint32 = 0x3E0041
	SMSGTaxiNodeStatus          uint32 = 0x450128
	SMSGActivateTaxiReply       uint32 = 0x450129
	SMSGNewTaxiPath             uint32 = 0x45012A
	SMSGShowTaxiNodes           uint32 = 0x450179
)

// The battleground watch (Opcodes.h:102-110, 358, 775, 852, 1162, 1206-1219, 1756, 2166-2170, 2458, 2473):
// the queue/port/leave/status/log/spirit-queue client packets this client sends, and the status/world-state/match/
// positions/join/leave/spirit-time/capture server packets it reads.
const (
	CMSGAreaSpiritHealerQuery uint32 = 0x3E0046
	CMSGAreaSpiritHealerQueue uint32 = 0x3E0047
	CMSGBattlefieldLeave      uint32 = 0x3D001F
	CMSGBattlefieldList       uint32 = 0x3D002A
	CMSGBattlefieldPort       uint32 = 0x3E00C7
	// CMSGBattlemasterHello is the battlemaster's list probe (NPCPackets.cpp:94-97): the packed GUID of the NPC the
	// character stands next to. The handler answers SMSG_BATTLEFIELD_LIST.
	CMSGBattlemasterHello        uint32 = 0x3D018F
	CMSGBattlemasterJoin         uint32 = 0x3E00BD
	CMSGBattlemasterJoinArena    uint32 = 0x3E00BE
	CMSGPVPLogData               uint32 = 0x3D0028
	CMSGRequestBattlefieldStatus uint32 = 0x430008

	SMSGBattlefieldStatusNeedConfirmation uint32 = 0x4B0000
	SMSGBattlefieldStatusActive           uint32 = 0x4B0001
	SMSGBattlefieldStatusQueued           uint32 = 0x4B0002
	SMSGBattlefieldStatusNone             uint32 = 0x4B0003
	SMSGBattlefieldStatusFailed           uint32 = 0x4B0004
	SMSGBattlefieldList                   uint32 = 0x4B0005
	SMSGBattlegroundPlayerPositions       uint32 = 0x4B0006
	SMSGUpdateCapturePoint                uint32 = 0x4B0007
	SMSGCapturePointRemoved               uint32 = 0x4B0008
	SMSGBattlegroundPlayerJoined          uint32 = 0x4B0009
	SMSGBattlegroundPlayerLeft            uint32 = 0x4B000A
	SMSGPVPMatchStatistics                uint32 = 0x4B0010
	SMSGPVPMatchSetState                  uint32 = 0x4B002E
	SMSGPVPMatchComplete                  uint32 = 0x4B002F
	SMSGPVPMatchInitialize                uint32 = 0x4B0030
	SMSGAreaSpiritHealerTime              uint32 = 0x4501EA
	SMSGInitWorldStates                   uint32 = 0x4501F0
	SMSGUpdateWorldState                  uint32 = 0x4501F2
)

// The map change of round A7 (Opcodes.h:62, 998, 1064, 1783, 2273, 2410, 2433, 2468, 2510).
const (
	CMSGAreaTrigger          uint32 = 0x3D0089
	CMSGSuspendTokenResponse uint32 = 0x440008
	CMSGWorldPortResponse    uint32 = 0x430025
	SMSGTransferAborted      uint32 = 0x4501AE
	SMSGSuspendToken         uint32 = 0x450041
	SMSGResumeToken          uint32 = 0x450042
	SMSGUpdateLastInstance   uint32 = 0x450134
	SMSGWorldServerInfo      uint32 = 0x450046
	SMSGInstanceInfo         uint32 = 0x4500DD
)

// clientNames names the client opcodes this client sends, for error messages.
var clientNames = map[uint32]string{
	CMSGAuthSession:             "CMSG_AUTH_SESSION",
	CMSGAuthContinuedSession:    "CMSG_AUTH_CONTINUED_SESSION",
	CMSGEnterEncryptedModeAck:   "CMSG_ENTER_ENCRYPTED_MODE_ACK",
	CMSGPing:                    "CMSG_PING",
	CMSGLogDisconnect:           "CMSG_LOG_DISCONNECT",
	CMSGQueuedMessagesEnd:       "CMSG_QUEUED_MESSAGES_END",
	CMSGEnumCharacters:          "CMSG_ENUM_CHARACTERS",
	CMSGPlayerLogin:             "CMSG_PLAYER_LOGIN",
	CMSGLoadingScreenNotify:     "CMSG_LOADING_SCREEN_NOTIFY",
	CMSGMoveInitActiveMoverDone: "CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE",
	CMSGTimeSyncResponse:        "CMSG_TIME_SYNC_RESPONSE",
	CMSGLogoutRequest:           "CMSG_LOGOUT_REQUEST",

	CMSGMoveStartForward:            "CMSG_MOVE_START_FORWARD",
	CMSGMoveStartBackward:           "CMSG_MOVE_START_BACKWARD",
	CMSGMoveStop:                    "CMSG_MOVE_STOP",
	CMSGMoveStartStrafeLeft:         "CMSG_MOVE_START_STRAFE_LEFT",
	CMSGMoveStopStrafe:              "CMSG_MOVE_STOP_STRAFE",
	CMSGMoveJump:                    "CMSG_MOVE_JUMP",
	CMSGMoveStartTurnLeft:           "CMSG_MOVE_START_TURN_LEFT",
	CMSGMoveStopTurn:                "CMSG_MOVE_STOP_TURN",
	CMSGMoveSetRunMode:              "CMSG_MOVE_SET_RUN_MODE",
	CMSGMoveSetWalkMode:             "CMSG_MOVE_SET_WALK_MODE",
	CMSGMoveFallLand:                "CMSG_MOVE_FALL_LAND",
	CMSGMoveStartSwim:               "CMSG_MOVE_START_SWIM",
	CMSGMoveStopSwim:                "CMSG_MOVE_STOP_SWIM",
	CMSGMoveSetFacing:               "CMSG_MOVE_SET_FACING",
	CMSGMoveHeartbeat:               "CMSG_MOVE_HEARTBEAT",
	CMSGMoveSetFacingHeartbeat:      "CMSG_MOVE_SET_FACING_HEARTBEAT",
	CMSGMoveTeleportAck:             "CMSG_MOVE_TELEPORT_ACK",
	CMSGMoveForceRunSpeedChangeAck:  "CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK",
	CMSGMoveForceSwimSpeedChangeAck: "CMSG_MOVE_FORCE_SWIM_SPEED_CHANGE_ACK",
	CMSGMoveWaterWalkAck:            "CMSG_MOVE_WATER_WALK_ACK",
	CMSGMoveKnockBackAck:            "CMSG_MOVE_KNOCK_BACK_ACK",
	CMSGQueryPlayerNames:            "CMSG_QUERY_PLAYER_NAMES",
	CMSGQueryCreature:               "CMSG_QUERY_CREATURE",
	CMSGQueryGameObject:             "CMSG_QUERY_GAME_OBJECT",
	CMSGDBQueryBulk:                 "CMSG_DB_QUERY_BULK",
	CMSGSetAdvancedCombatLogging:    "CMSG_SET_ADVANCED_COMBAT_LOGGING",
	CMSGSuspendTokenResponse:        "CMSG_SUSPEND_TOKEN_RESPONSE",
	CMSGWorldPortResponse:           "CMSG_WORLD_PORT_RESPONSE",
	CMSGAreaTrigger:                 "CMSG_AREA_TRIGGER",
	CMSGSummonResponse:              "CMSG_SUMMON_RESPONSE",

	CMSGChatMessageSay:                 "CMSG_CHAT_MESSAGE_SAY",
	CMSGChatMessageYell:                "CMSG_CHAT_MESSAGE_YELL",
	CMSGChatMessageParty:               "CMSG_CHAT_MESSAGE_PARTY",
	CMSGChatMessageWhisper:             "CMSG_CHAT_MESSAGE_WHISPER",
	CMSGChatMessageEmote:               "CMSG_CHAT_MESSAGE_EMOTE",
	CMSGChatMessageGuild:               "CMSG_CHAT_MESSAGE_GUILD",
	CMSGChatMessageOfficer:             "CMSG_CHAT_MESSAGE_OFFICER",
	CMSGChatMessageRaid:                "CMSG_CHAT_MESSAGE_RAID",
	CMSGChatMessageRaidWarning:         "CMSG_CHAT_MESSAGE_RAID_WARNING",
	CMSGChatMessageInstanceChat:        "CMSG_CHAT_MESSAGE_INSTANCE_CHAT",
	CMSGChatMessageChannel:             "CMSG_CHAT_MESSAGE_CHANNEL",
	CMSGChatJoinChannel:                "CMSG_CHAT_JOIN_CHANNEL",
	CMSGChatLeaveChannel:               "CMSG_CHAT_LEAVE_CHANNEL",
	CMSGChatChannelList:                "CMSG_CHAT_CHANNEL_LIST",
	CMSGChatAddonMessage:               "CMSG_CHAT_ADDON_MESSAGE",
	CMSGChatAddonMessageTargeted:       "CMSG_CHAT_ADDON_MESSAGE_TARGETED",
	CMSGChatRegisterAddonPrefixes:      "CMSG_CHAT_REGISTER_ADDON_PREFIXES",
	CMSGChatUnregisterAllAddonPrefixes: "CMSG_CHAT_UNREGISTER_ALL_ADDON_PREFIXES",
	CMSGPartyInvite:                    "CMSG_PARTY_INVITE",
	CMSGPartyInviteResponse:            "CMSG_PARTY_INVITE_RESPONSE",
	CMSGPartyUninvite:                  "CMSG_PARTY_UNINVITE",
	CMSGSetLootMethod:                  "CMSG_SET_LOOT_METHOD",
	CMSGLeaveGroup:                     "CMSG_LEAVE_GROUP",
	CMSGSetPartyLeader:                 "CMSG_SET_PARTY_LEADER",
	CMSGChangeSubGroup:                 "CMSG_CHANGE_SUB_GROUP",
	CMSGSwapSubGroups:                  "CMSG_SWAP_SUB_GROUPS",
	CMSGConvertRaid:                    "CMSG_CONVERT_RAID",
	CMSGSetAssistantLeader:             "CMSG_SET_ASSISTANT_LEADER",
	CMSGUpdateRaidTarget:               "CMSG_UPDATE_RAID_TARGET",
	CMSGRequestPartyMemberStats:        "CMSG_REQUEST_PARTY_MEMBER_STATS",
	CMSGGuildAcceptInvite:              "CMSG_ACCEPT_GUILD_INVITE",
	CMSGGuildDeclineInvitation:         "CMSG_GUILD_DECLINE_INVITATION",
	CMSGGuildLeave:                     "CMSG_GUILD_LEAVE",
	CMSGSetSelection:                   "CMSG_SET_SELECTION",
	CMSGTalkToGossip:                   "CMSG_TALK_TO_GOSSIP",
	CMSGGossipSelectOption:             "CMSG_GOSSIP_SELECT_OPTION",
	CMSGQuestGiverHello:                "CMSG_QUEST_GIVER_HELLO",
	CMSGSpiritHealerActivate:           "CMSG_SPIRIT_HEALER_ACTIVATE",
	CMSGCloseInteraction:               "CMSG_CLOSE_INTERACTION",
	CMSGAttackSwing:                    "CMSG_ATTACK_SWING",
	CMSGAttackStop:                     "CMSG_ATTACK_STOP",
	CMSGCastSpell:                      "CMSG_CAST_SPELL",
	CMSGCancelCast:                     "CMSG_CANCEL_CAST",

	CMSGCreateCharacter:                "CMSG_CREATE_CHARACTER",
	CMSGCheckCharacterNameAvailability: "CMSG_CHECK_CHARACTER_NAME_AVAILABILITY",
	CMSGCharDelete:                     "CMSG_CHAR_DELETE",

	CMSGLootUnit:         "CMSG_LOOT_UNIT",
	CMSGLootMoney:        "CMSG_LOOT_MONEY",
	CMSGLootItem:         "CMSG_LOOT_ITEM",
	CMSGLootRelease:      "CMSG_LOOT_RELEASE",
	CMSGLootRoll:         "CMSG_LOOT_ROLL",
	CMSGGameObjUse:       "CMSG_GAME_OBJ_USE",
	CMSGGameObjReportUse: "CMSG_GAME_OBJ_REPORT_USE",

	CMSGPetAction:        "CMSG_PET_ACTION",
	CMSGPetSpellAutocast: "CMSG_PET_SPELL_AUTOCAST",
	CMSGPetStopAttack:    "CMSG_PET_STOP_ATTACK",

	CMSGAutoEquipItem:     "CMSG_AUTO_EQUIP_ITEM",
	CMSGAutoEquipItemSlot: "CMSG_AUTO_EQUIP_ITEM_SLOT",
	CMSGAutoStoreBagItem:  "CMSG_AUTO_STORE_BAG_ITEM",
	CMSGSwapItem:          "CMSG_SWAP_ITEM",
	CMSGSwapInvItem:       "CMSG_SWAP_INV_ITEM",
	CMSGSplitItem:         "CMSG_SPLIT_ITEM",
	CMSGDestroyItem:       "CMSG_DESTROY_ITEM",
	CMSGUseItem:           "CMSG_USE_ITEM",
	CMSGOpenItem:          "CMSG_OPEN_ITEM",

	CMSGListInventory:    "CMSG_LIST_INVENTORY",
	CMSGBuyItem:          "CMSG_BUY_ITEM",
	CMSGSellItem:         "CMSG_SELL_ITEM",
	CMSGSellAllJunkItems: "CMSG_SELL_ALL_JUNK_ITEMS",
	CMSGBuyBackItem:      "CMSG_BUY_BACK_ITEM",
	CMSGRepairItem:       "CMSG_REPAIR_ITEM",

	CMSGQuestGiverStatusQuery:   "CMSG_QUEST_GIVER_STATUS_QUERY",
	CMSGQuestGiverQueryQuest:    "CMSG_QUEST_GIVER_QUERY_QUEST",
	CMSGQuestGiverAcceptQuest:   "CMSG_QUEST_GIVER_ACCEPT_QUEST",
	CMSGQuestGiverCompleteQuest: "CMSG_QUEST_GIVER_COMPLETE_QUEST",
	CMSGQuestGiverRequestReward: "CMSG_QUEST_GIVER_REQUEST_REWARD",
	CMSGQuestGiverChooseReward:  "CMSG_QUEST_GIVER_CHOOSE_REWARD",
	CMSGQuestGiverCloseQuest:    "CMSG_QUEST_GIVER_CLOSE_QUEST",
	CMSGQuestLogRemoveQuest:     "CMSG_QUEST_LOG_REMOVE_QUEST",

	CMSGTrainerList:     "CMSG_TRAINER_LIST",
	CMSGTrainerBuySpell: "CMSG_TRAINER_BUY_SPELL",

	CMSGMailGetList:                       "CMSG_MAIL_GET_LIST",
	CMSGMailTakeMoney:                     "CMSG_MAIL_TAKE_MONEY",
	CMSGMailTakeItem:                      "CMSG_MAIL_TAKE_ITEM",
	CMSGQueryNextMailTime:                 "CMSG_QUERY_NEXT_MAIL_TIME",
	CMSGMailMarkAsRead:                    "CMSG_MAIL_MARK_AS_READ",
	CMSGMailDelete:                        "CMSG_MAIL_DELETE",
	CMSGMailReturnToSender:                "CMSG_MAIL_RETURN_TO_SENDER",
	CMSGSendMail:                          "CMSG_SEND_MAIL",
	CMSGAuctionHelloRequest:               "CMSG_AUCTION_HELLO_REQUEST",
	CMSGAuctionSellItem:                   "CMSG_AUCTION_SELL_ITEM",
	CMSGAuctionRemoveItem:                 "CMSG_AUCTION_REMOVE_ITEM",
	CMSGAuctionPlaceBid:                   "CMSG_AUCTION_PLACE_BID",
	CMSGAuctionBrowseQuery:                "CMSG_AUCTION_BROWSE_QUERY",
	CMSGAuctionListItemsByBucketKey:       "CMSG_AUCTION_LIST_ITEMS_BY_BUCKET_KEY",
	CMSGAuctionListOwnedItems:             "CMSG_AUCTION_LIST_OWNED_ITEMS",
	CMSGAuctionGetCommodityQuote:          "CMSG_AUCTION_GET_COMMODITY_QUOTE",
	CMSGAuctionConfirmCommoditiesPurchase: "CMSG_AUCTION_CONFIRM_COMMODITIES_PURCHASE",
	CMSGAuctionSellCommodity:              "CMSG_AUCTION_SELL_COMMODITY",

	CMSGTaxiNodeStatusQuery:      "CMSG_TAXI_NODE_STATUS_QUERY",
	CMSGEnableTaxiNode:           "CMSG_ENABLE_TAXI_NODE",
	CMSGTaxiQueryAvailableNodes:  "CMSG_TAXI_QUERY_AVAILABLE_NODES",
	CMSGActivateTaxi:             "CMSG_ACTIVATE_TAXI",
	CMSGAreaSpiritHealerQuery:    "CMSG_AREA_SPIRIT_HEALER_QUERY",
	CMSGAreaSpiritHealerQueue:    "CMSG_AREA_SPIRIT_HEALER_QUEUE",
	CMSGBattlefieldLeave:         "CMSG_BATTLEFIELD_LEAVE",
	CMSGBattlefieldList:          "CMSG_BATTLEFIELD_LIST",
	CMSGBattlefieldPort:          "CMSG_BATTLEFIELD_PORT",
	CMSGBattlemasterHello:        "CMSG_BATTLEMASTER_HELLO",
	CMSGBattlemasterJoin:         "CMSG_BATTLEMASTER_JOIN",
	CMSGPVPLogData:               "CMSG_PVP_LOG_DATA",
	CMSGRequestBattlefieldStatus: "CMSG_REQUEST_BATTLEFIELD_STATUS",
}

// ClientName returns the core name of a client opcode this client sends, or its hex value.
func ClientName(core uint32) string {
	if name, ok := clientNames[core]; ok {
		return name
	}
	return fmt.Sprintf("0x%06X", core)
}

// ServerName returns the core name of a server opcode, or "" when the number is not in the core's enum.
func ServerName(core uint32) string { return serverNames[core] }

// The ClassicOpcodes configuration the server runs with: the defaults of the config keys read by
// ClassicOpcodes.cpp, which is what the live server uses (Classic.OpcodeGroupShift = -1 lets the realm's build
// decide). A deployment that changes them needs a different table; this client documents and uses the defaults.
const (
	// opcodeGroupShiftFrom is the realm build from which every opcode group is one higher (ClassicOpcodes.cpp
	// ExtraGroupShift, "realm->Build >= 70291").
	opcodeGroupShiftFrom = 70291
	build70755First      = 70755
	build70790First      = 70790
	firstRetailClient    = 0x2A
	classicClientConn    = 0x45 // the 70245 client connection group (retail 0x44)
	clientMainInsert     = 0x13B
	firstRetailServer    = 0x45
	lastRetailServer     = 0x6A
	chatRetailServer     = 0x4A
	guildRetailServer    = 0x51
	serverMainInsert     = 0x98
)

// serverOverrides are the explicit core server opcode -> Classic 70245 number overrides (ClassicOpcodes.cpp:55-67).
var serverOverrides = map[uint32]uint32{
	0x4501B6: 0x4601B5, // SMSG_ACCOUNT_DATA_TIMES
	0x4501B7: 0x4601B6, // SMSG_GAME_TIME_UPDATE
	0x4501B8: 0x4601B7, // SMSG_GAME_TIME_SET
	0x4501B9: 0x4601B8, // SMSG_LOGIN_SET_TIME_SPEED
	0x4501C0: 0x4601BF, // SMSG_SERVER_TIME_OFFSET
	0x45036F: 0x460371, // SMSG_MIRROR_VARS
	0x450380: 0x460382, // SMSG_LAST_CATALOG_FETCH_RESPONSE
	0x51002F: 0x52002E, // SMSG_QUERY_GUILD_FOLLOW_INFO_RESPONSE
}

// buildMessageChanges is the server's per-build message insert/remove list (ClassicOpcodes.cpp Build70755Changes
// and Build70790Changes with the shipped defaults).
type buildMessageChanges struct {
	firstBuild uint32
	clientNew  []uint32
	serverNew  []uint32
	removed    []uint32 // previous build's numbering
}

var (
	build70755 = buildMessageChanges{
		firstBuild: build70755First,
		clientNew:  []uint32{0x440071, 0x440072},
		serverNew:  []uint32{0x470125},
	}
	build70790 = buildMessageChanges{
		firstBuild: build70790First,
		serverNew:  []uint32{0x4700ED},
		removed:    []uint32{0x470125},
	}
)

// OpcodeTable translates a connection's opcodes. The server keeps the same data in a process-wide table plus the
// connection's client build (WorldSocket::_clientBuild) and the realm's build (ExtraGroupShift); a server that
// talks to two builds at once would use two of these.
type OpcodeTable struct {
	clientBuild uint32
	realmBuild  uint32
	shift       uint32
}

// NewOpcodeTable returns the translation for a connection whose client logged in with clientBuild on a realm of
// realmBuild (auth realmlist.gamebuild, which is what extraGroupShift reads).
func NewOpcodeTable(clientBuild, realmBuild uint32) *OpcodeTable {
	t := &OpcodeTable{clientBuild: clientBuild, realmBuild: realmBuild}
	if realmBuild >= opcodeGroupShiftFrom {
		t.shift = 1
	}
	return t
}

// Shift reports the ExtraGroupShift the table uses (0 or 1).
func (t *OpcodeTable) Shift() uint32 { return t.shift }

// ClientBuild returns the build this connection's client claimed (WorldSocket::_clientBuild).
func (t *OpcodeTable) ClientBuild() uint32 { return t.clientBuild }

// RealmBuild returns the realm's build, which selects the group shift.
func (t *OpcodeTable) RealmBuild() uint32 { return t.realmBuild }

func applies(ch buildMessageChanges, clientBuild uint32) bool {
	return clientBuild != 0 && ch.firstBuild != 0 && clientBuild >= ch.firstBuild
}

// clientToBase maps a 70755/70790 client number to the 70707 numbering (ClassicOpcodes.cpp ClientToBaseBuild).
func clientToBase(opcode uint32, ch buildMessageChanges) uint32 {
	group := opcode >> 16
	var lower uint32
	for _, added := range ch.clientNew {
		if added>>16 != group {
			continue
		}
		if added == opcode {
			return UnknownOpcode
		}
		if added < opcode {
			lower++
		}
	}
	return opcode - lower
}

// serverFromBase maps a previous build's server number to this build's number
// (ClassicOpcodes.cpp ServerFromBaseBuild).
func serverFromBase(opcode uint32, ch buildMessageChanges) uint32 {
	group := opcode >> 16
	var lower uint32
	for _, removed := range ch.removed {
		if removed>>16 == group && opcode > removed {
			lower++
		}
	}
	opcode -= lower
	for _, added := range ch.serverNew {
		if added>>16 == group && opcode >= added {
			opcode++
		}
	}
	return opcode
}

// TranslateClientWire returns the core OpcodeClient for a wire number the client sent. UnknownOpcode means the
// number names a message this build does not have (ClassicOpcodes::TranslateClientOpcode).
func (t *OpcodeTable) TranslateClientWire(wire uint32) uint32 {
	num := wire
	if applies(build70790, t.clientBuild) {
		num = clientToBase(num, build70790)
		if num == UnknownOpcode {
			return UnknownOpcode
		}
	}
	if applies(build70755, t.clientBuild) {
		num = clientToBase(num, build70755)
		if num == UnknownOpcode {
			return UnknownOpcode
		}
	}
	if t.shift != 0 {
		group := num >> 16
		if group == classicClientConn+t.shift {
			num = (classicClientConn << 16) | (num & 0xFFFF)
		}
		if group == classicClientConn-1 && (num&0xFFFF) > clientMainInsert {
			num--
		}
	}
	return translateClient70245(num)
}

func translateClient70245(opcode uint32) uint32 {
	if core, ok := clientOverrides[opcode]; ok {
		return core
	}
	group := opcode >> 16
	if group >= firstRetailClient+1 && group <= classicClientConn {
		return ((group - 1) << 16) | (opcode & 0xFFFF)
	}
	return opcode
}

// TranslateServerCore returns the wire number for a core OpcodeServer (ClassicOpcodes::TranslateServerOpcode).
func (t *OpcodeTable) TranslateServerCore(core uint32) uint32 {
	num := translateServer70245(core)
	if t.shift != 0 {
		group := num >> 16
		if group == firstRetailServer+1 && (num&0xFFFF) >= serverMainInsert {
			num++
		}
		if group >= firstRetailServer+1 && group <= lastRetailServer+1 {
			num = ((group + t.shift) << 16) | (num & 0xFFFF)
		}
	}
	if applies(build70755, t.clientBuild) {
		num = serverFromBase(num, build70755)
	}
	if applies(build70790, t.clientBuild) {
		num = serverFromBase(num, build70790)
	}
	return num
}

func translateServer70245(core uint32) uint32 {
	if num, ok := serverOverrides[core]; ok {
		return num
	}
	group := core >> 16
	if group >= firstRetailServer && group <= lastRetailServer {
		index := core & 0xFFFF
		if group == firstRetailServer && index >= 0x17E && index <= 0x1C0 {
			index--
		}
		if group == chatRetailServer && index >= 4 {
			index += 2
		}
		if group == guildRetailServer && index >= 0x1C {
			index--
		}
		return ((group + 1) << 16) | index
	}
	return core
}

// blockedServerCores are the server opcodes ClassicOpcodes.cpp never sends to a Classic client
// (BlockedServerOpcodes, ClassicOpcodes.cpp:62-76). Some of them share their Classic wire number with a message the
// server does send (SMSG_HEALTH_UPDATE with SMSG_POWER_UPDATE, SMSG_GUILD_CHALLENGE_COMPLETED with
// SMSG_GUILD_ITEM_LOOTED_NOTIFY, because Classic removed one message before index 0x1C), so the inverse table must
// prefer the sendable opcode: the lower core number is Health/Completed and would otherwise win.
var blockedServerCores = map[uint32]bool{
	0x45017D: true, // SMSG_HEALTH_UPDATE (health travels through UnitData in Classic)
	0x51001B: true, // SMSG_GUILD_CHALLENGE_COMPLETED
	0x51001A: true, // SMSG_GUILD_CHALLENGE_UPDATE
	0x4A001B: true, // SMSG_CHANNEL_LIST
}

// The inverse of TranslateServerCore depends on the client build through the 70755/70790 steps and on the realm
// build through the group shift, exactly like the forward table, so there is one map per (build class, shift).
var serverWireMaps = func() [3][2]map[uint32]uint32 {
	var tables [3][2]map[uint32]uint32
	classes := []struct {
		at    int
		build uint32
	}{{0, 0}, {1, build70755First}, {2, build70790First}}
	for _, c := range classes {
		for shift := uint32(0); shift <= 1; shift++ {
			var realmBuild uint32 = opcodeGroupShiftFrom - 1 // shift 0
			if shift == 1 {
				realmBuild = opcodeGroupShiftFrom
			}
			t := NewOpcodeTable(c.build, realmBuild)
			out := make(map[uint32]uint32, len(serverNames))
			cores := make([]uint32, 0, len(serverNames))
			for core := range serverNames {
				cores = append(cores, core)
			}
			sortUint32(cores)
			// two passes: every opcode the server can send takes its wire number first, then a blocked one may
			// still name a number no sendable opcode uses. Ascending core order keeps the tie-break deterministic.
			for _, blocked := range []bool{false, true} {
				for _, core := range cores {
					if blockedServerCores[core] != blocked {
						continue
					}
					wire := t.TranslateServerCore(core)
					prev, taken := out[wire]
					if !taken || (!blocked && core < prev) {
						out[wire] = core
					}
				}
			}
			tables[c.at][shift] = out
		}
	}
	return tables
}()

func buildClass(clientBuild uint32) int {
	switch {
	case clientBuild >= build70790First:
		return 2
	case clientBuild >= build70755First:
		return 1
	default:
		return 0
	}
}

// TranslateServerWire returns the core OpcodeServer for a wire number the server sent, or 0 when the core has no
// such number for this connection's builds. The realm build picks the shift, exactly as it does for the forward
// direction; two opcodes can share a Classic number and the one the server can actually send wins (see
// blockedServerCores).
func (t *OpcodeTable) TranslateServerWire(wire uint32) uint32 {
	return serverWireMaps[buildClass(t.clientBuild)][t.shift][wire]
}

// WireForClient returns the wire number for a core OpcodeClient this client wants to send. An explicit table
// entry wins, because that is the number the real client sends (CMSG_LOGOUT_REQUEST is 0x3F0074, not the 0x3F0072
// the pure group arithmetic would give); without one, the arithmetic number (core group + 1) is used.
func (t *OpcodeTable) WireForClient(core uint32) (uint32, bool) {
	group := core >> 16
	var ideal uint32
	haveIdeal := group >= firstRetailClient+1 && group <= classicClientConn
	if haveIdeal {
		ideal = t.wireFromClient70245(((group + 1) << 16) | (core & 0xFFFF))
	}
	// explicit table entries for this core, in a deterministic order
	wires := make([]uint32, 0, 2)
	for wire, mapped := range clientOverrides {
		if mapped == core {
			wires = append(wires, wire)
		}
	}
	sortUint32(wires)
	var best uint32
	for _, wire := range wires {
		candidate := t.wireFromClient70245(wire)
		if t.TranslateClientWire(candidate) != core {
			continue
		}
		if haveIdeal && candidate == ideal {
			return candidate, true
		}
		if best == 0 {
			best = candidate
		}
	}
	if best != 0 {
		return best, true
	}
	if haveIdeal && t.TranslateClientWire(ideal) == core {
		return ideal, true
	}
	return 0, false
}

// wireFromClient70245 inverts TranslateClientOpcode: a 70245 classic number becomes the wire number of this
// connection (main-group insert, group shift, and the 70755 client step).
func (t *OpcodeTable) wireFromClient70245(num uint32) uint32 {
	group := num >> 16
	index := num & 0xFFFF
	if t.shift != 0 {
		if group == classicClientConn-1 && index >= clientMainInsert {
			index++
		}
		if group == classicClientConn {
			group = classicClientConn + t.shift
		}
	}
	wire := (group << 16) | index
	if applies(build70755, t.clientBuild) {
		wire = clientFromBase(wire, build70755)
	}
	return wire
}

// clientFromBase maps a 70707 client number to the wire number of a newer build (inverse of clientToBase).
func clientFromBase(opcode uint32, ch buildMessageChanges) uint32 {
	group := opcode >> 16
	for _, added := range ch.clientNew {
		if added>>16 != group {
			continue
		}
		if opcode >= added {
			opcode++
		}
	}
	return opcode
}

// sortUint32 is a tiny insertion sort; the tables are built once and the slice is about 1410 long.
func sortUint32(v []uint32) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// The client builds this package's opcode translation and the agent's packet parsers were written for.
// 70707 is the base numbering: the tables above are the 70707 numbers, and the 70755 and 70790 insert/remove lists
// apply only to newer claims. 70938 and 70959 are both in the >= 70790 class (docs/forever/LOGIN-R1-NOTES.md,
// PROTOCOL-LOGIN.md), so they share one opcode numbering, and both were run against a realm live. 70959 is the
// 1.60.1 client build the latest run used; it is the newest build here, so a login that is not pinned claims it
// first (docs/forever/BUGS2-NOTES.md). A build in the same class shares the opcode numbering (70888 is in 70938's
// class, PROTOCOL-LOGIN-VERIFY.md:50), but no run has checked such a build's packet layouts, so it is not claimed
// here.
const (
	Build70707 = 70707
	Build70938 = 70938
	Build70959 = 70959
)

// SupportedClientBuilds returns the builds a login may claim on its own (Options.ClientBuild 0), oldest first. A
// realm that accepts none of them is refused by name instead of guessed at.
func SupportedClientBuilds() []uint32 { return []uint32{Build70707, Build70938, Build70959} }
