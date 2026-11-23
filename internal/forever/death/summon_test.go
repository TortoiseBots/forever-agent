package death

// The summoning offer's two packets: the golden bytes of SMSG_SUMMON_REQUEST's reader and the answer's writer, both
// laid out from the server's own reader and writer (MovementPackets.cpp:1079-1110).

import (
	"encoding/hex"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// summonGolden is SMSG_SUMMON_REQUEST for the player guid {Low: 1, High: 2<<58}: packed GUID 01 80 01 08, virtual
// realm address 42, area 1535, reason 1 (a scenario) and the SkipStartingArea bit set (the bit writer's first bit is
// the byte's high bit, so the last byte is 0x80).
const summonGolden = "018001082a000000ff0500000180"

func TestParseSummonRequest(t *testing.T) {
	payload, err := hex.DecodeString(summonGolden)
	if err != nil {
		t.Fatal(err)
	}
	o, err := ParseSummonRequest(payload)
	if err != nil {
		t.Fatalf("ParseSummonRequest: %v", err)
	}
	want := wire.GUID{Low: 1, High: 2 << 58}
	if o.Summoner != want {
		t.Fatalf("the summoner is %s, want %s", o.Summoner, want)
	}
	if o.AreaID != 1535 || o.Reason != 1 {
		t.Fatalf("the offer: area %d, reason %d", o.AreaID, o.Reason)
	}
	if !o.SkipStartingArea {
		t.Fatal("the SkipStartingArea bit was not read")
	}

	// A short packet is an error, and so is one with bytes left after the layout.
	if _, err := ParseSummonRequest(payload[:len(payload)-1]); err == nil {
		t.Fatal("the parser accepted a short packet")
	}
	if _, err := ParseSummonRequest(append(append([]byte(nil), payload...), 0)); err == nil {
		t.Fatal("the parser accepted a packet with a byte left over")
	}
}

func TestBuildSummonResponse(t *testing.T) {
	summoner := wire.GUID{Low: 1, High: 2 << 58}
	// The Accept bit is the bit writer's first bit, the high bit of the byte after the packed GUID.
	if got, want := hex.EncodeToString(BuildSummonResponse(summoner, true)), "0180010880"; got != want {
		t.Fatalf("accept: %s, want %s", got, want)
	}
	if got, want := hex.EncodeToString(BuildSummonResponse(summoner, false)), "0180010800"; got != want {
		t.Fatalf("decline: %s, want %s", got, want)
	}
}
