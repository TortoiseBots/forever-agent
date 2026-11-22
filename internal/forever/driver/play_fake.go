package driver

import (
	"context"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// The scripted world of the player run, in the shape of the driver's own fake: a normal file, not a test file, so a
// later round's dry command runs the same machine. It stands in for the server: the bot module is GoldenOwner (the
// contract's golden lines), the party and the agent's own character are this struct, and the walk is a straight line.
//
// The fake does not prove what the server does. It proves the machine's sequence, its lines and its safety rules.

// PlaySend is one line the fake module got, on the run's clock.
type PlaySend struct {
	At   time.Time
	Line string
}

// PlayFake is the player run's GroupPort, PlayerPort, World and Walker over one virtual clock; its Module is the
// Owner port over the same bot table.
//
// The bot table is GoldenOwner's: a bot listed there is online and in the party once `.bot add` answered, and its
// map changes when the run summons it. A bot stands at the agent's side once it was told to follow (or was summoned)
// unless Stuck names it; before that it stands where BotPos puts it, or at the agent's side.
type PlayFake struct {
	*GoldenOwner
	// Module is the fake bot module over the same bot table: the Owner port of a play run.
	Module *PlayModule
	Route  *Route

	// The agent's own character. GoldenOwner.SelfMap is the agent's map too: it is what a summon compares against.
	SelfName  string
	SelfPos   Vec3
	SelfPhase PlayerPhase
	selfGUID  wire.GUID
	selfHP    [2]int

	// BotPos is where a bot stands before it is told to follow, by normalised name; a bot the map lacks stands at
	// the agent's side. BotHP is a bot's health and max health (900/900 when the map lacks it).
	BotPos map[string]Vec3
	BotHP  map[string][2]int
	// Moved names the bots that were told to follow or were summoned: they stand at the agent's side from then on.
	Moved map[string]bool
	// Stuck names the bots whose follow order does not move them: the run has to summon those.
	Stuck map[string]bool

	units []Unit
	epoch uint64

	// Actions is every port call the fake can see, in order and on the run's clock: the `.bot` lines and the group
	// commands (as "loot-method", "mark" and "disband"). Walks is every polyline the walker was given.
	Actions []PlaySend
	Walks   [][][3]float32
	// MaxRoster is the largest roster the run saw: FORM's proof that the five stood together.
	MaxRoster int
	// LootTaken, LootAt and LootReadback are the loot method the group took; LootErr makes the readback fail.
	LootTaken    party.LootMethod
	LootAt       int
	LootReadback bool
	LootErr      error
	// Marks are the icons set and Disbanded whether the group was disbanded.
	Marks     []party.MarkEntry
	Disbanded bool

	// Selected and Attacked are the agent's own selection and attack, Looted the corpses it opened.
	Selected wire.GUID
	Attacked wire.GUID
	Looted   []wire.GUID
	Released bool
	Rezzed   bool
	Used     []uint32
	Gossips  []int
	// TriggeredAt is when the entrance trigger was sent; TriggerFails makes it do nothing, so the agent never
	// arrives on the plan's map.
	TriggeredAt  time.Time
	TriggerFails bool
	// OnWalk, when set, is asked before a walk of that destination: a test scripts the party's state there.
	OnWalk func(f *PlayFake, to Vec3)
}

// NewPlayFake builds the fake for a route, with the agent at the plan's portal on the outer map and every bot of
// bots offline, so FORM adds them.
func NewPlayFake(route *Route, clock *VClock, self string, bots ...string) *PlayFake {
	owner := &GoldenOwner{Clock: clock, Bots: make([]*OwnerBot, 0, len(bots))}
	for _, name := range bots {
		owner.Bots = append(owner.Bots, &OwnerBot{Name: name, Level: 15, Class: "Warrior", Move: "follow", Mode: "engine"})
	}
	f := &PlayFake{
		GoldenOwner: owner, Route: route, SelfName: self, SelfPhase: PhaseAlive,
		BotPos: map[string]Vec3{}, BotHP: map[string][2]int{}, Moved: map[string]bool{}, Stuck: map[string]bool{},
		selfHP: [2]int{1000, 1000},
	}
	f.Module = &PlayModule{GoldenOwner: owner, f: f}
	f.selfGUID = wire.GUID{Low: 1}
	if route.Portal != nil {
		f.SelfPos = route.Portal.Pos
		f.GoldenOwner.SelfMap = route.Portal.Map
	} else {
		f.SelfPos = route.Entrance
		f.GoldenOwner.SelfMap = route.Map
	}
	// An offline bot that logs in stands where its owner is.
	for _, b := range owner.Bots {
		b.Map = f.GoldenOwner.SelfMap
	}
	return f
}

// SelfGUID is the agent's own guid.
func (f *PlayFake) SelfGUID() wire.GUID { return f.selfGUID }

// SetBotHP gives a bot its health and max health (the tank preflight and REST read them).
func (f *PlayFake) SetBotHP(name string, hp, maxHP int) {
	f.BotHP[strings.ToLower(name)] = [2]int{hp, maxHP}
}

// ClearBosses puts one dead unit of every plan boss in the view: the run's clear is every boss dead.
func (f *PlayFake) ClearBosses() {
	var n uint64
	for _, leg := range f.Route.Legs {
		if leg.Boss == nil {
			continue
		}
		n++
		f.units = append(f.units, Unit{GUID: wire.GUID{Low: 0xF130000000000000 + n}, Entry: leg.Boss.Entry, Pos: leg.Boss.Pos, Dead: true})
	}
}

// AddUnit puts one scripted unit in the view.
func (f *PlayFake) AddUnit(u Unit) { f.units = append(f.units, u) }

// send records the line a bot command of v and name builds.
func (f *PlayFake) send(v botcmd.Verb, name string) {
	line, err := botcmd.Line(v, name, "")
	if err != nil {
		return
	}
	f.act(line)
}

// act records one port call on the fake's clock.
func (f *PlayFake) act(line string) {
	f.Actions = append(f.Actions, PlaySend{At: f.Clock.Now(), Line: line})
}

// Line returns the first call whose line has the prefix, and whether there was one.
func (f *PlayFake) Line(prefix string) (PlaySend, bool) {
	for _, s := range f.Actions {
		if strings.HasPrefix(s.Line, prefix) {
			return s, true
		}
	}
	return PlaySend{}, false
}

// Lines returns the calls whose line has the prefix, in order.
func (f *PlayFake) Lines(prefix string) []PlaySend {
	var out []PlaySend
	for _, s := range f.Actions {
		if strings.HasPrefix(s.Line, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// PlayModule is the fake bot module: the contract's golden owner plus the lines and the party's reactions the play
// run reads. It is a type of its own because one Go type cannot answer both `.bot attack <Bot>` (the module's order)
// and the character's own attack: the player run's port has an Attack of its own.
type PlayModule struct {
	*GoldenOwner
	f *PlayFake
}

// Add is `.bot add`.
func (m *PlayModule) Add(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbAdd, name)
	rep, err := m.GoldenOwner.Add(ctx, name)
	if err == nil {
		// The bot logs in at its owner's side.
		if b := m.GoldenOwner.bot(name); b != nil {
			b.Map = m.GoldenOwner.SelfMap
		}
	}
	return rep, err
}

// Logout is `.bot logout`.
func (m *PlayModule) Logout(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbLogout, name)
	return m.GoldenOwner.Logout(ctx, name)
}

// Follow is `.bot follow`: a bot that is not Stuck moves to the agent's side.
func (m *PlayModule) Follow(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbFollow, name)
	rep, err := m.GoldenOwner.Follow(ctx, name)
	if err == nil && !m.f.Stuck[strings.ToLower(name)] {
		m.f.Moved[strings.ToLower(name)] = true
	}
	return rep, err
}

// Stay is `.bot stay`.
func (m *PlayModule) Stay(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbStay, name)
	return m.GoldenOwner.Stay(ctx, name)
}

// Attack is `.bot attack`: the module takes the requester's selected unit, which the fake does not model.
func (m *PlayModule) Attack(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbAttack, name)
	return m.GoldenOwner.Attack(ctx, name)
}

// Summon is `.bot summon`: the bot moves to the agent's map and side.
func (m *PlayModule) Summon(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbSummon, name)
	rep, err := m.GoldenOwner.Summon(ctx, name)
	if err == nil {
		m.f.Moved[strings.ToLower(name)] = true
	}
	return rep, err
}

// Who is `.bot who`.
func (m *PlayModule) Who(ctx context.Context) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbWho, "")
	return m.GoldenOwner.Who(ctx)
}

// --- GroupPort ---

// Roster is the party: the agent plus every online bot of the table that is in it.
func (f *PlayFake) Roster() []PartyMember {
	out := []PartyMember{{
		Name: f.SelfName, GUID: f.selfGUID, Online: true, Dead: f.SelfPhase != PhaseAlive,
		Ghost: f.SelfPhase == PhaseGhost, HP: f.selfHP[0], MaxHP: f.selfHP[1], Pos: f.SelfPos, Map: f.SelfMap,
	}}
	for _, b := range f.GoldenOwner.Bots {
		if !b.Online || !b.InParty {
			continue
		}
		hp, maxHP := 900, 900
		if v, ok := f.BotHP[strings.ToLower(b.Name)]; ok {
			hp, maxHP = v[0], v[1]
		}
		out = append(out, PartyMember{
			Name: b.Name, GUID: f.botGUID(b.Name), Online: true, Dead: b.Dead,
			HP: hp, MaxHP: maxHP, Pos: f.botPos(b), Map: b.Map,
		})
	}
	f.MaxRoster = max(f.MaxRoster, len(out))
	return out
}

// botGUID is a bot's guid: the table's order, past the agent's own.
func (f *PlayFake) botGUID(name string) wire.GUID {
	for i, b := range f.GoldenOwner.Bots {
		if strings.EqualFold(b.Name, name) {
			return wire.GUID{Low: uint64(0x200 + i)}
		}
	}
	return wire.GUID{}
}

// botPos is where a bot stands: the agent's side once it follows, else its scripted spot.
func (f *PlayFake) botPos(b *OwnerBot) Vec3 {
	if f.Moved[strings.ToLower(b.Name)] {
		return f.SelfPos
	}
	if p, ok := f.BotPos[strings.ToLower(b.Name)]; ok {
		return p
	}
	return f.SelfPos
}

// Leader is the agent's own guid: the run leads the party.
func (f *PlayFake) Leader() wire.GUID { return f.selfGUID }

// LootMethod takes the loot method, or answers the scripted readback failure.
func (f *PlayFake) LootMethod(method party.LootMethod, threshold int) error {
	if f.LootErr != nil {
		return f.LootErr
	}
	f.LootTaken, f.LootAt, f.LootReadback = method, threshold, true
	f.act("loot-method")
	return nil
}

// Mark records one raid target icon.
func (f *PlayFake) Mark(target wire.GUID, mark party.Mark) error {
	f.Marks = append(f.Marks, party.MarkEntry{Mark: mark, Target: target})
	f.act("mark")
	return nil
}

// Disband uninvites every bot of the table.
func (f *PlayFake) Disband() error {
	for _, b := range f.GoldenOwner.Bots {
		b.InParty = false
	}
	f.Disbanded = true
	f.act("disband")
	return nil
}

// --- PlayerPort ---

// Select is the agent's own selection.
func (f *PlayFake) Select(guid wire.GUID) error { f.Selected = guid; return nil }

// Attack starts the agent's own attack.
func (f *PlayFake) Attack(guid wire.GUID) error { f.Attacked = guid; return nil }

// StopAttack ends it.
func (f *PlayFake) StopAttack() error { f.Attacked = wire.GUID{}; return nil }

// FightTick is one tick of the agent's fight; the fake's fights are scripted by the units.
func (f *PlayFake) FightTick(ctx context.Context, target wire.GUID) error { return nil }

// Rest sits the agent down; the fake's party is always rested.
func (f *PlayFake) Rest(ctx context.Context, limit time.Duration) error { return nil }

// Phase is the character's state.
func (f *PlayFake) Phase() PlayerPhase { return f.SelfPhase }

// Release makes the character a ghost.
func (f *PlayFake) Release() error { f.Released, f.SelfPhase = true, PhaseGhost; return nil }

// AcceptRez brings the character back.
func (f *PlayFake) AcceptRez() error { f.Rezzed, f.SelfPhase = true, PhaseAlive; return nil }

// Loot opens one corpse.
func (f *PlayFake) Loot(ctx context.Context, guid wire.GUID) error {
	f.Looted = append(f.Looted, guid)
	return nil
}

// UseObject uses one game object.
func (f *PlayFake) UseObject(ctx context.Context, entry uint32, stand Vec3) error {
	f.Used = append(f.Used, entry)
	return nil
}

// Gossip picks one gossip option.
func (f *PlayFake) Gossip(ctx context.Context, entry uint32, option int) error {
	f.Gossips = append(f.Gossips, option)
	return nil
}

// --- World ---

// Self is the agent's own position and map.
func (f *PlayFake) Self() (Vec3, uint32, uint64, bool) {
	return f.SelfPos, f.GoldenOwner.SelfMap, f.epoch, false
}

// Units is the scripted view.
func (f *PlayFake) Units() []Unit { return f.units }

// PlayerGUID is a party member's guid.
func (f *PlayFake) PlayerGUID(name string) (wire.GUID, bool) {
	for _, m := range f.Roster() {
		if strings.EqualFold(m.Name, name) {
			return m.GUID, true
		}
	}
	return wire.GUID{}, false
}

// PlayerGUIDOfCounter knows no counters: the play run names no one by one.
func (f *PlayFake) PlayerGUIDOfCounter(uint64) (wire.GUID, bool) { return wire.GUID{}, false }

// EnterAreaTrigger takes the agent into the instance, and every bot that follows its owner with it.
func (f *PlayFake) EnterAreaTrigger(id int32) error {
	f.TriggeredAt = f.Clock.Now()
	if f.TriggerFails {
		return nil
	}
	f.SelfPos = f.Route.Entrance
	f.GoldenOwner.SelfMap = f.Route.Map
	f.epoch++
	for _, b := range f.GoldenOwner.Bots {
		if b.Online && b.InParty && b.Move != "stay" {
			b.Map = f.Route.Map
		}
	}
	return nil
}

// Auras knows none: the play run reads no auras.
func (f *PlayFake) Auras(wire.GUID) []uint32 { return nil }

// ThreatTable has none: the play run reads the module's orders, not the commander's threat.
func (f *PlayFake) ThreatTable(time.Time) (tbcmd.ThreatReport, bool) {
	return tbcmd.ThreatReport{}, false
}

// Encounters has none: the fake's bosses die by their units.
func (f *PlayFake) Encounters() []EncounterEvent { return nil }

// EncounterOpen is always false.
func (f *PlayFake) EncounterOpen() bool { return false }

// --- Walker ---

// Path is a straight line: the fake does not use the map data.
func (f *PlayFake) Path(from, to Vec3) (navmesh.Route, error) {
	return navmesh.Route{
		Kind: navmesh.RouteComplete, Corners: 2, Start: from, End: to,
		Points: []Vec3{from, to}, Length: float32(dist3(from, to)),
	}, nil
}

// Walk moves the agent at 7 yd/s.
func (f *PlayFake) Walk(ctx context.Context, pts [][3]float32) error {
	if len(pts) == 0 {
		return nil
	}
	if f.OnWalk != nil {
		f.OnWalk(f, Vec3(pts[len(pts)-1]))
	}
	f.Walks = append(f.Walks, append([][3]float32(nil), pts...))
	var length float64
	for i := 1; i < len(pts); i++ {
		length += dist3(Vec3(pts[i-1]), Vec3(pts[i]))
	}
	if length > 0 {
		if err := f.Clock.Sleep(ctx, time.Duration(length/7.0*float64(time.Second))); err != nil {
			return err
		}
	}
	f.SelfPos = Vec3(pts[len(pts)-1])
	return nil
}

// Stop ends a walk; the fake has none to end.
func (f *PlayFake) Stop() error { return nil }
