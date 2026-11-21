package world

// item_info against the fake DB world: one entry the server holds in both item
// stores, one it does not, rows in the request's order, and a second call
// answered from the record cache without a packet.

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// itemSparseRecord writes one ItemSparse record with the fields this test reads
// set: five strings (only Display, the name, is filled), then the 302-byte fixed
// part with OverallQualityID at its documented offset (DB2LoadInfo.h:3370-3478).
func itemSparseRecord(name string, quality int8) []byte {
	b := wire.NewWriter()
	for i := range 5 {
		if i == 4 {
			b.WriteBytes([]byte(name))
		}
		b.WriteU8(0)
	}
	fixed := make([]byte, 302)
	fixed[280] = 25 // ItemLevel, a uint16 the high byte of which stays 0
	fixed[298] = 20 // RequiredLevel
	fixed[299] = 5  // InventoryType
	fixed[300] = byte(quality)
	b.WriteBytes(fixed)
	return b.Bytes()
}

// itemRecord writes one Item record: ClassID, SubclassID, Material, InventoryType,
// then the rest of the 43-byte ItemLoadInfo minus the id (DB2LoadInfo.h:2738-2759).
func itemRecord(class int32, subclass uint8) []byte {
	b := wire.NewWriter()
	b.WriteI32(class)
	b.WriteU8(subclass)
	b.WriteBytes(make([]byte, 41))
	return b.Bytes()
}

func TestServeItemInfoAnswersRowsInOrder(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	go func() {
		dbAnswer(t, s, tables.TableItemSparse, 2589, tables.StatusValid, itemSparseRecord("Linen Cloth", 1))
		dbAnswer(t, s, tables.TableItemSparse, 999999, tables.StatusInvalid, nil)
		dbAnswer(t, s, tables.TableItem, 2589, tables.StatusValid, itemRecord(7, 5))
		dbAnswer(t, s, tables.TableItem, 999999, tables.StatusInvalid, nil)
	}()
	out, err := s.serveItemInfo(serveItemInfoParams{Entries: []int32{2589, 999999}})
	if err != nil {
		t.Fatalf("item_info: %v", err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("%d rows, want one per requested entry", len(out.Items))
	}
	found := out.Items[0]
	if found.ItemEntry != 2589 || !found.Found || found.ServeItemFields == nil {
		t.Fatalf("found row %+v", found)
	}
	if found.Name != "Linen Cloth" || found.Quality != 1 || found.QualityName != "common" {
		t.Fatalf("found row name %q quality %d %q", found.Name, found.Quality, found.QualityName)
	}
	if found.Class == nil || *found.Class != 7 || found.Subclass == nil || *found.Subclass != 5 {
		t.Fatalf("found row class pair %+v", found.ServeItemFields)
	}
	if found.ItemLevel != 25 || found.RequiredLevel != 20 || found.InventoryType != 5 {
		t.Fatalf("found row level %d required %d type %d", found.ItemLevel, found.RequiredLevel, found.InventoryType)
	}
	missing := out.Items[1]
	if missing.ItemEntry != 999999 || missing.Found || missing.ServeItemFields != nil {
		t.Fatalf("not-found row %+v", missing)
	}
	// The wire shape: a not-found row carries neither a name nor zeros a caller
	// could read as data (quality 0 is poor, so it is only sent for a found entry).
	raw, err := json.Marshal(missing)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"item_entry":999999,"found":false}` {
		t.Fatalf("not-found row marshals as %s", raw)
	}
	// One query per table, both entries in it: the ItemSparse and Item stores are
	// two broadcasts, not one per id.
	if len(fake.asked) != 2 || len(fake.asked[0]) != 2 || len(fake.asked[1]) != 2 {
		t.Fatalf("asked %v, want two batches of two ids", fake.asked)
	}
	// The second call is served from the cache: no packet at all, misses included.
	if _, err := s.serveItemInfo(serveItemInfoParams{Entries: []int32{2589, 999999}}); err != nil {
		t.Fatalf("cached item_info: %v", err)
	}
	if len(fake.asked) != 2 {
		t.Fatalf("the cached call sent %d more queries", len(fake.asked)-2)
	}
	// A repeated entry repeats its row in the request's order.
	out, err = s.serveItemInfo(serveItemInfoParams{Entries: []int32{2589, 2589}})
	if err != nil {
		t.Fatalf("repeated item_info: %v", err)
	}
	if len(out.Items) != 2 || out.Items[0].ItemEntry != 2589 || out.Items[1].ItemEntry != 2589 {
		t.Fatalf("repeated rows %+v", out.Items)
	}
}

func TestServeItemInfoParams(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	for _, p := range []serveItemInfoParams{
		{},
		{Entries: make([]int32, serveItemInfoMaxEntries+1)},
		{Entries: []int32{0}},
		{Entries: []int32{-3}},
	} {
		if _, err := s.serveItemInfo(p); err == nil {
			t.Fatalf("%+v was accepted", p)
		}
	}
	if len(fake.asked) != 0 {
		t.Fatal("a refused call sent a query")
	}
	// The method is on the served table, not on the not-ported list.
	ported, notPortedNames := ServeRPCMethods()
	if slices.Contains(notPortedNames, "item_info") {
		t.Fatal("item_info is listed as not ported")
	}
	if !slices.Contains(ported, "item_info") {
		t.Fatal("item_info is not registered")
	}
}
