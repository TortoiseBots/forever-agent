package fbcmd

// The dungeon-finder half of the module: `.bot lfg` fills the group, `.bot dungeon <name>` queues a dungeon and puts
// the group inside its door, `.bot dungeon cancel|leave` stops the queue or takes the character out, and
// `.bot lead on|pause|off` hands the dungeon to the tank bot. State is read through the module's LFG_REQ/LFG_RESP
// pair of the add-on protocol; the answers to the commands are the module's own system lines, matched here on their
// raw text.
//
// The caller decides when the run is "inside": the module's `inside` phase is one signal, the client's own map
// change to the instance is the other. This package never treats either one alone as proof.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
)

// LfgResult is the module's one-line answer to `.bot lfg`.
type LfgResult struct {
	Line    string
	Joining int    // bots the module started for the missing places
	Missing string // the gap it named ("1 tank, 2 damage"), "" when the group was already full
	// AlreadyFull is the party size when the module answered that no place was missing.
	AlreadyFull int
}

// LfgResult.Filled reports whether the module said it needs no more bots.
func (r LfgResult) Filled() bool { return r.AlreadyFull > 0 }

// QueueResult is the module's answer to `.bot dungeon <name>`.
type QueueResult struct {
	Line    string
	Name    string // the dungeon as the module names it
	Levels  [2]int
	InPlace bool // the answer said the group is complete and goes in at once
}

// LeadResult is the module's answer to `.bot lead on|pause|off`.
type LeadResult struct {
	State   string // on, pause or off
	Tank    string // the tank the module named, "" when the answer names none
	Line    string
	Outside bool // lead on: the module will lead once the group is inside a dungeon
}

// The command words this client sends.
const (
	leadOn    = "on"
	leadPause = "pause"
	leadOff   = "off"
)

// Lfg asks the module to fill the group's missing places now. role is the caller's own role (tank, healer, dps) or
// "" to let the module read it from the class and the group.
func (c *Client) Lfg(ctx context.Context, role string) (LfgResult, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if err := ValidateRole(role); err != nil {
		return LfgResult{}, err
	}
	line := ".bot lfg"
	if role != "" {
		line += " " + role
	}
	rep, err := c.run(ctx, line)
	if err != nil {
		return LfgResult{}, err
	}
	return parseLfgReply(replyRaw(rep))
}

// parseLfgReply reads the module's one-line answer. The module writes the line itself (localised; this client asks
// for English), so the parser accepts the two positive shapes and names every refusal it knows.
func parseLfgReply(lines []string) (LfgResult, error) {
	for _, raw := range lines {
		if text, bad := refusedLine(raw, lfgRefusals); bad {
			return LfgResult{}, &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range lines {
		if m := lfgJoiningRE.FindStringSubmatch(raw); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 0 {
				continue
			}
			return LfgResult{Line: chat.Sanitise(raw, 300), Joining: n, Missing: chat.Sanitise(m[2], 120)}, nil
		}
		if m := lfgFullRE.FindStringSubmatch(raw); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil || n <= 0 {
				continue
			}
			return LfgResult{Line: chat.Sanitise(raw, 300), AlreadyFull: n}, nil
		}
	}
	text := ""
	if len(lines) > 0 {
		text = chat.Sanitise(lines[0], 300)
	}
	return LfgResult{}, &RefusalError{Err: ErrNoAck, Text: text}
}

// QueueDungeon queues name (a dungeon word or "any") with the module's finder. role is the caller's own role.
func (c *Client) QueueDungeon(ctx context.Context, name, role string) (QueueResult, error) {
	name = strings.TrimSpace(name)
	if err := ValidateDungeonWord(name); err != nil {
		return QueueResult{}, err
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if err := ValidateRole(role); err != nil {
		return QueueResult{}, err
	}
	line := ".bot dungeon " + name
	if role != "" {
		line += " " + role
	}
	rep, err := c.run(ctx, line)
	if err != nil {
		return QueueResult{}, err
	}
	return parseQueueReply(replyRaw(rep))
}

func parseQueueReply(lines []string) (QueueResult, error) {
	for _, raw := range lines {
		if text, bad := refusedLine(raw, dungeonRefusals); bad {
			return QueueResult{}, &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range lines {
		m := dungeonQueuedRE.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		lo, err1 := strconv.Atoi(m[2])
		hi, err2 := strconv.Atoi(m[3])
		if err1 != nil || err2 != nil || lo < 0 || hi < lo {
			continue
		}
		res := QueueResult{
			Line:   chat.Sanitise(raw, 400),
			Name:   chat.Sanitise(m[1], 80),
			Levels: [2]int{lo, hi},
		}
		rest := raw[len(m[0]):]
		res.InPlace = strings.Contains(rest, "in you go") || strings.Contains(rest, "into ")
		return res, nil
	}
	text := ""
	if len(lines) > 0 {
		text = chat.Sanitise(lines[0], 300)
	}
	return QueueResult{}, &RefusalError{Err: ErrNoAck, Text: text}
}

// CancelDungeon stops a queue that has not entered yet.
func (c *Client) CancelDungeon(ctx context.Context) (string, error) {
	rep, err := c.run(ctx, ".bot dungeon cancel")
	if err != nil {
		return "", err
	}
	lines := replyRaw(rep)
	for _, raw := range lines {
		if text, bad := refusedLine(raw, dungeonRefusals); bad {
			return "", &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range lines {
		if strings.HasPrefix(raw, "Dungeon finder: ") && strings.Contains(raw, " cancelled. ") {
			return chat.Sanitise(raw, 400), nil
		}
	}
	text := ""
	if len(lines) > 0 {
		text = chat.Sanitise(lines[0], 300)
	}
	return "", &RefusalError{Err: ErrNoAck, Text: text}
}

// LeaveDungeon takes the character (and its bots) out of the instance.
func (c *Client) LeaveDungeon(ctx context.Context) (string, error) {
	rep, err := c.run(ctx, ".bot dungeon leave")
	if err != nil {
		return "", err
	}
	lines := replyRaw(rep)
	for _, raw := range lines {
		if text, bad := refusedLine(raw, dungeonRefusals); bad {
			return "", &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	for _, raw := range lines {
		if strings.HasPrefix(raw, "Dungeon finder: out you go. ") {
			return chat.Sanitise(raw, 400), nil
		}
	}
	text := ""
	if len(lines) > 0 {
		text = chat.Sanitise(lines[0], 300)
	}
	return "", &RefusalError{Err: ErrNoAck, Text: text}
}

// Lead sets the module's tank-lead switch: on, pause or off.
func (c *Client) Lead(ctx context.Context, word string) (LeadResult, error) {
	word = strings.ToLower(strings.TrimSpace(word))
	switch word {
	case leadOn, leadPause, leadOff:
	default:
		return LeadResult{}, fmt.Errorf("fbcmd: %q is not a lead order (on, pause, off)", word)
	}
	rep, err := c.run(ctx, ".bot lead "+word)
	if err != nil {
		return LeadResult{}, err
	}
	return parseLeadReply(word, replyRaw(rep))
}

func parseLeadReply(word string, lines []string) (LeadResult, error) {
	for _, raw := range lines {
		// A build whose command table predates `.bot lead` answers with the core's usage block instead of a
		// sentence: name that shape rather than printing "refused" over a list of subcommands.
		if strings.Contains(raw, "Possible subcommands:") || strings.HasPrefix(strings.TrimSpace(raw), "### USAGE: .bot") {
			return LeadResult{}, &RefusalError{
				Err:  ErrRefused,
				Text: "the module's build does not know .bot lead (it answered the core's .bot usage list)",
			}
		}
		if text, bad := refusedLine(raw, leadRefusals); bad {
			return LeadResult{}, &RefusalError{Err: ErrRefused, Text: text}
		}
	}
	state := ""
	switch word {
	case leadOn:
		state = "on"
	case leadPause:
		state = "pause"
	case leadOff:
		state = "off"
	}
	for _, raw := range lines {
		res := LeadResult{State: state, Line: chat.Sanitise(raw, 400), Tank: leadTank(raw)}
		switch {
		case strings.Contains(raw, " holds here and pulls nothing until you say 'go'."):
			res.State = "pause"
			return res, nil
		case strings.Contains(raw, "You lead again: your bots follow you."):
			res.State = "off"
			return res, nil
		case strings.Contains(raw, " leads once you are inside a dungeon or a raid."):
			res.State, res.Outside = "on", true
			return res, nil
		case strings.Contains(raw, " leads the dungeon now:"), strings.Contains(raw, " leads the raid now:"),
			strings.Contains(raw, " leads on."), strings.Contains(raw, " pulls the boss."):
			res.State = "on"
			return res, nil
		}
	}
	text := ""
	if len(lines) > 0 {
		text = chat.Sanitise(lines[0], 300)
	}
	return LeadResult{}, &RefusalError{Err: ErrNoAck, Text: text}
}

// leadTank reads the tank's name from the prefix of a lead answer: "<Name> leads ...", "<Name> holds ..." or
// "<Name> pulls ...". The name is checked with the server's own rule, so nothing from a line becomes an arbitrary
// string.
func leadTank(line string) string {
	for _, sep := range []string{" leads ", " holds ", " pulls "} {
		if i := strings.Index(line, sep); i > 0 {
			name := strings.TrimSpace(line[:i])
			if chat.ValidateName(name) == nil {
				return name
			}
		}
	}
	return ""
}

// LfgStatus reads the finder's state through the module's LFG protocol.
func (c *Client) LfgStatus(ctx context.Context) (pbc.LfgStatus, error) {
	ans, err := c.tr.Request(ctx, Request{
		Type: pbc.TypeLfgReq, Fields: pbc.LfgRequest(), Answer: pbc.TypeLfgResp, Wait: c.AnswerWait,
	})
	if err != nil {
		return pbc.LfgStatus{}, err
	}
	return pbc.ParseLfg(ans.Fields)
}

// Events returns the module's unsolicited pushes collected since the last call. It is optional: a transport that
// does not implement it (a fake in a unit test) reports none.
func (c *Client) Events() []pbc.Event {
	if e, ok := c.tr.(interface{ Events() []pbc.Event }); ok {
		return e.Events()
	}
	return nil
}

// The module's reply shapes, from its own handlers (cs_playerbot_v2.cpp HandleBotLfg/HandleBotDungeon/HandleBotLead,
// DungeonFinder.cpp, LfgBotFill.cpp).
var (
	lfgJoiningRE    = regexp.MustCompile(`^([0-9]+) bot\(s\) are joining \((.+?) missing\)\.`)
	lfgFullRE       = regexp.MustCompile(`^Your group is already full: ([0-9]+) players\.`)
	dungeonQueuedRE = regexp.MustCompile(`^Dungeon finder: (.+) \(levels ([0-9]+)-([0-9]+)\)\.`)
	lfgRefusals     = []string{"Only the group leader can fill the group.", "Usage: .bot lfg"}
	dungeonRefusals = []string{
		"The dungeon finder is off on this realm",
		"You are already queued for ",
		"Dungeon finder: not now, ",
		"Dungeon finder: you are already in an instance.",
		"Dungeon finder: only the group leader can queue the group.",
		`Dungeon finder: no dungeon called "`,
		"Dungeon finder: which one?",
		"Dungeon finder: no entrance of ",
		" does not let you in: ",
		"You are not queued for a dungeon.",
		"You are not in a dungeon.",
		"Dungeon finder: release or be resurrected first.",
		"Dungeon finder: not while you are in combat.",
		"Dungeon finder: no way out of this dungeon is known",
		"Dungeon finder: the teleport out was refused.",
		"Usage: .bot dungeon",
	}
	leadRefusals = []string{
		"You have no group yet: add a bot first.",
		"No bot of yours in the group.",
		"None of your bots in the group is a tank:",
		"Your tank does not lead now:",
		"In a raid the tank leads a raid group:",
		"Tank leads: say 'lead on'",
	}
)

// ValidateDungeonWord checks the word sent as the dungeon argument: letters, digits, spaces and the punctuation the
// module's own names use. It is typed into a chat line, so nothing that could start a command or a new line.
func ValidateDungeonWord(word string) error {
	if word == "" {
		return fmt.Errorf("fbcmd: the dungeon word is empty")
	}
	if len(word) > 64 {
		return fmt.Errorf("fbcmd: the dungeon word is over 64 bytes")
	}
	for _, r := range word {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ' ', r == '-', r == '_', r == '\'', r == '(', r == ')':
		default:
			return fmt.Errorf("fbcmd: the dungeon word has an unexpected character %q", r)
		}
	}
	return nil
}

// The default dungeons of the module's finder (Playerbot.Lfg.BotGroups.Activities, its public conf defaults): the
// name the module answers with, the short words players type, and the map id. They let the caller tie the client's
// own map change to the dungeon it queued; an unknown word still works with the module, without that tie.
type DungeonInfo struct {
	Name  string
	Keys  []string
	MapID uint32
}

var defaultDungeons = []DungeonInfo{
	{"Wailing Caverns", []string{"wc"}, 43},
	{"Scholomance", []string{"scholo"}, 289},
	{"Ragefire Chasm", []string{"rfc"}, 389},
	{"Deadmines", []string{"vc", "dm"}, 36},
	{"Shadowfang Keep", []string{"sfk"}, 33},
	{"Blackfathom Deeps", []string{"bfd"}, 48},
	{"Stormwind Stockades", []string{"stocks", "stockade"}, 34},
	{"Gnomeregan", []string{"gnomer"}, 90},
	{"Razorfen Kraul", []string{"rfk"}, 47},
	{"Scarlet Monastery - Graveyard", []string{"smgy", "gy", "graveyard"}, 189},
	{"Razorfen Downs", []string{"rfd"}, 129},
	{"Uldaman", []string{"ulda", "uld"}, 70},
	{"Zul'Farrak", []string{"zf"}, 209},
	{"Maraudon", []string{"mara"}, 349},
	{"Sunken Temple", []string{"st"}, 109},
	{"Blackrock Depths", []string{"brd"}, 230},
	{"Lower Blackrock Spire", []string{"lbrs"}, 229},
	{"Dire Maul (East)", []string{"dme", "dmeast"}, 429},
	{"Dire Maul (West)", []string{"dmw", "dmwest"}, 429},
	{"Dire Maul (North)", []string{"dmn", "dmnorth", "tribute"}, 429},
	{"Stratholme (Main Gate)", []string{"strat"}, 329},
	{"Scarlet Monastery - Armory", []string{"smarm", "arm", "armory"}, 189},
	{"Scarlet Monastery - Cathedral", []string{"smcath", "cath", "cathedral"}, 189},
	{"Scarlet Monastery - Library", []string{"smlib", "lib", "library"}, 189},
	{"Upper Blackrock Spire", []string{"ubrs"}, 229},
}

// LookupDungeon finds a dungeon by a short word or its name, as the module's finder does: a short word or the whole
// name wins at once, otherwise a name holding the word. A word naming more than one dungeon is not resolved.
func LookupDungeon(word string) (DungeonInfo, bool) {
	q := normalizeDungeonWord(word)
	if q == "" {
		return DungeonInfo{}, false
	}
	var partial []DungeonInfo
	for _, d := range defaultDungeons {
		if normalizeDungeonWord(d.Name) == q {
			return d, true
		}
		for _, k := range d.Keys {
			if normalizeDungeonWord(k) == q {
				return d, true
			}
		}
		if strings.Contains(normalizeDungeonWord(d.Name), q) {
			partial = append(partial, d)
		}
	}
	if len(partial) == 1 {
		return partial[0], true
	}
	return DungeonInfo{}, false
}

// normalizeDungeonWord is the module's FinderNormalize: lower case, letters and digits only.
func normalizeDungeonWord(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteByte(byte(r - 'A' + 'a'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}
