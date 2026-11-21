package world

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// Options is what the Battle.net login left plus the character to enter with.
type Options struct {
	Address         string
	RealmID         uint32
	RealmAddress    uint32
	RealmJoinTicket []byte
	ClientSecret    []byte
	JoinSecret      []byte
	ClientBuild     uint32
	RealmBuild      uint32
	// BuildAuthKey is auth.build_auth_key for the ticket's variant when the caller has it; empty makes the world
	// socket send the zero digest a server accepts when it does not check the build key.
	BuildAuthKey []byte
	// WorldConn is a realm socket the login's build probe already authenticated (bnet.Options.WorldAccept). Enter
	// and the character screen adopt it instead of authenticating again, and close it with the session. The socket
	// belongs to this Options once set: nothing else may close it. Nil means authenticate from the fields above.
	WorldConn *worldconn.Realm
	Character string        // character name (the surname may be omitted)
	Stay      time.Duration // how long to stay in the world after the login burst
	Deadline  time.Duration // whole run; default 2 minutes + Stay + Settle
	// WalkCheck runs round 3's walk (forever walk-check): look at the world for Settle (the command's --stay), walk
	// Dx/Dy yards from the spawn, wait WalkPause, walk back, report, log out. There is no plain stay after it; the
	// settle is a stay-like phase and counts in the default Deadline like Stay.
	WalkCheck bool
	Dx, Dy    float32
	WalkPause time.Duration
	// Settle is the look at the world before a check command does its work (walk-check's --stay); 0 means the
	// command's own default.
	Settle time.Duration
	// NavmeshDir (forever walk-check --navmesh) makes the walk a navmesh route to the absolute target
	// ToX/ToY/ToZ on the current map, instead of the Dx/Dy offset walk and back. Empty keeps the old walk.
	NavmeshDir string
	// Parents is the server's instance rule for the navmesh walk (walk-check --parent MAP=PARENT): an instance
	// map's missing tile comes from that one parent map's tile file (MMapManager.cpp:349-357).
	Parents map[uint32]uint32
	ToX     float32
	ToY     float32
	ToZ     float32
	// FightCheck runs round 4's fight (forever fight-check): settle, choose a target (Entry when set), attack it for
	// FightFor, cast the known spells the plan picks, report, log out.
	FightCheck bool
	Entry      int32
	FightFor   time.Duration
	// LevelMin and LevelMax are the level band fight-check accepts a target in (--level-min/--level-max). 0 means
	// the band relative to the character's own level (fightLevelWindow): three levels below to two above, clamped
	// to the client's 1-60.
	LevelMin, LevelMax int32
	// Round 6: death-check. It settles, waits for the character to die (a console command or a mob does that; the
	// client never kills itself), then releases, finds the corpse, runs back and reclaims it, or waits for a
	// resurrection offer and accepts it (AcceptResurrect), or uses the spirit healer (UseSpiritHealer).
	DeathCheck bool
	// Act is the `forever act` step script (forever-world-r0). Nil runs none of it; a non-nil script runs instead
	// of the stay, then the command logs out like every other one-shot mode.
	Act             []ActStep
	AcceptResurrect bool
	// AcceptResurrectFrom is --accept-resurrect-from NAME: with AcceptResurrect it answers only an offer whose
	// offerer the model knows by that name. Empty accepts an offer from any GUID, which is the default: the server
	// already decides who may offer a resurrection (MiscHandler.cpp:520-521).
	AcceptResurrectFrom string
	UseSpiritHealer     bool
	// DeathWait bounds how long death-check waits for the character to die; ResurrectWait how long --accept-resurrect
	// waits for an offer before it falls back to the release and the corpse run.
	DeathWait     time.Duration
	ResurrectWait time.Duration
	// CaptureDir is a debug option (--capture-dir; keep it outside the repository): every packet the combat, chat
	// and party handlers see is written to a new run directory under it (see capture_run.go for the layout, the
	// exclusive files and the bounds). The directory holds the run's identifiers, so the fixture generators rewrite
	// them before a payload becomes a fixture.
	CaptureDir string
	// Provoke is a debug option (--provoke): before the fight, provoke the refusals a fight log otherwise has no
	// real packet for — a swing from out of range and a self cast at no rage — so their traffic can be captured.
	Provoke bool
	// AllowInstanceRedirect lets SMSG_CONNECT_TO name a host other than the realm socket's; refused by default.
	AllowInstanceRedirect bool
	// Round 5: chat-check and party-check. The two modes are exclusive.
	ChatCheck        bool
	Say              string
	Whisper          string // NAME:TEXT
	EmoteText        string
	Listen           time.Duration
	PartyCheck       bool
	PartyInvite      string
	PartyAcceptFrom  string
	PartyDeclineFrom string
	PartyUninvite    string
	// MapCheck runs round A7's map check (forever map-check): settle, print the map, send one area trigger when
	// AreaTrigger is set, then stay in the world and follow every map change the server makes.
	MapCheck    bool
	AreaTrigger int32
	// WaitJoin makes the inviting side wait this long for the group to form before the stats/say/stay steps; without
	// it an invite-only run ends after the invite answer.
	WaitJoin  time.Duration
	PartyStay time.Duration
	// Watch prints the world model's position of one player once a second.
	Watch string
	// Round A0: forever addon-check. AddonCheck enters with the add-on transport: it registers AddonPrefix, sends
	// AddonSend once on AddonChannel (a chat-kind word: party, raid, instance, guild, officer, whisper:NAME; empty
	// means no send; channel:NAME is refused as not supported yet), then listens AddonListen. Listen also bounds
	// the plain listen.
	AddonCheck   bool
	AddonPrefix  string
	AddonSend    string
	AddonChannel string
	AddonListen  time.Duration
	// AddonInvite/AddonAcceptFrom form the two-client party for the add-on proof before the send: the same invite
	// and accept flow party-check uses, but the group is kept until the add-on listen ends and then left.
	AddonInvite     string
	AddonAcceptFrom string
	// Round A6: forever cmd-check. CommandCheck enters with the command channel; CommandLines are sent in order
	// after the GM guard; CommandTBState reads the typed state table; CommandQuiet and CommandTimeout override the
	// collector's defaults for every command of the run.
	CommandCheck   bool
	CommandLines   []string
	CommandTBState string
	CommandQuiet   time.Duration
	CommandTimeout time.Duration
	// Round A1: forever fb-check. FBCheck drives one WoW Forever Bots module: the module probe, FBAdd bots
	// ("class" or "class:role"), FBOrders (an order word per bot by whisper, "party:WORD" for a party word),
	// roster and detail through the add-on protocol, and FBDismiss (a name or "all", default "all").
	FBCheck   bool
	FBAdd     []string
	FBOrders  []string
	FBDismiss string
	// Round A2: forever fb-dungeon. FBDungeon drives the module's dungeon finder: fill the group with `.bot lfg`,
	// queue FBDungeonName (a dungeon word or "any") with FBDungeonRole as the player's own role, wait for the
	// module's `inside` and the client's own map change, hand the lead to the tank, watch FBDungeonWatch, then lead
	// off, leave when FBDungeonLeave is set and dismiss. Those closing steps run from a deferred cleanup on their
	// own context (2 min, apart from Deadline). The pacing fields are for tests; 0 keeps the run's own defaults
	// (2 s poll, 3 min form, 3 min entry, 30 s leave, 2 min cleanup).
	FBDungeon            bool
	FBDungeonName        string
	FBDungeonRole        string
	FBDungeonWatch       time.Duration
	FBDungeonLeave       bool
	FBDungeonPoll        time.Duration
	FBDungeonFormWait    time.Duration
	FBDungeonEnterWait   time.Duration
	FBDungeonLeaveWait   time.Duration
	FBDungeonCleanupWait time.Duration
	Logf                 func(format string, args ...any)
	// Round LOOT-1: forever loot-check and object-use. Entry (the fight's field) is the creature or game-object
	// entry both commands filter on. Roll is the vote loot-check sends on a group roll ("", "need", "greed" or
	// "pass"; empty sends none, so need is only ever sent when asked). LootMoney takes the window's copper,
	// TakeItems bounds how many items are taken (0 all), and LootRange is the search radius in yards (0: the
	// command's own default).
	LootCheck bool
	ObjectUse bool
	Roll      string
	LootMoney bool
	TakeItems int
	LootRange float32
	// At is --at x,y,z: the point a game-object target must be picked at when several objects of the entry are in
	// range (object-use, and loot-check's chest); AtRange is how close an object must stand to that point in yards
	// (0: objectAtRange).
	AtSet   bool
	AtX     float32
	AtY     float32
	AtZ     float32
	AtRange float32
	// Round D2: forever dungeon-run. DungeonRun plays one dungeon plan for the party the commander leads; Enter
	// hands the session to the driver until the run stops or the deadline ends it, then logs out. The driver's
	// ports are bound to this session (dungeon_session.go). A nil value runs none of it.
	DungeonRun *driver.Config
	// Round S7: forever dungeon-watch. DungeonWatch watches a run the server module leads: the same ports, PREPARE and
	// ENTER as a run, then the module's status and events and no order of ours. A nil value runs none of it.
	DungeonWatch *driver.Config
	// RouteWalk runs `forever route-walk`: enter the plan's instance, walk every selected leg on the navmesh, and
	// confirm every planned point with the server's own `.gps` position. A nil value runs none of it.
	RouteWalk *RouteWalkOptions
	// Serve makes this a long-running session: after the login burst the session serves the RPC socket on
	// Serve.Listener and stays in the world until the caller's context ends. A dropped link is logged in again
	// within ServeConfig's budget (a fresh session, rebuilt state), and only a spent budget ends the daemon. Stay
	// and the one-shot modes are ignored; Deadline, when above 0, still bounds the whole run.
	Serve *ServeConfig
}

func (o *Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

// Report is what the run observed.
type Report struct {
	Characters []Character
	Character  Character
	MapID      int32
	Position   [4]float32
	// InWorld is true once the server sent SMSG_MOVE_SET_ACTIVE_MOVER (the world the client sees) and the
	// completion was answered.
	InWorld         bool
	ActiveMoverGUID wire.GUID
	Stayed          time.Duration
	Counts          map[string]int // server packets by opcode name (unknown numbers as "wire 0xNNNNNN")
	TimeSyncs       int
	Pings           int
	Pongs           int
	Latency         time.Duration
	Logout          LogoutResponse
	LogoutComplete  bool
	// LinkLost is set when a socket closed (or the peer went silent) during the run: its side and the error are
	// what the command prints with the last server packet it had seen.
	LinkLost       bool
	LinkLostSide   string
	LinkLostDetail string
	LastPacket     string
	LastPacketAgo  time.Duration

	// Round 4: the fight, the spells the character knows, and the combat packets this client could not parse.
	Fight       FightReport
	KnownSpells []int32
	ParseErrors int
	// Captured is the number of packets written to the debug capture (0 when --capture-dir was not given).
	Captured int

	// Round 3: what the client understood of the world and where it walked.
	Objects       ObjectSummary
	Units         []UnitRow
	MonsterMoves  int
	MoveRelays    int
	Teleports     int
	MapChanges    int
	NamesAsked    int
	NamesFound    int
	Walked        bool
	WalkFrom      [4]float32
	WalkTo        [4]float32
	BackAt        [4]float32
	WalkClientPos [4]float32
	// The navmesh option's route (forever walk-check --navmesh DIR --to-x/y/z): the route kind, its
	// corner count and length, and the terrain flags of the corridor.
	NavPath        bool
	NavPathKind    string
	NavPathCorners int
	NavPathLength  float32
	NavPathFlags   string

	// Round 5: the chat log's summary and the party model at the end of the run.
	Chat  ChatSummary
	Party PartySnapshot

	// Round A0: the add-on transport's registered list and what it delivered.
	Addon AddonSummary
	// Round A1: the bot module run: the probe, the adds, the roster and its source, details, orders, dismiss.
	FB FBReport
	// Round A2: the dungeon run: the fill, the queue, the both-signal entry, the lead, the watch, the way out.
	FBDungeon FBDungeonReport

	// Round 6: the death flow's state and steps.
	Death DeathReport
	// Round A6: the command channel's runs, whether the GM guard confirmed, and the typed state table.
	Commands    []CommandRun
	GMConfirmed bool
	TBState     *TBStateTable
	// Round LOOT-1: the loot window loot-check opened and what the rolls did, and the object-use observation.
	Loot      LootReport
	ObjectUse ObjectUseReport
	// Round D2: the dungeon run's result (states, orders, summary, stop reason and exit), nil when the session
	// ran something else.
	Dungeon *driver.Result
	// Round S7: the watch's result, nil when the session ran something else.
	Watch *driver.Result
	// RouteWalk is the whole-plan walk's table, nil when the session ran something else.
	RouteWalk *RouteWalkReport
	// Round A7: the map changes the client followed. Difficulty is the last SMSG_WORLD_SERVER_INFO's difficulty,
	// AreaTriggers the area triggers sent, AbortedTransfers the transfers the server refused, and MapTransitions
	// one row per resolved change (map-check fills Objects for each).
	Difficulty       int16
	AreaTriggers     int
	AbortedTransfers int
	MapTransitions   []MapChangeRow
	// StaleMapPackets counts map-content packets read on the instance connection between the suspend answer and the
	// resume token and dropped instead of applied (they are not the new map's), and UnsolicitedTransfers the transfer
	// packets the server's own sequences cannot produce (both ignored, counted, rate-limited in the log).
	StaleMapPackets      int
	UnsolicitedTransfers int
}

// ObjectSummary counts the world model by object type, plus the update statistics.
type ObjectSummary struct {
	Map        uint32
	Objects    int
	Created    int
	Updated    int
	Destroyed  int
	OutOfRange int
	Updates    int
	// Partial counts objects with a block that was not decoded to its declared end; PartialKinds says which object
	// types and block forms those were and where their walk stopped; Unparsed counts the blocks that were skipped
	// whole, by object type and reason.
	Partial      int
	PartialKinds map[string]int
	Unparsed     map[string]int
	ByType       map[string]int
	// Resurrections counts units the model saw come back from the ghost state.
	Resurrections int
}

// UnitRow is one line of the nearest-units table.
type UnitRow struct {
	Type      string
	Entry     int32
	Name      string
	Level     int32
	Health    int64
	MaxHealth int64
	Distance  float32
}

// FightReport is what the fight-check observed: the target, the fight log, the damage both ways, the auras, and the
// fighter's own state at the end. It carries no identifiers: units are named by role in the log.
type FightReport struct {
	Target       wire.GUID
	TargetEntry  int32
	TargetName   string
	TargetLevel  int32
	TargetHealth int64
	TargetMax    int64
	Engaged      bool
	Killed       bool
	// Evaded is a fight the server ended with the target still alive (an evade, a leash or a hostile stop): the
	// command reports it as its own outcome and exits non-zero.
	Evaded      bool
	Swings      int
	Misses      int
	Casts       int
	Auras       int
	DamageDealt int32
	DamageTaken int32
	XPGain      int32
	// StaleCasts counts the cast packets dropped because they answered a Cast that had given up (a late START, GO or
	// failure of the quarantined generation): they never complete a later Cast of the same spell.
	StaleCasts    int
	LastSwingErr  string
	Events        []FightEvent
	CastRows      []FightRow
	AuraRows      []AuraRow
	SelfHealth    int64
	SelfMax       int64
	SelfPower     int32
	SelfMaxPower  int32
	SelfPowerType uint8
}

// Count is one line of the packet count.
type Count struct {
	Name  string
	Count int
}

// SortedCounts returns the packet counts by descending count, then name.
func (r *Report) SortedCounts() []Count {
	rows := make([]Count, 0, len(r.Counts))
	for name, n := range r.Counts {
		rows = append(rows, Count{name, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

const (
	sideRealm = iota
	sideInstance
)

type event struct {
	side int
	p    worldconn.Packet
	err  error
}

type session struct {
	opts     Options
	ctx      context.Context
	realm    *worldconn.Realm
	instance *worldconn.Instance
	events   chan event
	report   *Report
	start    time.Time

	// The two socket readers. pumpCancel stops them (it also unblocks a read through the context watcher of the
	// connection) and pumpDone is closed by each when it has returned; Enter waits for both before its
	// connections are closed for good, so a reader never touches a closing socket or inflater.
	pumpCtx    context.Context
	pumpCancel context.CancelFunc
	pumpDone   []chan struct{}
	// readHook, when not nil, runs in a socket reader after it has a packet and before it hands the packet to the
	// session. Tests set it to hold one reader at a chosen packet and force a cross-socket interleaving; the
	// session never sets it. It cannot change the instance stream's order, which is what the map boundary uses.
	readHook func(side int, p worldconn.Packet)

	// The link's liveness: each socket's reader closes its own channel on an error (a closed or reset socket), so
	// every wait ends at once instead of waiting out its own deadline. deadSide/deadErr name the first socket to
	// go and its error. deadMu guards every field from deadChs down to lastPacketAt: the session goroutine is the
	// only writer of lastPacketName/lastPacketAt, but a socket reader reads them when it marks the link dead.
	deadMu     sync.Mutex
	deadChs    [2]chan struct{}
	deadClosed [2]bool
	deadErrs   [2]error
	deadAny    bool
	// logoutSent is set just before CMSG_LOGOUT_REQUEST is written, and the instance socket's close is the session
	// ending from then on: WorldSession::LogoutPlayer sends SMSG_LOGOUT_COMPLETE on the realm socket and closes the
	// instance one right after (WorldSession.cpp:766,789). logoutComplete is set when the completion was handled:
	// from then on any socket that closes is the server tearing the session down, not a lost link. Before the
	// completion the realm socket closing is still a loss, whatever the response said.
	logoutSent     bool
	logoutComplete bool
	deadSide       int
	deadErr        error
	deadStale      bool
	lastPacketName string
	lastPacketAt   time.Time

	activeMoverDone bool
	pingSerial      uint32
	pingSentAt      time.Time

	// unknownKeys is how many distinct unknown wire opcodes the report has a key for. A flooding server that walks
	// wire numbers must not grow the report per packet: past maxUnknownReportKeys everything collapses into one key.
	unknownKeys int

	// Round 6: the death flow's own state (phase, corpse, reclaim deadline, pending offer).
	death *deathWatch

	// Round 3: the world model, the character's own GUID once the create block names it, and the mover.
	world *objects.World
	// inv is the character's live inventory: the login snapshot kept current by push results, the player's own
	// InvSlots updates and the destroy lists (items.Tracker). It is built with the session, so both the act runner's
	// `inv` step and the serve method read it from the first packet on.
	inv *items.Tracker
	// itemCast watches the cast an item `use` step asked for (inventory_act.go): the answers the server gives to it.
	itemCast itemCastWatch
	self     wire.GUID
	selfSeen bool
	mover    *objects.Mover
	// routeSelectGUID and routeSelectMap are the selection the route-walk has open for its `.gps` reads: the core
	// reports the selected unit, so the character selects itself once per map and only again when either changes.
	routeSelectGUID wire.GUID
	routeSelectMap  uint32

	// Round 5: the bounded chat log and the party model.
	chat  *chat.Log
	party *party.State
	// Round A0: the add-on inbox: the registered prefixes and the bounded FIFO of delivered messages. The two
	// counters are the receive feed's log budget: lines printed, and messages whose line was withheld.
	addon           *addonmsg.Inbox
	addonLogged     int
	addonSuppressed int
	// addonState is what this session knows of the prefix list the server holds. It starts known (a fresh session
	// and the server both hold nothing); a registration send that fails after the unregister-all packet makes it
	// unknown until a later full unregister+register succeeds.
	addonState addonRegistrationState
	// Round A6: the command run's feed, set by cmdLink.Pump on the session goroutine while a command runs. The
	// same goroutine calls it from handleChat/addNotice, so no lock is needed.
	cmdFeed func(chat.Line)
	// Round A6: the GM guard. It starts unconfirmed in every session; confirmGM proves the permission with the
	// non-mutating probe. gmStop is set by the first self say of a command text (a late echo included): the
	// account's commands are being said aloud, so no further line may be sent.
	gmConfirmed bool
	gmStop      error
	// Round A1: the bot module's protocol. pbcAsm reassembles a body per sequence number, pbcSeq hands out request
	// numbers, and pbcOff is the reason the protocol was turned off for this session (the caller falls back).
	// fbQueue serialises the module's chat commands on this session, and fbQuiet keeps the protocol's frames out
	// of the receive log (they are data, not chat).
	pbcAsm   *pbc.Assembler
	pbcSeq   uint32
	pbcOff   string
	fbQueue  *command.Session
	fbQuiet  bool
	fbFrames int
	// pbcForged counts protocol frames dropped by the authentication rule (a channel other than whisper, or a
	// sender that is not this character): they never reach the assembler and never turn the protocol off.
	pbcForged int
	// fbProbed is set only after this session's `.bot about` probe found the module; every other `.bot` command
	// is refused until then, whichever path sends it.
	fbProbed bool
	// fbHeardWhispers keeps the most recent whisper lines so an order can drain the ones that arrived before it
	// was sent (a late acknowledgement must not be taken for the next order's).
	fbHeardWhispers []chat.Line
	// Round A2: the module's unsolicited EVENT_PUSH frames seen during the finder's requests, kept for the dungeon
	// watch. The buffer is bounded; fbEventsDropped counts what the bound pushed out.
	fbEvents        []pbc.Event
	fbEventsDropped int

	// Round A7: the map change's state and the navmesh planner (per map: a new world drops it).
	mapChg  mapChange
	planner *navmesh.Planner

	// Round 5 debug capture (Options.CaptureDir): one run directory per link, opened on the first packet.
	captureRun *captureRun
	captureErr error
	// Round 4: the fight log, the known spells of the character, the cast-id counter, and one log line per opcode a
	// parser refused.
	fight            *fightLog
	known            []int32
	castSerial       uint32
	parseErrorLogged map[string]bool
	// fightSender is what Attack and Cast write through and wait on: nil means the session itself (the sockets).
	// The tests set a fake so a fight runs on a fake clock.
	fightSender objects.Sender
	// serve is the RPC loop's state when this session is serving (forever serve); nil for a one-shot run.
	serveRT *serveRuntime
	// Round LOOT-1: the open window, the rolls it started and the game object used last (loot.go).
	loot *lootWatch
	// Act runner (forever-world-r0): the last gossip menu and quest list, set by the NPC dispatch for the runner's wait.
	actMenu  *npc.GossipMessage
	actQuest *npc.QuestGiverQuestList

	// Round R3: the vendor's answers since the last step cleared them (vendor.go, vendor_act.go).
	vendor vendorWatch

	// Round B1: the combat log. The ring lives with the session (decision 2 of the trials plan), is fed by the
	// combat dispatch and outlives a reconnect, so serve's `events`, the watch's record and the trials runner read
	// one log with one seq axis. A serving daemon hands the dropped session's ring to the one that replaces it.
	combat *CombatLog
}

const (
	// maxUnknownReportKeys is the number of distinct unknown wire opcodes the packet count keeps. Known opcodes are
	// bounded by the table itself, so only the unknown ones need a cap.
	maxUnknownReportKeys = 64
	// otherUnknownKey holds every unknown wire opcode past the cap.
	otherUnknownKey = "other unknown wire opcodes"
)

// defaultRunOverhead is the room a run with no stated Deadline gets around its stays: the login, the character
// list, the walk and the logout. It is a variable, not a constant, so a test can shrink it and still run the real
// path in milliseconds; a run never writes it.
var defaultRunOverhead = 2 * time.Minute

// defaultRunDeadline is the whole-run bound when the caller sets none: the overhead plus every stay the run was
// asked for. walk-check's --stay is Settle rather than Stay, so a settle must be counted too; counting only Stay let
// a 290 s settle be cut off at the two-minute overhead, and the socket read armed for that same absolute time ended
// the run with "reading a frame header: i/o timeout" while the server was still sending time syncs.
func defaultRunDeadline(opts Options) time.Duration {
	return defaultRunOverhead + opts.Stay + opts.Settle
}

// newSession builds one world session: its link state, its bounded models and the context its reads and sends use.
// A serving daemon builds one for the first login and a fresh one for every re-entry, so no socket, model or channel
// of a dropped link survives into the next one.
func newSession(opts Options, report *Report, ctx context.Context) *session {
	s := &session{opts: opts, ctx: ctx, events: make(chan event, 512), report: report, start: time.Now(), lastPacketAt: time.Now()}
	s.deadChs[sideRealm] = make(chan struct{})
	s.deadChs[sideInstance] = make(chan struct{})
	s.fight = &fightLog{started: time.Now(), now: s.Now}
	s.death = &deathWatch{started: time.Now()}
	s.loot = newLootWatch()
	s.inv = items.NewTracker()
	s.world = objects.NewWorld(0)
	s.chat = chat.NewLog(0)
	s.party = party.NewState()
	s.combat = &CombatLog{}
	// The readers run on their own context, detached from the caller's: an interrupt or an expired run window ends
	// the run's waits (they use s.ctx), but the stop and cleanup paths that detach from that context still need the
	// packet loop to read their answers. shutdown ends the readers, and it closes the sockets whatever the run left
	// behind.
	s.pumpCtx, s.pumpCancel = context.WithCancel(context.WithoutCancel(ctx))
	return s
}

// Enter runs the world half: authenticate, list characters, log the named character in, answer the login burst
// and keep-alives for Stay, then log out. It returns what was observed even when it fails part-way.
func Enter(ctx context.Context, opts Options) (*Report, error) {
	if opts.Character == "" {
		return nil, errors.New("world: no character name")
	}
	if opts.Serve == nil && opts.Deadline <= 0 {
		opts.Deadline = defaultRunDeadline(opts)
	}
	if opts.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Deadline)
		defer cancel()
	}
	// A serving session outlives the caller's cancellation long enough to log out: the RPC loop notices the
	// caller's context and sends the logout itself, so the session's own waits are not cut off underneath it.
	sessionCtx := ctx
	if opts.Serve != nil {
		sessionCtx = context.WithoutCancel(ctx)
	}

	report := &Report{Counts: make(map[string]int)}
	s := newSession(opts, report, sessionCtx)
	defer s.pumpCancel()

	if err := s.authenticate(ctx, opts.Deadline); err != nil {
		return report, err
	}

	runErr := s.enter(ctx)
	if runErr != nil && s.opts.Serve == nil {
		// the character may be online already; tell the server it is leaving before the sockets drop. A serving
		// session logs out itself, and a serving session whose link died cannot be told anything.
		s.abortLogin()
	}
	s.shutdown(ctx)
	// A serving session that re-entered after a drop ended with a later session: its packet counts are the run's.
	if s.serveRT != nil {
		if cur := s.serveRT.st.session.Load(); cur != nil && cur != s {
			for name, n := range cur.report.Counts {
				report.Counts[name] += n
			}
		}
	}
	return report, runErr
}

// authenticate opens and authenticates the world socket for this session and starts its reader. deadline bounds
// the handshake (worldconn's own deadline when zero). A login that already authenticated the world socket for the
// build it selected (Options.WorldConn) is adopted instead of authenticated a second time.
func (s *session) authenticate(ctx context.Context, deadline time.Duration) error {
	realm := s.opts.WorldConn
	if realm != nil {
		// The login's build probe authenticated this socket for exactly the build the world server accepted, so
		// entering must not authenticate a second time: that would be a second session for a build already proven.
		s.opts.logf("world socket: adopting the connection the login authenticated (client build %d)", s.opts.ClientBuild)
	} else {
		var err error
		realm, err = worldconn.Authenticate(ctx, worldconn.Config{
			Address:               s.opts.Address,
			RealmID:               s.opts.RealmID,
			RealmAddress:          s.opts.RealmAddress,
			RealmJoinTicket:       s.opts.RealmJoinTicket,
			ClientSecret:          s.opts.ClientSecret,
			JoinSecret:            s.opts.JoinSecret,
			ClientBuild:           s.opts.ClientBuild,
			RealmBuild:            s.opts.RealmBuild,
			BuildAuthKey:          s.opts.BuildAuthKey,
			AllowInstanceRedirect: s.opts.AllowInstanceRedirect,
			Deadline:              deadline,
			Logf:                  s.opts.Logf,
		})
		if err != nil {
			return err
		}
	}
	s.realm = realm
	for _, name := range realm.HandshakeNames() {
		s.report.Counts[name]++
	}
	s.startPump(sideRealm, realm.Next)
	return nil
}

// teardown ends a session's links completely: its readers are cancelled, both sockets are closed and their readers
// have returned (bounded by two seconds; closing the sockets unblocks a read anyway). Nothing of the session is used
// afterwards. A serving daemon calls it on the old session before logging in again, so the new login never shares a
// socket, a pump or a model with the dropped one.
func (s *session) teardown() {
	s.pumpCancel()
	if s.captureRun != nil {
		_ = s.captureRun.close()
	}
	if s.instance != nil {
		_ = s.instance.Close()
	}
	if s.realm != nil {
		_ = s.realm.Close()
	}
	deadline := time.After(2 * time.Second)
	for _, done := range s.pumpDone {
		select {
		case <-done:
		case <-deadline:
			return
		}
	}
}

// ownDeadlineTimeout reports a socket timeout that is this run's own deadline rather than the peer's: the read was
// armed with the context's deadline (worldconn's operationDeadline), and the socket's timer can beat the context's.
func (s *session) ownDeadlineTimeout(err error) bool {
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		return false
	}
	deadline, ok := s.pumpCtx.Deadline()
	return ok && !time.Now().Before(deadline)
}

// startPump runs one socket reader and records how to wait for it. A reader that returns an error (a closed or reset
// socket) marks the link dead before it posts the error, so every wait in the session ends at once.
func (s *session) startPump(side int, next func(context.Context) (worldconn.Packet, error)) {
	done := make(chan struct{})
	s.pumpDone = append(s.pumpDone, done)
	go func() {
		defer close(done)
		for {
			p, err := next(s.pumpCtx)
			if err == nil && s.readHook != nil {
				// A test may hold this reader here, between the read and the hand-off. The classification of
				// the packet (old map, no map, new map) does not depend on it: it is this connection's own
				// stream position (mapchange.go's gate), not a cross-reader clock.
				s.readHook(side, p)
			}
			ev := event{side: side, p: p, err: err}
			if err != nil {
				// A read that failed because this run's own context ended is not a lost link. The socket's read
				// deadline is armed from that context's deadline and can fire a moment before its timer, so the
				// deadline itself is checked as well.
				if s.pumpCtx.Err() == nil && !s.ownDeadlineTimeout(err) {
					s.setLinkDead(side, err)
				}
			}
			select {
			case s.events <- ev:
			case <-s.pumpCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
}

// ErrLinkLost is a socket that closed (or reset) during the run, or a peer that stopped sending altogether. Every
// wait in the session (Attack, Cast, MoveTo, WaitUntil, the rest and stay loops, the logout) ends with it.
var ErrLinkLost = errors.New("world: the connection is lost")

// linkSilenceLimit is how long the link may go without a server packet before it counts as dead even though the
// socket is still open: the server sends a time sync every ten seconds and a pong for every ping, so half a minute
// of silence is a hung or half-open peer, not a quiet fight.
const linkSilenceLimit = 30 * time.Second

// linkSideName is the socket's name in the run's messages.
func linkSideName(side int) string {
	switch side {
	case sideRealm:
		return "realm"
	case sideInstance:
		return "instance"
	}
	return fmt.Sprintf("connection %d", side)
}

// setLinkDead records one socket's reader error (or a silent peer) and closes that socket's channel; a socket that
// was already marked stays marked.
func (s *session) setLinkDead(side int, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return // this run's own deadline, not the peer
	}
	s.deadMu.Lock()
	defer s.deadMu.Unlock()
	if s.logoutComplete || (s.logoutSent && side == sideInstance) {
		return // the session the client asked to end is ending
	}
	if side >= 0 && side < len(s.deadChs) && s.deadChs[side] != nil && !s.deadClosed[side] {
		s.deadClosed[side] = true
		s.deadErrs[side] = err
		close(s.deadChs[side])
	}
	if s.deadAny {
		return
	}
	s.deadAny = true
	s.deadSide = side
	s.deadErr = err
	now := time.Now()
	s.report.LinkLost = true
	s.report.LinkLostSide = linkSideName(side)
	s.report.LinkLostDetail = s.deadDetail(side)
	if !s.lastPacketAt.IsZero() {
		s.report.LastPacket = s.lastPacketName
		s.report.LastPacketAgo = now.Sub(s.lastPacketAt).Round(time.Millisecond)
	}
}

// notePacket records the last server packet the session handled and when. It takes deadMu because a socket reader
// reads these two fields in setLinkDead; the session goroutine is the only writer.
func (s *session) notePacket(name string) {
	s.deadMu.Lock()
	s.lastPacketName = name
	s.lastPacketAt = time.Now()
	s.deadMu.Unlock()
}

// lastPacket is the last server packet the session handled and when, or a zero time when it has handled none. It
// takes deadMu for the same reason as notePacket.
func (s *session) lastPacket() (string, time.Time) {
	s.deadMu.Lock()
	defer s.deadMu.Unlock()
	return s.lastPacketName, s.lastPacketAt
}

// deadDetail is what the run says about one socket's death.
func (s *session) deadDetail(side int) string {
	if s.deadStale {
		return fmt.Sprintf("no server packet for %s", linkSilenceLimit)
	}
	if side >= 0 && side < len(s.deadErrs) && s.deadErrs[side] != nil {
		return s.deadErrs[side].Error()
	}
	return "closed"
}

// linkDead is the link's state as an error: nil while either socket is alive.
func (s *session) linkDead() error { return s.deadErrorExcept(-1) }

// deadErrorExcept is linkDead with one socket's death ignored (the logout tolerates its instance socket closing
// before the completion arrives on the realm socket).
func (s *session) deadErrorExcept(ignore int) error {
	s.deadMu.Lock()
	side, found := -1, false
	for i := range s.deadClosed {
		if s.deadClosed[i] && i != ignore {
			side, found = i, true
			break
		}
	}
	stale := s.deadStale
	name := s.lastPacketName
	s.deadMu.Unlock()
	if !found {
		return nil
	}
	if stale {
		return fmt.Errorf("%w (the %s socket: no server packet for %s; the last was %s)",
			ErrLinkLost, linkSideName(side), linkSilenceLimit, name)
	}
	s.deadMu.Lock()
	detail := s.deadDetail(side)
	s.deadMu.Unlock()
	return fmt.Errorf("%w (the %s socket: %s)", ErrLinkLost, linkSideName(side), detail)
}

// silentSide is the socket a silence is noticed on: the one this wait is not ignoring. A wait that ignores no
// socket notices silence on the gameplay socket, as it always has; the logout ignores the instance socket, which
// the server closes with the completion, so its silence is the realm's.
func silentSide(ignore int) int {
	if ignore == sideInstance {
		return sideRealm
	}
	return sideInstance
}

// silenceLost is the lost-link error for a peer that stopped sending. The silence was just recorded on a socket
// this wait is not ignoring, so deadErrorExcept normally has it; when that socket's death is itself suppressed (a
// close that raced a completion the logout already has), the error is built here instead of returning a zero event
// that the caller would handle as traffic.
func (s *session) silenceLost(ignore int) error {
	if err := s.deadErrorExcept(ignore); err != nil {
		return err
	}
	name, _ := s.lastPacket()
	return fmt.Errorf("%w (the %s socket: no server packet for %s; the last was %s)",
		ErrLinkLost, linkSideName(silentSide(ignore)), linkSilenceLimit, name)
}

// markStale ends the link because the peer stopped sending; side is the socket the caller was waiting on that still
// matters. The gameplay socket is the one a fight notices.
func (s *session) markStale(side int) {
	s.deadMu.Lock()
	s.deadStale = true
	s.deadMu.Unlock()
	s.setLinkDead(side, nil)
}

// shutdown stops the readers, closes both sockets in a fixed order whatever happened before, and waits for the
// readers to return before Enter does. The wait is bounded by the run's deadline (or two seconds when that has
// already passed, because Close has then unblocked every read anyway).
func (s *session) shutdown(ctx context.Context) {
	s.pumpCancel()
	if s.captureRun != nil {
		_ = s.captureRun.close()
	}
	if s.instance != nil {
		_ = s.instance.Close()
	}
	if s.realm != nil {
		_ = s.realm.Close()
	}
	waitCtx := ctx
	if waitCtx.Err() != nil {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	for _, done := range s.pumpDone {
		select {
		case <-done:
		case <-waitCtx.Done():
			return
		}
	}
}

// abortLogin is the logout a failed run owes the server: a character whose socket just disappears stays online
// until the server's own timeout. Best effort, bounded by two seconds, and only after the character was logged in
// (the instance socket exists from CMSG_PLAYER_LOGIN on). A dead link cannot be told the character is leaving, so
// the run says exactly that instead of writing into a closed socket.
func (s *session) abortLogin() {
	if err := s.linkDead(); err != nil {
		s.opts.logf("world abort: logout not possible: connection lost (%s)", s.report.LinkLostDetail)
		return
	}
	if s.instance == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.sendOnCtx(ctx, sideInstance, wire.CMSGLogoutRequest, BuildLogoutRequest()); err != nil {
		s.opts.logf("world abort: logout not possible: connection lost (%v)", err)
	} else {
		s.opts.logf("world abort: CMSG_LOGOUT_REQUEST sent after the failure")
	}
	if err := s.sendOnCtx(ctx, sideInstance, wire.CMSGLogDisconnect, BuildLogDisconnect()); err != nil {
		s.opts.logf("world abort: CMSG_LOG_DISCONNECT: %v", err)
	} else {
		s.opts.logf("world abort: CMSG_LOG_DISCONNECT sent")
	}
}

// loginWorld is the login half of a run: authenticate the world socket, list the account's characters, log the named
// one in on a fresh instance socket, and answer the burst up to the loading-screen notification. It leaves the
// session in the world and says nothing about what the run does next: enter does, and a serving daemon's re-entry
// uses this half alone.
func (s *session) loginWorld(ctx context.Context) error {
	// ---- character list
	if err := s.sendOn(sideRealm, wire.CMSGEnumCharacters, nil); err != nil {
		return err
	}
	s.opts.logf("world character list: CMSG_ENUM_CHARACTERS sent")
	p, err := s.waitFor("the character list", 30*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGEnumCharactersResult
	})
	if err != nil {
		return err
	}
	list, err := ParseEnumCharactersResult(p.Payload)
	if err != nil {
		return err
	}
	s.report.Characters = list.All()
	s.opts.logf("world character list: %d character(s), max level %d, %d trailing bytes",
		len(s.report.Characters), list.MaxLevel, list.TrailingBytes)
	for _, c := range s.report.Characters {
		s.opts.logf("  character %s: level %d, class %d, race %d, map %d, at %.1f %.1f %.1f", chat.SanitiseName(c.DisplayName()),
			c.Level, c.Class, c.Race, c.MapID, c.X, c.Y, c.Z)
	}
	character, ok := findCharacter(s.report.Characters, s.opts.Character)
	if !ok {
		return fmt.Errorf("world: the account has no character named %q", chat.SanitiseName(s.opts.Character))
	}
	s.report.Character = character

	// ---- CMSG_PLAYER_LOGIN and the instance connection
	if err := s.sendOn(sideRealm, wire.CMSGPlayerLogin, BuildPlayerLogin(character.GUID)); err != nil {
		return err
	}
	s.opts.logf("world login: CMSG_PLAYER_LOGIN sent for %s (%s)", chat.SanitiseName(character.DisplayName()), character.GUID)
	p, err = s.waitFor("SMSG_CONNECT_TO", 30*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGConnectTo
	})
	if err != nil {
		return err
	}
	ct, err := worldconn.ParseConnectTo(p.Payload)
	if err != nil {
		return err
	}
	s.opts.logf("world login: SMSG_CONNECT_TO names %s (serial %d)", ct.Address(), ct.Serial)
	inst, err := s.realm.DialInstance(ctx, ct)
	if err != nil {
		return err
	}
	s.instance = inst
	for _, name := range inst.HandshakeNames() {
		s.report.Counts[name]++
	}
	s.startPump(sideInstance, inst.Next)

	// ---- the instance connection starts the login: SMSG_RESUME_COMMS, then the burst
	if _, err := s.waitFor("SMSG_RESUME_COMMS", 30*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGResumeComms
	}); err != nil {
		return err
	}
	now := clientTimeMs()
	if err := s.sendOn(sideInstance, wire.CMSGQueuedMessagesEnd, BuildQueuedMessagesEnd(now)); err != nil {
		return err
	}
	s.opts.logf("world login: SMSG_RESUME_COMMS answered with CMSG_QUEUED_MESSAGES_END (%d ms)", now)

	p, err = s.waitFor("SMSG_LOGIN_VERIFY_WORLD", 60*time.Second, func(p worldconn.Packet) bool {
		return p.Core == wire.SMSGLoginVerifyWorld
	})
	if err != nil {
		return err
	}
	verify, err := ParseLoginVerifyWorld(p.Payload)
	if err != nil {
		return err
	}
	s.report.MapID = verify.MapID
	s.report.Position = [4]float32{verify.X, verify.Y, verify.Z, verify.O}
	s.opts.logf("world: in world on map %d at %.2f %.2f %.2f (orientation %.2f), reason %d",
		verify.MapID, verify.X, verify.Y, verify.Z, verify.O, verify.Reason)
	// the real client sends the loading screen notification on the realm socket, with the map it just entered
	if err := s.sendOn(sideRealm, wire.CMSGLoadingScreenNotify, BuildLoadingScreenNotify(verify.MapID)); err != nil {
		return err
	}
	s.opts.logf("world: CMSG_LOADING_SCREEN_NOTIFY sent (map %d, showing false)", verify.MapID)
	return nil
}

// enter logs in and then runs what the session was asked to do: the serving loop, one of the one-shot checks with its
// logout, or the plain stay.
func (s *session) enter(ctx context.Context) error {
	if err := s.loginWorld(ctx); err != nil {
		return err
	}
	if s.opts.Serve != nil {
		return s.serve(ctx, s.opts.Serve)
	}

	// ---- stay: answer time syncs, the active-mover exchange and pings
	if s.opts.AddonCheck {
		err := s.addonCheck(ctx)
		s.summariseChat()
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.FBCheck {
		err := s.fbCheck(ctx)
		s.summariseChat()
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.FBDungeon {
		err := s.fbDungeon(ctx)
		s.summariseChat()
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.ChatCheck {
		err := s.chatCheck(ctx)
		s.summariseChat()
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.PartyCheck {
		err := s.partyCheck(ctx)
		s.summariseParty()
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.CommandCheck {
		err := s.commandCheck(ctx)
		s.summariseChat()
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.RouteWalk != nil {
		if err := s.routeWalk(ctx); err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.LootCheck {
		err := s.lootCheck(ctx)
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.ObjectUse {
		err := s.objectUse(ctx)
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.WalkCheck {
		if err := s.walkCheck(ctx); err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.Act != nil {
		if err := s.act(ctx, s.opts.Act); err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.FightCheck {
		if err := s.fightCheck(ctx); err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.DeathCheck {
		err := s.deathCheck(ctx)
		s.report.Death = s.deathReport()
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.MapCheck {
		if err := s.mapCheck(ctx); err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.DungeonRun != nil {
		res, err := s.dungeonRun(ctx, s.opts.DungeonRun)
		s.report.Dungeon = res
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if s.opts.DungeonWatch != nil {
		res, err := s.dungeonWatch(ctx, s.opts.DungeonWatch)
		s.report.Watch = res
		if err != nil {
			return err
		}
		return s.logout(ctx)
	}
	if err := s.stay(ctx); err != nil {
		return err
	}
	return s.logout(ctx)
}

// Send writes one client packet on the instance socket (the socket the real client sends gameplay on). A dead link
// refuses the send at once instead of writing into a half-open socket.
func (s *session) Send(core uint32, payload []byte) error { return s.SendCtx(s.ctx, core, payload) }

// SendCtx is Send on the caller's context. The stop and cleanup paths detach from the session's own context (the
// caller's interrupt or the run's deadline ended it) and hand their own bounded one here: the line must still reach
// the server, which the session's context would refuse at once.
func (s *session) SendCtx(ctx context.Context, core uint32, payload []byte) error {
	return s.sendOnCtx(ctx, sideInstance, core, payload)
}

// Now is the client clock the movement time field carries.
func (s *session) Now() time.Time { return time.Now() }

// drain is the one timed wait the session has: it answers server packets until end, applying each. A wait that
// runs out of time is not an error; a dead or silent link ends it with ErrLinkLost through next. check, when it is
// not nil, runs before every wait and after every handled packet, and its error ends the drain (the fight's death
// check).
func (s *session) drain(ctx context.Context, what string, end time.Time, check func() error) error {
	for {
		// The wait's own context bounds it: an operation that outlived the window it was given ends there, even
		// when the session's context (next's own wakeup) has not ended.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("world: waiting for %s: %w", what, err)
		}
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		remaining := time.Until(end)
		if remaining <= 0 {
			return nil
		}
		ev, err := s.nextCtx(ctx, what, remaining)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return nil
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
}

// WaitUntil answers server packets until until, so a walk keeps the session healthy (time syncs, pings and object
// updates all arrive while it walks). A map change that opens or resolves inside the wait ends it with ErrMapChange:
// the leg's line belongs to the old map, whatever the transition did in the meantime.
func (s *session) WaitUntil(ctx context.Context, until time.Time) error {
	epoch := s.mapEpoch()
	if err := s.drain(ctx, "the walk", until, nil); err != nil {
		return err
	}
	if s.mapEpoch() != epoch {
		return ErrMapChange
	}
	// A window that outlived mapChangeLimit ends the run here too: the walk is the one caller that would otherwise
	// keep waiting on a map the client may no longer be on.
	if err := s.mapChangeTimedOut(); err != nil {
		return err
	}
	return nil
}

// settle answers the server for d and applies what it sends, and ends at once when the character dies: a corpse
// gets no ping, no name query and no fight. A dead link ends it the same way (through next).
func (s *session) settle(ctx context.Context, d time.Duration) error {
	return s.drain(ctx, "the settle", time.Now().Add(d), s.checkSelfAlive)
}

// checkSelfAlive is the settle's death check: the fight command's dead exit code, before anything else is sent.
func (s *session) checkSelfAlive() error {
	if !s.selfDead() {
		return nil
	}
	s.opts.logf("fight-check: the character died while the client settled in the world")
	return ErrSelfDead
}

// collect answers the server for d and applies what it sends (the chat and party steps, the walk's pause).
func (s *session) collect(ctx context.Context, d time.Duration) error {
	return s.drain(ctx, "the settle", time.Now().Add(d), nil)
}

const (
	// nameQueryDeadline bounds one wait for a name answer; the server answers both lookups immediately.
	nameQueryDeadline = 3 * time.Second
	// maxNameQueryRounds bounds how many batches of lookups one run sends.
	maxNameQueryRounds = 4
	// maxPlayerNamesPerQuery is the server's Array<ObjectGuid, 50> (QueryPackets.h:112).
	maxPlayerNamesPerQuery = 50
	// maxCreatureQueries is what the official client batches per query (QueryPackets.cpp:66-67).
	maxCreatureQueries = 11
)

// queryNames asks for the names of the nearest units this client does not know yet, bounded in rounds and with a
// deadline per answer.
func (s *session) queryNames(ctx context.Context) {
	x, y, z := s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2]
	for round := 0; round < maxNameQueryRounds; round++ {
		players, entries := s.world.UnknownUnits(x, y, z, maxPlayerNamesPerQuery)
		if len(entries) > maxCreatureQueries {
			entries = entries[:maxCreatureQueries]
		}
		if len(players) == 0 && len(entries) == 0 {
			return
		}
		if len(players) > 0 {
			if err := s.sendOn(sideInstance, wire.CMSGQueryPlayerNames, objects.BuildQueryPlayerNames(players)); err != nil {
				return
			}
			s.report.NamesAsked += len(players)
			s.opts.logf("world: CMSG_QUERY_PLAYER_NAMES for %d player(s)", len(players))
			if _, err := s.waitFor("SMSG_QUERY_PLAYER_NAMES_RESPONSE", nameQueryDeadline, func(p worldconn.Packet) bool {
				return p.Core == wire.SMSGQueryPlayerNamesResponse
			}); err != nil {
				s.opts.logf("world: the player name answer did not come: %v", err)
				return
			}
		}
		if len(entries) > 0 {
			// The 6-bit count cannot name more than 63 entries; BuildQueryCreature splits a longer list.
			s.opts.logf("world: CMSG_QUERY_CREATURE for %d entr(y/ies)", len(entries))
			for _, packet := range objects.BuildQueryCreature(uint32Entries(entries)) {
				if err := s.sendOn(sideInstance, wire.CMSGQueryCreature, packet); err != nil {
					return
				}
				if _, err := s.waitFor("SMSG_QUERY_CREATURE_RESPONSE", nameQueryDeadline, func(p worldconn.Packet) bool {
					return p.Core == wire.SMSGCreatureQueryResponse
				}); err != nil {
					s.opts.logf("world: the creature name answer did not come: %v", err)
					return
				}
			}
		}
	}
}

// uint32Entries narrows the entries to the query's uint32 wire form.
func uint32Entries(entries []int32) []uint32 {
	out := make([]uint32, len(entries))
	for i, e := range entries {
		out[i] = uint32(e)
	}
	return out
}

// walkCheck is round 3's command: settle, summarise the world, walk dx/dy and back, report, and let Enter log out.
func (s *session) walkCheck(ctx context.Context) error {
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 8 * time.Second
	}
	pause := s.opts.WalkPause
	if pause <= 0 {
		pause = 3 * time.Second
	}
	s.sendPing()
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	x, y, z, o := s.report.WalkClientPos[0], s.report.WalkClientPos[1], s.report.WalkClientPos[2], s.report.WalkClientPos[3]
	s.queryNames(ctx)
	s.summarise(x, y, z)

	if s.opts.NavmeshDir != "" {
		return s.walkCheckPath(ctx, x, y, z)
	}

	// The walk: from the position the server put the character at (the create block's, not the login's).
	from := [4]float32{x, y, z, o}
	s.report.WalkFrom = from
	target := [4]float32{x + s.opts.Dx, y + s.opts.Dy, z, o}
	s.report.WalkTo = target
	if s.mover == nil {
		s.report.Walked = false
		s.opts.logf("walk-check: no create block of this character arrived, so the walk is skipped")
		return nil
	}
	if err := s.mover.MoveTo(ctx, target[0], target[1], target[2]); err != nil {
		return err
	}
	if err := s.collect(ctx, pause); err != nil {
		return err
	}
	if err := s.mover.MoveTo(ctx, from[0], from[1], from[2]); err != nil {
		return err
	}
	// The way back ends facing the way it came (the last leg's direction), and the server saves that facing with
	// the character. Turn back to the orientation the create block gave, so the walk leaves the character as it
	// found it: one CMSG_MOVE_SET_FACING at the spawn.
	if err := s.mover.Face(o); err != nil {
		return fmt.Errorf("walk-check: CMSG_MOVE_SET_FACING: %w", err)
	}
	bx, by, bz, bo := s.mover.Position()
	s.report.BackAt = [4]float32{bx, by, bz, bo}
	s.report.WalkClientPos = s.report.BackAt
	s.report.Walked = true
	// The way back faces the opposite of the way out (the walk's own two legs are the same line).
	turn := math.Mod(float64(o)+math.Pi, 2*math.Pi)
	s.opts.logf("walk-check: back at the spawn, the client believes it is at %.2f %.2f %.2f; orientation restored to %.2f (the way back faced %.2f)",
		bx, by, bz, bo, float32(turn))
	return nil
}

// walkCheckPath is walk-check with --navmesh: plan an absolute target on the current map and walk the
// route's points as one continuous movement. Without the option the command never reaches this.
func (s *session) walkCheckPath(ctx context.Context, x, y, z float32) error {
	if s.mover == nil {
		s.report.Walked = false
		s.opts.logf("walk-check: no create block of this character arrived, so the walk is skipped")
		return nil
	}
	mapID := uint32(s.world.MapID())
	planner, err := s.navPlanner()
	if err != nil {
		return fmt.Errorf("walk-check: navmesh: %w", err)
	}
	defer s.dropPlanner()
	route, err := planner.PathTo(mapID, navmesh.Vec3{x, y, z}, navmesh.Vec3{s.opts.ToX, s.opts.ToY, s.opts.ToZ})
	if err != nil {
		return fmt.Errorf("walk-check: navmesh: %w", err)
	}
	s.report.NavPath = true
	s.report.NavPathKind = string(route.Kind)
	s.report.NavPathCorners = route.Corners
	s.report.NavPathLength = route.Length
	s.report.NavPathFlags = strings.Join(route.FlagNames, "|")
	s.report.WalkFrom = [4]float32{x, y, z, s.mover.O}
	s.report.WalkTo = [4]float32{s.opts.ToX, s.opts.ToY, s.opts.ToZ, s.mover.O}
	s.opts.logf("walk-check: navmesh route on map %d: %s, %d corner(s), %.1f yd, flags %s%s",
		mapID, route.Kind, route.Corners, route.Length, strings.Join(route.FlagNames, "|"), reasonSuffix(route.Reason))
	if route.Kind == navmesh.RouteNone {
		return fmt.Errorf("walk-check: no navmesh route to %.2f %.2f %.2f: %s", s.opts.ToX, s.opts.ToY, s.opts.ToZ, route.Reason)
	}
	points := make([][3]float32, len(route.Points))
	for i, p := range route.Points {
		points[i] = [3]float32{p[0], p[1], p[2]}
	}
	if err := s.mover.WalkPath(ctx, points); err != nil {
		return err
	}
	bx, by, bz, bo := s.mover.Position()
	s.report.BackAt = [4]float32{bx, by, bz, bo}
	s.report.WalkClientPos = s.report.BackAt
	s.report.Walked = true
	s.opts.logf("walk-check: navmesh walk ended at %.2f %.2f %.2f (orientation %.2f)", bx, by, bz, bo)
	return nil
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// objectSummary is the world model's counts in the report's shape.
func (s *session) objectSummary() ObjectSummary {
	c := s.world.Counts()
	summary := ObjectSummary{
		Map:           s.world.MapID(),
		Objects:       c.Objects,
		Created:       c.Created,
		Updated:       c.Updated,
		Destroyed:     c.Destroyed,
		OutOfRange:    c.OutOfRange,
		Updates:       c.Applied,
		Partial:       c.Partial,
		PartialKinds:  make(map[string]int, len(c.PartialKinds)),
		Unparsed:      make(map[string]int, len(c.Unparsed)),
		ByType:        make(map[string]int),
		Resurrections: c.Resurrections,
	}
	for kind, n := range c.PartialKinds {
		summary.PartialKinds[kind] = n
	}
	for reason, n := range c.Unparsed {
		summary.Unparsed[reason] = n
	}
	for t, n := range c.ByType {
		summary.ByType[objects.TypeName(t)] = n
	}
	return summary
}

// summarise fills the report's object summary and nearest-units table, and prints both.
func (s *session) summarise(x, y, z float32) {
	c := s.world.Counts()
	summary := s.objectSummary()
	s.report.Objects = summary
	var typeNames []string
	for name := range summary.ByType {
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)
	s.opts.logf("world: %d object(s) in the model after %d update(s):", summary.Objects, summary.Updates)
	for _, name := range typeNames {
		s.opts.logf("  %-16s %d", name, summary.ByType[name])
	}
	s.opts.logf("world: %d created, %d updated, %d destroyed, %d out of range", summary.Created, summary.Updated, summary.Destroyed, summary.OutOfRange)
	if c.Partial > 0 {
		s.opts.logf("world: %d object(s) partial: a block naming them was not decoded to its declared end", c.Partial)
		kinds := make([]string, 0, len(c.PartialKinds))
		for kind := range c.PartialKinds {
			kinds = append(kinds, kind)
		}
		sort.Slice(kinds, func(i, j int) bool {
			if c.PartialKinds[kinds[i]] != c.PartialKinds[kinds[j]] {
				return c.PartialKinds[kinds[i]] > c.PartialKinds[kinds[j]]
			}
			return kinds[i] < kinds[j]
		})
		for _, kind := range kinds {
			s.opts.logf("  partial: %-58s %d", kind, c.PartialKinds[kind])
		}
	}
	if len(c.Unparsed) > 0 {
		total := 0
		keys := make([]string, 0, len(c.Unparsed))
		for key := range c.Unparsed {
			total += c.Unparsed[key]
			keys = append(keys, key)
		}
		sort.Strings(keys)
		s.opts.logf("world: %d unparsed block(s), skipped by size:", total)
		for _, key := range keys {
			if detail := c.UnparsedDetail[key]; detail != "" {
				s.opts.logf("  %-52s %d  (%s)", key, c.Unparsed[key], detail)
				continue
			}
			s.opts.logf("  %-52s %d", key, c.Unparsed[key])
		}
	}
	if c.UnknownMoves > 0 {
		s.opts.logf("world: %d movement packet(s) for a GUID the model does not hold (not created)", c.UnknownMoves)
	}

	units := s.world.Units(x, y, z)
	limit := 5
	if len(units) < limit {
		limit = len(units)
	}
	s.report.Units = make([]UnitRow, 0, limit)
	for _, u := range units[:limit] {
		row := UnitRow{
			Type:      objects.TypeName(u.Type),
			Entry:     u.Info.Entry,
			Name:      s.world.Name(u),
			Level:     u.Info.Level,
			Health:    u.Info.Health,
			MaxHealth: u.Info.MaxHealth,
			Distance:  objects.Distance(u.X, u.Y, u.Z, x, y, z),
		}
		s.report.Units = append(s.report.Units, row)
		s.opts.logf("  nearest: %-11s entry %-6d level %-3d health %d/%d  %5.1f yd  %s",
			row.Type, row.Entry, row.Level, row.Health, row.MaxHealth, row.Distance, row.Name)
	}
	s.report.NamesFound = 0
	for _, u := range units {
		if s.world.Name(u) != "" {
			s.report.NamesFound++
		}
	}
}

// stay answers the server until the stay time is over.
func (s *session) stay(ctx context.Context) error {
	end := time.Now().Add(s.opts.Stay)
	s.sendPing()
	s.opts.logf("world: staying %s, answering time syncs and pings", s.opts.Stay)
	pingEvery := 30 * time.Second
	nextPing := time.Now().Add(pingEvery)
	for {
		remaining := time.Until(end)
		if remaining <= 0 {
			break
		}
		wait := remaining
		if untilPing := time.Until(nextPing); untilPing < wait {
			wait = untilPing
		}
		ev, err := s.next("the stay", wait)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				if time.Now().After(nextPing) {
					s.sendPing()
					nextPing = time.Now().Add(pingEvery)
				}
				continue
			}
			return err
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
	}
	s.report.Stayed = time.Since(end.Add(-s.opts.Stay))
	s.opts.logf("world: stay over (%s, %d time syncs, %d pings, %d pongs, in world: %v)",
		s.report.Stayed.Round(time.Millisecond), s.report.TimeSyncs, s.report.Pings, s.report.Pongs, s.report.InWorld)
	return nil
}

// logoutWait bounds the wait for SMSG_LOGOUT_COMPLETE. The server completes a logout that is not instant after 20 s
// (WorldSession::ShouldLogOut), so 30 s leaves a margin without holding a run open on a server that died.
const logoutWait = 30 * time.Second

// logoutTeardownWait bounds the logout when the caller's context has already ended (the operator's interrupt, or the
// run's own deadline). The character must still be told to leave — a socket that just disappears keeps it online
// until the server's own timeout — but an interrupt must not hold the command for the whole logoutWait.
const logoutTeardownWait = 10 * time.Second

// logout sends CMSG_LOGOUT_REQUEST and waits for the completion on the realm socket.
//
// The instance socket's close is part of the logout: WorldSession::LogoutPlayer sends SMSG_LOGOUT_COMPLETE on the
// realm socket and then closes the instance socket (WorldSession.cpp:766,789), and the response itself is queued on
// the instance socket first (MiscHandler.cpp:264-267), so it may never arrive: a socket closed with a write still
// queued drops it. Either way the session is ending, not lost, and the completion is the fact the run waits for. A
// realm socket that closes before the completion, or a silent server, is still a real loss.
func (s *session) logout(ctx context.Context) error {
	// A caller context that already ended (an interrupt, the run's own deadline) is not the character's end: the
	// logout is teardown, so it runs on a short bounded context of its own rather than failing on the first wait.
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), logoutTeardownWait)
		defer cancel()
	}
	// The state is set before the request is written: an instant logout can close the instance socket as soon as
	// the request is read, before the send returns, and that close is the session ending, not a lost link.
	s.deadMu.Lock()
	s.logoutSent = true
	s.deadMu.Unlock()
	if err := s.sendOnCtx(ctx, sideInstance, wire.CMSGLogoutRequest, BuildLogoutRequest()); err != nil {
		return err
	}
	s.opts.logf("world logout: CMSG_LOGOUT_REQUEST sent")
	deadline := time.Now().Add(logoutWait)
	responded := false
	var completeAt time.Time
	for {
		if s.report.LogoutComplete {
			// The completion is in; give the response (the other socket) 500 ms to arrive, since it was queued
			// first. It may never come, which is not an error.
			grace := 500*time.Millisecond - time.Since(completeAt)
			if responded || grace <= 0 {
				break
			}
			ev, err := s.nextExceptCtx(ctx, "the response to the logout", grace, sideInstance)
			if err != nil || ev.err != nil {
				break
			}
			if err := s.handle(ctx, ev); err != nil {
				return err
			}
			if ev.p.Core == wire.SMSGLogoutResponse {
				responded = true
				if s.report.Logout.Result != 0 {
					return fmt.Errorf("world: the server refused the logout: result %d", s.report.Logout.Result)
				}
			}
			continue
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return fmt.Errorf("world: no SMSG_LOGOUT_COMPLETE within %s", logoutWait)
		}
		ev, err := s.nextExceptCtx(ctx, "the logout", wait, sideInstance)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				return fmt.Errorf("world: no SMSG_LOGOUT_COMPLETE within %s", logoutWait)
			}
			return err // the realm socket died (or went silent) before the completion
		}
		if ev.err != nil {
			continue // the instance socket closing is the logout proceeding
		}
		if err := s.handle(ctx, ev); err != nil {
			return err
		}
		if ev.p.Core == wire.SMSGLogoutResponse {
			responded = true
			if s.report.Logout.Result != 0 {
				return fmt.Errorf("world: the server refused the logout: result %d", s.report.Logout.Result)
			}
		}
		if s.report.LogoutComplete {
			completeAt = time.Now()
		}
	}
	if responded {
		s.opts.logf("world logout: SMSG_LOGOUT_RESPONSE (result %d, instant %v) and SMSG_LOGOUT_COMPLETE",
			s.report.Logout.Result, s.report.Logout.Instant)
		s.clearTeardownLoss()
		return nil
	}
	s.opts.logf("world logout: SMSG_LOGOUT_COMPLETE (no SMSG_LOGOUT_RESPONSE: the server closes the instance socket with the completion)")
	s.clearTeardownLoss()
	return nil
}

// clearTeardownLoss forgets a socket death that raced the logout's completion. The reader can see the server close a
// socket before this goroutine handles the completion already queued, and a logout that completed is not a lost link;
// a socket death that mattered ended the loop above with ErrLinkLost and never reaches here.
func (s *session) clearTeardownLoss() {
	s.deadMu.Lock()
	defer s.deadMu.Unlock()
	s.report.LinkLost = false
	s.report.LinkLostSide = ""
	s.report.LinkLostDetail = ""
	s.deadAny = false
	s.deadSide, s.deadErr = -1, nil
}

func (s *session) sendPing() {
	target := s.instance
	if target == nil || s.linkDead() != nil {
		return
	}
	s.pingSerial++
	b := BuildPing(s.pingSerial, uint32(s.report.Latency.Milliseconds()))
	s.pingSentAt = time.Now()
	if err := target.Send(s.ctx, wire.CMSGPing, b); err != nil {
		return
	}
	s.report.Pings++
}

// countPacket adds one packet to the report by name. An unknown wire opcode gets its own "wire 0xNNNNNN" key until
// maxUnknownReportKeys of them exist; beyond that they all count under otherUnknownKey, so a server that walks wire
// numbers cannot grow the map without bound.
func (s *session) countPacket(p worldconn.Packet) {
	if p.Name != "" {
		s.report.Counts[p.Name]++
		return
	}
	name := fmt.Sprintf("wire 0x%06X", p.Wire)
	if _, seen := s.report.Counts[name]; !seen {
		if s.unknownKeys >= maxUnknownReportKeys {
			name = otherUnknownKey
		} else {
			s.unknownKeys++
		}
	}
	s.report.Counts[name]++
}

// handle counts and answers one server packet. Unknown packets are counted and skipped, never an error.
func (s *session) handle(ctx context.Context, ev event) error {
	if ev.err != nil {
		// The reader already marked the link dead; the event is only here so a wait that owns this reader sees it.
		if err := s.linkDead(); err != nil {
			return err
		}
		// A reader that ended with this run's own context ended says so: the run's deadline is the reason, not the
		// socket's timeout (which may have fired a moment earlier).
		if err := s.ctx.Err(); err != nil {
			return fmt.Errorf("world: connection %d: %w", ev.side, err)
		}
		return fmt.Errorf("world: connection %d: %w", ev.side, ev.err)
	}
	p := ev.p
	if ev.side == sideInstance && s.holdMapPacket(ev) {
		return nil
	}
	if p.Name != "" {
		s.notePacket(p.Name)
	} else {
		s.notePacket(fmt.Sprintf("wire 0x%06X", p.Wire))
	}
	s.countPacket(p)
	switch p.Core {
	case wire.SMSGTimeSyncRequest:
		seq, err := ParseTimeSyncRequest(p.Payload)
		if err != nil {
			return err
		}
		now := clientTimeMs()
		if err := s.sendOnCtx(ctx, ev.side, wire.CMSGTimeSyncResponse, BuildTimeSyncResponse(seq, now)); err != nil {
			return err
		}
		s.report.TimeSyncs++
	case wire.SMSGPong:
		serial, err := ParsePong(p.Payload)
		if err != nil {
			return err
		}
		if serial == s.pingSerial {
			s.report.Latency = time.Since(s.pingSentAt)
		}
		s.report.Pongs++
	case wire.SMSGMoveSetActiveMover:
		guid, err := ParseMoveSetActiveMover(p.Payload)
		if err != nil {
			return err
		}
		s.report.ActiveMoverGUID = guid
		// The server names this character's own GUID here, before any party packet: the party model can now tell
		// whether a roster actually lists the client.
		s.party.SetSelf(guid)
		if !s.activeMoverDone {
			if err := s.sendOnCtx(ctx, ev.side, wire.CMSGMoveInitActiveMoverDone, BuildMoveInitActiveMoverComplete(clientTimeMs())); err != nil {
				return err
			}
			s.activeMoverDone = true
			s.report.InWorld = true
			s.opts.logf("world: SMSG_MOVE_SET_ACTIVE_MOVER for %s, CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE sent", guid)
		}
	case wire.SMSGCharacterLoginFailed:
		code, err := ParseCharacterLoginFailed(p.Payload)
		if err != nil {
			return err
		}
		return fmt.Errorf("world: the server refused the character login (reason %d)", code)
	case wire.SMSGLogoutResponse:
		r, err := ParseLogoutResponse(p.Payload)
		if err != nil {
			return err
		}
		s.report.Logout = r
	case wire.SMSGLogoutComplete:
		s.report.LogoutComplete = true
		s.deadMu.Lock()
		s.logoutComplete = true
		s.deadMu.Unlock()
	case wire.SMSGUpdateObject:
		s.capturePacket(p)
		u, err := objects.ParseUpdateObjectForBuild(p.Payload, s.opts.RealmBuild)
		if err != nil {
			return fmt.Errorf("world: SMSG_UPDATE_OBJECT: %w", err)
		}
		s.applyUpdate(u)
		now := s.transport().Now()
		s.world.Apply(u, now)
		s.combatDeaths(now)
		s.observeSelfIfTracking()
	case wire.SMSGOnMonsterMove:
		m, err := objects.ParseMonsterMove(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_ON_MONSTER_MOVE: %w", err)
		}
		s.report.MonsterMoves++
		s.world.ApplyMonsterMove(m, s.transport().Now())
	case wire.SMSGMoveUpdate:
		m, err := objects.ParseMoveUpdate(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_MOVE_UPDATE: %w", err)
		}
		s.report.MoveRelays++
		s.world.ApplyMoveUpdate(m.Info, nil, s.transport().Now())
	case wire.SMSGMoveUpdateTeleport:
		m, err := objects.ParseMoveUpdateTeleport(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_MOVE_UPDATE_TELEPORT: %w", err)
		}
		s.report.MoveRelays++
		s.world.ApplyMoveUpdate(m.Info, m.Speeds, s.transport().Now())
	case wire.SMSGMoveTeleport:
		t, err := objects.ParseMoveTeleport(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_MOVE_TELEPORT: %w", err)
		}
		s.report.Teleports++
		s.world.ApplyTeleport(t, s.transport().Now())
		if t.MoverGUID == s.self {
			// The graveyard teleport carries TransportGUID as an *engaged* empty GUID: RepopAtGraveyard passes
			// ObjectGuid::Empty into the Optional (Player.cpp:5254-5259), so the bit is set while the GUID is
			// empty. An empty transport is no transport (the capture's release teleport is exactly this shape).
			if (t.Transport != nil && !t.Transport.IsZero()) || t.HasVehicle {
				// An ack would tell the server the client is standing on the transport's seat, which this round
				// cannot model: report it and end the walk instead of sending an ack the client cannot honour.
				s.opts.logf("world: teleport onto a transport: not handled (transport %v, vehicle %v)", t.Transport != nil, t.HasVehicle)
				return fmt.Errorf("world: teleport onto a transport: not handled (transport %v, vehicle %v)", t.Transport != nil, t.HasVehicle)
			}
			// The server waits for CMSG_MOVE_TELEPORT_ACK before it accepts movement from the new position
			// (MovementHandler.cpp:424-480 reads it on the instance socket). Apply the destination, the facing and
			// the sequence to the mover first, so the first packet after the ack starts from there.
			sequence := t.Sequence
			if s.mover != nil {
				s.mover.SetTeleport(t.X, t.Y, t.Z, t.O, t.Sequence)
				sequence = s.mover.Sequence
				s.report.WalkClientPos = [4]float32{t.X, t.Y, t.Z, t.O}
			}
			if err := s.sendOnCtx(ctx, sideInstance, wire.CMSGMoveTeleportAck, objects.BuildTeleportAck(t.MoverGUID, sequence, clientTimeMs())); err != nil {
				return err
			}
			s.opts.logf("world: SMSG_MOVE_TELEPORT answered (map %d, client position %.2f %.2f %.2f)", s.world.MapID(), t.X, t.Y, t.Z)
		}
	case wire.SMSGMoveRoot, wire.SMSGMoveUnroot:
		guid, _, err := objects.ParseMoverFlag(p.Payload)
		if err != nil {
			return fmt.Errorf("world: the root/unroot push: %w", err)
		}
		if guid == s.self {
			// No ack: the server only validates the movement info an ack carries (HandleMovementAckMessage,
			// MovementHandler.cpp:777-784) and rounds 1-2 left SMSG_MOVE_ROOT unanswered with a healthy session.
			s.opts.logf("world: %s for the active mover (no ack sent, see OBJECTS-R1-NOTES.md)", wire.ServerName(p.Core))
		}
	case wire.SMSGQueryPlayerNamesResponse:
		names, err := objects.ParsePlayerNamesResponse(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_QUERY_PLAYER_NAMES_RESPONSE: %w", err)
		}
		for _, n := range names {
			if n.Found && n.Name != "" {
				s.world.SetName(n.GUID, n.Name)
			}
		}
	case wire.SMSGCreatureQueryResponse:
		creatures, err := objects.ParseCreatureQueryResponse(p.Payload)
		if err != nil {
			return fmt.Errorf("world: SMSG_QUERY_CREATURE_RESPONSE: %w", err)
		}
		for _, c := range creatures {
			if c.Found && c.Name != "" {
				s.world.SetEntryName(int32(c.Entry), c.Name)
			}
		}
	case wire.SMSGChat, wire.SMSGPrintNotification, wire.SMSGMotd, wire.SMSGChatServerMessage,
		wire.SMSGChatPlayerNotFound, wire.SMSGChatPlayerAmbiguous, wire.SMSGChatRestricted,
		wire.SMSGEmote, wire.SMSGTextEmote:
		return s.handleChat(p)
	case wire.SMSGPartyInvite, wire.SMSGPartyUpdate, wire.SMSGPartyMemberFullState,
		wire.SMSGPartyMemberPartialState, wire.SMSGGroupDecline, wire.SMSGGroupUninvite,
		wire.SMSGGroupDestroyed, wire.SMSGGroupNewLeader, wire.SMSGPartyCommandResult,
		wire.SMSGReadyCheckStarted, wire.SMSGReadyCheckResponse, wire.SMSGReadyCheckCompleted:
		return s.handleParty(p)
	case wire.SMSGDeathReleaseLoc, wire.SMSGCorpseReclaimDelay, wire.SMSGPreRessurect,
		wire.SMSGResurrectRequest, wire.SMSGCorpseLocation, wire.SMSGCorpseTransportQuery,
		wire.SMSGStandStateUpdate, wire.SMSGMoveSetRunSpeed,
		wire.SMSGMoveSetSwimSpeed, wire.SMSGMoveSetWaterWalk, wire.SMSGMoveSetLandWalk:
		return s.handleDeath(p)
	// The act round's NPC answers (forever-world-r0, npc.go): the menu lands on the runner's wait here, and the same
	// handler hands it to the spirit healer's flow.
	case wire.SMSGGossipMessage, wire.SMSGGossipComplete, wire.SMSGGossipOptionNPCInteract,
		wire.SMSGNPCInteractionOpenResult, wire.SMSGQuestGiverQuestList:
		return s.handleNPC(p)
	// Round A7's handlers live in mapchange.go; this is their one registration line.
	case wire.SMSGTransferPending, wire.SMSGSuspendToken, wire.SMSGResumeToken, wire.SMSGNewWorld,
		wire.SMSGTransferAborted, wire.SMSGUpdateLastInstance, wire.SMSGWorldServerInfo, wire.SMSGInstanceInfo:
		return s.handleTransfer(ctx, p)
	default:
		if isLootPacket(p.Core) {
			return s.handleLoot(p)
		}
		if isInventoryPacket(p.Core) {
			return s.handleInventory(p)
		}
		if isVendorPacket(p.Core) {
			return s.handleVendor(p)
		}
		s.handleCombat(p)
	}
	return nil
}

// handleCombat answers the combat, spell, aura, cooldown, power and experience packets. A packet this round's
// parser cannot consume exactly is counted (Report.ParseErrors) and merges nothing: it never fails the session,
// because the rest of the fight is still valid and the next packet is framed by its own header.
func (s *session) handleCombat(p worldconn.Packet) {
	now := s.transport().Now()
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGAttackStart:
		v, err := combat.ParseAttackStart(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplyAttackStart(v, now)
		if v.Attacker == s.self || v.Victim == s.self {
			s.fight.add("swing", fmt.Sprintf("%s attacked %s", s.fight.name(v.Attacker), s.fight.name(v.Victim)), 0)
		}
	case wire.SMSGAttackStop:
		v, err := combat.ParseAttackStop(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplyAttackStop(v, now)
		if v.Attacker == s.self {
			s.fight.attackStops++
		}
		if v.Attacker == s.self || v.Victim == s.self {
			s.fight.add("swing", fmt.Sprintf("%s stopped attacking %s", s.fight.name(v.Attacker), s.fight.name(v.Victim)), 0)
		}
	case wire.SMSGCancelCombat:
		s.world.ApplyCancelCombat(s.self, now)
	case wire.SMSGAttackSwingError:
		v, err := combat.ParseSwingError(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.fight.lastSwingErr = v.Reason
		s.fight.swingErrSeq++
		s.fight.add("swing", "swing error: "+v.Reason.String(), 0)
	case wire.SMSGAttackerStateUpdate:
		v, err := combat.ParseAttackerStateUpdate(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplyAttackRound(v, now)
		s.combatRow("melee", now, int32(v.MeleeSpellID), v.Damage, v.Attacker, v.Victim, "")
		if v.Attacker == s.self || v.Victim == s.self {
			s.fight.swing(v)
		}
	case wire.SMSGAttackSwingLandedLog:
		// The full log copy of a swing the basic packet already carried: parsed for the layout, not counted twice.
		if _, err := combat.ParseAttackSwingLanded(p.Payload); err != nil {
			s.noteParseError(p, err)
		}
	case wire.SMSGSpellStart:
		v, err := combat.ParseSpellStart(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplySpellCast(&v, false, now)
		s.combatCast("spell_start", now, &v)
		if s.fight.staleCast(v.SpellID, v.CastID, now) {
			return
		}
		s.fight.castStart(&v, now)
	case wire.SMSGSpellGo:
		v, err := combat.ParseSpellGo(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplySpellCast(&v, true, now)
		s.combatCast("spell_go", now, &v)
		s.itemCast.gone(&v, s.self)
		if s.fight.staleCast(v.SpellID, v.CastID, now) {
			return
		}
		s.fight.castGo(&v)
	case wire.SMSGCastFailed, wire.SMSGSpellFailure, wire.SMSGSpellFailedOther:
		var v combat.SpellFailure
		var err error
		switch p.Core {
		case wire.SMSGCastFailed:
			v, err = combat.ParseCastFailed(p.Payload)
		case wire.SMSGSpellFailure:
			v, err = combat.ParseSpellFailure(p.Payload)
		default:
			v, err = combat.ParseSpellFailedOther(p.Payload)
		}
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplySpellFailure(v, now)
		s.combatRow("spell_fail", now, v.SpellID, 0, v.Caster, wire.EmptyGUID, combat.SpellFailureName(v.Reason))
		if v.Caster == s.self || v.Caster.IsZero() {
			s.itemCast.failed(v)
			// CAST_FAILED is the pre-cast answer and carries the client's cast id; the failure forms carry the
			// server's (see fightLog.castFail). A failure of a generation that gave up is dropped first.
			if s.fight.staleCast(v.SpellID, v.CastID, now) {
				return
			}
			s.fight.castFail(v.SpellID, v.CastID, v.Reason, p.Core == wire.SMSGCastFailed)
		}
	case wire.SMSGSpellNonMeleeDamage:
		v, err := combat.ParseSpellNonMeleeDamage(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.combatRow("spell_damage", now, v.SpellID, v.Damage, v.Caster, v.Me, "")
		switch {
		case v.Me == s.self:
			s.fight.damageTaken += v.Damage
			s.fight.add("spell", fmt.Sprintf("spell %d hit you for %d", v.SpellID, v.Damage), v.Damage)
		case v.Caster == s.self:
			s.fight.damageDealt += v.Damage
			s.fight.add("spell", fmt.Sprintf("spell %d hit %s for %d", v.SpellID, s.fight.name(v.Me), v.Damage), v.Damage)
		}
	case wire.SMSGSpellHealLog:
		v, err := combat.ParseSpellHeal(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.combatRow("heal", now, v.SpellID, v.OriginalHeal, v.Caster, v.Target, "")
		if v.Target == s.self || v.Caster == s.self {
			s.fight.add("spell", fmt.Sprintf("spell %d healed %s for %d", v.SpellID, s.fight.name(v.Target), v.OriginalHeal), v.OriginalHeal)
		}
	case wire.SMSGSpellPeriodicAuraLog:
		v, err := combat.ParseSpellPeriodicAuraLog(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		for _, e := range v.Effects {
			kind := "periodic"
			if e.PeriodicTickIsHeal() {
				kind = "heal"
			}
			s.combatRow(kind, now, v.SpellID, e.Amount, v.Caster, v.Target, "")
		}
		if v.Target == s.self || v.Caster == s.self {
			for _, e := range v.Effects {
				s.fight.add("spell", fmt.Sprintf("spell %d ticked on %s for %d", v.SpellID, s.fight.name(v.Target), e.Amount), e.Amount)
			}
		}
	case wire.SMSGSpellEnergizeLog:
		v, err := combat.ParseSpellEnergize(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		if v.Target == s.self {
			s.fight.add("spell", fmt.Sprintf("spell %d gave %d %s", v.SpellID, v.Amount, combat.PowerName(v.Type)), 0)
		}
	case wire.SMSGSpellDelayed:
		if _, err := combat.ParseSpellDelayed(p.Payload); err != nil {
			s.noteParseError(p, err)
		}
	case wire.SMSGAuraUpdate:
		v, err := combat.ParseAuraUpdate(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		for _, ch := range s.world.ApplyAuraUpdate(&v, now) {
			switch ch.Kind {
			case objects.AuraAdded:
				s.combatRow("aura_applied", now, ch.SpellID, 0, ch.Caster, ch.Unit, "")
			case objects.AuraRemoved:
				s.combatRow("aura_removed", now, ch.SpellID, 0, ch.Caster, ch.Unit, "")
			}
		}
		s.observeSelfIfTracking()
		for _, info := range v.Auras {
			s.fight.aura(v.Unit, info)
		}
	case wire.SMSGSpellCooldown:
		v, err := combat.ParseSpellCooldown(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplyCooldowns(v, now)
	case wire.SMSGCooldownEvent:
		v, err := combat.ParseCooldownEvent(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ClearCooldown(s.self, v.SpellID, now)
	case wire.SMSGClearCooldown:
		v, err := combat.ParseClearCooldown(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ClearCooldown(s.self, int32(v.SpellID), now)
	case wire.SMSGClearCooldowns:
		v, err := combat.ParseClearCooldowns(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ClearCooldowns(s.self, v.Spells, now)
	case wire.SMSGPowerUpdate:
		v, err := combat.ParsePowerUpdate(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.world.ApplyPowerUpdate(&v, now)
	case wire.SMSGLogXPGain:
		v, err := combat.ParseXPGain(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.fight.xp += v.Amount
		s.fight.add("xp", fmt.Sprintf("%s: %d experience", combat.XPGainReasonName(v.Reason), v.Amount), 0)
	case wire.SMSGSendKnownSpells:
		v, err := combat.ParseKnownSpells(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return
		}
		s.known = append(s.known[:0], v.Known...)
		s.report.KnownSpells = append([]int32(nil), s.known...)
	}
}

// noteParseError counts a combat packet this round could not consume exactly and logs the first of each opcode.
func (s *session) noteParseError(p worldconn.Packet, err error) {
	s.report.ParseErrors++
	s.fight.parseErrors++
	if s.parseErrorLogged == nil {
		s.parseErrorLogged = make(map[string]bool)
	}
	name := p.Name
	if name == "" {
		name = fmt.Sprintf("wire 0x%06X", p.Wire)
	}
	if !s.parseErrorLogged[name] {
		s.parseErrorLogged[name] = true
		s.opts.logf("world: %s was not decoded (%v); it merges nothing", name, err)
	}
}

// applyUpdate records what the client learned about itself from an update: the create block of ThisIsYou carries the
// character's GUID, position and run speed. A later ThisIsYou create (a map change or a fresh burst) puts the
// character somewhere else, so the mover's belief follows it instead of keeping the first create's position.
// observeSelfIfTracking lets the death watch follow a model change without creating its state in sessions that do
// not use the death flow. A corpse run's walk is then cancelled the moment the model shows the character alive,
// rather than at its next heartbeat wait.
func (s *session) observeSelfIfTracking() {
	if s.death != nil {
		s.observeSelf(s.transport().Now())
	}
}

func (s *session) applyUpdate(u *objects.Update) {
	now := s.transport().Now()
	inv := s.tracker()
	if len(u.Destroy)+len(u.OutOfRange) != 0 {
		gone := make([]wire.GUID, 0, len(u.Destroy)+len(u.OutOfRange))
		gone = append(gone, u.Destroy...)
		gone = append(gone, u.OutOfRange...)
		inv.Destroyed(gone)
	}
	for _, b := range u.Blocks {
		// The server's own slots are the truth: the model merges them, and the tracker lets them confirm the moves
		// that touched them and drop the placements they contradict.
		if slots, ok := b.Info.ChangedInvSlots(); ok {
			inv.ServerSlots(now, slots)
		}
		if slots, ok := b.Info.ChangedContainerSlots(); ok {
			inv.ServerBag(now, b.GUID, slots)
		}
		// An update the decoder could not read must never be silent: slots it named are lost (stale), and an
		// ActivePlayerData update behind an equipment change is explained by the client's own move or is stale too.
		switch {
		case b.Info.InvSlotsUndecoded():
			inv.ServerUndecoded(now, true)
			s.noteInventoryStale("a server update named inventory slots next to fields this client does not decode")
		case b.Info.ActivePlayerSkipped():
			_, was := inv.Stale()
			inv.ServerUndecoded(now, false)
			if reason, stale := inv.Stale(); stale && !was {
				s.noteInventoryStale(reason)
			}
		}
		if b.Movement == nil || !b.Movement.ThisIsYou {
			continue
		}
		// Every own create is a fresh snapshot of the character (the login burst, a map change): the placements the
		// overlay guessed before it are settled by the slots this block carries, and the quest log is re-read from
		// the block on its next use (round R5).
		inv.Reset()
		if !s.selfSeen {
			s.selfSeen = true
			s.self = b.GUID
			if s.fight != nil {
				s.fight.self = b.GUID
			}
			if s.mover == nil {
				s.mover = &objects.Mover{
					GUID:       b.GUID,
					Sender:     s.transport(),
					ClientTime: func(time.Time) uint32 { return clientTimeMs() },
					Logf:       s.opts.logf,
					X:          b.Movement.X,
					Y:          b.Movement.Y,
					Z:          b.Movement.Z,
					O:          b.Movement.O,
					RunSpeed:   b.Movement.Run,
					Flags:      b.Movement.Flags,
				}
				s.world.SetActiveMover(b.GUID)
			}
			power, maxPower := b.Info.PowerValue()
			s.opts.logf("world: own create block: position %.2f %.2f %.2f, health %d/%d, %s %s, run speed %.2f yd/s, walk %.2f yd/s",
				b.Movement.X, b.Movement.Y, b.Movement.Z, b.Info.Health, b.Info.MaxHealth,
				combat.PowerName(int8(b.Info.DisplayPower)), combat.PowerText(int8(b.Info.DisplayPower), power, maxPower),
				b.Movement.Run, b.Movement.Walk)
		} else if s.mover != nil {
			s.mover.SetPosition(b.Movement.X, b.Movement.Y, b.Movement.Z, b.Movement.O)
			s.opts.logf("world: own create again: position reset to %.2f %.2f %.2f",
				b.Movement.X, b.Movement.Y, b.Movement.Z)
		}
		s.report.WalkClientPos = [4]float32{b.Movement.X, b.Movement.Y, b.Movement.Z, b.Movement.O}
	}
}

func (s *session) sendOn(side int, core uint32, payload []byte) error {
	return s.sendOnCtx(s.ctx, side, core, payload)
}

// sendOnCtx is sendOn with the caller's deadline (the abort logout keeps its own two seconds).
func (s *session) sendOnCtx(ctx context.Context, side int, core uint32, payload []byte) error {
	if err := s.linkDead(); err != nil {
		return err
	}
	// A session without sockets (the handler tests' fake transport) writes through it on either side, so the
	// transfer handlers are exercisable without a link.
	if s.realm == nil && s.instance == nil && s.fightSender != nil {
		return s.fightSender.Send(core, payload)
	}
	var err error
	switch side {
	case sideRealm:
		if s.realm == nil {
			return fmt.Errorf("world: no connection %d", side)
		}
		err = s.realm.Send(ctx, core, payload)
	case sideInstance:
		if s.instance == nil {
			return fmt.Errorf("world: no connection %d", side)
		}
		err = s.instance.Send(ctx, core, payload)
	default:
		return fmt.Errorf("world: no connection %d", side)
	}
	if err != nil && isLinkIOError(err) {
		// A write that fails on the socket is the same loss as a read that fails: name it, so the run reports
		// "connection lost" instead of a bare "broken pipe".
		s.setLinkDead(side, err)
		return s.deadErrorExcept(-1)
	}
	return err
}

// isLinkIOError reports whether an error came from the socket itself (a closed, reset or broken connection) rather
// than from the packet layer (an opcode without a wire number, a payload over the limit).
func isLinkIOError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

var errWaitTimeout = errors.New("world: wait timed out")

// waitFor reads events until match returns true, answering what the server asks in between (time syncs, active
// mover, logout packets). One absolute timeout bounds the wait.
func (s *session) waitFor(what string, timeout time.Duration, match func(worldconn.Packet) bool) (worldconn.Packet, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return worldconn.Packet{}, fmt.Errorf("world: no %s within %s", what, timeout)
		}
		ev, err := s.next(what, remaining)
		if err != nil {
			return worldconn.Packet{}, err
		}
		if ev.err != nil {
			if lost := s.deadErrorExcept(-1); lost != nil {
				return worldconn.Packet{}, lost
			}
			return worldconn.Packet{}, fmt.Errorf("world: the connection closed while waiting for %s: %w", what, ev.err)
		}
		if err := s.handle(s.ctx, ev); err != nil {
			return worldconn.Packet{}, err
		}
		if match(ev.p) {
			return ev.p, nil
		}
	}
}

func (s *session) next(what string, timeout time.Duration) (event, error) {
	return s.nextExceptCtx(s.ctx, what, timeout, -1)
}

// nextCtx is next on the caller's context instead of the session's. A stop or cleanup path handed a context detached
// from the session's own (context.WithoutCancel) keeps reading the same packet queue after that context ended; the
// caller's context is the wait's own end condition there.
func (s *session) nextCtx(ctx context.Context, what string, timeout time.Duration) (event, error) {
	return s.nextExceptCtx(ctx, what, timeout, -1)
}

// nextExceptCtx is nextCtx with one socket's death ignored: the logout tolerates the instance socket closing once
// the request is answered, because the completion travels on the realm socket.
func (s *session) nextExceptCtx(ctx context.Context, what string, timeout time.Duration, ignore int) (event, error) {
	end := time.Now().Add(timeout)
	for {
		// A packet the readers already delivered is handled before the link's death: it arrived first, and the
		// logout's response may sit in the queue when the server closes that socket.
		select {
		case ev := <-s.events:
			if ev.err != nil && ev.side == ignore {
				continue
			}
			return ev, nil
		default:
		}
		if err := s.deadErrorExcept(ignore); err != nil {
			// One packet may still be in the queue from before the reader died.
			select {
			case ev := <-s.events:
				if ev.err == nil && ev.side != ignore {
					return ev, nil
				}
			default:
			}
			return event{}, err
		}
		if err := s.mapChangeTimedOut(); err != nil {
			return event{}, err
		}
		remaining := time.Until(end)
		if s.mapChg.between {
			if left := mapChangeLimit - s.Now().Sub(s.mapChg.since); left < remaining {
				// Wake at the map change's own deadline, so a lost new world ends the run promptly instead of
				// waiting out this call's longer one.
				remaining = left
			}
		}
		if remaining <= 0 {
			if err := s.mapChangeTimedOut(); err != nil {
				return event{}, err
			}
			return event{}, errWaitTimeout
		}
		silent := false
		if _, at := s.lastPacket(); !at.IsZero() {
			silence := time.Since(at)
			if silence >= linkSilenceLimit {
				s.markStale(silentSide(ignore))
				return event{}, s.silenceLost(ignore)
			}
			if silence+remaining > linkSilenceLimit {
				remaining = linkSilenceLimit - silence
				silent = true
			}
		}
		realmCh, instanceCh := s.deadChs[sideRealm], s.deadChs[sideInstance]
		if ignore == sideRealm {
			realmCh = nil
		}
		if ignore == sideInstance {
			instanceCh = nil
		}
		timer := time.NewTimer(remaining)
		select {
		case ev := <-s.events:
			timer.Stop()
			if ev.err != nil && ev.side == ignore {
				continue
			}
			return ev, nil
		case <-timer.C:
			if silent {
				s.markStale(silentSide(ignore))
				return event{}, s.silenceLost(ignore)
			}
			// The timer may have been shortened by the map change's own deadline: that ended the run, and it must
			// not look like this wait's own timeout to a caller that treats one as the end of a stay.
			if err := s.mapChangeTimedOut(); err != nil {
				return event{}, err
			}
			return event{}, errWaitTimeout
		case <-realmCh:
			timer.Stop()
			continue // the dead check at the top reports it
		case <-instanceCh:
			timer.Stop()
			continue
		case <-ctx.Done():
			timer.Stop()
			return event{}, fmt.Errorf("world: waiting for %s: %w", what, ctx.Err())
		}
	}
}

// findCharacter matches the requested name against the character name and against name + surname, ignoring case.
func findCharacter(list []Character, want string) (Character, bool) {
	for _, c := range list {
		if strings.EqualFold(c.Name, want) || strings.EqualFold(c.DisplayName(), want) || strings.EqualFold(c.Name+" "+c.Surname, want) {
			return c, true
		}
	}
	return Character{}, false
}
