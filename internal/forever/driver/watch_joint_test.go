package driver

// The joint fixes: a watched party led by its tank and commanded by the module's own bot, an observer that stays
// outside, and an end that always says why.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// TestWatchObserverOutsideTheInstance: the party stands in the instance and this client's character is still on the
// outer map. PREPARE compares the party's map with the plan's, not with the character's, so the watch starts; the old
// check ended it with prepare:map.
func TestWatchObserverOutsideTheInstance(t *testing.T) {
	cfg := watchConfig()
	cfg.Enter = "none"
	run, f, logs := newWatch(t, cfg)
	f.selfMap = 1 // the outer map of a portal; the party stays on the plan's map 389
	f.selfPos = Vec3{10, 10, 10}
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" || !res.Clear {
		t.Fatalf("stop %q clear %v, want done; logs:\n%s", res.Stop, res.Clear, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 1 {
		t.Fatalf("%d run starts, want 1", n)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "watch: end reason=done") {
		t.Fatalf("no end line:\n%s", strings.Join(*logs, "\n"))
	}
}

// TestWatchBotCommanderRoster: the party's leader is its tank, and the module's run names that tank, while the
// commander is the module's own bot outside the party. The roster is adopted; a party led by anyone but the tank or
// the run's own leader is refused.
func TestWatchBotCommanderRoster(t *testing.T) {
	cfg := watchConfig()
	run, f, logs := newWatch(t, cfg)
	f.BotCommander = "Botcmd"
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if len(f.Acted()) == 0 {
		t.Fatal("nothing was sent")
	}
	// The roster's leader is the tank the run names; the commander is a bot, and the observer is not the commander.
	p, err := f.Party(context.Background())
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	if p.Commander != "Botcmd" || p.Leader != "Tank" {
		t.Fatalf("roster %+v", p)
	}

	// The tank is the run's own leader while another member carries the party's leader flag: accepted.
	run, f, logs = newWatch(t, cfg)
	f.RosterLeader = "Watchheal"
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	if res := run.Watch(context.Background()); res.Stop != "done" {
		t.Fatalf("run leader's roster: stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}

	// A party whose leader is neither its tank nor the run's leader is refused before anything is sent.
	run, f, logs = newWatch(t, cfg)
	f.RosterLeader = "Watchheal"
	f.bots["Tank"].role, f.bots["Watchdps1"].role = "dps", "tank"
	if res := run.Watch(context.Background()); res.Stop != "prepare:leader" {
		t.Fatalf("foreign leader: stop %q, want prepare:leader; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run starts after a refused roster", n)
	}
}

// TestWatchRejectsTheObserverAsCommander: the observer is never the party's commander. A roster that names it is
// refused at PREPARE, before an entry or a run start.
func TestWatchRejectsTheObserverAsCommander(t *testing.T) {
	cfg := watchConfig()
	run, f, logs := newWatch(t, cfg)
	f.BotCommander = cfg.Commander
	if res := run.Watch(context.Background()); res.Stop != "prepare:observer-commander" {
		t.Fatalf("stop %q, want prepare:observer-commander; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run starts", n)
	}
}

// TestWatchEngineOffIsNotReady: a member whose engine is off is not ready with the reason noengine, logged, and
// the watch ends on that reason, never blind: the state record itself was read whole.
func TestWatchEngineOffIsNotReady(t *testing.T) {
	cfg := watchConfig()
	run, f, logs := newWatch(t, cfg)
	f.bots["Watchheal"].engine = false
	// The parse fix is what keeps the record whole; the fake already hands the driver that typed report.
	if res := run.Watch(context.Background()); res.Stop != "prepare:noengine" {
		t.Fatalf("stop %q, want prepare:noengine; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "prepare: not ready: noengine:Watchheal") {
		t.Fatalf("no not-ready line for the engine-off member:\n%s", joined)
	}
	if strings.Contains(joined, "blind") {
		t.Fatalf("the watch went blind on an engine-off member:\n%s", joined)
	}
	if !strings.Contains(joined, "watch: end reason=prepare:noengine") {
		t.Fatalf("no end reason:\n%s", joined)
	}
}

// TestWatchBlindNamesItsReason: a state read that keeps failing ends the watch blind, and the end names the read's
// error: a blind end without a reason must not happen.
func TestWatchBlindNamesItsReason(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.Blind = 10 * time.Second
	cfg.Limits.Prepare = 60 * time.Second
	run, f, logs := newWatch(t, cfg)
	f.FailState = errors.New("the module answered nothing")
	res := run.Watch(context.Background())
	if res.Stop != "blind" {
		t.Fatalf("stop %q, want blind; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "the last was: the module answered nothing") {
		t.Fatalf("the blind end does not name its read's error:\n%s", joined)
	}
	if !strings.Contains(joined, "watch: end reason=blind") {
		t.Fatalf("no end reason:\n%s", joined)
	}
}

// TestWatchConfigRefusalIsLogged: even the refusal before anything is sent says why in the watch's log.
func TestWatchConfigRefusalIsLogged(t *testing.T) {
	cfg := watchConfig()
	cfg.Leader = ""
	run, _, logs := newWatch(t, cfg)
	if res := run.Watch(context.Background()); res.Stop != "config" {
		t.Fatalf("stop %q, want config", res.Stop)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "watch: end reason=config") {
		t.Fatalf("no end reason:\n%s", strings.Join(*logs, "\n"))
	}
}

// TestWatchZeroInstanceRefused: the party stands on the plan's map with instance id 0. That is the open world, not
// the instance the plan names, so the watch refuses the placement before it starts the module's run.
func TestWatchZeroInstanceRefused(t *testing.T) {
	run, f, logs := newWatch(t, watchConfig())
	for _, b := range f.bots {
		b.inst = 0
	}
	res := run.Watch(context.Background())
	if res.Stop != "enter:party-map" {
		t.Fatalf("stop %q, want enter:party-map; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run starts on a party in instance 0", n)
	}
}

// TestPartyInInstanceNeedsAnInstance: five bots sharing a map are not in while their instance id is 0, and a report
// older than the gate's freshness rule is not evidence of the entry either.
func TestPartyInInstanceNeedsAnInstance(t *testing.T) {
	run, f, _ := newWatch(t, watchConfig())
	for _, b := range f.bots {
		b.mapID, b.inst = run.route.Map, 0
	}
	r := f.report(5)
	run.rep, run.repAt = &r, run.now()
	if run.partyInInstance() {
		t.Fatal("five bots on the plan's map with inst=0 read as in the instance")
	}
	for _, b := range f.bots {
		b.inst = 1
	}
	r = f.report(5)
	run.rep, run.repAt = &r, run.now()
	if !run.partyInInstance() {
		t.Fatal("five bots on the plan's map in one instance read as out")
	}
	// The same report, read before the entry: stale, so the party is unknown.
	run.repAt = run.now().Add(-stateFreshFor - time.Second)
	if run.partyInInstance() {
		t.Fatal("a stale report read as the party in the instance")
	}
}

// TestWatchStartNeedsOneRunRecord: a start answer with no run record is not a started run. The watch refuses it
// instead of logging the module's run as started and polling a run that does not exist.
func TestWatchStartNeedsOneRunRecord(t *testing.T) {
	run, f, logs := newWatch(t, watchConfig())
	f.NoRunRecord = true
	res := run.Watch(context.Background())
	if res.Stop != "start:bad-report" {
		t.Fatalf("stop %q, want start:bad-report; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	joined := strings.Join(*logs, "\n")
	if strings.Contains(joined, "watch: started") {
		t.Fatalf("a zero-record start read as started:\n%s", joined)
	}
	if !strings.Contains(joined, "watch: end reason=start:bad-report") {
		t.Fatalf("no end reason:\n%s", joined)
	}
}

// TestStartRecord: the one record a start answer carries must name the leader and plan the command named, and be
// running; zero or several records are a malformed report.
func TestStartRecord(t *testing.T) {
	st := tbcmd.RunState{Lead: "Tank", Plan: "ragefire_chasm", State: "running"}
	if _, err := startRecord(ActionReport{Verb: "run", Confirmed: true, Count: 1, Run: &st}, "Tank", "ragefire_chasm"); err != nil {
		t.Fatalf("the whole answer: %v", err)
	}
	tests := map[string]ActionReport{
		"zero":       {Verb: "run", Confirmed: true},
		"several":    {Verb: "run", Confirmed: true, Count: 2, Run: &st},
		"other lead": {Verb: "run", Confirmed: true, Count: 1, Run: &tbcmd.RunState{Lead: "Moff", Plan: "ragefire_chasm", State: "running"}},
		"other plan": {Verb: "run", Confirmed: true, Count: 1, Run: &tbcmd.RunState{Lead: "Tank", Plan: "other", State: "running"}},
		"paused":     {Verb: "run", Confirmed: true, Count: 1, Run: &tbcmd.RunState{Lead: "Tank", Plan: "ragefire_chasm", State: "paused"}},
	}
	for name, rep := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := startRecord(rep, "Tank", "ragefire_chasm"); !errors.Is(err, tbcmd.ErrBadReport) {
				t.Fatalf("err %v, want ErrBadReport", err)
			}
		})
	}
}

// TestWatchReadyUsesTheLeaderDistance: a watched party's master is the module's own commander, outside the party and
// possibly outside its instance, so its dm= can be othermap while the party stands together around its tank. PREPARE
// measures a watched party by dl= (the party leader); a member 30 yards from that leader is still not ready.
func TestWatchReadyUsesTheLeaderDistance(t *testing.T) {
	run, f, logs := newWatch(t, watchConfig())
	for _, b := range f.bots {
		b.dmKind = "othermap"
	}
	// The module never lets the commander in: dm=othermap stands, and PREPARE must still measure the party by dl=.
	f.RefuseActions = map[ActionKind]string{ActionTeleTo: "refused"}
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	if res := run.Watch(context.Background()); res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 1 {
		t.Fatalf("%d run starts", n)
	}

	run, f, _ = newWatch(t, watchConfig())
	f.bots["Watchdps1"].dlKind, f.bots["Watchdps1"].dlYards = "away", 30
	if res := run.Watch(context.Background()); res.Stop != "prepare:range" {
		t.Fatalf("a member 30 yd from the tank: stop %q, want prepare:range", res.Stop)
	}
}

// TestPartyInInstanceNeedsAWholeReport: a state read that failed is not evidence of the placement, even when its
// answer still carries five bot lines; a report whose counts disagree is not whole either.
func TestPartyInInstanceNeedsAWholeReport(t *testing.T) {
	run, f, _ := newWatch(t, watchConfig())
	for _, b := range f.bots {
		b.mapID, b.inst = run.route.Map, 1
	}
	r := f.report(5)
	run.rep, run.repAt, run.repErr = &r, run.now(), errors.New("state: the answer was cut")
	if run.partyInInstance() {
		t.Fatal("a failed read read as the party in the instance")
	}
	run.repErr = nil
	if !run.partyInInstance() {
		t.Fatal("a whole report read as the party out")
	}
	r.Records--
	run.rep = &r
	if run.partyInInstance() {
		t.Fatal("a report whose counts disagree read as the party in the instance")
	}
}

// TestWatchObserverPollFailureKeepsTheEntry: the party is in; the observer's own teleport polls the state and the two
// reads fail with no report at all. That failure is not a placement: the watch reads again and starts, instead of
// ending enter:party-map on a party its own entry verified.
func TestWatchObserverPollFailureKeepsTheEntry(t *testing.T) {
	cfg := watchConfig()
	cfg.Enter = "tele"
	run, f, logs := newWatch(t, cfg)
	seenIn, failed := false, 0
	f.EmptyStateOn = func(f *Fake) bool {
		if f.bots[cfg.Tank()].mapID != run.route.Map {
			return false
		}
		if seenIn && failed < 2 {
			failed++
			return true
		}
		seenIn = true
		return false
	}
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	if res := run.Watch(context.Background()); res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 1 {
		t.Fatalf("%d run starts", n)
	}
}

// TestWatchStartRechecksThePlacement: the start's gate fails once and the start is retried; while it waits the party
// zones out together. The party's own map is what ready() compares in a watch, so without its own placement check the
// gate would pass and ask the module to start a run off the plan's map.
func TestWatchStartRechecksThePlacement(t *testing.T) {
	run, f, logs := newWatch(t, watchConfig())
	run.cfg.Poll = 2 * time.Second
	reads := 0
	f.EmptyStateOn = func(f *Fake) bool {
		if f.bots[run.cfg.Tank()].mapID != run.route.Map {
			return false
		}
		reads++
		return reads == 3 // the commander's placement read, PREPARE's, then the start gate's
	}
	f.Clock.After(time.Second, func() {
		for _, b := range f.bots {
			b.mapID, b.inst = 1, 0
		}
	})
	res := run.Watch(context.Background())
	if res.Stop != "start:party-map" {
		t.Fatalf("stop %q, want start:party-map; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run starts on a party that zoned out during the retry", n)
	}
}

// TestWatchLeaderMustBeTheTank: the start names the watch's leader, and the module refuses a run leader that does not
// lead its party. A --leader that is a healer is refused at PREPARE, before anything is sent.
func TestWatchLeaderMustBeTheTank(t *testing.T) {
	run, f, logs := newWatch(t, watchConfig())
	run.cfg.Leader = "Watchheal"
	res := run.Watch(context.Background())
	if res.Stop != "prepare:leader" {
		t.Fatalf("stop %q, want prepare:leader; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run starts with a leader that does not lead the party", n)
	}
}

// TestWatchLeaderMayBeThePartyLeader: --leader may name the member that holds the party's leader flag even when it
// is not the tank. PREPARE accepts the roster and the run starts on that member, not on the tank.
func TestWatchLeaderMayBeThePartyLeader(t *testing.T) {
	cfg := watchConfig()
	cfg.Leader = "Watchheal"
	cfg.Party = []string{"Tank", "Watchheal", "Watchdps1", "Watchdps2", "Watchdps3"}
	run, f, logs := newWatch(t, cfg)
	f.RosterLeader = "Watchheal"
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start Watchheal"); n != 1 {
		t.Fatalf("%d starts on the party's leader, want 1; logs:\n%s", n, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start Tank"); n != 0 {
		t.Fatalf("%d starts on the tank, want 0", n)
	}
}

// TestWatchLeaderFlagsMustMatchTheSummary: a roster whose summary leader and member leader flags disagree is not a
// roster to trust. PREPARE refuses it — one flag only, and the flagged member is the one the summary names — before
// an entry or a run start.
func TestWatchLeaderFlagsMustMatchTheSummary(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*tbcmd.PartyReport)
	}{
		{"a second flag", func(p *tbcmd.PartyReport) { p.Members[0].Leader = true }},
		{"no flag", func(p *tbcmd.PartyReport) {
			for i := range p.Members {
				p.Members[i].Leader = false
			}
		}},
		{"another member's flag", func(p *tbcmd.PartyReport) {
			p.Members[0].Leader, p.Members[1].Leader = true, false
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := watchConfig()
			cfg.Leader = "Watchheal"
			cfg.Party = []string{"Tank", "Watchheal", "Watchdps1", "Watchdps2", "Watchdps3"}
			run, f, logs := newWatch(t, cfg)
			f.RosterLeader = "Watchheal"
			f.OnParty = func(p tbcmd.PartyReport) tbcmd.PartyReport {
				tc.mutate(&p)
				return p
			}
			res := run.Watch(context.Background())
			if res.Stop != "prepare:leader" {
				t.Fatalf("stop %q, want prepare:leader; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
			}
			if n := countActions(f, "enter party"); n != 0 {
				t.Fatalf("%d entries after a contradictory roster", n)
			}
			if n := countActions(f, "run start"); n != 0 {
				t.Fatalf("%d run starts after a contradictory roster", n)
			}
		})
	}
}

// TestReadyNoEngineReason: the gate's own reason word for an engine-off member is noengine, and it is distinct from
// a report that could not be read.
func TestReadyNoEngineReason(t *testing.T) {
	cfg := watchConfig()
	run, f, _ := newWatch(t, cfg)
	if ok, why := run.ready(); ok || why != "report" {
		t.Fatalf("before a report: ok=%v why=%q", ok, why)
	}
	r := f.report(5)
	run.rep = &r
	run.repAt = run.now()
	if ok, why := run.ready(); !ok || why != "" {
		t.Fatalf("a whole report: ok=%v why=%q", ok, why)
	}
	f.bots["Watchdps1"].engine = false
	r = f.report(5)
	run.rep = &r
	if ok, why := run.ready(); ok || why != "noengine:Watchdps1" {
		t.Fatalf("engine off: ok=%v why=%q", ok, why)
	}
}

// The third joint fixes: the commander brought to the party (before PREPARE, and after a tele entry lands it), and a
// start gate that waits out the party's own teleport instead of reading a member in flight as an engine-off refusal.

// actionIndex is the first position of the action whose text starts with prefix, -1 when it was never sent.
func actionIndex(f *Fake, prefix string) int {
	for i, a := range f.Acted() {
		if strings.HasPrefix(a.String(), prefix) {
			return i
		}
	}
	return -1
}

// TestWatchCommanderComesToTheTankBeforePrepare: --enter none with the party placed and the module's own commander
// (the roster's, a bot outside the party) still on the outer map. The module reports the tank
// `stuck=blocked:bot in an instance` with `dm=othermap`, and PREPARE would end on stuck:<tank>. The watch teleports
// the commander to the tank's side before PREPARE, says where it stood and that it arrived, and the block is gone.
func TestWatchCommanderComesToTheTankBeforePrepare(t *testing.T) {
	cfg := watchConfig()
	run, f, logs := newWatch(t, cfg)
	// The live record: the tank stands in the instance, its master on the outer map, and the module blocks the bot
	// for it; the module's teleport lands the commander at the tank's side and the block goes with it.
	f.bots["Tank"].dmKind = "othermap"
	f.bots["Tank"].stuck = "bot in an instance"
	f.OnAction = func(f *Fake, a Action) bool {
		if a.Kind != ActionTeleTo || a.Who != f.BotCommander {
			return false
		}
		for _, b := range f.bots {
			if b.dmKind == "othermap" {
				b.dmKind, b.dmYards = "away", 3
			}
			if b.stuck == "bot in an instance" {
				b.stuck = ""
			}
		}
		return true
	}
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "tele Botcmd to Tank"); n != 1 {
		t.Fatalf("%d commander teleports, want 1; logs:\n%s", n, strings.Join(*logs, "\n"))
	}
	tele, gm := actionIndex(f, "tele Botcmd to Tank"), actionIndex(f, "gm on")
	if tele < 0 || gm < 0 || tele > gm {
		t.Fatalf("the commander's teleport (action %d) does not precede PREPARE's GM line (action %d)", tele, gm)
	}
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "the commander is on another map or instance than the tank; the watch moves it to the tank") {
		t.Fatalf("no line saying where the commander stood:\n%s", joined)
	}
	if !strings.Contains(joined, "watch: the commander is at the tank's side") {
		t.Fatalf("no line saying the commander arrived:\n%s", joined)
	}
}

// TestWatchCommanderFollowsATeleEntry: --enter tele lifts the party into the instance and leaves the module's
// commander on the outer map. The module blocks the tank while its master stands outside, and moves the commander in
// its own time. The watch moves the commander to the tank after the party lands and waits for the module's own report
// to see it with the party, so the start's readiness rule never reads the block as stuck and ends start:stuck.
func TestWatchCommanderFollowsATeleEntry(t *testing.T) {
	cfg := watchConfig()
	cfg.Enter = "tele"
	run, f, logs := newWatch(t, cfg)
	// The module's teleport of the commander is accepted and lands a beat later; the block on the tank and its dm=
	// are derived from the commander's own place, as the module writes them.
	inside := false
	f.OnAction = func(f *Fake, a Action) bool {
		if a.Kind != ActionTeleTo || a.Who != f.BotCommander {
			return false
		}
		f.Clock.After(3*time.Second, func() { inside = true })
		return true
	}
	f.OnState = func(f *Fake, _ int) error {
		tank := f.bots[cfg.Tank()]
		if tank == nil || tank.mapID != f.route.Map {
			return nil
		}
		// The party is in: the tank's master is away until the commander's teleport lands.
		if inside {
			tank.dmKind, tank.dmYards = "away", 3
			tank.stuck = ""
		} else {
			tank.dmKind = "othermap"
			tank.stuck = "bot in an instance"
		}
		return nil
	}
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "tele Botcmd to Tank"); n != 1 {
		t.Fatalf("%d commander teleports, want 1; logs:\n%s", n, strings.Join(*logs, "\n"))
	}
	enter, tele := actionIndex(f, "enter party"), actionIndex(f, "tele Botcmd to Tank")
	if enter < 0 || tele < 0 || tele < enter {
		t.Fatalf("the commander's teleport (action %d) does not follow the party's entry (action %d)", tele, enter)
	}
	if n := countActions(f, "run start"); n != 1 {
		t.Fatalf("%d run starts, want 1; logs:\n%s", n, strings.Join(*logs, "\n"))
	}
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "the commander is on another map or instance than the tank; the watch moves it to the tank") {
		t.Fatalf("no line saying where the commander stood:\n%s", joined)
	}
	if !strings.Contains(joined, "watch: the commander is at the tank's side") {
		t.Fatalf("no line saying the commander arrived:\n%s", joined)
	}
}

// TestWatchStartWaitsForThePartysTeleport: the start gate's own read lands mid-teleport — a member is still on the
// outer map with its engine off, the live line `engine=no ... map=1` — and the party lands a beat later. The gate
// waits for every member on the plan's map instead of ending start:party-map on a member in flight.
func TestWatchStartWaitsForThePartysTeleport(t *testing.T) {
	cfg := watchConfig() // --enter none: the party was placed, one member still in the placement's teleport
	run, f, logs := newWatch(t, cfg)
	reads := 0
	f.OnState = func(f *Fake, _ int) error {
		if f.bots[cfg.Tank()].mapID != f.route.Map {
			return nil
		}
		reads++
		// Reads 1 and 2 are the commander's placement and PREPARE; the third is the start's gate.
		if reads == 3 {
			f.bots["Watchheal"].mapID, f.bots["Watchheal"].engine = 1, false
			f.Clock.After(time.Second, func() {
				f.bots["Watchheal"].mapID, f.bots["Watchheal"].engine = f.route.Map, true
			})
		}
		return nil
	}
	f.ScriptRun(FakeRunScript{Steps: 8, Pulls: 2, ProgressEvery: 3 * time.Second, DoneAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 1 {
		t.Fatalf("%d run starts, want 1; logs:\n%s", n, strings.Join(*logs, "\n"))
	}
}

// TestWatchStartTeleportThatNeverLands: the member never lands. The gate's wait is bounded, and at the bound the
// existing error (start:party-map) is the one reported, with no run started on a party that is not all on the plan's
// map.
func TestWatchStartTeleportThatNeverLands(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.Enter = 10 * time.Second
	run, f, logs := newWatch(t, cfg)
	reads := 0
	f.OnState = func(f *Fake, _ int) error {
		if f.bots[cfg.Tank()].mapID != f.route.Map {
			return nil
		}
		reads++
		if reads >= 3 {
			f.bots["Watchheal"].mapID, f.bots["Watchheal"].engine = 1, false
		}
		return nil
	}
	res := run.Watch(context.Background())
	if res.Stop != "start:party-map" {
		t.Fatalf("stop %q, want start:party-map; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run starts on a party that never landed", n)
	}
}

// TestCommanderAway: the module's own dm= is the evidence that the party's commander stands elsewhere: another map or
// instance, or a distance past a master's follow distance; the boundary itself is still with the tank.
func TestCommanderAway(t *testing.T) {
	cases := []struct {
		name string
		dist tbcmd.Distance
		want string
	}{
		{"with the tank", tbcmd.Distance{Has: true, Kind: "away", Yards: 3}, ""},
		{"other map or instance", tbcmd.Distance{Has: true, Kind: "othermap", Yards: 0}, "on another map or instance than the tank"},
		{"at the follow distance", tbcmd.Distance{Has: true, Kind: "away", Yards: MasterFollowYd}, ""},
		{"past it", tbcmd.Distance{Has: true, Kind: "away", Yards: MasterFollowYd + 1}, "21 yd from the tank"},
		{"no distance", tbcmd.Distance{}, ""},
		{"the bot is its own master", tbcmd.Distance{Has: true, Kind: "self"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commanderAway(&tbcmd.BotState{DistMaster: tc.dist}); got != tc.want {
				t.Fatalf("commanderAway = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWatchCommanderMoveRefusedKeepsTheExistingError: the module refuses the commander's own teleport, and the core's
// appear would have been accepted. The placement never falls back to it — appear moves this client's character, not
// the party's commander — so no appear is sent, the observer stands where it stood, the refusal is logged once, and
// the block the commander's absence causes still ends PREPARE on its own error rather than waiting out the watch.
func TestWatchCommanderMoveRefusedKeepsTheExistingError(t *testing.T) {
	cfg := watchConfig()
	run, f, logs := newWatch(t, cfg)
	// The observer stands on the outer map: an appear would read as a move at once, and it must never be sent.
	f.selfMap = 1
	f.selfPos = Vec3{10, 10, 10}
	f.bots["Tank"].dmKind = "othermap"
	f.bots["Tank"].stuck = "bot in an instance"
	f.RefuseActions = map[ActionKind]string{ActionTeleTo: "refused"}
	pos, mapID, _, _ := f.Self()
	res := run.Watch(context.Background())
	if res.Stop != "prepare:stuck" {
		t.Fatalf("stop %q, want prepare:stuck; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	joined := strings.Join(*logs, "\n")
	if n := countActions(f, "appear"); n != 0 {
		t.Fatalf("%d appear actions on a refused commander teleport; the observer is never moved in its place:\n%s", n, joined)
	}
	if got, gotMap, _, _ := f.Self(); got != pos || gotMap != mapID {
		t.Fatalf("the observer moved from %v map %d to %v map %d", pos, mapID, got, gotMap)
	}
	if !strings.Contains(joined, "watch: the commander could not be moved (refused); the watch goes on without it") {
		t.Fatalf("no line saying the commander could not be moved:\n%s", joined)
	}
	if n := countActions(f, "run start"); n != 0 {
		t.Fatalf("%d run starts on a blocked party", n)
	}
}

// TestWatchCommanderUnnamedFailsTheWatch: the module's roster names no commander. The placement has nothing to move
// and never moves this client's character in its place, so the watch ends on its own clear reason before PREPARE's GM
// lines, with nothing sent and the observer where it stood.
func TestWatchCommanderUnnamedFailsTheWatch(t *testing.T) {
	cfg := watchConfig()
	run, f, logs := newWatch(t, cfg)
	f.OnParty = func(rep tbcmd.PartyReport) tbcmd.PartyReport {
		rep.Commander = ""
		return rep
	}
	pos, mapID, _, _ := f.Self()
	res := run.Watch(context.Background())
	if res.Stop != "commander:unnamed" {
		t.Fatalf("stop %q, want commander:unnamed; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "watch: the roster names no commander; the watch does not move its own character in its place") {
		t.Fatalf("no line saying the roster named no commander:\n%s", joined)
	}
	// Nothing is sent in the commander's place, and nothing of PREPARE's either: the end's own instance read is the
	// only line.
	for _, a := range f.Acted() {
		if a.Kind != ActionInstanceInfo {
			t.Fatalf("action %q sent on an unnamed commander; logs:\n%s", a.String(), joined)
		}
	}
	if got, gotMap, _, _ := f.Self(); got != pos || gotMap != mapID {
		t.Fatalf("the observer moved from %v map %d to %v map %d", pos, mapID, got, gotMap)
	}
}
