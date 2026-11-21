package objects

// Hand-built SMSG_QUERY_GAME_OBJECT_RESPONSE packets (QueryPackets.cpp:350-388). The layout is the server's
// writer field for field: uint32 entry, packed GUID, Bits<1>(Allow) flushed, Size<uint32>(statsData), then the
// stats bytes (int32 type, int32 display, four C-string names, icon/cast-bar/unknown C strings, 35 int32 data,
// float size, uint8 quest-item count with int32 ids, int32 content tuning, int32 required level). Sample text is
// the template's own: a door and a chest with the type ids the SharedDefines GameobjectTypes enum gives (0 door,
// 3 chest).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var goQueryGUID = wire.GUID{Low: 0x0A0B0C0D0E0F1011, High: 0xF101121314151617}

// writeGOStats writes one stats block by hand: the fixed fields, the C strings with NUL terminators, the data
// words, the size, the quest items and the tuning pair.
func writeGOStats(t *testing.T, w *wire.Buffer, typ, display uint32, names [4]string, quest []int32) {
	t.Helper()
	w.WriteI32(int32(typ))
	w.WriteI32(int32(display))
	for _, n := range names {
		w.WriteString(n)
		w.WriteU8(0)
	}
	w.WriteString("icon")
	w.WriteU8(0)
	w.WriteString("")
	w.WriteU8(0)
	w.WriteString("")
	w.WriteU8(0)
	for range MaxGameObjectData {
		w.WriteI32(0)
	}
	w.WriteFloat32(1.5)
	w.WriteU8(uint8(len(quest)))
	for _, q := range quest {
		w.WriteI32(q)
	}
	w.WriteI32(7)
	w.WriteI32(10)
}

// buildGOQuery builds one SMSG_QUERY_GAME_OBJECT_RESPONSE payload: entry, GUID, allow bit, stats size, stats.
func buildGOQuery(t *testing.T, entry uint32, guid wire.GUID, allow bool, stats []byte) []byte {
	t.Helper()
	w := wire.NewWriter()
	w.WriteU32(entry)
	wire.WritePackedGUID(w, guid)
	w.WriteBit(allow)
	w.FlushBits()
	if !allow {
		w.WriteU32(0)
		return w.Bytes()
	}
	w.WriteU32(uint32(len(stats)))
	w.WriteBytes(stats)
	return w.Bytes()
}

// TestGameObjectQueryDoor: a door answer (type 0, GameobjectTypes GAMEOBJECT_TYPE_DOOR) parses to its entry, its
// four names, its type and its tuning pair.
func TestGameObjectQueryDoor(t *testing.T) {
	stats := wire.NewWriter()
	writeGOStats(t, stats, 0, 1369, [4]string{"Gate", "Porte", "Tor", ""}, []int32{1234})
	got, err := ParseGameObjectQueryResponse(buildGOQuery(t, 17153, goQueryGUID, true, stats.Bytes()))
	if err != nil {
		t.Fatalf("ParseGameObjectQueryResponse: %v", err)
	}
	if !got.Found || got.Entry != 17153 || got.GOName() != "Gate" || got.Type != 0 || got.DisplayID != 1369 {
		t.Fatalf("answer %+v", got)
	}
	if got.Name[1] != "Porte" || got.Size != 1.5 || got.ContentTuningID != 7 || got.RequiredLevel != 10 {
		t.Fatalf("answer %+v", got)
	}
	if len(got.QuestItems) != 1 || got.QuestItems[0] != 1234 {
		t.Fatalf("answer %+v", got)
	}
}

// TestGameObjectQueryChest: a chest answer (type 3) parses; a missing entry (allow clear) parses unFound with no
// names and no trailing bytes.
func TestGameObjectQueryChest(t *testing.T) {
	stats := wire.NewWriter()
	writeGOStats(t, stats, 3, 271, [4]string{"Chest", "", "", ""}, nil)
	got, err := ParseGameObjectQueryResponse(buildGOQuery(t, 123329, goQueryGUID, true, stats.Bytes()))
	if err != nil {
		t.Fatalf("ParseGameObjectQueryResponse: %v", err)
	}
	if !got.Found || got.Type != 3 || got.GOName() != "Chest" {
		t.Fatalf("answer %+v", got)
	}
	missing, err := ParseGameObjectQueryResponse(buildGOQuery(t, 999999, goQueryGUID, false, nil))
	if err != nil {
		t.Fatalf("missing: %v", err)
	}
	if missing.Found || missing.Entry != 999999 {
		t.Fatalf("missing %+v", missing)
	}
}

// TestGameObjectQueryShort: a packet that ends inside the header, inside the stats or with a trailing byte is
// refused (never a half name in the cache).
func TestGameObjectQueryShort(t *testing.T) {
	stats := wire.NewWriter()
	writeGOStats(t, stats, 3, 271, [4]string{"Chest", "", "", ""}, nil)
	full := buildGOQuery(t, 123329, goQueryGUID, true, stats.Bytes())
	for _, n := range []int{0, 1, 5, len(full) - 10, len(full) - 1} {
		if _, err := ParseGameObjectQueryResponse(full[:n]); err == nil {
			t.Fatalf("length %d parsed", n)
		}
	}
	trailing := append(append([]byte(nil), full...), 0)
	if _, err := ParseGameObjectQueryResponse(trailing); err == nil {
		t.Fatal("trailing byte parsed")
	}
	// A stats block that ends early is the same refusal.
	short := wire.NewWriter()
	short.WriteI32(3)
	got := buildGOQuery(t, 1, goQueryGUID, true, short.Bytes())
	if _, err := ParseGameObjectQueryResponse(got); err == nil {
		t.Fatal("short stats parsed")
	}
}

// TestBuildQueryGameObject: the request is the entry and the packed GUID (QueryGameObject::Read,
// QueryPackets.cpp:344-348), readable back field for field.
func TestBuildQueryGameObject(t *testing.T) {
	raw := BuildQueryGameObject(17153, goQueryGUID)
	b := wire.NewReader(raw)
	if got := b.ReadU32(); got != 17153 {
		t.Fatalf("entry %d", got)
	}
	if got := wire.ReadPackedGUID(b); got != goQueryGUID {
		t.Fatalf("guid %v", got)
	}
	if b.Remaining() != 0 {
		t.Fatalf("%d bytes left", b.Remaining())
	}
}
