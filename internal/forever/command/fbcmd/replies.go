package fbcmd

// The reply lines this client matches, in one place: every server string whose shape a command depends on is here,
// so a worded change in the module shows up as one edit rather than a hunt through the client.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
)

const (
	// aboutMarker is in the probe's answer. It names the public project, which is what the module is.
	aboutMarker = "Forever Bots"
	// onTheWaySuffix ends a successful add's line: the created bot's name is the rest.
	onTheWaySuffix = " is on the way."
	// dismissedPrefix starts a successful dismiss's line.
	dismissedPrefix = "Dismissed "
	dismissedSuffix = " companion(s)."
	// listPattern is one line of the roster fallback: a name, the level, the class and the state word.
	listPattern = `^([A-Za-z][A-Za-z']*): level ([0-9]+) (.+?), (online|offline)(, saved)?$`
	// botPrefix separates the bot's name from its order acknowledgement in a whisper.
	botPrefix = ": "
)

// addRefusals are the add command's refusal prefixes (the module writes its own refusals; these are the stable
// parts of them).
var addRefusals = []string{
	"Bot add failed",
	"Couldn't create the bot",
	"but the login was refused",
	"That race can't be",
	"That race is from the other faction",
	"No race of your faction",
	"Don't know '",
	"WoW Forever Bots. Usage:",
	" is full:",
}

// dismissRefusals are the dismiss command's refusal texts.
var dismissRefusals = []string{
	"No bot or summoned alt",
	".bot dismiss <name|all>",
}

// orderRefusals are the parts of an order acknowledgement that mean the bot did not take the order.
var orderRefusals = []string{
	"not attacking yet",
	"can't be summoned",
	"not a tank",
	"only take orders from",
	"refused",
}

var listRE = regexp.MustCompile(listPattern)

// BotRow is one bot of the chat fallback list (`.bot list`).
type BotRow struct {
	Name    string
	Level   int
	Class   string
	Online  bool
	Saved   bool
	Line    string // the display form of the whole line
	RawLine string
}

// onTheWay reads "<Name> is on the way." and returns the name. The name is checked with the server's own name
// rule, so nothing from a line can travel into a whisper as an arbitrary string.
func onTheWay(line string) (string, bool) {
	i := strings.Index(line, onTheWaySuffix)
	if i <= 0 {
		return "", false
	}
	name := strings.TrimSpace(line[:i])
	if err := chat.ValidateName(name); err != nil {
		return "", false
	}
	return name, true
}

// dismissedCount reads "Dismissed N companion(s).".
func dismissedCount(line string) (int, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, dismissedPrefix) || !strings.HasSuffix(t, dismissedSuffix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(t, dismissedPrefix), dismissedSuffix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// listRow reads one fallback line at offset i.
func listRow(lines []string, i int) (BotRow, bool) {
	line := strings.TrimSpace(lines[i])
	m := listRE.FindStringSubmatch(line)
	if m == nil {
		return BotRow{}, false
	}
	level, err := strconv.Atoi(m[2])
	if err != nil {
		return BotRow{}, false
	}
	if err := chat.ValidateName(m[1]); err != nil {
		return BotRow{}, false
	}
	return BotRow{
		Name:    m[1],
		Level:   level,
		Class:   chat.Sanitise(m[3], 32),
		Online:  m[4] == "online",
		Saved:   m[5] != "",
		Line:    chat.Sanitise(line, 200),
		RawLine: line,
	}, true
}

// refusedLine reports whether a line is one of the named refusals, and its display form.
func refusedLine(line string, fragments []string) (string, bool) {
	for _, f := range fragments {
		if strings.Contains(line, f) {
			return chat.Sanitise(strings.TrimSpace(line), 300), true
		}
	}
	return "", false
}

// agreement is one acknowledgement: the line with the bot's "<Name>: " prefix removed, when it has it.
func acknowledgement(bot, text string) string {
	text = strings.TrimSpace(text)
	if len(text) > len(bot)+len(botPrefix) && strings.EqualFold(text[:len(bot)], bot) &&
		text[len(bot):len(bot)+len(botPrefix)] == botPrefix {
		return text[len(bot)+len(botPrefix):]
	}
	return text
}

// refusedAck reports whether an acknowledgement says the bot did not take the order.
func refusedAck(text string) (string, bool) { return refusedLine(text, orderRefusals) }

// ValidateClass checks a class word against the classes this server can create.
func ValidateClass(class string) error {
	switch strings.ToLower(class) {
	case "warrior", "paladin", "hunter", "rogue", "priest", "shaman", "mage", "warlock", "druid":
		return nil
	}
	return fmt.Errorf("fbcmd: %q is not a class this server creates (warrior, paladin, hunter, rogue, priest, shaman, mage, warlock, druid)", class)
}

// ValidateRole checks a role word. Empty means the module picks one.
func ValidateRole(role string) error {
	switch strings.ToLower(role) {
	case "", "tank", "healer", "dps":
		return nil
	}
	return fmt.Errorf("fbcmd: %q is not a role (tank, healer, dps)", role)
}

// ValidateWord checks an order word: letters, digits and spaces, at most 64 bytes. An order is typed into a chat
// line, so nothing that could start a new line or a command is allowed.
func ValidateWord(word string) error {
	if word == "" {
		return fmt.Errorf("fbcmd: the order word is empty")
	}
	if len(word) > 64 {
		return fmt.Errorf("fbcmd: the order word is over 64 bytes")
	}
	for _, r := range word {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '-', r == '_':
		default:
			return fmt.Errorf("fbcmd: the order word has an unexpected character %q", r)
		}
	}
	return nil
}

// validateName checks a bot name with the server's own rule and returns the normalised form.
func validateName(name string) (string, error) {
	n, err := chat.NormaliseName(name)
	if err != nil {
		return "", fmt.Errorf("fbcmd: bot name: %w", err)
	}
	return n, nil
}
