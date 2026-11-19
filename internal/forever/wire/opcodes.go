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
	// The death flow's client packets (Opcodes.h:252, 413, 777-778, 837, 846, 891, 981).
	CMSGRepopRequest         uint32 = 0x3E00C8
	CMSGReclaimCorpse        uint32 = 0x3E0076
	CMSGResurrectResponse    uint32 = 0x4300AF
	CMSGQueryCorpseLocation  uint32 = 0x43008C
	CMSGQueryCorpseTransport uint32 = 0x43008D
	CMSGTalkToGossip         uint32 = 0x3E0027
	CMSGGossipSelectOption   uint32 = 0x3E0029
	CMSGSpiritHealerActivate uint32 = 0x3E0045
	CMSGCloseInteraction     uint32 = 0x3E0028
	CMSGQueryPlayerNames     uint32 = 0x440010
	CMSGQueryCreature        uint32 = 0x3D0141

	// The combat the client sends (Opcodes.h:66-67, 168-179, 954).
	CMSGSetSelection uint32 = 0x3E00CA
	CMSGAttackSwing  uint32 = 0x3D011C
	CMSGAttackStop   uint32 = 0x3D011D
	CMSGCastSpell    uint32 = 0x3D0174
	CMSGCancelCast   uint32 = 0x3D0177

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
	CMSGChatAddonMessage               uint32 = 0x2C002A
	CMSGChatAddonMessageTargeted       uint32 = 0x2C002B
	CMSGChatRegisterAddonPrefixes      uint32 = 0x2C0005
	CMSGChatUnregisterAllAddonPrefixes uint32 = 0x2C0006
	CMSGPartyInvite                    uint32 = 0x430030
	CMSGPartyInviteResponse            uint32 = 0x430032
	CMSGPartyUninvite                  uint32 = 0x430073
	CMSGLeaveGroup                     uint32 = 0x430075
	CMSGRequestPartyMemberStats        uint32 = 0x43007F

	SMSGChat                    uint32 = 0x4A0001
	SMSGChatPlayerAmbiguous     uint32 = 0x4A0004
	SMSGChatRestricted          uint32 = 0x4A0007
	SMSGChatPlayerNotFound      uint32 = 0x4A000D
	SMSGChatServerMessage       uint32 = 0x4A001C
	SMSGPrintNotification       uint32 = 0x45006E
	SMSGMotd                    uint32 = 0x4A0003
	SMSGEmote                   uint32 = 0x450273
	SMSGTextEmote               uint32 = 0x450126
	SMSGPartyInvite             uint32 = 0x450061
	SMSGPartyUpdate             uint32 = 0x450097
	SMSGPartyMemberFullState    uint32 = 0x450203
	SMSGPartyMemberPartialState uint32 = 0x450202
	SMSGGroupDecline            uint32 = 0x45023B
	SMSGGroupUninvite           uint32 = 0x45023D
	SMSGGroupDestroyed          uint32 = 0x45023E
	SMSGGroupNewLeader          uint32 = 0x4500D6
	SMSGPartyCommandResult      uint32 = 0x450240
	SMSGReadyCheckStarted       uint32 = 0x450099
	SMSGReadyCheckResponse      uint32 = 0x45009A
	SMSGReadyCheckCompleted     uint32 = 0x45009B

	// Object and movement traffic of round 3 (Opcodes.h:1919-1998, 2185, 2174).
	SMSGUpdateObject             uint32 = 0x5C0000
	SMSGOnMonsterMove            uint32 = 0x5E0002
	SMSGMoveUpdate               uint32 = 0x5E000E
	SMSGMoveUpdateTeleport       uint32 = 0x5E000F
	SMSGMoveTeleport             uint32 = 0x5E0034
	SMSGMoveRoot                 uint32 = 0x5E0029
	SMSGMoveUnroot               uint32 = 0x5E002A
	SMSGNewWorld                 uint32 = 0x45002B
	SMSGTransferPending          uint32 = 0x450071
	SMSGQueryPlayerNamesResponse uint32 = 0x640027
	SMSGCreatureQueryResponse    uint32 = 0x490006

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
	SMSGClearCooldown        uint32 = 0x450166
	SMSGClearCooldowns       uint32 = 0x670015
	SMSGPowerUpdate          uint32 = 0x45017E
	SMSGSpellDelayed         uint32 = 0x670032
	SMSGLogXPGain            uint32 = 0x450190
	SMSGDeathReleaseLoc      uint32 = 0x45017F
	// The rest of the death flow (Opcodes.h:27, 228, 1948-1976, 377, 430, 1318): the graveyard, the corpse, a
	// resurrection offer, the spirit healer's menu and the movement state a ghost is given.
	SMSGResurrectRequest     uint32 = 0x450012
	SMSGCorpseLocation       uint32 = 0x4500F7
	SMSGCorpseTransportQuery uint32 = 0x4501BE
	SMSGCorpseReclaimDelay   uint32 = 0x4501F4
	SMSGPreRessurect         uint32 = 0x450211
	SMSGStandStateUpdate     uint32 = 0x4501C8
	SMSGGossipMessage        uint32 = 0x650018
	SMSGMoveSetRunSpeed      uint32 = 0x5E0020
	SMSGMoveSetSwimSpeed     uint32 = 0x5E0022
	SMSGMoveSetWaterWalk     uint32 = 0x5E002B
	SMSGMoveSetLandWalk      uint32 = 0x5E002E
	SMSGSendKnownSpells      uint32 = 0x670019
	SMSGLevelUpInfo          uint32 = 0x450194
	SMSGSpellExecuteLog      uint32 = 0x670033
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
	CMSGQueryPlayerNames:            "CMSG_QUERY_PLAYER_NAMES",
	CMSGQueryCreature:               "CMSG_QUERY_CREATURE",
	CMSGSuspendTokenResponse:        "CMSG_SUSPEND_TOKEN_RESPONSE",
	CMSGWorldPortResponse:           "CMSG_WORLD_PORT_RESPONSE",
	CMSGAreaTrigger:                 "CMSG_AREA_TRIGGER",

	CMSGChatMessageSay:                 "CMSG_CHAT_MESSAGE_SAY",
	CMSGChatMessageYell:                "CMSG_CHAT_MESSAGE_YELL",
	CMSGChatMessageParty:               "CMSG_CHAT_MESSAGE_PARTY",
	CMSGChatMessageWhisper:             "CMSG_CHAT_MESSAGE_WHISPER",
	CMSGChatMessageEmote:               "CMSG_CHAT_MESSAGE_EMOTE",
	CMSGChatAddonMessage:               "CMSG_CHAT_ADDON_MESSAGE",
	CMSGChatAddonMessageTargeted:       "CMSG_CHAT_ADDON_MESSAGE_TARGETED",
	CMSGChatRegisterAddonPrefixes:      "CMSG_CHAT_REGISTER_ADDON_PREFIXES",
	CMSGChatUnregisterAllAddonPrefixes: "CMSG_CHAT_UNREGISTER_ALL_ADDON_PREFIXES",
	CMSGPartyInvite:                    "CMSG_PARTY_INVITE",
	CMSGPartyInviteResponse:            "CMSG_PARTY_INVITE_RESPONSE",
	CMSGPartyUninvite:                  "CMSG_PARTY_UNINVITE",
	CMSGLeaveGroup:                     "CMSG_LEAVE_GROUP",
	CMSGRequestPartyMemberStats:        "CMSG_REQUEST_PARTY_MEMBER_STATS",
	CMSGSetSelection:                   "CMSG_SET_SELECTION",
	CMSGAttackSwing:                    "CMSG_ATTACK_SWING",
	CMSGAttackStop:                     "CMSG_ATTACK_STOP",
	CMSGCastSpell:                      "CMSG_CAST_SPELL",
	CMSGCancelCast:                     "CMSG_CANCEL_CAST",

	CMSGCreateCharacter:                "CMSG_CREATE_CHARACTER",
	CMSGCheckCharacterNameAvailability: "CMSG_CHECK_CHARACTER_NAME_AVAILABILITY",
	CMSGCharDelete:                     "CMSG_CHAR_DELETE",
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
