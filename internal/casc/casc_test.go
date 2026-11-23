package casc

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testBlteSingle builds the single-chunk BLTE form: magic, a zero header size, then the mode byte and its data.
func testBlteSingle(mode byte, payload []byte) []byte {
	b := make([]byte, 8, 9+len(payload))
	copy(b, "BLTE")
	b = append(b, mode)
	return append(b, payload...)
}

func testBlteZ(payload []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		panic(err)
	}
	w.Close()
	return testBlteSingle('Z', buf.Bytes())
}

type testChunk struct {
	mode    byte
	data    []byte
	content int
}

func testBlteMulti(chunks []testChunk) []byte {
	b := make([]byte, 12, 12+24*len(chunks))
	copy(b, "BLTE")
	binary.BigEndian.PutUint32(b[4:], uint32(12+24*len(chunks)))
	b[8] = 0x0F
	n := len(chunks)
	b[9], b[10], b[11] = byte(n>>16), byte(n>>8), byte(n)
	for _, c := range chunks {
		var e [24]byte
		binary.BigEndian.PutUint32(e[0:], uint32(1+len(c.data)))
		binary.BigEndian.PutUint32(e[4:], uint32(c.content))
		b = append(b, e[:]...)
	}
	for _, c := range chunks {
		b = append(b, c.mode)
		b = append(b, c.data...)
	}
	return b
}

func writeBuildInfo(t *testing.T, dir string, rows ...string) {
	t.Helper()
	lines := append([]string{
		"Branch!STRING:0|Active!DEC:1|Build Key!HEX:16|Product!STRING:0|Version!STRING:0",
	}, rows...)
	s := strings.Join(lines, "\r\n") + "\r\n"
	if err := os.WriteFile(filepath.Join(dir, ".build.info"), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildInfoPicksActiveRow(t *testing.T) {
	const (
		inactive = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		active   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		other    = "cccccccccccccccccccccccccccccccc"
	)
	info, err := ParseBuildInfo([]byte(strings.Join([]string{
		"Branch!STRING:0|Active!DEC:1|Build Key!HEX:16|Product!STRING:0|Version!STRING:0",
		"eu|0|" + inactive + "|wow|1.0.0",
		"us|1|" + active + "|wow|1.18.1",
		"xx|0|" + other + "|ptr|1.18.2",
		"",
	}, "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	row, err := info.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if got := row.BuildKey(); got != active {
		t.Errorf("active row build key = %q, want %q", got, active)
	}
	if got := row.Version(); got != "1.18.1" {
		t.Errorf("active row version = %q", got)
	}
	// A named product wins over the active row, and picks that product's row.
	row, err = info.Select("ptr")
	if err != nil {
		t.Fatal(err)
	}
	if got := row.BuildKey(); got != other {
		t.Errorf("product ptr build key = %q, want %q", got, other)
	}
	// A named product with two rows uses the active one.
	row, err = info.Select("WOW")
	if err != nil {
		t.Fatal(err)
	}
	if got := row.BuildKey(); got != active {
		t.Errorf("product wow build key = %q, want the active %q", got, active)
	}
}

func TestBuildInfoSeveralActiveNeedsProduct(t *testing.T) {
	info, err := ParseBuildInfo([]byte(strings.Join([]string{
		"Branch!STRING:0|Active!DEC:1|Build Key!HEX:16|Product!STRING:0|Version!STRING:0",
		"eu|1|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|wow|1.18.1",
		"us|1|bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb|ptr|1.18.2",
		"",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = info.Select("")
	if err == nil {
		t.Fatal("two active rows without a product did not error")
	}
	for _, want := range []string{"wow", "ptr"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	row, err := info.Select("ptr")
	if err != nil {
		t.Fatal(err)
	}
	if got := row.BuildKey(); got != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("product ptr build key = %q", got)
	}
	if _, err := info.Select("nope"); err == nil || !strings.Contains(err.Error(), "wow") || !strings.Contains(err.Error(), "ptr") {
		t.Errorf("unknown product error = %v, want one naming both products", err)
	}
}

type testIdxEntry struct {
	key     [9]byte
	archive uint32
	offset  uint64
	size    uint32
}

// testIdx builds a version 7 index file: the 16-byte file header in its guarded block, 8 bytes of padding, then the
// entry array in its own guarded block. The hashes are placeholders; the reader does not verify them.
func testIdx(bucket byte, entries []testIdxEntry) []byte {
	b := make([]byte, 0x28, 0x28+18*len(entries))
	binary.LittleEndian.PutUint32(b[0:], 16)
	binary.LittleEndian.PutUint32(b[4:], 0x12345678)
	binary.LittleEndian.PutUint16(b[8:], 7)
	b[10] = bucket
	b[11] = 0
	b[12] = 4 // SpanSizeBytes
	b[13] = 5 // SpanOffsetBytes
	b[14] = 9 // KeyBytes
	b[15] = 30
	binary.LittleEndian.PutUint64(b[16:], 1023<<30)
	binary.LittleEndian.PutUint32(b[0x20:], uint32(18*len(entries)))
	for _, e := range entries {
		e40 := uint64(e.archive)<<30 | e.offset&(1<<30-1)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], e.size)
		b = append(b, e.key[:]...)
		b = append(b, byte(e40>>32), byte(e40>>24), byte(e40>>16), byte(e40>>8), byte(e40))
		b = append(b, sz[:]...)
	}
	return b
}

func writeIdx(t *testing.T, dir string, bucket byte, version uint32, entries []testIdxEntry) {
	t.Helper()
	name := fmt.Sprintf("%02x%08x.idx", bucket, version)
	if err := os.WriteFile(filepath.Join(dir, name), testIdx(bucket, entries), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIndexNewestVersionPerBucket(t *testing.T) {
	dir := t.TempDir()
	keyA := [9]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	keyB := [9]byte{9, 8, 7, 6, 5, 4, 3, 2, 1}
	keyC := [9]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22, 0x33}
	// The older file of bucket 0 maps keyA somewhere else; only the newest version of a bucket may count.
	writeIdx(t, dir, 0, 0x1b, []testIdxEntry{{keyA, 2, 0x99, 400}})
	writeIdx(t, dir, 0, 0x1f, []testIdxEntry{{keyA, 1, 0x123456, 500}, {keyB, 3, 0x2000, 600}})
	writeIdx(t, dir, 1, 5, []testIdxEntry{{keyC, 4, 0x3000, 700}})
	if err := os.WriteFile(filepath.Join(dir, "shmem"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := LoadIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := append(keyA[:], 0xFF, 0xFF) // a full 16-byte EKey is matched by its first 9 bytes
	e, ok := idx.lookup(a)
	if !ok {
		t.Fatal("keyA not found")
	}
	if e.archive != 1 || e.offset != 0x123456 || e.size != 500 {
		t.Errorf("keyA = archive %d offset %#x size %d; the newest bucket 0 file has archive 1 offset 0x123456 size 500",
			e.archive, e.offset, e.size)
	}
	if e, ok := idx.lookup(keyB[:]); !ok || e.archive != 3 || e.size != 600 {
		t.Errorf("keyB = %+v, %v", e, ok)
	}
	if e, ok := idx.lookup(keyC[:]); !ok || e.archive != 4 || e.offset != 0x3000 {
		t.Errorf("keyC = %+v, %v", e, ok)
	}
	if _, ok := idx.lookup([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8}); ok {
		t.Error("an unknown key resolved")
	}
}

func TestBLTEModesNZ(t *testing.T) {
	plain := []byte("the minimap texture bytes, not text really")
	got, err := DecodeBLTE(testBlteSingle('N', plain))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("single N chunk = %q", got)
	}
	got, err = DecodeBLTE(testBlteZ(plain))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("single Z chunk = %q, want %q", got, plain)
	}
	multi := testBlteMulti([]testChunk{
		{mode: 'N', data: []byte("hello "), content: 6},
		{mode: 'Z', data: func() []byte {
			var buf bytes.Buffer
			w := zlib.NewWriter(&buf)
			w.Write(plain)
			w.Close()
			return buf.Bytes()
		}(), content: len(plain)},
		{mode: 'N', data: []byte{0, 1, 2, 3}, content: 4},
	})
	got, err = DecodeBLTE(multi)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte("hello "), plain...), 0, 1, 2, 3)
	if !bytes.Equal(got, want) {
		t.Errorf("multi chunk = %q, want %q", got, want)
	}
	if empty, err := DecodeBLTE(testBlteSingle('N', nil)); err != nil || len(empty) != 0 {
		t.Errorf("empty single chunk = %q, %v", empty, err)
	}
}

func TestBLTEEncryptedChunk(t *testing.T) {
	_, err := DecodeBLTE(testBlteMulti([]testChunk{{mode: 'E', data: []byte("salsa20 ciphertext"), content: 18}}))
	if !errors.Is(err, ErrEncrypted) {
		t.Errorf("E chunk error = %v, want ErrEncrypted", err)
	}
}

func TestBLTEHostileSizes(t *testing.T) {
	valid := testBlteMulti([]testChunk{{mode: 'N', data: []byte("abc"), content: 3}})
	corrupt := func(edit func(b []byte) []byte) []byte {
		return edit(append([]byte(nil), valid...))
	}
	setBE32 := func(b []byte, off int, v uint32) []byte {
		binary.BigEndian.PutUint32(b[off:], v)
		return b
	}
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short", []byte("BLTE\x00\x00\x00")},
		{"bad magic", []byte("BLTX\x00\x00\x00\x0c\x0f\x00\x00\x01")},
		{"header too small", corrupt(func(b []byte) []byte { return setBE32(b, 4, 11) })},
		{"header past the input", corrupt(func(b []byte) []byte { return setBE32(b, 4, 1<<20) })},
		{"chunk count over the cap", corrupt(func(b []byte) []byte {
			b[9], b[10], b[11] = 1, 0, 1 // 65536 chunks is allowed; one more is not
			return b
		})},
		{"table size mismatch", corrupt(func(b []byte) []byte { return setBE32(b, 4, 13+24) })},
		{"zero encoded size", corrupt(func(b []byte) []byte { return setBE32(b, 12, 0) })},
		{"encoded size past the input", corrupt(func(b []byte) []byte { return setBE32(b, 12, 1<<16) })},
		{"content past the decoded cap", corrupt(func(b []byte) []byte { return setBE32(b, 16, 300<<20) })},
		{"two chunks past the decoded cap", testBlteMulti([]testChunk{
			{mode: 'N', data: nil, content: 200 << 20},
			{mode: 'N', data: nil, content: 200 << 20},
		})},
		{"unknown mode", corrupt(func(b []byte) []byte { b[12+24] = 'Q'; return b })},
		{"N chunk length disagrees", corrupt(func(b []byte) []byte { return setBE32(b, 16, 4) })},
		{"truncated chunk data", valid[:len(valid)-1]},
		{"Z chunk content disagrees", testBlteMulti([]testChunk{{mode: 'Z', data: testBlteZ([]byte("abc"))[9:], content: 4}})},
		{"corrupt zlib", testBlteSingle('Z', []byte("not zlib at all"))},
	}
	overCap := map[string]bool{
		"content past the decoded cap":    true,
		"two chunks past the decoded cap": true,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeBLTE(tc.data)
			if err == nil {
				t.Fatalf("decoded %d bytes, want an error", len(got))
			}
			if got != nil {
				t.Errorf("returned %d bytes with the error", len(got))
			}
			if overCap[tc.name] && !errors.Is(err, ErrTooLarge) {
				t.Errorf("error = %v, want ErrTooLarge", err)
			}
		})
	}
}

func writeConfig(t *testing.T, dir, buildKey, text string) {
	t.Helper()
	p := filepath.Join(dir, "Data", "config", buildKey[:2], buildKey[2:4], buildKey)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadEKeyRoundTrip(t *testing.T) {
	const (
		buildKey = "00112233445566778899aabbccddeeff"
		encCKey  = "11111111111111111111111111111111"
		rootCKey = "22222222222222222222222222222222"
	)
	ekey, err := hex.DecodeString("a1b2c3d4e5f60718293a4b5c6d7e8f90")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("one encoded file, BLTE compressed")
	otherKey := [9]byte{0xFE, 0xED, 0xFA, 0xCE, 1, 2, 3, 4, 5}
	otherPayload := []byte("the second span")

	dir := t.TempDir()
	writeBuildInfo(t, dir, "us|1|"+buildKey+"|wow|1.18.1")
	writeConfig(t, dir, buildKey, strings.Join([]string{
		"# Build Configuration",
		"",
		"root = " + rootCKey,
		"encoding = " + encCKey + " " + hex.EncodeToString(ekey),
		"encoding-size = 10 20",
	}, "\n")+"\n")

	data := filepath.Join(dir, "Data", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}

	// Two spans in data.000, each behind the 30-byte local header.
	first := testBlteZ(payload)
	second := testBlteSingle('N', otherPayload)
	span := func(blte []byte) []byte {
		b := make([]byte, 30)
		binary.LittleEndian.PutUint32(b[16:], uint32(30+len(blte)))
		return append(b, blte...)
	}
	archive := append(span(first), span(second)...)
	if err := os.WriteFile(filepath.Join(data, "data.000"), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	firstSize := uint32(30 + len(first))
	secondSize := uint32(30 + len(second))
	writeIdx(t, data, 0, 0x1b, []testIdxEntry{
		{key: key9(ekey), archive: 0, offset: 0, size: firstSize},
		{key: otherKey, archive: 0, offset: uint64(firstSize), size: secondSize},
	})

	s, err := Open(dir, "wow")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.ReadEKey(ekey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("ReadEKey = %q, want %q", got, payload)
	}
	got, err = s.ReadEKey(otherKey[:])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, otherPayload) {
		t.Errorf("second ReadEKey = %q, want %q", got, otherPayload)
	}
	if _, err := s.ReadEKey(bytes.Repeat([]byte{0x77}, 16)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown EKey error = %v, want ErrNotFound", err)
	}
	cfg := s.Config()
	if cfg == nil {
		t.Fatal("Config is nil")
	}
	if hex.EncodeToString(cfg.Encoding.EKey) != hex.EncodeToString(ekey) {
		t.Errorf("config encoding EKey = %x", cfg.Encoding.EKey)
	}
	if hex.EncodeToString(cfg.Root) != rootCKey {
		t.Errorf("config root = %x", cfg.Root)
	}
}

func key9(ekey []byte) [9]byte {
	var k [9]byte
	copy(k[:], ekey)
	return k
}

func FuzzBLTE(f *testing.F) {
	f.Add([]byte("BLTE"))
	f.Add(testBlteSingle('N', []byte("hello")))
	f.Add(testBlteSingle('N', nil))
	f.Add(testBlteZ([]byte("hellohellohello")))
	f.Add(testBlteMulti([]testChunk{
		{mode: 'N', data: []byte("abc"), content: 3},
		{mode: 'Z', data: func() []byte {
			var buf bytes.Buffer
			w := zlib.NewWriter(&buf)
			w.Write([]byte("deflated"))
			w.Close()
			return buf.Bytes()
		}(), content: 8},
	}))
	f.Add(testBlteMulti([]testChunk{{mode: 'E', data: []byte("keyname"), content: 7}}))
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := DecodeBLTE(data)
		if err == nil && len(out) > maxBLTESize {
			t.Fatalf("decoded %d bytes over the %d cap", len(out), maxBLTESize)
		}
	})
}
