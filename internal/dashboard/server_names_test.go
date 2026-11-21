package dashboard

// The server-name override for spellText (CORE-GAINS-3): the server's own
// SpellName records win over the packed table, and no reply means the id as
// today.

import "testing"

func TestServerSpellNamesOverride(t *testing.T) {
	defer SetServerSpellNames(map[uint32]string{})
	// The packed table names Fireball (133); the server's name wins.
	SetServerSpellNames(map[uint32]string{133: "Fireball (server)"})
	if got := spellText(133); got != "Fireball (server)" {
		t.Fatalf("server name not used: %q", got)
	}
	// An id the server did not name falls back to the packed table (774 is
	// "Rejuvenation" there: this fails if the fallback is ever lost, unlike
	// re-reading the server-named id).
	if got := spellText(774); got != "Rejuvenation" {
		t.Fatalf("packed-table fallback lost: %q", got)
	}
	// Clearing the server names restores today's behavior.
	SetServerSpellNames(map[uint32]string{})
	if got := spellText(133); got != "Fireball" {
		t.Fatalf("after clear: %q, want Fireball", got)
	}
	// An id neither names is "spell <id>" as today; zero is "".
	if got := spellText(4294967); got != "spell 4294967" {
		t.Fatalf("unknown id: %q", got)
	}
	if got := spellText(0); got != "" {
		t.Fatalf("zero id: %q", got)
	}
}

// TestServerSpellNamesConcurrent pins finding 2's fix: the page reads names
// while a session-side fetch replaces them, so parallel reads and writes
// must not race (run with -race).
func TestServerSpellNamesConcurrent(t *testing.T) {
	defer SetServerSpellNames(map[uint32]string{})
	done := make(chan struct{})
	for w := range 4 {
		go func(w int) {
			defer func() {
				if w == 0 {
					close(done)
				}
			}()
			for i := range 200 {
				SetServerSpellNames(map[uint32]string{uint32(i): "n"})
				_ = spellText(uint32(i))
			}
		}(w)
	}
	<-done
}
