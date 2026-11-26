package live

// The #275 bench findings fixed here: the live sweep of 2026-11-25 stopped
// every class trial in setup. Three are the bench's own reads and one is a
// kit the server does not hold:
//
//   - `status` keeps the level it had at login after `.levelup`, while the
//     server's own `unit` row carries the new one, so the level row failed
//     UNVERIFIED (item 3).
//   - the power set is read back from one `units` snapshot taken right after
//     the command; a lagging snapshot read 19% for a 15% set (item 4).
//   - `.tb logout` answers before the bot leaves, so the next trial's login
//     found it "already logged in"; teardown must see it leave (item 5).
//   - a spec kit the Forever spell table does not hold (rogue combat 52507)
//     cannot be learned: the trial is SKIPPED with needs=bot_spec, never an
//     INVALID from a learn the server can only answer `unknown` (item 6).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// TestPlayerLevelReadsTheOwnUnitRow pins item 3: the status snapshot stays at
// the pre-levelup level, the server's unit row carries the new one, and the
// row must pass on that read (with exactly one delta sent).
func TestPlayerLevelReadsTheOwnUnitRow(t *testing.T) {
	f := newFakeServe(t)
	var mu sync.Mutex
	unitLevel := 30
	target := ""
	f.statusFn = func() map[string]any {
		mu.Lock()
		tg := target
		mu.Unlock()
		return map[string]any{"state": "in_world", "guid": gTester, "character": "Atank",
			"level": 30, "mono_ms": 1000, "target": tg}
	}
	f.ownUnit = map[string]any{"guid": gTester, "name": "Atank", "type": "player", "level": 30,
		"health": 2000, "max_health": 2000, "power": 100, "max_power": 100, "auras": []any{}}
	f.hook = func(method string, params map[string]any) {
		if method == "target" {
			mu.Lock()
			target, _ = params["guid"].(string)
			mu.Unlock()
			return
		}
		if method != "gm" {
			return
		}
		text, _ := params["text"].(string)
		if !strings.HasPrefix(text, ".levelup ") {
			return
		}
		var delta int
		if _, err := fmt.Sscanf(text, ".levelup %d", &delta); err != nil {
			return
		}
		time.AfterFunc(20*time.Millisecond, func() {
			mu.Lock()
			unitLevel += delta
			level := unitLevel
			mu.Unlock()
			f.setOwnUnitLevel(level)
		})
	}
	cl := NewClient(f.rpc())
	row, err := levelStepSetup(cl, NewLedger("")).ensureLevel(context.Background(), trialLevelOf(60))
	if err != nil {
		t.Fatal(err)
	}
	lines := levelUpLines(f)
	if len(lines) != 1 || levelUpDelta(t, lines[0]) != 30 {
		t.Fatalf("want one .levelup 30 from the stale read, got %v", lines)
	}
	if !rowOK(row) || intOf(row["value"]) != 60 {
		t.Fatalf("the unit row's new level is the read-back: %v", row)
	}
}

// TestPlayerLevelDoesNotTrustAStaleStatus pins the review's case: the status
// snapshot carries the target level, the player's unit row cannot be read, and
// the world may be at another level. A stale status that happens to equal the
// target is no proof, so the row fails UNVERIFIED and no delta is sent.
func TestPlayerLevelDoesNotTrustAStaleStatus(t *testing.T) {
	f := newFakeServe(t)
	f.statusFn = func() map[string]any {
		return map[string]any{"state": "in_world", "guid": gTester, "character": "Atank",
			"level": 60, "mono_ms": 1000, "target": ""}
	}
	// No ownUnit and no units: `unit <guid>` answers "not in view".
	cl := NewClient(f.rpc())
	row, err := levelStepSetup(cl, NewLedger("")).ensureLevel(context.Background(), trialLevelOf(60))
	if err != nil {
		t.Fatal(err)
	}
	if rowOK(row) {
		t.Fatalf("a stale status equal to the target is no proof of the level: %v", row)
	}
	ev := rowEvidence(row)
	if !strings.Contains(ev, "UNVERIFIED") || !strings.Contains(ev, "unit row") {
		t.Fatalf("the row must say UNVERIFIED on the unit row: %q", ev)
	}
	if lines := levelUpLines(f); len(lines) != 0 {
		t.Fatalf("an unreadable level must send no .levelup: %v", lines)
	}
}

// TestEnsureManaWaitsOutAStaleUnitsSnapshot pins item 4: the first reads
// after `.tb power` still show 19%, a moment later the snapshot carries the
// 15% the command set. The row polls instead of judging one stale read.
func TestEnsureManaWaitsOutAStaleUnitsSnapshot(t *testing.T) {
	var mu sync.Mutex
	sent := false
	reads := 0
	prov, _ := manaProv(t, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		power := 1000
		if sent {
			reads++
			if reads < 3 {
				power = 190
			} else {
				power = 150
			}
		}
		return []map[string]any{{"guid": gHire, "name": "", "type": "player", "level": 40,
			"power": power, "max_power": 1000}}
	}, func(line string) []string {
		if strings.HasPrefix(line, ".tb power") {
			mu.Lock()
			sent, reads = true, 0
			mu.Unlock()
			return []string{"TortoiseBots: Bheal mana is set to 15 percent."}
		}
		return nil
	})
	pct := 15.0
	row := prov.ensureMana(context.Background(), BotSpec{Mana: &pct}, "Bheal")
	if !rowOK(row) {
		t.Fatalf("a lagging snapshot must be polled, not judged on one read: %v", row)
	}
	mu.Lock()
	gotReads := reads
	mu.Unlock()
	if gotReads < 3 {
		t.Fatalf("the row passed before the snapshot carried the set value (%d reads)", gotReads)
	}
	if ev := rowEvidence(row); !strings.Contains(ev, "set 15%") {
		t.Fatalf("the evidence must name the set percentage: %q", ev)
	}
}

// TestTrialTeardownWaitsForTheLogoutToLand pins item 5: the module answers
// `.tb logout` before the bot leaves, so the sweep's next login found it
// already online. Teardown reads the party roster and the hire's unit row
// until both prove it gone.
func TestTrialTeardownWaitsForTheLogoutToLand(t *testing.T) {
	s, f := newSetupFake(t)
	// The server still holds the hire's unit until the logout lands.
	f.units = []map[string]any{{"guid": gHire, "name": "Bheal", "type": "player", "level": 40,
		"health": 1500, "max_health": 1500, "auras": []any{}}}
	// The roster keeps the bot for two reads after the logout answer.
	s.scriptParty("Atank", "Bheal")
	s.scriptParty("Atank", "Bheal")
	s.scriptParty("Atank")
	s.tbFn = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb logout"):
			f.mu.Lock()
			f.units = nil // the character is gone from the server's world
			f.mu.Unlock()
			return []string{"TortoiseBots: Bheal is logging out."}
		case strings.HasPrefix(line, ".tb role clear"):
			return []string{"role-begin #T", "role bot=Acmd role= seq=8 status=queued #T",
				"role-end role= target=Acmd count=1 records=1 queued=1 #T"}
		}
		return nil
	}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2",
		TeardownWait: time.Second, TeardownPoll: time.Millisecond}
	r.tbRunner = s.runner
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", TrialID: "t",
		Guids: Guids{Bot: gHire}}
	problems := r.trialTeardown(context.Background(), rep, "t")
	for _, p := range problems {
		if strings.Contains(p, "logout") {
			t.Fatalf("the logout must be waited out, not reported: %v", problems)
		}
	}
	if got := f.callCount("party"); got < 3 {
		t.Fatalf("teardown stopped after %d roster read(s); it must see the bot leave", got)
	}
}

// TestTrialTeardownDoesNotProveALogoutFromTheRosterAlone pins the review's
// case: the roster drops the name (a kick or a disband) while the server still
// holds the bot's unit row. That is not a logout, and teardown must say so.
func TestTrialTeardownDoesNotProveALogoutFromTheRosterAlone(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{{"guid": gHire, "name": "Bheal", "type": "player", "level": 40,
		"health": 1500, "max_health": 1500, "auras": []any{}}}
	for range 20 {
		s.scriptParty("Atank")
	}
	s.tbFn = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb logout"):
			return []string{"TortoiseBots: Bheal is logging out."}
		case strings.HasPrefix(line, ".tb role clear"):
			return []string{"role-begin #T", "role bot=Acmd role= seq=8 status=queued #T",
				"role-end role= target=Acmd count=1 records=1 queued=1 #T"}
		}
		return nil
	}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2",
		TeardownPoll: time.Millisecond, LogoutWait: time.Millisecond}
	r.tbRunner = s.runner
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", TrialID: "t",
		Guids: Guids{Bot: gHire}}
	problems := r.trialTeardown(context.Background(), rep, "t")
	found := false
	for _, p := range problems {
		if strings.Contains(p, "logout of Bheal") && strings.Contains(p, "still answers its unit") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a roster drop with the unit still online is no logout: %v", problems)
	}
}

// TestTrialTeardownUnitReadErrorIsNoProof pins the narrow not-in-view match: a
// daemon without the unit method answers method-not-found, which says nothing
// about the character; the logout stays unproved.
func TestTrialTeardownUnitReadErrorIsNoProof(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{{"guid": gHire, "name": "Bheal", "type": "player", "level": 40,
		"health": 1500, "max_health": 1500, "auras": []any{}}}
	f.mu.Lock()
	f.errCodes["unit"] = []int{-32601, -32601, -32601, -32601, -32601, -32601, -32601, -32601}
	f.errMsg["unit"] = "method not found: unit"
	f.mu.Unlock()
	for range 20 {
		s.scriptParty("Atank")
	}
	s.tbFn = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb logout"):
			return []string{"TortoiseBots: Bheal is logging out."}
		case strings.HasPrefix(line, ".tb role clear"):
			return []string{"role-begin #T", "role bot=Acmd role= seq=8 status=queued #T",
				"role-end role= target=Acmd count=1 records=1 queued=1 #T"}
		}
		return nil
	}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2",
		TeardownPoll: time.Millisecond, LogoutWait: time.Millisecond}
	r.tbRunner = s.runner
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", TrialID: "t",
		Guids: Guids{Bot: gHire}}
	problems := r.trialTeardown(context.Background(), rep, "t")
	found := false
	for _, p := range problems {
		if strings.Contains(p, "logout of Bheal") && strings.Contains(p, "unit read failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a method-not-found unit read is no proof of a logout: %v", problems)
	}
}

// TestTalentKitAbsentOnForeverIsSkipped pins item 6: rogue combat's copied
// marker kit is 52507 (Blade Rush), which the server answers `unknown` for.
// The trial must be SKIPPED with needs=bot_spec, never INVALID from the learn.
func TestTalentKitAbsentOnForeverIsSkipped(t *testing.T) {
	s, _ := newSetupFake(t)
	s.scriptTB(".tb learn Bnrog 52507", "learn-begin #T",
		"learn bot=Bnrog spell=52507 result=unknown name=Blade%20Rush #T",
		"learn-end bot=Bnrog count=1 records=1 learned=0 #T")
	prov := levelProv(s)
	prov.TB = tbcmd.NewClient(s.runner)
	row := prov.ensureTalents(context.Background(), BotSpec{Class: "rogue", Spec: "combat"}, "Bnrog")
	if row == nil {
		t.Fatal("no bot_spec row")
	}
	if rowOK(row) {
		t.Fatalf("a kit the server cannot learn is no proof of the spec: %v", row)
	}
	ev := rowEvidence(row)
	if needs := skippedIn(ev); needs != "bot_spec" {
		t.Fatalf("want SKIPPED needs=bot_spec, got %q", ev)
	}
	if !strings.Contains(ev, "52507") || !strings.Contains(ev, "unknown") {
		t.Fatalf("the skip must name the missing id and the server's answer: %q", ev)
	}
}

// TestTalentKitInvalidIsStillAFailure keeps the other learn answers a failure:
// the server knows the spell and refused it, which is not a server gap.
func TestTalentKitInvalidIsStillAFailure(t *testing.T) {
	s, _ := newSetupFake(t)
	s.scriptTB(".tb learn Bnrog 52507", "learn-begin #T",
		"learn bot=Bnrog spell=52507 result=not-learned name=Blade%20Rush #T",
		"learn-end bot=Bnrog count=1 records=1 learned=0 #T")
	prov := levelProv(s)
	prov.TB = tbcmd.NewClient(s.runner)
	row := prov.ensureTalents(context.Background(), BotSpec{Class: "rogue", Spec: "combat"}, "Bnrog")
	if rowOK(row) || skippedIn(rowEvidence(row)) != "" {
		t.Fatalf("not-learned is a failed row, not a SKIP: %v", row)
	}
}
