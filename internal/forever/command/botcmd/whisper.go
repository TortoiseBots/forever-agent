package botcmd

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/chat"
)

// The two whisper words of the module's chat grammar A (B-6, round P3): a dead owned bot releases on `release` and
// runs back to its corpse on `corpse run`. They are the only lines a player run sends that are not `.bot` lines, and
// they are the whole of the grammar: no other word reaches the module as a whisper.
const (
	WhisperRelease   = "release"
	WhisperCorpseRun = "corpse run"
)

// Whisper checks one whisper request: the bot's name is normalised the way a `.bot` line's is, and the word must be
// one of the grammar's two. It returns the normalised bot's name. This layer builds no line for a whisper: the request
// is a chat word with a target, and the module answers nothing a reply could be classified from.
func Whisper(bot, word string) (string, error) {
	switch word {
	case WhisperRelease, WhisperCorpseRun:
	default:
		// A chat word, not a command line: the two constants are the whole grammar, so nothing else — a newline, a
		// chain separator, another word — can reach the wire.
		return "", fmt.Errorf("botcmd: %q is not a whisper word of the module's grammar", word)
	}
	return chat.NormaliseName(bot)
}
