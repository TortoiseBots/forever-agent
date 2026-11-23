package world

// The round-5 fake-server scenarios: chat-check's say/whisper/emote/notification exchange and party-check's
// invite or invite/accept/update/leave flow. The server packets are built here from the server's writers
// (ChatPackets.cpp, PartyPackets.cpp); the client packets are parsed here with the server's readers.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The wire numbers of the round-5 packets for build 70707, computed from the core's opcode table (the same
// hand-computation as the world notes).
const (
	fakeCMSGChatSay          = 0x2D0023
	fakeCMSGChatParty        = 0x2D0026
	fakeCMSGChatWhisper      = 0x2D0008
	fakeCMSGChatEmote        = 0x2D0024
	fakeCMSGPartyInvite      = 0x440030
	fakeCMSGPartyInviteReply = 0x440032
	fakeCMSGPartyUninvite    = 0x440073
	fakeCMSGLeaveGroup       = 0x440075
	fakeCMSGRequestStats     = 0x44007F

	fakeSMSGChat           = 0x4C0001
	fakeSMSGPrintNotify    = 0x47006E
	fakeSMSGPlayerNotFound = 0x4C000F
	fakeSMSGChatEmote      = 0x470274
	fakeSMSGPartyInvite    = 0x470061
	fakeSMSGPartyUpdate    = 0x470097
	fakeSMSGMemberFull     = 0x470204
	fakeSMSGPartyResult    = 0x470241
	fakeSMSGGroupDestroyed = 0x47023F
	fakeSMSGGroupDecline   = 0x47023C
	fakeSMSGGroupUninvite  = 0x47023E
)

// buildServerChat writes SMSG_CHAT the way Chat::Write does (ChatPackets.cpp:196-238).
func buildServerChat(typ uint8, lang uint32, sender, target wire.GUID, senderName, targetName, prefix, channel, text string) []byte {
	b := wire.NewWriter()
	b.WriteU8(typ)
	b.WriteU32(lang)
	wire.WritePackedGUID(b, sender)
	wire.WritePackedGUID(b, wire.EmptyGUID) // guild
	wire.WritePackedGUID(b, wire.EmptyGUID) // wow account
	wire.WritePackedGUID(b, target)
	b.WriteU32(0) // target virtual realm
	b.WriteU32(4242)
	b.WriteI32(0) // achievement
	b.WriteU32(0) // flags
	b.WriteFloat32(0)
	b.WriteI32(0) // spell
	b.WriteBits(uint64(len(senderName)), 11)
	b.WriteBits(uint64(len(targetName)), 11)
	b.WriteBits(uint64(len(prefix)), 5)
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBits(uint64(len(text)), 12)
	b.WriteBit(false) // hide
	b.WriteBit(false) // fake sender
	b.WriteBit(false) // broadcast text
	b.WriteBit(false) // channel guid
	b.WriteBit(false) // encounter event
	b.FlushBits()
	b.WriteString(senderName)
	b.WriteString(targetName)
	b.WriteString(prefix)
	b.WriteString(channel)
	b.WriteString(text)
	return b.Bytes()
}

// parseClientChatMessage reads CMSG_CHAT_MESSAGE_SAY/PARTY (ChatPackets.cpp:26-43).
func parseClientChatMessage(p []byte) (lang uint32, secure bool, text string, err error) {
	b := wire.NewReader(p)
	lang = b.ReadU32()
	n := int(b.ReadBits(11))
	secure = b.ReadBit()
	b.FlushBits()
	text = b.ReadString(n)
	if b.Err() != nil || b.Remaining() != 0 {
		return 0, false, "", fmt.Errorf("chat message: %d byte(s) left (%v)", b.Remaining(), b.Err())
	}
	return lang, secure, text, nil
}

// parseClientWhisper reads CMSG_CHAT_MESSAGE_WHISPER (ChatPackets.cpp:45-60).
func parseClientWhisper(p []byte) (target, text string, err error) {
	b := wire.NewReader(p)
	b.ReadU32() // language
	wire.ReadPackedGUID(b)
	b.ReadU32() // target virtual realm
	n := int(b.ReadBits(9))
	second := int(b.ReadBits(9))
	m := int(b.ReadBits(11))
	b.FlushBits()
	target = b.ReadString(n - 1)
	b.ReadU8()
	if second > 1 {
		b.ReadString(second - 1)
		b.ReadU8()
	}
	text = b.ReadString(m - 1)
	b.ReadU8()
	if b.Err() != nil || b.Remaining() != 0 {
		return "", "", fmt.Errorf("whisper: %d byte(s) left (%v)", b.Remaining(), b.Err())
	}
	return target, text, nil
}

// parseClientEmote reads CMSG_CHAT_MESSAGE_EMOTE (ChatPackets.cpp:126-132).
func parseClientEmote(p []byte) (string, error) {
	b := wire.NewReader(p)
	n := int(b.ReadBits(11))
	b.FlushBits()
	text := b.ReadString(n)
	if b.Err() != nil || b.Remaining() != 0 {
		return "", fmt.Errorf("emote: %d byte(s) left", b.Remaining())
	}
	return text, nil
}

// fakeLogout answers the logout like the normal scenario.
func (f *fakeServer) fakeLogout(c, realm *fakeConn) {
	c.writeFrame(fakeSMSGLogoutResponse, []byte{0, 0, 0, 0, 0})
	time.Sleep(20 * time.Millisecond)
	realm.writeFrame(fakeSMSGLogoutComplete, []byte{0})
	f.logoutOnce.Do(func() { close(f.logoutDone) })
	time.Sleep(20 * time.Millisecond)
	c.nc.Close()
}

// serveChat runs chat-check's exchange.
func (f *fakeServer) serveChat(c, realm *fakeConn) {
	c.writeFrame(fakeSMSGPrintNotify, func() []byte {
		b := wire.NewWriter()
		b.WriteBits(uint64(len("SENTINEL-NOTIFY")), 12)
		b.FlushBits()
		b.WriteString("SENTINEL-NOTIFY")
		return b.Bytes()
	}())
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "SENTINEL-SYSTEM"))
	said := 0
	for {
		wireNum, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() {
				return // the test's own drop: the client must notice it, the fake must not call it a failure
			}
			f.failf("chat: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNum)
		f.mu.Unlock()
		switch wireNum {
		case fakeCMSGTimeSyncResponse:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGMoveInitActiveMover:
		case fakeCMSGSetAdvancedCombatLogging:
			f.checkAdvancedLogging(payload)
		case fakeCMSGChatSay, fakeCMSGChatParty:
			lang, secure, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("chat: SAY: %v", err)
				return
			}
			if lang != chat.LanguageCommon {
				f.failf("chat: SAY language %d, want Common", lang)
			}
			if wireNum == fakeCMSGChatSay && !secure {
				f.failf("chat: SAY has IsSecure clear; the captured client sets it")
			}
			f.mu.Lock()
			if wireNum == fakeCMSGChatSay {
				f.chatSay = text
			} else {
				f.chatPartyText = text
			}
			f.mu.Unlock()
			// Say is echoed to the sender (Player::SendChatMessageToSetInRange, Player.cpp:23647-23658).
			typ := chat.TypeSay
			if wireNum == fakeCMSGChatParty {
				typ = chat.TypeParty
			}
			c.writeFrame(fakeSMSGChat, buildServerChat(typ, lang, mustGUID(f.clientGUID), wire.EmptyGUID, "Testchar", "", "", "", text))
			said++
		case fakeCMSGChatWhisper:
			target, text, err := parseClientWhisper(payload)
			if err != nil {
				f.failf("chat: WHISPER: %v", err)
				return
			}
			f.mu.Lock()
			f.chatWhisperTarget, f.chatWhisperText = target, text
			f.mu.Unlock()
			if target == "Nobody" {
				b := wire.NewWriter()
				b.WriteBits(uint64(len(target)), 9)
				b.FlushBits()
				b.WriteString(target)
				c.writeFrame(fakeSMSGPlayerNotFound, b.Bytes())
				continue
			}
			// A whisper to a connected player: the WHISPER packet to the target and WHISPER_INFORM to the sender
			// (Player::Whisper, Player.cpp:23706-23731). The fake plays both to the one client.
			c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, chat.LanguageUniversal, mustGUID(f.clientGUID), wire.EmptyGUID, target, target, "", "", text))
			c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisperInform, chat.LanguageUniversal, mustGUID(f.clientGUID), wire.EmptyGUID, target, target, "", "", text))
		case fakeCMSGChatEmote:
			text, err := parseClientEmote(payload)
			if err != nil {
				f.failf("chat: EMOTE: %v", err)
				return
			}
			f.mu.Lock()
			f.chatEmote = text
			f.mu.Unlock()
			c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeEmote, chat.LanguageUniversal, mustGUID(f.clientGUID), wire.EmptyGUID, "Testchar", "", "", "", text))
		case fakeCMSGLogoutRequest:
			if said == 0 {
				f.failf("chat: logout before any chat line")
			}
			f.fakeLogout(c, realm)
			return
		default:
			f.failf("chat: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// fakePartyUpdate writes SMSG_PARTY_UPDATE for this build (PartyPackets.cpp:584-627) with no optional blocks.
func fakePartyUpdate(partyGUID, leader, self wire.GUID, members []wire.GUID, names []string, destroyed bool) []byte {
	return fakePartyUpdateFull(partyGUID, leader, self, members, names, nil, nil, nil, false, false, destroyed)
}

// fakePartyUpdateFull is fakePartyUpdate with the per-member flags and subgroups (both parallel members, nil leaves
// every member at 0: the assistant bit is 0x01 and the subgroup is the 0-based wire value), a raid flags word, the
// optional loot block (PartyPackets.cpp:565-577) and allOnline: every member's Connected bit, where fakePartyUpdate
// sets only the first one's.
func fakePartyUpdateFull(partyGUID, leader, self wire.GUID, members []wire.GUID, names []string, memberFlags, subgroups []uint8, loot *party.LootSettings, raid, allOnline, destroyed bool) []byte {
	b := wire.NewWriter()
	flags := uint16(0)
	if raid {
		flags |= party.FlagRaid
	}
	typ := uint8(party.GroupTypeNormal)
	// MyIndex is the slot the receiving player has in the list, -1 when it has none: Group::SendUpdateToPlayer
	// sets it from the member slot it was handed (Group.cpp:862-875), and the model now requires the member at
	// MyIndex to be this character.
	myIndex := int32(-1)
	list := members
	if destroyed {
		flags = party.FlagDestroyed
		typ = party.GroupTypeNone
		myIndex = -1
		list = nil
	}
	for i, g := range list {
		if g == self {
			myIndex = int32(i)
			break
		}
	}
	b.WriteU16(flags)
	b.WriteU8(0) // party index
	b.WriteU8(typ)
	b.WriteI32(myIndex)
	wire.WritePackedGUID(b, partyGUID)
	b.WriteU32(1) // sequence
	b.WriteU8(0)  // 70291 byte
	wire.WritePackedGUID(b, leader)
	b.WriteU8(1)  // leader faction group
	b.WriteI32(0) // ping restriction
	b.WriteU64(0) // 70291 member-count prefix
	b.WriteU32(uint32(len(list)))
	for i, g := range list {
		b.WriteBits(uint64(len(names[i])), 6)
		b.WriteBits(1, 9) // empty voice id
		b.WriteBit(i == 0 || allOnline)
		b.WriteBit(false)
		b.WriteBit(false)
		wire.WritePackedGUID(b, g)
		mflags := uint8(0)
		if i < len(memberFlags) {
			mflags = memberFlags[i]
		}
		sub := uint8(0)
		if i < len(subgroups) {
			sub = subgroups[i]
		}
		b.WriteU8(sub)                          // subgroup
		b.WriteU8(mflags)                       // flags (MEMBER_FLAG_ASSISTANT is 0x01)
		b.WriteU8(0)                            // roles assigned
		b.WriteU8(0)                            // roles unknown
		b.WriteU8(1)                            // class
		b.WriteU8(1)                            // faction group
		wire.WritePackedGUID(b, wire.EmptyGUID) // leaver account
		b.WriteU32(0)                           // leave score
		b.WriteU32(0)                           // season
		b.WriteU32(0)                           // leaves
		b.WriteU32(0)                           // successes
		b.WriteI32(0)                           // consecutive
		b.WriteU64(0)                           // last penalty
		b.WriteU64(0)                           // expiration
		b.WriteI32(0)                           // flags
		b.WriteBit(false)                       // leaver status
		b.FlushBits()
		b.WriteString(names[i])
	}
	b.WriteBit(false) // challenge mode
	b.WriteBit(false) // lfg
	b.WriteBit(loot != nil)
	b.WriteBit(false) // difficulty
	b.FlushBits()
	if loot != nil {
		b.WriteU8(uint8(loot.Method))
		wire.WritePackedGUID(b, loot.Master)
		b.WriteU8(loot.Threshold)
	}
	return b.Bytes()
}

// fakeMemberState writes SMSG_PARTY_MEMBER_FULL_STATE (PartyPackets.cpp:241-288) with no auras, phases, chromie
// data, score or pet.
func fakeMemberState(guid wire.GUID, hp, maxHP, power, maxPower, level int) []byte {
	b := wire.NewWriter()
	b.WriteBit(false) // ForEnemy
	b.FlushBits()
	wire.WritePackedGUID(b, guid)
	b.WriteU8(0) // party type
	b.WriteU8(0)
	b.WriteU32(party.StatusOnline)
	b.WriteU8(0)  // power type
	b.WriteU16(0) // power display
	b.WriteU32(uint32(hp))
	b.WriteU32(uint32(maxHP))
	b.WriteU16(uint16(power))
	b.WriteU16(uint16(maxPower))
	b.WriteU16(uint16(level))
	b.WriteU16(0)    // spec
	b.WriteU16(12)   // zone
	b.WriteU16(0)    // wmo group
	b.WriteU32(0)    // wmo doodad
	b.WriteU16(1000) // x
	b.WriteU16(2000) // y
	b.WriteU16(50)   // z
	b.WriteU32(0)    // vehicle seat
	b.WriteU32(0)    // aura count
	b.WriteU32(0)    // phase flags
	b.WriteU32(0)    // phase count
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0) // conditional flags
	b.WriteU8(0)  // chromie faction
	b.WriteU32(0) // chromie mask
	b.WriteU32(0) // overall score
	b.WriteU32(0) // ladder score
	b.WriteU32(0) // score runs
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// fakePartyInvite writes SMSG_PARTY_INVITE (PartyPackets.cpp:82-107).
func fakePartyInvite(name string, inviter wire.GUID, canAccept bool) []byte {
	b := wire.NewWriter()
	b.WriteBit(canAccept) // CanAccept
	b.WriteBit(false)     // IsXRealm
	b.WriteBit(false)     // IsXNativeRealm
	b.WriteBit(false)     // ShouldSquelch
	b.WriteBit(false)     // AllowMultipleRoles
	b.WriteBit(false)     // QuestSessionActive
	b.WriteBits(uint64(len(name)), 6)
	b.WriteBits(0, 9) // surname
	b.WriteBit(false) // cross faction
	wire.WritePackedGUID(b, inviter)
	wire.WritePackedGUID(b, wire.EmptyGUID) // bnet account
	b.WriteU16(0)                           // cfg realm
	b.WriteU32(0)                           // realm address
	b.WriteBit(false)                       // realm IsLocal
	b.WriteBit(false)                       // realm IsInternal
	b.WriteBits(0, 8)
	b.WriteBits(0, 8)
	b.FlushBits()
	b.WriteU8(0)  // proposed roles
	b.WriteU32(0) // lfg slots
	b.WriteU32(0) // lfg completed mask
	b.WriteString(name)
	return b.Bytes()
}

// fakePartyResult writes SMSG_PARTY_COMMAND_RESULT (PartyPackets.cpp:34-48).
func fakePartyResult(operation, result uint8, name string) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(name)), 6)
	b.WriteBits(0, 9) // surname
	b.WriteBits(uint64(operation), 4)
	b.WriteBits(uint64(result), 6)
	b.FlushBits()
	b.WriteU32(0) // result data
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteString(name)
	return b.Bytes()
}

func mustGUID(packed []byte) wire.GUID {
	b := wire.NewReader(packed)
	return wire.ReadPackedGUID(b)
}

// invalidRoster builds the roster the partyInvalid case names. Every one of them is refused by the model before it
// can become actionable.
func (f *fakeServer) invalidRoster(inviter, us wire.GUID) (members []wire.GUID, names []string, leader wire.GUID) {
	switch f.partyInvalid {
	case "missing self":
		return []wire.GUID{inviter}, []string{"Inviter"}, inviter
	case "duplicate self":
		return []wire.GUID{inviter, us, us}, []string{"Inviter", "Testchar", "Testchar"}, inviter
	case "duplicate other":
		return []wire.GUID{inviter, inviter, us}, []string{"Inviter", "Inviter", "Testchar"}, inviter
	case "absent leader":
		return []wire.GUID{us}, []string{"Testchar"}, inviter
	case "empty guid":
		return []wire.GUID{inviter, wire.EmptyGUID}, []string{"Inviter", "Testchar"}, inviter
	case "wrong leader":
		// Structurally valid, but not the roster the accepted invite produces: the client accepted the inviter's
		// invite, so the inviter must lead it.
		return []wire.GUID{inviter, us}, []string{"Inviter", "Testchar"}, us
	}
	panic("fake: unknown invalid roster " + f.partyInvalid)
}

// serveParty runs party-check's invite flow, the invite/accept/update/leave flow, the decline flow, the uninvite
// flow, or an invalid-roster flow.
func (f *fakeServer) serveParty(c, realm *fakeConn) {
	inviter := wire.GUID{Low: 0x9001, High: 0x0800000000000000}
	us := mustGUID(f.clientGUID)
	partyGUID := wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000}
	if f.partyAccept || f.partyDecline || f.partyInvalid != "" || f.partyNoAccept {
		c.writeFrame(fakeSMSGPartyInvite, fakePartyInvite("Inviter", inviter, !f.partyNoAccept))
	}
	stage := 0
	for {
		wireNum, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() {
				return // the test's own drop: the client must notice it, the fake must not call it a failure
			}
			f.failf("party: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNum)
		f.mu.Unlock()
		switch wireNum {
		case fakeCMSGTimeSyncResponse:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGMoveInitActiveMover:
		case fakeCMSGSetAdvancedCombatLogging:
			f.checkAdvancedLogging(payload)
		case fakeCMSGPartyInvite:
			if f.partyAccept || f.partyDecline || f.partyInvalid != "" {
				f.failf("party: an invite was sent in a flow that waits for one")
				return
			}
			// PartyInviteClient::Read: the optional index, then 6/9-bit lengths, realm address, GUID, name.
			b := wire.NewReader(payload)
			b.ReadBit()
			b.FlushBits()
			n := int(b.ReadBits(6))
			b.ReadBits(9)
			b.ReadU32()
			wire.ReadPackedGUID(b)
			name := b.ReadString(n)
			b.ReadU8() // optional index
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("party: CMSG_PARTY_INVITE has %d byte(s) left", b.Remaining())
				return
			}
			f.mu.Lock()
			f.partyInvitedName = name
			f.mu.Unlock()
			c.writeFrame(fakeSMSGPartyResult, fakePartyResult(0, 0, name))
			stage = 2
			if f.partyUninvite {
				// The invitee accepted: the server's update lists both, and the leader's next step is the uninvite.
				c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(partyGUID, us, us, []wire.GUID{us, inviter}, []string{"Testchar", "Inviter"}, false))
				stage = 1
			}
		case fakeCMSGPartyInviteReply:
			if f.partyNoAccept {
				f.failf("party: an invite that cannot be accepted was answered")
				return
			}
			if !(f.partyAccept || f.partyDecline || f.partyInvalid != "") || stage != 0 {
				f.failf("party: invite response at stage %d", stage)
				return
			}
			if len(payload) < 1 || payload[0]&0x80 == 0 {
				f.failf("party: the invite response index bit is clear: % X", payload)
				return
			}
			b := wire.NewReader(payload)
			b.ReadBit() // index present
			accept := b.ReadBit()
			b.ReadBit() // roles
			b.FlushBits()
			idx := b.ReadU8()
			if idx != 0 {
				f.failf("party: the response index is %d", idx)
				return
			}
			if !accept {
				if !f.partyDecline {
					f.failf("party: a decline was sent in the accept flow")
					return
				}
				f.mu.Lock()
				f.partyDeclined = true
				f.mu.Unlock()
				stage = 9 // nothing more may be sent until the logout
				continue
			}
			if f.partyDecline {
				f.failf("party: an accept was sent in the decline flow")
				return
			}
			f.mu.Lock()
			f.partyAccepted = true
			f.mu.Unlock()
			if f.partyInvalid != "" {
				members, names, leader := f.invalidRoster(inviter, us)
				c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(partyGUID, leader, us, members, names, false))
				stage = 9 // the client must send nothing on an invalid roster
				continue
			}
			c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(partyGUID, inviter, us, []wire.GUID{inviter, us}, []string{"Inviter", "Testchar"}, false))
			stage = 1
		case fakeCMSGRequestStats:
			if stage != 1 {
				f.failf("party: member stats request at stage %d", stage)
				return
			}
			b := wire.NewReader(payload)
			if !b.ReadBit() {
				f.failf("party: the stats request index bit is clear")
				return
			}
			b.FlushBits()
			n := int(b.ReadU32())
			b.ReadU8()
			for range n {
				wire.ReadPackedGUID(b)
			}
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("party: the stats request has %d byte(s) left", b.Remaining())
				return
			}
			if n != 2 {
				f.failf("party: %d targets in the stats request, want 2", n)
				return
			}
			f.mu.Lock()
			f.partyStatsAsked = n
			f.mu.Unlock()
			c.writeFrame(fakeSMSGMemberFull, fakeMemberState(inviter, 60, 80, 100, 100, 1))
			c.writeFrame(fakeSMSGMemberFull, fakeMemberState(us, 60, 60, 0, 100, 1))
			stage = 3
		case fakeCMSGChatParty:
			_, _, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("party: the party line: %v", err)
				return
			}
			if stage != 3 {
				f.failf("party: the party line at stage %d", stage)
				return
			}
			f.mu.Lock()
			f.partySaid = text
			f.mu.Unlock()
			c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeParty, chat.LanguageCommon, us, wire.EmptyGUID, "Testchar", "", "", "", text))
			stage = 4
		case fakeCMSGPartyUninvite:
			if stage != 4 || !f.partyUninvite {
				f.failf("party: uninvite at stage %d", stage)
				return
			}
			// PartyUninvite::Read: the optional index, a 9-bit reason length, the target GUID, the index, the reason.
			b := wire.NewReader(payload)
			b.ReadBit()
			n := int(b.ReadBits(9))
			b.FlushBits()
			target := wire.ReadPackedGUID(b)
			b.ReadU8()
			reason := b.ReadString(n)
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("party: CMSG_PARTY_UNINVITE has %d byte(s) left", b.Remaining())
				return
			}
			if target != us || reason != "" {
				f.failf("party: the uninvite names %v with reason %q, want the client and an empty reason", target, reason)
				return
			}
			f.mu.Lock()
			f.partyUninvited = true
			f.mu.Unlock()
			c.writeFrame(fakeSMSGPartyResult, fakePartyResult(1, 0, "Testchar"))
			c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(partyGUID, wire.EmptyGUID, us, nil, nil, true))
			c.writeFrame(fakeSMSGGroupDestroyed, nil)
			stage = 5
		case fakeCMSGLeaveGroup:
			if stage != 4 || f.partyUninvite {
				f.failf("party: leave at stage %d", stage)
				return
			}
			if len(payload) != 1 || payload[0] != 0 {
				f.failf("party: CMSG_LEAVE_GROUP is % X, want one zero byte", payload)
				return
			}
			f.mu.Lock()
			f.partyLeft = true
			f.mu.Unlock()
			c.writeFrame(fakeSMSGPartyResult, fakePartyResult(2, 0, "Testchar"))
			c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(partyGUID, wire.EmptyGUID, us, nil, nil, true))
			c.writeFrame(fakeSMSGGroupDestroyed, nil)
			stage = 5
		case fakeCMSGLogoutRequest:
			if f.partyAccept && stage < 4 {
				f.failf("party: logout at stage %d, before the party line and leave", stage)
			}
			if f.partyInvalid != "" && stage != 9 {
				f.failf("party: logout at stage %d after an invalid roster, want 9", stage)
			}
			f.fakeLogout(c, realm)
			return
		default:
			f.failf("party: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// TestChatCheckAgainstFakeServer runs chat-check end to end: the say echo, a whisper to an offline name, an emote
// echo, a print notification and a system line.
func TestChatCheckAgainstFakeServer(t *testing.T) {
	f := newFakeServer(t)
	f.chat = true
	captureDir := t.TempDir()
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		ChatCheck:       true,
		CaptureDir:      captureDir,
		Settle:          200 * time.Millisecond,
		Say:             "hello there",
		Whisper:         "Nobody:psst",
		EmoteText:       "wave",
		Listen:          700 * time.Millisecond,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if f.chatSay != "hello there" {
		t.Fatalf("the fake saw say %q", f.chatSay)
	}
	if f.chatWhisperTarget != "Nobody" || f.chatWhisperText != "psst" {
		t.Fatalf("the fake saw whisper %q %q", f.chatWhisperTarget, f.chatWhisperText)
	}
	if f.chatEmote != "wave" {
		t.Fatalf("the fake saw emote %q", f.chatEmote)
	}
	if report.Chat.NotFound != 1 {
		t.Fatalf("player-not-found answers %d\nsteps:\n%s", report.Chat.NotFound, joinLines(steps))
	}
	if report.Chat.Types["CHAT_MSG_SAY"] < 1 || report.Chat.Types["CHAT_MSG_EMOTE"] < 1 {
		t.Fatalf("the echo lines are missing: %v", report.Chat.Types)
	}
	if report.Chat.Total < 4 {
		t.Fatalf("only %d chat line(s): %v", report.Chat.Total, report.Chat.Types)
	}
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
	for _, name := range []string{"SMSG_CHAT", "SMSG_PRINT_NOTIFICATION", "SMSG_CHAT_PLAYER_NOTFOUND"} {
		if report.Counts[name] == 0 {
			t.Errorf("the report does not count %s: %v", name, report.Counts)
		}
	}
	// The chat packets went through the session's one capture, into a run directory the shared reader reads back.
	recs, err := capturedir.ReadDir(captureDir)
	if err != nil {
		t.Fatalf("the capture: %v", err)
	}
	captured := map[uint32]bool{}
	for _, r := range recs {
		captured[r.Opcode] = true
	}
	for _, core := range []uint32{wire.SMSGChat, wire.SMSGChatPlayerNotFound, wire.SMSGPrintNotification} {
		if !captured[core] {
			t.Errorf("the capture has no %s", wire.ServerName(core))
		}
	}
	if report.Captured != len(recs) {
		t.Errorf("the report says %d captured packet(s), the directory holds %d", report.Captured, len(recs))
	}
}

// TestPartyCheckInviteAgainstFakeServer proves the invite packet: the server parses the name and answers OK.
func TestPartyCheckInviteAgainstFakeServer(t *testing.T) {
	f := newFakeServer(t)
	f.partyInvite = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		PartyCheck:      true,
		Settle:          200 * time.Millisecond,
		PartyInvite:     "Bot",
		PartyStay:       200 * time.Millisecond,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if f.partyInvitedName != "Bot" {
		t.Fatalf("the fake saw invite %q", f.partyInvitedName)
	}
	if report.Party.Counters["command results"] < 1 {
		t.Fatalf("no command result: %+v\nsteps:\n%s", report.Party.Counters, joinLines(steps))
	}
	if report.Party.InParty {
		t.Fatal("the model believes it is in a group it never joined")
	}
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
}

// TestPartyCheckAcceptFlowAgainstFakeServer runs the whole incoming half: an invite arrives, the client accepts, it
// receives the update and two member states, says a party line and leaves.
func TestPartyCheckAcceptFlowAgainstFakeServer(t *testing.T) {
	f := newFakeServer(t)
	f.partyAccept = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		PartyCheck:      true,
		Settle:          500 * time.Millisecond,
		PartyAcceptFrom: "Inviter",
		PartyStay:       600 * time.Millisecond,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if !f.partyAccepted || f.partyStatsAsked != 2 || f.partySaid != partySayText || !f.partyLeft {
		t.Fatalf("the fake saw accepted=%v stats=%d said=%q left=%v\nsteps:\n%s",
			f.partyAccepted, f.partyStatsAsked, f.partySaid, f.partyLeft, joinLines(steps))
	}
	if report.Party.Counters["states"] < 2 {
		t.Fatalf("member states %d: %+v\nsteps:\n%s", report.Party.Counters["states"], report.Party.Counters, joinLines(steps))
	}
	if report.Party.Counters["destroys"] < 1 {
		t.Fatalf("no destroyed event: %+v", report.Party.Counters)
	}
	if report.Party.InParty {
		t.Fatal("the model is still in the group after the leave")
	}
	sawMember := false
	for _, e := range report.Party.Events {
		if e.Text != "" && len(e.Text) > 0 && (e.Kind == party.EventMemberState) {
			sawMember = true
		}
	}
	if !sawMember {
		t.Fatalf("no member state event: %+v", report.Party.Events)
	}
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
	for _, name := range []string{"SMSG_PARTY_INVITE", "SMSG_PARTY_UPDATE", "SMSG_PARTY_MEMBER_FULL_STATE", "SMSG_PARTY_COMMAND_RESULT", "SMSG_GROUP_DESTROYED"} {
		if report.Counts[name] == 0 {
			t.Errorf("the report does not count %s: %v", name, report.Counts)
		}
	}
}

// TestPartyCheckRejectsAnInvalidRoster is the gate the brief asks for: on each hostile roster the client sends no
// member-stat request, no party chat line and no CMSG_LEAVE_GROUP, and names the reason.
func TestPartyCheckRejectsAnInvalidRoster(t *testing.T) {
	cases := []struct{ name, want, line string }{
		{"missing self", "is invalid", "invalid roster:"},
		{"duplicate self", "is invalid", "invalid roster:"},
		{"duplicate other", "is invalid", "invalid roster:"},
		{"absent leader", "is invalid", "invalid roster:"},
		{"empty guid", "is invalid", "invalid roster:"},
		{"wrong leader", "is unexpected", "unexpected roster:"},
	}
	for _, c := range cases {
		caseName := c.name
		t.Run(caseName, func(t *testing.T) {
			f := newFakeServer(t)
			f.partyAccept = true
			f.partyInvalid = caseName
			f.clientMayLeave = true // the client refuses the roster and leaves
			defer f.close()
			f.wg.Add(1)
			go f.serveRealm()

			var steps []string
			opts := Options{
				Address:         f.realmListener.Addr().String(),
				RealmID:         70,
				RealmAddress:    0x46010046,
				RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
				ClientSecret:    f.keyData[:32],
				JoinSecret:      f.keyData[32:],
				ClientBuild:     70707,
				RealmBuild:      70707,
				Character:       "XXXXXX",
				Deadline:        30 * time.Second,
				BuildAuthKey:    f.buildKey[:],
				PartyCheck:      true,
				Settle:          200 * time.Millisecond,
				PartyAcceptFrom: "Inviter",
				PartyStay:       300 * time.Millisecond,
				Logf: func(format string, args ...any) {
					steps = append(steps, fmt.Sprintf(format, args...))
				},
			}
			report, err := Enter(t.Context(), opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
			}
			f.wait(t)
			if f.partyStatsAsked != 0 || f.partySaid != "" || f.partyLeft {
				t.Fatalf("the client acted on an invalid roster: stats %d, party line %q, leave %v",
					f.partyStatsAsked, f.partySaid, f.partyLeft)
			}
			if report.Party.InParty {
				t.Fatal("the model believes it is in a party after an invalid roster")
			}
			found := false
			for _, line := range steps {
				if strings.Contains(line, c.line) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no %q line\nsteps:\n%s", c.line, joinLines(steps))
			}
		})
	}
}

// TestPartyCheckDeclineAgainstFakeServer: --decline-from answers an invite with accept=false and sends nothing else.
func TestPartyCheckDeclineAgainstFakeServer(t *testing.T) {
	f := newFakeServer(t)
	f.partyDecline = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:          f.realmListener.Addr().String(),
		RealmID:          70,
		RealmAddress:     0x46010046,
		RealmJoinTicket:  []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:     f.keyData[:32],
		JoinSecret:       f.keyData[32:],
		ClientBuild:      70707,
		RealmBuild:       70707,
		Character:        "XXXXXX",
		Deadline:         30 * time.Second,
		BuildAuthKey:     f.buildKey[:],
		PartyCheck:       true,
		Settle:           200 * time.Millisecond,
		PartyDeclineFrom: "Inviter",
		PartyStay:        300 * time.Millisecond,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if !f.partyDeclined || f.partyStatsAsked != 0 || f.partySaid != "" || f.partyLeft {
		t.Fatalf("the fake saw declined=%v stats=%d said=%q left=%v",
			f.partyDeclined, f.partyStatsAsked, f.partySaid, f.partyLeft)
	}
	if report.Party.InParty {
		t.Fatal("the model is in a party after declining")
	}
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
}

// TestPartyCheckUninviteAgainstFakeServer: the leader invites, waits for the join, says a line and uninvites the
// other member, whose removal disbands the two-member group (Group.cpp:588-591, 675-679).
func TestPartyCheckUninviteAgainstFakeServer(t *testing.T) {
	f := newFakeServer(t)
	f.partyUninvite = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Deadline:        40 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		PartyCheck:      true,
		Settle:          200 * time.Millisecond,
		PartyInvite:     "Testchar",
		PartyUninvite:   "Testchar",
		PartyStay:       300 * time.Millisecond,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if !f.partyUninvited || f.partyStatsAsked != 2 || f.partySaid != partySayText {
		t.Fatalf("the fake saw uninvited=%v stats=%d said=%q", f.partyUninvited, f.partyStatsAsked, f.partySaid)
	}
	if report.Party.InParty {
		t.Fatal("the model is still in the group after the uninvite disbanded it")
	}
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
}

// TestPartyCheckRefusesAnInviteThatCannotBeAccepted: CanAccept false means the client must not answer the invite.
func TestPartyCheckRefusesAnInviteThatCannotBeAccepted(t *testing.T) {
	f := newFakeServer(t)
	f.partyNoAccept = true
	f.clientMayLeave = true // the client refuses the invite and leaves
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		PartyCheck:      true,
		Settle:          200 * time.Millisecond,
		PartyAcceptFrom: "Inviter",
		PartyStay:       200 * time.Millisecond,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	if _, err := Enter(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "cannot be accepted") {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
}

// TestPartyCheckComparesNamesLikeTheServer: the invite names the inviter in one case and --accept-from uses another.
// The server normalises names before every lookup (ObjectMgr.cpp:157-185, ObjectAccessor.cpp:89-97), so the client
// does the same; a case-sensitive comparison would never find the invite.
func TestPartyCheckComparesNamesLikeTheServer(t *testing.T) {
	f := newFakeServer(t)
	f.partyAccept = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		PartyCheck:      true,
		Settle:          200 * time.Millisecond,
		PartyAcceptFrom: "iNvItEr",
		PartyStay:       200 * time.Millisecond,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if !f.partyAccepted {
		t.Fatalf("the accept was not sent\nsteps:\n%s", joinLines(steps))
	}
	if report.Party.InviteFrom != "" {
		t.Fatalf("the invite was not cleared after the accept: %q", report.Party.InviteFrom)
	}
}
