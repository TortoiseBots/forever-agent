package wire

import "testing"

// TestTradeOpcodes: every trade client opcode has its core name and a wire number that translates back to it, and
// the two server opcodes carry the names the core's enum gives them.
func TestTradeOpcodes(t *testing.T) {
	for _, c := range []struct {
		core uint32
		name string
	}{
		{CMSGInitiateTrade, "CMSG_INITIATE_TRADE"},
		{CMSGBeginTrade, "CMSG_BEGIN_TRADE"},
		{CMSGBusyTrade, "CMSG_BUSY_TRADE"},
		{CMSGAcceptTrade, "CMSG_ACCEPT_TRADE"},
		{CMSGUnacceptTrade, "CMSG_UNACCEPT_TRADE"},
		{CMSGCancelTrade, "CMSG_CANCEL_TRADE"},
		{CMSGSetTradeItem, "CMSG_SET_TRADE_ITEM"},
		{CMSGClearTradeItem, "CMSG_CLEAR_TRADE_ITEM"},
		{CMSGSetTradeGold, "CMSG_SET_TRADE_GOLD"},
	} {
		if got := ClientName(c.core); got != c.name {
			t.Errorf("ClientName(0x%06X) = %q, want %q", c.core, got, c.name)
		}
		table := NewOpcodeTable(realmBuild70707, realmBuild70707)
		w, ok := table.WireForClient(c.core)
		if !ok {
			t.Errorf("%s has no wire number", c.name)
			continue
		}
		// The group arithmetic: core group 0x3D is wire group 0x3E, same index.
		if want := 0x3E0000 | c.core&0xFFFF; w != want {
			t.Errorf("%s wire 0x%06X, want 0x%06X", c.name, w, want)
		}
		if back := table.TranslateClientWire(w); back != c.core {
			t.Errorf("%s wire 0x%06X translates to 0x%06X, want 0x%06X", c.name, w, back, c.core)
		}
	}
	if ServerName(SMSGTradeStatus) != "SMSG_TRADE_STATUS" || ServerName(SMSGTradeUpdated) != "SMSG_TRADE_UPDATED" {
		t.Errorf("server names %q %q", ServerName(SMSGTradeStatus), ServerName(SMSGTradeUpdated))
	}
}
