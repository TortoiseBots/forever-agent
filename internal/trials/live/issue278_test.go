package live

// issue278_test.go: round 3 of the bench fixes.
//
// The 2026-11-27 sweep with this branch still scored 2 of 50. Two defects
// remained:
//
//   - a teardown whose connection was aborted mid-run ("the call was aborted
//     and the connection is closed") never sent `.tb logout` again, and the
//     leftover hire made every later trial fail setup with "already logged
//     in or loading". The logout must be retried on a fresh call, a
//     leftover session must be ended (and proved gone) before the next
//     login, and a teardown's unproven problems must survive an early
//     return instead of vanishing with it.
//   - `.bot inv` was cut off before INV_END, so a worn trial copy could
//     never be proved and unequipped (the next equip answered "bags full").

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// teardownRunner wires a Runner's trial teardown over the setup fake, with
// waits short enough for a unit test.
func teardownRunner(s *setupFake) *Runner {
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2",
		TeardownPoll: time.Millisecond, LogoutWait: 5 * time.Millisecond}
	r.tbRunner = s.runner
	return r
}

// The live case: the first `.tb logout` ended in an aborted call ("the call
// was aborted and the connection is closed"). That is no proof the hire
// stayed online, so the logout must be attempted again on a fresh call; the
// roster and the unit row then decide. Returning unproved after the first
// transport error left the pool hire online for every later trial.
func TestTrialTeardownRetriesAnAbortedLogoutSend(t *testing.T) {
	s, _ := newSetupFake(t)
	for range 40 {
		s.scriptParty("Atank")
	}
	sends := 0
	inner := s.runner.bot
	s.runner.bot = func(ctx context.Context, line string, waitMS, maxLines int) (BotResult, error) {
		if strings.HasPrefix(line, ".tb logout") {
			sends++
			if sends == 1 {
				return BotResult{}, errors.New("timed out after 20s: the call was aborted and the connection is closed")
			}
		}
		return inner(ctx, line, waitMS, maxLines)
	}
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb logout") {
			return []string{"TortoiseBots: Bheal is logging out."}
		}
		return nil
	}
	r := teardownRunner(s)
	// The release sends are tokenless and wait a quiet window on the
	// runner's clock; each read advances it a second, so they end at once.
	var ticks int
	s.runner.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * time.Second)
	}
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", TrialID: "t",
		Guids: Guids{Bot: gHire}}
	problems := r.trialTeardown(context.Background(), rep, "t")
	if sends != 2 {
		t.Fatalf("an aborted logout send must be retried once (sends=%d): %v", sends, problems)
	}
	for _, p := range problems {
		if strings.Contains(p, "logout") {
			t.Fatalf("the retried logout must be proved by the roster and the unit row: %v", problems)
		}
	}
}

// The module answers `.tb login` with "that bot is already logged in or
// loading" (it names no bot), so the login row cannot pass and the next
// trial never hires. Setup must end that leftover session, prove it left,
// and log in again.
func TestSetupEndsALeftoverSessionBeforeLoggingInAgain(t *testing.T) {
	s, f := newSetupFake(t)
	trial := map[string]any{"id": "t", "bot": map[string]any{"class": "priest", "role": "healer", "level": 40}}

	// A live fake world: the roster answers for whoever is online now, and the
	// unit row follows the same state the logout and the login change.
	var stateMu sync.Mutex
	stale := true
	online := func() bool {
		stateMu.Lock()
		defer stateMu.Unlock()
		return stale
	}
	setOnline := func(v bool) {
		stateMu.Lock()
		defer stateMu.Unlock()
		stale = v
	}
	f.hook = func(method string, _ map[string]any) {
		if method != "party" {
			return
		}
		names := []string{"Acmd"}
		if online() {
			names = append(names, "Bheal")
		}
		members := make([]any, 0, len(names))
		for i, n := range names {
			members = append(members, map[string]any{"name": n,
				"guid": "0x000000000000000" + itoa(i+1), "online": true, "leader": i == 0})
		}
		f.mu.Lock()
		f.script["party"] = []any{map[string]any{
			"in_group": true, "leader": "0x0000000000000001", "members": members}}
		f.mu.Unlock()
	}

	logins, logouts := 0, 0
	s.tbFn = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb login"):
			logins++
			if logins == 1 {
				return []string{"that bot is already logged in or loading"}
			}
			setOnline(true)
			return []string{"TortoiseBots: Bheal is logging in."}
		case strings.HasPrefix(line, ".tb logout"):
			logouts++
			setOnline(false)
			f.mu.Lock()
			f.units = nil // the character is gone from the server's world
			f.mu.Unlock()
			return []string{"TortoiseBots: Bheal is logging out."}
		}
		return nil
	}
	s.scriptTB(".tb state", "state-begin #T",
		"state bot=Bheal role=healer rolesrc=forced class=priest level=40 hp=1500 hpmax=1500 hppct=100 power=mana pw=2000 pwmax=2000 st=non-combat incombat=no tgt= tgtentry= tgthp= victim= hold=no mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 dl=self dm=2.4 ord=7:ok lastorder=stay co=heal nc= cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= #T",
		"state-end count=1 records=1 resting=0 ready=yes target= #T")
	s.scriptTB(".tb engine", "TortoiseBots: engine on.")

	// Tokenless lines wait a quiet window on the runner's clock: each read
	// advances it a second, so the login and logout answer at once.
	var ticks int
	s.runner.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * time.Second)
	}

	prov := &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner), Server: "test2",
		Ledger: NewLedger(""), Player: "Acmd", Pool: map[string][]string{"priest": {"Bheal"}}, Timeout: 2 * time.Second,
		GrantPath: filepath.Join(t.TempDir(), "grants.json")}
	var hired []string
	prov.OnHire = func(name string) { hired = append(hired, name) }
	out, rows, err := prov.Provision(context.Background(), trial, map[string]any{}, "")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if out.HireName != "Bheal" {
		t.Fatalf("the hire must be the pool priest: %+v", out)
	}
	if logins != 2 || logouts != 1 {
		t.Fatalf("a leftover session must be logged out and the hire logged in again (logins=%d logouts=%d): %v", logins, logouts, rows)
	}
	present := false
	for _, row := range rows {
		if strOf(row["id"]) == "bot_present" && rowOK(row) {
			present = true
		}
	}
	if !present {
		t.Fatalf("the hire that logged in after the recovery must pass bot_present: %v", rows)
	}
	if len(hired) != 1 || hired[0] != "Bheal" {
		t.Fatalf("the hire must reach the teardown registry: %v", hired)
	}
}

// An aborted trial must not report its teardown as done: when a repeat ends
// early, the trial teardown still runs, and its unproven problems must come
// back with the error (so a sweep stops instead of starting the next trial
// on a dirty slot) and land in RUN.json.
func TestAbortedTrialReportsItsTeardownProblems(t *testing.T) {
	fake := newFakeServe(t)
	fake.errCodes["status"] = []int{-32601} // no status probe: the report below is the repeat's own failure
	out := t.TempDir()
	runner := &Runner{Out: out, Server: "s1", Label: "t",
		Client: NewClient(fake.rpc()), Ledger: NewLedger(filepath.Join(out, "ledger.jsonl")),
		Setup: &abortingSetup{}, TeardownPoll: time.Millisecond, LogoutWait: 5 * time.Millisecond,
		TeardownTimeout: 2 * time.Second}
	trial := map[string]any{"id": "aborting", "min_valid_s": 0}
	if err := writeTrialFile(out, trial); err != nil {
		t.Fatal(err)
	}
	_, err := runner.RunTrial(context.Background(), trialFilePath(out, "aborting"), 1)
	if err == nil {
		t.Fatal("an aborted trial must not return success")
	}
	var td *TeardownUnproven
	if !errors.As(err, &td) {
		t.Fatalf("the unproven teardown must be reported, got %v", err)
	}
	if len(td.Problems) == 0 {
		t.Fatal("the teardown was abandoned without a problem")
	}
	found := false
	for _, p := range td.Problems {
		if strings.Contains(p, "logout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the leftover hire's unproved logout must be a problem: %v", td.Problems)
	}
	raw, rerr := os.ReadFile(filepath.Join(out, "RUN.json"))
	if rerr != nil {
		t.Fatalf("RUN.json must record the unproven teardown: %v", rerr)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["teardown_unproven"] == nil {
		t.Fatalf("RUN.json names no teardown_unproven: %s", raw)
	}
}

// abortingSetup logs a hire in (registering it exactly as the live setup
// does) and then fails the repeat with a transport error, like a run whose
// call was aborted.
type abortingSetup struct{}

func (a *abortingSetup) Run(ctx context.Context, rep *Repeat) (map[string]any, error) {
	rep.noteHire("Bheal", gHire)
	rep.BotName, rep.Guids.Bot = "Bheal", gHire
	return nil, errors.New("timed out after 20s: the call was aborted and the connection is closed")
}
