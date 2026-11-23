package world

// The trade methods (issue #96): trade, trade_open, trade_expect, trade_set_item, trade_set_gold, trade_accept,
// trade_cancel and trade_status, plus cast with a trade_slot (trade_cast.go). Method names and JSON follow turtle-agent's
// (to, from, items [{item, count}], gold in copper, trade_slot, item_entry, wait_ms, open_wait_ms); the answers are the FA
// frame's: a call waits for the server's answer and returns it. See docs/forever/DAEMON-TRADE-NOTES.md.

import (
	"context"

	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveTradeParams is trade's parameter object: to is a player's name (in view) or a guid, gold is copper.
type serveTradeParams struct {
	To         string         `json:"to"`
	Items      []TradeItemReq `json:"items,omitempty"`
	Gold       uint64         `json:"gold,omitempty"`
	WaitMS     int            `json:"wait_ms,omitempty"`
	OpenWaitMS int            `json:"open_wait_ms,omitempty"`
}

type serveTradeOpenParams struct {
	To         string `json:"to"`
	OpenWaitMS int    `json:"open_wait_ms,omitempty"`
}

type serveTradeExpectParams struct {
	From   string `json:"from"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

type serveTradeSetItemParams struct {
	TradeSlot *uint8 `json:"trade_slot"`
	ItemEntry *int32 `json:"item_entry,omitempty"`
	Bag       *int   `json:"bag,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
	Clear     bool   `json:"clear,omitempty"`
}

type serveTradeSetGoldParams struct {
	Gold uint64 `json:"gold"`
}

type serveTradeAcceptParams struct {
	WaitMS int `json:"wait_ms,omitempty"`
}

// registerTradeMethods wires the trade methods into the method table.
func registerTradeMethods(srv *rpc.Server, st *serveState) {
	srv.Register("trade", serveCallTyped(st, func(s *session, ctx context.Context, p serveTradeParams) (ServeTradeResult, error) {
		return s.serveTrade(ctx, p)
	}))
	srv.Register("trade_open", serveCallTyped(st, func(s *session, ctx context.Context, p serveTradeOpenParams) (ServeTradeResult, error) {
		return s.serveTradeOpen(ctx, p)
	}))
	srv.Register("trade_expect", serveCallTyped(st, func(s *session, ctx context.Context, p serveTradeExpectParams) (ServeTradeResult, error) {
		return s.serveTradeExpect(ctx, p)
	}))
	srv.Register("trade_set_item", serveCallTyped(st, func(s *session, ctx context.Context, p serveTradeSetItemParams) (ServeTradeSetResult, error) {
		return s.serveTradeSetItem(ctx, p)
	}))
	srv.Register("trade_set_gold", serveCallTyped(st, func(s *session, ctx context.Context, p serveTradeSetGoldParams) (ServeTradeSetResult, error) {
		return s.serveTradeSetGold(ctx, p)
	}))
	srv.Register("trade_accept", serveCallTyped(st, func(s *session, ctx context.Context, p serveTradeAcceptParams) (ServeTradeResult, error) {
		return s.serveTradeAccept(ctx, p)
	}))
	srv.Register("trade_cancel", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (map[string]string, error) {
		return s.serveTradeCancel(), nil
	}))
	srv.Register("trade_status", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeTradeState, error) {
		return s.tradeState(), nil
	}))
}
