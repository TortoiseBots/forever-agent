package tbcmd

// The commander binding card: `.tb commander status [<commander>]` reads the
// module's binding table (§11) beside the run table (§10). One card per
// commander: the binding line, the members, the commander's last orders, and
// the run record when one of its bots is running. The line is
// `.tb commander status [<name>]`; the markers are commander-begin and
// commander-end (the end key is bindings=, not records=). A `run` record
// attaches to the card before it; a `member`/`order` whose cmd= does not name
// the open card is a bad report, never a second card's line. Every declared
// total is reconciled with the records it counts, two cards for one commander
// are a bad report, and so is one bot listed twice on a card: a repeated
// member record would satisfy bots= while a bound bot went missing.

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// XYZ is a map position: the commander's own spot (binding map/pos) or one
// member's (zone/pos). A nil pointer means the record carries no pos= (§11
// writes it empty when the spot is not known).
type XYZ struct {
	X, Y, Z float64
}

// Card is one commander's binding card: who leads, who is bound, the last
// orders, and the run when one runs. Orders is the cap the module keeps per
// card (8); Group is the lead's live party counter (the module stores none).
// Pos is the commander's own spot when the line carries map/pos, nil when
// not known; Map has the same presence (HasMap false when absent). Run is
// the card's run record (§10), nil when no run runs.
type Card struct {
	Cmd       string
	CGUID     string
	Online    bool
	Lead      string
	Group     int
	HasGroup  bool
	Bound     int
	Bots      int
	System    bool
	InParty   bool
	Raid      bool
	PlayerLed bool
	BoundAt   int
	Orders    int
	Map       uint32
	HasMap    bool
	Pos       *XYZ
	Members   []CardMember
	Log       []CardOrder
	Run       *RunState
	Extra     map[string]string
}

// CardMember is one bound bot. The lead comes first; Sub is the raid
// subgroup and HasSub false when the line carries no sub=.
type CardMember struct {
	Bot      string
	GUID     string
	Class    string
	Role     string
	Level    int
	HP       int
	MaxHP    int
	Power    int
	MaxPower int
	PowerTyp string // ptype: rage, mana, ...
	Alive    bool
	Lead     bool
	Map      uint32
	Zone     int
	Pos      XYZ
	Sub      int
	HasSub   bool
	Extra    map[string]string
}

// CardOrder is one logged order, oldest first. OK says the order queued;
// Detail and Result are the module's own words (their text is decoded).
type CardOrder struct {
	At     int
	Verb   string
	Detail string
	OK     bool
	Result string
	Extra  map[string]string
}

// CommanderReport is a whole status answer: one card per commander (no card
// is an empty slice, not an error), the end line's bindings count, and the
// raw lines for diagnostics.
type CommanderReport struct {
	Cards    []Card
	Bindings int
	Raw      []string
}

// commanderRecordKeys are the binding record's typed keys; anything else goes
// to Extra. cmd= is the card's identity too.
var commanderRecordKeys = map[string]bool{
	"cmd": true, "cguid": true, "online": true, "lead": true, "group": true,
	"bound": true, "bots": true, "system": true, "inparty": true, "raid": true,
	"playerled": true, "boundat": true, "orders": true, "map": true, "pos": true,
}

// commanderMemberKeys are the member record's typed keys.
var commanderMemberKeys = map[string]bool{
	"cmd": true, "bot": true, "guid": true, "class": true, "role": true,
	"level": true, "hp": true, "maxhp": true, "power": true, "maxpower": true,
	"ptype": true, "alive": true, "lead": true, "map": true, "zone": true,
	"pos": true, "sub": true,
}

// commanderOrderKeys are the order record's typed keys.
var commanderOrderKeys = map[string]bool{
	"cmd": true, "at": true, "verb": true, "detail": true, "ok": true, "result": true,
}

// CommanderStatus reads the binding cards: `.tb commander status [<name>]`.
// An empty commander reads every card; a name reads that commander's card, and
// the module answers `error code=no-binding` when it commands no bots. The
// run's own card (a run whose tank nobody commands) carries system=yes and
// names the tank in cmd=.
func (c *Client) CommanderStatus(ctx context.Context, commander string) (CommanderReport, error) {
	line := ".tb commander status"
	if strings.TrimSpace(commander) != "" {
		name, err := chat.NormaliseName(commander)
		if err != nil {
			return CommanderReport{}, fmt.Errorf("tbcmd: commander status: %w", err)
		}
		line += " " + name
	}
	// A card is a binding line plus five members, eight orders and a run
	// record at most; the collector caps a reply at 256 lines.
	rep, err := c.callMax(ctx, line, "commander-end", 256)
	if err != nil {
		return CommanderReport{}, err
	}
	return parseCommander(rep)
}

// parseCommander reads a commander-begin/commander-end reply. The records are
// four kinds: `binding` (one card), `member` and `order` (the open card's
// lines, checked by cmd=), and the trailing `run` record (§10) that attaches
// to the card before it. records= (required) counts every record between the
// markers, as the module's own end lines show (§11: count=12 records=12 with
// the run); bindings= counts the cards; each card's bots= and orders= must
// match the distinct member and the order records, and one commander has one
// card with one record per member.
func parseCommander(rep *command.Reply) (CommanderReport, error) {
	out := CommanderReport{}
	if rep == nil || !rep.Complete || len(rep.Lines) < 2 {
		return out, fmt.Errorf("%w: the reply ended without its commander-end line", ErrBadReport)
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
	if begin.kind != "commander-begin" {
		return out, fmt.Errorf("%w: the reply starts with %q, not %q (a missing begin marker)", ErrBadReport, begin.kind, "commander-begin")
	}
	if len(begin.values) > 0 {
		return out, fmt.Errorf("%w: commander-begin carries a field", ErrBadReport)
	}
	end, err := decodeLine(rep.Lines[len(rep.Lines)-1])
	if err != nil {
		return out, err
	}
	if end.kind != "commander-end" {
		return out, fmt.Errorf("%w: the reply ends with %q, not %q", ErrBadReport, end.kind, "commander-end")
	}
	out.Raw = append([]string(nil), rep.Raw...)
	if out.Bindings, err = end.requireInt("bindings", 0, math.MaxInt32); err != nil {
		return out, err
	}
	count, err := end.requireInt("count", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	if between := len(rep.Lines) - 2; count != between {
		return out, fmt.Errorf("%w: commander-end count=%d, %d line(s) between the markers", ErrBadReport, count, between)
	}
	wantRecords, err := end.requireInt("records", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	var recs []record
	for i := 1; i+1 < len(rep.Lines); i++ {
		rec, err := decodeLine(rep.Lines[i])
		if err != nil {
			return out, err
		}
		cont := strings.HasSuffix(rec.kind, "+")
		base := strings.TrimSuffix(rec.kind, "+")
		switch base {
		case "binding", "member", "order", "run":
		default:
			return out, fmt.Errorf("%w: a %q line inside a commander reply", ErrBadReport, rec.kind)
		}
		if !cont {
			recs = append(recs, rec)
			continue
		}
		// A continuation repeats the record's identity key as its first
		// key: cmd= for binding/member/order, lead= for the run record.
		if len(recs) == 0 {
			return out, fmt.Errorf("%w: a %s+ line before any %s line", ErrBadReport, base, base)
		}
		prev := &recs[len(recs)-1]
		if strings.TrimSuffix(prev.kind, "+") != base {
			return out, fmt.Errorf("%w: a %s+ line follows a %s line", ErrBadReport, base, prev.kind)
		}
		id := commanderIdentity(base)
		if len(rec.keys) == 0 || rec.keys[0] != id {
			return out, fmt.Errorf("%w: %s+ must repeat %s= as its first key", ErrBadReport, base, id)
		}
		if rec.values[id] != prev.values[id] {
			return out, fmt.Errorf("%w: %s+ for %q follows %q", ErrBadReport, base, rec.values[id], prev.values[id])
		}
		for _, k := range rec.keys {
			if k == id {
				continue
			}
			if prev.has(k) {
				return out, fmt.Errorf("%w: %s+ repeats %s", ErrBadReport, base, k)
			}
			prev.values[k] = rec.values[k]
			prev.keys = append(prev.keys, k)
		}
	}
	if len(recs) != wantRecords {
		return out, fmt.Errorf("%w: commander-end records=%d, %d record(s) between the markers", ErrBadReport, wantRecords, len(recs))
	}
	var open *Card
	seen := make(map[string]bool, len(recs))
	var membersSeen map[string]bool
	flush := func() error {
		if open == nil {
			return nil
		}
		if err := checkCommanderCard(*open); err != nil {
			return err
		}
		out.Cards = append(out.Cards, *open)
		open = nil
		return nil
	}
	for _, rec := range recs {
		switch rec.kind {
		case "binding":
			if err := flush(); err != nil {
				return out, err
			}
			card, err := parseCommanderCard(rec)
			if err != nil {
				return out, err
			}
			// §11: one card per commander.
			key := strings.ToLower(card.Cmd)
			if seen[key] {
				return out, fmt.Errorf("%w: a second card for commander %q", ErrBadReport, card.Cmd)
			}
			seen[key] = true
			open = &card
			membersSeen = make(map[string]bool, len(recs))
		case "member":
			if open == nil {
				return out, fmt.Errorf("%w: a member line before any binding line", ErrBadReport)
			}
			cmd, err := rec.requireText("cmd")
			if err != nil {
				return out, err
			}
			if !strings.EqualFold(cmd, open.Cmd) {
				return out, fmt.Errorf("%w: member for %q follows the %q card", ErrBadReport, cmd, open.Cmd)
			}
			m, err := parseCommanderMember(rec)
			if err != nil {
				return out, err
			}
			// §11: one record per bound bot in the world. A bot listed
			// twice would let the card's bots= pass with another bot
			// missing, so the repeat is a bad report, not a member.
			key := strings.ToLower(m.Bot)
			if membersSeen[key] {
				return out, fmt.Errorf("%w: the %q card lists %q twice", ErrBadReport, open.Cmd, m.Bot)
			}
			membersSeen[key] = true
			open.Members = append(open.Members, m)
		case "order":
			if open == nil {
				return out, fmt.Errorf("%w: an order line before any binding line", ErrBadReport)
			}
			cmd, err := rec.requireText("cmd")
			if err != nil {
				return out, err
			}
			if !strings.EqualFold(cmd, open.Cmd) {
				return out, fmt.Errorf("%w: order for %q follows the %q card", ErrBadReport, cmd, open.Cmd)
			}
			o, err := parseCommanderOrder(rec)
			if err != nil {
				return out, err
			}
			open.Log = append(open.Log, o)
		case "run":
			if open == nil {
				return out, fmt.Errorf("%w: a run record before any binding line", ErrBadReport)
			}
			st, err := parseRunState(rec)
			if err != nil {
				return out, err
			}
			cmd, hasCmd, err := rec.text("cmd")
			if err != nil {
				return out, err
			}
			if !hasCmd || cmd == "" {
				return out, rec.missing("cmd")
			}
			if !strings.EqualFold(cmd, open.Cmd) {
				return out, fmt.Errorf("%w: run for %q follows the %q card", ErrBadReport, cmd, open.Cmd)
			}
			if open.Run != nil {
				return out, fmt.Errorf("%w: two run records for the %q card", ErrBadReport, open.Cmd)
			}
			open.Run = &st
		}
	}
	if err := flush(); err != nil {
		return out, err
	}
	if out.Bindings != len(out.Cards) {
		return out, fmt.Errorf("%w: commander-end bindings=%d, %d card(s) between the markers", ErrBadReport, out.Bindings, len(out.Cards))
	}
	return out, nil
}

// checkCommanderCard reconciles one card's declared totals with the records
// that carried them: §11 writes one member record per bound bot in the world,
// so bots= is the number of distinct members, bound= (which counts offline
// bots too) is at least bots=, and orders= is the order count. A report whose
// totals disagree with its records is truncated or padded, never state to act
// on; a bot named twice counts once, whatever its record's case.
func checkCommanderCard(c Card) error {
	if c.Bound < c.Bots {
		return fmt.Errorf("%w: binding %q bound=%d under bots=%d", ErrBadReport, c.Cmd, c.Bound, c.Bots)
	}
	distinct := make(map[string]bool, len(c.Members))
	for _, m := range c.Members {
		distinct[strings.ToLower(m.Bot)] = true
	}
	if len(distinct) != c.Bots {
		return fmt.Errorf("%w: binding %q bots=%d, %d distinct member record(s)", ErrBadReport, c.Cmd, c.Bots, len(distinct))
	}
	if len(c.Log) != c.Orders {
		return fmt.Errorf("%w: binding %q orders=%d, %d order record(s)", ErrBadReport, c.Cmd, c.Orders, len(c.Log))
	}
	return nil
}

// commanderIdentity is the key a continuation of the kind repeats: cmd= for
// the card's own lines, lead= for the run record (§10 has no cmd= of its own).
func commanderIdentity(kind string) string {
	if kind == "run" {
		return "lead"
	}
	return "cmd"
}

// parseCommanderCard reads one binding record: the card's own line. group= is
// the lead's live party counter of this session: absent when the module has
// none, never a stored zero. map=/pos= is the commander's own spot: absent
// when not known, never the origin. offset: the module names the raid field
// `raid` and the player-led flag `playerled`.
func parseCommanderCard(rec record) (Card, error) {
	var out Card
	var err error
	if out.Cmd, err = rec.requireText("cmd"); err != nil {
		return out, err
	}
	if out.Cmd == "" {
		return out, fmt.Errorf("%w: binding has an empty cmd=", ErrBadReport)
	}
	if out.CGUID, err = rec.requireText("cguid"); err != nil {
		return out, err
	}
	if out.Online, err = rec.requireBool("online"); err != nil {
		return out, err
	}
	if out.Lead, err = rec.requireText("lead"); err != nil {
		return out, err
	}
	if out.Group, out.HasGroup, err = rec.optionalInt("group", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Bound, err = rec.requireInt("bound", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Bots, err = rec.requireInt("bots", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.System, err = rec.requireBool("system"); err != nil {
		return out, err
	}
	if out.InParty, err = rec.requireBool("inparty"); err != nil {
		return out, err
	}
	if out.Raid, err = rec.requireBool("raid"); err != nil {
		return out, err
	}
	if out.PlayerLed, err = rec.requireBool("playerled"); err != nil {
		return out, err
	}
	if out.BoundAt, err = rec.requireInt("boundat", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Orders, err = rec.requireInt("orders", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Orders > 8 {
		return out, fmt.Errorf("%w: binding orders=%d, the log holds at most 8", ErrBadReport, out.Orders)
	}
	if m, ok, err := rec.optionalMapID("map"); err != nil {
		return out, err
	} else if ok {
		out.Map, out.HasMap = m, true
	}
	if raw, ok := rec.rawValue("pos"); ok {
		x, y, z, err := coordinate(rec, "pos", raw)
		if err != nil {
			return out, err
		}
		out.Pos = &XYZ{X: x, Y: y, Z: z}
	}
	if out.HasMap && out.Pos == nil {
		return out, fmt.Errorf("%w: binding map= without pos=", ErrBadReport)
	}
	if out.Extra, err = rec.extra(commanderRecordKeys); err != nil {
		return out, err
	}
	return out, nil
}

// parseCommanderMember reads one member record: a bound bot, the lead first.
// guid= is the host guid text, decimal as the module writes it. sub= is the
// raid subgroup, absent outside a raid. zone=/pos= is the bot's own spot.
func parseCommanderMember(rec record) (CardMember, error) {
	var out CardMember
	var err error
	if out.Bot, err = rec.requireText("bot"); err != nil {
		return out, err
	}
	if out.Bot == "" {
		return out, fmt.Errorf("%w: member has an empty bot=", ErrBadReport)
	}
	if out.GUID, err = rec.requireText("guid"); err != nil {
		return out, err
	}
	if out.Class, err = rec.requireText("class"); err != nil {
		return out, err
	}
	if out.Role, err = rec.requireText("role"); err != nil {
		return out, err
	}
	if out.Level, err = rec.requireInt("level", 1, 255); err != nil {
		return out, err
	}
	if out.HP, err = rec.requireInt("hp", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.MaxHP, err = rec.requireInt("maxhp", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.HP > out.MaxHP {
		return out, fmt.Errorf("%w: hp=%d over maxhp=%d", ErrBadValue, out.HP, out.MaxHP)
	}
	if out.Power, err = rec.requireInt("power", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.MaxPower, err = rec.requireInt("maxpower", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.PowerTyp, err = rec.requireText("ptype"); err != nil {
		return out, err
	}
	if out.Alive, err = rec.requireBool("alive"); err != nil {
		return out, err
	}
	if out.Lead, err = rec.requireBool("lead"); err != nil {
		return out, err
	}
	if out.Map, err = rec.requireUint32("map"); err != nil {
		return out, err
	}
	if out.Zone, err = rec.requireInt("zone", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if raw, ok := rec.rawValue("pos"); ok {
		x, y, z, err := coordinate(rec, "pos", raw)
		if err != nil {
			return out, err
		}
		out.Pos = XYZ{X: x, Y: y, Z: z}
	} else {
		return out, rec.missing("pos")
	}
	if out.Sub, out.HasSub, err = rec.optionalInt("sub", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Extra, err = rec.extra(commanderMemberKeys); err != nil {
		return out, err
	}
	return out, nil
}

// parseCommanderOrder reads one order record: the commander's log, oldest
// first. detail= and result= are the module's own words (decoded); ok=yes
// means the order queued.
func parseCommanderOrder(rec record) (CardOrder, error) {
	var out CardOrder
	var err error
	if out.At, err = rec.requireInt("at", 0, math.MaxInt32); err != nil {
		return out, err
	}
	if out.Verb, err = rec.requireText("verb"); err != nil {
		return out, err
	}
	if out.Verb == "" {
		return out, fmt.Errorf("%w: order has an empty verb=", ErrBadReport)
	}
	if out.Detail, _, err = rec.text("detail"); err != nil {
		return out, err
	}
	if out.OK, err = rec.requireBool("ok"); err != nil {
		return out, err
	}
	if out.Result, err = rec.requireText("result"); err != nil {
		return out, err
	}
	if out.Extra, err = rec.extra(commanderOrderKeys); err != nil {
		return out, err
	}
	return out, nil
}
