package wire

import "testing"

// The live realm is build 70707 (auth.realmlist.gamebuild) and accepts only its own build
// (Realm.AlsoAcceptBuilds is empty), so a client claims 70707 too. The 70938 column is what the protocol
// document describes; a client that claims 70938 on that realm would be refused at the realm join.
const (
	realmBuild70707  = uint32(70707)
	clientBuild70938 = uint32(70938)
)

// The expected wire numbers below are computed by hand from ClassicOpcodes.cpp at commit 6517af05 with the
// shipped defaults (Classic.OpcodeGroupShift = -1, Classic.MainGroupInsertIndex = 0x98,
// Classic.ClientMainGroupInsertIndex = 0x13B, Build70755.ClientNew = 0x440071,0x440072,
// Build70755.ServerNew = 0x470125, Build70790.ServerRemoved = 0x470125, Build70790.ServerNew = 0x4700ED) and
// the ClassicClientOpcodes.inc table. The arithmetic is spelled out per row:
//
//	70245 server number = (group+1)<<16 | index, with -1 for 0x45 index 0x17E..0x1C0, +2 for chat 0x4A
//	index >= 4, -1 for guild 0x51 index >= 0x1C, and the explicit overrides above
//	FROMLINE then: index >= 0x98 in group 0x46 gets +1, then every group 0x46..0x6B gets +1
//	70755/70790 (only for a 70790 client): a server number >= 0x470125 gets +1; then >= 0x4700ED gets a net
//	-1+1, i.e. +1, and the 0x470125 insert is taken out again
func TestServerOpcodeTranslation(t *testing.T) {
	cases := []struct {
		core           uint32
		name           string
		w70707, w70938 uint32
	}{
		// 70245 group+1 only: 0x4C -> 0x4D, then shift -> 0x4E
		{0x4C0000, "SMSG_AUTH_CHALLENGE", 0x4E0000, 0x4E0000},
		{0x4C0004, "SMSG_ENTER_ENCRYPTED_MODE", 0x4E0004, 0x4E0004},
		{0x4C0006, "SMSG_RESUME_COMMS", 0x4E0006, 0x4E0006},
		{0x4C0008, "SMSG_CONNECT_TO", 0x4E0008, 0x4E0008},
		{0x4C0009, "SMSG_PONG", 0x4E0009, 0x4E0009},
		{0x4C000E, "SMSG_COMPRESSED_PACKET", 0x4E000E, 0x4E000E},
		// 70245 0x46 index 0..0x97: group+1 -> 0x47, no index bump
		{0x450001, "SMSG_AUTH_RESPONSE", 0x470001, 0x470001},
		{0x450018, "SMSG_ENUM_CHARACTERS_RESULT", 0x470018, 0x470018},
		{0x45002F, "SMSG_LOGIN_VERIFY_WORLD", 0x47002F, 0x47002F},
		{0x450064, "SMSG_FEATURE_SYSTEM_STATUS_GLUE_SCREEN", 0x470064, 0x470064},
		// 70245 override 0x4601B5, index >= 0x98 -> 0x1B6, group+1 -> 0x4701B6; a 70938 client adds the
		// 70755 insert and cancels it with the 70790 remove+insert -> 0x4701B7
		{0x4501B6, "SMSG_ACCOUNT_DATA_TIMES", 0x4701B6, 0x4701B7},
		// 70245 0x460123 -> index 0x124 -> 0x470124; 70938 gets +1 from the 0x4700ED insert
		{0x450123, "SMSG_SET_TIME_ZONE_INFORMATION", 0x470124, 0x470125},
		{0x45012F, "SMSG_LOGOUT_RESPONSE", 0x470130, 0x470131},
		{0x450130, "SMSG_LOGOUT_COMPLETE", 0x470131, 0x470132},
		{0x450268, "SMSG_TUTORIAL_FLAGS", 0x470269, 0x47026A},
		{0x4502B1, "SMSG_BATTLE_NET_CONNECTION_STATUS", 0x4702B2, 0x4702B3},
		// 70245 0x4A index 1 (not chat) -> 0x4B0001
		{0x490001, "SMSG_AVAILABLE_HOTFIXES", 0x4B0001, 0x4B0001},
		{0x49000E, "SMSG_CACHE_VERSION", 0x4B000E, 0x4B000E},
		// 70245 0x5F -> 0x60
		{0x5E0000, "SMSG_TIME_SYNC_REQUEST", 0x600000, 0x600000},
		{0x5E0003, "SMSG_MOVE_SET_ACTIVE_MOVER", 0x600003, 0x600003},
		// R0 NPC answers: plain group arithmetic, no inserts in these groups.
		{0x650018, "SMSG_GOSSIP_MESSAGE", 0x670018, 0x670018},
		{0x650017, "SMSG_GOSSIP_COMPLETE", 0x670017, 0x670017},
		{0x650028, "SMSG_GOSSIP_OPTION_NPC_INTERACTION", 0x670028, 0x670028},
		{0x45031A, "SMSG_NPC_INTERACTION_OPEN_RESULT", 0x47031B, 0x47031C},
		{0x450362, "SMSG_RECENT_ALLY_DATA_RESPONSE", 0x470363, 0x470364},
		{0x45021E, "SMSG_BATTLE_PAY_GET_DISTRIBUTION_LIST_RESPONSE", 0x47021F, 0x470220},
		{0x450360, "SMSG_ACCOUNT_ITEM_COLLECTION_DATA", 0x470361, 0x470362},
		{0x45036F, "SMSG_MIRROR_VARS", 0x470372, 0x470373},
		// W4's mount answers: plain group arithmetic below index 0x98, no inserts in this group.
		{0x45000F, "SMSG_MOUNT_RESULT", 0x47000F, 0x47000F},
		{0x450010, "SMSG_DISMOUNT_RESULT", 0x470010, 0x470010},
		// R3 vendor answers: below 0x98 the group+1 is the whole change; above it the index is one higher too
		// (0x45005C -> 0x47005C; 0x450170 -> 0x470171; 0x450171 -> 0x470172; 0x450172 -> 0x470173), and a 70938
		// client gets the net +1 of the 70755/70790 inserts from 0x4700ED on.
		{0x45005C, "SMSG_VENDOR_INVENTORY", 0x47005C, 0x47005C},
		{0x450170, "SMSG_SELL_RESPONSE", 0x470171, 0x470172},
		{0x450171, "SMSG_BUY_SUCCEEDED", 0x470172, 0x470173},
		{0x450172, "SMSG_BUY_FAILED", 0x470173, 0x470174},
		// R5 quest answers: group 0x65 -> 0x67, no inserts; a 70755+ client has no entry in this group.
		{0x65001B, "SMSG_QUEST_GIVER_STATUS", 0x67001B, 0x67001B},
		{0x650012, "SMSG_QUEST_GIVER_QUEST_DETAILS", 0x670012, 0x670012},
		{0x650013, "SMSG_QUEST_GIVER_REQUEST_ITEMS", 0x670013, 0x670013},
		{0x650014, "SMSG_QUEST_GIVER_OFFER_REWARD_MESSAGE", 0x670014, 0x670014},
		{0x650003, "SMSG_QUEST_GIVER_QUEST_COMPLETE", 0x670003, 0x670003},
		{0x650005, "SMSG_QUEST_GIVER_INVALID_QUEST", 0x670005, 0x670005},
		{0x650006, "SMSG_QUEST_GIVER_QUEST_FAILED", 0x670006, 0x670006},
		{0x65000C, "SMSG_QUEST_UPDATE_ADD_CREDIT", 0x67000C, 0x67000C},
		{0x65000D, "SMSG_QUEST_UPDATE_ADD_CREDIT_SIMPLE", 0x67000D, 0x67000D},
		{0x650009, "SMSG_QUEST_UPDATE_COMPLETE", 0x670009, 0x670009},
		{0x650007, "SMSG_QUEST_LOG_FULL", 0x670007, 0x670007},
		{0x65001A, "SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE", 0x67001A, 0x67001A},
		// R4 trainer and R6 taxi answers: their 0x45 rows follow the same group and index arithmetic as the vendor's
		// (0x45018A -> 0x47018A, 0x450179 -> 0x47017A, 0x450128 -> 0x470129), and a 70938 client gets the net +1 of
		// the 70755/70790 inserts from 0x4700ED on. SMSG_LEARNED_SPELLS is in the 0x67 group: group+1, then the
		// shift.
		{0x45018A, "SMSG_TRAINER_LIST", 0x47018A, 0x47018B},
		{0x45018B, "SMSG_TRAINER_BUY_FAILED", 0x47018B, 0x47018C},
		{0x670040, "SMSG_LEARNED_SPELLS", 0x690040, 0x690040},
		{0x67001A, "SMSG_SEND_SPELL_HISTORY", 0x69001A, 0x69001A},
		// W-106 mail answers: group 0x45 -> 0x47, and the translation shifts the two 0xE4/0xE5 rows up by one on both
		// builds (measured, as the vendor rows above); the two list rows (0x200, 0x201) take the 70707 shift (+1)
		// and, at 70938, the second insert (+2).
		{0x4500E4, "SMSG_MAIL_COMMAND_RESULT", 0x4700E5, 0x4700E5},
		{0x4500E5, "SMSG_NOTIFY_RECEIVED_MAIL", 0x4700E6, 0x4700E6},
		{0x450200, "SMSG_MAIL_LIST_RESULT", 0x470201, 0x470202},
		{0x450201, "SMSG_MAIL_QUERY_NEXT_TIME_RESULT", 0x470202, 0x470203},
		// Part 2 of #106 auction answers: group 0x45 -> 0x47: the 0x19x rows keep their index on 70707 and gain one on
		// 70938, the 0x2Fx rows follow the mail list rows' shifts (+1, +2).
		{0x450199, "SMSG_AUCTION_HELLO_RESPONSE", 0x470199, 0x47019A},
		{0x45019B, "SMSG_AUCTION_COMMAND_RESULT", 0x47019B, 0x47019C},
		{0x45019C, "SMSG_AUCTION_WON_NOTIFICATION", 0x47019C, 0x47019D},
		{0x45019D, "SMSG_AUCTION_OUTBID_NOTIFICATION", 0x47019D, 0x47019E},
		{0x45019E, "SMSG_AUCTION_CLOSED_NOTIFICATION", 0x47019E, 0x47019F},
		{0x45019F, "SMSG_AUCTION_OWNER_BID_NOTIFICATION", 0x47019F, 0x4701A0},
		{0x4502F5, "SMSG_AUCTION_LIST_BUCKETS_RESULT", 0x4702F6, 0x4702F7},
		{0x4502F6, "SMSG_AUCTION_LIST_ITEMS_RESULT", 0x4702F7, 0x4702F8},
		{0x4502F8, "SMSG_AUCTION_LIST_OWNED_ITEMS_RESULT", 0x4702F9, 0x4702FA},
		{0x4502FA, "SMSG_AUCTION_GET_COMMODITY_QUOTE_RESULT", 0x4702FB, 0x4702FC},
		{0x450128, "SMSG_TAXI_NODE_STATUS", 0x470129, 0x47012A},
		{0x450129, "SMSG_ACTIVATE_TAXI_REPLY", 0x47012A, 0x47012B},
		{0x45012A, "SMSG_NEW_TAXI_PATH", 0x47012B, 0x47012C},
		{0x450179, "SMSG_SHOW_TAXI_NODES", 0x47017A, 0x47017B},
		// A1 BG answers: group 0x4B -> 0x4D with the group shift (no index bump: index 0x00-0x10 and 0x2E-0x30
		// are all below 0x98), and group 0x45 -> 0x47 likewise. Neither the 70755 nor the 70790 insert touches
		// these groups (the only 0x47 insert is at index 0xED/0x125), so both builds share the wire numbers.
		{0x4B0000, "SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION", 0x4D0000, 0x4D0000},
		{0x4B0001, "SMSG_BATTLEFIELD_STATUS_ACTIVE", 0x4D0001, 0x4D0001},
		{0x4B0002, "SMSG_BATTLEFIELD_STATUS_QUEUED", 0x4D0002, 0x4D0002},
		{0x4B0003, "SMSG_BATTLEFIELD_STATUS_NONE", 0x4D0003, 0x4D0003},
		{0x4B0004, "SMSG_BATTLEFIELD_STATUS_FAILED", 0x4D0004, 0x4D0004},
		{0x4B0005, "SMSG_BATTLEFIELD_LIST", 0x4D0005, 0x4D0005},
		{0x4B0006, "SMSG_BATTLEGROUND_PLAYER_POSITIONS", 0x4D0006, 0x4D0006},
		{0x4B0007, "SMSG_UPDATE_CAPTURE_POINT", 0x4D0007, 0x4D0007},
		{0x4B0008, "SMSG_CAPTURE_POINT_REMOVED", 0x4D0008, 0x4D0008},
		{0x4B0009, "SMSG_BATTLEGROUND_PLAYER_JOINED", 0x4D0009, 0x4D0009},
		{0x4B000A, "SMSG_BATTLEGROUND_PLAYER_LEFT", 0x4D000A, 0x4D000A},
		{0x4B0010, "SMSG_PVP_MATCH_STATISTICS", 0x4D0010, 0x4D0010},
		{0x4B002E, "SMSG_PVP_MATCH_SET_STATE", 0x4D002E, 0x4D002E},
		{0x4B002F, "SMSG_PVP_MATCH_COMPLETE", 0x4D002F, 0x4D002F},
		{0x4B0030, "SMSG_PVP_MATCH_INITIALIZE", 0x4D0030, 0x4D0030},
		{0x4501EA, "SMSG_AREA_SPIRIT_HEALER_TIME", 0x4701EB, 0x4701EC},
		{0x4501F0, "SMSG_INIT_WORLD_STATES", 0x4701F1, 0x4701F2},
		{0x4501F2, "SMSG_UPDATE_WORLD_STATE", 0x4701F3, 0x4701F4},
	}
	for _, c := range cases {
		if got := NewOpcodeTable(realmBuild70707, realmBuild70707).TranslateServerCore(c.core); got != c.w70707 {
			t.Errorf("%s (0x%06X) for a 70707 client: wire 0x%06X, want 0x%06X", c.name, c.core, got, c.w70707)
		}
		if got := NewOpcodeTable(clientBuild70938, realmBuild70707).TranslateServerCore(c.core); got != c.w70938 {
			t.Errorf("%s (0x%06X) for a 70938 client: wire 0x%06X, want 0x%06X", c.name, c.core, got, c.w70938)
		}
		// and the inverse: the wire number the server sent must name the core opcode again
		if got := NewOpcodeTable(realmBuild70707, realmBuild70707).TranslateServerWire(c.w70707); got != c.core {
			t.Errorf("wire 0x%06X for a 70707 client named 0x%06X, want %s 0x%06X", c.w70707, got, c.name, c.core)
		}
		if got := NewOpcodeTable(clientBuild70938, realmBuild70707).TranslateServerWire(c.w70938); got != c.core {
			t.Errorf("wire 0x%06X for a 70938 client named 0x%06X, want %s 0x%06X", c.w70938, got, c.name, c.core)
		}
		if ServerName(c.core) != c.name {
			t.Errorf("ServerName(0x%06X) = %q, want %q", c.core, ServerName(c.core), c.name)
		}
	}
}

// Classic has no SMSG_HEALTH_UPDATE and no SMSG_GUILD_CHALLENGE_COMPLETED (BlockedServerOpcodes,
// ClassicOpcodes.cpp:62-76). Each shares its Classic number with a message the server does send:
// SMSG_HEALTH_UPDATE (0x45017D, index 0x17D untouched) and SMSG_POWER_UPDATE (0x45017E, index -1) both land on
// 0x46017D before the shift and on 0x47017E after it (index >= 0x98 gets +1), and SMSG_GUILD_CHALLENGE_COMPLETED
// (0x51001B) and SMSG_GUILD_ITEM_LOOTED_NOTIFY (0x51001C, -1 from index 0x1C) both land on 0x52001B, 0x53001B
// with the shift. The wire -> core direction must name the sendable one.
func TestServerWirePrefersTheOpcodeTheServerSends(t *testing.T) {
	for _, tc := range []struct {
		clientBuild, realmBuild uint32
		wire, want              uint32
		name                    string
	}{
		{realmBuild70707, realmBuild70707, 0x47017E, 0x45017E, "SMSG_POWER_UPDATE"},
		{realmBuild70707, realmBuild70707, 0x53001B, 0x51001C, "SMSG_GUILD_ITEM_LOOTED_NOTIFY"},
		{clientBuild70938, realmBuild70707, 0x47017F, 0x45017E, "SMSG_POWER_UPDATE"},             // 70790 steps do not touch these
		{clientBuild70938, realmBuild70707, 0x53001B, 0x51001C, "SMSG_GUILD_ITEM_LOOTED_NOTIFY"}, //
		{realmBuild70707, 70245, 0x46017D, 0x45017E, "SMSG_POWER_UPDATE"},                        // no group shift
		{realmBuild70707, 70245, 0x52001B, 0x51001C, "SMSG_GUILD_ITEM_LOOTED_NOTIFY"},            //
	} {
		table := NewOpcodeTable(tc.clientBuild, tc.realmBuild)
		if got := table.TranslateServerWire(tc.wire); got != tc.want {
			t.Errorf("build %d on realm %d: wire 0x%06X named 0x%06X, want %s 0x%06X", tc.clientBuild, tc.realmBuild, tc.wire, got, tc.name, tc.want)
		}
		if got := ServerName(tc.want); got != tc.name {
			t.Errorf("ServerName(0x%06X) = %q, want %q", tc.want, got, tc.name)
		}
	}
	// the forward direction still maps both colliding cores to that number (the server drops the blocked one
	// before it writes a packet, WorldSocket.cpp:522)
	table := NewOpcodeTable(realmBuild70707, realmBuild70707)
	if got := table.TranslateServerCore(0x45017D); got != 0x47017E {
		t.Errorf("SMSG_HEALTH_UPDATE forward = 0x%06X, want 0x47017E", got)
	}
	if got := table.TranslateServerCore(0x51001B); got != 0x53001B {
		t.Errorf("SMSG_GUILD_CHALLENGE_COMPLETED forward = 0x%06X, want 0x53001B", got)
	}
}

// The inverse table follows the realm build's group shift like the forward one: a realm below 70291 keeps
// SMSG_AUTH_CHALLENGE on 0x4D0000, and the shifted number must not name it there.
func TestServerWireUsesTheRealmShift(t *testing.T) {
	noShift := NewOpcodeTable(realmBuild70707, 70245)
	if got := noShift.TranslateServerWire(0x4D0000); got != 0x4C0000 {
		t.Fatalf("no shift: wire 0x4D0000 named 0x%06X, want SMSG_AUTH_CHALLENGE", got)
	}
	if got := noShift.TranslateServerWire(0x4E0000); got != 0 {
		t.Fatalf("no shift: wire 0x4E0000 named 0x%06X, want no opcode", got)
	}
	shifted := NewOpcodeTable(realmBuild70707, realmBuild70707)
	if got := shifted.TranslateServerWire(0x4E0000); got != 0x4C0000 {
		t.Fatalf("shift 1: wire 0x4E0000 named 0x%06X, want SMSG_AUTH_CHALLENGE", got)
	}
	if got := shifted.TranslateServerCore(0x4C0000); got != 0x4E0000 {
		t.Fatalf("shift 1: SMSG_AUTH_CHALLENGE wire 0x%06X, want 0x4E0000", got)
	}
}

// TestClientOpcodeTranslation: the client direction, same hand computation. The 70755 client step inserts two
// messages at 0x440071/0x440072, so every later number in the client main group 0x44 is two higher for a 70938
// client; the connection group 0x45 is 0x46 in both (the shift), as the .inc table's 0x45xxxx entries show.
func TestClientOpcodeTranslation(t *testing.T) {
	cases := []struct {
		core           uint32
		name           string
		w70707, w70938 uint32
	}{
		// .inc 0x450001..0x45000A; the shift moves the connection group to 0x46
		{0x440001, "CMSG_AUTH_SESSION", 0x460001, 0x460001},
		{0x440003, "CMSG_AUTH_CONTINUED_SESSION", 0x460003, 0x460003},
		{0x440005, "CMSG_ENTER_ENCRYPTED_MODE_ACK", 0x460005, 0x460005},
		{0x440006, "CMSG_PING", 0x460006, 0x460006},
		{0x440007, "CMSG_LOG_DISCONNECT", 0x460007, 0x460007},
		{0x44000A, "CMSG_QUEUED_MESSAGES_END", 0x46000A, 0x46000A},
		// .inc 0x440014 -> 0x430014, 0x440016 -> 0x430016; index < 0x13B, no shift change
		{0x430014, "CMSG_ENUM_CHARACTERS", 0x440014, 0x440014},
		{0x430016, "CMSG_PLAYER_LOGIN", 0x440016, 0x440016},
		// arithmetic group+1: 0x43 -> 0x44
		{0x430024, "CMSG_LOADING_SCREEN_NOTIFY", 0x440024, 0x440024},
		{0x4300AB, "CMSG_KEEP_ALIVE", 0x4400AB, 0x4400AD},
		{0x430000, "CMSG_CONNECT_TO_FAILED", 0x440000, 0x440000},
		// .inc 0x420064/0x42005C -> 0x410064/0x41005C (groups below 0x44: no shift)
		{0x410064, "CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE", 0x420064, 0x420064},
		{0x41005C, "CMSG_TIME_SYNC_RESPONSE", 0x42005C, 0x42005C},
		// .inc 0x3F0074 -> 0x3E0072, a manual entry (the arithmetic would be 0x3F0073)
		{0x3E0072, "CMSG_LOGOUT_REQUEST", 0x3F0074, 0x3F0074},
		// R0 gossip and quest-hello rows: .inc region-2 0x3F0029/0x3F002D -> core 0x3E0027/0x3E002B; the
		// 70959 class keeps the same wire numbers (no client inserts in this group).
		{0x3E0027, "CMSG_TALK_TO_GOSSIP", 0x3F0029, 0x3F0029},
		{0x3E0029, "CMSG_GOSSIP_SELECT_OPTION", 0x3F002B, 0x3F002B},
		{0x3E002B, "CMSG_QUEST_GIVER_HELLO", 0x3F002D, 0x3F002D},
		{0x3E0028, "CMSG_CLOSE_INTERACTION", 0x3F002A, 0x3F002A},
		// R3 vendor and R5 quest rows in the same 0x3E group: the .inc region-2 table lists each one, so the wire
		// number is core+2 (0x3E0036 -> 0x3F0038, 0x3E0087 -> 0x3F0089, 0x3E00EC -> 0x3F00EF, ...), unchanged at
		// 70938 (no client insert in this group).
		{0x3E0036, "CMSG_LIST_INVENTORY", 0x3F0038, 0x3F0038},
		{0x3E0037, "CMSG_SELL_ITEM", 0x3F0039, 0x3F0039},
		{0x3E0038, "CMSG_SELL_ALL_JUNK_ITEMS", 0x3F003A, 0x3F003A},
		{0x3E0039, "CMSG_BUY_ITEM", 0x3F003B, 0x3F003B},
		{0x3E003A, "CMSG_BUY_BACK_ITEM", 0x3F003C, 0x3F003C},
		{0x3E0087, "CMSG_REPAIR_ITEM", 0x3F0089, 0x3F0089},
		{0x3E0031, "CMSG_QUEST_GIVER_STATUS_QUERY", 0x3F0033, 0x3F0033},
		{0x3E002C, "CMSG_QUEST_GIVER_QUERY_QUEST", 0x3F002E, 0x3F002E},
		{0x3E002D, "CMSG_QUEST_GIVER_ACCEPT_QUEST", 0x3F002F, 0x3F002F},
		{0x3E002E, "CMSG_QUEST_GIVER_COMPLETE_QUEST", 0x3F0030, 0x3F0030},
		{0x3E0030, "CMSG_QUEST_GIVER_REQUEST_REWARD", 0x3F0032, 0x3F0032},
		{0x3E002F, "CMSG_QUEST_GIVER_CHOOSE_REWARD", 0x3F0031, 0x3F0031},
		{0x3E00EC, "CMSG_QUEST_GIVER_CLOSE_QUEST", 0x3F00EF, 0x3F00EF},
		{0x3E00D0, "CMSG_QUEST_LOG_REMOVE_QUEST", 0x3F00D3, 0x3F00D3},
		// R4 trainer and R6 taxi rows in the same 0x3E group, listed by the .inc region-2 table: wire = core+2,
		// unchanged at 70938.
		{0x3E0043, "CMSG_TRAINER_LIST", 0x3F0045, 0x3F0045},
		{0x3E0044, "CMSG_TRAINER_BUY_SPELL", 0x3F0046, 0x3F0046},
		{0x3E003E, "CMSG_TAXI_NODE_STATUS_QUERY", 0x3F0040, 0x3F0040},
		{0x3E003F, "CMSG_ENABLE_TAXI_NODE", 0x3F0041, 0x3F0041},
		{0x3E0040, "CMSG_TAXI_QUERY_AVAILABLE_NODES", 0x3F0042, 0x3F0042},
		{0x3E0041, "CMSG_ACTIVATE_TAXI", 0x3F0043, 0x3F0043},
		// W-106 mail rows: the .inc region-2 table lists the five 0x3E rows (wire = core+3 at 0x3E00D8..0x3E00DC), the
		// delete is a manual region-1 entry (0x3D00E7 -> 0x3E00E9), CMSG_SEND_MAIL is in the main group below the
		// 70755 inserts, and the return-to-sender row is plain group arithmetic past them (+2 from 70755).
		{0x3E00D8, "CMSG_MAIL_GET_LIST", 0x3F00DB, 0x3F00DB},
		{0x3E00D9, "CMSG_MAIL_TAKE_MONEY", 0x3F00DC, 0x3F00DC},
		{0x3E00DA, "CMSG_MAIL_TAKE_ITEM", 0x3F00DD, 0x3F00DD},
		{0x3E00DB, "CMSG_QUERY_NEXT_MAIL_TIME", 0x3F00DE, 0x3F00DE},
		{0x3E00DC, "CMSG_MAIL_MARK_AS_READ", 0x3F00DF, 0x3F00DF},
		{0x3D00E7, "CMSG_MAIL_DELETE", 0x3E00E9, 0x3E00E9},
		{0x430026, "CMSG_SEND_MAIL", 0x440026, 0x440026},
		{0x430081, "CMSG_MAIL_RETURN_TO_SENDER", 0x440081, 0x440083},
		// Part 2 of #106, the auction house: the .inc region-2 table lists each row (wire = core+2 at 0x3E005F..0x3E006D),
		// unchanged at 70938 (no client insert in this group).
		{0x3E005F, "CMSG_AUCTION_HELLO_REQUEST", 0x3F0061, 0x3F0061},
		{0x3E0060, "CMSG_AUCTION_SELL_ITEM", 0x3F0062, 0x3F0062},
		{0x3E0061, "CMSG_AUCTION_REMOVE_ITEM", 0x3F0063, 0x3F0063},
		{0x3E0063, "CMSG_AUCTION_PLACE_BID", 0x3F0065, 0x3F0065},
		{0x3E0064, "CMSG_AUCTION_BROWSE_QUERY", 0x3F0066, 0x3F0066},
		{0x3E0065, "CMSG_AUCTION_LIST_ITEMS_BY_BUCKET_KEY", 0x3F0067, 0x3F0067},
		{0x3E0067, "CMSG_AUCTION_LIST_OWNED_ITEMS", 0x3F0069, 0x3F0069},
		{0x3E006A, "CMSG_AUCTION_GET_COMMODITY_QUOTE", 0x3F006C, 0x3F006C},
		{0x3E006B, "CMSG_AUCTION_CONFIRM_COMMODITIES_PURCHASE", 0x3F006D, 0x3F006D},
		{0x3E006D, "CMSG_AUCTION_SELL_COMMODITY", 0x3F006F, 0x3F006F},
		// the two 70755 inserts: base 0x440070 unchanged, 0x440071 -> 0x440073, 0x440072 -> 0x440074
		{0x430070, "CMSG_CREATE_CHARACTER", 0x440070, 0x440070},
		{0x430071, "CMSG_CHECK_CHARACTER_NAME_AVAILABILITY", 0x440071, 0x440073},
		{0x43000A, "CMSG_START_WAR_GAME", 0x44000A, 0x44000A},
		{0x4300EA, "CMSG_BATTLE_PAY_GET_PURCHASE_LIST", 0x4400EA, 0x4400EC},
		{0x43010F, "CMSG_GET_UNDELETE_CHARACTER_COOLDOWN_STATUS", 0x44010F, 0x440111},
		// W4's dismount: .inc region-2 0x3F0155 -> core 0x3D0153, the same number for both builds.
		{0x3D0153, "CMSG_CANCEL_MOUNT_AURA", 0x3E0155, 0x3E0155},
		// A1 BG sends in the same 0x3E region-2 group: the .inc table lists each one (0x3E0047 -> 0x3F0049,
		// 0x3E00BD -> 0x3F00C0, 0x3E00C7 -> 0x3F00CA, 0x3E0089 -> 0x3F008B), unchanged at 70938 (no client
		// insert in this group); the 0x3D region-1 pair is identity arithmetic in that group.
		{0x3E0047, "CMSG_AREA_SPIRIT_HEALER_QUEUE", 0x3F0049, 0x3F0049},
		{0x3E00BD, "CMSG_BATTLEMASTER_JOIN", 0x3F00C0, 0x3F00C0},
		{0x3E00C7, "CMSG_BATTLEFIELD_PORT", 0x3F00CA, 0x3F00CA},
		{0x3D001F, "CMSG_BATTLEFIELD_LEAVE", 0x3E0020, 0x3E0020},
		{0x3D0028, "CMSG_PVP_LOG_DATA", 0x3E0029, 0x3E0029},
		{0x430008, "CMSG_REQUEST_BATTLEFIELD_STATUS", 0x440008, 0x440008},
		// main-group insert: classic index >= 0x13B is one higher on the wire
		{0x430134, "CMSG_CAN_REDEEM_TOKEN_FOR_BALANCE", 0x440133, 0x440135},
		{0x430176, "CMSG_SOCIAL_CONTRACT_REQUEST", 0x440177, 0x440179},
	}
	for _, c := range cases {
		for _, tc := range []struct {
			build uint32
			want  uint32
		}{{realmBuild70707, c.w70707}, {clientBuild70938, c.w70938}} {
			table := NewOpcodeTable(tc.build, realmBuild70707)
			got, ok := table.WireForClient(c.core)
			if !ok {
				t.Errorf("%s (0x%06X) for build %d: no wire number", c.name, c.core, tc.build)
				continue
			}
			if got != tc.want {
				t.Errorf("%s (0x%06X) for build %d: wire 0x%06X, want 0x%06X", c.name, c.core, tc.build, got, tc.want)
			}
			if back := table.TranslateClientWire(got); back != c.core {
				t.Errorf("wire 0x%06X for build %d named 0x%06X, want %s 0x%06X", got, tc.build, back, c.name, c.core)
			}
		}
	}
}

// TestGuildOpcodesTranslate: the guild invite flow's six opcodes are the core's numbers and survive the translation
// in both directions on the pinned build. A wrong constant is caught by the name, which is keyed by the core number.
func TestGuildOpcodesTranslate(t *testing.T) {
	table := NewOpcodeTable(realmBuild70707, realmBuild70707)
	for _, c := range []struct {
		core uint32
		name string
	}{
		{SMSGGuildInvite, "SMSG_GUILD_INVITE"},
		{SMSGGuildEventPlayerJoined, "SMSG_GUILD_EVENT_PLAYER_JOINED"},
		{SMSGGuildCommandResult, "SMSG_GUILD_COMMAND_RESULT"},
	} {
		if got := ServerName(c.core); got != c.name {
			t.Errorf("ServerName(0x%06X) = %q, want %q", c.core, got, c.name)
		}
		w := table.TranslateServerCore(c.core)
		if w == UnknownOpcode {
			t.Errorf("%s has no wire number", c.name)
			continue
		}
		if got := table.TranslateServerWire(w); got != c.core {
			t.Errorf("%s: wire 0x%06X named 0x%06X, want 0x%06X", c.name, w, got, c.core)
		}
	}
	for _, c := range []struct {
		core uint32
		name string
	}{
		{CMSGGuildAcceptInvite, "CMSG_ACCEPT_GUILD_INVITE"},
		{CMSGGuildDeclineInvitation, "CMSG_GUILD_DECLINE_INVITATION"},
		{CMSGGuildLeave, "CMSG_GUILD_LEAVE"},
	} {
		if got := ClientName(c.core); got != c.name {
			t.Errorf("ClientName(0x%06X) = %q, want %q", c.core, got, c.name)
		}
		w, ok := table.WireForClient(c.core)
		if !ok {
			t.Errorf("%s has no wire number", c.name)
			continue
		}
		if got := table.TranslateClientWire(w); got != c.core {
			t.Errorf("client wire 0x%06X named 0x%06X, want %s", w, got, c.name)
		}
	}
}

// TestPetOpcodesTranslate: the seven pet opcodes of round P1 are in the core's tables and survive the translation
// in both directions. The three client packets are explicit entries of the client's own table (0x3F0021, 0x3F0022
// and 0x3F0026), not the plain group arithmetic, so a body is pinned to the number the real client sends.
func TestPetOpcodesTranslate(t *testing.T) {
	table := NewOpcodeTable(realmBuild70707, realmBuild70707)
	for _, c := range []struct {
		core uint32
		name string
	}{
		{SMSGPetSpellsMessage, "SMSG_PET_SPELLS_MESSAGE"},
		{SMSGPetMode, "SMSG_PET_MODE"},
		{SMSGPetCastFailed, "SMSG_PET_CAST_FAILED"},
		{SMSGPetActionFeedback, "SMSG_PET_ACTION_FEEDBACK"},
	} {
		if got := ServerName(c.core); got != c.name {
			t.Errorf("ServerName(0x%06X) = %q, want %q", c.core, got, c.name)
		}
		w := table.TranslateServerCore(c.core)
		if w == UnknownOpcode {
			t.Errorf("%s has no wire number", c.name)
			continue
		}
		if got := table.TranslateServerWire(w); got != c.core {
			t.Errorf("%s: wire 0x%06X named 0x%06X, want 0x%06X", c.name, w, got, c.core)
		}
	}
	for _, c := range []struct {
		core uint32
		name string
		wire uint32
	}{
		{CMSGPetAction, "CMSG_PET_ACTION", 0x3F0021},
		{CMSGPetStopAttack, "CMSG_PET_STOP_ATTACK", 0x3F0022},
		{CMSGPetSpellAutocast, "CMSG_PET_SPELL_AUTOCAST", 0x3F0026},
	} {
		if got := ClientName(c.core); got != c.name {
			t.Errorf("ClientName(0x%06X) = %q, want %q", c.core, got, c.name)
		}
		w, ok := table.WireForClient(c.core)
		if !ok {
			t.Errorf("%s has no wire number", c.name)
			continue
		}
		if w != c.wire {
			t.Errorf("%s wire 0x%06X, want the client table's 0x%06X", c.name, w, c.wire)
		}
		if got := table.TranslateClientWire(w); got != c.core {
			t.Errorf("client wire 0x%06X named 0x%06X, want %s", w, got, c.name)
		}
	}
}

// TestShiftFromRealmBuild: the group shift follows the realm's build, not the client's (ClassicOpcodes.cpp
// ExtraGroupShift reads sRealmList->GetCurrentRealm()->Build). A realm below 70291 keeps the 70245 groups.
func TestShiftFromRealmBuild(t *testing.T) {
	old := NewOpcodeTable(realmBuild70707, 70245)
	if got := old.TranslateServerCore(0x4C0000); got != 0x4D0000 {
		t.Fatalf("no shift: SMSG_AUTH_CHALLENGE wire 0x%06X, want 0x4D0000", got)
	}
	if got := old.TranslateClientWire(0x450001); got != 0x440001 {
		t.Fatalf("no shift: wire 0x450001 named 0x%06X, want 0x440001", got)
	}
	if _, ok := old.WireForClient(0x440001); !ok {
		t.Fatal("no shift: CMSG_AUTH_SESSION has no wire number")
	} else if wire, _ := old.WireForClient(0x440001); wire != 0x450001 {
		t.Fatalf("no shift: CMSG_AUTH_SESSION wire 0x%06X, want 0x450001", wire)
	}
}

// TestClientWireUnknownNames have no core counterpart: the 70755 client messages at 0x440071/0x440072.
func TestClientWireUnknownNames(t *testing.T) {
	table := NewOpcodeTable(clientBuild70938, realmBuild70707)
	for _, wire := range []uint32{0x440071, 0x440072} {
		if got := table.TranslateClientWire(wire); got != UnknownOpcode {
			t.Errorf("70938 wire 0x%06X named 0x%06X, want UNKNOWN_OPCODE", wire, got)
		}
	}
}

// TestGainsOpcodesTranslate: every opcode the core-gains round uses is in the core's tables and survives the
// translation in both directions for our client's build.
func TestGainsOpcodesTranslate(t *testing.T) {
	table := NewOpcodeTable(realmBuild70707, realmBuild70707)
	servers := []struct {
		core uint32
		name string
	}{
		{0x450186, "SMSG_THREAT_UPDATE"},
		{0x450185, "SMSG_HIGHEST_THREAT_UPDATE"},
		{0x450187, "SMSG_THREAT_REMOVE"},
		{0x450188, "SMSG_THREAT_CLEAR"},
		{0x450228, "SMSG_ENCOUNTER_START"},
		{0x450229, "SMSG_ENCOUNTER_END"},
		{0x4B002B, "SMSG_BOSS_KILL"},
		{0x67000D, "SMSG_SPELL_INTERRUPT_LOG"},
		{0x670007, "SMSG_SPELL_DISPELL_LOG"},
		{0x670034, "SMSG_SPELL_MISS_LOG"},
		{0x670021, "SMSG_SPELL_INSTAKILL_LOG"},
		{0x67000E, "SMSG_ENVIRONMENTAL_DAMAGE_LOG"},
		{0x67000C, "SMSG_SPELL_ABSORB_LOG"},
		{0x450205, "SMSG_PROC_RESIST"},
		{0x67001D, "SMSG_SPELL_OR_DAMAGE_IMMUNE"},
	}
	for _, c := range servers {
		if got := ServerName(c.core); got != c.name {
			t.Errorf("ServerName(0x%06X) = %q, want %q", c.core, got, c.name)
		}
		w := table.TranslateServerCore(c.core)
		if w == UnknownOpcode {
			t.Errorf("%s has no wire number", c.name)
			continue
		}
		if got := table.TranslateServerWire(w); got != c.core {
			t.Errorf("%s: wire 0x%06X named 0x%06X, want 0x%06X", c.name, w, got, c.core)
		}
	}
	if got := ClientName(CMSGSetAdvancedCombatLogging); got != "CMSG_SET_ADVANCED_COMBAT_LOGGING" {
		t.Errorf("ClientName = %q", got)
	}
	w, ok := table.WireForClient(CMSGSetAdvancedCombatLogging)
	if !ok || w == UnknownOpcode {
		t.Fatal("CMSG_SET_ADVANCED_COMBAT_LOGGING has no wire number")
	}
	if got := table.TranslateClientWire(w); got != CMSGSetAdvancedCombatLogging {
		t.Errorf("client wire 0x%06X named 0x%06X", w, got)
	}
}
