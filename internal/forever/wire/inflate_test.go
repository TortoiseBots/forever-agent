package wire

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// compressedBody wraps deflate bytes in the server's three-uint32 header.
func compressedBody(packet, deflated []byte) []byte {
	out := make([]byte, 12, 12+len(deflated))
	binary.LittleEndian.PutUint32(out[0:4], uint32(len(packet)))
	binary.LittleEndian.PutUint32(out[4:8], adler32Seed(compressionSeed, packet))
	binary.LittleEndian.PutUint32(out[8:12], adler32Seed(compressionSeed, deflated))
	return append(out, deflated...)
}

func TestInflateTwoPacketsInOneStream(t *testing.T) {
	// build the two compressed bodies by hand: the stream is cumulative, so body 2 is the bytes added by the
	// second Flush
	var sink bytes.Buffer
	w, err := flate.NewWriter(&sink, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	p1 := append([]byte{0x0E, 0x00, 0x4E, 0x00}, bytes.Repeat([]byte("A"), 1200)...)
	p2 := append([]byte{0x1F, 0x00, 0x47, 0x00}, bytes.Repeat([]byte("A"), 600)...)
	write := func(p []byte) []byte {
		before := sink.Len()
		if _, err := w.Write(p); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		return compressedBody(p, sink.Bytes()[before:])
	}
	body1 := write(p1)
	body2 := write(p2)

	in := NewInflater()
	defer in.Close()
	for i, tc := range []struct {
		body   []byte
		opcode uint32
		want   []byte
	}{{body1, 0x4E000E, p1[4:]}, {body2, 0x47001F, p2[4:]}} {
		opcode, payload, err := in.DecompressPacket(tc.body)
		if err != nil {
			t.Fatalf("packet %d: %v", i+1, err)
		}
		if opcode != tc.opcode || !bytes.Equal(payload, tc.want) {
			t.Fatalf("packet %d: opcode 0x%06X, %d payload bytes", i+1, opcode, len(payload))
		}
	}
	// a fresh inflater does not know the stream's history: this must fail, which proves the reader really is one
	// continuous stream and not a per-packet decode (the second packet is a run of the same byte as the first, so
	// its back-references point into the first packet's output)
	fresh := NewInflater()
	defer fresh.Close()
	if _, _, err := fresh.DecompressPacket(body2); err == nil {
		t.Fatal("a fresh inflater decoded a packet that continues another deflate stream")
	}
}

// A hostile server can send a compressed packet whose deflate data produces fewer bytes than the header claims,
// with a compressedAdler that is valid (the seed is public). The read must fail, not wait for a chunk that only
// the reading goroutine could send; the reviewer's hang.
func TestInflateTruncatedChunkFailsWithinASecond(t *testing.T) {
	var sink bytes.Buffer
	w, err := flate.NewWriter(&sink, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	packet := append([]byte{0x0E, 0x00, 0x4E, 0x00}, bytes.Repeat([]byte("A"), 1200)...)
	if _, err := w.Write(packet); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	body := compressedBody(packet, sink.Bytes())
	claimed := append([]byte(nil), body...)
	binary.LittleEndian.PutUint32(claimed[0:4], uint32(len(packet)+200)) // more than the stream can produce

	in := NewInflater()
	defer in.Close()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, _, err := in.DecompressPacket(claimed)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a truncated packet decoded")
		}
	case <-time.After(time.Second):
		t.Fatal("a packet that claims more bytes than its deflate data produces still blocks after 1 s")
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("the truncated packet took %s", elapsed)
	}
}

// A failed packet poisons its own connection only: the deflate window is per inflater. A fresh inflater still has
// the stream's history for the same two packets, and the poisoned one refuses the next packet instead of reading
// from an unknown position.
func TestInflateFailureDoesNotShareState(t *testing.T) {
	var sink bytes.Buffer
	w, err := flate.NewWriter(&sink, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	p1 := append([]byte{0x0E, 0x00, 0x4E, 0x00}, bytes.Repeat([]byte("A"), 1200)...)
	p2 := append([]byte{0x1F, 0x00, 0x47, 0x00}, bytes.Repeat([]byte("A"), 600)...)
	write := func(p []byte) []byte {
		before := sink.Len()
		if _, err := w.Write(p); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		return compressedBody(p, sink.Bytes()[before:])
	}
	body1 := write(p1)
	body2 := write(p2)
	// body2 claims ten bytes more than the stream produces at this point
	truncated2 := append([]byte(nil), body2...)
	binary.LittleEndian.PutUint32(truncated2[0:4], uint32(len(p2)+10))

	poisoned := NewInflater()
	defer poisoned.Close()
	defer poisoned.Close() // idempotent
	if _, _, err := poisoned.DecompressPacket(body1); err != nil {
		t.Fatalf("first packet: %v", err)
	}
	if _, _, err := poisoned.DecompressPacket(truncated2); err == nil {
		t.Fatal("the truncated second packet decoded")
	}
	if _, _, err := poisoned.DecompressPacket(body2); err == nil {
		t.Fatal("a poisoned inflater decoded a later packet")
	}

	fresh := NewInflater()
	defer fresh.Close()
	for i, body := range [][]byte{body1, body2} {
		if _, _, err := fresh.DecompressPacket(body); err != nil {
			t.Fatalf("fresh inflater, packet %d: %v", i+1, err)
		}
	}
}

// Close is idempotent and may run while DecompressPacket is reading: there is no channel to close and no
// goroutine waiting on one. Run with -race.
func TestInflateCloseDuringDecompress(t *testing.T) {
	var sink bytes.Buffer
	w, _ := flate.NewWriter(&sink, flate.BestSpeed)
	packet := append([]byte{1, 0, 0, 0}, bytes.Repeat([]byte("z"), 1000)...)
	if _, err := w.Write(packet); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	body := compressedBody(packet, sink.Bytes())

	var wg sync.WaitGroup
	in := NewInflater()
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _ = in.DecompressPacket(body)
	}()
	go func() {
		defer wg.Done()
		_ = in.Close()
		_ = in.Close()
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close during DecompressPacket did not finish")
	}
}

func TestInflateRejectsBadChecksumsAndSizes(t *testing.T) {
	in := NewInflater()
	defer in.Close()
	var sink bytes.Buffer
	w, _ := flate.NewWriter(&sink, flate.BestSpeed)
	packet := append([]byte{1, 0, 0, 0}, bytes.Repeat([]byte("x"), 1100)...)
	if _, err := w.Write(packet); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	good := compressedBody(packet, sink.Bytes())

	bad := append([]byte(nil), good...)
	bad[8] ^= 0xFF // compressedAdler
	if _, _, err := NewInflater().DecompressPacket(bad); err == nil {
		t.Fatal("a compressed-adler mismatch decoded")
	}
	bad = append([]byte(nil), good...)
	bad[4] ^= 0xFF // uncompressedAdler
	fresh := NewInflater()
	defer fresh.Close()
	if _, _, err := fresh.DecompressPacket(bad); err == nil {
		t.Fatal("an uncompressed-adler mismatch decoded")
	}
	fresh2 := NewInflater()
	defer fresh2.Close()
	bad = append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(bad[0:4], 0)
	if _, _, err := fresh2.DecompressPacket(bad); err == nil {
		t.Fatal("an uncompressed size of 0 decoded")
	}
	bad = append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(bad[0:4], MaxUncompressed+1)
	if _, _, err := fresh2.DecompressPacket(bad); err == nil {
		t.Fatal("an oversized uncompressed size decoded")
	}
	if _, _, err := fresh2.DecompressPacket(good[:11]); err == nil {
		t.Fatal("a body shorter than its header decoded")
	}
}

// FuzzDecompressPacket: arbitrary compressed-packet bodies must never panic; a bogus claim is an error.
func FuzzDecompressPacket(f *testing.F) {
	f.Add([]byte{4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 0})
	f.Add(bytes.Repeat([]byte{0xFF}, 32))
	f.Fuzz(func(t *testing.T, body []byte) {
		in := NewInflater()
		defer in.Close()
		_, _, _ = in.DecompressPacket(body)
	})
}

// The seeded adler is zlib's adler32 with seed 0x9827D8F1 (WorldSocket::WritePacketToBuffer); these values come
// from Python's zlib.adler32(data, 0x9827D8F1), i.e. from zlib itself.
func TestAdlerSeedVectors(t *testing.T) {
	cases := []struct {
		data []byte
		want uint32
	}{
		{nil, 0x9827D8F1},
		{[]byte("A"), 0x7168D932},
		{[]byte("ABC"), 0x24B1D9B7},
		{[]byte{0, 1, 2, 3, 4, 5, 6, 7}, 0x606CD90D},
	}
	for _, c := range cases {
		if got := adler32Seed(compressionSeed, c.data); got != c.want {
			t.Fatalf("adler32(% X) = %#08X, want %#08X", c.data, got, c.want)
		}
	}
}
