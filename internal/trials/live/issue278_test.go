package live

// issue278_test.go: round 3 of the bench fixes.
//
// The 2026-11-27 sweep with this branch still scored 2 of 50. Two defects
// remained:
//
//   - a teardown whose connection was aborted mid-run ("the call was aborted
//     and the connection is closed") never sent `.tb logout` again, and the
//     leftover hire made every later trial fail setup with "already logged
//     in or loading". The logout must be retried on a fresh call, and a
//     teardown's unproven problems must survive an early return instead of
//     vanishing with it. A session the trial did not start is never ended:
//     it is reported and left alone.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// Review finding (high): the module answers `.tb login` with "that bot is
// already logged in or loading" (it names no bot). Nothing proves whose
// session that is, so the trial never acquired it: the setup reports it,
// sends no logout, and never puts it on the teardown list. The old code
// registered the name and logged the session out.
func TestSetupLeavesASessionItDidNotStartAlone(t *testing.T) {
	s, _ := newSetupFake(t)
	trial := map[string]any{"id": "t", "bot": map[string]any{"class": "priest", "role": "healer", "level": 40}}
	s.scriptParty("Atank", "Bheal") // someone's Bheal is online and grouped
	var sent []string
	s.tbFn = func(line string) []string {
		sent = append(sent, line)
		switch {
		case strings.HasPrefix(line, ".tb login"):
			return []string{"that bot is already logged in or loading"}
		case strings.HasPrefix(line, ".tb logout"):
			return []string{"TortoiseBots: Bheal is logging out."}
		}
		return nil
	}
	var ticks int
	s.runner.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * time.Second)
	}
	prov := &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner), Server: "test2",
		Ledger: NewLedger(""), Player: "Acmd", Pool: map[string][]string{"priest": {"Bheal"}}, Timeout: 2 * time.Second,
		GrantPath: filepath.Join(t.TempDir(), "grants.json")}
	rep := &Repeat{Client: s.runner.client, Ledger: NewLedger(""), TrialID: "t"}
	var hired []string
	prov.OnHire = func(name string) { hired = append(hired, name); rep.noteHire(name, "") }
	prov.OnHireGUID = func(name, guid string) { rep.noteHire(name, guid) }
	out, rows, err := prov.Provision(context.Background(), trial, map[string]any{}, "")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if out.HireName != "" {
		t.Fatalf("a session this trial did not start is no hire: %+v", out)
	}
	failed := false
	for _, row := range rows {
		if strOf(row["id"]) == "bot_present" && !rowOK(row) && strings.Contains(rowEvidence(row), "did not start") {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("bot_present must fail and say the session is not this trial's: %v", rows)
	}
	if len(hired) != 0 || len(acquiredHires(rep)) != 0 {
		t.Fatalf("the session must never reach the teardown list: hired=%v list=%+v", hired, acquiredHires(rep))
	}
	r := teardownRunner(s)
	_ = r.trialTeardown(context.Background(), rep, "t")
	for _, l := range sent {
		if strings.HasPrefix(l, ".tb logout") || strings.HasPrefix(l, ".tb stay") || strings.HasPrefix(l, ".tb free") {
			t.Fatalf("nothing may be sent to a session this trial did not acquire: %v", sent)
		}
	}
}

// The extra-hire half of the same rule.
func TestExtraHireLeavesASessionItDidNotStartAlone(t *testing.T) {
	s, _ := newSetupFake(t)
	s.scriptParty("Atank", "Bx")
	var sent []string
	s.tbFn = func(line string) []string {
		sent = append(sent, line)
		switch {
		case strings.HasPrefix(line, ".tb login"):
			return []string{"that bot is already logged in or loading"}
		case strings.HasPrefix(line, ".tb logout"):
			return []string{"TortoiseBots: Bx is logging out."}
		}
		return nil
	}
	var ticks int
	s.runner.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * time.Second)
	}
	prov := &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner), Server: "test2",
		Ledger: NewLedger(""), Player: "Acmd", Pool: map[string][]string{"priest": {"Bx"}}, Timeout: 2 * time.Second,
		GrantPath: filepath.Join(t.TempDir(), "grants.json")}
	rep := &Repeat{Client: s.runner.client, Ledger: NewLedger(""), TrialID: "t"}
	prov.OnExtra = func(name string) { rep.noteHire(name, "") }
	prov.OnHireGUID = func(name, guid string) { rep.noteHire(name, guid) }
	trial := map[string]any{"extra_bots": []any{map[string]any{
		"class": "priest", "role": "healer", "level": float64(40)}}}
	out, rows, err := prov.ProvisionExtras(context.Background(), trial, map[string]any{"player": "0x0000000000000001"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ExtraNames) != 0 {
		t.Fatalf("a session this trial did not start is no extra hire: %+v", out)
	}
	failed := false
	for _, row := range rows {
		if strOf(row["id"]) == "extra_bots_present" && !rowOK(row) && strings.Contains(rowEvidence(row), "did not start") {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("extra_bots_present must fail and say the session is not this trial's: %v", rows)
	}
	if got := acquiredHires(rep); len(got) != 0 {
		t.Fatalf("the session must never reach the teardown list: %+v", got)
	}
	r := teardownRunner(s)
	_ = r.trialTeardown(context.Background(), rep, "t")
	for _, l := range sent {
		if strings.HasPrefix(l, ".tb logout") || strings.HasPrefix(l, ".tb stay") || strings.HasPrefix(l, ".tb free") {
			t.Fatalf("nothing may be sent to a session this trial did not acquire: %v", sent)
		}
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
