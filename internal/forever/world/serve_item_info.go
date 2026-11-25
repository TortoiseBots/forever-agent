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

// serveItemInfoParams is item_info's parameter object. Entry is the single-item form (one entry is one entries list of
// one); exactly one of Entry and Entries may be given. wait_ms is reserved for the method frame D2 brings: this round's
// fetch is bounded only by the session's own DB deadline, so a non-zero wait_ms is refused rather than silently ignored.
type serveItemInfoParams struct {
	Entry   *int32  `json:"entry,omitempty"`
	Entries []int32 `json:"entries"`
	WaitMS  int     `json:"wait_ms,omitempty"`
}

// entries is the call's id list: entries when given, else the single entry.
func (p serveItemInfoParams) ids() ([]int32, error) {
	if p.Entry == nil {
		return p.Entries, nil
	}
	if len(p.Entries) != 0 {
		return nil, fmt.Errorf("item_info: give entry or entries, not both")
	}
	if *p.Entry <= 0 {
		return nil, fmt.Errorf("item_info: entry %d is not a positive item id", *p.Entry)
	}
	return []int32{*p.Entry}, nil
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

// qualityNames is the client's item-quality ladder, 0 poor through 7 heirloom
// (ItemQuality, SharedDefines.h:384).
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
	entries, err := p.ids()
	if err != nil {
		return ServeItemInfo{}, err
	}
	p.Entries = entries
	if n := len(p.Entries); n < 1 || n > serveItemInfoMaxEntries {
		return ServeItemInfo{}, fmt.Errorf("item_info: needs 1-%d entries, got %d", serveItemInfoMaxEntries, n)
	}
	if p.WaitMS != 0 {
		return ServeItemInfo{}, fmt.Errorf("item_info: wait_ms is not supported yet (the fetch uses the session's DB deadline)")
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
