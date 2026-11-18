package wire

import "testing"

// The character's GUID as the real client sent it in CMSG_PLAYER_LOGIN (packet capture, repack 0.1.37.4):
// mask 0xE003 (bytes 0, 1, 13, 14, 15 are non-zero) and those bytes 3B 0D 18 01 08. That is low 0x0D3B and
// high 0x0801180000000000 (ObjectGuid high type 2 = Player).
func TestPackedGUIDFromCapture(t *testing.T) {
	const fixture = "\x03\xE0\x3B\x0D\x18\x01\x08"
	g := ReadPackedGUID(NewReader([]byte(fixture)))
	if g.Low != 0x0D3B || g.High != 0x0801180000000000 {
		t.Fatalf("guid = %s", g)
	}
	if g.Type() != 2 {
		t.Fatalf("type = %d, want Player (2)", g.Type())
	}
	w := NewWriter()
	WritePackedGUID(w, g)
	if got := string(w.Bytes()); got != fixture {
		t.Fatalf("packed = % X, want % X", got, fixture)
	}
}

// The empty GUID is mask 0 and no bytes; a mask whose byte 15 is zero is read as empty but its bytes are still
// consumed (ObjectGuid.cpp:1113-1126).
func TestPackedGUIDEmptyAndConsumption(t *testing.T) {
	if g := ReadPackedGUID(NewReader([]byte{0, 0})); !g.IsZero() {
		t.Fatalf("mask 0 read %s", g)
	}
	// mask 0x0001 with one byte 0x7F: byte 15 is zero, so the GUID is empty, but 0x7F was consumed
	r := NewReader([]byte{0x01, 0x00, 0x7F, 0xAA})
	if g := ReadPackedGUID(r); !g.IsZero() {
		t.Fatalf("mask without byte 15 read %s", g)
	}
	if got := r.ReadU8(); got != 0xAA {
		t.Fatalf("next byte = %#x, want 0xaa (the packed byte must be consumed)", got)
	}
}

func TestPackedGUIDTruncated(t *testing.T) {
	r := NewReader([]byte{0x03, 0xE0, 0x00})
	ReadPackedGUID(r)
	if r.Err() == nil {
		t.Fatal("a GUID cut inside its bytes did not set an error")
	}
}
