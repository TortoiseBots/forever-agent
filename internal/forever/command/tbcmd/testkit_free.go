package tbcmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// The test kit's free-text commands: the setup words the module answers in
// plain server text, not the v2 grammar (TB-COMMANDS section 2): `create`,
// `login`, `logout`, `group`, `ungroup`, `engine`, `level`, `train`, `hurt`,
// `power`, `rage`, `additem`, `use`, `spells`, `spellscan`, `where`,
// `cast`, `castfail`, `castlog`. Each method sends through the same Runner as
// the typed calls (a fresh token per call), and the answer's acceptance is
// always a read-back through the typed reports: `State` for online and
// grouped, `party` for the roster, the units model for hp, and `Learn`/
// `ItemScan` style records for spells and items. A refusal is the server's
// message text in a TestkitRefused error; a transport or collector error is
// returned as is.

// TestkitRefused is a free-text `.tb` command's refusal: the server's own
// message text (its wording is the core's and the module's, never this
// layer's), kept whole for the evidence.
type TestkitRefused struct {
	Cmd  string
	Text string
}

func (e *TestkitRefused) Error() string {
	if e.Text == "" {
		return fmt.Sprintf("tbcmd: .tb %s refused", e.Cmd)
	}
	return fmt.Sprintf("tbcmd: .tb %s refused: %s", e.Cmd, e.Text)
}

// looksLikeRefusal classifies the server's free-text answer: a line that
// reads like a refusal (the wording of RequireBot, the GM side and the core)
// refuses, anything else that proves the change holds accepts. The matcher is
// deliberately word-level: the server's wording is its own, and matching a
// whole sentence would pin text the module never promised.
var refusalWords = []string{
	"not-a-bot", "not a bot", "not-a-player", "not in the world", "not logged in",
	"not created", "not logged", "not grouped", "not ungrouped", "no such player",
	"no character named", "that name is not allowed", "is not a logged-in bot",
	"real player's character", "only the core's", "not taught", "not stored",
	"bags full", "does not exist", "the item was not stored", "dead",
	"the percent must be", "the level must be", "usage:", "is dead",
	"refused", "refuse", "cannot", "can't", "failed", "fail", "no permission",
	"unknown command", "incorrect syntax", "not confirmed",
}

// IsRefusal reports whether err is a *TestkitRefused.
func IsRefusal(err error) bool {
	_, ok := asTestkitRefused(err)
	return ok
}

func asTestkitRefused(err error) (*TestkitRefused, bool) {
	for e := err; e != nil; {
		if t, ok := e.(*TestkitRefused); ok {
			return t, true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		e = u.Unwrap()
	}
	return nil, false
}

// looksLikeRefusal is true when the server's text reads like a refusal.
func looksLikeRefusal(text string) bool {
	lower := strings.ToLower(text)
	for _, w := range refusalWords {
		if strings.Contains(lower, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// freeTextNamed sends one tokenless plain-text `.tb` line and returns the
// server's visible answer lines (the collector's tokenless tail), narrowed
// to the lines that name
// the command's subject: the tokenless channel also carries other lines
// (a GM probe block, a concurrent typed reply), and a refusal matcher must
// not read those as this command's answer. Lines that name nobody are
// dropped; an empty answer is not a refusal (the caller's read-back decides).
func (c *Client) freeTextNamed(ctx context.Context, line, name string) ([]string, *command.Reply, error) {
	o := c.Base
	o.Token = ""
	o.EndKind = ""
	rep, err := c.run.Run(ctx, line, o)
	if err != nil {
		return nil, rep, err
	}
	if rep == nil {
		return nil, nil, fmt.Errorf("%w: .tb %s: no reply", ErrBadReport, line)
	}
	lines := dropServerNoise(rep.Raw)
	if name != "" {
		lines = namedOnly(lines, name)
	}
	return lines, rep, nil
}

// namedOnly keeps the lines that name subject (case-insensitive).
func namedOnly(lines []string, subject string) []string {
	needle := strings.ToLower(subject)
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), needle) {
			out = append(out, l)
		}
	}
	return out
}

// dropServerNoise removes lines the command channel sends beside the answer:
// the GM probe's help block (the serve confirms the GM channel once per
// session, and a late block can land in the next reply) and the login
// welcome. They contain words a refusal matcher reads as a refusal, but the
// command itself answered.
func dropServerNoise(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "### USAGE:"),
			strings.HasPrefix(t, "|- "),
			strings.HasPrefix(t, "Possible subcommands:"),
			strings.HasPrefix(t, "Welcome to a TrinityCore server."):
			continue
		}
		out = append(out, line)
	}
	return out
}

// FreeTextCreate sends `.tb create <account> <name> <race id> <class id>
// [gender]`: a new level 1 character. The acceptance is the server's own
// "created" line; anything reading like a refusal (a taken name, a bad
// race/class) is a *TestkitRefused. The caller proves the character with a
// typed read-back (login + `State`).
func (c *Client) FreeTextCreate(ctx context.Context, account, name string, raceID, classID uint8, gender *uint8) ([]string, error) {
	if strings.TrimSpace(account) == "" {
		return nil, fmt.Errorf("tbcmd: create needs an account")
	}
	setup, err := setupName("create", name)
	if err != nil {
		return nil, err
	}
	line := ".tb create " + account + " " + setup + " " + strconv.Itoa(int(raceID)) + " " + strconv.Itoa(int(classID))
	if gender != nil {
		if *gender > 1 {
			return nil, fmt.Errorf("tbcmd: create gender is 0 or 1 (got %d)", *gender)
		}
		line += " " + strconv.Itoa(int(*gender))
	}
	raws, rep, err := c.freeTextNamed(ctx, line, name)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "create", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) || !strings.Contains(strings.ToLower(joined), "created") {
		return raws, &TestkitRefused{Cmd: "create", Text: joined}
	}
	return raws, nil
}

// FreeTextLogin sends `.tb login <character> [master]`: the character joins
// the world as a bot. The acceptance is the server's "logging in" line; the
// caller proves it with `State` (the bot reads back online).
func (c *Client) FreeTextLogin(ctx context.Context, name, master string) ([]string, error) {
	setup, err := setupName("login", name)
	if err != nil {
		return nil, err
	}
	line := ".tb login " + setup
	if strings.TrimSpace(master) != "" {
		m, err := setupName("login", master)
		if err != nil {
			return nil, err
		}
		line += " " + m
	}
	raws, rep, err := c.freeTextNamed(ctx, line, name)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "login", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) || !strings.Contains(strings.ToLower(joined), "logging in") {
		return raws, &TestkitRefused{Cmd: "login", Text: joined}
	}
	return raws, nil
}

// FreeTextLogout sends `.tb logout <character>`: the bot leaves.
func (c *Client) FreeTextLogout(ctx context.Context, name string) ([]string, error) {
	setup, err := setupName("login", name)
	if err != nil {
		return nil, err
	}
	raws, rep, err := c.freeTextNamed(ctx, ".tb logout "+setup, name)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "logout", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) {
		return raws, &TestkitRefused{Cmd: "logout", Text: joined}
	}
	return raws, nil
}

// FreeTextGroup sends `.tb group <bot> <master>`: the bot joins the master's
// party (the master leads). The caller proves it with `Party` (the bot reads
// back in the party list).
func (c *Client) FreeTextGroup(ctx context.Context, bot, master string) ([]string, error) {
	b, err := setupName("group", bot)
	if err != nil {
		return nil, err
	}
	m, err := setupName("group", master)
	if err != nil {
		return nil, err
	}
	raws, rep, err := c.freeTextNamed(ctx, ".tb group "+b+" "+m, b)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "group", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) || !strings.Contains(strings.ToLower(joined), "joined the party") {
		return raws, &TestkitRefused{Cmd: "group", Text: joined}
	}
	return raws, nil
}

// FreeTextUngroup sends `.tb ungroup <bot>`.
func (c *Client) FreeTextUngroup(ctx context.Context, bot string) ([]string, error) {
	b, err := setupName("group", bot)
	if err != nil {
		return nil, err
	}
	raws, rep, err := c.freeTextNamed(ctx, ".tb ungroup "+b, b)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "ungroup", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) {
		return raws, &TestkitRefused{Cmd: "ungroup", Text: joined}
	}
	return raws, nil
}

// FreeTextEngine sends `.tb engine <character> on|off`: the bot runs the
// real engine or the spike behaviour. The caller proves it with `State` (the
// engine=yes/no read-back).
func (c *Client) FreeTextEngine(ctx context.Context, name string, on bool) ([]string, error) {
	setup, err := setupName("engine", name)
	if err != nil {
		return nil, err
	}
	word := "off"
	if on {
		word = "on"
	}
	raws, rep, err := c.freeTextNamed(ctx, ".tb engine "+setup+" "+word, name)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "engine", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) {
		return raws, &TestkitRefused{Cmd: "engine", Text: joined}
	}
	return raws, nil
}

// FreeTextLevel sends `.tb level <bot> <1-60>`: the core's own level-up path.
// The caller proves it with the units model (the level reads back).
func (c *Client) FreeTextLevel(ctx context.Context, name string, level uint8) ([]string, error) {
	setup, err := setupName("level", name)
	if err != nil {
		return nil, err
	}
	if level < 1 || level > 60 {
		return nil, fmt.Errorf("tbcmd: level must be 1 to 60 (got %d)", level)
	}
	raws, rep, err := c.freeTextNamed(ctx, ".tb level "+setup+" "+strconv.Itoa(int(level)), name)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "level", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) || !strings.Contains(strings.ToLower(joined), "is level") {
		return raws, &TestkitRefused{Cmd: "level", Text: joined}
	}
	return raws, nil
}

// FreeTextTrain sends `.tb train <bot>`: the class-trainer + skill-line
// passes. The caller proves it with a `Learn` of the kit read-back (the
// spells read back known).
func (c *Client) FreeTextTrain(ctx context.Context, name string) ([]string, error) {
	setup, err := setupName("train", name)
	if err != nil {
		return nil, err
	}
	raws, rep, err := c.freeTextNamed(ctx, ".tb train "+setup, name)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "train", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) {
		return raws, &TestkitRefused{Cmd: "train", Text: joined}
	}
	return raws, nil
}

// FreeTextHurt sends `.tb hurt [<character>] <pct>`: the character's health
// to that percent of its maximum (at least 1). An empty name is the no-name
// form for the sender's own character. The caller proves it with the units
// model (hp/max_hp reads back at the percent).
func (c *Client) FreeTextHurt(ctx context.Context, target string, pct uint8) ([]string, error) {
	if pct > 100 {
		return nil, fmt.Errorf("tbcmd: hurt percent must be 0 to 100 (got %d)", pct)
	}
	line := ".tb hurt " + strconv.Itoa(int(pct))
	if strings.TrimSpace(target) != "" {
		setup, err := setupName("hurt", target)
		if err != nil {
			return nil, err
		}
		line = ".tb hurt " + setup + " " + strconv.Itoa(int(pct))
	}
	raws, rep, err := c.freeTextNamed(ctx, line, target)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "hurt", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) || !strings.Contains(strings.ToLower(joined), "health is") {
		return raws, &TestkitRefused{Cmd: "hurt", Text: joined}
	}
	return raws, nil
}

// FreeTextPower sends `.tb power [<character>] mana|rage|energy <pct>`: that
// power to the percent of its maximum. An empty name is the no-name form for
// the sender's own character. The caller proves it with the units model.
func (c *Client) FreeTextPower(ctx context.Context, target, kind string, pct uint8) ([]string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "mana", "rage", "energy":
	default:
		return nil, fmt.Errorf("tbcmd: power kind %q is not mana, rage or energy", kind)
	}
	if pct > 100 {
		return nil, fmt.Errorf("tbcmd: power percent must be 0 to 100 (got %d)", pct)
	}
	line := ".tb power " + kind + " " + strconv.Itoa(int(pct))
	if strings.TrimSpace(target) != "" {
		setup, err := setupName("power", target)
		if err != nil {
			return nil, err
		}
		line = ".tb power " + setup + " " + kind + " " + strconv.Itoa(int(pct))
	}
	raws, rep, err := c.freeTextNamed(ctx, line, target)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "power", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) || !strings.Contains(strings.ToLower(joined), "is ") {
		return raws, &TestkitRefused{Cmd: "power", Text: joined}
	}
	return raws, nil
}

// FreeTextAddItem sends `.tb additem <bot> <item id> [count]`: a new item
// into the bags. The caller proves it with `ItemScan` (the count reads back).
func (c *Client) FreeTextAddItem(ctx context.Context, bot string, item uint32, count uint32) ([]string, error) {
	setup, err := setupName("additem", bot)
	if err != nil {
		return nil, err
	}
	if item == 0 {
		return nil, fmt.Errorf("tbcmd: additem item id 0 is not an id")
	}
	if count == 0 {
		count = 1
	}
	line := ".tb additem " + setup + " " + strconv.FormatUint(uint64(item), 10)
	if count != 1 {
		line += " " + strconv.FormatUint(uint64(count), 10)
	}
	raws, rep, err := c.freeTextNamed(ctx, line, bot)
	if err != nil {
		return raws, err
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return raws, &TestkitRefused{Cmd: "additem", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) {
		return raws, &TestkitRefused{Cmd: "additem", Text: joined}
	}
	return raws, nil
}

// RemoveItemResult is the module's `removeitem` answer: how many of the item
// it took out of the bot's bags and how many the bags still hold.
type RemoveItemResult struct {
	Bot     string
	Item    uint32
	Removed int
	Left    int
}

// FreeTextRemoveItem sends `.tb removeitem <bot> <item id> [count]`: the
// module takes count of an item out of the bot's bags (no count removes
// every one). The module answers one line,
// `removeitem bot=<name> item=<id> removed=<n> left=<n>`; that line is
// parsed and must name the asked bot and item. The old negative-count
// `.tb additem` form is not a removal and is never sent. The caller still
// proves the result with `ItemScan`.
func (c *Client) FreeTextRemoveItem(ctx context.Context, bot string, item uint32, count uint32) (RemoveItemResult, []string, error) {
	setup, err := setupName("removeitem", bot)
	if err != nil {
		return RemoveItemResult{}, nil, err
	}
	if item == 0 {
		return RemoveItemResult{}, nil, fmt.Errorf("tbcmd: removeitem item id 0 is not an id")
	}
	line := ".tb removeitem " + setup + " " + strconv.FormatUint(uint64(item), 10)
	if count > 0 {
		line += " " + strconv.FormatUint(uint64(count), 10)
	}
	raws, rep, err := c.freeTextNamed(ctx, line, bot)
	if err != nil {
		return RemoveItemResult{}, raws, err
	}
	if res, ok, perr := parseRemoveItem(raws); ok {
		if perr != nil {
			return RemoveItemResult{}, raws, perr
		}
		if !strings.EqualFold(res.Bot, setup) || res.Item != item {
			return res, raws, fmt.Errorf("%w: removeitem answered for %q item %d, asked for %q item %d",
				ErrBadReport, res.Bot, res.Item, setup, item)
		}
		return res, raws, nil
	}
	joined := strings.Join(raws, "\n")
	if rep != nil && rep.Refusal != nil {
		return RemoveItemResult{}, raws, &TestkitRefused{Cmd: "removeitem", Text: firstNonEmpty(raws, rep.RefusalText)}
	}
	if looksLikeRefusal(joined) {
		return RemoveItemResult{}, raws, &TestkitRefused{Cmd: "removeitem", Text: joined}
	}
	return RemoveItemResult{}, raws, fmt.Errorf("%w: .tb %s: no removeitem record in the answer", ErrBadReport, line)
}

// parseRemoveItem finds the `removeitem bot= item= removed= left=` line. ok
// is false when no line starts with the record word; err is set when the
// record is there but a key is missing or not a number.
func parseRemoveItem(raws []string) (RemoveItemResult, bool, error) {
	for _, raw := range raws {
		fields := strings.Fields(raw)
		at := -1
		for i, f := range fields {
			if f == "removeitem" {
				at = i
				break
			}
		}
		if at < 0 {
			continue
		}
		kv := map[string]string{}
		for _, f := range fields[at+1:] {
			if strings.HasPrefix(f, "#") {
				continue
			}
			if k, v, found := strings.Cut(f, "="); found {
				kv[k] = v
			}
		}
		var res RemoveItemResult
		res.Bot = kv["bot"]
		item, ierr := strconv.ParseUint(kv["item"], 10, 32)
		removed, rerr := strconv.Atoi(kv["removed"])
		left, lerr := strconv.Atoi(kv["left"])
		if res.Bot == "" || ierr != nil || rerr != nil || lerr != nil || removed < 0 || left < 0 {
			return res, true, fmt.Errorf("%w: removeitem record needs bot=, item=, removed=, left=: %q", ErrBadReport, raw)
		}
		res.Item, res.Removed, res.Left = uint32(item), removed, left
		return res, true, nil
	}
	return RemoveItemResult{}, false, nil
}

// firstNonEmpty prefers the raw lines the server sent; the collector's
// refusal text is the fallback when the run ended another way.
func firstNonEmpty(raws []string, fallback string) string {
	if joined := strings.Join(raws, "\n"); strings.TrimSpace(joined) != "" {
		return joined
	}
	return fallback
}
