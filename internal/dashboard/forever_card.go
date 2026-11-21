package dashboard

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/runmap"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
)

// This file is the dashboard's live view over a forever serve slot (L5):
// BuildForeverCard fills the existing Card fields from the serve answers
// (status, party, commander {op:status}, units {within:100}, event_log tail).
// Guids stay text on the card, keyed by serveclient Key (runmap.GUIDKey), so
// a unit and its target meet even when only the low 64 bits match. No tbm*
// call is made here or in the Forever poll: bots come from the commander
// status, mobs from units.

// foreverStatus is the serve status answer the card reads.
type foreverStatus struct {
	State     string  `json:"state"`
	Character string  `json:"character"`
	Level     int     `json:"level"`
	Map       uint32  `json:"map"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	Health    int64   `json:"health"`
	MaxHealth int64   `json:"max_health"`
	Power     int32   `json:"power"`
	MaxPower  int32   `json:"max_power"`
	PowerType uint8   `json:"power_type,omitempty"`
	InCombat  bool    `json:"in_combat"`
	Dead      bool    `json:"dead"`
	Ghost     bool    `json:"ghost"`
	Error     string  `json:"error,omitempty"`
}

// foreverPartyMember is one row of the serve party answer.
type foreverPartyMember struct {
	Name      string `json:"name"`
	GUID      string `json:"guid"`
	Online    bool   `json:"online"`
	Dead      bool   `json:"dead"`
	Leader    bool   `json:"leader"`
	Health    int32  `json:"health"`
	MaxHealth int32  `json:"max_health"`
	PowerType uint8  `json:"power_type,omitempty"`
	Power     int32  `json:"power"`
	MaxPower  int32  `json:"max_power"`
	Level     uint16 `json:"level"`
}

// foreverCommanderCard picks the card the dashboard shows from a commander {op:status} answer: the card with a
// run when one has it, else the first (a character commands one lead in practice). Nil when it commands none.
func foreverCommanderCard(res serveclient.CommanderResult) *serveclient.CommanderCard {
	var first *serveclient.CommanderCard
	for i := range res.Cards {
		c := &res.Cards[i]
		if c.Run != nil {
			return c
		}
		if first == nil {
			first = c
		}
	}
	return first
}

// foreverUnit is one row of the serve units answer.
type foreverUnit struct {
	GUID      string  `json:"guid"`
	Type      string  `json:"type"`
	Entry     int32   `json:"entry,omitempty"`
	Name      string  `json:"name"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	Health    int64   `json:"health"`
	MaxHealth int64   `json:"max_health"`
	Level     int32   `json:"level"`
	Target    string  `json:"target,omitempty"`
	InCombat  bool    `json:"in_combat"`
	Dead      bool    `json:"dead"`
	Moving    bool    `json:"moving"`
	Casting   bool    `json:"casting,omitempty"`
	SpellID   int32   `json:"spell_id,omitempty"`
}

// keyOf keys a serve guid in its text form: the run-map key, so the high
// word matters (two guids that share their low 64 bits are two units).
func keyOf(guid string) uint64 {
	return runmap.GUIDKey(guid)
}

// BuildForeverCard turns one poll of a serve slot into the card. It is pure:
// the same inputs give the same card. Bots come from the commander status,
// mobs from units; the run state and stall come from the commander run
// record, not from dungeon_status.
func BuildForeverCard(in Inputs) Card {
	c := Card{ID: in.Agent.ID, RPC: in.Agent.Addr, Kind: "forever", Orders: []Order{}, Changes: []Change{}, Bots: []BotView{}, Mobs: []MobView{}, Events: []Event{}}
	c.Run = RunInfo{State: "idle", Phase: "idle", Leg: -1}
	c.Stall.Level = "none"
	var st foreverStatus
	if len(in.Status) > 0 && json.Unmarshal(in.Status, &st) == nil && st.Character != "" {
		c.Online = true
		c.Character, c.Level, c.Session, c.Map = st.Character, st.Level, st.State, st.Map
		if st.State == "online" {
			c.Pos = &Pos{X: st.X, Y: st.Y, Z: st.Z}
		}
		c.LastError = st.Error
	} else {
		c.Error = in.StatusErr
		if c.Error == "" {
			c.Error = "no answer"
		}
		return c
	}
	var party struct {
		InGroup bool                 `json:"in_group"`
		Members []foreverPartyMember `json:"members"`
	}
	if len(in.Party) > 0 {
		_ = json.Unmarshal(in.Party, &party)
	}
	var cmdRes serveclient.CommanderResult
	if len(in.Commander) > 0 {
		_ = json.Unmarshal(in.Commander, &cmdRes)
	}
	cmd := foreverCommanderCard(cmdRes)
	var units []foreverUnit
	if len(in.Units) > 0 {
		_ = json.Unmarshal(in.Units, &units)
	}
	fillForeverBots(&c, cmd, party.Members)
	fillForeverMobs(&c, units)
	fillForeverRun(&c, cmd)
	fillForeverOrders(&c, cmd, in.Now)
	fillForeverStall(&c, in.Now, in.ProgressSince)
	if in.Plan != nil {
		c.Label.Map = in.Plan.Map
	}
	return c
}

// fillForeverBots builds the card's bots from the commander status members.
// Party rows only fill in online/dead when the status names no member for
// them; the status is the source of roles, hp and positions.
func fillForeverBots(c *Card, cmd *serveclient.CommanderCard, party []foreverPartyMember) {
	if cmd == nil {
		return
	}
	byName := map[string]foreverPartyMember{}
	for _, m := range party {
		byName[m.Name] = m
	}
	for _, m := range cmd.Members {
		b := BotView{
			GUID: m.GUID, Name: m.Bot, Class: m.Class, Role: m.Role,
			Lead: m.Lead, Level: m.Level,
			HPPct: pct(float64(m.HP), float64(m.MaxHP)), PowerPct: pct(float64(m.Power), float64(m.MaxPower)),
			Alive: m.Alive,
		}
		// A member with no position (absent, or 0,0 the way the page already reads it) is unknown, never drawn at 0,0.
		if m.Pos != nil && (m.Pos.X != 0 || m.Pos.Y != 0) {
			b.X, b.Y = Project(float32(m.Pos.X), float32(m.Pos.Y))
			b.Known = true
			b.InMap = true
		}
		if p, ok := byName[m.Bot]; ok {
			if !p.Online {
				b.Alive = false
			}
			if p.Dead {
				b.Alive = false
			}
		}
		c.Bots = append(c.Bots, b)
	}
	sort.SliceStable(c.Bots, func(i, j int) bool { return c.Bots[i].Lead && !c.Bots[j].Lead })
}

// fillForeverMobs builds the card's mobs from the units answer: every
// non-player unit in view. Guids stay text, keyed by serveclient Key.
func fillForeverMobs(c *Card, units []foreverUnit) {
	byKey := map[uint64]string{}
	for _, u := range units {
		byKey[keyOf(u.GUID)] = u.Name
	}
	self := map[uint64]bool{}
	for _, b := range c.Bots {
		if b.GUID != "" {
			self[keyOf(b.GUID)] = true
		}
	}
	for _, u := range units {
		if u.Type == "player" || self[keyOf(u.GUID)] {
			continue
		}
		m := MobView{
			GUID: u.GUID, TargetGUID: targetGUID(u.Target), Entry: uint32(u.Entry),
			HPPct: pct(float64(u.Health), float64(u.MaxHealth)),
			Leg:   -1, Group: -1,
			Engaged: u.Target != "" && u.Target != "0x00000000000000000000000000000000",
		}
		m.X, m.Y = Project(u.X, u.Y)
		m.Kind = "hostile"
		if u.Target != "" {
			if name, ok := byKey[keyOf(u.Target)]; ok {
				m.Target = name
			}
		}
		c.Mobs = append(c.Mobs, m)
	}
	fillForeverTargets(c, units)
}

// fillForeverTargets names each bot's and mob's target by guid key.
func fillForeverTargets(c *Card, units []foreverUnit) {
	byKey := map[uint64]string{}
	for _, b := range c.Bots {
		if b.GUID != "" {
			byKey[keyOf(b.GUID)] = b.Name
		}
	}
	for _, u := range units {
		if u.Entry != 0 {
			if _, ok := byKey[keyOf(u.GUID)]; !ok {
				byKey[keyOf(u.GUID)] = u.Name
			}
		} else if u.Name != "" {
			if _, ok := byKey[keyOf(u.GUID)]; !ok {
				byKey[keyOf(u.GUID)] = u.Name
			}
		}
	}
	for _, u := range units {
		if u.Target == "" || keyOf(u.Target) == 0 {
			continue
		}
		name, ok := byKey[keyOf(u.Target)]
		if !ok {
			continue
		}
		self := keyOf(u.GUID)
		if self == 0 {
			continue
		}
		for i := range c.Bots {
			if keyOf(c.Bots[i].GUID) == self {
				c.Bots[i].Target = name
				c.Bots[i].TargetGUID = u.Target
			}
		}
		for i := range c.Mobs {
			if keyOf(c.Mobs[i].GUID) == self {
				c.Mobs[i].Target = name
			}
		}
	}
	c.Feed = Feed{Have: true}
}

// fillForeverRun fills the run state from the commander run record: the plan,
// the step counts, and a run state the page's buttons read.
func fillForeverRun(c *Card, cmd *serveclient.CommanderCard) {
	if cmd == nil || cmd.Run == nil {
		return
	}
	r := cmd.Run
	c.Label.Plan = r.Plan
	c.Run.State = r.State
	if c.Run.State == "" {
		c.Run.State = "running"
	}
	c.Run.Phase = r.Phase
	if c.Run.Phase == "" {
		c.Run.Phase = c.Run.State
	}
	if r.Leg != nil {
		c.Run.Leg = *r.Leg
	}
	c.Run.LegName = r.LegName
	c.Run.Minutes = round1(float64(r.Elapsed) / 60)
	c.Run.LastStep = r.Note
	c.Counters.Deaths = r.Deaths
	c.Counters.Wipes = r.Wipes
}

// fillForeverOrders fills the card's orders from the commander card's order log (the module's own record of what it
// queued for the bots): the last maxOrders, oldest first, so the page shows the stay status of the latest order. The
// module's `at` is a unix second when it looks like one; any other value is not a clock the card can show, so the
// poll's own time stands in for it.
func fillForeverOrders(c *Card, cmd *serveclient.CommanderCard, now time.Time) {
	if cmd == nil {
		return
	}
	log := cmd.Log
	if len(log) > maxOrders {
		log = log[len(log)-maxOrders:]
	}
	for _, o := range log {
		at := now
		if o.At >= 1_000_000_000 {
			at = time.Unix(int64(o.At), 0).UTC()
		}
		ord := Order{At: at, Verb: o.Verb, Text: strings.TrimSpace("cmd " + o.Verb + " " + o.Detail), Via: "cmd", Reply: "ACK"}
		if !o.OK {
			ord.Reply, ord.Message = "ERR", o.Result
		}
		c.Orders = append(c.Orders, ord)
	}
}

// fillForeverStall sets the stall timer from the last progress mark the
// poller tracked: yellow at two minutes without progress, red at five, never
// from the run's total elapsed time (a healthy long run is not stalled). A
// run that is not running, and a run whose progress is not known yet, has no
// stall: the module's run record carries no progress timestamp of its own.
func fillForeverStall(c *Card, now, since time.Time) {
	if c.Run.State != "running" || since.IsZero() {
		c.Stall = StallInfo{Level: "none"}
		return
	}
	age := now.Sub(since)
	if age < 0 {
		age = 0
	}
	secs := round1(age.Seconds())
	switch {
	case age >= StallRed:
		c.Stall = StallInfo{Seconds: secs, Level: "red"}
	case age >= StallYellow:
		c.Stall = StallInfo{Seconds: secs, Level: "yellow"}
	default:
		c.Stall = StallInfo{Seconds: secs, Level: "none"}
	}
}
