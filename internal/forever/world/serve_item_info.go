package world

// The `item_info` method: the records the DB store holds for the requested item
// entries, joined per entry (iteminfo.go). It is a read of the server's own
// stores, not of the world model, so it never needs a unit or the character.

import (
	"context"
	"fmt"

	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveItemInfoMaxEntries bounds one call: the DB query batcher and the wire
// count both take far more, and a caller asking about a whole loot window or a
// vendor page stays below it.
const serveItemInfoMaxEntries = 64

// serveItemInfoParams is item_info's parameter object. wait_ms is accepted for
// the method frame D2 brings and does not change this round's fetch: the
// records are answered inline and the session's own DB deadline bounds them.
type serveItemInfoParams struct {
	Entries []int32 `json:"entries"`
	WaitMS  int     `json:"wait_ms,omitempty"`
}

// ServeItemInfo is item_info's result: one row per requested entry, in the
// request's order, a repeated entry repeated.
type ServeItemInfo struct {
	Items []ServeItemRow `json:"items"`
}

// ServeItemRow is one entry's answer. The fields hang off an embedded pointer so
// that a not-found row carries neither a name nor the zero values of fields it
// does not know: quality 0 is a real quality (poor), not "unknown".
type ServeItemRow struct {
	ItemEntry int32 `json:"item_entry"`
	Found     bool  `json:"found"`
	*ServeItemFields
}

// ServeItemFields is what a found entry answers. Class and Subclass stay absent
// when the Item store did not answer for the entry (iteminfo.go).
type ServeItemFields struct {
	Name           string `json:"name"`
	Quality        int8   `json:"quality"`
	QualityName    string `json:"quality_name"`
	Class          *int32 `json:"class,omitempty"`
	Subclass       *uint8 `json:"subclass,omitempty"`
	InventoryType  int8   `json:"inventory_type"`
	ItemLevel      uint16 `json:"item_level"`
	RequiredLevel  int8   `json:"required_level"`
	SellPrice      uint32 `json:"sell_price"`
	BuyPrice       uint32 `json:"buy_price"`
	Stackable      int32  `json:"stackable"`
	Bonding        uint8  `json:"bonding"`
	ContainerSlots uint8  `json:"container_slots"`
}

// qualityNames is the client's item-quality ladder (ItemQuality, the core's own
// order in DBCEnums.h): 0 poor through 7 heirloom.
var qualityNames = [...]string{"poor", "common", "uncommon", "rare", "epic", "legendary", "artifact", "heirloom"}

// qualityName names a quality id; an id past the ladder stays unnamed rather
// than guessed at.
func qualityName(q int8) string {
	if q < 0 || int(q) >= len(qualityNames) {
		return ""
	}
	return qualityNames[q]
}

// registerItemInfoMethods wires item_info into the method table.
func registerItemInfoMethods(srv *rpc.Server, st *serveState) {
	srv.Register("item_info", serveCallTyped(st, func(s *session, _ context.Context, p serveItemInfoParams) (ServeItemInfo, error) {
		return s.serveItemInfo(p)
	}))
}

// serveItemInfo answers one item_info call. Bad parameters are errors before any
// packet goes out; a fetch that fails is an error naming how many entries the
// server answered, so a caller can see whether a retry is worth it.
func (s *session) serveItemInfo(p serveItemInfoParams) (ServeItemInfo, error) {
	if n := len(p.Entries); n < 1 || n > serveItemInfoMaxEntries {
		return ServeItemInfo{}, fmt.Errorf("item_info: needs 1-%d entries, got %d", serveItemInfoMaxEntries, n)
	}
	for _, e := range p.Entries {
		if e <= 0 {
			return ServeItemInfo{}, fmt.Errorf("item_info: entry %d is not a positive item id", e)
		}
	}
	infos, err := s.itemInfos(p.Entries)
	if err != nil {
		answered := 0
		for _, info := range infos {
			if info.Found {
				answered++
			}
		}
		return ServeItemInfo{}, fmt.Errorf("item_info: %d of %d item ids answered: %w", answered, len(infos), err)
	}
	out := ServeItemInfo{Items: make([]ServeItemRow, 0, len(p.Entries))}
	for _, e := range p.Entries {
		info := infos[e]
		row := ServeItemRow{ItemEntry: e, Found: info.Found}
		if info.Found {
			row.ServeItemFields = &ServeItemFields{
				Name: info.Name, Quality: info.Quality, QualityName: qualityName(info.Quality),
				Class: info.Class, Subclass: info.Subclass,
				InventoryType: info.InventoryType, ItemLevel: info.ItemLevel, RequiredLevel: info.RequiredLevel,
				SellPrice: info.SellPrice, BuyPrice: info.BuyPrice, Stackable: info.Stackable,
				Bonding: info.Bonding, ContainerSlots: info.ContainerSlots,
			}
		}
		out.Items = append(out.Items, row)
	}
	return out, nil
}
