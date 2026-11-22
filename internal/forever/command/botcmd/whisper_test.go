package botcmd

import "testing"

// TestWhisperClosedVocabulary: the whisper grammar is the two words of B-6 and nothing else, and the name comes back
// normalised the way a `.bot` line's is.
func TestWhisperClosedVocabulary(t *testing.T) {
	for _, word := range []string{WhisperRelease, WhisperCorpseRun} {
		name, err := Whisper("Botone", word)
		if err != nil {
			t.Fatalf("%q was refused: %v", word, err)
		}
		if name != "Botone" {
			t.Fatalf("the name = %q; want Botone", name)
		}
	}
	for _, word := range []string{"", "res", "Release", "corpse", "corpse  run", "release now", "run", "release\n"} {
		if _, err := Whisper("Botone", word); err == nil {
			t.Fatalf("%q passed the whisper grammar", word)
		}
	}
	for _, name := range []string{"", "Bot\\one", "Bot\none"} {
		if _, err := Whisper(name, WhisperRelease); err == nil {
			t.Fatalf("the name %q passed normalisation", name)
		}
	}
}
