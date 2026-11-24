package main

// tbm-check's --bots parser: at most four owned bot names, each the v1 builder's normalised character name, no
// name twice. The check adds exactly these names and removes exactly the ones it added, so the parser's
// normalisation is the cleanup's own name.

import (
	"strings"
	"testing"
)

func TestParseTBMBots(t *testing.T) {
	got, err := parseTBMBots("")
	if err != nil || got != nil {
		t.Fatalf("no --bots = %v, %v; want no names and no error", got, err)
	}
	got, err = parseTBMBots("botone, Bottwo ,Bobthree")
	if err != nil {
		t.Fatalf("three names: %v", err)
	}
	if strings.Join(got, ",") != "Botone,Bottwo,Bobthree" {
		t.Fatalf("the names = %q, want the builder's normalised form", got)
	}
	got, err = parseTBMBots("A,B,C,D")
	if err != nil || len(got) != 4 {
		t.Fatalf("four names = %v, %v; a full party must be accepted", got, err)
	}
	for _, bad := range []string{
		"A,B,C,D,E",     // a party holds four bots
		"Botone,botone", // one name twice
		"Bot one",       // a name with a space is not a character name
		"Bot1",          // a digit is not a character name
	} {
		if got, err := parseTBMBots(bad); err == nil {
			t.Fatalf("--bots %q = %v, want a usage error", bad, got)
		}
	}
}
