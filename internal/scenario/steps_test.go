package scenario

import (
	"encoding/json"
	"testing"
)

// TestForeverPageDroppedIsANumber: the serve daemon's event_log answers dropped as a count (serveclient.EventLogPage),
// so the scenario's page must decode a number, not a bool.
func TestForeverPageDroppedIsANumber(t *testing.T) {
	var page foreverPage
	if err := json.Unmarshal([]byte(`{"events":[],"next":4,"more":false,"dropped":3,"epoch":"e1"}`), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Dropped != 3 {
		t.Fatalf("dropped %d, want 3", page.Dropped)
	}
}

// TestBotModuleLine: a bot step carries the bare module line, and the runner maps it to the session server's own
// prefix: ForeverCore's module registers its table under `.tb`, the turtle module under `.bot`. A file that already
// writes either mark is mapped to the session's prefix, so the same file runs on both and the raw line is what
// went out (never `.bot .tb ...`).
func TestBotModuleLine(t *testing.T) {
	cases := []struct {
		forever bool
		command string
		want    string
	}{
		{true, "state Bheal", ".tb state Bheal"},
		{true, "  .tb state Bheal  ", ".tb state Bheal"},
		{true, ".bot state Bheal", ".tb state Bheal"},
		{true, ".BOT state Bheal", ".tb state Bheal"},
		{true, ".tb", ".tb"},
		{false, "state Bheal", ".bot state Bheal"},
		{false, "  .bot state Bheal  ", ".bot state Bheal"},
		{false, ".tb state Bheal", ".bot state Bheal"},
		{false, ".bot", ".bot"},
	}
	for _, c := range cases {
		if got := botModuleLine(c.forever, c.command); got != c.want {
			t.Errorf("botModuleLine(%v, %q) = %q, want %q", c.forever, c.command, got, c.want)
		}
	}
}
