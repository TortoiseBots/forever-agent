// Package pbc is the add-on protocol a WoW Forever Bots server module speaks over the client's add-on message
// transport. This client uses it to read state (the roster, one bot's detail); orders go out as whisper or party
// chat, not through this protocol.
//
// One message is a line of six fields separated by '|': protocol digit 1, a sequence number as eight lower-case
// hex digits, the part index (from 1), the part count, a message type in capitals, and the payload. The payload is
// a list of fields, also joined by '|'. Inside any field a backslash is written as two and a pipe as backslash-p.
// A body longer than 200 bytes is cut into parts; the parts carry the body's bytes in order, so an escape pair
// may be split across two parts. Nothing in the protocol carries a version: the message types and their field
// counts are the only version signal (see the parsers' drift rule).
//
// The server sends a request's answer on the request's own sequence number and its unsolicited notifications on
// numbers from 0x80000000 up, so a caller can tell an answer from a push.
package pbc

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The protocol's fixed values and the server's bounds.
const (
	// Prefix is the add-on prefix every message of this protocol carries.
	Prefix = "PBC"
	// Version is the protocol digit of the envelope.
	Version = "1"
	// MaxChunk is how many payload bytes one client part carries. The envelope adds about thirty bytes; the
	// transport refuses a text over 255 bytes.
	MaxChunk = 200
	// MaxParts is the server's cap on a client's part count. The same figure bounds this client's own reassembly
	// (the module's receive side, AddonControl.h:32 kMaxReassemblyChunks).
	MaxParts = 64
	// MaxFrame is the largest body this client accepts once reassembled. The module writes parts of 240 bytes
	// (AddonControl.h:29 kMaxChunkBytes) and caps a client's part count at 64 (AddonControl.h:32), so the largest
	// body its own bound admits is 64 x 240 = 15,360 bytes; a roster of about 45 bots (the largest an account can
	// hold) needs far less. The old 4,096-byte bound cut a large roster off mid-answer.
	MaxFrame = MaxParts * 240
	// MaxPending is how many different bodies (sender, sequence) may be incomplete at once. The TTL alone would
	// let a peer park an unbounded number of them for six seconds.
	MaxPending = 64
	// DefaultTTL bounds how long an incomplete body is kept. The server drops its own buffers after five seconds;
	// the client keeps a little longer so a slow but complete answer still lands.
	DefaultTTL = 6 * time.Second
	// FirstServerSeq is the first sequence number a server push uses. Answers echo the request's number, which is
	// below this.
	FirstServerSeq = 0x80000000
	// LastRequestSeq is the highest sequence number a client request may use.
	LastRequestSeq = 0x7FFFFFFE
)

// Message types this client sends and reads. The module knows more; this round uses these.
const (
	TypePing       = "PING"
	TypePong       = "PONG"
	TypeRosterReq  = "ROSTER_REQ"
	TypeRosterResp = "ROSTER_RESP"
	TypeDetailReq  = "BOT_DETAIL_REQ"
	TypeDetailResp = "BOT_DETAIL_RESP"
	TypeLfgReq     = "LFG_REQ"
	TypeLfgResp    = "LFG_RESP"
	TypeEventPush  = "EVENT_PUSH"
)

// RosterFieldCount is how many comma-separated values one roster record held when this client was written. The
// module appends values at the end, so a record with more is accepted and the extras are kept raw; one with fewer
// is a shape this client cannot read.
const RosterFieldCount = 19

// Errors callers branch on.
var (
	// ErrNotFrame is a text that is not this protocol's envelope.
	ErrNotFrame = errors.New("pbc: the text is not a protocol frame")
	// ErrTooManyParts is a part count past the server's cap.
	ErrTooManyParts = fmt.Errorf("pbc: a body has more than %d parts", MaxParts)
	// ErrPartIndex is a part index outside 1..count.
	ErrPartIndex = errors.New("pbc: the part index is outside 1..count")
	// ErrFrameTooBig is a body past MaxFrame.
	ErrFrameTooBig = fmt.Errorf("pbc: a body is larger than %d bytes", MaxFrame)
	// ErrTooManyBodies is more incomplete bodies at once than MaxPending.
	ErrTooManyBodies = fmt.Errorf("pbc: more than %d bodies under construction", MaxPending)
	// ErrBotUnknown is the module's id-only answer to a detail request: it does not know that bot.
	ErrBotUnknown = errors.New("pbc: the module does not know that bot")
	// ErrType is a message type with characters outside the protocol's grammar.
	ErrType = errors.New("pbc: the message type is not capitals, digits and underscores")
	// ErrProtoMismatch is an answer whose shape this client was not written for: a record with fewer values, a
	// count that disagrees with the lines, an answer of another type. It is the drift signal: the caller falls
	// back instead of guessing.
	ErrProtoMismatch = errors.New("pbc: the answer does not have the shape this client reads")
	// ErrNoFields is an answer with no fields at all.
	ErrNoFields = errors.New("pbc: the answer has no fields")
)

// Part is one parsed frame. Sender is not on the wire: the transport sets it from the delivering add-on message,
// so assemblies of two senders can never mix even on the same sequence number.
type Part struct {
	Sender  wire.GUID
	Seq     uint32
	Index   int
	Total   int
	Type    string
	Payload string
}

// FormatPart writes one part's text: version, sequence, index, count, type, payload.
func FormatPart(seq uint32, index, total int, mtype, payload string) string {
	return fmt.Sprintf("%s|%08x|%d|%d|%s|%s", Version, seq, index, total, mtype, payload)
}

// ParsePart reads one frame text. It checks the envelope's own bounds; the meaning of the payload is the
// caller's. The type is kept as sent, including one this client does not know: the caller decides what an unknown
// type means for its request.
func ParsePart(text string) (Part, error) {
	if !strings.HasPrefix(text, Version+"|") {
		return Part{}, ErrNotFrame
	}
	// The envelope's first five separators are raw; everything after the fifth is the payload.
	fields := strings.SplitN(text, "|", 6)
	if len(fields) != 6 {
		return Part{}, ErrNotFrame
	}
	seq, err := strconv.ParseUint(fields[1], 16, 32)
	if err != nil || seq == 0 || seq > 0xFFFFFFFF {
		return Part{}, ErrNotFrame
	}
	index, err := strconv.Atoi(fields[2])
	if err != nil || index < 1 {
		return Part{}, ErrPartIndex
	}
	total, err := strconv.Atoi(fields[3])
	if err != nil || total < 1 {
		return Part{}, ErrPartIndex
	}
	if total > MaxParts {
		return Part{}, ErrTooManyParts
	}
	if index > total {
		return Part{}, ErrPartIndex
	}
	mtype := fields[4]
	if !ValidType(mtype) {
		return Part{}, ErrType
	}
	payload := fields[5]
	if len(payload) > MaxFrame {
		return Part{}, ErrFrameTooBig
	}
	return Part{Seq: uint32(seq), Index: index, Total: total, Type: mtype, Payload: payload}, nil
}

// ValidType reports whether a message type is inside the grammar: non-empty, at most 32 bytes of A-Z, 0-9 and '_'.
func ValidType(t string) bool {
	if t == "" || len(t) > 32 {
		return false
	}
	for i := range len(t) {
		switch c := t[i]; {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// Escape writes one field: a backslash becomes two, a pipe becomes backslash-p.
func Escape(s string) string {
	if !strings.ContainsAny(s, "\\|") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := range len(s) {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '|':
			b.WriteString(`\p`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// Unescape reads one escaped field. A backslash before any other byte drops itself, as the server's reader does; a
// trailing backslash is kept.
func Unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if s[i+1] == 'p' {
				b.WriteByte('|')
			} else {
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// BuildBody joins fields with the protocol's separator, escaping each one.
func BuildBody(fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = Escape(f)
	}
	return strings.Join(parts, "|")
}

// SplitFields splits a body on its unescaped separators and unescapes each field. It is the inverse of BuildBody
// for a well-formed body.
func SplitFields(body string) []string {
	out := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(body); {
		switch body[i] {
		case '\\':
			if i+1 < len(body) {
				i += 2
			} else {
				i++
			}
		case '|':
			out = append(out, Unescape(body[start:i]))
			start = i + 1
			i++
		default:
			i++
		}
	}
	out = append(out, Unescape(body[start:]))
	return out
}

// BuildRequest cuts one request into its parts. The body is escaped fields joined by pipes, then cut every
// MaxChunk bytes; a cut may fall inside an escape pair or a multi-byte character, which is safe because the
// receiver glues the parts back together before reading the fields.
func BuildRequest(seq uint32, mtype string, fields []string) ([]string, error) {
	if seq == 0 || seq > LastRequestSeq {
		return nil, fmt.Errorf("pbc: request sequence %d is outside 1..0x%x", seq, LastRequestSeq)
	}
	if !ValidType(mtype) {
		return nil, ErrType
	}
	body := BuildBody(fields)
	total := (len(body) + MaxChunk - 1) / MaxChunk
	if total == 0 {
		total = 1
	}
	if total > MaxParts {
		return nil, ErrTooManyParts
	}
	parts := make([]string, 0, total)
	for i := 0; i < total; i++ {
		end := (i + 1) * MaxChunk
		if end > len(body) {
			end = len(body)
		}
		parts = append(parts, FormatPart(seq, i+1, total, mtype, body[i*MaxChunk:end]))
	}
	return parts, nil
}

// Result is one reassembled body.
type Result struct {
	Sender wire.GUID
	Seq    uint32
	Type   string
	Body   string
}

// assemblyKey identifies one body under construction: one sender's one sequence number.
type assemblyKey struct {
	sender wire.GUID
	seq    uint32
}

// assembly is one body under construction.
type assembly struct {
	total int
	// parts holds the payload of each index, 1-based. An empty part is missing; a part may legitimately be empty,
	// which the count tracks separately.
	parts   []string
	got     []bool
	mtype   string
	bodyLen int
	at      time.Time
}

// Assembler reassembles parts per (sender, sequence number). It is not safe for concurrent use; the session feeds
// it from one goroutine. Bounds: the server's part cap, MaxFrame bytes in all, MaxPending bodies at once, and a TTL
// per body.
type Assembler struct {
	buffers map[assemblyKey]*assembly
	ttl     time.Duration
}

// NewAssembler returns an empty assembler with DefaultTTL.
func NewAssembler() *Assembler {
	return &Assembler{buffers: map[assemblyKey]*assembly{}, ttl: DefaultTTL}
}

// Add takes one part. done is true when a body is complete; the caller then reads it. A duplicate index is
// ignored, a conflicting count restarts the body, a body past MaxFrame is dropped with ErrFrameTooBig, and a new
// body while MaxPending are held is refused with ErrTooManyBodies.
func (a *Assembler) Add(p Part, now time.Time) (res Result, done bool, err error) {
	if a.ttl <= 0 {
		a.ttl = DefaultTTL
	}
	a.expire(now)
	if p.Total > MaxParts {
		return Result{}, false, ErrTooManyParts
	}
	if p.Index < 1 || p.Index > p.Total {
		return Result{}, false, ErrPartIndex
	}
	if p.Total == 1 {
		if len(p.Payload) > MaxFrame {
			return Result{}, false, ErrFrameTooBig
		}
		return Result{Sender: p.Sender, Seq: p.Seq, Type: p.Type, Body: p.Payload}, true, nil
	}
	key := assemblyKey{sender: p.Sender, seq: p.Seq}
	buf := a.buffers[key]
	if buf == nil || buf.total != p.Total || buf.mtype != p.Type {
		if buf == nil && len(a.buffers) >= MaxPending {
			return Result{}, false, ErrTooManyBodies
		}
		buf = &assembly{total: p.Total, parts: make([]string, p.Total+1), got: make([]bool, p.Total+1), mtype: p.Type, at: now}
		a.buffers[key] = buf
	}
	if buf.got[p.Index] {
		return Result{}, false, nil
	}
	if buf.bodyLen+len(p.Payload) > MaxFrame {
		delete(a.buffers, key)
		return Result{}, false, ErrFrameTooBig
	}
	buf.parts[p.Index] = p.Payload
	buf.got[p.Index] = true
	buf.bodyLen += len(p.Payload)
	buf.at = now
	for i := 1; i <= buf.total; i++ {
		if !buf.got[i] {
			return Result{}, false, nil
		}
	}
	var b strings.Builder
	b.Grow(buf.bodyLen)
	for i := 1; i <= buf.total; i++ {
		b.WriteString(buf.parts[i])
	}
	delete(a.buffers, key)
	return Result{Sender: p.Sender, Seq: p.Seq, Type: buf.mtype, Body: b.String()}, true, nil
}

// Expire drops bodies whose last part is older than the TTL and returns how many it dropped.
func (a *Assembler) Expire(now time.Time) int { return a.expire(now) }

func (a *Assembler) expire(now time.Time) int {
	ttl := a.ttl
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	n := 0
	for seq, buf := range a.buffers {
		if now.Sub(buf.at) >= ttl {
			delete(a.buffers, seq)
			n++
		}
	}
	return n
}

// Pending is how many bodies are under construction, for diagnostics.
func (a *Assembler) Pending() int { return len(a.buffers) }

// ---- requests ----

// RosterRequest asks for the bots of the caller's account. The module reads the flag word as bits: bit one
// includes bots that are not logged in. This client asks for every bot (15), the value the module's own add-on
// uses when no flags are given.
func RosterRequest() []string { return []string{"15"} }

// DetailRequest asks for one bot's state by its numeric id.
func DetailRequest(id uint32) []string { return []string{strconv.FormatUint(uint64(id), 10)} }

// LfgRequest asks where the caller stands with the module's dungeon finder. The request carries no fields; the
// module reads the caller's group and finder state itself.
func LfgRequest() []string { return nil }

// ---- lfg ----

// LfgStatus is LFG_RESP: the finder's phase, the dungeon, the waits and the group. The phase is one of the module's
// own words (DungeonFinderState in its header): idle, forming, entering, leaving or inside. Inside is the server's
// map being a dungeon; the client's own map change is a second signal the caller checks separately.
type LfgStatus struct {
	Phase       string
	Name        string
	WaitedSec   uint32
	FormWaitSec uint32
	Members     uint32
	Size        uint32
	CanLeave    bool
	Raw         []string
}

// LfgPhases are the phase words the module defines. A phase outside them is a shape this client was not written for.
var LfgPhases = []string{"idle", "forming", "entering", "leaving", "inside"}

// LfgFieldCount is how many fields LFG_RESP held when this client was written. The module appends fields at the end,
// so more are kept raw; fewer are ErrProtoMismatch.
const LfgFieldCount = 7

// ParseLfg reads an LFG_RESP's fields: phase, dungeon name, seconds waited, form wait seconds, group members, party
// size, can leave (0/1). A count or a flag that is not a number, a phase this client does not know, or a negative
// number is the drift signal (ErrProtoMismatch), never a guessed state.
func ParseLfg(fields []string) (LfgStatus, error) {
	if len(fields) < LfgFieldCount {
		return LfgStatus{}, fmt.Errorf("%w: an LFG answer has %d field(s), this client reads %d", ErrProtoMismatch, len(fields), LfgFieldCount)
	}
	phase := strings.ToLower(strings.TrimSpace(fields[0]))
	known := false
	for _, p := range LfgPhases {
		if phase == p {
			known = true
			break
		}
	}
	if !known {
		return LfgStatus{}, fmt.Errorf("%w: the finder phase is %q", ErrProtoMismatch, fields[0])
	}
	num := func(i int, what string) (uint32, error) {
		n, err := parseInt(fields[i])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%w: the %s field %q", ErrProtoMismatch, what, fields[i])
		}
		return uint32(n), nil
	}
	waited, err := num(2, "seconds waited")
	if err != nil {
		return LfgStatus{}, err
	}
	form, err := num(3, "form wait")
	if err != nil {
		return LfgStatus{}, err
	}
	members, err := num(4, "group members")
	if err != nil {
		return LfgStatus{}, err
	}
	size, err := num(5, "party size")
	if err != nil {
		return LfgStatus{}, err
	}
	if fields[6] != "0" && fields[6] != "1" {
		return LfgStatus{}, fmt.Errorf("%w: can-leave is %q, not 0 or 1", ErrProtoMismatch, fields[6])
	}
	return LfgStatus{
		Phase: phase, Name: fields[1], WaitedSec: waited, FormWaitSec: form,
		Members: members, Size: size, CanLeave: fields[6] == "1",
		Raw: append([]string(nil), fields[LfgFieldCount:]...),
	}, nil
}

// ---- roster ----

// RosterEntry is one bot of the roster. The module sends one comma-separated record per bot; extras past the
// fields this client knows are kept in Raw.
type RosterEntry struct {
	ID          uint32
	Name        string
	Level       uint32
	Class       string
	Race        string
	Role        string
	Zone        string
	Online      bool
	HPPercent   int
	ManaPercent int
	Distance    int
	Intent      string
	LastRule    string
	GroupID     uint32
	Spec        string
	Headless    bool
	Saved       bool
	Grouped     bool
	SpecID      uint32
	Raw         []string
}

// ParseRoster reads a ROSTER_RESP's fields: a count, then that many records.
func ParseRoster(fields []string) ([]RosterEntry, error) {
	if len(fields) == 0 {
		return nil, ErrNoFields
	}
	count, err := parseInt(fields[0])
	if err != nil || count < 0 {
		return nil, fmt.Errorf("%w: the count field", ErrProtoMismatch)
	}
	if count != len(fields)-1 {
		return nil, fmt.Errorf("%w: the count says %d, the answer holds %d record(s)", ErrProtoMismatch, count, len(fields)-1)
	}
	out := make([]RosterEntry, 0, count)
	for _, rec := range fields[1 : 1+count] {
		e, err := parseRosterRecord(rec)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func parseRosterRecord(rec string) (RosterEntry, error) {
	// The name is a character name and cannot hold a comma, so a plain split is the module's own record format.
	v := strings.Split(rec, ",")
	if len(v) < RosterFieldCount {
		return RosterEntry{}, fmt.Errorf("%w: a roster record has %d value(s), this client reads %d", ErrProtoMismatch, len(v), RosterFieldCount)
	}
	num := func(i int, what string) (int, error) {
		n, err := parseInt(v[i])
		if err != nil {
			return 0, fmt.Errorf("%w: the record's %s", ErrProtoMismatch, what)
		}
		return n, nil
	}
	id, err := num(0, "id")
	if err != nil {
		return RosterEntry{}, err
	}
	level, err := num(2, "level")
	if err != nil {
		return RosterEntry{}, err
	}
	if level < 0 {
		return RosterEntry{}, fmt.Errorf("%w: the record's level is %d", ErrProtoMismatch, level)
	}
	hp, err := num(8, "hp percent")
	if err != nil {
		return RosterEntry{}, err
	}
	mana, err := num(9, "mana percent")
	if err != nil {
		return RosterEntry{}, err
	}
	dist, err := num(10, "distance")
	if err != nil {
		return RosterEntry{}, err
	}
	group, err := num(13, "group id")
	if err != nil {
		return RosterEntry{}, err
	}
	specID, err := num(18, "spec id")
	if err != nil {
		return RosterEntry{}, err
	}
	for _, f := range []struct {
		i    int
		what string
	}{{7, "online"}, {15, "headless"}, {16, "saved"}, {17, "grouped"}} {
		if v[f.i] != "0" && v[f.i] != "1" {
			return RosterEntry{}, fmt.Errorf("%w: the record's %s is %q, not 0 or 1", ErrProtoMismatch, f.what, v[f.i])
		}
	}
	return RosterEntry{
		ID:          uint32(id),
		Name:        v[1],
		Level:       uint32(level),
		Class:       v[3],
		Race:        v[4],
		Role:        v[5],
		Zone:        v[6],
		Online:      v[7] == "1",
		HPPercent:   hp,
		ManaPercent: mana,
		Distance:    dist,
		Intent:      v[11],
		LastRule:    v[12],
		GroupID:     uint32(group),
		Spec:        v[14],
		Headless:    v[15] == "1",
		Saved:       v[16] == "1",
		Grouped:     v[17] == "1",
		SpecID:      uint32(specID),
		Raw:         append([]string(nil), v[RosterFieldCount:]...),
	}, nil
}

// ---- detail ----

// BotDetail is BOT_DETAIL_RESP: the id, then key=value fields. Keys this client does not know are kept in Extra,
// so a module that appends one does not break the read. Known is false for the module's id-only answer (it does
// not know the bot); Live is true only when the snapshot block came, so a zero hp or map is not mistaken for a
// missing one (the module writes hp first inside that block, AddonControl.cpp:610-654).
type BotDetail struct {
	ID          uint32
	Known       bool
	Live        bool
	Present     map[string]bool
	Name        string
	Level       int
	Class       string
	Race        string
	HP          int
	HPMax       int
	Mana        int
	ManaMax     int
	Map         uint32
	X, Y, Z     float64
	ZoneID      uint32
	AreaID      uint32
	Indoors     bool
	Role        string
	SpecID      uint32
	XP          int64
	XPForLevel  int64
	RestXP      int64
	LastRule    string
	IntentDepth int
	Extra       map[string]string
}

// ParseDetail reads a BOT_DETAIL_RESP's fields. want is the id the request named; the answer's first field must
// be that id. A response with only the id is the module's "I do not know this bot" and returns ErrBotUnknown with
// Known false; an answer with only the character fields (name, level, class, race) is known but not live.
func ParseDetail(fields []string, want uint32) (BotDetail, error) {
	if len(fields) == 0 {
		return BotDetail{}, ErrNoFields
	}
	id, err := parseInt(fields[0])
	if err != nil || id < 0 {
		return BotDetail{}, fmt.Errorf("%w: the id field", ErrProtoMismatch)
	}
	if uint32(id) != want {
		return BotDetail{}, fmt.Errorf("%w: the answer is for id %d, the request named %d", ErrProtoMismatch, id, want)
	}
	d := BotDetail{ID: uint32(id), Present: map[string]bool{}, Extra: map[string]string{}}
	if len(fields) == 1 {
		return d, ErrBotUnknown
	}
	d.Known = true
	var bad error
	into := func(dst *int, v string) {
		if bad != nil {
			return
		}
		*dst, bad = parseInt(v)
	}
	u32 := func(dst *uint32, v string) {
		if bad != nil {
			return
		}
		var n int
		if n, bad = parseInt(v); bad == nil {
			*dst = uint32(n)
		}
	}
	i64 := func(dst *int64, v string) {
		if bad != nil {
			return
		}
		*dst, bad = parseInt64(v)
	}
	flt := func(dst *float64, v string) {
		if bad != nil {
			return
		}
		*dst, bad = parseFloat(v)
	}
	for _, f := range fields[1:] {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			// A field that is not key=value is not one this client reads: keep it raw rather than lose it.
			d.Extra["?"+f] = f
			continue
		}
		d.Present[k] = true
		switch k {
		case "name":
			d.Name = v
		case "level":
			into(&d.Level, v)
		case "class":
			d.Class = v
		case "race":
			d.Race = v
		case "hp":
			into(&d.HP, v)
		case "hp_max":
			into(&d.HPMax, v)
		case "mana":
			into(&d.Mana, v)
		case "mana_max":
			into(&d.ManaMax, v)
		case "map":
			u32(&d.Map, v)
		case "pos_x":
			flt(&d.X, v)
		case "pos_y":
			flt(&d.Y, v)
		case "pos_z":
			flt(&d.Z, v)
		case "zone_id":
			u32(&d.ZoneID, v)
		case "area_id":
			u32(&d.AreaID, v)
		case "indoors":
			if v != "0" && v != "1" {
				if bad == nil {
					bad = fmt.Errorf("indoors=%q", v)
				}
			}
			d.Indoors = v == "1"
		case "role":
			d.Role = v
		case "spec_id":
			u32(&d.SpecID, v)
		case "xp":
			i64(&d.XP, v)
		case "xp_for_level":
			i64(&d.XPForLevel, v)
		case "rest_xp":
			i64(&d.RestXP, v)
		case "last_rule":
			d.LastRule = v
		case "intent_depth":
			into(&d.IntentDepth, v)
		default:
			d.Extra[k] = v
		}
	}
	if bad != nil {
		return BotDetail{}, fmt.Errorf("%w: a known key's value is %v", ErrProtoMismatch, bad)
	}
	d.Live = d.Present["hp"]
	return d, nil
}

// ---- push events ----

// Event is an EVENT_PUSH: the module tells the client about something it did not ask for, or refuses one request
// with a named reason.
type Event struct {
	Severity string
	BotID    uint32
	Bot      string
	Name     string
	Detail   string
}

// ParseEvent reads an EVENT_PUSH's fields: severity, bot id, event name, detail.
func ParseEvent(fields []string) (Event, error) {
	if len(fields) < 4 {
		return Event{}, fmt.Errorf("%w: an event has %d field(s)", ErrProtoMismatch, len(fields))
	}
	id, _ := parseInt(fields[1])
	return Event{
		Severity: fields[0],
		BotID:    uint32(id),
		Name:     fields[2],
		Detail:   fields[3],
	}, nil
}

// ---- helpers ----

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if n > 1<<31-1 || n < -(1<<31) {
		return 0, errors.New("out of range")
	}
	return int(n), nil
}

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func parseFloat(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("not a finite number")
	}
	return f, nil
}
