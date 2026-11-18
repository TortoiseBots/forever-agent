package wire

import (
	"bytes"
	"testing"
)

// The bit writer packs most significant bit first and a numeric write flushes the pending partial byte
// (ByteBuffer.h:144-258, 495-534). 101 + 110011 is nine bits: 10111001 then the last bit in the next byte.
func TestBitPackingAcrossBytes(t *testing.T) {
	b := NewWriter()
	b.WriteBits(0b101, 3)
	b.WriteBits(0b110011, 6)
	b.FlushBits()
	if got, want := b.Bytes(), []byte{0xB9, 0x80}; !bytes.Equal(got, want) {
		t.Fatalf("bits = % X, want % X", got, want)
	}
}

// A numeric write after bits flushes the partial byte and starts on a byte boundary; that is what makes the
// headless client's CMSG_AUTH_SESSION end up with the UseIPv6 bit as a whole zero byte.
func TestNumericWriteFlushesBits(t *testing.T) {
	b := NewWriter()
	b.WriteBit(false) // UseIPv6
	b.WriteU32(0x4E)
	b.WriteBytes([]byte("J"))
	if got, want := b.Bytes(), []byte{0x00, 0x4E, 0x00, 0x00, 0x00, 'J'}; !bytes.Equal(got, want) {
		t.Fatalf("bytes = % X, want % X", got, want)
	}
}

// Every bit width round trips, also across byte boundaries, and the reader's bit position matches the writer's.
func TestBitRoundTrip(t *testing.T) {
	for width := 1; width <= 32; width++ {
		b := NewWriter()
		value := uint64(1)<<(width-1) | 0x55
		value &= (uint64(1) << width) - 1
		b.WriteBits(value, width)
		b.WriteBits(3, 3)
		b.FlushBits()
		r := NewReader(b.Bytes())
		if got := uint64(r.ReadBits(width)); got != value {
			t.Fatalf("width %d: read %#x, wrote %#x", width, got, value)
		}
		if got := r.ReadBits(3); got != 3 {
			t.Fatalf("width %d: second value %d, want 3", width, got)
		}
		if r.Err() != nil {
			t.Fatalf("width %d: %v", width, r.Err())
		}
	}
}

// A numeric read after bits discards the rest of the partial byte (ByteBuffer::read<T> calls ResetBitPos), so the
// bytes line up the way the server's readers expect.
func TestNumericReadResetsBitPosition(t *testing.T) {
	r := NewReader([]byte{0x80, 0x2A, 0x00, 0x00, 0x00})
	if !r.ReadBit() {
		t.Fatal("first bit is not set")
	}
	if r.ReadBit() {
		t.Fatal("second bit is set")
	}
	if got := r.ReadU32(); got != 0x2A {
		t.Fatalf("uint32 = %#x, want 0x2a (the partial bit byte must be dropped)", got)
	}
}

func TestReadPastEndIsAnError(t *testing.T) {
	r := NewReader([]byte{1, 2, 3})
	r.ReadU32()
	if r.Err() == nil {
		t.Fatal("a short uint32 read did not set an error")
	}
	if r.ReadU8() != 0 || r.Remaining() != 3 {
		t.Fatalf("a sticky error must not consume: remaining %d", r.Remaining())
	}
	r = NewReader(nil)
	r.ReadBits(9)
	if r.Err() == nil {
		t.Fatal("reading bits past the end did not set an error")
	}
	r = NewReader([]byte("abc"))
	if got := r.ReadCString(); got != "" || r.Err() == nil {
		t.Fatalf("ReadCString without a terminator = %q, err %v", got, r.Err())
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{Size: 0x1234}
	h.Tag[0], h.Tag[11] = 0xAB, 0xCD
	p := EncodeHeader(h)
	if len(p) != HeaderSize {
		t.Fatalf("header is %d bytes", len(p))
	}
	got, err := DecodeHeader(p)
	if err != nil || got != h {
		t.Fatalf("decoded %+v, %v; want %+v", got, err, h)
	}
	if _, err := DecodeHeader(p[:15]); err == nil {
		t.Fatal("a 15-byte header decoded")
	}
}

// FuzzBufferReads feeds arbitrary bytes to every reader: nothing may panic, and a failed read must set the sticky
// error instead of returning data.
func FuzzBufferReads(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x03, 0xE0, 0x65, 0x0D, 0x18, 0x01, 0x08})
	f.Add(bytes.Repeat([]byte{0xFF}, 64))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewReader(data)
		ReadPackedGUID(r)
		ReadPackedGUID(r)
		r.ReadBits(6)
		r.ReadBits(9)
		r.ReadCString()
		r.ReadFloat32()
		r.ReadU64()
		_ = r.Err()
	})
}

// WriteBit writes the most significant bit first: true, false, true is 0xA0, and a bit read after a numeric write
// starts at the next byte (the server's WriteBit/ReadBit).
func TestSingleBitOrder(t *testing.T) {
	b := NewWriter()
	for _, bit := range []bool{true, false, true, false, false, false, false, false} {
		b.WriteBit(bit)
	}
	b.WriteBit(true)
	b.WriteU8(0x42)
	if got, want := b.Bytes(), []byte{0xA0, 0x80, 0x42}; !bytes.Equal(got, want) {
		t.Fatalf("bits = % X, want % X", got, want)
	}
	r := NewReader([]byte{0xA0, 0x80})
	for i, want := range []bool{true, false, true, false, false, false, false, false, true} {
		if got := r.ReadBit(); got != want {
			t.Fatalf("bit %d = %v, want %v", i, got, want)
		}
	}
}
