package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

// TestPrintLinkLost: the run's connection-lost line names the socket, the last packet and how stale it was, so a
// script reading the output can tell a closed connection from a fight that lost on its own.
func TestPrintLinkLost(t *testing.T) {
	var out bytes.Buffer
	printLinkLost(&out, &world.Report{
		LinkLost:       true,
		LinkLostSide:   "instance",
		LinkLostDetail: "worldconn: reading a frame header: EOF",
		LastPacket:     "SMSG_ATTACK_START",
		LastPacketAgo:  2500 * time.Millisecond,
	})
	line := out.String()
	for _, want := range []string{"connection lost on the instance socket", "EOF", "SMSG_ATTACK_START", "2.5s before the end"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the line %q does not carry %q", line, want)
		}
	}
	// A run whose link held prints nothing.
	out.Reset()
	printLinkLost(&out, &world.Report{})
	if out.Len() != 0 {
		t.Fatalf("a healthy run printed %q", out.String())
	}
}
