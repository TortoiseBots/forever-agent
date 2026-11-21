package tbcmd

// The typed state and threat models: every key the reply grammar documents gets a field, the grammar is version 2
// (see v2.go), and a report is only accepted with its begin and end markers and a count that matches. A required
// key that is missing is ErrBadReport; a key the module adds later is kept in Extra. Optional keys carry a
// presence bit (or a pointer) so "not known" is never a zero the caller could act on.

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// BotState is one bot's state line. Every field here is a required key of the state record.
type BotState struct {
	Name       string
	Role       string // tank, healer, dps; empty when no role is set
	RoleSource string // forced, auto
	Class      string
	Level      int

	HP    int
	HPMax int
	HPPct int

	PowerType string // mana, rage, energy, ...
	Power     int
	PowerMax  int
	// ManaPct is the mana percent; HasManaPct is false for a bot that has no mana (the key absent).
	ManaPct    int
	HasManaPct bool

	State    string // combat, non-combat, dead
	InCombat bool

	// TargetGUID is empty when the bot has no target. The entry and health are absent keys when not known.
	TargetGUID     string
	TargetEntry    int
	HasTargetEntry bool
	TargetHPPct    int
	HasTargetHPPct bool
	VictimGUID     string

	Hold bool
	Move string // follow, stay; empty when the bot does not move

	Stuck  StuckState
	Engine bool
	// NoEngine is the typed "engine off" state: `engine=no`, or `stuck=noengine` while the engine is being attached.
	// Such a bot runs no engine tick, so it carries no strategy lists (co= and nc= are absent keys); the driver reads
	// it as not ready with the reason noengine, never as an unreadable report.
	NoEngine bool

	X, Y, Z float64
	Map     uint32
	// Instance is inst; the corpse position and map are present only for a dead bot.
	Instance     uint32
	CorpseX      float64
	CorpseY      float64
	CorpseZ      float64
	CorpseMap    uint32
	HasCorpse    bool
	HasCorpseMap bool

	DistLeader Distance
	DistMaster Distance

	Order     OrderState
	LastOrder string

	CombatStrategies    []string
	NonCombatStrategies []string

	CastQueued    int
	CastDelivered int
	CastDropped   int
	CastPending   int

	Eating      bool
	Drinking    bool
	Resting     bool
	RestReasons []string

	// Extra holds every field this model does not know, decoded. Unknown keys are not an error.
	Extra map[string]string
}

// StuckState is the `stuck` field: empty (not stuck), blocked:<reason>, away:<seconds left>, or noengine.
type StuckState struct {
	Raw     string
	Kind    string // "", blocked, away, noengine, or the unknown word
	Reason  string // blocked
	Seconds int    // away
}

// Distance is dl/dm: a number of yards, self or othermap; Has is false when the key was absent.
type Distance struct {
	// Has says whether the line carried the key: a missing distance is not known, which is not zero yards.
	Has   bool
	Yards float64
	Kind  string // self, othermap, or "" for a number
}

// OrderState is the `ord` field: <seq>:<queued|ok|fail:<code>>, or the empty value for no order.
type OrderState struct {
	Raw    string
	Seq    int
	Status string // queued, ok, fail; empty for no order
	Code   string // fail's error code
}

// StateReport is a whole state answer: the bot records and the end line. Count is the end line's count (the lines
// between the markers, continuation lines included); Records is the number of bot records; len(Bots) is Records.
type StateReport struct {
	Bots    []BotState
	Count   int
	Records int
	Resting int
	Ready   bool // the end line's ready=yes
	Why     []string
	Target  string
	End     map[string]string
	Raw     []string
}

// State returns the bots of one state answer. Use StateReport for the end line's count and resting number.
func (c *Client) State(ctx context.Context, target string) ([]BotState, error) {
	rep, err := c.StateReport(ctx, target)
	if err != nil {
		return nil, err
	}
	return rep.Bots, nil
}

// stateKeys are the keys parseStateRecord types; anything else goes to Extra.
var stateKeys = map[string]bool{
	"bot": true, "role": true, "rolesrc": true, "class": true, "level": true,
	"hp": true, "hpmax": true, "hppct": true, "power": true, "pw": true, "pwmax": true, "manapct": true,
	"st": true, "incombat": true, "tgt": true, "tgtentry": true, "tgthp": true, "victim": true,
	"hold": true, "mv": true, "stuck": true, "engine": true,
	"pos": true, "map": true, "inst": true, "corpse": true, "corpsemap": true, "dl": true, "dm": true,
	"ord": true, "lastorder": true, "co": true, "nc": true,
	"cast_queued": true, "cast_delivered": true, "cast_dropped": true, "cast_pending": true,
	"eating": true, "drinking": true, "resting": true, "rest_why": true,
}

// StateReport reads one state answer and checks it: both markers, the end line's count, the resting number, the
// ready flag, and one record per bot with every required key.
func (c *Client) StateReport(ctx context.Context, target string) (StateReport, error) {
	words, err := targetWords(target)
	if err != nil {
		return StateReport{}, err
	}
	rep, err := c.call(ctx, strings.Join(append([]string{".tb", "state"}, words...), " "), "state-end")
	if err != nil {
		return StateReport{}, err
	}
	rep2, err := parseReport(rep, "state-begin", "state-end", "state", "bot")
	if err != nil {
		return StateReport{}, err
	}
	end := rep2.End
	out := StateReport{
		Raw:  append([]string(nil), rep.Raw...),
		End:  make(map[string]string, len(end.keys)),
		Bots: make([]BotState, 0, len(rep2.Records)),
	}
	for _, k := range end.keys {
		if s, err := decodePart(end.values[k]); err == nil {
			out.End[k] = s
		}
	}
	if out.Count, err = end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Records, err = end.requireInt("records", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Records != len(rep2.Records) {
		return out, fmt.Errorf("%w: state-end records=%d, %d bot line(s)", ErrBadReport, out.Records, len(rep2.Records))
	}
	if out.Resting, err = end.requireInt("resting", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Ready, err = end.requireBool("ready"); err != nil {
		return out, err
	}
	if out.Target, err = end.requireText("target"); err != nil {
		return out, err
	}
	why, hasWhy, err := end.optionalList("why")
	if err != nil {
		return out, err
	}
	if hasWhy {
		out.Why = why
	}
	if out.Ready && hasWhy {
		return out, fmt.Errorf("%w: state-end ready=yes carries why=%v", ErrBadReport, why)
	}
	if !out.Ready && (!hasWhy || len(why) == 0) {
		return out, fmt.Errorf("%w: state-end ready=no has no non-empty why=", ErrBadReport)
	}
	for _, rec := range rep2.Records {
		b, err := parseStateRecord(rec)
		if err != nil {
			return out, err
		}
		out.Bots = append(out.Bots, b)
	}
	// The end line's resting number is the count of lines with resting=yes; a disagreement is a report that must
	// not drive a party (the module and the lines say different things).
	yes := 0
	for _, b := range out.Bots {
		if b.Resting {
			yes++
		}
	}
	if out.Resting != yes {
		return out, fmt.Errorf("%w: state-end resting=%d, %d line(s) report resting=yes", ErrBadReport, out.Resting, yes)
	}
	// Ready implies rested: a report that calls a party with a resting member ready is inconsistent. The other
	// direction is not a rule: ready=no with resting=0 is a party that is alive and up but far from its master or
	// blocked, and its why= names those reasons.
	if out.Ready && out.Resting != 0 {
		return out, fmt.Errorf("%w: state-end ready=yes with resting=%d", ErrBadReport, out.Resting)
	}
	return out, nil
}

func parseStateRecord(rec record) (BotState, error) {
	b := BotState{Extra: make(map[string]string)}
	var err error
	if b.Name, err = rec.requireText("bot"); err != nil {
		return b, err
	}
	if b.Role, err = requiredWordOrEmpty(rec, "role", "tank", "healer", "dps", "none"); err != nil {
		return b, err
	}
	if b.Role == "none" { // the module prints role=none for a bot with no role
		b.Role = ""
	}
	if b.RoleSource, err = requiredWord(rec, "rolesrc", "forced", "auto"); err != nil {
		return b, err
	}
	if b.Class, err = rec.requireText("class"); err != nil {
		return b, err
	}
	if b.Level, err = rec.requireInt("level", 1, 255); err != nil {
		return b, err
	}
	if b.HP, err = rec.requireInt("hp", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.HPMax, err = rec.requireInt("hpmax", 1, math.MaxInt32); err != nil {
		return b, err
	}
	if b.HP > b.HPMax {
		return b, fmt.Errorf("%w: hp=%d over hpmax=%d", ErrBadValue, b.HP, b.HPMax)
	}
	if b.HPPct, err = rec.requireInt("hppct", 0, 100); err != nil {
		return b, err
	}
	if b.PowerType, err = rec.requireText("power"); err != nil {
		return b, err
	}
	if b.Power, err = rec.requireInt("pw", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.PowerMax, err = rec.requireInt("pwmax", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.ManaPct, b.HasManaPct, err = rec.optionalInt("manapct", 0, 100); err != nil {
		return b, err
	}
	if b.State, err = requiredWord(rec, "st", "combat", "non-combat", "dead"); err != nil {
		return b, err
	}
	if b.InCombat, err = rec.requireBool("incombat"); err != nil {
		return b, err
	}
	if b.TargetGUID, err = requiredGUID(rec, "tgt"); err != nil {
		return b, err
	}
	// The command reference marks these two "empty when none": the module writes the engine's target entry and
	// health percent empty when the bot has no target. Every other optional number is absent when unknown.
	if b.TargetEntry, b.HasTargetEntry, err = rec.optionalIntEmpty("tgtentry", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.TargetHPPct, b.HasTargetHPPct, err = rec.optionalIntEmpty("tgthp", 0, 100); err != nil {
		return b, err
	}
	if b.VictimGUID, err = requiredGUID(rec, "victim"); err != nil {
		return b, err
	}
	if b.Hold, err = rec.requireBool("hold"); err != nil {
		return b, err
	}
	if b.Move, err = requiredWordOrEmpty(rec, "mv", "follow", "stay"); err != nil {
		return b, err
	}
	if b.Stuck, err = parseStuck(rec); err != nil {
		return b, err
	}
	if b.Engine, err = rec.requireBool("engine"); err != nil {
		return b, err
	}
	b.NoEngine = !b.Engine || b.Stuck.Kind == "noengine"
	if b.X, b.Y, b.Z, err = parsePos(rec, "pos"); err != nil {
		return b, err
	}
	if b.Map, err = rec.requireUint32("map"); err != nil {
		return b, err
	}
	if b.Instance, err = rec.requireUint32("inst"); err != nil {
		return b, err
	}
	if b.CorpseX, b.CorpseY, b.CorpseZ, b.HasCorpse, err = optionalPos(rec, "corpse"); err != nil {
		return b, err
	}
	if cm, ok, err := rec.optionalInt("corpsemap", 0, math.MaxInt32); err != nil {
		return b, err
	} else if ok {
		b.CorpseMap, b.HasCorpseMap = uint32(cm), true
	}
	if b.HasCorpse != b.HasCorpseMap {
		return b, fmt.Errorf("%w: corpse= and corpsemap= must appear together", ErrBadReport)
	}
	if b.DistLeader, err = parseDistance(rec, "dl"); err != nil {
		return b, err
	}
	if b.DistMaster, err = parseDistance(rec, "dm"); err != nil {
		return b, err
	}
	if b.Order, err = parseOrderState(rec); err != nil {
		return b, err
	}
	if b.LastOrder, err = rec.requireText("lastorder"); err != nil {
		return b, err
	}
	// A bot with the engine off has no strategy lists and writes neither key; every other bot carries both, and a
	// missing one there is a report this client must not act on.
	if b.NoEngine {
		if b.CombatStrategies, _, err = rec.optionalList("co"); err != nil {
			return b, err
		}
		if b.NonCombatStrategies, _, err = rec.optionalList("nc"); err != nil {
			return b, err
		}
	} else {
		if b.CombatStrategies, err = rec.list("co"); err != nil {
			return b, err
		}
		if b.NonCombatStrategies, err = rec.list("nc"); err != nil {
			return b, err
		}
	}
	if b.CastQueued, err = rec.requireInt("cast_queued", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.CastDelivered, err = rec.requireInt("cast_delivered", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.CastDropped, err = rec.requireInt("cast_dropped", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.CastPending, err = rec.requireInt("cast_pending", 0, math.MaxInt32); err != nil {
		return b, err
	}
	if b.Eating, err = rec.requireBool("eating"); err != nil {
		return b, err
	}
	if b.Drinking, err = rec.requireBool("drinking"); err != nil {
		return b, err
	}
	if b.Resting, err = rec.requireBool("resting"); err != nil {
		return b, err
	}
	if b.RestReasons, err = rec.list("rest_why"); err != nil {
		return b, err
	}
	for _, k := range rec.keys {
		if !stateKeys[k] {
			s, err := decodePart(rec.values[k])
			if err != nil {
				return b, rec.bad(k, err)
			}
			b.Extra[k] = s
		}
	}
	return b, nil
}

// requiredWord decodes a required value and refuses a word outside the documented set.
func requiredWord(rec record, key string, words ...string) (string, error) {
	return wordValue(rec, key, false, words...)
}

// requiredWordOrEmpty is requiredWord with the empty value (the grammar's "no value") accepted.
func requiredWordOrEmpty(rec record, key string, words ...string) (string, error) {
	return wordValue(rec, key, true, words...)
}

func wordValue(rec record, key string, allowEmpty bool, words ...string) (string, error) {
	s, err := rec.requireText(key)
	if err != nil {
		return "", err
	}
	if s == "" && allowEmpty {
		return "", nil
	}
	for _, w := range words {
		if s == w {
			return s, nil
		}
	}
	return "", fmt.Errorf("%w: %s=%q is not one of %s", ErrBadValue, key, s, strings.Join(words, "/"))
}

// requiredGUID reads a required GUID text: the empty value, or 32 upper-case hex digits.
func requiredGUID(rec record, key string) (string, error) {
	s, err := rec.requireText(key)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", nil
	}
	if len(s) != 32 {
		return "", fmt.Errorf("%w: %s=%q is not a GUID", ErrBadValue, key, s)
	}
	for i := range len(s) {
		if !isUpperHex(s[i]) {
			return "", fmt.Errorf("%w: %s=%q is not upper-case hex", ErrBadValue, key, s)
		}
	}
	return s, nil
}

// parsePos reads one `pos=`/`corpse=` value: three comma-separated decimals, each with at most two fraction digits.
func parsePos(rec record, key string) (x, y, z float64, err error) {
	v, ok := rec.rawValue(key)
	if !ok {
		return 0, 0, 0, rec.missing(key)
	}
	parts := strings.Split(v, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("%w: %s=%q is not x,y,z", ErrBadValue, key, v)
	}
	var out [3]float64
	for i, p := range parts {
		s, err := decodePart(p)
		if err != nil {
			return 0, 0, 0, rec.bad(key, err)
		}
		f, err := rec.floatValue(key, s)
		if err != nil || !validCoord(s) {
			return 0, 0, 0, fmt.Errorf("%w: %s=%q is not a coordinate", ErrBadValue, key, v)
		}
		out[i] = f
	}
	return out[0], out[1], out[2], nil
}

// optionalPos reads `corpse=`: absent, or the empty value the module writes when the bot has no corpse, is not
// known (and not a position at the origin). The pair rule with corpsemap= then holds on presence, not on text.
func optionalPos(rec record, key string) (x, y, z float64, present bool, err error) {
	v, ok := rec.rawValue(key)
	if !ok || v == "" {
		return 0, 0, 0, false, nil
	}
	x, y, z, err = parsePos(rec, key)
	return x, y, z, true, err
}

// validCoord reports a decimal with an optional sign and at most two fraction digits.
func validCoord(s string) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return false
	}
	if i == len(s) {
		return true
	}
	if s[i] != '.' {
		return false
	}
	i++
	start = i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > start && i-start <= 2 && i == len(s)
}

func parseStuck(rec record) (StuckState, error) {
	v, err := rec.requireText("stuck")
	if err != nil {
		return StuckState{}, err
	}
	s := StuckState{Raw: v}
	switch {
	case v == "":
	case v == "noengine":
		s.Kind = "noengine"
	case strings.HasPrefix(v, "blocked:"):
		s.Kind = "blocked"
		s.Reason = v[len("blocked:"):]
		if s.Reason == "" {
			return s, fmt.Errorf("%w: stuck=blocked: has no reason", ErrBadValue)
		}
	case strings.HasPrefix(v, "away:"):
		s.Kind = "away"
		n, err := command.Atoi("away", v[len("away:"):])
		if err != nil || n < 0 {
			return s, fmt.Errorf("%w: stuck=%q has no seconds", ErrBadValue, v)
		}
		s.Seconds = n
	default:
		// A word this client does not know: kept, not an error (a newer module may add one).
		s.Kind = v
	}
	return s, nil
}

func parseDistance(rec record, key string) (Distance, error) {
	v, ok, err := rec.text(key)
	if err != nil || !ok {
		return Distance{}, err
	}
	switch v {
	case "":
		return Distance{}, nil
	case "self", "othermap":
		return Distance{Has: true, Kind: v}, nil
	}
	f, err := rec.floatValue(key, v)
	if err != nil {
		return Distance{}, err
	}
	if f < 0 {
		return Distance{}, fmt.Errorf("%w: %s=%q is negative", ErrBadValue, key, v)
	}
	return Distance{Has: true, Yards: f}, nil
}

// parseOrderState reads the order part of a state line, or the empty value for no order. The module writes it as one
// item of two parts (Rec::P): the sequence and the result, each encoded on its own, so a failed order's result
// `fail:<code>` arrives as `<seq>:fail%3A<code>` and is the second part after decoding. The parts are split on ':'
// before any decoding; a third raw part (`<seq>:fail:<code>`, an older writer) is read the same way.
func parseOrderState(rec record) (OrderState, error) {
	v, err := rec.requireText("ord")
	if err != nil {
		return OrderState{}, err
	}
	o := OrderState{Raw: v}
	if v == "" {
		return o, nil
	}
	parts, err := rec.parts("ord")
	if err != nil {
		return o, err
	}
	if len(parts) != 2 && len(parts) != 3 {
		return o, fmt.Errorf("%w: ord=%q is not seq:status[:code]", ErrBadValue, v)
	}
	n, err := command.Atoi("ord seq", parts[0])
	if err != nil || n < 0 {
		return o, fmt.Errorf("%w: ord=%q has no sequence", ErrBadValue, v)
	}
	o.Seq = n
	if len(parts) == 2 {
		// The module's own form: the result is one part, `fail:<code>` decoded from `fail%3A<code>`.
		if status, code, ok := strings.Cut(parts[1], ":"); ok {
			parts = []string{parts[0], status, code}
		}
	}
	switch parts[1] {
	case "queued", "ok":
		if len(parts) != 2 {
			return o, fmt.Errorf("%w: ord=%q has a code without fail", ErrBadValue, v)
		}
		o.Status = parts[1]
	case "fail":
		if len(parts) != 3 || parts[2] == "" {
			return o, fmt.Errorf("%w: ord=%q has fail without a code", ErrBadValue, v)
		}
		o.Status = "fail"
		o.Code = parts[2]
	default:
		return o, fmt.Errorf("%w: ord=%q has status %q", ErrBadValue, v, parts[1])
	}
	return o, nil
}

// WaitRested polls State until the end line reports ready (or, when a module omits ready, every bot reports
// resting=no) or the timeout expires. The poll interval is Client.Poll (2 s by default); the returned report is
// the last one it read.
func (c *Client) WaitRested(ctx context.Context, target string, timeout time.Duration) (StateReport, error) {
	poll := c.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last StateReport
	for {
		rep, err := c.StateReport(waitCtx, target)
		if err != nil {
			return last, err
		}
		last = rep
		if len(rep.Bots) > 0 && rep.Ready && rep.Resting == 0 {
			return rep, nil
		}
		select {
		case <-waitCtx.Done():
			return last, fmt.Errorf("%w after %s: %d of %d bot(s) resting", ErrNotRested, timeout, rep.Resting, len(rep.Bots))
		case <-time.After(poll):
		}
	}
}
