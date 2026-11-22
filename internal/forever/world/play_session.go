package world

// The live adapters of the player run (round P5a, #88): the driver's three player ports bound to a world session.
// Owner talks to the bot module through bot-commands v1 on the session's command channel, Group reads the party
// model and sends the group commands of G2, Player is the agent's own character (selection and attack, the fight
// tick, rest, the death watch, loot, objects, gossip). World, Walker and Clock are the commander's adapters of
// dungeon_session.go, reused unchanged.
//
// Like the commander's adapters they are thin: every read comes from the model the packet loop keeps, every send
// goes through the session, and nothing here runs on a goroutine of its own, so the run's one goroutine owns every
// call. A player run is never a GM run: the command link below admits the `.bot` verbs of the contract and nothing
// else, and does not ask for the GM guard (a player has no GM permission to confirm).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The adapters' bounds. The reply window is a variable so the tests shorten it.
var (
	// playReplyQuiet ends a `.bot` reply: the module answers in the tick it takes the line, so this long without a
	// new system line is the end of the answer (the replies carry no end marker).
	playReplyQuiet = 700 * time.Millisecond
	// playReplyDeadline bounds one whole request.
	playReplyDeadline = 8 * time.Second
)

const (
	// playChaseSlice bounds one tick's walk into melee reach, so the run's own loop (units, deaths, marks) is read
	// again at least this often while the agent runs after its target.
	playChaseSlice = 1500 * time.Millisecond
	// playMaxRefusedSwings is how many swings the server may end with no contact before the agent leaves the target
	// to the bots: a unit it cannot attack (friendly, flagged) is not worth a packet per tick.
	playMaxRefusedSwings = 3
	// playUseReach is how far from the point a plan names a use_object's object may stand.
	playUseReach = 15
	// unitDynFlagLootable is UNIT_DYNFLAG_LOOTABLE, the dynamic flag the server sets on a corpse the player may
	// loot (UnitDefines.h UnitDynFlags): the §3.7 rule loots only corpses that carry it.
	unitDynFlagLootable = 0x0001
	// unknownMap is the map of a party member the client cannot place: no real map has this id, so a member on it
	// is never "on the run's map".
	unknownMap = math.MaxUint32
)

// playPortErr maps the session's failures onto the ports' named errors: a dead link, and a walk or a chase cut by a
// transfer or a teleport (the run re-reads its position and goes on).
func playPortErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, objects.ErrMapChange), errors.Is(err, objects.ErrTeleported), errors.Is(err, ErrMapChange):
		return driver.ErrMapChange
	}
	return dungeonPortErr(err)
}

// newPlayPorts binds one session to the player run's six ports. ctx is the run's own context: the group commands
// and the death calls take none in the ports' signatures, so they run on it.
func newPlayPorts(ctx context.Context, s *session) driver.PlayPorts {
	return driver.PlayPorts{
		Owner:  newPlayOwner(s),
		Group:  &playGroup{ctx: ctx, s: s, zoneMap: map[uint16]uint32{}},
		Player: newPlayPlayer(ctx, s),
		World:  dungeonWorld{s: s},
		Walker: dungeonWalker{s: s},
		Clock:  dungeonClock{s: s},
	}
}

// --- Owner: bot-commands v1 over the command channel ---

// playLink is the player run's command transport: the session's own pump, and a send that admits one thing, a line
// of the contract (`.bot <verb> ...`). A player is not a GM, so the GM guard is not asked for; the say-echo watch is
// still on (feedCommand), and a line the server said aloud as chat stops every later send the way it does for the GM
// channel.
type playLink struct {
	cmdLink
}

// playLineAllowed is the closed vocabulary of the player run's command channel: `.bot`, one of the contract's verbs,
// and arguments of plain words. A newline or a chain separator never gets through (the validator below also refuses
// them), and nothing that is not `.bot` does, whatever it names.
func playLineAllowed(text string) bool {
	if strings.ContainsAny(text, "\r\n\x00") {
		return false
	}
	if !strings.HasPrefix(text, ".bot ") {
		return false
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return false
	}
	_, ok := botcmd.ParseVerb(fields[1])
	return ok
}

func (l *playLink) SendCommand(ctx context.Context, text string) error {
	if l.s.gmStop != nil {
		return l.s.gmStop
	}
	if !playLineAllowed(text) {
		return fmt.Errorf("world: a player run sends only bot-commands v1 lines, not %q", chat.Sanitise(text, 64))
	}
	return l.s.SendCommandCtx(ctx, text)
}

// playOwner is the Owner port over botcmd.Client on the session's command channel.
type playOwner struct {
	s *session
	c *botcmd.Client
	// who is the rows of the last `.bot who`, for the run's roster check.
	who []botcmd.WhoRow
}

func newPlayOwner(s *session) *playOwner {
	c := botcmd.NewClient(command.NewSession(&playLink{cmdLink: cmdLink{s: s}}))
	c.Base = s.commandOptions(command.Options{Quiet: playReplyQuiet, Deadline: playReplyDeadline})
	return &playOwner{s: s, c: c}
}

func (o *playOwner) Add(ctx context.Context, bot string) (botcmd.Reply, error) {
	r, err := o.c.Add(ctx, bot)
	return r, dungeonPortErr(err)
}

func (o *playOwner) Logout(ctx context.Context, bot string) (botcmd.Reply, error) {
	r, err := o.c.Logout(ctx, bot)
	return r, dungeonPortErr(err)
}

func (o *playOwner) Follow(ctx context.Context, bot string) (botcmd.Reply, error) {
	r, err := o.c.Follow(ctx, bot)
	return r, dungeonPortErr(err)
}

func (o *playOwner) Stay(ctx context.Context, bot string) (botcmd.Reply, error) {
	r, err := o.c.Stay(ctx, bot)
	return r, dungeonPortErr(err)
}

func (o *playOwner) Attack(ctx context.Context, bot string) (botcmd.Reply, error) {
	r, err := o.c.Attack(ctx, bot)
	return r, dungeonPortErr(err)
}

func (o *playOwner) Summon(ctx context.Context, bot string) (botcmd.Reply, error) {
	r, err := o.c.Summon(ctx, bot)
	return r, dungeonPortErr(err)
}

// Who reads `.bot who` and answers its header as the Owner port's reply; the rows stay in Rows.
func (o *playOwner) Who(ctx context.Context) (botcmd.Reply, error) {
	rows, err := o.c.Who(ctx)
	if err != nil {
		return botcmd.Reply{}, dungeonPortErr(err)
	}
	o.who = rows
	return botcmd.Reply{Verb: string(botcmd.VerbWho), OK: true, Text: fmt.Sprintf("%d bot(s)", len(rows))}, nil
}

// Rows is the rows of the last `.bot who`.
func (o *playOwner) Rows() []botcmd.WhoRow { return append([]botcmd.WhoRow(nil), o.who...) }

// Whisper says one word of the module's chat grammar A to one bot. botcmd.Whisper is the gate: the two words
// `release` and `corpse run` are the whole grammar. The module answers a whisper with nothing a reply could be
// classified from, so only the send can fail.
func (o *playOwner) Whisper(ctx context.Context, bot, word string) error {
	name, err := botcmd.Whisper(bot, word)
	if err != nil {
		return err
	}
	if err := o.s.linkDead(); err != nil {
		return dungeonPortErr(err)
	}
	lang, err := o.s.chatLanguage()
	if err != nil {
		return err
	}
	return dungeonPortErr(o.s.SendCtx(ctx, wire.CMSGChatMessageWhisper, chat.BuildWhisper(lang, name, word)))
}

// --- Group: the party model and G2's group commands ---

// playGroup is the Group port: the roster comes from the party model the packet loop keeps, and the commands are
// G2's own (each sends its packet and waits for the readback, D-7).
type playGroup struct {
	ctx context.Context
	s   *session
	// zoneMap is the map each zone was last seen on. The party's member stats carry a zone, not a map, and a member
	// out of view has no object to read a map from: a zone belongs to one map, so a member in view teaches the zone
	// and a member out of view is placed by it.
	zoneMap map[uint16]uint32
}

// Roster is the party's members, this character included, in roster order. A client with no valid roster (no
// group, a raid, a roster the model refused) has no party to read: nothing is returned rather than a guess.
func (g *playGroup) Roster() []driver.PartyMember {
	s := g.s
	s.initChat()
	if !s.party.InParty || !s.party.RosterValid {
		return nil
	}
	now := s.Now()
	cur := s.world.MapID()
	members := s.party.SnapshotOf()
	out := make([]driver.PartyMember, 0, len(members))
	for _, m := range members {
		pm := driver.PartyMember{
			Name:      m.Name,
			GUID:      m.GUID,
			Online:    m.Connected,
			Dead:      m.Status&party.StatusDead != 0,
			Ghost:     m.Status&party.StatusGhost != 0,
			HP:        int(m.Health),
			MaxHP:     int(m.MaxHealth),
			PowerType: int(m.PowerType),
			Power:     int(m.CurrentPower),
			MaxPower:  int(m.MaxPower),
			Pos:       driver.Vec3{float32(m.X), float32(m.Y), float32(m.Z)},
			Map:       unknownMap,
		}
		o, inView := s.world.Object(m.GUID)
		self := m.GUID == s.party.SelfGUID
		if inView || self {
			// In view the character is on this map; the zone it stands in is now known to belong to it.
			pm.Map = cur
			if m.ZoneID != 0 {
				g.zoneMap[m.ZoneID] = cur
			}
		} else if mp, ok := g.zoneMap[m.ZoneID]; ok && m.ZoneID != 0 {
			pm.Map = mp
		}
		switch {
		case self:
			pm.Online = true
			g.fillFromModel(&pm, o, inView, now)
			if x, y, z, ok := g.selfPos(); ok {
				pm.Pos = driver.Vec3{x, y, z}
			}
			pm.Dead, pm.Ghost = s.selfDead(), s.world.GhostAt(m.GUID)
		case inView:
			g.fillFromModel(&pm, o, true, now)
			pm.Dead = pm.Dead || s.world.DeadAt(m.GUID)
			pm.Ghost = pm.Ghost || s.world.GhostAt(m.GUID)
		}
		out = append(out, pm)
	}
	return out
}

// fillFromModel takes what the object model knows over the member stats, which are one packet old and a whole
// yard coarse: health, the member's own power bar, and the position.
func (g *playGroup) fillFromModel(pm *driver.PartyMember, o *objects.Object, inView bool, now time.Time) {
	if !inView || o == nil || !o.Info.HasUnit {
		return
	}
	if o.Info.MaxHealth > 0 {
		pm.HP, pm.MaxHP = int(o.Info.Health), int(o.Info.MaxHealth)
	}
	if slot, ok := o.Info.PowerSlot(uint8(pm.PowerType)); ok && o.Info.MaxPower[slot] > 0 {
		pm.Power, pm.MaxPower = int(o.Info.Power[slot]), int(o.Info.MaxPower[slot])
	}
	x, y, z := o.Position(now)
	pm.Pos = driver.Vec3{x, y, z}
}

func (g *playGroup) selfPos() (x, y, z float32, ok bool) {
	if g.s.mover == nil {
		return 0, 0, 0, false
	}
	x, y, z, _ = g.s.mover.Position()
	return x, y, z, true
}

// Leader is the current leader's guid, empty when there is no group.
func (g *playGroup) Leader() wire.GUID {
	g.s.initChat()
	if !g.s.party.InParty {
		return wire.EmptyGUID
	}
	return g.s.party.LeaderGUID
}

// LootMethod sets the loot method (no master) and waits for the roster's loot block.
func (g *playGroup) LootMethod(method party.LootMethod, threshold int) error {
	return dungeonPortErr(g.s.groupLootMethod(g.ctx, method, "", threshold))
}

// Mark sets one raid target icon and waits for its slot.
func (g *playGroup) Mark(target wire.GUID, mark party.Mark) error {
	return dungeonPortErr(g.s.groupMark(g.ctx, target, mark))
}

// Disband uninvites every other member (no disband packet exists) and waits for the group's end.
func (g *playGroup) Disband() error { return dungeonPortErr(g.s.groupDisband(g.ctx)) }

// --- Player: the agent's own character ---

// playPlayer is the Player port. It keeps the little state a tick needs: the target it is swinging at, whether the
// swing was started, and the spells it casts between swings.
type playPlayer struct {
	ctx context.Context
	s   *session

	target    wire.GUID
	attacking bool
	plan      spellPlan
	hook      FightHook
	// stopsAtSwing and contactAtSwing are the fight log's attack-stop and contact counts when the swing started: a
	// stop the server sent after them, with no contact, is a swing it refused.
	stopsAtSwing   int
	contactAtSwing int
	refused        int
	lastErrSeq     uint64
	gaveUp         map[wire.GUID]bool
}

func newPlayPlayer(ctx context.Context, s *session) *playPlayer {
	// The fight tick reads the fight log's attack-stop, contact and swing-error counters; a session builds one at
	// login, and the log is named for this character so its lines say "you".
	if s.fight == nil {
		s.fight = &fightLog{started: s.Now(), now: s.Now}
	}
	s.fight.self = s.self
	return &playPlayer{ctx: ctx, s: s, gaveUp: map[wire.GUID]bool{}}
}

// Select is CMSG_SET_SELECTION: the unit `.bot attack` takes for a bot.
func (p *playPlayer) Select(guid wire.GUID) error {
	if err := p.s.transport().Send(wire.CMSGSetSelection, combat.BuildSetSelection(guid)); err != nil {
		return playPortErr(fmt.Errorf("world: CMSG_SET_SELECTION: %w", err))
	}
	return nil
}

// Attack points the agent's own fight at a unit: the target is selected, and the swing starts on the next tick (the
// walk into reach and the swing are the tick's). A unit that is gone or dead by now is nothing to attack and is not
// an error (the pull's own loop picks the next one); one the server flags unattackable is left to the bots.
func (p *playPlayer) Attack(guid wire.GUID) error {
	s := p.s
	if guid.IsZero() || guid == s.self {
		return errors.New("world: an attack needs another unit")
	}
	if _, err := s.liveUnit(guid); err != nil {
		switch {
		case errors.Is(err, ErrTargetGone):
			p.endSwing()
			return nil
		case errors.Is(err, ErrCannotAttack):
			s.opts.logf("play: %s is not attackable; the bots take it", guid)
			p.endSwing()
			return nil
		}
		return err
	}
	if err := p.Select(guid); err != nil {
		return err
	}
	p.target, p.attacking, p.refused = guid, false, 0
	p.plan = planSpells(s.known)
	p.hook = s.castBetweenSwings(&p.plan, guid)
	return nil
}

// endSwing forgets the target; it sends nothing.
func (p *playPlayer) endSwing() { p.target, p.attacking = wire.GUID{}, false }

// StopAttack ends the agent's own attack.
func (p *playPlayer) StopAttack() error {
	was := p.attacking
	p.endSwing()
	if !was {
		return nil
	}
	if err := p.s.transport().Send(wire.CMSGAttackStop, combat.BuildAttackStop()); err != nil {
		return playPortErr(fmt.Errorf("world: CMSG_ATTACK_STOP: %w", err))
	}
	return nil
}

// FightTick keeps the agent's own fight going for one tick: it closes in on the target, faces it, starts the swing
// when it is in reach and casts the known spells between swings (the fight-check's own hook). It sends nothing when
// the swing is already on and the target is in reach, so a tick costs no packet in the steady state. The server
// ending the attack with no contact three times leaves the target to the bots; a dead target ends the swing.
func (p *playPlayer) FightTick(ctx context.Context, target wire.GUID) error {
	s := p.s
	if target.IsZero() {
		return nil
	}
	if err := s.linkDead(); err != nil {
		return dungeonPortErr(err)
	}
	if s.betweenMaps() {
		return driver.ErrMapChange
	}
	if s.mover == nil {
		return errors.New("world: no mover (the character's create block never arrived)")
	}
	if p.gaveUp[target] {
		return nil
	}
	if p.target != target {
		if err := p.Attack(target); err != nil {
			return err
		}
		if p.target != target {
			return nil // the target is gone: nothing to swing at
		}
	}
	if s.selfDead() {
		p.endSwing()
		return nil
	}
	obj, ok := s.world.Object(target)
	if !ok || obj.Dead || (obj.Info.Health <= 0 && obj.Info.MaxHealth > 0) {
		return p.StopAttack()
	}
	if p.attacking && s.fight.attackStops > p.stopsAtSwing {
		// The server ended the attack. With contact it is the end of a fight (an evade, a death on its way); with none
		// it refused the target.
		if s.fight.contact == p.contactAtSwing {
			p.refused++
			if p.refused >= playMaxRefusedSwings {
				s.opts.logf("play: the server ended the swing on %s %d times with no contact; the bots take it", target, p.refused)
				p.gaveUp[target] = true
				p.endSwing()
				return nil
			}
		}
		p.attacking = false
	}

	link := s.transport()
	now := link.Now()
	self := s.selfObject()
	tx, ty, tz := obj.Position(now)
	reach := meleeReach(self, obj)
	dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
	forced := false
	if s.fight.swingErrSeq != p.lastErrSeq {
		p.lastErrSeq = s.fight.swingErrSeq
		forced = s.fight.lastSwingErr == combat.SwingNotInRange
	}
	if dist > reach-0.3 || forced {
		stopAt := reach - 0.3
		if forced {
			stopAt = min(reach*0.85, dist-forcedStep)
			if stopAt < 0.2 {
				stopAt = 0.2
			}
		}
		goal := objects.ChaseGoal{
			Dest: func(now time.Time) (float32, float32, float32) { return obj.Position(now) },
			Arrived: func(now time.Time) bool {
				x, y, z := obj.Position(now)
				return objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), x, y, z) <= stopAt
			},
		}
		cctx, cancel := context.WithTimeout(ctx, playChaseSlice)
		err := s.mover.ChaseTo(cctx, goal)
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			// The slice ended with the target still running: the run's loop reads the world and calls again.
		default:
			return playPortErr(err)
		}
		tx, ty, tz = obj.Position(link.Now())
		dist = objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), tx, ty, tz)
	}
	want := objects.OrientationTo(s.MoverX(), s.MoverY(), tx, ty)
	if absf(normalizeAngle(s.MoverO()-want)) > 0.2 {
		if err := s.mover.Face(want); err != nil {
			return playPortErr(fmt.Errorf("world: facing the target: %w", err))
		}
	}
	if !p.attacking && dist <= reach-0.3 {
		if err := link.Send(wire.CMSGAttackSwing, combat.BuildAttackSwing(target)); err != nil {
			return playPortErr(fmt.Errorf("world: CMSG_ATTACK_SWING: %w", err))
		}
		p.attacking = true
		p.stopsAtSwing, p.contactAtSwing = s.fight.attackStops, s.fight.contact
	}
	if p.attacking && p.hook != nil {
		if err := p.hook(ctx, s); err != nil {
			return playPortErr(err)
		}
	}
	return nil
}

// Rest sits the agent's own health back up to the bar (the fight-check's rest) and returns; the run waits for the
// rest of the party itself.
func (p *playPlayer) Rest(ctx context.Context, limit time.Duration) error {
	if err := p.s.linkDead(); err != nil {
		return dungeonPortErr(err)
	}
	if p.s.mover == nil {
		return nil
	}
	p.s.rest(ctx, 0.8, limit)
	return dungeonPortErr(p.s.linkDead())
}

// Phase is the character's own state, from the death watch (which derives it from the model on every look).
func (p *playPlayer) Phase() driver.PlayerPhase {
	s := p.s
	if s.self.IsZero() {
		return driver.PhaseAlive
	}
	s.observeSelf(s.transport().Now())
	switch s.deathState().phase {
	case DeathCorpse:
		return driver.PhaseDead
	case DeathGhost:
		return driver.PhaseGhost
	case DeathResurrecting:
		if s.world.GhostAt(s.self) {
			return driver.PhaseGhost
		}
		return driver.PhaseDead
	}
	return driver.PhaseAlive
}

// Release is CMSG_REPOP_REQUEST and the wait for the ghost (the death watch's own release). A character that is
// not dead any more has nothing to release: that is the state the call was after.
func (p *playPlayer) Release() error {
	err := p.s.Release(p.ctx)
	if errors.Is(err, ErrNotDead) {
		return nil
	}
	return playPortErr(err)
}

// RezOffer is the resurrect offer that is up and who offers it: a party member by roster name first (the model's
// own name cache may not hold a bot), else the offerer's cached name.
func (p *playPlayer) RezOffer() (driver.RezOffer, bool) {
	s := p.s
	now := s.transport().Now()
	s.observeSelf(now)
	offer := s.deathState().liveOffer(now)
	if offer == nil {
		return driver.RezOffer{}, false
	}
	s.initChat()
	if m, ok := s.party.Members[offer.Offerer]; ok && m.Name != "" {
		return driver.RezOffer{By: m.Name}, true
	}
	return driver.RezOffer{By: s.offererName(offer)}, true
}

// AcceptRez answers the offer that is up with accept and waits for the resurrection.
func (p *playPlayer) AcceptRez() error { return playPortErr(p.s.AcceptResurrect(p.ctx)) }

// DeclineRez refuses the offer that is up, so the next one may come.
func (p *playPlayer) DeclineRez() error { return playPortErr(p.s.DeclineResurrect(p.ctx)) }

// Loot opens one corpse or one game object's window, votes greed on every roll the window starts (D-9), takes
// everything and closes it, all through the session's loot flow. A corpse the dynamic flag does not show lootable is
// not tried (the server would only answer a refusal), and a window that never opens is ErrCannotLoot.
func (p *playPlayer) Loot(ctx context.Context, guid wire.GUID) error {
	s := p.s
	if err := s.linkDead(); err != nil {
		return dungeonPortErr(err)
	}
	o, ok := s.world.Object(guid)
	if !ok {
		return driver.ErrCannotLoot
	}
	isObject := o.Info.HasGameObject || o.Type == objects.TypeGameObject
	if !isObject && o.Info.DynamicFlags&unitDynFlagLootable == 0 {
		return driver.ErrCannotLoot
	}
	release, err := s.lootTarget(ctx, o)
	defer release()
	if err != nil {
		if errors.Is(err, ErrLootRefused) || errors.Is(err, ErrNoLootWindow) {
			return driver.ErrCannotLoot
		}
		return playPortErr(err)
	}
	if !s.report.Loot.Opened || s.loot.window == nil {
		return driver.ErrCannotLoot
	}
	if err := s.voteRolls(ctx, s.loot.window, true, loot.RollGreed); err != nil {
		return playPortErr(err)
	}
	return playPortErr(s.takeLoot(ctx, s.loot.window, 0, true))
}

// UseObject uses the game object of this entry that stands nearest the point (CMSG_GAMEOBJ_USE), walking into reach
// first when the run stopped short. The use's effect is the run's to read from Objects: a use that changes nothing is
// not an error here, because the step's own wait is what judges it. A use that opens a loot window gives it back.
func (p *playPlayer) UseObject(ctx context.Context, entry uint32, stand driver.Vec3) error {
	s := p.s
	if err := s.linkDead(); err != nil {
		return dungeonPortErr(err)
	}
	matches, _ := s.objectsOfEntry(stand[0], stand[1], stand[2], playUseReach, int32(entry))
	if len(matches) == 0 {
		return fmt.Errorf("world: no game object of entry %d within %d yd of the stand", entry, playUseReach)
	}
	release, err := s.lootTarget(ctx, matches[0])
	defer release()
	if err != nil && !errors.Is(err, ErrNoLootWindow) && !errors.Is(err, ErrLootRefused) {
		return playPortErr(err)
	}
	if s.loot.window != nil {
		if rerr := s.releaseLoot(ctx, s.loot.window); rerr != nil {
			return playPortErr(rerr)
		}
	}
	return nil
}

// Gossip talks to the nearest creature of the entry, reads its menu and picks the option the plan names, through the
// act runner's own steps (npc, open, select). A menu without the option is ErrNoGossipOption: the menu lags the
// event that adds an option, and the run asks again.
func (p *playPlayer) Gossip(ctx context.Context, entry uint32, option int) error {
	s := p.s
	if err := s.linkDead(); err != nil {
		return dungeonPortErr(err)
	}
	a := &actState{}
	step := func(verb string, args map[string]string) ActStep { return ActStep{Verb: verb, Args: args} }
	if err := s.actNPC(ctx, a, step(actVerbNPC, map[string]string{"entry": strconv.FormatUint(uint64(entry), 10)})); err != nil {
		return playPortErr(err)
	}
	if err := s.actOpen(ctx, a, step(actVerbOpen, nil)); err != nil {
		return playPortErr(err)
	}
	has := false
	for _, o := range a.menu.Options {
		if int(o.ID) == option {
			has = true
			break
		}
	}
	if !has || a.quests != nil {
		_ = s.actClose(ctx, a, step(actVerbClose, nil))
		return driver.ErrNoGossipOption
	}
	return playPortErr(s.actSelect(ctx, a, step(actVerbSelec, map[string]string{"option": strconv.Itoa(option)})))
}

// Objects is the game objects in the agent's own view, with the state a plan names (the raw GOState: 0 active, 1
// ready, 2 active_alt; any other value reads as "").
func (p *playPlayer) Objects() []driver.PlayObject {
	s := p.s
	var x, y, z float32
	if s.mover != nil {
		x, y, z, _ = s.mover.Position()
	}
	objs := s.world.GameObjects(x, y, z)
	out := make([]driver.PlayObject, 0, len(objs))
	for _, o := range objs {
		out = append(out, driver.PlayObject{
			GUID:  o.GUID,
			Entry: uint32(o.Info.Entry),
			Pos:   driver.Vec3{o.X, o.Y, o.Z},
			State: playObjectState(o.Info.GOState),
		})
	}
	return out
}

// playObjectState is a GOState in the words a plan names it by.
func playObjectState(st int8) string {
	switch st {
	case 0:
		return "active"
	case 1:
		return "ready"
	case 2:
		return "active_alt"
	}
	return ""
}

// compile-time interface checks: the player run's ports must stay satisfiable by these types.
var (
	_ driver.Owner      = (*playOwner)(nil)
	_ driver.GroupPort  = (*playGroup)(nil)
	_ driver.PlayerPort = (*playPlayer)(nil)
)
