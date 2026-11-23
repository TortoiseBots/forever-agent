package world

// Create on login (#99): CreateSpec.OnlyIfMissing reads the character list first and sends the create only when the
// name is not on it. The first failing case is the existing character: without the flag the same call would send a
// second create for a name the account holds.

import "testing"

func TestCreateOnLoginSendsNothingForAnExistingCharacter(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "Agtalpha", Race: 1, Class: 1})
	defer f.close()
	spec := CreateSpec{Options: cs.options(f), Name: "agtalpha", Race: 1, Class: 1, NoAppearance: true, OnlyIfMissing: true}
	report, err := CreateCharacter(t.Context(), spec)
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	f.wait(t)
	if !report.Existed || report.GUID != fakeCharGUID(0) {
		t.Fatalf("report %+v, want Existed with the list's guid", report)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.creates) != 0 {
		t.Fatalf("%d create packet(s) for a character the account holds", len(cs.creates))
	}
	if report.Code != 0 || len(report.After) != 0 {
		t.Fatalf("an existing character produced create output: %+v", report)
	}
}

func TestCreateOnLoginCreatesAMissingCharacter(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "Someone", Race: 1, Class: 1})
	defer f.close()
	spec := CreateSpec{Options: cs.options(f), Name: "Agtalpha", Race: 2, Class: 4, Sex: 1, NoAppearance: true, OnlyIfMissing: true}
	report, err := CreateCharacter(t.Context(), spec)
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	f.wait(t)
	if report.Existed || report.Code != fakeCharCreateSuccess {
		t.Fatalf("report %+v", report)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.creates) != 1 || cs.creates[0].Name != "Agtalpha" || cs.creates[0].Race != 2 || cs.creates[0].Class != 4 || cs.creates[0].Sex != 1 {
		t.Fatalf("creates %+v", cs.creates)
	}
}

// TestCreateOnLoginSurnameMatchesCaseInsensitively: a surname given must match the listed one, ignoring case.
func TestCreateOnLoginSurnameMatchesCaseInsensitively(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "Agtalpha", Surname: "Smith", Race: 1, Class: 1})
	defer f.close()
	same := CreateSpec{Options: cs.options(f), Name: "Agtalpha", Surname: "smith", Race: 1, Class: 1, NoAppearance: true, OnlyIfMissing: true}
	report, err := CreateCharacter(t.Context(), same)
	if err != nil || !report.Existed {
		t.Fatalf("same surname: %+v %v", report, err)
	}
	f.wait(t)
}
