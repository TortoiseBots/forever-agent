package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
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

// PlayVote is one roll the character answered while it looted a corpse: greed for every item it rolled on (D-9). The
// vote is the port's own, as it is in the live adapter (lootTarget and voteRolls): the run's decision is which
// corpses it opens, not how the window is filled.
type PlayVote struct {
	Corpse wire.GUID
	Item   string
	Vote   string
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
	// NoAnswer names the bots (by lower-cased name, like Moved) that do not answer the corpse-run whisper: they stay
	// ghosts, and a recovery that needs them cannot finish (§3.10's wipe-unrecovered).
	NoAnswer map[string]bool
	// RosterOffline names the bots (by lower-cased name) whose party row stays while the roster pushes it offline: the
	// row is there, the member is not. The owner table still has the bot; only the roster's last push disagrees.
	RosterOffline map[string]bool
	// Graveyard is where a released ghost stands, on the plan's portal map: the plan's portal, 40 yd out, unless a test
	// scripts another point.
	Graveyard Vec3

	units []Unit
	// objects is the scripted game objects the steps read back (Objects is the PlayerPort's view), and UseNoChange
	// names the entries whose use changes no state: a door that stays shut, which the use_object step cannot claim.
	objects     []PlayObject
	UseNoChange map[uint32]bool
	// GossipMissing is how many talks answer a menu without the plan's option (ErrNoGossipOption) before the menu has
	// it, GossipMenuTimeout is how many talks time out waiting for the menu (ErrNoGossipMenu), and GossipErr answers
	// every talk instead.
	GossipMissing     int
	GossipMenuTimeout int
	GossipErr         error
	epoch             uint64

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
	// RaidGroup is whether the group was converted to a raid, ConvertCalls how many converts were sent and RaidMoves
	// every move the layout asked for (SPEC #108 R4b). ConvertErr and MoveErr answer the two calls instead of working:
	// the run's raid-convert and raid-move stops.
	RaidGroup    bool
	ConvertCalls int
	ConvertErr   error
	MoveErr      error
	RaidMoves    []RaidSlot
	// frozen is the roster served while it is set: what the party said when FreezeRoster took it. A stats request
	// clears it, the way the server's SMSG_PARTY_MEMBER_FULL_STATE answers would.
	frozen []PartyMember
	// StatsRequests is how many member stats requests the Group port got.
	StatsRequests int

	// Selected and Attacked are the agent's own selection and attack, Looted the corpses it opened, and LootRefused
	// the corpses it tried and may not loot. Votes is every roll the character answered greed (§3.7, D-9).
	Selected    wire.GUID
	Attacked    wire.GUID
	Looted      []wire.GUID
	LootRefused []wire.GUID
	Votes       []PlayVote
	// agentAttacks is every unit the agent started its own attack on, in order (a re-attack after a walk is another).
	agentAttacks []wire.GUID
	// chaseAt is when a mob chasing the agent last moved; the fake's mobs follow the character at its own run speed.
	chaseAt time.Time
	// Rolls is the items whose roll is open on a corpse, NoLoot the corpses the character may not loot, and
	// LootFail how many times Loot on that corpse answers a transient non-ErrCannotLoot error.
	Rolls    map[wire.GUID][]string
	NoLoot   map[wire.GUID]bool
	LootFail map[wire.GUID]int
	// Released and Rezzed are the character's own release and accept, Declined the offers it refused. Offer is the
	// resurrect offer that is up, if any.
	Released bool
	Rezzed   bool
	Declined int
	Offer    *RezOffer
	Used     []uint32
	Gossips  []int
	// TriggeredAt is when the entrance trigger was sent; TriggerFails makes it do nothing, so the agent never
	// arrives on the plan's map.
	TriggeredAt  time.Time
	TriggerFails bool
	// OnWalk, when set, is asked before a walk of that destination: a test scripts the party's state there.
	OnWalk func(f *PlayFake, to Vec3)
	// WalkBlock, when set, is the walk instead of the straight move: a test scripts a walker that blocks until its
	// context ends (the walk-back bound's own case).
	WalkBlock func(ctx context.Context) error
	// OnRest, when set, is asked before the party is sat down: the pre-boss gate's rows are scripted there, after the
	// pull's gather and before the gate's first read.
	OnRest func(f *PlayFake)
	// PathFunc, when set, answers the walker's route: a test scripts a partial route (the navmesh's edge) or none.
	PathFunc func(from, to Vec3) navmesh.Route

	// The fight. A unit the group is ordered onto turns on the tank (and its pack-mates on their own members) after
	// AggroDelay, and loses playHitPerTick of its health on every FightTick until it dies. BotPower is a bot's power:
	// the type (0 mana, 1 rage, 2 focus, 3 energy) and its value and maximum; BotRegen is what it regains per second
	// (a bot that eats and drinks between fights). A bot both maps lack uses rage and is never short of it. AggroAt is
	// when a pack first turned on the party.
	AggroDelay  time.Duration
	BotPower    map[string][3]int
	BotRegen    map[string]int
	powerAt     map[string]time.Time
	AggroAt     time.Time
	pending     []pendingAggro
	encounters  []EncounterEvent
	OnFightTick func(f *PlayFake)
	// EmitCasts makes every online bot of the party cast on each fight tick: one spell_go row per bot in the scripted
	// combat log (CombatRows), the evidence the spell judge reads. The dry command sets it; the rows are the fake's own.
	EmitCasts bool
	combat    [][]byte
	// SightFunc, when set, is the fake's line of sight (true = in sight): it answers the agent's Sight port and makes the
	// module's attack order refuse the way the real one does when the tank does not see its mark (out of
	// PlayModuleSightYd or out of sight from where the tank stands): the order is logged, the tank does nothing.
	// BlindOrders counts those refusals.
	SightFunc   func(from, to Vec3) bool
	BlindOrders int
	// ModuleSight, when set, is the module's own line of sight in place of SightFunc: the agent's Sight port still
	// answers from SightFunc, so a test scripts the two disagreeing (the live 2026-11-24 tunnel pull: the navmesh ray
	// said clear at 21 yd, the core's line of sight did not). RefuseOrders is how many of the tank's next attack orders
	// the module refuses whatever the sight (a mark the module did not find yet). Every refusal whispers the module's
	// "I dont see my rti attack target" to the agent; SightRefusals counts them per bot.
	ModuleSight  func(from, to Vec3) bool
	RefuseOrders int
	// WhisperOnce scripts the live module's one whisper for a mark it cannot find: the order is still refused, but the
	// Refusals count stands at one, as it did live (three orders, one whisper, 2026-11-24).
	WhisperOnce bool
	refusals    map[string]int
	// AttackCode, when set, is asked for the unit the tank's attack order lands on: a code it returns is the module's
	// refusal of that order (the live `not-attackable` on a friendly quest giver) and the order does nothing.
	AttackCode func(target wire.GUID) string
	// NonHostileFactions are the faction templates the fake's Reactions port answers friendly or neutral; any other
	// faction is hostile, but the ones it does not know.
	NonHostileFactions map[int32]bool
	// UnknownFactions are the faction templates the fake's Reactions port does not know (known false: the unit's data
	// has not arrived). The zero faction is unknown like the real adapter's, never friendly.
	UnknownFactions map[int32]bool
	// NoAggro is how many of a unit's aggro events the fake swallows, by guid: a mob that never joins the fight (a
	// pillar between it and the party), or one that evades when it is ordered. Each due pack's settle skips the unit
	// and counts one off; when the count is spent the unit turns on the party as usual.
	NoAggro map[wire.GUID]int
	// OnRezPoll is asked on every poll of the resurrect offer: a test scripts a death during the character's wait.
	OnRezPoll func(f *PlayFake)
	// OnUnits, when set, is asked before every read of the view: a test scripts a unit that leaves a snapshot or two (a
	// delayed push, a range transition) with RemoveUnit and puts it back with AddUnit. The hook must not read the view.
	OnUnits func(f *PlayFake)
}

// The fake's fight numbers: a scripted unit's health when a test gave it none, and what the group takes off a unit
// per tick of the agent's fight. They are the fake's own, not the server's.
const (
	playMobHP      = 2000
	playHitPerTick = 700
)

// pendingAggro is one pack's turn on the party, due at a.
type pendingAggro struct {
	at     time.Time
	target wire.GUID
	guids  []wire.GUID
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
		NoAnswer: map[string]bool{}, RosterOffline: map[string]bool{}, Rolls: map[wire.GUID][]string{}, NoLoot: map[wire.GUID]bool{},
		NoAggro: map[wire.GUID]int{}, UseNoChange: map[uint32]bool{},
		BotPower: map[string][3]int{}, BotRegen: map[string]int{}, powerAt: map[string]time.Time{},
		selfHP: [2]int{1000, 1000},
	}
	f.Module = &PlayModule{GoldenOwner: owner, f: f}
	f.selfGUID = wire.GUID{Low: 1}
	if route.Portal != nil {
		f.SelfPos = route.Portal.Pos
		f.GoldenOwner.SelfMap = route.Portal.Map
		f.Graveyard = Vec3{route.Portal.Pos[0] + 40, route.Portal.Pos[1], route.Portal.Pos[2]}
	} else {
		f.SelfPos = route.Entrance
		f.GoldenOwner.SelfMap = route.Map
		f.Graveyard = Vec3{route.Entrance[0] + 40, route.Entrance[1], route.Entrance[2]}
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

// SetSelfHP gives the agent's own character its health and max health (the roster row the agent's own pull reads).
func (f *PlayFake) SetSelfHP(hp, maxHP int) { f.selfHP = [2]int{hp, maxHP} }

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

// AddUnit puts one scripted unit in the view; a hostile a test gave no health gets playMobHP.
func (f *PlayFake) AddUnit(u Unit) {
	if !u.Friendly && !u.Dead && u.MaxHP == 0 {
		u.HP, u.MaxHP = playMobHP, playMobHP
	}
	f.units = append(f.units, u)
}

// SpawnPacks puts one unit of every plan group in the view, on the group's own positions: the fake's dungeon holds
// all of its packs from the start, and the run walks up to them.
func (f *PlayFake) SpawnPacks() {
	for _, g := range f.Route.AllGroups() {
		for i, e := range g.Entries {
			if e == VoidwalkerEntry {
				continue // a warlock bot's summoned pet: it has no spawn of its own in the view
			}
			pos := g.Centre
			if len(g.Positions) > 0 {
				pos = g.Positions[i%len(g.Positions)]
			}
			f.AddUnit(Unit{GUID: f.newGUID(e), Entry: e, Pos: pos})
		}
	}
}

// SpawnBosses puts one live unit of every plan boss in the view, on its plan position.
func (f *PlayFake) SpawnBosses() {
	for _, leg := range f.Route.Legs {
		if leg.Boss == nil {
			continue
		}
		f.AddUnit(Unit{GUID: f.newGUID(leg.Boss.Entry), Entry: leg.Boss.Entry, Pos: leg.Boss.Pos})
	}
}

// newGUID gives a scripted hostile of entry its guid: a creature guid's high half, a counter in the low one.
func (f *PlayFake) newGUID(entry uint32) wire.GUID {
	return wire.GUID{High: 0xF130000000000000, Low: 0x50000 + uint64(len(f.units)) + uint64(entry)}
}

// UnitOf finds a scripted unit of that entry: the first one still alive, else the first.
func (f *PlayFake) UnitOf(entry uint32) (Unit, bool) {
	var first *Unit
	for i := range f.units {
		if f.units[i].Entry != entry {
			continue
		}
		if !f.units[i].Dead {
			return f.units[i], true
		}
		if first == nil {
			first = &f.units[i]
		}
	}
	if first == nil {
		return Unit{}, false
	}
	return *first, true
}

// RemoveUnit drops a unit from the view: a boss that vanishes before its corpse is seen.
func (f *PlayFake) RemoveUnit(guid wire.GUID) {
	out := f.units[:0]
	for _, u := range f.units {
		if u.GUID != guid {
			out = append(out, u)
		}
	}
	f.units = out
}

// MoveUnit moves one scripted unit to pos: a patrol's step, or a unit a test walks off during a hold.
func (f *PlayFake) MoveUnit(guid wire.GUID, pos Vec3) {
	for i := range f.units {
		if f.units[i].GUID == guid {
			f.units[i].Pos = pos
		}
	}
}

// SetBotPower scripts a bot's power type (0 mana, 1 rage, 2 focus, 3 energy) and its value and maximum.
func (f *PlayFake) SetBotPower(name string, ptype, power, maxPower int) {
	f.BotPower[strings.ToLower(name)] = [3]int{ptype, power, maxPower}
}

// Encounter puts one instance encounter event in the world's queue: the next read answers it.
func (f *PlayFake) Encounter(e EncounterEvent) { f.encounters = append(f.encounters, e) }

// AggroUnit turns one unit and its pack on the party at once (a test's add).
func (f *PlayFake) AggroUnit(guid wire.GUID) { f.engage(guid, 0) }

// engage turns a unit's pack on the party after d: the unit and every living unit of its plan group target the tank.
// A unit no plan group claims (a boss, an add) turns on the party alone.
func (f *PlayFake) engage(guid wire.GUID, d time.Duration) {
	tank := wire.GUID{Low: 0x200}
	if len(f.GoldenOwner.Bots) > 0 {
		if g, ok := f.PlayerGUID(f.GoldenOwner.Bots[0].Name); ok {
			tank = g
		}
	}
	f.pending = append(f.pending, pendingAggro{at: f.Clock.Now().Add(d), target: tank, guids: f.packOf(guid)})
	f.settle()
}

// settle applies the packs whose aggro is due: every living unit of them that is not already fighting turns on the
// party. A unit keeps a target that is already fighting for the party (an add does not take a fighting pack's member),
// but the bots' order takes the mob off the character: the module's own threat, as the tank's order does on the server.
func (f *PlayFake) settle() {
	f.chase()
	now := f.Clock.Now()
	rest := f.pending[:0]
	for _, a := range f.pending {
		if now.Before(a.at) {
			rest = append(rest, a)
			continue
		}
		for i := range f.units {
			u := &f.units[i]
			if u.Dead || u.Friendly || !slices.Contains(a.guids, u.GUID) || (!u.Target.IsZero() && u.Target != f.selfGUID) {
				continue
			}
			if n := f.NoAggro[u.GUID]; n > 0 {
				f.NoAggro[u.GUID] = n - 1
				continue
			}
			u.Target = a.target
			if f.AggroAt.IsZero() {
				f.AggroAt = now
			}
		}
	}
	f.pending = rest
}

// playMobChaseYd is how fast a mob that is fighting the character moves toward it (a player's run speed, the walk's
// own 7 yd/s), playMobReachYd is how near it comes (a body's reach), and playMeleeReachYd is a melee body's own reach:
// the character's swing only touches a mob that stands inside it.
const (
	playMobChaseYd   = 7.0
	playMobReachYd   = 2.0
	playMeleeReachYd = 5.0
)

// playAssistYd is the fake's stand-in for the core's creature assistance radius (10 yd): a hostile within it of the
// unit the character hits is drawn in by the hit too, as the server's social aggro does.
const playAssistYd = 10.0

// engageAgent is the server's own answer to the character's swing: the hostile unit the character swings at inside its
// own reach turns on the character, and so do its pack-mates and any other hostile within playAssistYd of it (the
// server's social aggro), unless something else already holds them or their pack's aggro is still due (the module's
// pull). A unit the scripted pull's NoAggro swallows does not hear the character's swing either.
func (f *PlayFake) engageAgent(guid wire.GUID) {
	hit, ok := unitByGUID(f.units, guid)
	if !ok || hit.Dead || hit.Friendly || dist3(hit.Pos, f.SelfPos) > playMeleeReachYd {
		return
	}
	for _, a := range f.pending {
		if slices.Contains(a.guids, guid) {
			return
		}
	}
	join := map[wire.GUID]bool{guid: true}
	for _, g := range f.packOf(guid) {
		join[g] = true
	}
	for _, u := range f.units {
		if u.Dead || u.Friendly || f.bossEntry(u.Entry) {
			continue // a plan boss is the run's own boss pull, not a neighbour drawn in by a swing
		}
		if dist3(u.Pos, hit.Pos) <= playAssistYd {
			join[u.GUID] = true
		}
	}
	for i := range f.units {
		u := &f.units[i]
		if !join[u.GUID] || u.Dead || u.Friendly || !u.Target.IsZero() {
			continue
		}
		if u.GUID != guid && f.NoAggro[u.GUID] > 0 {
			continue
		}
		due := false
		for _, a := range f.pending {
			if slices.Contains(a.guids, u.GUID) {
				due = true
				break
			}
		}
		if due {
			continue
		}
		u.Target = f.selfGUID
		if f.AggroAt.IsZero() {
			f.AggroAt = f.Clock.Now()
		}
	}
}

// bossEntry says whether an entry is one of the route's bosses: a boss is pulled by the run's own boss flow (and its
// adds' marks), never drawn in as a swing's social neighbour.
func (f *PlayFake) bossEntry(entry uint32) bool {
	for _, leg := range f.Route.Legs {
		if leg.Boss != nil && leg.Boss.Entry == entry {
			return true
		}
	}
	return false
}

// chase moves every mob that is fighting the character toward it at playMobChaseYd, up to playMobReachYd: the mob a
// pulled character runs from follows it to the party.
func (f *PlayFake) chase() {
	now := f.Clock.Now()
	last := f.chaseAt
	f.chaseAt = now
	if last.IsZero() {
		return
	}
	step := now.Sub(last).Seconds() * playMobChaseYd
	if step <= 0 {
		return
	}
	for i := range f.units {
		u := &f.units[i]
		if u.Dead || u.Friendly || u.Target != f.selfGUID {
			continue
		}
		d := dist3(u.Pos, f.SelfPos)
		if d <= playMobReachYd {
			continue
		}
		f.units[i].Pos = lerpVec(u.Pos, f.SelfPos, math.Min(1, step/d))
	}
}

// packOf is the units one unit's plan group claims, or the unit alone when no group holds it.
func (f *PlayFake) packOf(guid wire.GUID) []wire.GUID {
	idx := -1
	for i := range f.units {
		if f.units[i].GUID == guid {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	groups := f.Route.AllGroups()
	claim := MatchPacks(f.units, groups, len(groups))
	gid, ok := claim.GroupOf(idx)
	if !ok {
		return []wire.GUID{guid}
	}
	out := make([]wire.GUID, 0, len(claim.Members(gid)))
	for _, ui := range claim.Members(gid) {
		out = append(out, f.units[ui].GUID)
	}
	return out
}

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

// Add is `.bot add`. The core refuses a sixth member of a party: until the group is converted to a raid, an add that
// would make it larger than party.MaxPartyMembers is refused with `party-full`.
func (m *PlayModule) Add(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbAdd, name)
	if !m.f.RaidGroup && m.f.partyMembers() >= party.MaxPartyMembers {
		return m.GoldenOwner.refuse(botcmd.VerbAdd, name, "party-full", "your party is full: convert it to a raid first")
	}
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

// Attack is `.bot attack`: the module takes the requester's selected unit, so the fake turns that unit's pack on the
// party the way a pulled pack does.
func (m *PlayModule) Attack(ctx context.Context, name string) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbAttack, name)
	if m.f.AttackCode != nil {
		if code := m.f.AttackCode(m.f.Selected); code != "" {
			return m.GoldenOwner.refuse(botcmd.VerbAttack, name, code, "the core says this bot may not attack that unit")
		}
	}
	rep, err := m.GoldenOwner.Attack(ctx, name)
	if err == nil {
		if m.f.tankBlind(name) {
			m.f.BlindOrders++
			// The live module whispers its sight refusal once for a mark it cannot find (2026-11-24: three orders, one
			// whisper): WhisperOnce scripts that, so a test reads the pull's own refusal bounds, not the fake's answer.
			if m.f.WhisperOnce && m.f.refusals[strings.ToLower(name)] > 0 {
				return rep, err
			}
			if m.f.refusals == nil {
				m.f.refusals = map[string]int{}
			}
			m.f.refusals[strings.ToLower(name)]++
			return rep, err
		}
		m.f.engage(m.f.Selected, m.f.AggroDelay)
	}
	return rep, err
}

// tankBlind says whether the order's bot is the tank and the module would not find its mark: the real module's
// RtiTargetValue::CalculateMark fails on range or line of sight from the bot's own position.
func (f *PlayFake) tankBlind(name string) bool {
	if len(f.GoldenOwner.Bots) == 0 || !strings.EqualFold(name, f.GoldenOwner.Bots[0].Name) {
		return false
	}
	if f.RefuseOrders > 0 {
		f.RefuseOrders--
		return true
	}
	sight := f.SightFunc
	if f.ModuleSight != nil {
		sight = f.ModuleSight
	}
	if sight == nil {
		return false
	}
	u, ok := unitByGUID(f.units, f.Selected)
	if !ok {
		return false
	}
	from := f.botPos(f.GoldenOwner.bot(name))
	// The module reads the bot's own unit; a scripted unit of the tank (a live position the table's rule lacks) is
	// what it sees.
	if g, ok := f.PlayerGUID(name); ok {
		if bu, ok := unitByGUID(f.units, g); ok && !bu.Dead {
			from = bu.Pos
		}
	}
	return dist3(from, u.Pos) > PlayModuleSightYd || !sight(from, u.Pos)
}

// SightRefusals is the Refusals port: how many "I dont see my rti attack target" whispers the bot sent so far.
func (f *PlayFake) SightRefusals(bot string) int { return f.refusals[strings.ToLower(bot)] }

// InSight is the Sight port over SightFunc; without one the fake cannot say.
func (f *PlayFake) InSight(from, to Vec3) (bool, bool) {
	if f.SightFunc == nil {
		return false, false
	}
	return f.SightFunc(from, to), true
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

// Whisper says one word of the chat grammar to a bot. A bot that does not answer the corpse-run whisper (NoAnswer)
// stays a ghost: the recovery then runs out of time.
func (m *PlayModule) Whisper(ctx context.Context, name, word string) error {
	norm, err := botcmd.Whisper(name, word)
	if err != nil {
		return err
	}
	m.f.act("whisper " + norm + ": " + word)
	key := strings.ToLower(norm)
	if m.f.NoAnswer[key] {
		return nil
	}
	if err := m.GoldenOwner.Whisper(ctx, norm, word); err != nil {
		return err
	}
	if word == botcmd.WhisperCorpseRun {
		m.f.Moved[key] = true // it runs back to its owner's side
	}
	return nil
}

// Who is `.bot who`.
func (m *PlayModule) Who(ctx context.Context) (botcmd.Reply, error) {
	m.f.send(botcmd.VerbWho, "")
	return m.GoldenOwner.Who(ctx)
}

// --- GroupPort ---

// Roster is the party: the agent plus every online bot of the table that is in it. A frozen roster (FreezeRoster)
// answers what the party said when it was frozen until a member stats request merges the live table again.
func (f *PlayFake) Roster() []PartyMember {
	if f.frozen != nil {
		return append([]PartyMember(nil), f.frozen...)
	}
	return f.rosterLive()
}

// rosterLive is the party as the fake's own table stands.
func (f *PlayFake) rosterLive() []PartyMember {
	out := []PartyMember{{
		Name: f.SelfName, GUID: f.selfGUID, Online: true, Dead: f.SelfPhase != PhaseAlive,
		Ghost: f.SelfPhase == PhaseGhost, HP: f.selfHP[0], MaxHP: f.selfHP[1], Pos: f.SelfPos, Map: f.SelfMap,
	}}
	for _, b := range f.GoldenOwner.Bots {
		key := strings.ToLower(b.Name)
		off := f.RosterOffline[key]
		if !b.InParty || (!b.Online && !off) {
			continue
		}
		hp, maxHP := 900, 900
		if v, ok := f.BotHP[key]; ok {
			hp, maxHP = v[0], v[1]
		}
		// Rage unless the test scripted a pool: a bot short of nothing is always ready to go on.
		p := [3]int{1, 100, 100}
		if v, ok := f.BotPower[key]; ok {
			p = v
			if rate := f.BotRegen[key]; rate > 0 {
				if at, ok := f.powerAt[key]; ok {
					p[1] = min(p[2], p[1]+rate*int(f.Clock.Now().Sub(at)/time.Second))
					f.BotPower[key] = p
				}
				f.powerAt[key] = f.Clock.Now()
			}
		}
		out = append(out, PartyMember{
			Name: b.Name, GUID: f.botGUID(b.Name), Online: b.Online && !off, Dead: b.Dead, Ghost: b.Ghost,
			HP: hp, MaxHP: maxHP, PowerType: p[0], Power: p[1], MaxPower: p[2],
			Pos: f.botPos(b), Map: b.Map,
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

// FreezeRoster freezes the party roster as it stands: every later Roster answers those rows — what a member was last
// seen as (dead, far, on a map the run cannot place) — until a member stats request merges the live table again. The
// fake's stand-in for a server that pushed nothing after a release, a resurrection and a summon, one likely reading of
// Run B (2026-11-24; the run log has no roster dump).
func (f *PlayFake) FreezeRoster() {
	f.frozen = append([]PartyMember(nil), f.rosterLive()...)
}

// partyMembers is the group's size as the core would count it: this character and every bot in the party.
func (f *PlayFake) partyMembers() int {
	n := 1
	for _, b := range f.GoldenOwner.Bots {
		if b.InParty {
			n++
		}
	}
	return n
}

// MemberFacts is the dump's raw rows: the fake has one source (its own party table), so each row carries the roster
// reading only.
func (f *PlayFake) MemberFacts() []MemberFacts {
	roster := f.Roster()
	out := make([]MemberFacts, 0, len(roster))
	for _, m := range roster {
		out = append(out, MemberFacts{
			Name: m.Name, GUID: m.GUID,
			RosterOnline: m.Online, RosterDead: m.Dead, RosterGhost: m.Ghost,
			RosterHP: m.HP, RosterMaxHP: m.MaxHP, RosterPos: m.Pos,
		})
	}
	return out
}

// RequestStats is the request that refreshes the roster: the fake answers by serving the live table again.
func (f *PlayFake) RequestStats(ctx context.Context, guids []wire.GUID) error {
	f.StatsRequests++
	f.frozen = nil
	return nil
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

// ConvertRaid is the group layer's convert_raid (SPEC #108 R4b): the party becomes a raid, and `.bot add` then takes
// more than five members (before it, a sixth add is refused the way the core refuses it).
func (f *PlayFake) ConvertRaid(ctx context.Context) error {
	f.ConvertCalls++
	f.act("convert-raid")
	if f.ConvertErr != nil {
		return f.ConvertErr
	}
	f.RaidGroup = true
	return nil
}

// RaidMove is the group layer's raid_move: the member goes to its subgroup, or MoveErr answers.
func (f *PlayFake) RaidMove(ctx context.Context, name string, subgroup int) error {
	f.act("raid-move " + name + " " + strconv.Itoa(subgroup))
	if f.MoveErr != nil {
		return f.MoveErr
	}
	f.RaidMoves = append(f.RaidMoves, RaidSlot{Name: name, Subgroup: subgroup})
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

// Select is the agent's own selection (recorded as a "select <guid>" action, the order of `.bot attack` it is for).
func (f *PlayFake) Select(guid wire.GUID) error {
	f.Selected = guid
	f.act("select " + guid.String())
	return nil
}

// Attack starts the agent's own attack: the unit turns on the character unless something else already holds it (the
// module's own pull is the business of the pack's aggro, not the fake's).
func (f *PlayFake) Attack(guid wire.GUID) error {
	f.Attacked = guid
	f.agentAttacks = append(f.agentAttacks, guid)
	f.engageAgent(guid)
	return nil
}

// StopAttack ends it.
func (f *PlayFake) StopAttack() error { f.Attacked = wire.GUID{}; return nil }

// FightTick is one tick of the agent's fight: the units the group turned on lose health, and one that runs out dies
// and clears its target. OnFightTick runs first, so a test can script an add or a removal at that moment.
func (f *PlayFake) FightTick(ctx context.Context, target wire.GUID) error {
	if f.OnFightTick != nil {
		f.OnFightTick(f)
	}
	f.settle()
	if f.EmitCasts {
		f.emitCasts()
	}
	for i := range f.units {
		u := &f.units[i]
		if u.Dead || u.Friendly || u.Target.IsZero() {
			continue
		}
		if !f.friendTarget(u.Target) {
			continue
		}
		u.HP -= playHitPerTick
		if u.HP <= 0 {
			u.HP, u.Dead, u.Target = 0, true, wire.GUID{}
		}
	}
	return nil
}

// playCasts is the one spell each class of the fake's bots casts: an ability of that class in the shared spell table.
// A class the table lacks casts nothing.
var playCasts = map[string]struct {
	id   int
	name string
}{
	"warrior": {78, "Heroic Strike"},
	"rogue":   {1752, "Sinister Strike"},
	"priest":  {2050, "Lesser Heal"},
	"mage":    {133, "Fireball"},
}

// SetBotClass gives a bot of the table its class (the `who` row and the spell it casts).
func (f *PlayFake) SetBotClass(name, class string) {
	if b := f.GoldenOwner.bot(name); b != nil {
		b.Class = class
	}
}

// emitCasts appends one spell_go row for every online bot in the party, as the session's combat log would write it.
func (f *PlayFake) emitCasts() {
	now := f.Clock.Now()
	// The agent's own swing: a row that names the player's character, so redaction has something to hide.
	f.combat = append(f.combat, []byte(fmt.Sprintf(`{"seq":%d,"kind":"swing","time":%q,"mono_ms":%d,"source":{"guid":%q,"name":%q},"target":{"guid":"","name":"Mob"},"amount":10}`,
		len(f.combat)+1, now.Format("2006-01-02T15:04:05.000-07:00"), now.UnixMilli(), f.selfGUID.String(), f.SelfName)))
	for _, b := range f.GoldenOwner.Bots {
		if !b.Online || !b.InParty || b.Dead || b.Ghost {
			continue
		}
		c, ok := playCasts[strings.ToLower(b.Class)]
		if !ok {
			continue
		}
		row := fmt.Sprintf(`{"seq":%d,"kind":"spell_go","time":%q,"mono_ms":%d,"source":{"guid":%q,"name":%q},"target":{"guid":"","name":""},"spell_id":%d,"spell_name":%q}`,
			len(f.combat)+1, now.Format("2006-01-02T15:04:05.000-07:00"), now.UnixMilli(), f.botGUID(b.Name).String(), b.Name,
			c.id, c.name)
		f.combat = append(f.combat, []byte(row))
	}
}

// CombatRows is the scripted combat log: the rows above seq, in order. It never drops a row.
func (f *PlayFake) CombatRows(seq, limit int) ([]json.RawMessage, int, int, int) {
	if len(f.combat) == 0 {
		return nil, seq, 0, 0
	}
	if limit <= 0 || limit > combatPageMax {
		limit = combatPageMax
	}
	start := max(seq, 0)
	if start >= len(f.combat) {
		return nil, seq, 1, 0
	}
	end := min(len(f.combat), start+limit)
	rows := make([]json.RawMessage, 0, end-start)
	for _, row := range f.combat[start:end] {
		rows = append(rows, json.RawMessage(row))
	}
	return rows, end, 1, 0
}

// friendTarget says whether a unit's target is a party member.
func (f *PlayFake) friendTarget(g wire.GUID) bool {
	if g == f.selfGUID {
		return true
	}
	for _, b := range f.GoldenOwner.Bots {
		if b.Online && b.InParty && f.botGUID(b.Name) == g {
			return true
		}
	}
	return false
}

// Rest sits the agent down; the fake's party is always rested.
func (f *PlayFake) Rest(ctx context.Context, limit time.Duration) error {
	if f.OnRest != nil {
		f.OnRest(f)
	}
	return nil
}

// Phase is the character's state.
func (f *PlayFake) Phase() PlayerPhase { return f.SelfPhase }

// Release makes the character a ghost at the graveyard on the plan's portal map.
func (f *PlayFake) Release() error {
	f.Released, f.SelfPhase = true, PhaseGhost
	if p, mapID := f.portal(); p != nil {
		f.SelfPos, f.GoldenOwner.SelfMap = f.Graveyard, mapID
	}
	f.act("release")
	return nil
}

// portal is the plan's portal point and map, nil when the plan has none.
func (f *PlayFake) portal() (*Vec3, uint32) {
	if f.Route.Portal == nil {
		return nil, 0
	}
	return &f.Route.Portal.Pos, f.Route.Portal.Map
}

// AcceptRez answers the offer that is up and brings the character back.
func (f *PlayFake) AcceptRez() error {
	f.Rezzed, f.SelfPhase, f.Offer = true, PhaseAlive, nil
	f.act("accept-rez")
	return nil
}

// RezOffer is the offer that is up.
func (f *PlayFake) RezOffer() (RezOffer, bool) {
	if f.OnRezPoll != nil {
		f.OnRezPoll(f)
	}
	if f.Offer == nil {
		return RezOffer{}, false
	}
	return *f.Offer, true
}

// DeclineRez refuses the offer that is up.
func (f *PlayFake) DeclineRez() error {
	f.Declined++
	f.Offer = nil
	f.act("decline-rez")
	return nil
}

// Loot opens one corpse: a corpse the character may not loot answers ErrCannotLoot, and every roll open on the one it
// opens is answered greed (D-9).
func (f *PlayFake) Loot(ctx context.Context, guid wire.GUID) error {
	if f.LootFail[guid] > 0 {
		f.LootFail[guid]--
		return fmt.Errorf("loot failed for %s", guid)
	}
	if f.NoLoot[guid] {
		f.LootRefused = append(f.LootRefused, guid)
		return ErrCannotLoot
	}
	f.Looted = append(f.Looted, guid)
	for _, item := range f.Rolls[guid] {
		f.Votes = append(f.Votes, PlayVote{Corpse: guid, Item: item, Vote: "greed"})
	}
	f.act("loot " + guid.String())
	return nil
}

// UseObject uses one game object: it becomes active unless UseNoChange names its entry.
func (f *PlayFake) UseObject(ctx context.Context, entry uint32, stand Vec3) error {
	f.Used = append(f.Used, entry)
	f.act("use " + strconv.FormatUint(uint64(entry), 10))
	if f.UseNoChange[entry] {
		return nil
	}
	for i := range f.objects {
		if f.objects[i].Entry == entry {
			f.objects[i].State = "active"
		}
	}
	return nil
}

// Gossip picks one gossip option: every talk is recorded, and a menu the test scripts without the option (or a
// GossipErr) answers instead of the close.
func (f *PlayFake) Gossip(ctx context.Context, entry uint32, option int) error {
	f.Gossips = append(f.Gossips, option)
	if f.GossipErr != nil {
		return f.GossipErr
	}
	if f.GossipMenuTimeout > 0 {
		f.GossipMenuTimeout--
		return ErrNoGossipMenu
	}
	if f.GossipMissing > 0 {
		f.GossipMissing--
		return ErrNoGossipOption
	}
	return nil
}

// AddObject puts one scripted game object in the view; one a test gave no guid and no state gets them (ready).
func (f *PlayFake) AddObject(o PlayObject) {
	if o.State == "" {
		o.State = "ready"
	}
	if o.GUID.IsZero() {
		o.GUID = wire.GUID{High: 0xF110000000000000, Low: 0x70000 + uint64(len(f.objects)) + uint64(o.Entry)}
	}
	f.objects = append(f.objects, o)
}

// ObjectOf finds a scripted game object of that entry: the first one of the list.
func (f *PlayFake) ObjectOf(entry uint32) (PlayObject, bool) {
	for _, o := range f.objects {
		if o.Entry == entry {
			return o, true
		}
	}
	return PlayObject{}, false
}

// RemoveObject drops a game object from the view: a door the plan's wait reads as gone once it saw it.
func (f *PlayFake) RemoveObject(guid wire.GUID) {
	out := f.objects[:0]
	for _, o := range f.objects {
		if o.GUID != guid {
			out = append(out, o)
		}
	}
	f.objects = out
}

// Objects is the character's view of the game objects, as the steps read it.
func (f *PlayFake) Objects() []PlayObject { return f.objects }

// --- World ---

// Self is the agent's own position and map.
func (f *PlayFake) Self() (Vec3, uint32, uint64, bool) {
	return f.SelfPos, f.GoldenOwner.SelfMap, f.epoch, false
}

// Units is the scripted view, with the packs whose aggro is due settled first.
func (f *PlayFake) Units() []Unit {
	if f.OnUnits != nil {
		f.OnUnits(f)
	}
	f.settle()
	return f.units
}

// HostileToPlayer is the Reactions port: every faction is hostile but the ones a test listed in NonHostileFactions;
// the zero faction and the ones in UnknownFactions are not known (a unit whose data has not arrived), as the real
// adapter answers for a template the view does not carry.
func (f *PlayFake) HostileToPlayer(u Unit) (hostile, known bool) {
	if u.Faction <= 0 || f.UnknownFactions[u.Faction] {
		return false, false
	}
	return !f.NonHostileFactions[u.Faction], true
}

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
	// The core resurrects a ghost whose corpse is in the instance the moment it arrives (§3.10).
	if f.SelfPhase == PhaseGhost {
		f.SelfPhase = PhaseAlive
	}
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

// Encounters answers the events a test queued and forgets them: a boss whose corpse the view never shows still
// counts dead from its encounter's end.
func (f *PlayFake) Encounters() []EncounterEvent {
	out := f.encounters
	f.encounters = nil
	return out
}

// EncounterOpen is always false.
func (f *PlayFake) EncounterOpen() bool { return false }

// --- Walker ---

// Path is a straight line: the fake does not use the map data. A test's PathFunc answers instead.
func (f *PlayFake) Path(from, to Vec3) (navmesh.Route, error) {
	if f.PathFunc != nil {
		return f.PathFunc(from, to), nil
	}
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
	if f.WalkBlock != nil {
		return f.WalkBlock(ctx)
	}
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

// ClearTarget makes one unit stop targeting anyone while it stays alive (an evade or a reset, a test's script).
func (f *PlayFake) ClearTarget(guid wire.GUID) {
	for i := range f.units {
		if f.units[i].GUID == guid {
			f.units[i].Target = wire.GUID{}
		}
	}
}
