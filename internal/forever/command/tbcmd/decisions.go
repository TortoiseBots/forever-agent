package tbcmd

// The decisions read: a bounded, paged answer of a run's decision rows. A module records one row per decision of the
// run it leads; this read names a run (by its leader's name or by the run id the module printed) and returns the rows
// oldest first, in pages the command sizes itself.
//
// The answer is a run-begin report: one `decisions` header record, one `decision` record per row, then run-end. A row
// is one line and is never split, so the decode is strict the way the rest of this layer is: a missing key, a word
// outside the documented set, a repeated row and a count that disagrees are ErrBadReport or ErrBadValue, never a zero.
// Keys this layer does not type are kept in Extra. The module's own refusals are DecisionsRefusal; a module build
// without the command is ErrNotSupported.
//
// Paging: `since=` asks for the rows after that sequence, `next=` is the cursor for the next call and `more=yes` says
// a newer row waits. The pull below follows the cursor with its own bound, asks never more than the command's own
// limit allows, and stops when the module says no more, when a page brings no row, or at its page bound.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

const (
	// decisionsPageMax is the command's own clamp on one page (a larger limit= is cut to this by the module).
	decisionsPageMax = 200
	// decisionsPageDefault is the limit the pull asks for when the caller states none: the largest page the command
	// allows, so the fewest calls.
	decisionsPageDefault = decisionsPageMax
	// decisionsMaxPages bounds one pull: a module that always says more cannot keep the run's end waiting.
	decisionsMaxPages = 25
)

// ErrNotSupported is a module build that has no run decisions command: the word it does not know comes back as
// unknown-verb (or one of the other "not there" codes), and nothing was read.
var ErrNotSupported = errors.New("not supported by this module build")

// DecisionsRefusal is the module's refusal of a decisions read, with the code it named. The codes are the command's
// own: capture-off (the decision capture is off), no-capture (a player-led or stopped run), no-run (neither a leader
// nor a run id), bad-argument and unexpected-argument.
type DecisionsRefusal struct {
	Code string
	Msg  string
}

func (e *DecisionsRefusal) Error() string {
	if e.Msg != "" {
		return "tbcmd: the module refused the decisions read: " + e.Code + ": " + e.Msg
	}
	return "tbcmd: the module refused the decisions read: " + e.Code
}

// DecisionRow is one `decision` record: the fixed key order is the interface, so the typed fields are the documented
// setters and Extra keeps every key this layer does not know.
type DecisionRow struct {
	// Seq is the row's cursor; it rises with every new decision and is the since= of the next page.
	Seq int
	// T is the module's clock at the decision, in unix milliseconds, and DT the time since the previous one.
	T  int64
	DT int64
	// State is the run's state word (running, paused, done), Phase the plan phase and Step the plan step.
	State string
	Phase string
	Step  int
	// Record says what the module did with the record the row describes: tick (written), stub (a cut record) or
	// dropped (the sink refused the line).
	Record string
	// Order, Note and Say are the decision's own texts.
	Order string
	Note  string
	Say   string
	// Trunc says the module cut a free text of the row at its stored bound; the row is still one whole line.
	Trunc bool
	// Extra is every key this layer does not type, decoded.
	Extra map[string]string
}

// JSON returns the row as one JSON object, the form the run's record writes: the documented keys with their own
// types and every unknown key as the text it came as.
func (r DecisionRow) JSON() ([]byte, error) {
	m := make(map[string]any, 12+len(r.Extra))
	m["seq"] = r.Seq
	m["t"] = r.T
	m["dt"] = r.DT
	m["state"] = r.State
	m["phase"] = r.Phase
	m["step"] = r.Step
	m["record"] = r.Record
	m["order"] = r.Order
	m["note"] = r.Note
	m["say"] = r.Say
	if r.Trunc {
		m["trunc"] = true
	}
	for k, v := range r.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// DecisionsHeader is the answer's one header record: which run the rows belong to and where the page sits.
type DecisionsHeader struct {
	Run     string
	Lead    string
	Plan    string
	Capture string
	// Count is the rows the page carries, Since the cursor the call asked for, Next the cursor of the next call and
	// More that a newer row waits.
	Count int
	Since int
	Next  int
	More  bool
	Extra map[string]string
}

// DecisionsPage is one whole answer: the header, its rows and the end line's keys.
type DecisionsPage struct {
	Header DecisionsHeader
	Rows   []DecisionRow
	End    map[string]string
}

// DecisionsPull is a pull's result: every row it read, the run and capture the header named, the pages it took and
// why it stopped short of the module's end (if it did).
type DecisionsPull struct {
	Run     string
	Capture string
	Rows    []DecisionRow
	Pages   int
	// Truncated says the pull stopped at its own page bound while the module still held newer rows.
	Truncated bool
	// Note says why the pull stopped early (a page with no row, or the page bound); empty when it read to the end.
	Note string
}

// RunDecisions reads one page of a run's decision rows: `.tb run decisions <leader|run> [since=<seq>] [limit=<n>]`.
// limit is clamped to the command's own 1..200 (0 or less takes the default page); the caller's target is a leader
// name or a run id, a single word the module's grammar can carry.
func (c *Client) RunDecisions(ctx context.Context, target string, since int, hasSince bool, limit int) (DecisionsPage, error) {
	word, err := runTarget(target)
	if err != nil {
		return DecisionsPage{}, err
	}
	if hasSince && (since < 0 || since > math.MaxInt32) {
		return DecisionsPage{}, fmt.Errorf("%w: since %d is not a decision sequence", ErrBadValue, since)
	}
	if limit <= 0 {
		limit = decisionsPageDefault
	} else if limit > decisionsPageMax {
		limit = decisionsPageMax
	}
	line := ".tb run decisions " + word
	if hasSince {
		line += fmt.Sprintf(" since=%d", since)
	}
	line += fmt.Sprintf(" limit=%d", limit)
	// One header, a page of at most 200 rows and the end line: the collector's own floor is 16 and the page is what
	// the limit bounds, so the cap follows the page.
	rep, err := c.callMax(ctx, line, "run-end", limit+8)
	if err != nil {
		return DecisionsPage{}, decisionsErr(err)
	}
	return parseDecisionsPage(rep)
}

// PullDecisions reads a run's decision rows to the module's end, one page at a time. limit is the page size it asks
// for (0 takes the default page, the command's largest); since/hasSince start it above an earlier cursor. The pull
// follows next=, asks never more than the command allows and stops on more=no, on a page that brought no row, or at
// decisionsMaxPages (Truncated then says so).
//
// Every page must agree with the protocol, or the pull is refused rather than written: a page asked for rows above a
// cursor repeats that cursor, next= is the newest row read (and the cursor asked for when no row came back), a later
// page starts above the cursor, and the run, capture, leader and plan are the ones the first page named. A page that breaks one of
// these would duplicate, drop or mix rows in a file that otherwise looks whole.
func (c *Client) PullDecisions(ctx context.Context, target string, since int, hasSince bool, limit int) (DecisionsPull, error) {
	pull := DecisionsPull{}
	cursor, hasCursor := since, hasSince
	head := DecisionsHeader{}
	for page := 0; page < decisionsMaxPages; page++ {
		rep, err := c.RunDecisions(ctx, target, cursor, hasCursor, limit)
		if err != nil {
			return pull, err
		}
		// A page that was asked for rows above a cursor repeats that cursor; the first page of a pull that asked for
		// none has no cursor to compare (the module answers its own window).
		if hasCursor && rep.Header.Since != cursor {
			return pull, fmt.Errorf("%w: the page says since=%d, the pull asked since=%d", ErrBadReport, rep.Header.Since, cursor)
		}
		if page == 0 {
			head = rep.Header
		} else if rep.Header.Run != head.Run || rep.Header.Capture != head.Capture ||
			rep.Header.Lead != head.Lead || rep.Header.Plan != head.Plan {
			return pull, fmt.Errorf("%w: the page names run=%q lead=%q plan=%q capture=%q, not %q/%q/%q/%q",
				ErrBadReport, rep.Header.Run, rep.Header.Lead, rep.Header.Plan, rep.Header.Capture,
				head.Run, head.Lead, head.Plan, head.Capture)
		}
		if len(rep.Rows) == 0 {
			if rep.Header.Next != cursor {
				return pull, fmt.Errorf("%w: a page with no row moved next to %d from since=%d", ErrBadReport, rep.Header.Next, cursor)
			}
		} else {
			first, last := rep.Rows[0].Seq, rep.Rows[len(rep.Rows)-1].Seq
			if hasCursor && first <= cursor {
				return pull, fmt.Errorf("%w: the page starts at seq=%d, not above since=%d", ErrBadReport, first, cursor)
			}
			if rep.Header.Next != last {
				return pull, fmt.Errorf("%w: the page ends at seq=%d but says next=%d", ErrBadReport, last, rep.Header.Next)
			}
		}
		if pull.Run == "" {
			pull.Run, pull.Capture = rep.Header.Run, rep.Header.Capture
		}
		pull.Pages++
		pull.Rows = append(pull.Rows, rep.Rows...)
		if !rep.Header.More {
			return pull, nil
		}
		if len(rep.Rows) == 0 {
			// more=yes with no row: the cursor cannot move, so another call would ask the same question. The rows the
			// module still counts are past its own window; the pull stops and says so.
			pull.Note = fmt.Sprintf("the module answered more=yes at seq %d but sent no row; the older rows are past its window", rep.Header.Next)
			return pull, nil
		}
		cursor, hasCursor = rep.Header.Next, true
	}
	pull.Truncated = true
	pull.Note = fmt.Sprintf("the pull stopped at its own bound of %d pages", decisionsMaxPages)
	return pull, nil
}

// decisionsErr maps the module's error line onto this read's vocabulary: the command's own refusals keep their code,
// the "not there" codes are ErrNotSupported and anything else is the module's error as it came.
func decisionsErr(err error) error {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case "capture-off", "no-capture", "no-run", "bad-argument", "unexpected-argument":
		return &DecisionsRefusal{Code: ce.Code, Msg: ce.Msg}
	case "unknown-verb", "unsupported", "invalid", "engine-not-built":
		return fmt.Errorf("%w: the module answered code=%s", ErrNotSupported, ce.Code)
	}
	return err
}

// runTarget checks the one word a decisions read names: a leader's name or a run id. It is the run-id shape (letters,
// digits, dot, underscore and dash, at most 64 bytes), so no space, '#' or '=' can reach the line, and it is passed
// as it came: a run id is case-sensitive and is not a character name.
func runTarget(target string) (string, error) {
	word := strings.TrimSpace(target)
	if word == "" {
		return "", fmt.Errorf("%w: no run or leader", ErrBadValue)
	}
	if len(word) > 64 {
		return "", fmt.Errorf("%w: %q is over 64 bytes", ErrBadValue, word)
	}
	for i := range word {
		ch := word[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case ch == '.' || ch == '_' || ch == '-':
		default:
			return "", fmt.Errorf("%w: %q carries %q; a run id or a leader is letters, digits, dot, underscore and dash", ErrBadValue, word, ch)
		}
	}
	return word, nil
}

// parseDecisionsPage reads one whole answer. The shape is fixed: run-begin, exactly one `decisions` header record,
// zero or more `decision` rows (one line each, never split), then run-end. The end line's counts must agree with the
// lines between the markers, and every row must be newer than the one before it and than the cursor asked for.
func parseDecisionsPage(rep *command.Reply) (DecisionsPage, error) {
	out := DecisionsPage{}
	if rep == nil {
		return out, fmt.Errorf("%w: no reply", ErrBadReport)
	}
	if !rep.Complete || len(rep.Lines) < 3 {
		return out, fmt.Errorf("%w: the reply ended without its run-end line", ErrBadReport)
	}
	if rep.Token == "" {
		return out, fmt.Errorf("%w: the run had no token", ErrBadReport)
	}
	for _, ln := range rep.Lines {
		if ln.Token != rep.Token {
			return out, fmt.Errorf("%w: a line carries token %q, the run's is %q", ErrBadReport, ln.Token, rep.Token)
		}
	}
	begin, err := decodeLine(rep.Lines[0])
	if err != nil {
		return out, err
	}
	if begin.kind != "run-begin" {
		return out, fmt.Errorf("%w: the reply starts with %q, not %q (a missing begin marker)", ErrBadReport, begin.kind, "run-begin")
	}
	if len(begin.values) > 0 {
		return out, fmt.Errorf("%w: run-begin carries a field", ErrBadReport)
	}
	head, err := decodeLine(rep.Lines[1])
	if err != nil {
		return out, err
	}
	if head.kind != "decisions" {
		return out, fmt.Errorf("%w: the first line inside the report is %q, not %q", ErrBadReport, head.kind, "decisions")
	}
	if out.Header, err = parseDecisionsHeader(head); err != nil {
		return out, err
	}
	end, err := decodeLine(rep.Lines[len(rep.Lines)-1])
	if err != nil {
		return out, err
	}
	if end.kind != "run-end" {
		return out, fmt.Errorf("%w: the reply ends with %q, not %q", ErrBadReport, end.kind, "run-end")
	}
	verb, err := end.requireText("verb")
	if err != nil {
		return out, err
	}
	if verb != "decisions" {
		return out, fmt.Errorf("%w: run-end verb=%q, the command was %q", ErrBadReport, verb, "decisions")
	}
	between := len(rep.Lines) - 2
	count, err := end.requireInt("count", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	if count != between {
		return out, fmt.Errorf("%w: run-end count=%d, %d line(s) between the markers", ErrBadReport, count, between)
	}
	records, err := end.requireInt("records", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	if records != between {
		return out, fmt.Errorf("%w: run-end records=%d, %d record line(s) between the markers", ErrBadReport, records, between)
	}
	if out.End, err = end.extra(endLineKeys); err != nil {
		return out, err
	}
	prev := -1
	for i := 2; i < len(rep.Lines)-1; i++ {
		rec, err := decodeLine(rep.Lines[i])
		if err != nil {
			return out, err
		}
		if rec.kind != "decision" {
			return out, fmt.Errorf("%w: a %q line inside a decisions reply", ErrBadReport, rec.kind)
		}
		row, err := parseDecisionRow(rec)
		if err != nil {
			return out, err
		}
		if row.Seq <= prev {
			return out, fmt.Errorf("%w: decision seq=%d follows seq=%d; the rows are oldest first", ErrBadReport, row.Seq, prev)
		}
		prev = row.Seq
		out.Rows = append(out.Rows, row)
	}
	if len(out.Rows) != out.Header.Count {
		return out, fmt.Errorf("%w: decisions count=%d, %d row(s) in the page", ErrBadReport, out.Header.Count, len(out.Rows))
	}
	return out, nil
}

// endLineKeys are the end line's own numeric keys; everything else is kept for the caller.
var endLineKeys = map[string]bool{"verb": true, "count": true, "records": true}

// parseDecisionsHeader reads the one header record.
func parseDecisionsHeader(rec record) (DecisionsHeader, error) {
	h := DecisionsHeader{}
	var err error
	if h.Run, err = rec.requireText("run"); err != nil {
		return h, err
	}
	if h.Lead, err = rec.requireText("lead"); err != nil {
		return h, err
	}
	if h.Plan, err = rec.requireText("plan"); err != nil {
		return h, err
	}
	if h.Capture, err = rec.requireText("capture"); err != nil {
		return h, err
	}
	if h.Count, err = rec.requireInt("count", 0, decisionsPageMax); err != nil {
		return h, err
	}
	if h.Since, err = rec.requireInt("since", 0, math.MaxInt32); err != nil {
		return h, err
	}
	if h.Next, err = rec.requireInt("next", 0, math.MaxInt32); err != nil {
		return h, err
	}
	if h.More, err = rec.requireBool("more"); err != nil {
		return h, err
	}
	if h.Extra, err = rec.extra(decisionsHeaderKeys); err != nil {
		return h, err
	}
	return h, nil
}

// decisionsHeaderKeys is the header record's typed set.
var decisionsHeaderKeys = map[string]bool{
	"run": true, "lead": true, "plan": true, "capture": true, "count": true, "since": true, "next": true, "more": true,
}

// parseDecisionRow reads one row. Every documented key is required; trunc=yes is the only truncation the module
// writes, and a row is one line, so a continuation line never reaches this function.
func parseDecisionRow(rec record) (DecisionRow, error) {
	d := DecisionRow{}
	var err error
	if d.Seq, err = rec.requireInt("seq", 0, math.MaxInt32); err != nil {
		return d, err
	}
	if d.T, err = requiredInt64(rec, "t", 0); err != nil {
		return d, err
	}
	if d.DT, err = requiredInt64(rec, "dt", 0); err != nil {
		return d, err
	}
	if d.State, err = requiredWord(rec, "state", "running", "paused", "done"); err != nil {
		return d, err
	}
	if d.Phase, err = requiredWord(rec, "phase", phaseWords()...); err != nil {
		return d, err
	}
	if d.Step, err = rec.requireInt("step", 0, math.MaxInt32); err != nil {
		return d, err
	}
	if d.Record, err = requiredWord(rec, "record", "tick", "stub", "dropped"); err != nil {
		return d, err
	}
	if d.Order, err = rec.requireText("order"); err != nil {
		return d, err
	}
	if d.Note, err = rec.requireText("note"); err != nil {
		return d, err
	}
	if d.Say, err = rec.requireText("say"); err != nil {
		return d, err
	}
	trunc, present, err := rec.optionalBool("trunc")
	if err != nil {
		return d, err
	}
	if present && !trunc {
		return d, fmt.Errorf("%w: trunc=no; the module writes trunc=yes only", ErrBadValue)
	}
	d.Trunc = trunc
	if d.Extra, err = rec.extra(decisionRowKeys); err != nil {
		return d, err
	}
	return d, nil
}

// decisionRowKeys is the row's typed set.
var decisionRowKeys = map[string]bool{
	"seq": true, "t": true, "dt": true, "state": true, "phase": true, "step": true, "record": true,
	"order": true, "note": true, "say": true, "trunc": true,
}

// requiredInt64 is requireInt for a value wider than int's 32-bit floor (the module's unix-millisecond clock).
func requiredInt64(rec record, key string, min int64) (int64, error) {
	s, err := rec.requireText(key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < min {
		return 0, fmt.Errorf("%w: %s=%q is not a number of at least %d", ErrBadValue, key, s, min)
	}
	return n, nil
}
