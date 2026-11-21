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
