package tbcmd

import (
	"strings"
	"testing"
)

// TestStateRoleNoneIsAccepted: the module prints role=none for a bot that has
// no role (the live rerun's real state line shape). The parser must accept it
// and report an empty role, not refuse the whole record.
func TestStateRoleNoneIsAccepted(t *testing.T) {
	line := strings.Replace(stateLineT("Agentone", "no", ""), "role=dps", "role=none", 1)
	b := mustBot(t, line)
	if b.Role != "" {
		t.Fatalf("role=none must parse to the empty role, got %q", b.Role)
	}
	// An unknown word is still refused.
	bad := strings.Replace(line, "role=none", "role=banana", 1)
	if _, err := NewClient(oneReply("state-begin #T", bad, stateEndT(1, 0, "yes", ""))).
		StateReport(t.Context(), "party"); err == nil {
		t.Fatal("role=banana must stay a parse error")
	}
}
