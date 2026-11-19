package world

// Round A2: `forever fb-dungeon`, the WoW Forever Bots dungeon run. The module's `.bot lfg` fills the group with
// the account's companions, `.bot dungeon <name>` queues a dungeon and puts the group inside its door, `.bot lead on`
// hands the dungeon to the tank bot, and the finder's state is polled through LFG_REQ/LFG_RESP. The run is "inside"
// only when BOTH the module says `inside` AND the client's own map change confirmed the instance; neither signal
// alone is taken as proof. Drift in any answer fails softly: the run says what it could not read and never invents a
// state.
//
// The module expects the player to follow the leading tank (its lead answer says it "waits for mana and for you").
// This client does not walk, so it stays where it entered; the tank holds when the player falls behind. The run
// reports the tank's own party lines ("waiting for mana", "waiting for you") and the module's event pushes.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/fbcmd"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The run's defaults; Options may shorten them for tests.
const (
	fbDungeonPollDefault      = 2 * time.Second
	fbDungeonFormWaitDefault  = 3 * time.Minute
	fbDungeonEnterWaitDefault = 3 * time.Minute
	fbDungeonLeaveWaitDefault = 30 * time.Second
	// fbDungeonCleanupBudget is the cleanup's own bound, reserved apart from the run's deadline: the closing steps
	// run on a context of their own, so an expired operational context (or a caller's cancel) does not skip them.
	// Lead off, leave (bounded by fbDungeonLeaveWait) and four dismissals (fbDismissWait each) fit well inside it,
	// with room for the commands' round-trips.
	fbDungeonCleanupBudget = 2 * time.Minute
	// fbDungeonFillSize is the party the run fills to before queueing.
	fbDungeonFillSize = 5
	// fbDungeonReportEvery is how often the watch prints a health snapshot (deaths and events print at once).
	fbDungeonReportEvery = 30 * time.Second
	// fbDungeonPartyLines and fbDungeonDeaths bound what the report keeps.
	fbDungeonPartyLines = 64
	fbDungeonDeaths     = 16
)

// FBDungeonHealth is one watch sample of one bot.
type FBDungeonHealth struct {
	Name     string
	HP       int
	Mana     int
	Zone     string
	LastRule string
	Distance int
}

// FBDungeonReport is what fb-dungeon observed.
type FBDungeonReport struct {
	About    string
	Language string

	LfgLine   string
	LfgErr    string
	PartySize int
	Party     []string

	QueueLine string
	QueueErr  string
	Dungeon   string

	Phases    []string // the finder phases seen, in order, consecutive repeats collapsed
	Status    pbc.LfgStatus
	EnterErr  string
	Inside    bool
	StartMap  int32
	InsideMap int32
	WantMap   uint32 // the dungeon's map from the public default table, 0 when unknown
	InsideWhy string

	LeadOnLine  string
	LeadOnErr   string
	LeadTank    string
	LeadOutside bool
	// LeadTankGUID is the lead answer's tank resolved to a GUID: it is in the client's party and the
	// authenticated bot roster lists its name. A zero GUID means the identity is unknown.
	LeadTankGUID wire.GUID
	// LeadTankWhy says why no tank identity is known, when one is needed and missing.
	LeadTankWhy string
	FollowNote  string

	Watch        time.Duration
	WatchSamples int
	Health       []FBDungeonHealth
	Deaths       []string
	PartyLines   []string
	Events       []pbc.Event
	EventsLost   int

	LeadOffLine string
	LeadOffErr  string
	LeaveLine   string
	LeaveErr    string
	CancelLine  string
	CancelErr   string
	LeftMap     int32
	LeftMapSet  bool
	LeaveMapErr string

	Dismissed  []string
	DismissErr string

	// Cleanup is each closing step's result, in order, whatever the run's exit was. CleanupErr names the steps
	// whose answer could not be read (a lost link, an expired context), so a run never hides a half-undone group.
	Cleanup    []string
	CleanupErr string

	PBC        string
	PBCReason  string
	Frames     int
	Forged     int
	PBCDropped int
}

// fbTank is the one party talker the run accepts as the lead tank. The module's lead answer names it, the client's
// party gives that name a GUID, and the authenticated bot roster confirms the same name is a bot of this account: a
// line is the tank's only when its sender GUID is this GUID. A zero GUID is the state before the identity is known;
// lines arriving then are kept nowhere, because this client cannot tell the tank from any other party member.
type fbTank struct {
	guid  wire.GUID
	name  string
	why   string // why no identity is known, for the report
	noted bool
}

func (s *session) fbDungeonPoll() time.Duration {
	if s.opts.FBDungeonPoll > 0 {
		return s.opts.FBDungeonPoll
	}
	return fbDungeonPollDefault
}

func (s *session) fbDungeonFormWait() time.Duration {
	if s.opts.FBDungeonFormWait > 0 {
		return s.opts.FBDungeonFormWait
	}
	return fbDungeonFormWaitDefault
}

func (s *session) fbDungeonEnterWait() time.Duration {
	if s.opts.FBDungeonEnterWait > 0 {
		return s.opts.FBDungeonEnterWait
	}
	return fbDungeonEnterWaitDefault
}

func (s *session) fbDungeonLeaveWait() time.Duration {
	if s.opts.FBDungeonLeaveWait > 0 {
		return s.opts.FBDungeonLeaveWait
	}
	return fbDungeonLeaveWaitDefault
}

func (s *session) fbDungeonCleanupWait() time.Duration {
	if s.opts.FBDungeonCleanupWait > 0 {
		return s.opts.FBDungeonCleanupWait
	}
	return fbDungeonCleanupBudget
}

// fbDungeonRunContext bounds the run's own steps and keeps the cleanup's budget out of them: with a public deadline
// the steps end the cleanup budget before it, so the closing steps still have a live context (and live sockets) to
// work on even when the run's own window runs out. Without a deadline, or with less than the budget left, the steps
// get the run's context unchanged.
func fbDungeonRunContext(ctx context.Context, reserve time.Duration) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	op := deadline.Add(-reserve)
	if !time.Now().Before(op) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, op)
}

// fbDungeon drives `forever fb-dungeon`.
func (s *session) fbDungeon(ctx context.Context) error {
	rep := &s.report.FBDungeon
	s.initChat()
	s.fbQuiet = true
	// The run's steps get their own window; the cleanup's budget stays outside it (see fbDungeonRunContext).
	op, stopOps := fbDungeonRunContext(ctx, s.fbDungeonCleanupWait())
	defer stopOps()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 4 * time.Second
	}
	if err := s.collect(op, settle); err != nil {
		return err
	}
	if err := s.RegisterAddonPrefixes(pbc.Prefix); err != nil {
		return fmt.Errorf("world: registering the add-on prefix: %w", err)
	}
	s.opts.logf("fb-dungeon: registered the add-on prefix %s", pbc.Prefix)

	link := fbLink{s: s}
	c := fbcmd.NewClient(link)
	c.Base = s.commandOptions(command.Options{})

	about, err := c.Probe(op)
	if err != nil {
		s.report.FBDungeon.PBCReason = "the module is absent"
		s.summariseFBDungeon()
		return fmt.Errorf("world: the bot module probe: %w", err)
	}
	rep.About = about
	s.opts.logf("fb-dungeon: module: %s", about)

	if line, err := c.Language(op, "en"); err != nil {
		rep.Language = chat.Sanitise(err.Error(), 300)
		s.opts.logf("fb-dungeon: language: %v", err)
	} else {
		rep.Language = line
		s.opts.logf("fb-dungeon: language: %s", line)
	}

	rep.StartMap = s.report.MapID
	name := strings.TrimSpace(s.opts.FBDungeonName)
	if name == "" {
		name = "rfc"
	}
	if d, ok := fbcmd.LookupDungeon(name); ok {
		rep.WantMap = d.MapID
	}

	// 1. Fill the party to five with `.bot lfg`.
	lr, lerr := c.Lfg(op, s.opts.FBDungeonRole)
	if lerr != nil {
		rep.LfgErr = chat.Sanitise(lerr.Error(), 400)
		s.opts.logf("fb-dungeon: lfg: %v", lerr)
	} else {
		rep.LfgLine = lr.Line
		s.opts.logf("fb-dungeon: lfg: %s", lr.Line)
	}
	if lerr == nil || errors.Is(lerr, fbcmd.ErrNoAck) || fbCleanupFatal(lerr) {
		// The group may exist from here on (a fatal answer means the command's effect is unknown, not that nothing
		// happened). Every exit — a refused command, a lost link, an expired run window — runs the cleanup, on a
		// context of its own: the run's own window must not be able to skip the closing steps. A cleanup that
		// cannot get its answers says so (CleanupErr) instead of hiding a left-behind group.
		defer func() {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.fbDungeonCleanupWait())
			defer cancel()
			s.fbDungeonCleanupRun(cctx, c, rep)
		}()
		if lerr == nil || errors.Is(lerr, fbcmd.ErrNoAck) {
			if err := s.fbFillWatch(op, rep, s.fbDungeonFormWait()); err != nil {
				s.summariseFBDungeon()
				return err
			}
		}
	}
	if rep.PartySize < fbDungeonFillSize {
		why := fmt.Sprintf("the party has %d member(s), not %d", rep.PartySize, fbDungeonFillSize)
		if rep.LfgErr != "" {
			rep.LfgErr += "; " + why
		} else {
			rep.LfgErr = why
		}
		s.opts.logf("fb-dungeon: the party did not fill: %d member(s) after %s", rep.PartySize, s.fbDungeonFormWait())
		return nil
	}

	// 2. Queue the dungeon.
	qr, qerr := c.QueueDungeon(op, name, s.opts.FBDungeonRole)
	if qerr != nil {
		rep.QueueErr = chat.Sanitise(qerr.Error(), 400)
		s.opts.logf("fb-dungeon: queue %s: %v", chat.Sanitise(name, 64), qerr)
		return nil
	}
	rep.QueueLine, rep.Dungeon = qr.Line, qr.Name
	s.opts.logf("fb-dungeon: queue: %s", qr.Line)

	// 3. Wait for both signals. The party tap runs from here: the identity it accepts is unknown until the module
	// names its tank at step 4, so nothing said in this phase is kept as a tank line.
	tank := &fbTank{why: "the module has not named its lead tank yet"}
	if err := s.fbDungeonEnter(op, c, rep, tank); err != nil {
		return err
	}
	if !rep.Inside {
		s.opts.logf("fb-dungeon: not inside: %s", rep.EnterErr)
		return nil
	}

	// 4. Hand the lead to the tank.
	lead, lerr := c.Lead(op, "on")
	if lerr != nil {
		rep.LeadOnErr = chat.Sanitise(lerr.Error(), 400)
		s.opts.logf("fb-dungeon: lead on: %v", lerr)
		s.fbTankRefused(rep, tank, "the module refused the lead-on command")
	} else {
		rep.LeadOnLine, rep.LeadTank, rep.LeadOutside = lead.Line, lead.Tank, lead.Outside
		s.opts.logf("fb-dungeon: lead on: %s", lead.Line)
		if lead.Outside {
			rep.LeadOnErr = "the module answered that the tank leads once inside: it and the client's map disagree"
			s.opts.logf("fb-dungeon: %s", rep.LeadOnErr)
			s.fbTankRefused(rep, tank, "the module did not take the lead yet")
		} else {
			rep.FollowNote = "the module expects the player to follow the tank; this client stays at the entrance and the tank holds when the player falls behind"
			s.opts.logf("fb-dungeon: %s", rep.FollowNote)
			if err := s.fbDungeonResolveTank(op, c, rep, tank, lead.Tank); err != nil {
				return err
			}
		}
	}
	if rep.LeadOnErr != "" {
		s.opts.logf("fb-dungeon: watching without the tank lead (the module refused it)")
	}

	// 5. Watch.
	if err := s.fbDungeonWatch(op, c, rep, tank); err != nil {
		return err
	}
	// 6. The closing steps run from the deferred cleanup, whatever this function returns.
	return nil
}

// fbFillWatch waits for the party model to list fbDungeonFillSize members, printing each join. It is the `done`
// condition of `.bot lfg`: the module's answer alone starts the bots, the party model says they are in.
func (s *session) fbFillWatch(ctx context.Context, rep *FBDungeonReport, timeout time.Duration) error {
	deadline := s.Now().Add(timeout)
	last := -1
	for {
		size := 0
		if s.party.RosterValid {
			size = len(s.party.Members)
		}
		if size != last {
			if size > 0 {
				rep.Party = s.fbPartyNames()
				s.opts.logf("fb-dungeon: party: %d member(s): %s", size, strings.Join(rep.Party, ", "))
			}
			last = size
		}
		rep.PartySize = size
		if size >= fbDungeonFillSize {
			return nil
		}
		remaining := deadline.Sub(s.Now())
		if remaining <= 0 {
			return nil
		}
		if err := s.collect(ctx, min(remaining, s.fbDungeonPoll())); err != nil {
			return err
		}
	}
}

// fbPartyNames lists the party's member names, this character's included, in the roster's order.
func (s *session) fbPartyNames() []string {
	if s.party == nil || !s.party.RosterValid {
		return nil
	}
	out := make([]string, 0, len(s.party.Members))
	for _, guid := range s.party.Order {
		if m := s.party.Members[guid]; m != nil && m.Name != "" {
			out = append(out, chat.SanitiseName(m.Name))
		}
	}
	return out
}

// fbDungeonEnter polls the finder until BOTH the module says `inside` AND the client's own map change confirmed the
// instance, or the bound runs out. A protocol failure fails soft: the caller is told what could not be read.
func (s *session) fbDungeonEnter(ctx context.Context, c *fbcmd.Client, rep *FBDungeonReport, tank *fbTank) error {
	deadline := s.Now().Add(s.fbDungeonEnterWait())
	prevFeed := s.cmdFeed
	s.cmdFeed = s.fbDungeonPartyTap(rep, tank)
	defer func() { s.cmdFeed = prevFeed }()
	lastPhase := ""
	badAnswers := 0
	softErr := ""
	for {
		if err := s.collect(ctx, s.fbDungeonPoll()); err != nil {
			return err
		}
		st, err := c.LfgStatus(ctx)
		if err != nil {
			if errors.Is(err, ErrLinkLost) {
				return err
			}
			switch {
			case errors.Is(err, fbcmd.ErrPBCOff):
				rep.EnterErr = "the add-on protocol is unavailable: " + chat.Sanitise(err.Error(), 200)
				rep.PBCReason = s.pbcOff
				s.opts.logf("fb-dungeon: lfg: %v (the entry cannot be confirmed)", err)
				return nil
			case errors.Is(err, pbc.ErrProtoMismatch), errors.Is(err, fbcmd.ErrBadAnswer):
				badAnswers++
				softErr = chat.Sanitise(err.Error(), 200)
				rep.PBC, rep.PBCReason = "ok", softErr
				if badAnswers == 1 {
					s.opts.logf("fb-dungeon: lfg: %v (the protocol stays on)", err)
				}
			default:
				return err
			}
		} else {
			rep.Status = st
			if st.Phase != lastPhase {
				rep.Phases = append(rep.Phases, st.Phase)
				lastPhase = st.Phase
				s.opts.logf("fb-dungeon: finder: %s (%s), %d/%d member(s), waited %ds",
					chat.Sanitise(st.Phase, 16), chat.Sanitise(st.Name, 80), st.Members, st.Size, st.WaitedSec)
			}
			mapOK, why := s.fbMapConfirms(rep.StartMap, rep.WantMap)
			if st.Phase == "inside" && mapOK {
				rep.Inside = true
				rep.InsideMap = s.report.MapID
				rep.InsideWhy = why
				rep.PBC = "ok"
				s.opts.logf("fb-dungeon: inside confirmed: the module says inside (%s) and the client's map change put it on map %d",
					chat.Sanitise(st.Name, 80), rep.InsideMap)
				return nil
			}
		}
		remaining := deadline.Sub(s.Now())
		if remaining <= 0 {
			_, why := s.fbMapConfirms(rep.StartMap, rep.WantMap)
			phase := lastPhase
			if phase == "" {
				phase = "unknown"
			}
			rep.EnterErr = fmt.Sprintf("no confirmed entry within %s: the finder's phase is %q, the client's map %d (%s)",
				s.fbDungeonEnterWait(), phase, s.report.MapID, why)
			if softErr != "" {
				rep.EnterErr += "; the LFG answer could not be read: " + softErr
			}
			s.opts.logf("fb-dungeon: %s", rep.EnterErr)
			return nil
		}
	}
}

// fbMapConfirms reports whether the client's own map change says the character is on the instance. A known dungeon
// name ties the map to its default map id; an unknown word only needs a change away from the map the queue started
// on. The second return says why not, for the report.
func (s *session) fbMapConfirms(startMap int32, want uint32) (bool, string) {
	id := s.report.MapID
	if id == startMap {
		return false, "the client is still on the map it queued from"
	}
	if want != 0 && uint32(id) != want {
		return false, fmt.Sprintf("the client's map change went to %d, not the dungeon's map %d", id, want)
	}
	return true, ""
}

// fbDungeonWatch watches for the configured time, reporting finder state, the bots' health, deaths, the tank's party
// lines and the module's event pushes. A protocol failure stops the protocol reads (soft) but not the party log.
func (s *session) fbDungeonWatch(ctx context.Context, c *fbcmd.Client, rep *FBDungeonReport, tank *fbTank) error {
	watch := s.opts.FBDungeonWatch
	if watch <= 0 {
		return nil
	}
	prevFeed := s.cmdFeed
	s.cmdFeed = s.fbDungeonPartyTap(rep, tank)
	defer func() { s.cmdFeed = prevFeed }()

	start := s.Now()
	end := start.Add(watch)
	lastReport := start
	dead := map[string]bool{}
	var rows []pbc.RosterEntry
	lfgOff := ""
	s.opts.logf("fb-dungeon: watching for %s", watch)
	for {
		now := s.Now()
		if !now.Before(end) {
			break
		}
		step := end.Sub(now)
		if step > s.fbDungeonPoll() {
			step = s.fbDungeonPoll()
		}
		if err := s.collect(ctx, step); err != nil {
			return err
		}
		if lfgOff == "" {
			st, err := c.LfgStatus(ctx)
			if err != nil {
				if errors.Is(err, ErrLinkLost) {
					return err
				}
				if errors.Is(err, fbcmd.ErrPBCOff) || errors.Is(err, pbc.ErrProtoMismatch) || errors.Is(err, fbcmd.ErrBadAnswer) {
					lfgOff = chat.Sanitise(err.Error(), 300)
					rows = nil
					s.noticeFBStatus(rep, lfgOff)
					s.opts.logf("fb-dungeon: finder state unavailable (%s); watching the party and the map only", lfgOff)
				} else {
					return err
				}
			} else {
				if len(rep.Phases) == 0 || rep.Phases[len(rep.Phases)-1] != st.Phase || st.Phase != "inside" {
					if len(rep.Phases) == 0 || rep.Phases[len(rep.Phases)-1] != st.Phase {
						rep.Phases = append(rep.Phases, st.Phase)
						s.opts.logf("fb-dungeon: finder: %s (%s), %d/%d member(s), waited %ds",
							chat.Sanitise(st.Phase, 16), chat.Sanitise(st.Name, 80), st.Members, st.Size, st.WaitedSec)
					}
				}
				rep.Status = st
				if r, err := c.Roster(ctx); err != nil {
					if errors.Is(err, ErrLinkLost) {
						return err
					}
					if errors.Is(err, fbcmd.ErrPBCOff) || errors.Is(err, pbc.ErrProtoMismatch) || errors.Is(err, fbcmd.ErrBadAnswer) {
						lfgOff = chat.Sanitise(err.Error(), 300)
						rows = nil
						s.noticeFBStatus(rep, lfgOff)
						s.opts.logf("fb-dungeon: bot health unavailable (%s)", lfgOff)
					} else {
						return err
					}
				} else {
					rows = r
				}
			}
		}
		for _, raw := range c.Events() {
			// The module's strings are sanitised and bounded here, once, where the report keeps them: the log line
			// and the closing block then print the stored copy, and no other print path can carry a control byte.
			ev := fbBoundEvent(raw)
			if len(rep.Events) < maxFBEvents {
				rep.Events = append(rep.Events, ev)
			}
			s.opts.logf("fb-dungeon: event: %s %s: %s (bot %d)", ev.Severity, ev.Name, ev.Detail, ev.BotID)
		}
		rep.WatchSamples++
		for _, r := range rows {
			if !r.Grouped || !r.Online {
				continue
			}
			name := chat.SanitiseName(r.Name)
			if r.HPPercent <= 0 {
				if !dead[name] && len(rep.Deaths) < fbDungeonDeaths {
					dead[name] = true
					rep.Deaths = append(rep.Deaths, name)
					s.opts.logf("fb-dungeon: watch: %s is down (0%% hp)", name)
				}
			} else if dead[name] {
				delete(dead, name)
				s.opts.logf("fb-dungeon: watch: %s is up again (%d%% hp)", name, r.HPPercent)
			}
		}
		now = s.Now()
		if now.Sub(lastReport) >= fbDungeonReportEvery || !now.Before(end) {
			lastReport = now
			s.fbDungeonHealthLine(rep, rows, now.Sub(start))
		}
	}
	s.fbDungeonHealthLine(rep, rows, end.Sub(start))
	s.opts.logf("fb-dungeon: watched %s: %d sample(s), %d death(s), %d event(s), %d party line(s)",
		rep.Watch.Round(time.Second), rep.WatchSamples, len(rep.Deaths), len(rep.Events), len(rep.PartyLines))
	return nil
}

// fbDungeonPartyTap records the lead tank's party lines: the module's lead says "waiting for mana" and "waiting for
// you" in party chat, and those are the run's own report of why the tank is not moving. Only a line whose sender
// GUID is the resolved tank's is kept: another party member's talk (a second bot, a real player) is not the tank's,
// and a line that arrives before the identity is known is kept nowhere and named in the report instead.
func (s *session) fbDungeonPartyTap(rep *FBDungeonReport, tank *fbTank) func(chat.Line) {
	return func(line chat.Line) {
		if line.Type != chat.TypeParty {
			return
		}
		if tank.guid.IsZero() || line.SenderGUID != tank.guid {
			if tank.guid.IsZero() {
				s.fbTankUnidentified(rep, tank)
			}
			return
		}
		text := fmt.Sprintf("%s: %s", tank.name, chat.Sanitise(line.Text, 200))
		if len(rep.PartyLines) < fbDungeonPartyLines {
			rep.PartyLines = append(rep.PartyLines, text)
		}
		s.opts.logf("fb-dungeon: party: %s", text)
	}
}

// fbDungeonResolveTank turns the module's lead answer into the one speaker the run accepts as the tank. The name
// must be a party member's (that gives the GUID) AND a name the authenticated bot roster lists: the roster is this
// account's bots, answered by the module only to this character's own add-on frames, so a real player's name is not
// a tank identity even when the party holds it.
func (s *session) fbDungeonResolveTank(ctx context.Context, c *fbcmd.Client, rep *FBDungeonReport, tank *fbTank, name string) error {
	if name == "" {
		s.fbTankRefused(rep, tank, "the module's lead answer named no tank")
		return nil
	}
	guid, inParty := s.fbPartyMember(name)
	if !inParty {
		s.fbTankRefused(rep, tank, fmt.Sprintf("%q is not in the client's party", chat.SanitiseName(name)))
		return nil
	}
	if guid.IsZero() {
		s.fbTankRefused(rep, tank, fmt.Sprintf("the party lists no GUID for %q", chat.SanitiseName(name)))
		return nil
	}
	rows, err := c.Roster(ctx)
	if err != nil {
		if errors.Is(err, ErrLinkLost) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		s.fbTankRefused(rep, tank, "the bot roster could not be read: "+chat.Sanitise(err.Error(), 200))
		return nil
	}
	for _, r := range rows {
		if strings.EqualFold(strings.TrimSpace(r.Name), name) {
			tank.guid, tank.name, tank.why, tank.noted = guid, chat.SanitiseName(r.Name), "", false
			rep.LeadTankGUID = guid
			s.opts.logf("fb-dungeon: lead tank: %s (%s), in the party and the bot roster", tank.name, guid)
			return nil
		}
	}
	s.fbTankRefused(rep, tank, fmt.Sprintf("%q is not in the bot roster", chat.SanitiseName(name)))
	return nil
}

// fbTankUnidentified records, once, that a party line could not be taken as the tank's because no identity is known.
func (s *session) fbTankUnidentified(rep *FBDungeonReport, tank *fbTank) {
	if rep.LeadTankWhy != "" {
		return
	}
	why := tank.why
	if why == "" {
		why = "the module has not named its lead tank yet"
	}
	rep.LeadTankWhy = "lead tank not identified when a party line arrived: " + why
	if !tank.noted {
		tank.noted = true
		s.opts.logf("fb-dungeon: %s; the party lines are not taken as the tank's", rep.LeadTankWhy)
	}
}

// fbTankRefused records that the lead answer's tank has no accepted identity, replacing an earlier reason: the
// resolution is the last word on it.
func (s *session) fbTankRefused(rep *FBDungeonReport, tank *fbTank, why string) {
	tank.guid, tank.name = wire.GUID{}, ""
	tank.why, tank.noted = why, true
	rep.LeadTankGUID = wire.GUID{}
	rep.LeadTankWhy = "lead tank not identified: " + why
	s.opts.logf("fb-dungeon: %s; the party lines are not taken as the tank's", rep.LeadTankWhy)
}

// noticeFBStatus keeps the protocol's soft failure in the report.
func (s *session) noticeFBStatus(rep *FBDungeonReport, reason string) {
	rep.PBC = "unavailable"
	rep.PBCReason = reason
}

// fbBoundEvent sanitises and bounds the module's event strings where the report keeps them, so every print path (the
// watch's log line and the closing block) shows the same safe, bounded text.
func fbBoundEvent(ev pbc.Event) pbc.Event {
	ev.Severity = chat.Sanitise(ev.Severity, 16)
	ev.Name = chat.Sanitise(ev.Name, 32)
	ev.Detail = chat.Sanitise(ev.Detail, 200)
	return ev
}

// fbDungeonHealthLine prints one watch snapshot and keeps it in the report. The roster's strings are sanitised and
// bounded at the store, so the log line and the closing block print the same safe copy.
func (s *session) fbDungeonHealthLine(rep *FBDungeonReport, rows []pbc.RosterEntry, at time.Duration) {
	rep.Watch = at
	var parts []string
	rep.Health = rep.Health[:0]
	for _, r := range rows {
		if !r.Grouped || !r.Online {
			continue
		}
		h := FBDungeonHealth{
			Name: chat.SanitiseName(r.Name), HP: r.HPPercent, Mana: r.ManaPercent,
			Zone: chat.Sanitise(r.Zone, 32), LastRule: chat.Sanitise(r.LastRule, 48), Distance: r.Distance,
		}
		rep.Health = append(rep.Health, h)
		parts = append(parts, fmt.Sprintf("%s %d%%h %d%%m %s%s",
			h.Name, h.HP, h.Mana, h.Zone, fbRuleSuffix(h.LastRule)))
	}
	phase := "unknown"
	if rep.Status.Phase != "" {
		phase = rep.Status.Phase
	}
	if len(parts) == 0 {
		s.opts.logf("fb-dungeon: watch: t+%s finder %s; no bot health read",
			at.Round(time.Second), chat.Sanitise(phase, 16))
		return
	}
	s.opts.logf("fb-dungeon: watch: t+%s finder %s (%s) %d/%d; %s",
		at.Round(time.Second), chat.Sanitise(phase, 16), chat.Sanitise(rep.Status.Name, 80),
		rep.Status.Members, rep.Status.Size, strings.Join(parts, ", "))
}

// fbRuleSuffix shows a health sample's last rule; the rule was sanitised and bounded where it was stored.
func fbRuleSuffix(rule string) string {
	if rule == "" {
		return ""
	}
	return " [" + rule + "]"
}

// fbCleanupFatal reports whether an error means nothing else the cleanup sends can be answered: the link is dead, or
// a context ended. A refusal from the module is not fatal: the next step is still worth trying.
func fbCleanupFatal(err error) bool {
	return errors.Is(err, ErrLinkLost) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// fbDungeonCleanupRun undoes what the run started, from whatever exit reached it: the lead goes back to the player,
// the instance is left (or the queue cancelled), and every companion is dismissed. Each step's result is recorded in
// the report; a step whose command was sent but whose answer never came (a lost link, an expired context) is named
// in CleanupErr, so the closing block says what could not be cleaned instead of staying silent.
func (s *session) fbDungeonCleanupRun(ctx context.Context, c *fbcmd.Client, rep *FBDungeonReport) {
	var unconfirmed []string
	var fatal error
	failed := func(name string, err error) {
		rep.Cleanup = append(rep.Cleanup, name+": "+chat.Sanitise(err.Error(), 300))
		if fbCleanupFatal(err) {
			unconfirmed = append(unconfirmed, name)
			if fatal == nil {
				fatal = err
			}
		}
	}
	kept := func(name, line string) { rep.Cleanup = append(rep.Cleanup, name+": "+line) }

	// 1. Give the lead back, when the module took it.
	if rep.LeadOnErr == "" && rep.LeadOnLine != "" {
		if lr, err := c.Lead(ctx, "off"); err != nil {
			rep.LeadOffErr = chat.Sanitise(err.Error(), 300)
			s.opts.logf("fb-dungeon: cleanup: lead off: %v", err)
			failed("lead off", err)
		} else {
			rep.LeadOffLine = lr.Line
			s.opts.logf("fb-dungeon: cleanup: lead off: %s", lr.Line)
			kept("lead off", lr.Line)
		}
	}

	// 2. Leave the instance, or cancel a queue that never opened. A queue whose phase was never read still holds a
	// group on the finder, so a queue line without a phase cancels too.
	from := s.report.MapID
	switch {
	case s.opts.FBDungeonLeave && (rep.Inside || s.report.MapID != rep.StartMap):
		if line, err := c.LeaveDungeon(ctx); err != nil {
			rep.LeaveErr = chat.Sanitise(err.Error(), 300)
			s.opts.logf("fb-dungeon: cleanup: leave: %v", err)
			failed("leave", err)
		} else {
			rep.LeaveLine = line
			s.opts.logf("fb-dungeon: cleanup: leave: %s", line)
			kept("leave", line)
			s.fbWaitMapAway(ctx, rep, from)
		}
	case rep.Status.Phase == "forming" || rep.Status.Phase == "entering" ||
		(rep.Status.Phase == "" && rep.QueueLine != ""):
		if line, err := c.CancelDungeon(ctx); err != nil {
			rep.CancelErr = chat.Sanitise(err.Error(), 300)
			s.opts.logf("fb-dungeon: cleanup: cancel: %v", err)
			failed("cancel", err)
		} else {
			rep.CancelLine = line
			s.opts.logf("fb-dungeon: cleanup: cancel: %s", line)
			kept("cancel", line)
		}
	}

	// 3. Dismiss every companion and wait for the party to drop them.
	names := append([]string(nil), rep.Party...)
	if len(names) == 0 {
		names = s.fbPartyNames()
	}
	if res, err := c.Dismiss(ctx, "all"); err != nil {
		rep.DismissErr = chat.Sanitise(err.Error(), 300)
		s.opts.logf("fb-dungeon: cleanup: dismiss all: %v", err)
		failed("dismiss", err)
	} else {
		rep.Dismissed = append(rep.Dismissed, res.Line)
		s.opts.logf("fb-dungeon: cleanup: dismiss all: %s", res.Line)
		kept("dismiss", res.Line)
		for _, name := range names {
			if s.selfNameEqual(name) {
				continue
			}
			if err := s.fbWaitAbsent(ctx, name, fbDismissWait); err != nil {
				rep.DismissErr = chat.Sanitise(err.Error(), 300)
				s.opts.logf("fb-dungeon: cleanup: dismiss %s: %v", chat.SanitiseName(name), err)
				failed("dismiss "+chat.SanitiseName(name), err)
			}
		}
	}
	if s.party.RosterValid {
		s.opts.logf("fb-dungeon: party now holds %d member(s)", len(s.party.Members))
	}

	if len(unconfirmed) > 0 {
		rep.CleanupErr = fmt.Sprintf("could not confirm %s: %s", strings.Join(unconfirmed, ", "), chat.Sanitise(fatal.Error(), 300))
		s.opts.logf("fb-dungeon: cleanup incomplete: %s", rep.CleanupErr)
	}
	s.summariseFBDungeon()
}

// fbWaitMapAway waits, bounded, for the client's own map change out of the instance. The leave answer already moved
// the character server-side; this is the client-side confirmation, and its absence is reported, not invented.
func (s *session) fbWaitMapAway(ctx context.Context, rep *FBDungeonReport, from int32) {
	if s.report.MapID != from {
		rep.LeftMap, rep.LeftMapSet = s.report.MapID, true
		s.opts.logf("fb-dungeon: the client's map change left the instance: map %d", rep.LeftMap)
		return
	}
	deadline := s.Now().Add(s.fbDungeonLeaveWait())
	for s.Now().Before(deadline) {
		if s.report.MapID != from {
			rep.LeftMap, rep.LeftMapSet = s.report.MapID, true
			s.opts.logf("fb-dungeon: the client's map change left the instance: map %d", rep.LeftMap)
			return
		}
		remaining := deadline.Sub(s.Now())
		if remaining > s.fbDungeonPoll() {
			remaining = s.fbDungeonPoll()
		}
		if err := s.collect(ctx, remaining); err != nil {
			rep.LeaveMapErr = "the link ended while waiting for the way out"
			return
		}
	}
	rep.LeaveMapErr = fmt.Sprintf("no map change out of map %d within %s", from, s.fbDungeonLeaveWait())
	s.opts.logf("fb-dungeon: %s", rep.LeaveMapErr)
}

// selfNameEqual reports whether name is this character's own name (the party list includes it).
func (s *session) selfNameEqual(name string) bool {
	for _, m := range s.party.Members {
		if m.GUID == s.self && strings.EqualFold(m.Name, name) {
			return true
		}
	}
	return false
}

// summariseFBDungeon fills the protocol state and prints the run's own closing lines.
func (s *session) summariseFBDungeon() {
	rep := &s.report.FBDungeon
	rep.Frames = s.fbFrames
	rep.Forged = s.pbcForged
	rep.EventsLost = s.fbEventsDropped
	if s.pbcOff != "" {
		rep.PBC = "unavailable"
		if rep.PBCReason == "" {
			rep.PBCReason = s.pbcOff
		}
	} else if rep.PBC == "" && rep.Inside {
		rep.PBC = "ok"
	}
	if s.pbcOff != "" {
		s.opts.logf("fb-dungeon: pbc: unavailable (%s)", s.pbcOff)
	} else if rep.PBC == "ok" {
		s.opts.logf("fb-dungeon: pbc: ok")
	}
}

// FBDungeonSummary is the command's closing block; the CLI prints it so every run words the result the same way.
func FBDungeonSummary(a FBDungeonReport) []string {
	var out []string
	if a.LfgLine != "" {
		out = append(out, "fb-dungeon: lfg: "+a.LfgLine)
	} else if a.LfgErr != "" {
		out = append(out, "fb-dungeon: lfg: "+a.LfgErr)
	}
	if a.PartySize > 0 {
		out = append(out, fmt.Sprintf("fb-dungeon: party of %d: %s", a.PartySize, strings.Join(a.Party, ", ")))
	}
	if a.QueueLine != "" {
		out = append(out, "fb-dungeon: queue: "+a.QueueLine)
	} else if a.QueueErr != "" {
		out = append(out, "fb-dungeon: queue: "+a.QueueErr)
	}
	if a.Inside {
		out = append(out, fmt.Sprintf("fb-dungeon: inside confirmed: module inside (%s), client map %d from %d",
			strings.Join(a.Phases, " -> "), a.InsideMap, a.StartMap))
	} else if a.EnterErr != "" {
		out = append(out, "fb-dungeon: not confirmed inside: "+a.EnterErr)
	}
	if a.LeadOnLine != "" {
		out = append(out, "fb-dungeon: lead on: "+a.LeadOnLine)
	} else if a.LeadOnErr != "" {
		out = append(out, "fb-dungeon: lead on: "+a.LeadOnErr)
	}
	if a.LeadTankWhy != "" {
		out = append(out, "fb-dungeon: "+a.LeadTankWhy)
	}
	if a.FollowNote != "" {
		out = append(out, "fb-dungeon: "+a.FollowNote)
	}
	if a.WatchSamples > 0 {
		out = append(out, fmt.Sprintf("fb-dungeon: watched %s: %d sample(s), %d death(s), %d event(s), %d party line(s)",
			a.Watch.Round(time.Second), a.WatchSamples, len(a.Deaths), len(a.Events), len(a.PartyLines)))
	}
	if len(a.Deaths) > 0 {
		out = append(out, "fb-dungeon: down: "+strings.Join(a.Deaths, ", "))
	}
	if len(a.Health) > 0 {
		var parts []string
		for _, h := range a.Health {
			parts = append(parts, fmt.Sprintf("%s %d%%h %d%%m", h.Name, h.HP, h.Mana))
		}
		out = append(out, "fb-dungeon: last health: "+strings.Join(parts, ", "))
	}
	for _, ev := range a.Events {
		out = append(out, fmt.Sprintf("fb-dungeon: event %s %s: %s", ev.Severity, ev.Name, ev.Detail))
	}
	if a.LeadOffLine != "" {
		out = append(out, "fb-dungeon: lead off: "+a.LeadOffLine)
	} else if a.LeadOffErr != "" {
		out = append(out, "fb-dungeon: lead off: "+a.LeadOffErr)
	}
	if a.LeaveLine != "" {
		out = append(out, "fb-dungeon: leave: "+a.LeaveLine)
	} else if a.LeaveErr != "" {
		out = append(out, "fb-dungeon: leave: "+a.LeaveErr)
	}
	if a.CancelLine != "" {
		out = append(out, "fb-dungeon: cancel: "+a.CancelLine)
	} else if a.CancelErr != "" {
		out = append(out, "fb-dungeon: cancel: "+a.CancelErr)
	}
	if a.LeaveMapErr != "" {
		out = append(out, "fb-dungeon: "+a.LeaveMapErr)
	} else if a.LeftMapSet {
		out = append(out, fmt.Sprintf("fb-dungeon: back on map %d", a.LeftMap))
	}
	if len(a.Dismissed) > 0 {
		out = append(out, "fb-dungeon: "+a.Dismissed[0])
	} else if a.DismissErr != "" {
		out = append(out, "fb-dungeon: dismiss: "+a.DismissErr)
	}
	if a.CleanupErr != "" {
		out = append(out, "fb-dungeon: cleanup incomplete: "+a.CleanupErr)
	}
	switch a.PBC {
	case "ok":
		out = append(out, "fb-dungeon: pbc: ok")
	case "unavailable":
		out = append(out, "fb-dungeon: pbc: unavailable ("+a.PBCReason+")")
	}
	if a.Frames > 0 {
		out = append(out, fmt.Sprintf("fb-dungeon: %d protocol frame(s) received", a.Frames))
	}
	if a.Forged > 0 {
		out = append(out, fmt.Sprintf("fb-dungeon: %d protocol frame(s) ignored (not this character's whisper)", a.Forged))
	}
	if a.EventsLost > 0 {
		out = append(out, fmt.Sprintf("fb-dungeon: %d event(s) dropped past the run's bound", a.EventsLost))
	}
	return out
}
