package world

// The vendor of round R3: the four server answers that change what the client knows at a vendor, and the packet a
// step stores for the flow in vendor_act.go. A vendor's list arrives only after CMSG_LIST_INVENTORY has been sent a
// second time (the first answers SMSG_NPC_INTERACTION_OPEN_RESULT, ItemHandler.cpp:619-645); a purchase answers
// SMSG_BUY_SUCCEEDED and then a push result naming the slot, a refusal SMSG_BUY_FAILED; a sale that worked answers
// nothing at all (its item moves into a buyback slot, F11), and SMSG_SELL_RESPONSE comes only on an error.

import (
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// vendorWatch holds the vendor answers since the last step cleared them. The session goroutine is the only reader
// and writer, like the other watches.
type vendorWatch struct {
	// open is the last SMSG_NPC_INTERACTION_OPEN_RESULT.
	open *npc.InteractionOpenResult
	// inv is the last vendor list.
	inv *items.VendorInventory
	// bought and buyFail are the answers to the last purchase.
	bought  *items.BuySucceeded
	buyFail *items.BuyFailed
	// sold is the last SMSG_SELL_RESPONSE (a refusal; a sale that worked sends none).
	sold *items.SellResponse
}

// isVendorPacket reports the opcodes handleVendor owns.
func isVendorPacket(core uint32) bool {
	switch core {
	case wire.SMSGVendorInventory, wire.SMSGBuySucceeded, wire.SMSGBuyFailed, wire.SMSGSellResponse:
		return true
	}
	return false
}

// handleVendor applies one vendor answer. A packet this round's parser cannot consume exactly is counted and
// merges nothing.
func (s *session) handleVendor(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGVendorInventory:
		inv, err := items.ParseVendorInventory(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.vendor.inv = &inv
		// SendListInventory starts the interaction on the vendor (ItemHandler.cpp:626).
		s.bankInteractionAt(inv.Vendor)
		if inv.Reason == items.VendorInventoryReasonEmpty {
			s.opts.logf("world: the vendor list is empty")
		}
	case wire.SMSGBuySucceeded:
		v, err := items.ParseBuySucceeded(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.vendor.bought = &v
	case wire.SMSGBuyFailed:
		v, err := items.ParseBuyFailed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.vendor.buyFail = &v
	case wire.SMSGSellResponse:
		v, err := items.ParseSellResponse(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.vendor.sold = &v
		s.opts.logf("world: the vendor refused: %s", v.Name())
	}
	return nil
}
