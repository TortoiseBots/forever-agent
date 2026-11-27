package world

// The two guild offers the playing character sends (#290): a guild invite by name and a guild charter offered to a
// character for signing. Both send the client's own packets; the answers arrive as guild rows in the session's event
// stream (the join event, the invite's decline/expire packets and SMSG_PETITION_SIGN_RESULTS).
//
// The invite is confirmed by the core's SMSG_GUILD_COMMAND_RESULT for GUILD_COMMAND_INVITE_PLAYER, the same answer
// the client's own guild window reads. The charter offer has no success packet: the core forwards the signature list
// to the target and answers only a refused offer with a command result, so the call returns after the send and the
// event stream carries the signature when it comes.

import (
	"context"
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/guild"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// guildInvite invites a character to this character's guild (CMSG_GUILD_INVITE_BY_NAME) and answers once the core's
// command result for that name is in. The character must be in a guild; the core checks the rank and the target and
// names a refusal in the same result.
func (s *session) guildInvite(ctx context.Context, name string) error {
	const what = "guild_invite"
	norm, err := chat.NormaliseName(name)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if s.selfGuildGUID().IsZero() {
		return fmt.Errorf("%s: not in a guild", what)
	}
	st := s.guildInit()
	before := st.results
	if err := s.Send(wire.CMSGGuildInviteByName, guild.BuildInviteByName(norm)); err != nil {
		return err
	}
	s.opts.logf("guild: invite %s", norm)
	var answer *guild.CommandResult
	if err := s.waitGuildConfirm(ctx, what, guildConfirmWait, func() bool {
		r := st.lastResult
		if st.results <= before || r == nil || r.Command != guild.CommandInvitePlayer {
			return false
		}
		// A success names the invited character; the core's rank refusal (result 8) carries no name.
		if r.Result == guild.ResultSuccess && !strings.EqualFold(r.Name, norm) {
			return false
		}
		answer = r
		return true
	}); err != nil {
		return err
	}
	if answer.Result != guild.ResultSuccess {
		return fmt.Errorf("%s: the server refused it: result %d for command %d", what, answer.Result, answer.Command)
	}
	return nil
}

// guildCharterForOffer finds the charter an offer names: the item GUID the caller passed, or the only guild charter
// (entry 5863) in the inventory. An explicit item must be in the inventory and be a charter, so a typo refuses
// before the packet goes out.
func guildCharterForOffer(inv items.Inventory, want wire.GUID) (items.Item, error) {
	if !want.IsZero() {
		for _, it := range inv.Items {
			if it.GUID != want {
				continue
			}
			if it.Entry != guild.GuildCharterItem {
				return items.Item{}, fmt.Errorf("item %s is not a guild charter (entry %d)", want, guild.GuildCharterItem)
			}
			return it, nil
		}
		return items.Item{}, fmt.Errorf("item %s is not in the inventory", want)
	}
	found := inv.Find(guild.GuildCharterItem)
	switch len(found) {
	case 0:
		return items.Item{}, fmt.Errorf("no guild charter (entry %d) in the inventory", guild.GuildCharterItem)
	case 1:
		return found[0], nil
	default:
		return items.Item{}, fmt.Errorf("the inventory holds %d guild charters: pass item=<guid>", len(found))
	}
}

// offerCharter offers a guild charter to a character for signing (CMSG_OFFER_PETITION). The target is a player in
// view; a guilded target is refused here because the core answers that offer with a command result and no signature
// list. The call returns after the send: the signature, or the core's refusal of a signature, is a guild event.
func (s *session) offerCharter(ctx context.Context, guid wire.GUID, name string, item wire.GUID) error {
	const what = "offer_charter"
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	charter, err := guildCharterForOffer(s.inventoryNow(), item)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	target, err := s.serveUnitByName(GUID(guid), name)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	who := s.eventName("", target.GUID)
	if who == "" {
		who = target.GUID.String()
	}
	// A player's GUID carries HighGuid::Player (ObjectGuid.h); the unit's PlayerData may not be decoded for a
	// character the client only met, so the GUID type is the reliable test.
	if target.GUID.Type() != wire.TypePlayer {
		return fmt.Errorf("%s: %s is not a player", what, who)
	}
	if self := s.selfOrMover(); !self.IsZero() && target.GUID == self {
		return fmt.Errorf("%s: that is this character", what)
	}
	if !target.Info.Guild.IsZero() {
		return fmt.Errorf("%s: %s is already in a guild", what, who)
	}
	if err := s.Send(wire.CMSGOfferPetition, guild.BuildOfferCharter(charter.GUID, target.GUID)); err != nil {
		return err
	}
	s.opts.logf("guild: offer the charter %s to %s", charter.GUID, who)
	return nil
}
