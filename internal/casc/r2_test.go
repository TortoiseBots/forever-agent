package casc

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The layout of the encoding file and the root file is the public CASC description: the encoding file's "EN" header,
// per-page first-key index and variable length CKey -> EKey entries; the root file's "TSFM" header, then blocks of a
// record count, content and locale flags, FileDataID deltas, content keys and (unless the block says NoNameHash) name
// hashes. Every test builds its bytes here; no client data is used.

// testEncEntry is one CKey -> EKey row of a test encoding file.
type testEncEntry struct {
	ckey []byte
	ekey []byte
}

// testEncodingFile builds an encoding file with the given pages. Each page holds its entries' rows (one encoding key
// each) and is padded to the 1 KiB page size. The caller gives the pages in sorted CKey order, as the format requires.
func testEncodingFile(pages [][]testEncEntry) []byte {
	const pageSize = 1024
	b := make([]byte, 0x16)
	copy(b, "EN")
	b[2] = 1                             // version
	b[3] = 16                            // CKey size
	b[4] = 16                            // EKey size
	binary.BigEndian.PutUint16(b[5:], 1) // CKey page size, KiB
	binary.BigEndian.PutUint16(b[7:], 1) // EKey spec page size, KiB
	binary.BigEndian.PutUint32(b[9:], uint32(len(pages)))
	binary.BigEndian.PutUint32(b[13:], 0) // no EKey spec pages
	b[17] = 0                             // flags
	binary.BigEndian.PutUint32(b[18:], 0) // no ESpec strings
	for _, page := range pages {
		first := make([]byte, 16)
		if len(page) > 0 {
			copy(first, page[0].ckey)
		}
		b = append(b, first...)
		b = append(b, make([]byte, 16)...) // page checksum: not verified
	}
	for _, page := range pages {
		body := make([]byte, 0, pageSize)
		for _, e := range page {
			row := make([]byte, 6)
			row[0] = 1 // one encoding key
			body = append(body, row...)
			body = append(body, e.ckey...)
			body = append(body, e.ekey...)
		}
		if len(body) > pageSize {
			panic("test encoding page is over its size")
		}
		b = append(b, body...)
		b = append(b, make([]byte, pageSize-len(body))...)
	}
	return b
}

// testCKey returns a distinct 16-byte key.
func testCKey(n byte) []byte { return bytes.Repeat([]byte{n}, 16) }

func TestEncodingLookup(t *testing.T) {
	// Two pages, sorted by CKey.
	page1 := []testEncEntry{
		{ckey: testCKey(0x10), ekey: testCKey(0xA0)},
		{ckey: testCKey(0x20), ekey: testCKey(0xA1)},
	}
	page2 := []testEncEntry{
		{ckey: testCKey(0x30), ekey: testCKey(0xA2)},
		{ckey: testCKey(0x40), ekey: testCKey(0xA3)},
	}
	enc, err := ParseEncoding(testEncodingFile([][]testEncEntry{page1, page2}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []testEncEntry{
		{ckey: testCKey(0x10), ekey: testCKey(0xA0)},
		{ckey: testCKey(0x20), ekey: testCKey(0xA1)},
		{ckey: testCKey(0x30), ekey: testCKey(0xA2)},
		{ckey: testCKey(0x40), ekey: testCKey(0xA3)},
	} {
		got, err := enc.EKeyFor(want.ckey)
		if err != nil {
			t.Fatalf("EKeyFor %x: %v", want.ckey[:2], err)
		}
		if !bytes.Equal(got, want.ekey) {
			t.Errorf("EKeyFor %x = %x, want %x", want.ckey[:2], got[:2], want.ekey[:2])
		}
	}
	// A key before the first page, between two entries and after the last page is not found.
	for _, ckey := range [][]byte{testCKey(0x01), testCKey(0x25), testCKey(0xFF)} {
		if _, err := enc.EKeyFor(ckey); !errors.Is(err, ErrNotFound) {
			t.Errorf("EKeyFor %x error = %v, want ErrNotFound", ckey[:2], err)
		}
	}
}

func TestEncodingHostile(t *testing.T) {
	valid := testEncodingFile([][]testEncEntry{{
		{ckey: testCKey(0x10), ekey: testCKey(0xA0)},
	}})
	cases := map[string][]byte{
		"empty":                nil,
		"short header":         valid[:0x15],
		"bad magic":            append([]byte("EX"), valid[2:]...),
		"bad version":          withBE8(valid, 2, 2),
		"bad ckey size":        withBE8(valid, 3, 8),
		"truncated page":       valid[:len(valid)-1],
		"page count overflow":  withBE32(valid, 9, 1<<20),
		"page size zero":       withBE16(valid, 5, 0),
		"espec block overflow": withBE32(valid, 18, 1<<20),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEncoding(data); err == nil {
				t.Fatal("parsed, want an error")
			}
		})
	}
}

// withBE32 returns a copy of data with a big-endian 32-bit value at off.
func withBE32(data []byte, off int, v uint32) []byte {
	b := append([]byte{}, data...)
	binary.BigEndian.PutUint32(b[off:], v)
	return b
}

// withBE16 returns a copy of data with a big-endian 16-bit value at off.
func withBE16(data []byte, off int, v uint16) []byte {
	b := append([]byte{}, data...)
	binary.BigEndian.PutUint16(b[off:], v)
	return b
}

// withBE8 returns a copy of data with byte v at off.
func withBE8(data []byte, off int, v byte) []byte {
	b := append([]byte{}, data...)
	b[off] = v
	return b
}

// testRootBlock is one block of a test root file.
type testRootBlock struct {
	contentFlags uint32
	localeFlags  uint32
	ids          []uint32 // the real FileDataIDs; the deltas are computed from them
	ckeys        [][]byte
	names        []uint64
}

// testRoot builds a TSFM root file. version 0 is the 8.2 header (total and named counts after the magic); version 1
// and 2 are the 10.1.7 header (a header size and a version before the counts), and version 2 uses the expanded block
// header (locale first, then the content flags in three fields).
func testRoot(version uint32, blocks []testRootBlock) []byte {
	total, named := 0, 0
	for _, bl := range blocks {
		total += len(bl.ids)
		if bl.contentFlags&noNameHash == 0 {
			named += len(bl.ids)
		}
	}
	le := binary.LittleEndian
	body := []byte{}
	for _, bl := range blocks {
		var head [17]byte
		le.PutUint32(head[0:], uint32(len(bl.ids)))
		if version == 2 {
			le.PutUint32(head[4:], bl.localeFlags)
			le.PutUint32(head[8:], bl.contentFlags&0x1FFFF)
			le.PutUint32(head[12:], 0)
			head[16] = byte(bl.contentFlags >> 17)
			body = append(body, head[:17]...)
		} else {
			le.PutUint32(head[4:], bl.contentFlags)
			le.PutUint32(head[8:], bl.localeFlags)
			body = append(body, head[:12]...)
		}
		for i, id := range bl.ids {
			delta := int32(id)
			if i > 0 {
				delta = int32(id) - int32(bl.ids[i-1]) - 1
			}
			var d [4]byte
			le.PutUint32(d[:], uint32(delta))
			body = append(body, d[:]...)
		}
		for _, ck := range bl.ckeys {
			if len(ck) != 16 {
				panic("test root ckey is not 16 bytes")
			}
			body = append(body, ck...)
		}
		if bl.contentFlags&noNameHash == 0 {
			for _, n := range bl.names {
				var d [8]byte
				le.PutUint64(d[:], n)
				body = append(body, d[:]...)
			}
		}
	}
	b := []byte("TSFM")
	switch version {
	case 0:
		var head [8]byte
		le.PutUint32(head[0:], uint32(total))
		le.PutUint32(head[4:], uint32(named))
		b = append(b, head[:8]...)
	case 1, 2:
		var head [16]byte
		le.PutUint32(head[0:], 20) // header size: magic, size, version and two counts
		le.PutUint32(head[4:], version)
		le.PutUint32(head[8:], uint32(total))
		le.PutUint32(head[12:], uint32(named))
		b = append(b, head[:16]...)
	default:
		panic("test root version")
	}
	return append(b, body...)
}

func TestRootDeltaDecoding(t *testing.T) {
	// Consecutive ids (delta 0) carry the +1; the first id is the first delta and a gap adds to it.
	ids := []uint32{100, 101, 103, 104}
	data := testRoot(0, []testRootBlock{{
		contentFlags: 0x08,
		localeFlags:  0x02,
		ids:          ids,
		ckeys: [][]byte{
			testCKey(0x10), testCKey(0x11), testCKey(0x12), testCKey(0x13),
		},
		names: []uint64{0xA0, 0xA1, 0xA2, 0xA3},
	}})
	root, err := ParseRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.files) != len(ids) {
		t.Fatalf("%d files, want %d", len(root.files), len(ids))
	}
	want := map[uint32]byte{100: 0x10, 101: 0x11, 103: 0x12, 104: 0x13}
	for id, first := range want {
		e, ok := root.files[id]
		if !ok {
			t.Fatalf("FileDataID %d missing", id)
		}
		if e.ckey[0] != first {
			t.Errorf("FileDataID %d ckey = %x, want one starting %x", id, e.ckey[:2], first)
		}
	}
	if _, ok := root.files[102]; ok {
		t.Error("FileDataID 102 exists, the delta gap is not honoured")
	}
}

func TestRootLocaleFilter(t *testing.T) {
	const (
		lowViolence = 0x80
	)
	block := func(id uint32, ck byte, content, locale uint32) testRootBlock {
		bl := testRootBlock{contentFlags: content, localeFlags: locale, ids: []uint32{id}, ckeys: [][]byte{testCKey(ck)}}
		if content&noNameHash == 0 {
			bl.names = []uint64{uint64(id)}
		}
		return bl
	}
	blocks := []testRootBlock{
		// The low-violence entry first, the plain one replaces it.
		block(10, 0x99, 0x08|lowViolence, 0x02),
		block(10, 0x21, 0x08, 0x02),
		// The other way round: the low-violence entry must not replace the plain one.
		block(11, 0x22, 0x08, 0x02),
		block(11, 0x98, 0x08|lowViolence, 0x02),
		// All locales and enUS are kept, enCN only is dropped.
		block(12, 0x23, 0x08, 0),
		block(13, 0x24, 0x08, 0x400),
		block(14, 0x25, 0x08, 0x02),
		// A block without name hashes; if its absent array were read, the next block would misparse.
		block(15, 0x26, 0x08|noNameHash, 0x02),
		block(16, 0x27, 0x08, 0x02),
	}
	root, err := ParseRoot(testRoot(0, blocks))
	if err != nil {
		t.Fatal(err)
	}
	if len(root.files) != 6 {
		t.Errorf("%d files, want 6 (10, 11, 12, 14, 15, 16)", len(root.files))
	}
	for id, first := range map[uint32]byte{10: 0x21, 11: 0x22, 12: 0x23, 14: 0x25, 15: 0x26, 16: 0x27} {
		e, ok := root.files[id]
		if !ok {
			t.Errorf("FileDataID %d missing", id)
			continue
		}
		if e.ckey[0] != first {
			t.Errorf("FileDataID %d ckey starts %#x, want %#x", id, e.ckey[0], first)
		}
	}
	if _, ok := root.files[13]; ok {
		t.Error("the enCN only entry is kept")
	}
}

func TestRootVersion2Blocks(t *testing.T) {
	// The 10.1.7 header with version 2's expanded block header: locale first, then the content flags in three fields.
	// 0x60008 needs bits below 17 and above: the split reassembles it.
	data := testRoot(2, []testRootBlock{{
		contentFlags: 0x60008,
		localeFlags:  0x02,
		ids:          []uint32{7, 9},
		ckeys:        [][]byte{testCKey(0x31), testCKey(0x32)},
		names:        []uint64{1, 2},
	}})
	root, err := ParseRoot(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.files) != 2 {
		t.Fatalf("%d files, want 2", len(root.files))
	}
	if got := root.files[9].ckey[0]; got != 0x32 {
		t.Errorf("FileDataID 9 ckey starts %#x, want 0x32", got)
	}
}

func TestRootHostileCounts(t *testing.T) {
	valid := testRoot(0, []testRootBlock{{
		contentFlags: 0x08,
		localeFlags:  0x02,
		ids:          []uint32{100},
		ckeys:        [][]byte{testCKey(0x10)},
		names:        []uint64{1},
	}})
	cases := map[string][]byte{
		"empty":                  nil,
		"short":                  []byte("TSF"),
		"bad magic":              append([]byte("TSFX"), valid[4:]...),
		"header only":            valid[:12],
		"records past the input": withLE32(valid, 12, 1<<28),
		"total too high":         withLE32(valid, 4, 1000),
		"named too high":         withLE32(valid, 8, 1000),
		"truncated deltas":       valid[:16],
		"truncated ckeys":        valid[:len(valid)-8],
		"delta goes negative":    withLE32(valid, 12, 0xFFFFFFFF),
		"version 2 header size":  withLE32(valid, 4, 0x7FFFFFFF),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			root, err := ParseRoot(data)
			if err == nil {
				t.Fatalf("parsed %d files, want an error", len(root.files))
			}
		})
	}
}

// withLE32 returns a copy of data with a little-endian 32-bit value at off.
func withLE32(data []byte, off int, v uint32) []byte {
	b := append([]byte{}, data...)
	binary.LittleEndian.PutUint32(b[off:], v)
	return b
}

func TestReadFileDataIDEndToEnd(t *testing.T) {
	payloads := [][]byte{
		[]byte("the minimap texture of one outdoors tile"),
		[]byte("another file the root names"),
	}
	var (
		spans   [][]byte // the 30-byte local header plus the BLTE stream
		entries []testIdxEntry
	)
	// addSpan appends a BLTE-encoded payload to data.000 and returns its content and encoding keys.
	addSpan := func(payload []byte, compressed bool) (ckey, ekey []byte) {
		encoded := testBlteSingle('N', payload)
		if compressed {
			encoded = testBlteZ(payload)
		}
		ckey = sum16(payload)
		ekey = sum16(encoded)
		offset := 0
		for _, s := range spans {
			offset += len(s)
		}
		span := make([]byte, 30)
		binary.LittleEndian.PutUint32(span[16:], uint32(30+len(encoded)))
		span = append(span, encoded...)
		entries = append(entries, testIdxEntry{key: key9(ekey), archive: 0, offset: uint64(offset), size: uint32(len(span))})
		spans = append(spans, span)
		return ckey, ekey
	}

	// The two game files, then the root naming their FileDataIDs, then the encoding file naming all three.
	ckey1, ekey1 := addSpan(payloads[0], true)
	ckey2, ekey2 := addSpan(payloads[1], false)

	rootBytes := testRoot(0, []testRootBlock{{
		contentFlags: 0x08,
		localeFlags:  0x02,
		ids:          []uint32{200000, 200001},
		ckeys:        [][]byte{ckey1, ckey2},
		names:        []uint64{1, 2},
	}})
	rootCKey := sum16(rootBytes)
	rootEncoded := testBlteSingle('N', rootBytes)
	rootEKey := sum16(rootEncoded)
	offset := 0
	for _, s := range spans {
		offset += len(s)
	}
	rootSpan := make([]byte, 30)
	binary.LittleEndian.PutUint32(rootSpan[16:], uint32(30+len(rootEncoded)))
	rootSpan = append(rootSpan, rootEncoded...)
	entries = append(entries, testIdxEntry{key: key9(rootEKey), archive: 0, offset: uint64(offset), size: uint32(len(rootSpan))})
	spans = append(spans, rootSpan)

	encEntries := []testEncEntry{
		{ckey: rootCKey, ekey: rootEKey},
		{ckey: ckey1, ekey: ekey1},
		{ckey: ckey2, ekey: ekey2},
	}
	sort.Slice(encEntries, func(i, j int) bool { return bytes.Compare(encEntries[i].ckey, encEntries[j].ckey) < 0 })
	encBytes := testEncodingFile([][]testEncEntry{encEntries})
	encCKey := sum16(encBytes)
	encEncoded := testBlteZ(encBytes)
	encEKey := sum16(encEncoded)
	offset = 0
	for _, s := range spans {
		offset += len(s)
	}
	encSpan := make([]byte, 30)
	binary.LittleEndian.PutUint32(encSpan[16:], uint32(30+len(encEncoded)))
	encSpan = append(encSpan, encEncoded...)
	entries = append(entries, testIdxEntry{key: key9(encEKey), archive: 0, offset: uint64(offset), size: uint32(len(encSpan))})
	spans = append(spans, encSpan)

	const buildKey = "00112233445566778899aabbccddeeff"
	dir := t.TempDir()
	writeBuildInfo(t, dir, "us|1|"+buildKey+"|wow|1.18.1")
	writeConfig(t, dir, buildKey, strings.Join([]string{
		"root = " + hex.EncodeToString(rootCKey),
		"encoding = " + hex.EncodeToString(encCKey) + " " + hex.EncodeToString(encEKey),
	}, "\n")+"\n")
	data := filepath.Join(dir, "Data", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := []byte{}
	for _, s := range spans {
		archive = append(archive, s...)
	}
	if err := os.WriteFile(filepath.Join(data, "data.000"), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	writeIdx(t, data, 0, 0x1b, entries)

	s, err := Open(dir, "wow")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, id := range []uint32{200000, 200001} {
		got, err := s.ReadFileDataID(id)
		if err != nil {
			t.Fatalf("ReadFileDataID %d: %v", id, err)
		}
		if !bytes.Equal(got, payloads[i]) {
			t.Errorf("ReadFileDataID %d = %q, want %q", id, got, payloads[i])
		}
	}
	if _, err := s.ReadFileDataID(1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown FileDataID error = %v, want ErrNotFound", err)
	}
}

// sum16 is the 16-byte MD5 of data, the CKey and EKey of the test files.
func sum16(data []byte) []byte {
	s := md5.Sum(data)
	return s[:]
}

func FuzzRoot(f *testing.F) {
	f.Add(testRoot(0, nil))
	f.Add(testRoot(1, []testRootBlock{{
		contentFlags: 0x08,
		localeFlags:  0x02,
		ids:          []uint32{1, 2, 3},
		ckeys:        [][]byte{testCKey(1), testCKey(2), testCKey(3)},
		names:        []uint64{1, 2, 3},
	}}))
	f.Add(testRoot(2, []testRootBlock{{
		contentFlags: 0x08,
		localeFlags:  0,
		ids:          []uint32{9},
		ckeys:        [][]byte{testCKey(9)},
		names:        []uint64{9},
	}}))
	f.Add([]byte("TSFM"))
	f.Fuzz(func(t *testing.T, data []byte) {
		root, err := ParseRoot(data)
		if err == nil && len(root.files) > len(data)/20 {
			t.Fatalf("%d files from %d bytes", len(root.files), len(data))
		}
	})
}

func FuzzEncoding(f *testing.F) {
	f.Add(testEncodingFile([][]testEncEntry{{}}))
	f.Add(testEncodingFile([][]testEncEntry{{
		{ckey: testCKey(0x10), ekey: testCKey(0xA0)},
	}}))
	f.Fuzz(func(t *testing.T, data []byte) {
		enc, err := ParseEncoding(data)
		if err != nil {
			return
		}
		if _, err := enc.EKeyFor(testCKey(0x10)); err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatalf("EKeyFor on parsed data: %v", err)
		}
	})
}
