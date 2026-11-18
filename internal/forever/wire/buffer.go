// Package wire is the byte layer of the Forever (WoW Classic 1.60.1) world socket: the server's bit-packed
// ByteBuffer, the 16-byte packet header, the 128-bit packed GUID and the ClassicOpcodes translation.
//
// Everything here mirrors ForeverCore src/server/shared/Packets/ByteBuffer.{h,cpp},
// src/server/game/Server/WorldSocket.h (PacketHeader) and src/server/game/Server/Protocol/ClassicOpcodes.cpp.
// Reads are bounded: a length that does not fit the buffer sets an error instead of slicing or allocating.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// ErrShort is the sticky read error of a Buffer that ran out of bytes or bits.
var ErrShort = errors.New("wire: read past the end of the packet")

// Buffer is the server's ByteBuffer: a growable byte buffer with an MSB-first bit writer and reader. A numeric
// read or write resets the bit position, exactly like ByteBuffer::read<T>/append (ByteBuffer.h:495-534).
type Buffer struct {
	data   []byte
	rpos   int
	wpos   int
	bitpos int // 8 = byte aligned; otherwise the number of bits used of curbit (the server's _bitpos)
	curbit byte
	err    error

	// guidHook, when set, receives every GUID a read decodes, including the ones a parser drops. It exists for the
	// fixture audits (a real identifier must not reach a committed fixture); no production path sets it.
	guidHook func(GUID)
}

// NewWriter returns an empty Buffer.
func NewWriter() *Buffer { return &Buffer{bitpos: 8} }

// NewReader returns a Buffer that reads from data.
func NewReader(data []byte) *Buffer { return &Buffer{data: data, wpos: len(data), bitpos: 8} }

// SetGUIDHook makes every GUID this reader decodes pass through hook before it is returned or dropped. It is an
// audit hook: call it before the first read, and leave it nil outside a test.
func (b *Buffer) SetGUIDHook(hook func(GUID)) { b.guidHook = hook }

// Err reports the first read error, if any.
func (b *Buffer) Err() error { return b.err }

func (b *Buffer) fail() {
	if b.err == nil {
		b.err = ErrShort
	}
}

// Len returns the number of bytes written (the payload length of a writer).
func (b *Buffer) Len() int { return b.wpos }

// Remaining returns the number of unread bytes.
func (b *Buffer) Remaining() int { return b.wpos - b.rpos }

// Bytes returns the written bytes. The bit writer is not flushed; call FlushBits first for the last partial byte.
func (b *Buffer) Bytes() []byte { return b.data[:b.wpos] }

// Reset makes the buffer writable again and empty.
func (b *Buffer) Reset() {
	b.data = b.data[:0]
	b.rpos, b.wpos, b.bitpos, b.curbit, b.err = 0, 0, 8, 0, nil
}

// ---- writing ----

func (b *Buffer) writeRaw(p []byte) {
	if b.bitpos != 8 {
		b.FlushBits()
	}
	b.data = append(b.data, p...)
	b.wpos = len(b.data)
}

// FlushBits writes the pending partial bit byte, if any (ByteBuffer::FlushBits).
func (b *Buffer) FlushBits() {
	if b.bitpos == 8 {
		return
	}
	b.bitpos = 8
	b.data = append(b.data, b.curbit)
	b.wpos = len(b.data)
	b.curbit = 0
}

// WriteU8 appends one byte.
func (b *Buffer) WriteU8(v uint8) { b.writeRaw([]byte{v}) }

// WriteU16 appends a little-endian uint16.
func (b *Buffer) WriteU16(v uint16) {
	var p [2]byte
	binary.LittleEndian.PutUint16(p[:], v)
	b.writeRaw(p[:])
}

// WriteU32 appends a little-endian uint32.
func (b *Buffer) WriteU32(v uint32) {
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], v)
	b.writeRaw(p[:])
}

// WriteI32 appends a little-endian int32.
func (b *Buffer) WriteI32(v int32) { b.WriteU32(uint32(v)) }

// WriteU64 appends a little-endian uint64.
func (b *Buffer) WriteU64(v uint64) {
	var p [8]byte
	binary.LittleEndian.PutUint64(p[:], v)
	b.writeRaw(p[:])
}

// WriteFloat32 appends the IEEE-754 bits of v, little-endian.
func (b *Buffer) WriteFloat32(v float32) { b.WriteU32(math.Float32bits(v)) }

// WriteBytes appends p.
func (b *Buffer) WriteBytes(p []byte) {
	if len(p) == 0 {
		return
	}
	b.writeRaw(p)
}

// WriteString appends the bytes of s without a length or terminator.
func (b *Buffer) WriteString(s string) {
	if s == "" {
		return
	}
	b.writeRaw([]byte(s))
}

// WriteBit writes one bit, most significant bit first (ByteBuffer::WriteBit).
func (b *Buffer) WriteBit(bit bool) {
	b.bitpos--
	if bit {
		b.curbit |= 1 << b.bitpos
	}
	if b.bitpos == 0 {
		b.bitpos = 8
		b.data = append(b.data, b.curbit)
		b.wpos = len(b.data)
		b.curbit = 0
	}
}

// WriteBits writes the low bits of value, most significant bit first (ByteBuffer::WriteBits).
func (b *Buffer) WriteBits(value uint64, bits int) {
	if bits <= 0 {
		return
	}
	if bits > 64 {
		panic("wire: WriteBits with more than 64 bits")
	}
	if bits < 64 {
		value &= (uint64(1) << bits) - 1
	}
	if bits > b.bitpos {
		b.curbit |= byte(value >> (bits - b.bitpos))
		bits -= b.bitpos
		b.bitpos = 8
		b.data = append(b.data, b.curbit)
		b.wpos = len(b.data)
		for bits >= 8 {
			bits -= 8
			b.data = append(b.data, byte(value>>bits))
			b.wpos = len(b.data)
		}
		b.bitpos = 8 - bits
		if bits == 0 {
			b.curbit = 0
		} else {
			b.curbit = byte(value&((uint64(1)<<bits)-1)) << b.bitpos
		}
		return
	}
	b.bitpos -= bits
	b.curbit |= byte(value << b.bitpos)
	if b.bitpos == 0 {
		b.bitpos = 8
		b.data = append(b.data, b.curbit)
		b.wpos = len(b.data)
		b.curbit = 0
	}
}

// ---- reading ----

// resetBitPos discards the partially read bit byte (ByteBuffer::ResetBitPos). Every numeric and byte read does
// it first, exactly like the server.
func (b *Buffer) resetBitPos() {
	b.bitpos = 8
	b.curbit = 0
}

func (b *Buffer) readRawByte() byte {
	if b.rpos >= b.wpos {
		b.fail()
		return 0
	}
	c := b.data[b.rpos]
	b.rpos++
	return c
}

// ReadU8 reads one byte.
func (b *Buffer) ReadU8() uint8 {
	b.resetBitPos()
	if b.err != nil || b.rpos >= b.wpos {
		b.fail()
		return 0
	}
	c := b.data[b.rpos]
	b.rpos++
	return c
}

func (b *Buffer) readN(n int) []byte {
	b.resetBitPos()
	if n < 0 || b.err != nil || n > b.wpos-b.rpos {
		b.fail()
		return nil
	}
	p := b.data[b.rpos : b.rpos+n]
	b.rpos += n
	return p
}

// ReadU16 reads a little-endian uint16.
func (b *Buffer) ReadU16() uint16 {
	p := b.readN(2)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(p)
}

// ReadU32 reads a little-endian uint32.
func (b *Buffer) ReadU32() uint32 {
	p := b.readN(4)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(p)
}

// ReadI32 reads a little-endian int32.
func (b *Buffer) ReadI32() int32 { return int32(b.ReadU32()) }

// ReadU64 reads a little-endian uint64.
func (b *Buffer) ReadU64() uint64 {
	p := b.readN(8)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(p)
}

// ReadI16 reads a little-endian int16.
func (b *Buffer) ReadI16() int16 { return int16(b.ReadU16()) }

// ReadI8 reads one signed byte.
func (b *Buffer) ReadI8() int8 { return int8(b.ReadU8()) }

// ReadFloat32 reads a little-endian IEEE-754 single.
func (b *Buffer) ReadFloat32() float32 { return math.Float32frombits(b.ReadU32()) }

// ReadBytes reads n bytes; nil (and an error) when they do not fit.
func (b *Buffer) ReadBytes(n int) []byte { return b.readN(n) }

// ReadString reads n bytes as a string.
func (b *Buffer) ReadString(n int) string { return string(b.readN(n)) }

// Skip discards n bytes.
func (b *Buffer) Skip(n int) {
	if n < 0 || b.err != nil || n > b.wpos-b.rpos {
		b.fail()
		return
	}
	b.resetBitPos()
	b.rpos += n
}

// ReadBit reads one bit, most significant bit first (ByteBuffer::ReadBit).
func (b *Buffer) ReadBit() bool {
	if b.err != nil {
		return false
	}
	if b.bitpos >= 8 {
		b.curbit = b.readRawByte()
		if b.err != nil {
			return false
		}
		b.bitpos = 0
	}
	b.bitpos++
	return (b.curbit>>(8-b.bitpos))&1 != 0
}

// ReadBits reads bits bits, most significant bit first (ByteBuffer::ReadBits). The server reads at most 32.
func (b *Buffer) ReadBits(bits int) uint32 {
	if bits <= 0 {
		return 0
	}
	if bits > 32 {
		panic("wire: ReadBits with more than 32 bits")
	}
	if b.err != nil {
		return 0
	}
	var value uint32
	if bits > 8-b.bitpos {
		bitsInBuffer := 8 - b.bitpos
		value = uint32(b.curbit&byte((1<<bitsInBuffer)-1)) << (bits - bitsInBuffer)
		bits -= bitsInBuffer
		for bits >= 8 {
			bits -= 8
			c := b.readRawByte()
			if b.err != nil {
				return 0
			}
			value |= uint32(c) << bits
		}
		if bits > 0 {
			c := b.readRawByte()
			if b.err != nil {
				return 0
			}
			b.curbit = c
			value |= (uint32(c) >> (8 - bits)) & ((1 << bits) - 1)
			b.bitpos = bits
		} else {
			b.bitpos = 8
			b.curbit = 0
		}
		return value
	}
	value = (uint32(b.curbit) >> (8 - b.bitpos - bits)) & ((1 << bits) - 1)
	b.bitpos += bits
	return value
}

// ReadCString reads a NUL-terminated string. The terminator is required: a string that runs to the end of the
// packet is an error, like ByteBuffer::ReadCString.
func (b *Buffer) ReadCString() string {
	b.resetBitPos()
	start := b.rpos
	for b.rpos < b.wpos {
		if b.data[b.rpos] == 0 {
			s := string(b.data[start:b.rpos])
			b.rpos++
			return s
		}
		b.rpos++
	}
	b.fail()
	return ""
}

// ---------------------------------------------------------------------------------------------
// Packet header

// HeaderSize is the size of the header on the wire: uint32 Size and the 12-byte GCM tag
// (WorldSocket.h PacketHeader).
const HeaderSize = 16

// TagSize is the AES-GCM tag length of the world socket.
const TagSize = 12

// Header is the framed packet header. Size counts the 4-byte opcode and the payload; Tag is zero before
// encryption is switched on.
type Header struct {
	Size uint32
	Tag  [TagSize]byte
}

// EncodeHeader writes h.
func EncodeHeader(h Header) []byte {
	p := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint32(p, h.Size)
	copy(p[4:], h.Tag[:])
	return p
}

// DecodeHeader parses the 16-byte header.
func DecodeHeader(p []byte) (Header, error) {
	if len(p) != HeaderSize {
		return Header{}, fmt.Errorf("wire: header is %d bytes, want %d", len(p), HeaderSize)
	}
	h := Header{Size: binary.LittleEndian.Uint32(p)}
	copy(h.Tag[:], p[4:])
	return h, nil
}

// MaxFrame is the largest server frame this client reads. The largest packet the server sends in this round is
// a compressed hotfix list (about 200 KB uncompressed); the bound exists so a hostile size cannot allocate.
const MaxFrame = 4 << 20

// MaxClientFrame is the server's own limit for a frame it accepts: PacketHeader::IsValidSize is Size < 0x10000 and
// Size includes the 4-byte opcode (WorldSocket.h:71-76), so a larger client frame is refused before it is parsed.
const MaxClientFrame = 0x10000
