package db2

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tField is one column of a hand-built Map.db2: its plain bits, or a bit-packed value, a pallet, a pallet
// array or common data.
type tField struct {
	bitOffset int // the column's place in the record, in bits
	bitSize   int
	comp      uint32
	packedOff uint32 // immediate, pallet: the value's bit offset in the packed area
	packedW   uint32
	signed    bool
	arraySize uint32
	pallets   []uint32    // pallet and pallet array
	common    [][2]uint32 // common data: id, value
	def       uint32      // common data: the value of an id not listed
}

// mapFields is the 26-column 70959 layout as the test files store it: the two strings and a packed
// signed InstanceType like the real Map.db2, the other columns plain.
func mapFields() []tField {
	fs := make([]tField, 26)
	for i := range fs {
		fs[i] = tField{bitOffset: 32 * i, bitSize: 32}
	}
	fs[8] = tField{bitOffset: 32 * 8, bitSize: 3, comp: compSignedImmediate, packedOff: 2, packedW: 3, signed: true}
	return fs
}

const (
	testRecordSize   = 104
	testPackedOffset = 32
	testInstanceType = 3 // the packed value the builder writes, and Maps must read
)

// Offsets in a WDC5 file the builder writes, for the tests that corrupt one number.
const (
	offRecordCount  = 136
	offRecordSize   = 144
	offFlags        = 172
	offIDIndex      = 174
	offTotalFields  = 176
	offPackedData   = 180
	offColumnMeta   = 188
	offCommonData   = 192
	offPalletData   = 196
	offSectionCount = 200
	offSections     = 204
	secTact         = 0
	secFileOffset   = 8
	secRecordCount  = 12
	secStringSize   = 16
	secIDListSize   = 24
	secCopyCount    = 36
)

// mapDB2 is one file to build: the directory and name of every record, its id, the copy table and
// which sections are encrypted (an encrypted file gives every record its own section).
type mapDB2 struct {
	wdc4      bool
	dirs      []string
	names     []string
	ids       []uint32
	copies    [][2]uint32
	encrypted []bool
	fields    []tField // nil: mapFields()
	inlineIDs bool     // the ids are field 2 of every record, there is no ID list
	layout    uint32   // 0: the 70959 layout hash
	patch     func(i int, r []byte)
}

// sections gives every section's first record and count.
func (m mapDB2) sections() [][2]int {
	if len(m.encrypted) == 0 {
		return [][2]int{{0, len(m.dirs)}}
	}
	out := make([][2]int, len(m.encrypted))
	for i := range out {
		out[i] = [2]int{i, 1}
	}
	return out
}

func (m mapDB2) isEncrypted(i int) bool { return len(m.encrypted) > 0 && m.encrypted[i] }

// buildMapDB2 writes the file the reader is tested against. A string column holds the distance from
// the field to the string in the record-plus-strings blob, as WDC2 and later do; the first section
// carries the string block and the copy table.
func buildMapDB2(t testing.TB, m mapDB2) []byte {
	t.Helper()
	le := binary.LittleEndian
	fields := m.fields
	if fields == nil {
		fields = mapFields()
	}
	layout := m.layout
	if layout == 0 {
		layout = mapLayoutHash
	}
	secs := m.sections()
	headerLen := 204
	if m.wdc4 {
		headerLen = 72
	}

	// The strings, and every string column's distance from its field.
	strs := []byte{0}
	dirOff := make([]int, len(m.dirs))
	nameOff := make([]int, len(m.names))
	for i := range m.dirs {
		dirOff[i] = len(strs)
		strs = append(append(strs, m.dirs[i]...), 0)
		nameOff[i] = len(strs)
		strs = append(append(strs, m.names[i]...), 0)
	}
	stringBase := len(m.dirs) * testRecordSize
	records := make([][]byte, len(m.dirs))
	for i := range records {
		r := make([]byte, testRecordSize)
		if m.dirs[i] != "" {
			le.PutUint32(r[0:], uint32(stringBase-i*testRecordSize+dirOff[i]))
		}
		if m.names[i] != "" {
			le.PutUint32(r[4:], uint32(stringBase-i*testRecordSize-4+nameOff[i]))
		}
		r[testPackedOffset] = testInstanceType << 2
		if m.inlineIDs {
			le.PutUint32(r[8:], m.ids[i])
		}
		if m.patch != nil {
			m.patch(i, r)
		}
		records[i] = r
	}

	// The pallet data: pallet fields, then pallet arrays; then the common data.
	var palletData, commonData []byte
	for _, comp := range []uint32{compPallet, compPalletArray} {
		for _, f := range fields {
			if f.comp == comp {
				for _, v := range f.pallets {
					palletData = le.AppendUint32(palletData, v)
				}
			}
		}
	}
	for _, f := range fields {
		if f.comp == compCommonData {
			for _, kv := range f.common {
				commonData = le.AppendUint32(commonData, kv[0])
				commonData = le.AppendUint32(commonData, kv[1])
			}
		}
	}

	// The sections' data offsets: records, the first section's strings, ids, copies.
	encBytes := 0
	for i := range secs {
		if m.isEncrypted(i) {
			encBytes += 4 + 4*secs[i][1] // the encrypted id count and the ids
		}
	}
	dataStart := headerLen + 40*len(secs) + 4*len(fields) + columnMetaLen*len(fields) + len(palletData) + len(commonData) + encBytes
	type layoutOf struct{ records, strs, ids, copies int }
	ls := make([]layoutOf, len(secs))
	off := dataStart
	for i, s := range secs {
		l := layoutOf{records: off}
		off += s[1] * testRecordSize
		l.strs = off
		if i == 0 {
			off += len(strs)
		}
		l.ids = off
		if !m.inlineIDs {
			off += 4 * s[1]
		}
		if i == 0 && len(m.copies) > 0 {
			l.copies = off
			off += 8 * len(m.copies)
		}
		ls[i] = l
	}

	var b bytes.Buffer
	u32 := func(v uint32) { binary.Write(&b, le, v) }
	u16 := func(v uint16) { binary.Write(&b, le, v) }
	if m.wdc4 {
		b.WriteString("WDC4")
	} else {
		b.WriteString("WDC5")
		u32(5)
		schema := make([]byte, 128)
		copy(schema, "WOWSTATIC_1_60_1_70959")
		b.Write(schema)
	}
	flags, idIndex := uint16(flagNonInline), uint16(0)
	if m.inlineIDs {
		flags, idIndex = 0, 2
	}
	u32(uint32(len(records)))
	u32(uint32(len(fields)))
	u32(testRecordSize)
	u32(uint32(len(strs)))
	u32(0x1234) // table hash
	u32(layout)
	u32(0) // min id
	u32(0) // max id
	u32(1) // locale
	u16(flags)
	u16(idIndex)
	u32(uint32(len(fields)))
	u32(testPackedOffset)
	u32(0) // parent lookups
	u32(uint32(columnMetaLen * len(fields)))
	u32(uint32(len(commonData)))
	u32(uint32(len(palletData)))
	u32(uint32(len(secs)))

	for i, s := range secs {
		tact := uint64(0)
		if m.isEncrypted(i) {
			tact = 0x0123456789abcdef
		}
		binary.Write(&b, le, tact)
		u32(uint32(ls[i].records))
		u32(uint32(s[1]))
		strBytes := 0
		if i == 0 {
			strBytes = len(strs)
		}
		u32(uint32(strBytes))
		u32(0) // offset records end
		idBytes := 4 * s[1]
		if m.inlineIDs {
			idBytes = 0
		}
		u32(uint32(idBytes))
		u32(0) // relationship data
		u32(0) // offset map ids
		copies := 0
		if i == 0 {
			copies = len(m.copies)
		}
		u32(uint32(copies))
	}
	for _, f := range fields {
		binary.Write(&b, le, int16(32-f.bitSize))
		u16(uint16(f.bitOffset / 8))
	}
	for _, f := range fields {
		u16(uint16(f.bitOffset))
		u16(uint16(f.bitSize))
		switch f.comp {
		case compPallet, compPalletArray:
			u32(uint32(4 * len(f.pallets)))
		case compCommonData:
			u32(uint32(8 * len(f.common)))
		default:
			u32(0)
		}
		u32(f.comp)
		switch f.comp {
		case compImmediate, compSignedImmediate:
			u32(f.packedOff)
			u32(f.packedW)
			signed := uint32(0)
			if f.signed {
				signed = 1
			}
			u32(signed)
		case compCommonData:
			u32(f.def)
			u32(0)
			u32(0)
		case compPallet:
			u32(f.packedOff)
			u32(f.packedW)
			u32(0)
		case compPalletArray:
			u32(f.packedOff)
			u32(f.packedW)
			u32(f.arraySize)
		default:
			u32(0)
			u32(0)
			u32(0)
		}
	}
	b.Write(palletData)
	b.Write(commonData)
	for i := range secs {
		if m.isEncrypted(i) {
			u32(uint32(secs[i][1]))
			for k := range secs[i][1] {
				u32(m.ids[secs[i][0]+k])
			}
		}
	}
	if b.Len() != dataStart {
		t.Fatalf("the header blocks end at %d, the data was laid out at %d", b.Len(), dataStart)
	}
	for i, s := range secs {
		for _, r := range records[s[0] : s[0]+s[1]] {
			b.Write(r)
		}
		if i == 0 {
			b.Write(strs)
		}
		if !m.inlineIDs {
			for k := range s[1] {
				u32(m.ids[s[0]+k])
			}
		}
		if i == 0 {
			for _, c := range m.copies {
				u32(c[0])
				u32(c[1])
			}
		}
	}
	return b.Bytes()
}

// openBytes parses a hand-built file.
func openBytes(t *testing.T, b []byte) *File {
	t.Helper()
	f, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// twoMaps is the smallest useful file: two plain maps.
func twoMaps() mapDB2 {
	return mapDB2{
		dirs:  []string{"Azeroth", "Bmap"},
		names: []string{"Eastern Kingdoms", "B Map"},
		ids:   []uint32{0, 209},
	}
}

func TestWDCStringFieldOffsetIsRelative(t *testing.T) {
	// Two records. Directory is the distance from the field, so the second record's value is smaller by
	// one record: a reader that adds it to the string block (or to the file) reads the wrong place.
	f := openBytes(t, buildMapDB2(t, twoMaps()))
	if f.Version != 5 || f.RecordCount != 2 || f.TotalFieldCount != 26 {
		t.Fatalf("file = version %d, %d records, %d fields", f.Version, f.RecordCount, f.TotalFieldCount)
	}
	rows, _, err := f.Maps()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0] != (Map{ID: 0, Directory: "Azeroth", MapName: "Eastern Kingdoms", InstanceType: testInstanceType}) {
		t.Errorf("first row = %+v", rows[0])
	}
	if rows[1] != (Map{ID: 209, Directory: "Bmap", MapName: "B Map", InstanceType: testInstanceType}) {
		t.Errorf("second row = %+v, want Directory \"Bmap\" and MapName \"B Map\"", rows[1])
	}
}

func TestMapDB2CopyTable(t *testing.T) {
	m := twoMaps()
	m.copies = [][2]uint32{{999, 209}}
	rows, notes, err := openBytes(t, buildMapDB2(t, m)).Maps()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(notes) != 0 {
		t.Fatalf("rows = %+v, notes = %q", rows, notes)
	}
	if got := rows[2]; got != (Map{ID: 999, Directory: "Bmap", MapName: "B Map", InstanceType: testInstanceType}) {
		t.Errorf("copy = %+v", got)
	}
}

func TestCopySourceInSkippedSectionIsSkipped(t *testing.T) {
	// Record 2 (id 1) is in an encrypted section. The copy of id 1 must not read record index 1 instead:
	// that is "Bmap", a different map.
	m := mapDB2{
		dirs:      []string{"Azeroth", "Bmap", ""},
		names:     []string{"Eastern Kingdoms", "B Map", ""},
		ids:       []uint32{10, 20, 1},
		encrypted: []bool{false, false, true},
		copies:    [][2]uint32{{999, 1}, {998, 20}, {997, 12345}},
	}
	f := openBytes(t, buildMapDB2(t, m))
	rows, notes, err := f.Maps()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == 999 {
			t.Fatalf("the copy of a skipped row was read as %+v", r)
		}
	}
	if len(rows) != 3 || rows[2].ID != 998 || rows[2].Directory != "Bmap" {
		t.Errorf("rows = %+v, want the two plain rows and the copy of id 20", rows)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "2 copy rows skipped") {
		t.Errorf("notes = %q, want a count of 2 skipped copies", notes)
	}
}

func TestWDC4Header(t *testing.T) {
	m := mapDB2{wdc4: true, dirs: []string{"Azeroth"}, names: []string{"Eastern Kingdoms"}, ids: []uint32{0}}
	path := filepath.Join(t.TempDir(), "Map.db2")
	if err := os.WriteFile(path, buildMapDB2(t, m), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 4 || f.Schema != "" {
		t.Errorf("version %d, schema %q", f.Version, f.Schema)
	}
	rows, _, err := f.Maps()
	if err != nil || len(rows) != 1 || rows[0].Directory != "Azeroth" {
		t.Errorf("rows = %+v, %v", rows, err)
	}
}

func TestEncryptedSectionSkipped(t *testing.T) {
	b := buildMapDB2(t, mapDB2{
		dirs:      []string{"Azeroth", ""},
		names:     []string{"Eastern Kingdoms", ""},
		ids:       []uint32{0, 99},
		encrypted: []bool{false, true},
	})
	f := openBytes(t, b)
	rows, _, err := f.Maps()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != 0 || rows[0].Directory != "Azeroth" {
		t.Fatalf("rows = %+v: the encrypted record is read", rows)
	}
	if len(f.Notes) != 1 || !bytes.Contains([]byte(f.Notes[0]), []byte("section 1 is encrypted")) {
		t.Errorf("notes = %q", f.Notes)
	}
	if !f.SkippedID(99) || f.SkippedID(0) || f.SkippedID(5) {
		t.Errorf("SkippedID: 99 = %v, 0 = %v, 5 = %v; want true, false, false", f.SkippedID(99), f.SkippedID(0), f.SkippedID(5))
	}
}

func TestMapDB2RefusesOtherLayout(t *testing.T) {
	fields := mapFields()[:20]
	m := mapDB2{dirs: []string{"Azeroth"}, names: []string{"Eastern Kingdoms"}, ids: []uint32{0}, fields: fields}
	if _, _, err := openBytes(t, buildMapDB2(t, m)).Maps(); err == nil {
		t.Error("a 20-field Map.db2 is read as the 70959 layout")
	}
	// The same 26 fields under another layout hash are another client's table.
	m = twoMaps()
	m.layout = 0xD43AFAC4
	if _, _, err := openBytes(t, buildMapDB2(t, m)).Maps(); err == nil {
		t.Error("a 26-field file with another layout hash is read as the 70959 layout")
	}
}

func TestOpenRejectsOtherFiles(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":     nil,
		"not a db2": []byte("WDBC...."),
		"truncated": buildMapDB2(t, mapDB2{dirs: []string{"Azeroth"}, names: []string{"Eastern Kingdoms"}, ids: []uint32{0}})[:300],
	} {
		path := filepath.Join(t.TempDir(), "Map.db2")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestOpenRefusesOversizeFile(t *testing.T) {
	old := maxFileSize
	maxFileSize = 100
	defer func() { maxFileSize = old }()
	path := filepath.Join(t.TempDir(), "Map.db2")
	if err := os.WriteFile(path, buildMapDB2(t, twoMaps()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("err = %v, want the file size cap", err)
	}
}

// instanceTypes reads the InstanceType column of every record through the column's own compression.
func instanceTypes(t *testing.T, m mapDB2) ([]uint32, error) {
	t.Helper()
	rows, _, err := openBytes(t, buildMapDB2(t, m)).Maps()
	var out []uint32
	for _, r := range rows {
		out = append(out, r.InstanceType)
	}
	return out, err
}

func TestColumnCompressions(t *testing.T) {
	base := func(field tField, patch func(i int, r []byte)) mapDB2 {
		m := twoMaps()
		m.fields = mapFields()
		m.fields[8] = field
		m.patch = patch
		return m
	}
	// The packed area starts at byte 32; instance type is at bit 2 of it, 3 bits wide.
	pack := func(vals ...byte) func(int, []byte) {
		return func(i int, r []byte) { r[testPackedOffset] = vals[i] << 2 }
	}
	t.Run("plain", func(t *testing.T) {
		got, err := instanceTypes(t, base(tField{bitOffset: 32 * 8, bitSize: 32},
			func(i int, r []byte) { binary.LittleEndian.PutUint32(r[32:], uint32(5+i)) }))
		if err != nil || len(got) != 2 || got[0] != 5 || got[1] != 6 {
			t.Errorf("plain = %v, %v", got, err)
		}
	})
	t.Run("immediate", func(t *testing.T) {
		got, err := instanceTypes(t, base(tField{bitOffset: 32 * 8, bitSize: 3, comp: compImmediate, packedOff: 2, packedW: 3}, pack(7, 2)))
		if err != nil || len(got) != 2 || got[0] != 7 || got[1] != 2 {
			t.Errorf("immediate = %v, %v; an unsigned 3-bit 7 must stay 7", got, err)
		}
	})
	t.Run("signed immediate", func(t *testing.T) {
		got, err := instanceTypes(t, base(mapFields()[8], pack(3, 2)))
		if err != nil || len(got) != 2 || got[0] != 3 || got[1] != 2 {
			t.Errorf("signed immediate = %v, %v", got, err)
		}
		// 7 in 3 bits is -1: an instance type below zero is an error, not 4294967295.
		if _, err := instanceTypes(t, base(mapFields()[8], pack(7, 2))); err == nil {
			t.Error("a negative instance type is accepted")
		}
	})
	t.Run("pallet", func(t *testing.T) {
		f := tField{bitOffset: 32 * 8, bitSize: 3, comp: compPallet, packedOff: 2, packedW: 3, pallets: []uint32{0, 5, 3}}
		got, err := instanceTypes(t, base(f, pack(2, 1)))
		if err != nil || len(got) != 2 || got[0] != 3 || got[1] != 5 {
			t.Errorf("pallet = %v, %v", got, err)
		}
		if _, err := instanceTypes(t, base(f, pack(6, 1))); err == nil {
			t.Error("a pallet index past the pallet is accepted")
		}
	})
	t.Run("pallet array", func(t *testing.T) {
		// Field 9 is an array of two values per index; the file stores the index in 2 bits at bit 8.
		m := twoMaps()
		m.fields = mapFields()
		m.fields[9] = tField{bitOffset: 32 * 9, bitSize: 2, comp: compPalletArray, packedOff: 8, packedW: 2, arraySize: 2,
			pallets: []uint32{10, 11, 20, 21, 30, 31}}
		m.patch = func(i int, r []byte) { r[testPackedOffset+1] = byte(2 - i) }
		f := openBytes(t, buildMapDB2(t, m))
		for i, want := range [][]uint32{{30, 31}, {20, 21}} {
			got, err := f.values(i, 9)
			if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
				t.Errorf("record %d: values = %v, %v, want %v (every element, not the first only)", i, got, err, want)
			}
		}
		if _, err := f.value(0, 9); err == nil {
			t.Error("value() reads an array column as a scalar")
		}
		m.patch = func(i int, r []byte) { r[testPackedOffset+1] = 3 } // index 3 of 3
		if _, err := openBytes(t, buildMapDB2(t, m)).values(0, 9); err == nil {
			t.Error("a pallet array index past the pallet is accepted")
		}
	})
	t.Run("common data", func(t *testing.T) {
		// The value is keyed by the record's id; an id not listed takes the default.
		f := tField{bitOffset: 32 * 8, bitSize: 32, comp: compCommonData, def: 4, common: [][2]uint32{{209, 2}, {77, 1}}}
		got, err := instanceTypes(t, base(f, nil))
		if err != nil || len(got) != 2 || got[0] != 4 || got[1] != 2 {
			t.Errorf("common data = %v, %v, want [4 2] (id 0 takes the default, id 209 its value)", got, err)
		}
	})
}

func TestReadBitsCrossesNineBytes(t *testing.T) {
	// A 60-bit value at bit 5 spans 9 bytes: its top bits are in the ninth.
	want := uint64(0x0F0E0D0C0B0A0908)&(1<<60-1) | 1<<59
	var r [16]byte
	v := want << 5
	binary.LittleEndian.PutUint64(r[0:], v)
	r[8] = byte(want >> (64 - 5))
	r[0] |= 0b10101 // bits below the value must not leak in
	if got := readBits(r[:], 5, 60); got != want {
		t.Errorf("readBits = %#x, want %#x", got, want)
	}
}

func TestInlineIDs(t *testing.T) {
	m := twoMaps()
	m.inlineIDs = true
	rows, _, err := openBytes(t, buildMapDB2(t, m)).Maps()
	if err != nil || len(rows) != 2 || rows[0].ID != 0 || rows[1].ID != 209 {
		t.Errorf("rows = %+v, %v; the ids are the inline column", rows, err)
	}
}

func TestDuplicateIDsRefused(t *testing.T) {
	m := twoMaps()
	m.ids = []uint32{209, 209}
	if _, _, err := openBytes(t, buildMapDB2(t, m)).Maps(); err == nil {
		t.Error("two rows with one id are accepted")
	}
	m = twoMaps()
	m.copies = [][2]uint32{{209, 0}}
	if _, _, err := openBytes(t, buildMapDB2(t, m)).Maps(); err == nil {
		t.Error("a copy row under an id that exists is accepted")
	}
}

// put32 writes a little-endian number into a copy of the file.
func put32(b []byte, at int, v uint32) []byte {
	out := append([]byte(nil), b...)
	binary.LittleEndian.PutUint32(out[at:], v)
	return out
}

func put16(b []byte, at int, v uint16) []byte {
	out := append([]byte(nil), b...)
	binary.LittleEndian.PutUint16(out[at:], v)
	return out
}

// palletFile is a file with a pallet column (field 8) and a common-data column (field 9), for the
// corruption tests of the two blocks.
func palletFile(t testing.TB) []byte {
	m := twoMaps()
	m.fields = mapFields()
	m.fields[8] = tField{bitOffset: 32 * 8, bitSize: 3, comp: compPallet, packedOff: 2, packedW: 3, pallets: []uint32{0, 5, 3}}
	m.fields[9] = tField{bitOffset: 32 * 9, bitSize: 32, comp: compCommonData, common: [][2]uint32{{209, 2}}}
	return buildMapDB2(t, m)
}

// TestHostileFiles corrupts one number at a time. Open must return an error for each, never panic, and
// never allocate by the declared size: the fuzz target below covers the rest of the space.
func TestHostileFiles(t *testing.T) {
	plain := buildMapDB2(t, twoMaps())
	pallets := palletFile(t)
	metaAt := func(field int, nsec int) int { return offSections + 40*nsec + 4*26 + 24*field }
	twoSec := buildMapDB2(t, mapDB2{dirs: []string{"A", "B"}, names: []string{"A", "B"}, ids: []uint32{0, 1}, encrypted: []bool{false, false}})
	cases := []struct {
		name string
		b    []byte
		want string // a word of the error
	}{
		{"huge record count and size", put32(put32(plain, offRecordCount, math.MaxUint32), offRecordSize, math.MaxUint32), "record size"},
		{"huge record count", put32(plain, offRecordCount, math.MaxUint32), "do not fit"},
		{"huge record size", put32(plain, offRecordSize, math.MaxUint32), "record size"},
		{"record size past the cap", put32(plain, offRecordSize, maxRecordSize+1), "record size"},
		{"huge total field count", put32(plain, offTotalFields, math.MaxUint32), "fields"},
		{"no fields", put32(plain, offTotalFields, 0), "fields"},
		{"huge section count", put32(plain, offSectionCount, math.MaxUint32), "sections"},
		{"section count past the file", put32(plain, offSectionCount, maxSections), "sections"},
		{"section records past the file", put32(put32(plain, offRecordCount, math.MaxUint32/200), offSections+secRecordCount, math.MaxUint32/200), "do not fit"},
		{"section records disagree with the header", put32(plain, offSections+secRecordCount, 5), "records"},
		{"section string table past the file", put32(plain, offSections+secStringSize, math.MaxUint32), "past"},
		{"section starts past the file", put32(plain, offSections+secFileOffset, math.MaxUint32), "past"},
		{"section starts inside the header", put32(plain, offSections+secFileOffset, 10), "inside"},
		{"copy table past the file", put32(plain, offSections+secCopyCount, math.MaxUint32), "past"},
		{"id list of zero with ids in the list", put32(plain, offSections+secIDListSize, 0), "ids"},
		{"id list of the wrong size", put32(plain, offSections+secIDListSize, 5), "ids"},
		{"id list past the file", put32(plain, offSections+secIDListSize, math.MaxUint32), "past"},
		{"packed data past the record", put32(plain, offPackedData, testRecordSize), "packed data"},
		{"column meta of the wrong size", put32(plain, offColumnMeta, 24), "column meta"},
		{"column meta past the file", put32(plain, offColumnMeta, math.MaxUint32), "column meta"},
		{"pallet data of 0xFFFFFFFC bytes", put32(pallets, metaAt(8, 1)+4, 0xFFFFFFFC), "pallet data"},
		{"pallet data past the header total", put32(pallets, metaAt(8, 1)+4, 16), "pallet data"},
		{"header pallet total past the file", put32(pallets, offPalletData, math.MaxUint32), "pallet"},
		{"header pallet total larger than the columns use", put32(pallets, offPalletData, 16), "pallet data"},
		{"header pallet total smaller than the columns use", put32(pallets, offPalletData, 8), "pallet data"},
		{"common data of 0xFFFFFFF8 bytes", put32(pallets, metaAt(9, 1)+4, 0xFFFFFFF8), "common data"},
		{"header common total larger than the columns use", put32(pallets, offCommonData, 16), "common data"},
		{"header common total smaller than the columns use", put32(pallets, offCommonData, 0), "common data"},
		{"odd pallet data", put32(pallets, metaAt(8, 1)+4, 6), "pallet data"},
		{"field past the record", put16(plain, metaAt(3, 1), 3000), "past"},
		{"field wider than the record", put16(plain, metaAt(25, 1)+2, 256), "past"},
		{"field of zero bits", put16(plain, metaAt(3, 1)+2, 0), "bits"},
		{"unknown compression", put32(plain, metaAt(3, 1)+8, 9), "compression"},
		{"packed value past the record", put32(pallets, metaAt(8, 1)+12, 4000), "packed"},
		{"second section overruns", put32(twoSec, offSections+40+secFileOffset, uint32(len(twoSec))-10), "past"},
	}
	for _, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v", c.name, r)
				}
			}()
			_, err := parse(c.b)
			if err == nil {
				t.Errorf("%s: no error", c.name)
			} else if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
			}
		}()
	}
}

func TestHostileEncryptedIDCount(t *testing.T) {
	m := mapDB2{dirs: []string{"A", ""}, names: []string{"A", ""}, ids: []uint32{0, 99}, encrypted: []bool{false, true}}
	b := buildMapDB2(t, m)
	// The encrypted id list (count, then ids) sits right before the first section's data.
	start := int(binary.LittleEndian.Uint32(b[offSections+secFileOffset:])) - 8
	if got := binary.LittleEndian.Uint32(b[start:]); got != 1 {
		t.Fatalf("the encrypted id count is at the wrong place: %d", got)
	}
	if _, err := parse(put32(b, start, math.MaxUint32)); err == nil {
		t.Error("an encrypted id count of 0xFFFFFFFF is accepted")
	}
	if _, err := parse(put32(b, start, 2)); err == nil {
		t.Error("more encrypted ids than the section has records are accepted")
	}
}

func TestIDListMissingAtFileEnd(t *testing.T) {
	// One record, flag 0x4, and the section ends where the records and strings end: the id list size is
	// zero. The reader must refuse the file, not read four bytes past it.
	b := buildMapDB2(t, mapDB2{dirs: []string{"A"}, names: []string{"A"}, ids: []uint32{7}})
	b = put32(b, offSections+secIDListSize, 0)
	b = b[:len(b)-4]
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	if _, err := parse(b); err == nil {
		t.Error("a flag-0x4 file without an ID list is accepted")
	}
}

func TestHostileStringsAndIDs(t *testing.T) {
	good := buildMapDB2(t, twoMaps())
	recStart := int(binary.LittleEndian.Uint32(good[offSections+secFileOffset:]))
	for name, patch := range map[string]func(b []byte){
		"string offset past the block": func(b []byte) { binary.LittleEndian.PutUint32(b[recStart:], 0x00ffffff) },
		"string offset into the records": func(b []byte) {
			binary.LittleEndian.PutUint32(b[recStart:], 8) // the record's own bytes
		},
		"string offset backwards": func(b []byte) { binary.LittleEndian.PutUint32(b[recStart:], 0xfffffff0) },
		"unterminated string": func(b []byte) {
			b[len(b)-4*2-1] = 'x' // the last string byte (before the two ids) is its NUL
		},
	} {
		b := append([]byte(nil), good...)
		patch(b)
		g, err := parse(b)
		if err != nil {
			t.Errorf("%s: parse error %v", name, err)
			continue
		}
		if _, _, err := g.Maps(); err == nil {
			t.Errorf("%s: Maps returns rows from a corrupt file", name)
		}
	}
}

func TestInlineIDColumnThatIsCommonData(t *testing.T) {
	// The id column is common data, keyed by the id: reading it asks for the id, which asks for the column.
	m := twoMaps()
	m.inlineIDs = true
	m.fields = mapFields()
	m.fields[2] = tField{bitOffset: 64, bitSize: 32, comp: compCommonData, common: [][2]uint32{{1, 1}}}
	f := openBytes(t, buildMapDB2(t, m))
	if _, _, err := f.Maps(); err == nil {
		t.Error("an id column that is common data is accepted")
	}
}

func TestEveryTruncationAndByteFlip(t *testing.T) {
	files := map[string][]byte{
		"plain":   buildMapDB2(t, twoMaps()),
		"pallets": palletFile(t),
		"enc": buildMapDB2(t, mapDB2{dirs: []string{"A", ""}, names: []string{"A", ""}, ids: []uint32{0, 99},
			encrypted: []bool{false, true}, copies: [][2]uint32{{5, 0}}}),
	}
	try := func(name string, b []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s: panic %v", name, r)
			}
		}()
		if f, err := parse(b); err == nil {
			f.Maps()
		}
	}
	for name, b := range files {
		for n := range len(b) {
			try(name+" truncated", b[:n])
		}
		for i := range b {
			for _, v := range []byte{0x00, 0x7f, 0x80, 0xff} {
				c := append([]byte(nil), b...)
				c[i] = v
				try(name+" flipped", c)
			}
		}
	}
}

// FuzzParse feeds arbitrary bytes to the reader: it must return a File or an error, never panic, and
// never allocate more than the input's size allows. The seeds are hand-built files, not client data:
//
//	go test -fuzz=FuzzParse -fuzztime=60s ./internal/db2/
func FuzzParse(f *testing.F) {
	for _, m := range []mapDB2{
		twoMaps(),
		{wdc4: true, dirs: []string{"Azeroth"}, names: []string{"E"}, ids: []uint32{0}},
		{dirs: []string{"A", ""}, names: []string{"A", ""}, ids: []uint32{0, 99}, encrypted: []bool{false, true}, copies: [][2]uint32{{5, 0}}},
		{dirs: []string{"A", "B"}, names: []string{"A", "B"}, ids: []uint32{1, 2}, inlineIDs: true},
	} {
		f.Add(buildMapDB2(f, m))
	}
	f.Add(palletFile(f))
	f.Add([]byte("WDC5"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		file, err := parse(b)
		if err != nil {
			return
		}
		file.Maps()
	})
}
