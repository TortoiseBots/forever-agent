package driver

// The death recordings: `.deathrecap <name>` is a core GM command (TrinityCore
// PR #380 on Karrz1/TrinityCore, not merged yet) that prints the last recorded
// death of the named player as plain text: a header line, one frame line per
// sampled frame (the victim's position and health), then one line per cast
// before the death. The server answers "no recording" where it has none, and
// on a core without the PR the collector refuses the line as unknown command,
// so the watch asks once per death and never spams.
//
// The parser reads that text (the reply's Raw lines, as the core sent them).
// The hook lives in the watch: when the event ring records a bot death, the
// agent asks `.deathrecap <bot>` once through the GM line rules it already has
// (only for bots of this session's run), parses the answer, and attaches the
// parsed timeline to the death in the watch record and the bug hunter's ticket
// text. A core without the command, or "no recording", is recorded once and
// the agent stops asking. Bounded: one request per death (state per death, never
// per bot), one deadline, at most one request per watch tick, a few retries, at
// most MaxDeathRecapLines lines parsed.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// MaxDeathRecapLines bounds one parsed recording: the core sends at most 60
// lines (GetLastDeathLines' maxLines), header included.
const MaxDeathRecapLines = 60

// DeathRecapWait bounds one `.deathrecap` request on the watch: a core send
// that cannot be confirmed must not hold the poll loop.
const DeathRecapWait = 10 * time.Second

// deathRecapNoRecordingRe is the core's answer where it recorded no death of
// the named player (cs_misc.cpp, PR #380's PSendSysMessage): the whole line
// "No death recording for <name>.", matched exactly and only as the reply's
// first line.
var deathRecapNoRecordingRe = regexp.MustCompile(`^No death recording for \S+\.$`)

// Retry bounds of one death's request: a transient error (timeout, garbled
// reply, a stale recording) is retried this many times, DeathRecapRetryGap
// times the try number apart, and a request older than DeathRecapQueueTTL is
// given up. At most one request goes out per watch tick.
const (
	DeathRecapTries    = 3
	DeathRecapRetryGap = 5 * time.Second
	DeathRecapQueueTTL = 2 * time.Minute
)

// DeathRecap runs the hook's one request per death: the line is sent through
// act and its gate, like every other GM line, and only for a bot of this
// session's run. The reply's lines are parsed and returned; the caller
// attaches them to the death in the watch record and the ticket text.
func (r *Run) deathRecap(ctx context.Context, bot string) ([]DeathRecapLine, error) {
	name, err := chat.NormaliseName(bot)
	if err != nil {
		return nil, fmt.Errorf("deathrecap: %w", err)
	}
	party := false
	for _, n := range r.cfg.Party {
		if strings.EqualFold(n, name) {
			party = true
			break
		}
	}
	if !party {
		return nil, fmt.Errorf("deathrecap: %q is not a bot of this run", bot)
	}
	cctx, cancel := context.WithTimeout(ctx, DeathRecapWait)
	defer cancel()
	rep, why := r.act(cctx, Action{Kind: ActionDeathRecap, Who: name})
	switch why {
	case "":
	case "link", "stop":
		return nil, fmt.Errorf("%w: %s", errDeathRecapEnded, why)
	default:
		if why == "error" && rep.Err != nil {
			return nil, fmt.Errorf("deathrecap: %w", rep.Err)
		}
		return nil, fmt.Errorf("deathrecap: %s", why)
	}
	lines, err := ParseDeathRecap(rep.Lines)
	if err != nil {
		return nil, err
	}
	// The reply is bound to the unit asked about: a recap of another player (a foreign line among the reply's
	// tokenless lines) is never this bot's evidence.
	if v := lines[0].Victim; !strings.EqualFold(v, name) {
		return nil, fmt.Errorf("deathrecap: the recap is for %q, not %q", v, name)
	}
	return lines, nil
}

// errDeathRecapEnded marks a request the run's own end or lost link cut off: nothing is retried.
var errDeathRecapEnded = errors.New("deathrecap: the run ended")

// DeathRecapKind is one parsed line's kind: the header, one frame, one cast,
// or the core's "no casts recorded" note.
type DeathRecapKind int

const (
	DeathRecapHeader DeathRecapKind = iota + 1
	DeathRecapFrame
	DeathRecapCast
	DeathRecapNoCasts
)

// DeathRecapLine is one parsed line of the recording.
type DeathRecapLine struct {
	Kind DeathRecapKind
	// Header holds the raw header text (the recap id, the victim, the map,
	// the frame and event counts); ID and Victim are its parsed recap id and
	// victim name.
	Header string
	ID     string
	Victim string
	// OffsetS is the line's offset in seconds from the death (negative).
	OffsetS float64
	// Position and health are the frame's own: X/Y/Z in yards, HP/MaxHP.
	X, Y, Z float64
	HP      uint64
	MaxHP   uint64
	HPPct   uint64
	// Caster, Spell and Target are the cast's own, as the core printed them.
	Caster string
	Spell  string
	Target string
}

// String renders one parsed line as the watch record and the ticket text carry
// it: the same words the core sent, on one line.
func (l DeathRecapLine) String() string {
	switch l.Kind {
	case DeathRecapHeader:
		return l.Header
	case DeathRecapFrame:
		return fmt.Sprintf("%+.1fs  pos %.1f %.1f %.1f  hp %d/%d (%d%%)",
			l.OffsetS, l.X, l.Y, l.Z, l.HP, l.MaxHP, l.HPPct)
	case DeathRecapCast:
		return fmt.Sprintf("%+.1fs  %s casts %s on %s", l.OffsetS, l.Caster, l.Spell, l.Target)
	case DeathRecapNoCasts:
		return "no casts recorded"
	}
	return ""
}

// ErrDeathRecapNone answers where the core recorded no death of the player:
// the command ran and said so. The caller records it once and stops asking.
var ErrDeathRecapNone = errors.New("deathrecap: the server has no death recording for the player")

// ErrDeathRecapMissing answers where the core has no such command (PR #380 not
// applied): the collector refuses the line as unknown command. The caller
// records it once and stops asking.
var ErrDeathRecapMissing = errors.New("deathrecap: the server has no .deathrecap command")

var (
	deathRecapHeaderRe = regexp.MustCompile(`^Recap #([0-9]+) of (\S+): map .*[0-9]+ frames, [0-9]+ events,`)
	deathRecapFrameRe  = regexp.MustCompile(`^([+-][0-9.]+)s\s+pos\s+(-?[0-9.]+)\s+(-?[0-9.]+)\s+(-?[0-9.]+)\s+hp\s+([0-9]+)/([0-9]+)\s+\(([0-9]+)%\)$`)
	// The core prints "<caster> casts <spell> on <target>" with single spaces. The caster ends at the first
	// " casts ", the target starts after the last " on ", and the spell is everything between (it may be empty or
	// hold " on "), so the split is one fixed rule.
	deathRecapCastRe = regexp.MustCompile(`^([+-][0-9.]+)s\s+(.+?) casts (.*) on (.+)$`)
)

// ParseDeathRecap parses the core's `.deathrecap` text: at most
// MaxDeathRecapLines lines, the header, then the frames, then the casts.
// The format strings are PR #380's own (DeathRecap.cpp GetLastDeathLines):
//
//	Recap #<id> of <victim>: map <map> instance <inst>, <n> frames, <m> events, offsets in seconds from the death.
//	<offset>s  pos <x> <y> <z>  hp <hp>/<max> (<pct>%)
//	<offset>s  <caster> casts <spell> on <target>
//	  no casts recorded
//
// The reply is the tokenless collection, so an unrelated system line (an
// announcement, other GM output) can come before or among the recap lines: lines
// before the header and lines of no known shape after it are skipped, never a
// reason to drop the recap. "No death recording for <name>." as the first
// line is ErrDeathRecapNone. A reply with no header (the core's other refusals,
// "No player with that name." and the like, included) or past the line bound is
// an error and nothing is attached.
func ParseDeathRecap(lines []string) ([]DeathRecapLine, error) {
	if len(lines) > MaxDeathRecapLines {
		return nil, fmt.Errorf("deathrecap: %d lines, over the %d-line bound", len(lines), MaxDeathRecapLines)
	}
	trimmed := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		trimmed = append(trimmed, t)
	}
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("deathrecap: the server sent no reply")
	}
	if deathRecapNoRecordingRe.MatchString(trimmed[0]) {
		return nil, ErrDeathRecapNone
	}
	start := -1
	var hm []string
	for i, l := range trimmed {
		if m := deathRecapHeaderRe.FindStringSubmatch(l); m != nil {
			start, hm = i, m
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("deathrecap: no header line: %.60q", trimmed[0])
	}
	out := []DeathRecapLine{{Kind: DeathRecapHeader, Header: trimmed[start], ID: hm[1], Victim: hm[2]}}
	for _, l := range trimmed[start+1:] {
		if l == "no casts recorded" {
			out = append(out, DeathRecapLine{Kind: DeathRecapNoCasts})
			continue
		}
		if m := deathRecapFrameRe.FindStringSubmatch(l); m != nil {
			offset, err1 := strconv.ParseFloat(m[1], 64)
			x, err2 := strconv.ParseFloat(m[2], 64)
			y, err3 := strconv.ParseFloat(m[3], 64)
			z, err4 := strconv.ParseFloat(m[4], 64)
			hp, err5 := strconv.ParseUint(m[5], 10, 64)
			maxHP, err6 := strconv.ParseUint(m[6], 10, 64)
			pct, err7 := strconv.ParseUint(m[7], 10, 64)
			if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil || err7 != nil {
				continue
			}
			out = append(out, DeathRecapLine{Kind: DeathRecapFrame, OffsetS: offset, X: x, Y: y, Z: z, HP: hp, MaxHP: maxHP, HPPct: pct})
			continue
		}
		if m := deathRecapCastRe.FindStringSubmatch(l); m != nil {
			offset, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			out = append(out, DeathRecapLine{Kind: DeathRecapCast, OffsetS: offset, Caster: m[2], Spell: m[3], Target: m[4]})
			continue
		}
		// A foreign system line among the recap's own: skipped.
	}
	return out, nil
}

// deathRecapStops answers whether the hook's error means the server will not
// answer `.deathrecap` for anyone this session: a core without the command (the
// collector's unknown-command refusal) or an account without its permission (the
// line came back as ordinary chat). Asking again would only repeat it.
func deathRecapStops(err error) (string, bool) {
	switch {
	case errors.Is(err, command.ErrUnknownCommand):
		return "the server has no .deathrecap command", true
	case errors.Is(err, command.ErrNoPermission):
		return "the account has no permission for .deathrecap", true
	}
	return "", false
}

// deathRecapReq is one death's request: keyed by the death itself (its index in
// the watch's death log, the bot and the event's second), never by the bot.
type deathRecapReq struct {
	bot       string
	atS       int
	idx       int
	tries     int
	queued    time.Time
	notBefore time.Time
}

// deathRecapState is the hook's memory: the queued requests, one per death,
// the last recap id attached per bot (a reply that repeats it is the earlier
// death's, not the new one's), and the stop (a missing command or permission
// ends all asking for the session, with one log line).
type deathRecapState struct {
	stopped string
	queue   []*deathRecapReq
	lastID  map[string]string
}

// deathRecapQueue is the watch's bug-hunter hook, called from the event poll
// when the ring records a death of a party member: it only queues the request
// for the death just appended to the death log. The request itself goes out
// off the poll (deathRecapPump), so a wipe never stalls the watch loop.
func (r *Run) deathRecapQueue(w *watchRun, bot string, idx int, atS int) {
	if w.deathRecap == nil {
		w.deathRecap = &deathRecapState{}
	}
	s := w.deathRecap
	if s.stopped != "" || idx < 0 || idx >= len(w.deaths) {
		return
	}
	for _, q := range s.queue {
		if q.idx == idx {
			return
		}
	}
	s.queue = append(s.queue, &deathRecapReq{bot: bot, atS: atS, idx: idx, queued: r.now()})
}

// deathRecapSet attaches a death's recap text to that death only.
func (w *watchRun) deathRecapSet(q *deathRecapReq, text []string) {
	if q.idx >= 0 && q.idx < len(w.deaths) {
		w.deaths[q.idx].Recap = append([]string(nil), text...)
	}
}

// deathRecapPump runs at most one queued request per call, after the poll's
// event handling and the rescue decisions. The core records only the last death
// of a player, so when a bot has several deaths queued only the newest is asked
// and the earlier ones are marked not recorded; the recap attaches to the death
// that was asked about and to no other. final (the watch is ending) ignores the
// retry gap and gives each request one try.
func (r *Run) deathRecapPump(ctx context.Context, w *watchRun, final bool) {
	s := w.deathRecap
	if s == nil || len(s.queue) == 0 {
		return
	}
	if r.lost || r.stopReason != "" {
		s.queue = nil
		return
	}
	now := r.now()
	var keep []*deathRecapReq
	for _, q := range s.queue {
		newer := false
		for _, o := range s.queue {
			if o != q && strings.EqualFold(o.bot, q.bot) && o.idx > q.idx {
				newer = true
				break
			}
		}
		switch {
		case newer:
			w.deathRecapSet(q, []string{"not recorded"})
		case now.Sub(q.queued) > DeathRecapQueueTTL:
			w.deathRecapSet(q, []string{"not recorded"})
		default:
			keep = append(keep, q)
		}
	}
	s.queue = keep
	var req *deathRecapReq
	for _, q := range s.queue {
		if final || !now.Before(q.notBefore) {
			req = q
			break
		}
	}
	if req == nil {
		return
	}
	lines, err := r.deathRecap(ctx, req.bot)
	r.deathRecapDone(w, req, lines, err, final)
}

// deathRecapDone applies one request's answer: attach, give up, or retry later.
func (r *Run) deathRecapDone(w *watchRun, req *deathRecapReq, lines []DeathRecapLine, err error, final bool) {
	s := w.deathRecap
	drop := func() {
		for i, q := range s.queue {
			if q == req {
				s.queue = append(s.queue[:i], s.queue[i+1:]...)
				return
			}
		}
	}
	if err == nil && s.lastID != nil && s.lastID[strings.ToLower(req.bot)] == lines[0].ID {
		err = fmt.Errorf("deathrecap: recap #%s is the earlier death's, not this one's", lines[0].ID)
	}
	switch {
	case err == nil:
		text := make([]string, 0, len(lines))
		for _, l := range lines {
			text = append(text, l.String())
		}
		if s.lastID == nil {
			s.lastID = map[string]string{}
		}
		s.lastID[strings.ToLower(req.bot)] = lines[0].ID
		w.deathRecapSet(req, text)
		drop()
		r.logf("watch: deathrecap: %s: %d line(s)", req.bot, len(text))
	case errors.Is(err, errDeathRecapEnded):
		s.queue = nil
	case errors.Is(err, ErrDeathRecapNone):
		w.deathRecapSet(req, []string{"no recording"})
		drop()
		r.logf("watch: deathrecap: the server has no recording for %s", req.bot)
	default:
		if why, stop := deathRecapStops(err); stop {
			s.stopped = why
			s.queue = nil
			r.logf("watch: deathrecap: %s; not asking again", why)
			return
		}
		req.tries++
		if final || req.tries >= DeathRecapTries {
			w.deathRecapSet(req, []string{"not recorded"})
			drop()
			r.logf("watch: deathrecap: %v; giving up on this death", err)
			return
		}
		req.notBefore = r.now().Add(time.Duration(req.tries) * DeathRecapRetryGap)
		r.logf("watch: deathrecap: %v; trying again later", err)
	}
}

// deathRecapFlush asks what is still queued as the watch ends: one try each,
// at most a few, so the last poll's deaths are not left unasked.
func (r *Run) deathRecapFlush(ctx context.Context, w *watchRun) {
	for i := 0; i < 5; i++ {
		if w.deathRecap == nil || len(w.deathRecap.queue) == 0 {
			return
		}
		r.deathRecapPump(ctx, w, true)
	}
}
