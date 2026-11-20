package tbcmd

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// The test kit's typed commands (TB-COMMANDS section 6): `learn`, `equip`,
// `itemscan` over the v2 grammar, plus `kill` and `gm` (section 8). Every
// call is token-matched like every other layer call: the run's token ends
// every line, the reply is a whole report of the command, and its `count`
// and `records` agree with the lines between the markers (v2.go). A
// per-record refusal is the module's *CommandError, so a caller acts only on
// a nil error; a free-text `.tb` command's refusal is the server's message
// text, classified by looksLikeRefusal (TestkitRefused / ErrTestkitRefused).

// ---- learn ----

// LearnResult is one spell line of a learn answer.
type LearnResult struct {
	Bot    string
	Spell  uint32
	Result string // learned, known, unknown, invalid, not-learned
	Name   string
	Extra  map[string]string
}

var learnRecordKeys = map[string]bool{"bot": true, "spell": true, "result": true, "name": true}

// LearnReport is a whole learn answer: the per-spell records and the end
// line's learned count.
type LearnReport struct {
	Results []LearnResult
	Bot     string
	Learned int
	Raw     []string
}

// parseLearn reads a learn reply: learn-begin, one learn record per spell,
// learn-end count/records/bot/learned. A record whose result is not
// "learned" or "known" is a partial result, not a parse failure, so it
// stays in Results and the caller decides (setup treats unknown/invalid/
// not-learned as a failed row).
func parseLearn(rep *command.Reply) (LearnReport, error) {
	r, err := parseReport(rep, "learn-begin", "learn-end", "learn", "spell")
	if err != nil {
		return LearnReport{}, err
	}
	end := r.End
	out := LearnReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Bot, err = end.requireText("bot"); err != nil {
		return out, err
	}
	if out.Learned, err = end.requireInt("learned", 0, math.MaxInt32); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var res LearnResult
		if res.Bot, err = rec.requireText("bot"); err != nil {
			return out, err
		}
		if res.Spell, err = rec.requireUint32("spell"); err != nil {
			return out, err
		}
		if res.Result, err = rec.requireText("result"); err != nil {
			return out, err
		}
		switch res.Result {
		case "learned", "known", "unknown", "invalid", "not-learned":
		default:
			return out, fmt.Errorf("%w: learn result=%q is none of learned known unknown invalid not-learned", ErrBadReport, rec.values["result"])
		}
		if res.Name, _, err = rec.text("name"); err != nil {
			return out, err
		}
		if res.Extra, err = rec.extra(learnRecordKeys); err != nil {
			return out, err
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
}

// Learn sends `.tb learn [<character>] <spell id>...` (at most 40 ids): the
// target is a host bot or the sender's own character; another real player's
// character is refused with `error code=not-a-bot cmd=learn`. An empty target
// names the sender's own character (the no-name form: the first word is the
// first spell id).
func (c *Client) Learn(ctx context.Context, target string, spells ...uint32) (LearnReport, error) {
	if len(spells) == 0 {
		return LearnReport{}, fmt.Errorf("tbcmd: learn needs at least one spell id")
	}
	if len(spells) > 40 {
		return LearnReport{}, fmt.Errorf("tbcmd: learn takes at most 40 spell ids (got %d)", len(spells))
	}
	words := make([]string, 0, len(spells)+1)
	if target != "" {
		name, err := setupName("learn", target)
		if err != nil {
			return LearnReport{}, err
		}
		words = append(words, name)
	}
	for _, id := range spells {
		if id == 0 {
			return LearnReport{}, fmt.Errorf("tbcmd: learn spell id 0 is not an id")
		}
		words = append(words, strconv.FormatUint(uint64(id), 10))
	}
	line := strings.Join(append([]string{".tb", "learn"}, words...), " ")
	rep, err := c.call(ctx, line, "learn-end")
	if err != nil {
		return LearnReport{}, err
	}
	out, err := parseLearn(rep)
	if err != nil {
		return LearnReport{}, err
	}
	if out.Bot != "" && target != "" && !strings.EqualFold(out.Bot, target) {
		return out, fmt.Errorf("%w: learn answered for %q, asked for %q", ErrBadReport, out.Bot, target)
	}
	return out, nil
}

// ---- equip ----

// EquipReport is a whole equip answer: ONE record `equip bot=.. item=..
// result=<equipped|refused> ...` and the end line's bot.
type EquipReport struct {
	Bot      string
	Item     uint32
	Result   string // equipped, refused
	Slot     int
	HasSlot  bool
	Why      string
	Code     int
	HasCode  bool
	Replaced bool
	Note     string
	Raw      []string
}

var equipRecordKeys = map[string]bool{"bot": true, "item": true, "name": true, "result": true,
	"slot": true, "why": true, "code": true, "replaced": true, "note": true}

// parseEquip reads an equip reply: exactly one record between the markers
// (the module writes one line per command).
func parseEquip(rep *command.Reply) (EquipReport, error) {
	r, err := parseReport(rep, "equip-begin", "equip-end", "equip", "bot")
	if err != nil {
		return EquipReport{}, err
	}
	if len(r.Records) != 1 {
		return EquipReport{}, fmt.Errorf("%w: equip carries %d records, want 1", ErrBadReport, len(r.Records))
	}
	end := r.End
	out := EquipReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Bot, err = end.requireText("bot"); err != nil {
		return out, err
	}
	rec := r.Records[0]
	if bot, err := rec.requireText("bot"); err != nil {
		return out, err
	} else if !strings.EqualFold(bot, out.Bot) {
		return out, fmt.Errorf("%w: equip record for %q, end for %q", ErrBadReport, bot, out.Bot)
	} else {
		out.Bot = bot
	}
	if out.Item, err = rec.requireUint32("item"); err != nil {
		return out, err
	}
	var result string
	if result, err = rec.requireText("result"); err != nil {
		return out, err
	}
	switch result {
	case "equipped", "refused":
		out.Result = result
	default:
		return out, fmt.Errorf("%w: equip result=%q is none of equipped refused", ErrBadReport, rec.values["result"])
	}
	if out.Slot, out.HasSlot, err = rec.optionalInt("slot", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Why, _, err = rec.text("why"); err != nil {
		return out, err
	}
	if out.Code, out.HasCode, err = rec.optionalInt("code", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if _, ok := rec.rawValue("replaced"); ok {
		rep2, err := rec.requireBool("replaced")
		if err != nil {
			return out, err
		}
		out.Replaced = rep2
	}
	if out.Note, _, err = rec.text("note"); err != nil {
		return out, err
	}
	if _, err = rec.extra(equipRecordKeys); err != nil {
		return out, err
	}
	return out, nil
}

// Equip sends `.tb equip [<character>] <item id> [replace]`: the target is
// a host bot or the sender's own character; an empty target is the no-name
// form (the first word is the item id) for the sender's own character. The
// module answers exactly one record; a refused slot without replace is a
// *CommandError for the caller, never a silent pass.
func (c *Client) Equip(ctx context.Context, target string, item uint32, replace bool) (EquipReport, error) {
	if item == 0 {
		return EquipReport{}, fmt.Errorf("tbcmd: equip item id 0 is not an id")
	}
	words := make([]string, 0, 3)
	if target != "" {
		name, err := setupName("equip", target)
		if err != nil {
			return EquipReport{}, err
		}
		words = append(words, name)
	}
	words = append(words, strconv.FormatUint(uint64(item), 10))
	if replace {
		words = append(words, "replace")
	}
	line := strings.Join(append([]string{".tb", "equip"}, words...), " ")
	rep, err := c.call(ctx, line, "equip-end")
	if err != nil {
		return EquipReport{}, err
	}
	out, err := parseEquip(rep)
	if err != nil {
		return EquipReport{}, err
	}
	if target != "" && !strings.EqualFold(out.Bot, target) {
		return out, fmt.Errorf("%w: equip answered for %q, asked for %q", ErrBadReport, out.Bot, target)
	}
	return out, nil
}

// ---- itemscan ----

// ItemScanResult is one item line of an itemscan answer.
type ItemScanResult struct {
	Bot      string
	Item     uint32
	Exists   bool
	Name     string
	Class    string // "<c>:<sc>", only when the item exists
	Inv      int
	HasInv   bool
	Req      int
	HasReq   bool
	Quality  int
	HasQual  bool
	Ilvl     int
	HasIlvl  bool
	CanEquip bool
	HasCan   bool
	Slot     int
	HasSlot  bool
	Why      string
	Extra    map[string]string
}

var itemScanRecordKeys = map[string]bool{"bot": true, "item": true, "exists": true, "name": true,
	"class": true, "inv": true, "req": true, "quality": true, "ilvl": true,
	"canequip": true, "slot": true, "why": true}

// ItemScanReport is a whole itemscan answer: one record per id plus the end
// line's exist/canequip tallies.
type ItemScanReport struct {
	Results  []ItemScanResult
	Bot      string
	Exist    int
	CanEquip int
	Raw      []string
}

// parseItemScan reads an itemscan reply: one record per id (a missing item
// answers exists=no with the check keys absent), end tallies exist/canequip.
func parseItemScan(rep *command.Reply) (ItemScanReport, error) {
	r, err := parseReport(rep, "itemscan-begin", "itemscan-end", "itemscan", "item")
	if err != nil {
		return ItemScanReport{}, err
	}
	end := r.End
	out := ItemScanReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Bot, err = end.requireText("bot"); err != nil {
		return out, err
	}
	if out.Exist, err = end.requireInt("exist", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.CanEquip, err = end.requireInt("canequip", 0, math.MaxInt32); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var res ItemScanResult
		if res.Bot, err = rec.requireText("bot"); err != nil {
			return out, err
		}
		if !strings.EqualFold(res.Bot, out.Bot) {
			return out, fmt.Errorf("%w: itemscan record for %q, end for %q", ErrBadReport, res.Bot, out.Bot)
		}
		if res.Item, err = rec.requireUint32("item"); err != nil {
			return out, err
		}
		if res.Exists, err = rec.requireBool("exists"); err != nil {
			return out, err
		}
		if res.Exists {
			if res.Name, _, err = rec.text("name"); err != nil {
				return out, err
			}
			cls, err := rec.requireText("class")
			if err != nil {
				return out, err
			}
			res.Class = cls
			if res.Inv, res.HasInv, err = rec.optionalInt("inv", 0, math.MaxInt32); err != nil {
				return out, err
			}
			if res.Req, res.HasReq, err = rec.optionalInt("req", 0, math.MaxInt32); err != nil {
				return out, err
			}
			if res.Quality, res.HasQual, err = rec.optionalInt("quality", 0, math.MaxInt32); err != nil {
				return out, err
			}
			if res.Ilvl, res.HasIlvl, err = rec.optionalInt("ilvl", 0, math.MaxInt32); err != nil {
				return out, err
			}
			if res.CanEquip, res.HasCan, err = rec.optionalBool("canequip"); err != nil {
				return out, err
			}
			if !res.HasCan {
				return out, fmt.Errorf("%w: itemscan for %d exists but has no canequip=", ErrBadReport, res.Item)
			}
			if res.CanEquip {
				if res.Slot, res.HasSlot, err = rec.optionalInt("slot", 0, math.MaxInt32); err != nil {
					return out, err
				}
				if !res.HasSlot {
					return out, fmt.Errorf("%w: itemscan for %d canequip but has no slot=", ErrBadReport, res.Item)
				}
			}
			if res.Why, _, err = rec.text("why"); err != nil {
				return out, err
			}
		}
		if res.Extra, err = rec.extra(itemScanRecordKeys); err != nil {
			return out, err
		}
		out.Results = append(out.Results, res)
	}
	gotExist, gotCan := 0, 0
	for _, res := range out.Results {
		if res.Exists {
			gotExist++
		}
		if res.CanEquip {
			gotCan++
		}
	}
	if gotExist != out.Exist {
		return out, fmt.Errorf("%w: itemscan-end exist=%d, %d record(s) exist", ErrBadReport, out.Exist, gotExist)
	}
	if gotCan != out.CanEquip {
		return out, fmt.Errorf("%w: itemscan-end canequip=%d, %d record(s) canequip", ErrBadReport, out.CanEquip, gotCan)
	}
	return out, nil
}

// ItemScan sends `.tb itemscan <bot> <item id>...`: bot-only (the sender's
// own character is NOT accepted here), read-only, at most 40 ids; nothing is
// created or changed. The end tallies are checked against the records.
func (c *Client) ItemScan(ctx context.Context, bot string, items ...uint32) (ItemScanReport, error) {
	name, err := setupName("itemscan", bot)
	if err != nil {
		return ItemScanReport{}, err
	}
	if len(items) == 0 {
		return ItemScanReport{}, fmt.Errorf("tbcmd: itemscan needs at least one item id")
	}
	if len(items) > 40 {
		return ItemScanReport{}, fmt.Errorf("tbcmd: itemscan takes at most 40 item ids (got %d)", len(items))
	}
	words := make([]string, 0, len(items)+1)
	words = append(words, name)
	for _, id := range items {
		if id == 0 {
			return ItemScanReport{}, fmt.Errorf("tbcmd: itemscan item id 0 is not an id")
		}
		words = append(words, strconv.FormatUint(uint64(id), 10))
	}
	line := strings.Join(append([]string{".tb", "itemscan"}, words...), " ")
	rep, err := c.call(ctx, line, "itemscan-end")
	if err != nil {
		return ItemScanReport{}, err
	}
	return parseItemScan(rep)
}

// ---- kill and gm ----

// KillResult is one bot's line of a kill answer.
type KillResult struct {
	Bot    string
	Status string // killed, refused
	Code   string // refusal code, only with refused
	Msg    string
	Extra  map[string]string
}

var killRecordKeys = map[string]bool{"bot": true, "tag": true, "status": true, "code": true, "msg": true}

// KillReport is a whole kill answer.
type KillReport struct {
	Results []KillResult
	Target  string
	Bots    int
	Killed  int
	Refused int
	Raw     []string
}

// parseKill reads a kill reply: kill-begin, one kill record per addressed
// character, kill-end with the target/bots/killed/refused tally. A refused
// record stays listed (the tally counts it); the caller turns it into a
// *CommandError so a refusal never passes as a kill.
func parseKill(rep *command.Reply) (KillReport, error) {
	r, err := parseReport(rep, "kill-begin", "kill-end", "kill", "bot")
	if err != nil {
		return KillReport{}, err
	}
	end := r.End
	out := KillReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Target, err = labelText(end, "target"); err != nil {
		return out, err
	}
	if out.Bots, err = end.requireInt("bots", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Killed, err = end.requireInt("killed", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Refused, err = end.requireInt("refused", 0, math.MaxInt32); err != nil {
		return out, err
	}
	for _, rec := range r.Records {
		var res KillResult
		if res.Bot, err = rec.requireText("bot"); err != nil {
			return out, err
		}
		tag, err := rec.requireText("tag")
		if err != nil {
			return out, err
		}
		if tag != "SETUP" {
			return out, fmt.Errorf("%w: kill tag=%q is not SETUP", ErrBadReport, rec.values["tag"])
		}
		if res.Status, err = requiredWord(rec, "status", "killed", "refused"); err != nil {
			return out, err
		}
		if res.Status == "refused" {
			if res.Code, err = rec.requireText("code"); err != nil {
				return out, err
			}
			if res.Msg, _, err = rec.text("msg"); err != nil {
				return out, err
			}
		}
		if res.Extra, err = rec.extra(killRecordKeys); err != nil {
			return out, err
		}
		out.Results = append(out.Results, res)
	}
	if len(out.Results) != out.Bots {
		return out, fmt.Errorf("%w: kill-end bots=%d, %d record(s)", ErrBadReport, out.Bots, len(out.Results))
	}
	k, f := 0, 0
	for _, res := range out.Results {
		if res.Status == "killed" {
			k++
		} else {
			f++
		}
	}
	if k != out.Killed || f != out.Refused {
		return out, fmt.Errorf("%w: kill-end killed=%d refused=%d, %d killed %d refused line(s)",
			ErrBadReport, out.Killed, out.Refused, k, f)
	}
	return out, nil
}

// Kill sends `.tb kill <bot|party [leader]>`: records carry tag=SETUP, the
// module refuses a character that may not be killed. A refused record is the
// module's refusal, returned as a *CommandError.
func (c *Client) Kill(ctx context.Context, target string) (KillReport, error) {
	words, err := targetWords(target)
	if err != nil {
		return KillReport{}, err
	}
	line := strings.Join(append([]string{".tb", "kill"}, words...), " ")
	rep, err := c.call(ctx, line, "kill-end")
	if err != nil {
		return KillReport{}, err
	}
	out, err := parseKill(rep)
	if err != nil {
		return KillReport{}, err
	}
	if bad := killRefusal(out); bad != nil {
		return out, bad
	}
	return out, nil
}

// RoleSelf sets the sender's own character's role (the no-name form:
// `.tb role tank`, `.tb role healer`, ...). Role's targetWords refuses an
// empty target, and clear is not a role word the module takes here (use
// Role with a name for the party form).
func (c *Client) RoleSelf(ctx context.Context, role string) (RoleReport, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "tank", "healer", "dps", "none", "clear", "heal":
	default:
		return RoleReport{}, fmt.Errorf("tbcmd: role %q is not tank, healer, dps or none", role)
	}
	if role == "heal" {
		role = "healer"
	}
	line := ".tb role " + role
	rep, err := c.call(ctx, line, "role-end")
	if err != nil {
		return RoleReport{}, err
	}
	return parseRole(rep)
}

// killRefusal turns a refused kill record into the module's named error.
func killRefusal(rep KillReport) error {
	for _, res := range rep.Results {
		if res.Status == "refused" {
			return &CommandError{Code: res.Code, Cmd: "kill", HasMsg: res.Msg != "", Msg: res.Msg}
		}
	}
	return nil
}

// GMReport is a whole gm answer: one record and the end line's gm flag.
type GMReport struct {
	Bot    string
	Status string // ok, refused
	GM     bool
	Raw    []string
}

var gmRecordKeys = map[string]bool{"bot": true, "tag": true, "status": true, "gm": true}

// parseGM reads a gm reply: gm-begin, ONE gm record, gm-end target/gm.
func parseGM(rep *command.Reply) (GMReport, error) {
	r, err := parseReport(rep, "gm-begin", "gm-end", "gm", "bot")
	if err != nil {
		return GMReport{}, err
	}
	if len(r.Records) != 1 {
		return GMReport{}, fmt.Errorf("%w: gm carries %d records, want 1", ErrBadReport, len(r.Records))
	}
	end := r.End
	out := GMReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Bot, err = end.requireText("target"); err != nil {
		return out, err
	}
	rec := r.Records[0]
	var bot string
	if bot, err = rec.requireText("bot"); err != nil {
		return out, err
	}
	if !strings.EqualFold(bot, out.Bot) {
		return out, fmt.Errorf("%w: gm record for %q, end for %q", ErrBadReport, bot, out.Bot)
	}
	out.Bot = bot
	tag, err := rec.requireText("tag")
	if err != nil {
		return out, err
	}
	if tag != "SETUP" {
		return out, fmt.Errorf("%w: gm tag=%q is not SETUP", ErrBadReport, rec.values["tag"])
	}
	if out.Status, err = requiredWord(rec, "status", "ok", "refused"); err != nil {
		return out, err
	}
	var gm bool
	if gm, err = rec.requireBool("gm"); err != nil {
		return out, err
	}
	out.GM = gm
	endGM, err := end.requireBool("gm")
	if err != nil {
		return out, err
	}
	if endGM != gm {
		return out, fmt.Errorf("%w: gm-end gm=%v, record gm=%v", ErrBadReport, endGM, gm)
	}
	if _, err = rec.extra(gmRecordKeys); err != nil {
		return out, err
	}
	return out, nil
}

// GM sends `.tb gm <character> on|off` (`Player::SetGameMaster`): the target
// is a host bot or the sender's own character. A refused record is the
// module's refusal, returned as a *CommandError.
func (c *Client) GameMaster(ctx context.Context, target string, on bool) (GMReport, error) {
	name, err := setupName("gm", target)
	if err != nil {
		return GMReport{}, err
	}
	word := "off"
	if on {
		word = "on"
	}
	line := ".tb gm " + name + " " + word
	rep, err := c.call(ctx, line, "gm-end")
	if err != nil {
		return GMReport{}, err
	}
	out, err := parseGM(rep)
	if err != nil {
		return GMReport{}, err
	}
	if !strings.EqualFold(out.Bot, target) {
		return out, fmt.Errorf("%w: gm answered for %q, asked for %q", ErrBadReport, out.Bot, target)
	}
	if out.Status == "refused" {
		return out, &CommandError{Code: "refused", Cmd: "gm", HasMsg: false}
	}
	return out, nil
}
