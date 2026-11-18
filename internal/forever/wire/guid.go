package wire

import "fmt"

// GUID is the 128-bit object GUID of the Classic client (ObjectGuid, ObjectGuid.h:307-450): a low and a high
// uint64. The high half carries the type in bits 58-63.
type GUID struct {
	Low  uint64
	High uint64
}

// EmptyGUID is the all-zero GUID.
var EmptyGUID GUID

// IsZero reports whether g is the empty GUID.
func (g GUID) IsZero() bool { return g.Low == 0 && g.High == 0 }

// Type returns the ObjectGuid high-type nibble (bits 58-63 of High).
func (g GUID) Type() uint8 { return uint8(g.High>>58) & 0x3F }

// String prints both halves so a log line is unambiguous.
func (g GUID) String() string { return fmt.Sprintf("0x%016X%016X", g.High, g.Low) }

// bytes returns the 16 wire bytes of the GUID: low half first, then high half, each little-endian
// (ObjectGuid::GetRawValue).
func (g GUID) bytes() [16]byte {
	var out [16]byte
	for i := range 8 {
		out[i] = byte(g.Low >> (8 * i))
		out[i+8] = byte(g.High >> (8 * i))
	}
	return out
}

func guidFromBytes(p [16]byte) GUID {
	var g GUID
	for i := range 8 {
		g.Low |= uint64(p[i]) << (8 * i)
		g.High |= uint64(p[i+8]) << (8 * i)
	}
	return g
}

// WritePackedGUID writes the packed form (ObjectGuid.cpp:1087-1111): a uint16 mask whose bit i is set when byte
// i of the 16 GUID bytes is not zero, followed by those bytes in order.
func WritePackedGUID(b *Buffer, g GUID) {
	raw := g.bytes()
	var mask uint16
	for i, c := range raw {
		if c != 0 {
			mask |= 1 << i
		}
	}
	b.WriteU16(mask)
	for _, c := range raw {
		if c != 0 {
			b.WriteU8(c)
		}
	}
}

// ReadPackedGUID reads the packed form (ObjectGuid.cpp:1113-1134). The server reads popcount(mask) bytes even
// when the high bit (the GUID type byte) is clear, and then returns the empty GUID; this does the same, so the
// reader position after either form matches the server.
func ReadPackedGUID(b *Buffer) GUID { return b.reportGUID(readPackedGUID(b)) }

func readPackedGUID(b *Buffer) GUID {
	mask := b.ReadU16()
	if b.Err() != nil {
		return EmptyGUID
	}
	count := 0
	for i := range 16 {
		if mask&(1<<i) != 0 {
			count++
		}
	}
	packed := b.ReadBytes(count)
	if packed == nil && count != 0 {
		return EmptyGUID
	}
	if mask&0x8000 == 0 {
		return EmptyGUID
	}
	var raw [16]byte
	at := 0
	for i := range 16 {
		if mask&(1<<i) != 0 {
			raw[i] = packed[at]
			at++
		}
	}
	return guidFromBytes(raw)
}

// ReadGUID reads the 16 raw GUID bytes (not a form any packet of this round uses; kept for completeness).
func ReadGUID(b *Buffer) GUID { return b.reportGUID(readGUID(b)) }

func readGUID(b *Buffer) GUID {
	p := b.ReadBytes(16)
	if p == nil {
		return EmptyGUID
	}
	var raw [16]byte
	copy(raw[:], p)
	return guidFromBytes(raw)
}

// reportGUID is the audit seam: every GUID a read decodes is passed to the hook before the parser sees (or drops)
// it. The hook is nil in production.
func (b *Buffer) reportGUID(g GUID) GUID {
	if b.guidHook != nil {
		b.guidHook(g)
	}
	return g
}

// WriteGUID writes the 16 raw GUID bytes.
func WriteGUID(b *Buffer, g GUID) {
	raw := g.bytes()
	b.WriteBytes(raw[:])
}
