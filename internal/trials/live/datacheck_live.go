package live

// datacheck_live.go: the live DataProbe of `trials forever-data-check`.
//
// The data check follows the A4 safety rules of every trial: it runs inside a
// quiet arena (the arena lock, the quiet probe and the stranger check before
// every spawn and while a spawn is read), every spawned unit is ledgered in
// the run dir's ledger.jsonl (where `trials cleanup` finds it) before
// anything touches it, and the teardown runs on every exit on a context of
// its own. A unit is claimed only when it is provably this probe's own: it
// was absent from a snapshot taken just before the spawn, it is the only new
// unit of the entry and it stands where the add spawns it (at the tester).
// The serve's `gm` answer carries no text, so the spawn reply cannot name the
// guid; anything not provable is left alone and reported, never killed.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

const (
	// spawnScanYards is how far the before/after snapshots of a spawn look.
	spawnScanYards = 60.0
	// spawnClaimYards is where an add spawns the unit: at the tester.
	spawnClaimYards = 5.0
)

var (
	spawnWait    = 5 * time.Second
	killProofFor = 3 * time.Second
	killPollGap  = 300 * time.Millisecond
)

// LiveDataProbe answers the data check against a live serve.
type LiveDataProbe struct {
	Client  *Client
	TB      *tbcmd.Client
	Guard   *Guard
	Ledger  *Ledger
	Scratch string

	// Dir is the serve dir written into every summon row, so `trials cleanup`
	// can reach the serve of this run.
	Dir string
	// Stranger reports a real player within reach of the arena (nil: none).
	Stranger func(ctx context.Context) (string, bool)
	// NoSpawn, when set, is why no mob is spawned (no quiet arena).
	NoSpawn string
	Log     func(string)

	// Unidentified counts spawns that went out but whose unit could not be
	// told apart from another (nothing is killed for them).
	Unidentified int

	spawns  map[int]string
	pending map[string]bool
}

// NewLiveDataProbe returns the probe over an online scratch bot.
func NewLiveDataProbe(client *Client, tb *tbcmd.Client, guard *Guard, ledger *Ledger, scratch string) *LiveDataProbe {
	return &LiveDataProbe{Client: client, TB: tb, Guard: guard, Ledger: ledger, Scratch: scratch,
		spawns: map[int]string{}, pending: map[string]bool{}}
}

func (p *LiveDataProbe) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(fmt.Sprintf(format, args...))
	}
}

// Learn asks `.tb learn <scratch> <id>` and returns the module's own word for
// the spell; `refused` when the module refused, `no-row` when its answer held
// no row for the spell (neither is an answer about the spell).
func (p *LiveDataProbe) Learn(ctx context.Context, spell int) (string, error) {
	rep, err := p.TB.Learn(ctx, p.Scratch, uint32(spell))
	if err != nil {
		if tbcmd.IsRefusal(err) {
			return "refused", nil
		}
		return "", err
	}
	for _, res := range rep.Results {
		if int(res.Spell) == spell {
			return res.Result, nil
		}
	}
	return "no-row", nil
}

// ItemScan asks `.tb itemscan <scratch> <entry>`: a record is the server's
// answer (`exists=no` is explicit); a refusal or a reply with no record for
// the entry is not.
func (p *LiveDataProbe) ItemScan(ctx context.Context, entry int) (ItemAnswer, error) {
	rep, err := p.TB.ItemScan(ctx, p.Scratch, uint32(entry))
	if err != nil {
		if tbcmd.IsRefusal(err) {
			return ItemAnswer{Note: "refused: " + trimLen(err.Error(), 100)}, nil
		}
		return ItemAnswer{}, err
	}
	for _, res := range rep.Results {
		if int(res.Item) == entry {
			return ItemAnswer{Answered: true, Exists: res.Exists, CanEquip: res.CanEquip}, nil
		}
	}
	return ItemAnswer{Note: "the reply held no record for the entry"}, nil
}

func (p *LiveDataProbe) stranger(ctx context.Context) (string, bool) {
	if p.Stranger == nil {
		return "", false
	}
	return p.Stranger(ctx)
}

// readCtx is the context of one unit read: it outlives an interrupt, so the
// claim of a unit that was spawned just before Ctrl-C still happens.
func readCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

// Spawn summons one entry and claims the unit it produced: a unit absent from
// the snapshot taken just before the add, the only new unit of the entry,
// standing at the tester. The claim is ledgered (with the serve dir) before
// anything else touches it. Anything that cannot be proved this probe's own is
// never claimed and never killed.
func (p *LiveDataProbe) Spawn(ctx context.Context, entry int) (SpawnAnswer, error) {
	if p.NoSpawn != "" {
		return SpawnAnswer{Reason: p.NoSpawn, Halt: true}, nil
	}
	if why := p.Guard.AbortReason(); why != "" {
		return SpawnAnswer{Reason: why, Halt: true}, nil
	}
	if why, near := p.stranger(ctx); near {
		return SpawnAnswer{Reason: why, Halt: true}, nil
	}
	known := map[string]bool{}
	for _, g := range p.Ledger.SummonGUIDs() {
		known[normGUIDOf(g)] = true
	}
	rctx, cancel := readCtx(ctx)
	rows, err := p.Client.UnitsFilter(rctx, spawnScanYards, entry, "")
	cancel()
	if err != nil {
		if IsDisconnected(err) {
			return SpawnAnswer{}, err
		}
		return SpawnAnswer{Reason: "the unit list was unreadable before the spawn, so a new unit could not be told apart: nothing spawned"}, nil
	}
	before := map[string]bool{}
	for _, u := range rows {
		before[normGUIDOf(u.GUID)] = true
	}
	// An interrupt that lands while the add is in flight may have let the line
	// run: the unit read below is the proof, so only an error on a live run
	// decides anything here.
	if err := p.Guard.Send(ctx, "npc_add_temp", TargetNone, "", itoa(entry)); err != nil && ctx.Err() == nil {
		if IsDisconnected(err) {
			return SpawnAnswer{}, err
		}
		if !IsNoReply(err) {
			// a refused line is an answer (no unit will appear), not a
			// transport failure.
			return SpawnAnswer{Reason: "npc add temp refused: " + trimLen(err.Error(), 100)}, nil
		}
		// the line ran; the unit read below is the proof
	}
	deadline := time.Now().Add(spawnWait)
	lastStranger := time.Now()
	for {
		rctx, cancel := readCtx(ctx)
		rows, err := p.Client.UnitsFilter(rctx, spawnScanYards, entry, "")
		cancel()
		if err == nil {
			var fresh []Unit
			for _, u := range rows {
				g := normGUIDOf(u.GUID)
				if g == "" || u.Dead || known[g] || before[g] {
					continue
				}
				fresh = append(fresh, u)
			}
			sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Distance < fresh[j].Distance })
			switch {
			case len(fresh) == 1 && fresh[0].Distance <= spawnClaimYards:
				g := normGUIDOf(fresh[0].GUID)
				p.Ledger.Add(map[string]any{"kind": "summon", "guid": g, "entry": entry,
					"source": "forever-data-check", "dir": p.Dir})
				p.spawns[entry] = g
				p.pending[g] = true
				ans := SpawnAnswer{OK: true, Level: fresh[0].Level}
				// A stranger may arrive while the unit is read: the claim stands
				// (it must be killed) but nothing further is spawned.
				if why, near := p.stranger(ctx); near {
					ans.Halt, ans.Reason = true, why
				}
				return ans, nil
			case len(fresh) >= 1:
				p.Unidentified++
				p.Ledger.Add(map[string]any{"kind": "summon_unclaimed", "entry": entry,
					"fresh_units": len(fresh), "source": "forever-data-check"})
				return SpawnAnswer{Halt: true, Reason: fmt.Sprintf(
					"%d new unit(s) of the entry appeared (nearest %.1f yd): none is provably this probe's own, nothing was killed",
					len(fresh), fresh[0].Distance)}, nil
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			reason := "no new unit of the entry appeared in " + spawnWait.String()
			if ctx.Err() != nil {
				// the add went out and nothing could be proved: a unit may be
				// standing there that this run can no longer identify.
				p.Unidentified++
				reason = "interrupted while the spawned unit was being read: it could not be identified"
			}
			return SpawnAnswer{Reason: reason, Halt: ctx.Err() != nil}, nil
		}
		if time.Since(lastStranger) >= time.Second {
			lastStranger = time.Now()
			if why, near := p.stranger(ctx); near {
				p.Unidentified++
				return SpawnAnswer{Reason: why, Halt: true}, nil
			}
		}
		sleepCtx(ctx, 300*time.Millisecond)
	}
}

// dead reports whether guid is proved dead or gone by a unit read.
func (p *LiveDataProbe) dead(ctx context.Context, guid string) bool {
	rctx, cancel := readCtx(ctx)
	defer cancel()
	u, err := p.Client.Unit(rctx, guid)
	if err != nil {
		return notInView(err)
	}
	return u.Dead
}

// killAndProve kills one ledgered unit and reads it back; a failed kill is
// retried (three tries) because a unit that is not proved dead is a leftover.
func (p *LiveDataProbe) killAndProve(ctx context.Context, guid string) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if p.dead(ctx, guid) {
			delete(p.pending, guid)
			return nil
		}
		if err := p.Guard.Kill(ctx, guid); err != nil {
			last = err
			if IsDisconnected(err) {
				break
			}
			continue
		}
		deadline := time.Now().Add(killProofFor)
		for {
			if p.dead(ctx, guid) {
				delete(p.pending, guid)
				return nil
			}
			if !time.Now().Before(deadline) {
				break
			}
			time.Sleep(killPollGap)
		}
		last = errf("%s still alive after the kill", guid)
	}
	if last == nil {
		last = errf("%s not proved dead", guid)
	}
	return last
}

// Kill kills the unit this probe claimed for entry and proves it dead (a
// no-op when there is none: the report already says the spawn failed). A
// failure leaves the guid pending for Cleanup.
func (p *LiveDataProbe) Kill(ctx context.Context, entry int) error {
	guid := p.spawns[entry]
	if guid == "" {
		return nil
	}
	if err := p.killAndProve(ctx, guid); err != nil {
		return err
	}
	delete(p.spawns, entry)
	return nil
}

// Cleanup is the teardown: it kills every ledgered unit not yet proved dead
// and returns what it could not clear, each ledgered as a leftover. The
// caller passes a context of its own (never the run's, which an interrupt may
// have cancelled).
func (p *LiveDataProbe) Cleanup(ctx context.Context) []string {
	guids := make([]string, 0, len(p.pending))
	for g := range p.pending {
		guids = append(guids, g)
	}
	sort.Strings(guids)
	var left []string
	for _, g := range guids {
		if err := p.killAndProve(ctx, g); err != nil {
			left = append(left, g)
			p.Ledger.Add(map[string]any{"kind": "mob_leftover", "guids": []any{g},
				"reason": trimLen(err.Error(), 120), "source": "forever-data-check"})
			p.logf("data check teardown: %s not cleared: %v", g, err)
		}
	}
	if p.Unidentified > 0 {
		left = append(left, fmt.Sprintf("unidentified:%d", p.Unidentified))
	}
	return left
}

// LiveDataCheck is everything RunLiveDataCheck needs; the command fills it
// from the lease and the scratch login.
type LiveDataCheck struct {
	Client        *Client
	TB            *tbcmd.Client
	Server        string
	Dir           string // the serve dir (ledger rows)
	Ledger        *Ledger
	Scratch       string
	ScratchGUID   string
	PlayerGUID    string
	ArenaStateDir string
	Arenas        []Arena
	// Abort stops the run (a lost lease or an interrupt); TeardownAbort is the
	// teardown's own check (a lost lease only: Ctrl-C must not cancel the kills).
	Abort         func() string
	TeardownAbort func() string
	Plan          DataCheckPlan
	CheckedAt     string
	Log           func(string)
}

// RunLiveDataCheck runs the data check as a trial does: inside a quiet arena
// under the arena lock, the stranger check before and during every spawn, the
// guard with the run's abort, the ledger in the run dir, and a teardown that
// runs on every exit on a fresh context.
func RunLiveDataCheck(ctx context.Context, o LiveDataCheck) (report *DataCheckReport, err error) {
	marks := NewMarks("")
	marks.Start("")
	guard := NewGuard(o.Client, GuardOptions{Ledger: o.Ledger, Marks: marks,
		PlayerGUID: o.PlayerGUID, HireGUID: o.ScratchGUID, Summons: o.Ledger.SummonGUIDs, Abort: o.Abort})
	rep := &Repeat{Client: o.Client, Guard: guard, Marks: marks, Ledger: o.Ledger, Server: o.Server,
		TrialID: "forever-data-check", BotName: o.Scratch,
		Guids: Guids{Player: normGUIDOf(o.PlayerGUID), Bot: normGUIDOf(o.ScratchGUID)}}
	sit := &LiveSituation{Server: o.Server, ArenaStateDir: o.ArenaStateDir, Arenas: o.Arenas, Log: o.Log}
	t := &tankRun{sit: sit, r: rep, tb: o.TB, kind: "forever-data-check"}
	probe := NewLiveDataProbe(o.Client, o.TB, guard, o.Ledger, o.Scratch)
	probe.Dir, probe.Log, probe.Stranger = o.Dir, o.Log, t.strangerNear
	defer func() {
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		guard.SetAbort(o.TeardownAbort)
		left := probe.Cleanup(tctx)
		if report != nil {
			report.Leftover = left
		} else if len(left) > 0 && err == nil {
			err = fmt.Errorf("data check teardown left %s", strings.Join(left, ", "))
		}
		sit.Finish()
	}()
	if arena, why := t.acquireArena(ctx, 0); arena == nil {
		probe.NoSpawn = "no quiet arena: " + why
	}
	return RunDataCheck(ctx, probe, o.Plan, o.Server, o.CheckedAt)
}

// DataCheckExit is the command's exit code: 0 only for a clean report. A
// `differs` row, a unit teardown could not clear, or (unless allowUnknown)
// any `unknown` row fails it: an unknown is evidence the check did not get.
func DataCheckExit(report *DataCheckReport, allowUnknown bool) int {
	if report == nil {
		return 1
	}
	if report.Counts["differs"] > 0 || len(report.Leftover) > 0 {
		return 1
	}
	if report.Counts["unknown"] > 0 && !allowUnknown {
		return 1
	}
	return 0
}

// ScratchOnline is the refusal of a scratch bot that is already logged in:
// the check never takes over (or logs out) a character it did not log in.
type ScratchOnline struct{ Name string }

func (e *ScratchOnline) Error() string {
	return "scratch bot " + e.Name + " is already online; the data check does not take over a running bot"
}

// CheckScratch refuses a scratch bot name that is empty, that is a trial's
// pool bot (`.tb learn` persists on the scratch bot, so it must be a
// character no trial ever hires), or that is the slot's own character.
func CheckScratch(name string, pool map[string][]string, player string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("no scratch bot: name a dedicated one with --scratch or the server's `scratch_bot` (never a bot_pool bot)")
	}
	if strings.EqualFold(name, player) {
		return errors.New("scratch bot " + name + " is the slot's own character")
	}
	for class, names := range pool {
		for _, n := range names {
			if strings.EqualFold(strings.TrimSpace(n), name) {
				return errors.New("scratch bot " + name + " is in bot_pool (" + class +
					"): `.tb learn` would leave spells on a bot the trials score; use a dedicated scratch bot")
			}
		}
	}
	return nil
}

// ScratchLogin logs the scratch bot in and returns its guid from the party
// roster (the serve's party rows carry the name). A bot that is already
// online is refused with *ScratchOnline.
func ScratchLogin(ctx context.Context, client *Client, tb *tbcmd.Client, name, player string) (string, error) {
	lines, err := tb.FreeTextLogin(ctx, name, player)
	if err != nil {
		if alreadyLoggedIn(lines) {
			return "", &ScratchOnline{Name: name}
		}
		return "", err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := client.Party(ctx)
		if err == nil {
			for _, m := range info.Members {
				if strings.EqualFold(m.Name, name) && m.GUID != "" {
					return normGUIDOf(m.GUID), nil
				}
			}
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return "", errf("scratch bot %s did not appear in the party roster", name)
		}
		sleepCtx(ctx, time.Second)
	}
}
