package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// TestLootCheckUsage: the two new commands refuse their bad flags before a login is attempted.
func TestLootCheckUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	base := []string{"--rest", "127.0.0.1:1", "--rpc", "127.0.0.1:2", "--ca", "ca.pem", "--account", "a@b", "--character", "XXXXXX"}
	if code := foreverCmd(context.Background(), append(append([]string{"loot-check"}, base...), "--roll", "needless"), &stdout, &stderr); code != exitUsage {
		t.Fatalf("a bad --roll returned %d, want the usage code", code)
	}
	if !strings.Contains(stderr.String(), "unknown roll vote") {
		t.Fatalf("the refusal does not name the flag: %q", stderr.String())
	}
	stderr.Reset()
	if code := foreverCmd(context.Background(), append([]string{"object-use"}, base...), &stdout, &stderr); code != exitUsage {
		t.Fatalf("object-use without --entry returned %d", code)
	}
	if !strings.Contains(stderr.String(), "--entry is required") {
		t.Fatalf("the refusal does not say the entry is required: %q", stderr.String())
	}
}

// TestPrintLootCheckSummary: the window, the roll outcome, the money and the release are all named.
func TestPrintLootCheckSummary(t *testing.T) {
	var out bytes.Buffer
	printLootCheckSummary(&out, &world.Report{Loot: world.LootReport{
		Kind: "unit", Entry: 299, Name: "a beast", Distance: 2,
		Opened: true, Method: loot.MethodGroupLoot, Threshold: 2, Coins: 311,
		Items: []world.LootItemRow{
			{LootListID: 0, ItemID: 4583, Count: 1, Slot: loot.SlotAllowLoot},
			{LootListID: 3, ItemID: 11754, Count: 1, Slot: loot.SlotRollOngoing, Roll: true},
		},
		MoneyTaken: 311,
		Removed:    []uint8{0},
		Released:   true,
		Rolls: []world.LootRollRow{{
			LootListID: 3, ItemID: 11754, ValidRolls: loot.FlagPass | loot.FlagNeed | loot.FlagGreed,
			TimeLimit: 60000, OurVote: loot.RollNeed, Vote: loot.RollNeed, Number: 77,
			Winner: "you", VoteSeen: 1, Ended: true,
		}},
	}})
	text := out.String()
	for _, want := range []string{
		"target: unit, entry 299", "2 item(s)", "slot 0: item 4583 x1, allow loot",
		"needs a roll", "our vote need", "you won with 77 on a need vote", "money taken: 311 copper",
		"item slots removed by the server: [0]", "window released: true",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the summary does not carry %q:\n%s", want, text)
		}
	}
	out.Reset()
	printLootCheckSummary(&out, &world.Report{Loot: world.LootReport{Kind: "unit", Entry: 5, Failure: loot.ErrorTooFar}})
	if !strings.Contains(out.String(), "you are too far away") {
		t.Fatalf("the refusal is not named: %q", out.String())
	}
}

// TestPrintObjectUseSummary: the state transition and the loot window (or their absence) are named.
func TestPrintObjectUseSummary(t *testing.T) {
	var out bytes.Buffer
	printObjectUseSummary(&out, &world.Report{ObjectUse: world.ObjectUseReport{
		Found: true, Entry: 17153, Distance: 2, StateKnown: true, StateBefore: 1, StateAfter: 0,
		StateChanged: true, FlagsBefore: 0, FlagsAfter: 1,
	}})
	text := out.String()
	for _, want := range []string{"entry 17153", "ready (closed) -> active (open)", "changed: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the summary does not carry %q:\n%s", want, text)
		}
	}
	out.Reset()
	printObjectUseSummary(&out, &world.Report{ObjectUse: world.ObjectUseReport{Found: false}})
	if !strings.Contains(out.String(), "no object of that entry") {
		t.Fatalf("the empty report: %q", out.String())
	}
}
