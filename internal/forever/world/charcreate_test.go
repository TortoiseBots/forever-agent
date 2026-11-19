package world

import (
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestResolveDeleteCharacter: the delete's name lookup collects every match instead of taking the first one. This
// core has a surname, so two characters can share a first name and differ only there, and the server normalises a
// name to first letter upper and the rest lower (normalizePlayerName, ObjectMgr.cpp:157-181), so any case is the same
// name.
func TestResolveDeleteCharacter(t *testing.T) {
	first := wire.GUID{Low: 0x2211, High: 0x0A44330000000000}
	second := wire.GUID{Low: 0x2212, High: 0x0A44330000000000}
	cased := wire.GUID{Low: 0x2213, High: 0x0A44330000000000}
	list := []Character{
		{GUID: first, Name: "Agtone", Surname: "First", Level: 2},
		{GUID: second, Name: "Agtone", Surname: "Second", Level: 1},
		{GUID: cased, Name: "Agtcase"},
		{GUID: wire.EmptyGUID, Name: "Agtshadow"}, // no GUID: nothing could be deleted for it
	}

	t.Run("not found", func(t *testing.T) {
		_, err := resolveDeleteCharacter(list, wire.GUID{}, "Agtmissing", "")
		var lookup *CharacterLookupError
		if !errors.As(err, &lookup) || len(lookup.Candidates) != 0 {
			t.Fatalf("error %v (%T)", err, err)
		}
		if !strings.Contains(err.Error(), `no character named "Agtmissing"`) {
			t.Fatalf("error %q", err)
		}
	})

	t.Run("case variants", func(t *testing.T) {
		for _, want := range []string{"Agtcase", "agtcase", "AGTCASE", "AgtCase"} {
			c, err := resolveDeleteCharacter(list, wire.GUID{}, want, "")
			if err != nil || c.GUID != cased {
				t.Fatalf("%q: %s, %v", want, c.GUID, err)
			}
		}
	})

	t.Run("same first name is ambiguous", func(t *testing.T) {
		_, err := resolveDeleteCharacter(list, wire.GUID{}, "Agtone", "")
		var lookup *CharacterLookupError
		if !errors.As(err, &lookup) || len(lookup.Candidates) != 2 {
			t.Fatalf("error %v (%T)", err, err)
		}
		for _, want := range []string{"Agtone First", "Agtone Second", first.String(), second.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the error does not list %q: %v", want, err)
			}
		}
	})

	t.Run("the surname picks one", func(t *testing.T) {
		for _, c := range []struct {
			name, surname string
		}{
			{"Agtone First", ""},
			{"Agtone SECOND", ""},
			{"Agtone", "second"},
		} {
			c2, err := resolveDeleteCharacter(list, wire.GUID{}, c.name, c.surname)
			if err != nil {
				t.Fatalf("%q %q: %v", c.name, c.surname, err)
			}
			if c2.GUID != first && c2.GUID != second {
				t.Fatalf("%q %q resolved %s", c.name, c.surname, c2.GUID)
			}
		}
	})

	t.Run("surname twice", func(t *testing.T) {
		if _, err := resolveDeleteCharacter(list, wire.GUID{}, "Agtone First", "First"); err == nil {
			t.Fatal("the surname in both fields was accepted")
		}
	})

	t.Run("zero GUID skipped", func(t *testing.T) {
		if _, err := resolveDeleteCharacter(list, wire.GUID{}, "Agtshadow", ""); err == nil {
			t.Fatal("a row without a GUID was resolved")
		}
	})

	t.Run("by GUID", func(t *testing.T) {
		c, err := resolveDeleteCharacter(list, second, "", "")
		if err != nil || c.GUID != second {
			t.Fatalf("by GUID: %s, %v", c.GUID, err)
		}
		missing := wire.GUID{Low: 0x9999, High: 0x0A44330000000000}
		_, err = resolveDeleteCharacter(list, missing, "", "")
		if err == nil || !strings.Contains(err.Error(), missing.String()) {
			t.Fatalf("a missing GUID: %v", err)
		}
	})
}

// TestNormalizePlayerName: the server's normalisation is first letter upper, the rest lower (ObjectMgr.cpp:157-181),
// and it keeps the first word only.
func TestNormalizePlayerName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"AGTCASE", "Agtcase"},
		{"agtcase", "Agtcase"},
		{"Agtcase Last", "Agtcase"},
		{"  agtcase  ", "Agtcase"},
		{"", ""},
	} {
		if got := normalizePlayerName(c.in); got != c.want {
			t.Errorf("%q normalised to %q, want %q", c.in, got, c.want)
		}
	}
}
