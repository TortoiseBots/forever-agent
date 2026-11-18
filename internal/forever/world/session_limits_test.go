package world

import (
	"context"
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// The packet count keeps one key per distinct unknown wire opcode, so a flooding server that walks wire numbers
// could grow the report without bound. The cap collapses the excess into one key, and known/seen keys stay exact.
func TestUnknownReportKeysAreBounded(t *testing.T) {
	s := &session{report: &Report{Counts: make(map[string]int)}}
	handle := func(p worldconn.Packet) {
		t.Helper()
		if err := s.handle(context.Background(), event{side: sideRealm, p: p}); err != nil {
			t.Fatalf("handle %s: %v", p.Name, err)
		}
	}

	for i := range maxUnknownReportKeys {
		handle(worldconn.Packet{Wire: uint32(0x400000 + i)})
	}
	if len(s.report.Counts) != maxUnknownReportKeys {
		t.Fatalf("%d keys after %d unknown opcodes, want %d", len(s.report.Counts), maxUnknownReportKeys, maxUnknownReportKeys)
	}

	// distinct unknown opcodes past the cap share one key
	const extra = 40
	for i := range extra {
		handle(worldconn.Packet{Wire: uint32(0x500000 + i)})
	}
	if got := s.report.Counts[otherUnknownKey]; got != extra {
		t.Fatalf("%q counts %d, want %d", otherUnknownKey, got, extra)
	}
	if len(s.report.Counts) != maxUnknownReportKeys+1 {
		t.Fatalf("%d keys after the flood, want %d", len(s.report.Counts), maxUnknownReportKeys+1)
	}

	// an unknown key that already has a row keeps counting on it
	handle(worldconn.Packet{Wire: 0x400000})
	if got := s.report.Counts[fmt.Sprintf("wire 0x%06X", 0x400000)]; got != 2 {
		t.Fatalf("a known-to-the-report unknown opcode counts %d, want 2", got)
	}
	if len(s.report.Counts) != maxUnknownReportKeys+1 {
		t.Fatalf("a repeat grew the map to %d keys", len(s.report.Counts))
	}

	// a translated packet is counted by name and never touches the unknown cap
	handle(worldconn.Packet{Wire: 0x4E0000, Core: 1, Name: "SMSG_TEST"})
	if got := s.report.Counts["SMSG_TEST"]; got != 1 {
		t.Fatalf("SMSG_TEST counts %d, want 1", got)
	}
	if s.unknownKeys != maxUnknownReportKeys {
		t.Fatalf("unknownKeys = %d, want %d", s.unknownKeys, maxUnknownReportKeys)
	}
}
