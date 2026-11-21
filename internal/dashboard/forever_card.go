package dashboard

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/runmap"
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

// foreverCommanderMember is one member of the commander status: the module's
// member record as the daemon decodes it.
type foreverCommanderMember struct {
	Bot      string    `json:"bot"`
	GUID     string    `json:"guid"`
	Class    string    `json:"class"`
	Role     string    `json:"role"`
	Level    int       `json:"level"`
	HP       int64     `json:"hp"`
	MaxHP    int64     `json:"maxhp"`
	Power    int64     `json:"power"`
	MaxPower int64     `json:"maxpower"`
	PowerTyp string    `json:"ptype"`
	Alive    bool      `json:"alive"`
	Lead     bool      `json:"lead"`
	Map      uint32    `json:"map"`
	Pos      []float64 `json:"pos"`
}

// foreverCommanderRun is the run record of the commander status.
type foreverCommanderRun struct {
	Lead    string `json:"lead"`
	Plan    string `json:"plan"`
	State   string `json:"state"`
	Step    int    `json:"step"`
	Steps   int    `json:"steps"`
	Leg     int    `json:"leg"`
	LegName string `json:"legname"`
	Kind    string `json:"kind"`
	Phase   string `json:"phase"`
	Note    string `json:"note"`
	Elapsed int    `json:"elapsed"`
	Deaths  int    `json:"deaths"`
	Wipes   int    `json:"wipes"`
}

// foreverCommanderStatus is the daemon's decode of the module's status
// report (the commander {op:status} result's status field).
type foreverCommanderStatus struct {
	Cmd     string                   `json:"cmd"`
	Online  bool                     `json:"online"`
	Lead    string                   `json:"lead"`
	Bound   int                      `json:"bound"`
	Map     uint32                   `json:"map"`
	Pos     []float64                `json:"pos"`
	Members []foreverCommanderMember `json:"members"`
	Run     *foreverCommanderRun     `json:"run"`
}

// foreverCommanderResult is the commander method's answer: sent line, module
// reply, and the decoded status. Run-active and not-available are shown as
// the reply (see forever_control.go).
type foreverCommanderResult struct {
	Sent   string                  `json:"sent"`
	Reply  map[string]any          `json:"reply"`
	Status *foreverCommanderStatus `json:"status,omitempty"`
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
	c := Card{ID: in.Agent.ID, RPC: in.Agent.Addr, Orders: []Order{}, Changes: []Change{}, Bots: []BotView{}, Mobs: []MobView{}, Events: []Event{}}
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
	var cmd foreverCommanderResult
	if len(in.Commander) > 0 {
		_ = json.Unmarshal(in.Commander, &cmd)
	}
	var units []foreverUnit
	if len(in.Units) > 0 {
		_ = json.Unmarshal(in.Units, &units)
	}
	fillForeverBots(&c, cmd.Status, party.Members)
	fillForeverMobs(&c, units)
	fillForeverRun(&c, cmd.Status, in.Now)
	fillForeverStall(&c, in.Now)
	if in.Plan != nil {
		c.Label.Map = in.Plan.Map
	}
	return c
}

// fillForeverBots builds the card's bots from the commander status members.
// Party rows only fill in online/dead when the status names no member for
// them; the status is the source of roles, hp and positions.
func fillForeverBots(c *Card, cmd *foreverCommanderStatus, party []foreverPartyMember) {
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
		if len(m.Pos) >= 2 {
			b.X, b.Y = Project(float32(m.Pos[0]), float32(m.Pos[1]))
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
		for i := range c.Bots {
			if c.Bots[i].GUID == u.GUID {
				c.Bots[i].Target = name
				if keyOf(u.Target) != 0 {
					c.Bots[i].TargetGUID = u.Target
				}
			}
		}
		for i := range c.Mobs {
			if c.Mobs[i].GUID == u.GUID {
				c.Mobs[i].Target = name
			}
		}
	}
	c.Feed = Feed{Have: true}
}

// fillForeverRun fills the run state from the commander run record: the plan,
// the step counts, and a run state the page's buttons read.
func fillForeverRun(c *Card, cmd *foreverCommanderStatus, now time.Time) {
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
	c.Run.Leg, c.Run.LegName = r.Leg, r.LegName
	c.Run.Minutes = round1(float64(r.Elapsed) / 60)
	c.Run.LastStep = r.Note
	c.Counters.Deaths = r.Deaths
	c.Counters.Wipes = r.Wipes
	_ = now
}

// fillForeverStall sets the stall timer from the run's elapsed time: a run
// that makes no progress stalls like a turtle one (yellow at two minutes
// without progress, red at five). The commander report carries no
// last-progress word, so elapsed is the stall age while running.
func fillForeverStall(c *Card, now time.Time) {
	_ = now
	if c.Run.State != "running" {
		c.Stall = StallInfo{Level: "none"}
		return
	}
	secs := c.Run.Minutes * 60
	switch {
	case secs >= 5*60:
		c.Stall = StallInfo{Seconds: secs, Level: "red"}
	case secs >= 2*60:
		c.Stall = StallInfo{Seconds: secs, Level: "yellow"}
	default:
		c.Stall = StallInfo{Seconds: secs, Level: "none"}
	}
}
