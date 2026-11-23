package tbmproto

import (
	"strconv"
	"strings"
)

// Incoming is one delivered message for the reader: Kind "addon" (a text
// with a prefix on some channel) or "system" (one chat line, one check).
type Incoming struct {
	Kind, Channel, Prefix, Text string
}

// RosterRow is one decoded ROSTER entry.
type RosterRow struct {
	GUID              uint32
	Name              string
	Class             uint32
	State             string
	Grouped           bool
	Location          string
	CCMark, ClassName string
}

// Ack is a decoded ACTION_ACK.
type Ack struct {
	Intent, Scope string
	Count         int
	Detail        string
}

// ActionErr is a decoded ACTION_ERR (Intent set) or ROSTER_ERROR
// (Intent "roster": the code travels in Code, the sentence in Message).
type ActionErr struct {
	Intent, Code, Message string
}

// Hello is the decoded hello trailer: TRANSPORT, PROTO, VERSION, CAPS,
// LATEST. Complete means TRANSPORT arrived at all.
type Hello struct {
	Complete                      bool
	Transport, Version            string
	Proto, MinAddon               int
	HasProto, HasVersion, HasCaps bool
	Caps                          []string
	// Verbs is the module's verb set from the VERBS stream, lowercased and in
	// the order received; HasVerbs reports whether the stream committed.
	Verbs                     []string
	HasVerbs                  bool
	LatestModule, LatestAddon string
}

// Check accepts a hello only when the trailer is whole: TRANSPORT plus
// PROTO, VERSION and CAPS all seen. A newer module that still accepts
// protocol 1 passes; one that needs protocol 2 is incompatible.
func (h Hello) Check() error {
	if !h.Complete || !h.HasProto || !h.HasVersion || !h.HasCaps {
		return ErrHelloIncomplete
	}
	if h.MinAddon > ProtoLevel {
		return ErrProtoIncompatible
	}
	return nil
}

// Result is the accumulated output of one reader run.
type Result struct {
	Hello           Hello
	Roster          []RosterRow
	RosterCommitted bool
	AltBots         []string
	RosterError     *ActionErr
	BotState        map[string]string
	Acks            []Ack
	Errs            []ActionErr
	Problems        []*Error
	Unknown         []string
	Ignored         int
}

// ccMarks disambiguates a 7th ROSTER field: a mark id is the CC mark, a
// class name is the class (spec section 1 drift note).
var ccMarks = map[string]bool{
	"moon": true, "circle": true, "square": true, "cross": true,
	"triangle": true, "diamond": true, "star": true, "skull": true,
}

type openStream struct {
	want          int
	kept, skipped int
	rows          []RosterRow
	pairs         map[string]string
	verbs         []string
	counted       int
}

// Reader accumulates one hello: feed each message, then Finish, then Result.
type Reader struct {
	res     Result
	streams map[string]*openStream
}

// NewReader returns an empty reader.
func NewReader() *Reader { return &Reader{streams: map[string]*openStream{}} }

// Feed handles one message per the spec 4.2 order: prefix filter, length
// check, TBM: filter, then the type dispatch.
func (r *Reader) Feed(in Incoming) {
	var lines []string
	if in.Kind == "addon" {
		if in.Prefix != Prefix {
			r.res.Ignored++
			return
		}
		if len(in.Text) > MaxText {
			r.problem("text-too-long", in.Text)
			return
		}
		lines = strings.Split(in.Text, "\n")
	} else {
		lines = []string{in.Text}
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, LinePrefix) {
			r.res.Ignored++
			continue
		}
		r.line(line)
	}
}

// Finish turns every still-open stream into an unterminated-stream problem.
func (r *Reader) Finish() {
	for _, name := range []string{"ROSTER", "BOTSTATE", "CC_ASSIGN", "CLAIMED", "VERBS"} {
		if r.streams[name] != nil {
			r.problem("unterminated-stream", name)
		}
	}
}

// Result returns the accumulated output.
func (r *Reader) Result() Result { return r.res }

func (r *Reader) problem(code, line string) {
	r.res.Problems = append(r.res.Problems, &Error{Code: code, Line: line})
}

func (r *Reader) line(line string) {
	rest := strings.TrimPrefix(line, LinePrefix)
	typ := rest
	fields := []string(nil)
	if i := strings.Index(rest, "|"); i >= 0 {
		typ, fields = rest[:i], strings.Split(rest[i+1:], "|")
	}
	switch typ {
	case "PROTO":
		if len(fields) != 2 {
			r.problem("field-count", line)
			return
		}
		a, err1 := parseUint(fields[0])
		b, err2 := parseUint(fields[1])
		if err1 != nil || err2 != nil {
			r.problem("bad-number", line)
			return
		}
		r.res.Hello.Proto, r.res.Hello.MinAddon = a, b
		r.res.Hello.HasProto = true
	case "VERSION":
		if len(fields) != 1 || fields[0] == "" {
			r.problem("empty-field", line)
			return
		}
		r.res.Hello.Version = fields[0]
		r.res.Hello.HasVersion = true
	case "CAPS":
		raw := strings.Join(fields, "|")
		var caps []string
		for _, c := range strings.FieldsFunc(raw, func(ch rune) bool { return ch == ',' || ch == '|' || ch == ' ' }) {
			if c = strings.ToLower(c); c != "" {
				caps = append(caps, c)
			}
		}
		r.res.Hello.Caps = caps
		r.res.Hello.HasCaps = true
	case "LATEST":
		if len(fields) >= 1 {
			r.res.Hello.LatestModule = fields[0]
		}
		if len(fields) >= 2 {
			r.res.Hello.LatestAddon = fields[1]
		}
	case "TRANSPORT":
		if len(fields) >= 1 {
			r.res.Hello.Transport = fields[0]
		}
		r.res.Hello.Complete = true
	case "ROSTER_BEGIN", "BOTSTATE_BEGIN", "CC_ASSIGN_BEGIN", "CLAIMED_BEGIN", "VERBS_BEGIN":
		name := strings.TrimSuffix(typ, "_BEGIN")
		if len(fields) != 1 {
			r.problem("field-count", line)
			return
		}
		n, err := parseUint(fields[0])
		if err != nil {
			r.problem("bad-number", line)
			return
		}
		if r.streams[name] != nil {
			r.problem("nested-stream", line)
		}
		s := &openStream{want: n}
		if name == "BOTSTATE" {
			s.pairs = map[string]string{}
		}
		r.streams[name] = s
	case "ROSTER_END", "BOTSTATE_END", "CC_ASSIGN_END", "CLAIMED_END", "VERBS_END":
		name := strings.TrimSuffix(typ, "_END")
		s := r.streams[name]
		r.streams[name] = nil
		if s == nil {
			r.problem("end-without-begin", line)
			return
		}
		switch name {
		case "ROSTER":
			if s.kept+s.skipped != s.want {
				r.problem("stream-count", line)
				return
			}
			r.res.Roster = s.rows
			r.res.RosterCommitted = true
		case "BOTSTATE":
			if s.kept+s.skipped != s.want {
				r.problem("stream-count", line)
				return
			}
			r.res.BotState = s.pairs
		case "VERBS":
			if s.counted != s.want {
				r.problem("stream-count", line)
				return
			}
			r.res.Hello.Verbs = s.verbs
			r.res.Hello.HasVerbs = true
		default:
			if s.counted != s.want {
				r.problem("stream-count", line)
			}
		}
	case "ROSTER":
		if s := r.streams["ROSTER"]; s != nil {
			row, ok := parseRoster(fields)
			if !ok {
				s.skipped++
				r.problem("bad-row", line)
				return
			}
			s.kept++
			s.rows = append(s.rows, row)
			return
		}
		r.problem("row-outside-stream", line)
	case "BOTSTATE":
		if s := r.streams["BOTSTATE"]; s != nil {
			if len(fields) != 2 {
				s.skipped++
				r.problem("bad-pair", line)
				return
			}
			if !validPairs(fields[1]) {
				s.skipped++
				r.problem("bad-pair", line)
				return
			}
			s.kept++
			s.pairs[fields[0]] = fields[1]
			return
		}
		if len(fields) != 2 || !validPairs(fields[1]) {
			r.problem("bad-pair", line)
			return
		}
		if r.res.BotState == nil {
			r.res.BotState = map[string]string{}
		}
		r.res.BotState[fields[0]] = fields[1]
	case "CC_ASSIGN", "CLAIMED":
		if s := r.streams[typ]; s != nil {
			s.counted++
		}
	case "VERBS":
		if s := r.streams["VERBS"]; s != nil {
			for _, tok := range strings.Split(strings.Join(fields, "|"), ",") {
				if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
					s.verbs = append(s.verbs, tok)
					s.counted++
				}
			}
			return
		}
		r.problem("row-outside-stream", line)
	case "ALTBOTS":
		joined := strings.Join(fields, "|")
		if joined == "" {
			r.res.AltBots = nil
			return
		}
		r.res.AltBots = strings.Split(joined, ",")
	case "ROSTER_ERROR":
		if len(fields) < 2 {
			r.problem("field-count", line)
			return
		}
		msg := strings.Join(fields[1:], "|")
		r.res.RosterError = &ActionErr{Intent: "roster", Code: fields[0], Message: msg}
	case "ACTION_ACK":
		if len(fields) != 4 {
			r.problem("field-count", line)
			return
		}
		if fields[0] == "" || fields[3] == "" {
			r.problem("empty-field", line)
			return
		}
		if !validScope(fields[1]) {
			r.problem("bad-scope", line)
			return
		}
		n, err := parseUint(fields[2])
		if err != nil {
			r.problem("bad-number", line)
			return
		}
		r.res.Acks = append(r.res.Acks, Ack{Intent: fields[0], Scope: fields[1], Count: n, Detail: fields[3]})
	case "ACTION_ERR":
		if len(fields) != 3 {
			r.problem("field-count", line)
			return
		}
		if fields[0] == "" || fields[1] == "" {
			r.problem("empty-field", line)
			return
		}
		r.res.Errs = append(r.res.Errs, ActionErr{Intent: fields[0], Code: fields[1], Message: fields[2]})
	default:
		r.res.Unknown = append(r.res.Unknown, line)
	}
}

// parseUint reads an unsigned decimal; class ids and guids fit an int.
func parseUint(s string) (int, error) {
	if s == "" {
		return 0, strconv.ErrSyntax
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// parseRoster decodes 6+ fields; a 7th field holding a CC mark id is the
// mark, else the class name; "-" means empty.
func parseRoster(fields []string) (RosterRow, bool) {
	var row RosterRow
	if len(fields) < 6 {
		return row, false
	}
	guid, err1 := parseUint(fields[0])
	class, err2 := parseUint(fields[2])
	if err1 != nil || err2 != nil {
		return row, false
	}
	if fields[1] == "" || fields[3] == "" || fields[5] == "" {
		return row, false
	}
	var grouped bool
	switch fields[4] {
	case "0":
	case "1":
		grouped = true
	default:
		return row, false
	}
	row = RosterRow{GUID: uint32(guid), Name: fields[1], Class: uint32(class), State: fields[3], Grouped: grouped, Location: fields[5]}
	switch {
	case len(fields) >= 8:
		row.CCMark, row.ClassName = dashEmpty(fields[6]), dashEmpty(fields[7])
	case len(fields) == 7:
		if ccMarks[fields[6]] {
			row.CCMark = fields[6]
		} else {
			row.ClassName = dashEmpty(fields[6])
		}
	}
	return row, true
}

// dashEmpty maps the module's "-" for none to the empty string.
func dashEmpty(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// validPairs checks comma-separated key=value pairs with non-empty keys.
func validPairs(kv string) bool {
	for _, p := range strings.Split(kv, ",") {
		i := strings.Index(p, "=")
		if i <= 0 {
			return false
		}
	}
	return true
}

// validScope accepts party, raid or bot:<non-empty>.
func validScope(s string) bool {
	if s == "party" || s == "raid" {
		return true
	}
	return strings.HasPrefix(s, "bot:") && len(s) > len("bot:")
}
