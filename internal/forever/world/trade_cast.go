package world

// A cast at the other player's item in the trade window (cast with trade_slot): an enchant for the item the other player put
// in the non-traded slot, which the core applies when the trade completes (Spell.cpp:3826-3841). The core addresses only that
// one slot: the target block names ObjectGuid::TradeItem and the core reads it as "the other player's slot 6"
// (Spell.cpp:413-418), so any other slot is refused here before a packet goes out. The send and the wait are Cast's
// (fight.go) with the trade target block in place of a unit.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/trade"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// castTradeSlot casts spellID at the trade window's non-traded slot and returns the outcome Cast would ("success" or the
// server's refusal).
func (s *session) castTradeSlot(ctx context.Context, spellID int32, slot uint8) (string, error) {
	if spellID <= 0 {
		return "", errors.New("cast needs a spell id")
	}
	if slot != trade.SlotNonTraded {
		return "", fmt.Errorf("cast: trade_slot %d is not castable: the core addresses only the other player's slot %d", slot, trade.SlotNonTraded)
	}
	if err := s.tradeWorld(); err != nil {
		return "", err
	}
	if err := s.tradeWindow(); err != nil {
		return "", err
	}
	if s.mover == nil {
		return "", errors.New("world: no mover (the character's create block never arrived)")
	}
	link := s.transport()
	if until, ok := s.fight.quarantinedUntil(spellID, link.Now()); ok {
		if err := link.WaitUntil(ctx, until); err != nil {
			return "", err
		}
	}
	s.castSerial++
	castID := combat.CastGUID(s.world.MapID(), spellID, s.castSerial)
	before := s.fight.castSeq
	req := combat.CastRequest{CastID: castID, SpellID: spellID, Target: trade.CastTarget()}
	s.fight.beginCast(spellID, castID)
	defer s.fight.castReset()
	s.fight.add("cast", fmt.Sprintf("CMSG_CAST_SPELL for spell %d at trade slot %d", spellID, slot), 0)
	if err := link.Send(wire.CMSGCastSpell, combat.BuildCastSpell(req)); err != nil {
		return "", fmt.Errorf("world: CMSG_CAST_SPELL: %w", err)
	}
	deadline := link.Now().Add(castWaitBase)
	hardDeadline := link.Now().Add(castWaitMax)
	for {
		if s.fight.castSeq > before {
			return s.fight.castResult, nil
		}
		if ends := s.fight.castEnds; ends.After(deadline) {
			deadline = ends.Add(2 * time.Second)
			if deadline.After(hardDeadline) {
				deadline = hardDeadline
			}
		}
		if link.Now().After(deadline) {
			s.fight.quarantineCast(spellID, link.Now())
			return "", fmt.Errorf("world: no answer to CMSG_CAST_SPELL for spell %d", spellID)
		}
		if err := link.WaitUntil(ctx, link.Now().Add(100*time.Millisecond)); err != nil {
			return "", err
		}
	}
}
