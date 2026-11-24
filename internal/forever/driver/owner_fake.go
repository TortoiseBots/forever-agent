package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
)

// summonCooldown is the fake's summon cooldown, the module's default of 300 s: the ENTER phase waits on it.
const summonCooldown = 300 * time.Second

// OwnerBot is one bot the GoldenOwner answers about: the module state the player run's FORM, ENTER and END phases
// read. Level, Class, Move and Mode fill the `who` row; SummonCooldownUntil is on the scripted clock.
type OwnerBot struct {
	Name                string
	Online              bool
	InParty             bool
	Map                 uint32
	SummonCooldownUntil time.Time
	InCombat            bool
	Dead                bool
	// Ghost is set for a dead bot that released: it is at the spirit healer until the whisper `corpse run` brings it
	// back (B-6).
	Ghost bool
	Level int
	Class string
	Move  string
	Mode  string
}

// GoldenOwner is the fake bot module of the player-run rounds: it answers contract bot-commands v1 from a scripted
// bot table, on the driver's virtual clock, and records every line it was asked. It stands in for the module side
// (TortoiseBots' player commands), which is not merged yet.
//
// Its lines are built only with botcmd.Line, so a case the contract cannot express cannot be scripted here either.
type GoldenOwner struct {
	Clock   *VClock
	SelfMap uint32 // the requester's map, for the summon rules
	Bots    []*OwnerBot
	seen    []string
}

// Rows is the rows `.bot who` lists: one per online bot, in the table's order. It makes the fake a WhoRows.
func (o *GoldenOwner) Rows() []botcmd.WhoRow {
	var rows []botcmd.WhoRow
	for _, b := range o.Bots {
		if b.Online {
			rows = append(rows, botcmd.WhoRow{Bot: b.Name, Level: b.Level, Class: b.Class, Map: b.Map, Move: b.Move, Mode: b.Mode})
		}
	}
	return rows
}

// Lines returns the lines the fake was asked, in order.
func (o *GoldenOwner) Lines() []string { return append([]string(nil), o.seen...) }

// start records the line for v and name and returns the bot's normalised name. A name the contract cannot carry
// never becomes a line, so it is an error here rather than a refusal from a module that never saw it.
func (o *GoldenOwner) start(v botcmd.Verb, name string) (string, error) {
	norm, err := chat.NormaliseName(name)
	if err != nil {
		return "", err
	}
	line, err := botcmd.Line(v, norm, "")
	if err != nil {
		return "", err
	}
	o.seen = append(o.seen, line)
	return norm, nil
}

// bot finds the table's entry for a normalised name.
func (o *GoldenOwner) bot(name string) *OwnerBot {
	for _, b := range o.Bots {
		if strings.EqualFold(b.Name, name) {
			return b
		}
	}
	return nil
}

// ok and refuse write the two reply shapes of the contract; refuse also answers the error a caller acts on.
func (o *GoldenOwner) ok(v botcmd.Verb, bot, text string) botcmd.Reply {
	return botcmd.Reply{Verb: string(v), Bot: bot, OK: true, Text: text}
}

func (o *GoldenOwner) refuse(v botcmd.Verb, bot, code, text string) (botcmd.Reply, error) {
	r := botcmd.Reply{Verb: string(v), Bot: bot, Code: code, Text: text}
	return r, r.Err()
}

// Add is `.bot add`: an offline character of the table logs in and joins the party.
func (o *GoldenOwner) Add(ctx context.Context, name string) (botcmd.Reply, error) {
	bot, err := o.start(botcmd.VerbAdd, name)
	if err != nil {
		return botcmd.Reply{}, err
	}
	b := o.bot(bot)
	switch {
	case b == nil:
		return o.refuse(botcmd.VerbAdd, bot, "not-found", "no character by that name")
	case b.Online:
		return o.refuse(botcmd.VerbAdd, bot, "already-online", "it is already in the world")
	}
	b.Online, b.InParty = true, true
	return o.ok(botcmd.VerbAdd, bot, "queued for login; it joins your party"), nil
}

// Logout is `.bot logout`.
func (o *GoldenOwner) Logout(ctx context.Context, name string) (botcmd.Reply, error) {
	bot, err := o.start(botcmd.VerbLogout, name)
	if err != nil {
		return botcmd.Reply{}, err
	}
	b := o.bot(bot)
	if b == nil || !b.Online {
		return o.refuse(botcmd.VerbLogout, bot, "not-found", "no bot by that name is online")
	}
	b.Online, b.InParty = false, false
	return o.ok(botcmd.VerbLogout, bot, "logging it out"), nil
}

// tactic answers follow, stay, attack and flee: the bot must be online, in the party and alive.
func (o *GoldenOwner) tactic(v botcmd.Verb, name, text string, apply func(*OwnerBot)) (botcmd.Reply, error) {
	bot, err := o.start(v, name)
	if err != nil {
		return botcmd.Reply{}, err
	}
	b := o.bot(bot)
	switch {
	case b == nil || !b.Online:
		return o.refuse(v, bot, "not-found", "no bot by that name is online")
	case !b.InParty:
		return o.refuse(v, bot, "not-in-group", "it is not in your group")
	case b.Dead:
		return o.refuse(v, bot, "dead", "it is dead")
	}
	if apply != nil {
		apply(b)
	}
	return o.ok(v, bot, text), nil
}

// Follow is `.bot follow`.
func (o *GoldenOwner) Follow(ctx context.Context, name string) (botcmd.Reply, error) {
	return o.tactic(botcmd.VerbFollow, name, "following you", func(b *OwnerBot) { b.Move = "follow" })
}

// Stay is `.bot stay`.
func (o *GoldenOwner) Stay(ctx context.Context, name string) (botcmd.Reply, error) {
	return o.tactic(botcmd.VerbStay, name, "staying where it is", func(b *OwnerBot) { b.Move = "stay" })
}

// Attack is `.bot attack`: the module takes the requester's selected unit, so the fake needs no selection.
func (o *GoldenOwner) Attack(ctx context.Context, name string) (botcmd.Reply, error) {
	return o.tactic(botcmd.VerbAttack, name, "attacking your selected unit", nil)
}

// Summon is `.bot summon`: the bot moves to the requester's map within the cooldown, once it is there the map
// rule answers already-on-map.
func (o *GoldenOwner) Summon(ctx context.Context, name string) (botcmd.Reply, error) {
	bot, err := o.start(botcmd.VerbSummon, name)
	if err != nil {
		return botcmd.Reply{}, err
	}
	b := o.bot(bot)
	switch {
	case b == nil || !b.Online:
		return o.refuse(botcmd.VerbSummon, bot, "not-found", "no bot by that name is online")
	case !b.InParty:
		return o.refuse(botcmd.VerbSummon, bot, "not-in-group", "it is not in your group")
	case b.Dead:
		return o.refuse(botcmd.VerbSummon, bot, "dead", "it is dead")
	case b.InCombat:
		return o.refuse(botcmd.VerbSummon, bot, "in-combat", "it is in combat")
	case b.Map == o.SelfMap:
		return o.refuse(botcmd.VerbSummon, bot, "already-on-map", "it is already on your map")
	case o.Clock != nil && !b.SummonCooldownUntil.IsZero() && o.Clock.Now().Before(b.SummonCooldownUntil):
		return o.refuse(botcmd.VerbSummon, bot, "on-cooldown", "the summon is on cooldown")
	}
	b.Map = o.SelfMap
	if o.Clock != nil {
		b.SummonCooldownUntil = o.Clock.Now().Add(summonCooldown)
	}
	return o.ok(botcmd.VerbSummon, bot, "moving it next to you"), nil
}

// Whisper is one word of the module's chat grammar A (B-6) said to a bot: `release` makes a dead bot a ghost and
// `corpse run` brings a ghost back on its owner's map. The module answers no whisper, so there is no refusal here; a
// bot that is not in the state the word is for simply stays as it is.
func (o *GoldenOwner) Whisper(ctx context.Context, name, word string) error {
	bot, err := botcmd.Whisper(name, word)
	if err != nil {
		return err
	}
	b := o.bot(bot)
	if b == nil {
		return nil
	}
	switch word {
	case botcmd.WhisperRelease:
		if b.Dead {
			b.Dead, b.Ghost = false, true
		}
	case botcmd.WhisperCorpseRun:
		if b.Ghost {
			b.Ghost, b.Map = false, o.SelfMap
		}
	}
	return nil
}

// Who is `.bot who`: the header's count of the table's online bots (the module lists online bots only).
func (o *GoldenOwner) Who(ctx context.Context) (botcmd.Reply, error) {
	line, err := botcmd.Line(botcmd.VerbWho, "", "")
	if err != nil {
		return botcmd.Reply{}, err
	}
	o.seen = append(o.seen, line)
	n := 0
	for _, b := range o.Bots {
		if b.Online {
			n++
		}
	}
	return o.ok(botcmd.VerbWho, "", fmt.Sprintf("%d bot(s)", n)), nil
}
