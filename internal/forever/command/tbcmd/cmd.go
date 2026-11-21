package tbcmd

// `.tb cmd`: the module's one order path (commander packages P3 and P6). One call carries the whole order for the
// caller's bound party, and the answer is one grammar-v2 report, not one line per bot:
//
//   - an order verb (P3: move, walk, pull, pullback, attack, focus, rest, stay, follow, hold, revive) answers
//     `cmd` with `verb=`, `status=accepted`, `bots=<count>` and `result=<scope> <count>`, where the scope is
//     `party` for a party-wide order and `bot:<name>` for one the lead's bot executes; the end line adds `lead=`
//     and `bots=` (TB-COMMANDS section 14).
//   - a task verb (P6: loot, open, use, gossip) answers `cmd` with `verb=`, `bot=`, `status=accepted` and
//     `target=<32-hex guid>`.
//   - lootmethod answers `cmd` with `verb=`, `status=ok`, `method=` and `threshold=`.
//   - state answers `task` records (`bot=`, `busy=`, `status=none|running|done|failed`, optional `verb=`,
//     `entry=`, `text=`) and nothing else.
//   - a refusal is the module's single `error code=<code> cmd=cmd [msg=]` line, as for every typed command.
//
// The report is the slice-3 OrderReport so a caller does not branch on the path: `accepted` is the cmd path's word
// for a queued order, and the bots the answer accounts for are `queued` results. The cmd answer carries no
// per-bot sequence, so a caller cannot match `ord=<seq>` for a cmd order; StateReport's `ord=` still shows the
// module's sequence after the fact.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// cmdStateMaxLines is the line cap of a `state` call: the begin and end markers and one task record per bot of a
// raid-sized party, with room to spare.
const cmdStateMaxLines = 256

// cmdMaxWord is the longest argument word the cmd grammar takes (the controller's table uses the same cap).
const cmdMaxWord = 96

// Cmd sends one `.tb cmd <verb> [args...]` line and decodes its answer into the slice-3 report shape. A target
// GUID the agent's own model wrote as `0x<32 hex>` is sent as the bare 32 hex digits the module's grammar takes.
//
// The answer is bound to the verb sent: a reply whose verb is another one is a bad report, as is a reply that
// holds no record for an order verb. A record the module marks `refused` is a CmdRefusedError, and a status word
// the model does not know is a bad report.
func (c *Client) Cmd(ctx context.Context, verb string, args ...string) (OrderReport, error) {
	line, err := CmdLine(verb, args)
	if err != nil {
		return OrderReport{}, err
	}
	// A state answer is one record per bot in the party (a raid is 40), so it gets the collector's full cap; every
	// other answer is one record.
	max := 0
	if strings.EqualFold(verb, "state") {
		max = cmdStateMaxLines
	}
	rep, err := c.callMax(ctx, line, "cmd-end", max)
	if err != nil {
		return OrderReport{}, err
	}
	return parseCmd(rep, verb)
}

// CmdRefusedError is a `.tb cmd` answer whose record says `status=refused`: the module took the line but did not
// queue it. Reason is the module's own words (its `result=`, `msg=` or `code=`), empty when it gave none.
type CmdRefusedError struct {
	Verb   string
	Reason string
}

func (e *CmdRefusedError) Error() string {
	msg := "tb: cmd " + e.Verb + " refused"
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// CmdLine builds the line the wire carries: the verb, then the argument words. The grammar the module takes is
// letters, digits and `_ . : -`, plus `@` and `,` for an object target's `entry:<id>@x,y,z`; a `#` (the token
// marker), a space and `=` never reach the wire. A `0x`-prefixed 32-hex GUID loses its prefix here, so the line a
// caller shows is the line the module saw (the run's token is appended after it).
func CmdLine(verb string, args []string) (string, error) {
	v, err := cmdWord("verb", verb)
	if err != nil {
		return "", err
	}
	parts := []string{".tb", "cmd", v}
	for _, a := range args {
		w, err := cmdWord("argument", a)
		if err != nil {
			return "", err
		}
		parts = append(parts, w)
	}
	return strings.Join(parts, " "), nil
}

// cmdWord checks one word and returns it as the wire takes it: a `0x`-prefixed 32-hex GUID loses the prefix (the
// module's own guid grammar is the bare digits; the agent's model writes the `0x` form).
func cmdWord(what, s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("tbcmd: %s is empty", what)
	}
	if len(s) > cmdMaxWord {
		return "", fmt.Errorf("tbcmd: %s is over %d bytes", what, cmdMaxWord)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_.:-@,", r):
		default:
			return "", fmt.Errorf("tbcmd: %s %q has an unexpected character %q", what, s, r)
		}
	}
	return bareGuid(s), nil
}

// bareGuid drops the `0x`/`0X` of a 32-hex GUID: `0x0000000000000000000000000000BEEF` is sent as
// `0000000000000000000000000000BEEF`. Every other word is returned unchanged, so no other grammar word can be
// mistaken for a guid.
func bareGuid(s string) string {
	if len(s) != 2+32 || (s[:2] != "0x" && s[:2] != "0X") {
		return s
	}
	for i := 2; i < len(s); i++ {
		if !isUpperHex(s[i]) && !(s[i] >= 'a' && s[i] <= 'f') {
			return s
		}
	}
	return s[2:]
}

// parseCmd decodes a `.tb cmd` report for the verb that was sent. The sent verb decides the data kind: a `state`
// answer is the only one with `task` records, every other verb answers exactly one `cmd` record. A reply for
// another verb is a bad report.
func parseCmd(rep *command.Reply, sent string) (OrderReport, error) {
	sent = strings.ToLower(sent)
	kind, identity := "cmd", "verb"
	if sent == "state" {
		kind, identity = "task", "bot"
	}
	r, err := parseReport(rep, "cmd-begin", "cmd-end", kind, identity)
	if err != nil {
		return OrderReport{}, err
	}
	out := OrderReport{Raw: append([]string(nil), rep.Raw...)}
	if out.Verb, err = r.End.requireText("verb"); err != nil {
		return out, err
	}
	if out.Verb != sent {
		return out, fmt.Errorf("%w: sent %q, the answer is for %q", ErrBadReport, sent, out.Verb)
	}
	if out.Count, err = r.End.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Lead, _, err = r.End.text("lead"); err != nil {
		return out, err
	}
	endBots, hasEndBots, err := r.End.optionalInt("bots", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	endOK, hasOK, err := r.End.optionalInt("ok", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	if out.Refused, _, err = r.End.optionalInt("refused", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if kind == "task" {
		err = parseCmdTasks(r.Records, &out)
	} else {
		if len(r.Records) != 1 {
			return out, fmt.Errorf("%w: a %s answer holds %d cmd record(s), not one", ErrBadReport, sent, len(r.Records))
		}
		var recBots int
		var hasRecBots bool
		recBots, hasRecBots, err = parseCmdRecords(r.Records, &out)
		if err == nil && hasRecBots && hasEndBots && recBots != endBots {
			err = fmt.Errorf("%w: cmd bots=%d, cmd-end bots=%d", ErrBadReport, recBots, endBots)
		}
	}
	if err != nil {
		return out, err
	}
	// Every record the model accepts is a queued one, so the end line's own tally must say the same: the module
	// writes ok=<records> refused=0 on the task path and nothing on the order path.
	if out.Refused > 0 {
		return out, fmt.Errorf("%w: cmd-end refused=%d, but the answer's record is not refused", ErrBadReport, out.Refused)
	}
	if hasOK && endOK != len(r.Records) {
		return out, fmt.Errorf("%w: cmd-end ok=%d, the answer holds %d record(s)", ErrBadReport, endOK, len(r.Records))
	}
	if hasEndBots {
		out.Bots = endBots
	} else {
		out.Bots = len(out.Results)
	}
	out.Queued = out.Bots - out.Refused
	return out, nil
}

// cmdStatusWords are the record statuses the module writes for a taken order: `accepted` for the order and task
// verbs, `ok` for lootmethod.
var cmdStatusWords = []string{"accepted", "ok"}

// parseCmdRecords reads the single `cmd` record of an order, task or lootmethod answer and returns the record's
// own bots= count when it has one.
func parseCmdRecords(recs []record, out *OrderReport) (int, bool, error) {
	known := map[string]bool{
		"verb": true, "status": true, "bots": true, "result": true, "bot": true, "target": true,
	}
	recBots, hasRecBots := 0, false
	for _, rec := range recs {
		verb, err := rec.requireText("verb")
		if err != nil {
			return 0, false, err
		}
		if verb != out.Verb {
			return 0, false, fmt.Errorf("%w: cmd verb=%q, cmd-end verb=%q", ErrBadReport, verb, out.Verb)
		}
		status, err := rec.requireText("status")
		if err != nil {
			return 0, false, err
		}
		if status == "refused" {
			reason, _, _ := rec.text("result")
			if reason == "" {
				reason, _, _ = rec.text("msg")
			}
			return 0, false, &CmdRefusedError{Verb: verb, Reason: reason}
		}
		if status, err = requiredWord(rec, "status", cmdStatusWords...); err != nil {
			return 0, false, err
		}
		out.Status = status
		if recBots, hasRecBots, err = rec.optionalInt("bots", 0, math.MaxInt32); err != nil {
			return 0, false, err
		}
		if out.Result, _, err = rec.text("result"); err != nil {
			return 0, false, err
		}
		fields, err := rec.extra(known)
		if err != nil {
			return 0, false, err
		}
		bot, hasBot, err := rec.text("bot")
		if err != nil {
			return 0, false, err
		}
		target, hasTarget, err := rec.text("target")
		if err != nil {
			return 0, false, err
		}
		switch {
		case hasBot:
			res := OrderResult{Bot: bot, Verb: verb, Status: cmdQueued(out.Status), Extra: fields}
			if hasTarget {
				res.Target, res.HasTarget = target, true
			}
			out.Results = append(out.Results, res)
		case len(fields) > 0:
			// A whole-order record's keys this model does not type (`method=`, `threshold=` of lootmethod).
			out.Fields = fields
		}
	}
	// An order the lead's bot executes names it in `result=bot:<name> <count>`; the record itself has no bot key,
	// so the typed result comes from the scope.
	if len(out.Results) == 0 && out.Status != "" {
		if name, ok := botScope(out.Result); ok {
			out.Results = append(out.Results, OrderResult{Bot: name, Verb: out.Verb, Status: cmdQueued(out.Status)})
		}
	}
	return recBots, hasRecBots, nil
}

// parseCmdTasks reads a `state` answer's per-bot task records. An `entry=` of 0 is accepted: a task on a target
// with no creature or object entry writes none, and a zero must not refuse the whole answer.
func parseCmdTasks(recs []record, out *OrderReport) error {
	known := map[string]bool{
		"bot": true, "busy": true, "status": true, "verb": true, "entry": true, "text": true,
	}
	for _, rec := range recs {
		bot, err := rec.requireText("bot")
		if err != nil {
			return err
		}
		busy, err := rec.requireBool("busy")
		if err != nil {
			return err
		}
		status, err := requiredWord(rec, "status", "none", "running", "done", "failed")
		if err != nil {
			return err
		}
		res := OrderResult{Bot: bot, Status: status}
		if res.Verb, _, err = rec.text("verb"); err != nil {
			return err
		}
		if res.Msg, res.HasMsg, err = rec.text("text"); err != nil {
			return err
		}
		extra, err := rec.extra(known)
		if err != nil {
			return err
		}
		if extra == nil {
			extra = make(map[string]string, 2)
		}
		extra["busy"] = "no"
		if busy {
			extra["busy"] = "yes"
		}
		if n, has, err := rec.optionalInt("entry", 0, math.MaxInt32); err != nil {
			return err
		} else if has {
			extra["entry"] = strconv.Itoa(n)
		}
		res.Extra = extra
		out.Results = append(out.Results, res)
	}
	return nil
}

// cmdQueued is the per-bot word the slice-3 path uses for an order the module took. The cmd path says `accepted`
// for the whole call; a caller's report does not depend on which path the verb's row picked.
func cmdQueued(status string) string {
	if status == "accepted" {
		return "queued"
	}
	return status
}

// botScope reads an order answer's result scope: `bot:<name> <count>` names the bot that executes it; `party
// <count>` and an empty result name none.
func botScope(result string) (string, bool) {
	name, ok := strings.CutPrefix(result, "bot:")
	if !ok {
		return "", false
	}
	if i := strings.LastIndexByte(name, ' '); i >= 0 {
		name = name[:i]
	}
	return name, name != ""
}
