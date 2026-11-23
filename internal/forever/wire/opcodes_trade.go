package wire

// The trade window's packets (issue #96): the client packets a real client sends to open, fill, accept and close a
// window, and the two server packets that drive it. The numbers are the core's OpcodeClient and OpcodeServer values
// (Opcodes.h:42, 135, 145, 176, 251, 522-524, 961-962, 1032, 2428-2429); none is in the Classic client override table, so
// the wire number is the group arithmetic's.
const (
	CMSGInitiateTrade  uint32 = 0x3D0000
	CMSGBeginTrade     uint32 = 0x3D0001
	CMSGBusyTrade      uint32 = 0x3D0002
	CMSGIgnoreTrade    uint32 = 0x3D0003
	CMSGAcceptTrade    uint32 = 0x3D0004
	CMSGUnacceptTrade  uint32 = 0x3D0005
	CMSGCancelTrade    uint32 = 0x3D0006
	CMSGSetTradeItem   uint32 = 0x3D0007
	CMSGClearTradeItem uint32 = 0x3D0008
	CMSGSetTradeGold   uint32 = 0x3D0009
	SMSGTradeUpdated   uint32 = 0x450016
	SMSGTradeStatus    uint32 = 0x450017
)

func init() {
	for core, name := range map[uint32]string{
		CMSGInitiateTrade:  "CMSG_INITIATE_TRADE",
		CMSGBeginTrade:     "CMSG_BEGIN_TRADE",
		CMSGBusyTrade:      "CMSG_BUSY_TRADE",
		CMSGIgnoreTrade:    "CMSG_IGNORE_TRADE",
		CMSGAcceptTrade:    "CMSG_ACCEPT_TRADE",
		CMSGUnacceptTrade:  "CMSG_UNACCEPT_TRADE",
		CMSGCancelTrade:    "CMSG_CANCEL_TRADE",
		CMSGSetTradeItem:   "CMSG_SET_TRADE_ITEM",
		CMSGClearTradeItem: "CMSG_CLEAR_TRADE_ITEM",
		CMSGSetTradeGold:   "CMSG_SET_TRADE_GOLD",
	} {
		clientNames[core] = name
	}
}
