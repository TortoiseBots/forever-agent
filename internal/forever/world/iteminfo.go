package world

// Item info through the DB store: the client has no item query on this server
// (SMSG_ITEM_QUERY_SINGLE_RESPONSE has no counterpart), the stores answer
// CMSG_DB_QUERY_BULK with the ItemSparse and Item records they hold
// (HotfixHandler.cpp:25-55), and itemInfos joins the two per entry. The cache
// under fetchRecords makes a second read of the same entry free.

import (
	"github.com/pjerra/forever-agent/internal/forever/tables"
)

// ItemInfo is one entry's joined record. Found is the ItemSparse record: an
// entry whose Item record came back but whose ItemSparse did not is not an
// item the client could show. Class and Subclass stay nil without the Item
// record: the two stores are loaded separately and one may be missing.
type ItemInfo struct {
	Entry          int32
	Found          bool
	Name           string
	Quality        int8
	Class          *int32
	Subclass       *uint8
	InventoryType  int8
	ItemLevel      uint16
	RequiredLevel  int8
	SellPrice      uint32
	BuyPrice       uint32
	Stackable      int32
	Bonding        uint8
	ContainerSlots uint8
}

// itemInfos reads the requested entries' ItemSparse and Item records: one
// fetchRecords per table (ItemSparse first, then Item), deduplicated, both
// through the session's record cache. Every requested entry is in the result;
// an entry the server does not hold is Found false. A repeated entry asks once.
func (s *session) itemInfos(entries []int32) (map[int32]ItemInfo, error) {
	out := make(map[int32]ItemInfo, len(entries))
	ids := make([]uint32, 0, len(entries))
	seen := make(map[int32]bool, len(entries))
	for _, e := range entries {
		out[e] = ItemInfo{Entry: e}
		if seen[e] {
			continue
		}
		seen[e] = true
		ids = append(ids, uint32(e))
	}
	sparse, err := s.fetchRecords(tables.TableItemSparse, ids)
	if err != nil {
		return out, err
	}
	for id, data := range sparse {
		rec, err := tables.ParseItemSparse(data, id)
		if err != nil {
			return out, err
		}
		info := out[int32(id)]
		info.Found = true
		info.Name = rec.Name
		info.Quality = rec.Quality
		info.InventoryType = rec.InventoryType
		info.ItemLevel = rec.ItemLevel
		info.RequiredLevel = rec.RequiredLevel
		info.SellPrice = rec.SellPrice
		info.BuyPrice = rec.BuyPrice
		info.Stackable = rec.Stackable
		info.Bonding = rec.Bonding
		info.ContainerSlots = rec.ContainerSlots
		out[int32(id)] = info
	}
	items, err := s.fetchRecords(tables.TableItem, ids)
	if err != nil {
		return out, err
	}
	for id, data := range items {
		rec, err := tables.ParseItem(data, id)
		if err != nil {
			return out, err
		}
		info := out[int32(id)]
		class, subclass := rec.Class, rec.Subclass
		info.Class, info.Subclass = &class, &subclass
		out[int32(id)] = info
	}
	return out, nil
}
