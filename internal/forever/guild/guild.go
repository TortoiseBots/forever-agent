// Package guild is the Forever (Classic 1.60.1) guild layer this round models: the invite, the player-joined
// event and the command result the server sends, and the accept, decline and leave the client sends
// (src/server/game/Server/Packets/GuildPackets.cpp). The roster is not modelled: the join event plus the
// character's own UNIT_FIELD_GUILD_GUID prove membership.
//
// Every parser consumes its payload to the last byte, refuses a payload that ends inside a field and refuses a
// trailing byte. Names come back through chat.SanitiseName, so nothing a parser returns can carry a control byte.
package guild

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// EmblemFields is the number of uint32 emblem components SMSG_GUILD_INVITE carries (GuildPackets.cpp:186-214).
const EmblemFields = 5

var errShort = errors.New("guild: the packet ends inside a field")

func fail(what string, b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("guild: %s: %w", what, errShort)
	}
	return nil
}

// Invite is SMSG_GUILD_INVITE (GuildInvite::Write, GuildPackets.cpp:186-214): the four bit-packed string lengths,
// the two realm ids, the guild and the old guild, the emblem block and the achievement points, then the four
// strings. GuildName is the guild the invite offers; OldGuildName is the guild the invited player would leave.
type Invite struct {
	InviterName       string
	InviterSurname    string
	GuildName         string
	OldGuildName      string
	Guild             wire.GUID
	OldGuild          wire.GUID
	InviterRealm      uint32
	GuildRealm        uint32
	OldGuildRealm     uint32
	Emblems           [EmblemFields]uint32
	AchievementPoints int32
}

// ParseInvite decodes SMSG_GUILD_INVITE.
func ParseInvite(p []byte) (Invite, error) {
	var inv Invite
	b := wire.NewReader(p)
	nameLen := int(b.ReadBits(6))
	surnameLen := int(b.ReadBits(9))
	guildLen := int(b.ReadBits(7))
	oldGuildLen := int(b.ReadBits(7))
	b.FlushBits()
	inv.InviterRealm = b.ReadU32()
	inv.GuildRealm = b.ReadU32()
	inv.Guild = wire.ReadPackedGUID(b)
	inv.OldGuildRealm = b.ReadU32()
	inv.OldGuild = wire.ReadPackedGUID(b)
	for i := range inv.Emblems {
		inv.Emblems[i] = b.ReadU32()
	}
	inv.AchievementPoints = b.ReadI32()
	inv.InviterName = b.ReadString(nameLen)
	inv.InviterSurname = b.ReadString(surnameLen)
	inv.GuildName = b.ReadString(guildLen)
	inv.OldGuildName = b.ReadString(oldGuildLen)
	if err := fail("SMSG_GUILD_INVITE", b); err != nil {
		return inv, err
	}
	if b.Remaining() != 0 {
		return inv, fmt.Errorf("guild: SMSG_GUILD_INVITE: %d trailing byte(s)", b.Remaining())
	}
	inv.InviterName = chat.SanitiseName(inv.InviterName)
	inv.InviterSurname = chat.SanitiseName(inv.InviterSurname)
	inv.GuildName = chat.SanitiseName(inv.GuildName)
	inv.OldGuildName = chat.SanitiseName(inv.OldGuildName)
	return inv, nil
}

// MemberJoined is SMSG_GUILD_EVENT_PLAYER_JOINED (GuildPackets.cpp:254-269): who joined, on which realm.
type MemberJoined struct {
	GUID    wire.GUID
	Realm   uint32
	Name    string
	Surname string
}

// ParseMemberJoined decodes SMSG_GUILD_EVENT_PLAYER_JOINED.
func ParseMemberJoined(p []byte) (MemberJoined, error) {
	var j MemberJoined
	b := wire.NewReader(p)
	j.GUID = wire.ReadPackedGUID(b)
	j.Realm = b.ReadU32()
	nameLen := int(b.ReadBits(6))
	surnameLen := int(b.ReadBits(9))
	b.FlushBits()
	j.Name = b.ReadString(nameLen)
	j.Surname = b.ReadString(surnameLen)
	if err := fail("SMSG_GUILD_EVENT_PLAYER_JOINED", b); err != nil {
		return j, err
	}
	if b.Remaining() != 0 {
		return j, fmt.Errorf("guild: SMSG_GUILD_EVENT_PLAYER_JOINED: %d trailing byte(s)", b.Remaining())
	}
	j.Name = chat.SanitiseName(j.Name)
	j.Surname = chat.SanitiseName(j.Surname)
	return j, nil
}

// CommandResult is SMSG_GUILD_COMMAND_RESULT (GuildPackets.cpp:146-155): the server's answer to a guild command,
// naming the player it was about.
type CommandResult struct {
	Result  uint8
	Command uint8
	Name    string
}

// ParseCommandResult decodes SMSG_GUILD_COMMAND_RESULT.
func ParseCommandResult(p []byte) (CommandResult, error) {
	var r CommandResult
	b := wire.NewReader(p)
	r.Result = b.ReadU8()
	r.Command = b.ReadU8()
	nameLen := int(b.ReadBits(8))
	b.FlushBits()
	r.Name = b.ReadString(nameLen)
	if err := fail("SMSG_GUILD_COMMAND_RESULT", b); err != nil {
		return r, err
	}
	if b.Remaining() != 0 {
		return r, fmt.Errorf("guild: SMSG_GUILD_COMMAND_RESULT: %d trailing byte(s)", b.Remaining())
	}
	r.Name = chat.SanitiseName(r.Name)
	return r, nil
}

// GuildCharterItem is the guild charter's item entry (#define GUILD_CHARTER_ITEM_ID 5863,
// PetitionsHandler.cpp:36): the item an offer petition names.
const GuildCharterItem int32 = 5863

// The guild command and result values this flow names (GuildCommandType and GuildCommandError, Guild.h:131-166).
const (
	CommandInvitePlayer uint8 = 1 // GUILD_COMMAND_INVITE_PLAYER
	ResultSuccess       uint8 = 0 // ERR_GUILD_COMMAND_SUCCESS
)

// BuildInviteByName is CMSG_GUILD_INVITE_BY_NAME (GuildInviteByName::Read, GuildPackets.cpp:175-183): the name's
// 9-bit length, the absent arena-team bit, then the name's bytes. The server normalises the name before its lookup.
func BuildInviteByName(name string) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(name)), 9)
	b.WriteBit(false) // no arena team
	b.FlushBits()
	b.WriteString(name)
	return b.Bytes()
}

// BuildOfferCharter is CMSG_OFFER_PETITION (OfferPetition::Read, PetitionPackets.cpp:164-168): the charter item's
// packed GUID, then the target player's.
func BuildOfferCharter(item, target wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, item)
	wire.WritePackedGUID(b, target)
	return b.Bytes()
}

// PetitionSignResults is SMSG_PETITION_SIGN_RESULTS (PetitionSignResults::Write, PetitionPackets.cpp:128-137): the
// charter item, the player whose signature it reports and the four-bit PetitionSigns result.
type PetitionSignResults struct {
	Item   wire.GUID
	Player wire.GUID
	Error  int32
}

// Signed reports PETITION_SIGN_OK: the player signed this charter.
func (r PetitionSignResults) Signed() bool { return r.Error == 0 }

// ParsePetitionSignResults decodes SMSG_PETITION_SIGN_RESULTS.
func ParsePetitionSignResults(p []byte) (PetitionSignResults, error) {
	var r PetitionSignResults
	b := wire.NewReader(p)
	r.Item = wire.ReadPackedGUID(b)
	r.Player = wire.ReadPackedGUID(b)
	r.Error = int32(b.ReadBits(4))
	b.FlushBits()
	if err := fail("SMSG_PETITION_SIGN_RESULTS", b); err != nil {
		return r, err
	}
	if b.Remaining() != 0 {
		return r, fmt.Errorf("guild: SMSG_PETITION_SIGN_RESULTS: %d trailing byte(s)", b.Remaining())
	}
	return r, nil
}

// BuildAccept is CMSG_ACCEPT_GUILD_INVITE (GuildPackets.cpp:159-162): the packed GUID of the guild the invite
// named. The core's handler ignores it and joins the invite's own guild (GuildHandler.cpp:75); the real client
// sends the invite's guild, which is what the session passes.
func BuildAccept(guild wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, guild)
	return b.Bytes()
}

// BuildDecline is CMSG_GUILD_DECLINE_INVITATION (Opcodes.h:429, GuildHandler.cpp:82): no body.
func BuildDecline() []byte {
	return []byte{}
}

// BuildLeave is CMSG_GUILD_LEAVE (Opcodes.h:439, GuildHandler.cpp:125-129): no body.
func BuildLeave() []byte {
	return []byte{}
}
