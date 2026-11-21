package bg

// Hostile packets: every parser must refuse a truncated status without panicking, and a count beyond its bound
// must be refused before it allocates (the packet is server data).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// parseFunc is one parser under the truncation sweep.
type parseFunc struct {
	name string
	call func([]byte) error
}

// truncationSweep feeds every proper prefix of a valid packet to its parser and requires an error each time.
func truncationSweep(t *testing.T, valid []byte, parsers []parseFunc) {
	t.Helper()
	for _, p := range parsers {
		for cut := range len(valid) {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked on %d of %d byte(s): %v", p.name, cut, len(valid), r)
					}
				}()
				if err := p.call(valid[:cut]); err == nil {
					t.Fatalf("%s parsed a %d-byte prefix of a %d-byte packet", p.name, cut, len(valid))
				}
			}()
		}
	}
}

// TestParsersRefuseEveryTruncation: a short read, a short bit block and a cut string block are all errors, never a
// half-filled status or a panic.
func TestParsersRefuseEveryTruncation(t *testing.T) {
	list := wire.NewWriter()
	wire.WritePackedGUID(list, bgMaster)
	list.WriteI32(2)
	list.WriteU8(10)
	list.WriteU8(60)
	list.WriteU32(1)
	list.WriteI32(0x77)
	list.WriteBit(true)
	list.WriteBit(false)
	list.FlushBits()
	basic := wire.NewWriter()
	writeTestTicket(basic)
	header := wire.NewWriter()
	writeHeader(header, []uint64{0x1F10000000000002})
	need := append(append([]byte{}, header.Bytes()...), 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09)
	active := append(append([]byte{}, header.Bytes()...), 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0xFE)
	active = append(active, 0x00)
	queued := append(append([]byte{}, header.Bytes()...), 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x00)
	failed := wire.NewWriter()
	writeTestTicket(failed)
	failed.WriteU64(0x1F10000000000002)
	failed.WriteU32(10)
	wire.WritePackedGUID(failed, bgSelf)

	parsers := []parseFunc{
		{"list", func(p []byte) error { _, err := ParseList(p); return err }},
		{"none", func(p []byte) error { _, err := ParseNone(p); return err }},
		{"need-confirmation", func(p []byte) error { _, err := ParseNeedConfirmation(p); return err }},
		{"active", func(p []byte) error { _, err := ParseActive(p); return err }},
		{"queued", func(p []byte) error { _, err := ParseQueued(p); return err }},
		{"failed", func(p []byte) error { _, err := ParseFailed(p); return err }},
	}
	truncationSweep(t, list.Bytes(), parsers[:1])
	truncationSweep(t, basic.Bytes(), parsers[1:2])
	truncationSweep(t, need, parsers[2:3])
	truncationSweep(t, active, parsers[3:4])
	truncationSweep(t, queued, parsers[4:5])
	truncationSweep(t, failed.Bytes(), parsers[5:6])
}

// TestListRefusesOverlongCounts: a count beyond the bound is refused before it allocates (a hostile count must not
// size it).
func TestListRefusesOverlongCounts(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, bgMaster)
	b.WriteI32(2)
	b.WriteU8(10)
	b.WriteU8(60)
	b.WriteU32(0xFFFFFFF0)
	if _, err := ParseList(b.Bytes()); err == nil {
		t.Fatal("a battlefields count of 0xfffffff0 parsed")
	}
}
