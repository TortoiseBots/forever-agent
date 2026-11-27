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
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/data"
	"github.com/pjerra/forever-agent/internal/trials/gear"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
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

// TestTrialTeardownLogsOutWhenTheClearRanOutOfTime pins the live 2026-11-27
// regression: the item clear used the teardown context up (a 20-copy stack
// cost one read and one removal per copy), and the logout sent afterwards on
// that same context answered "cancelled: the call was aborted and the
// connection is closed", so the pool hire stayed online and every later trial
// failed setup with "already logged in or loading". The release steps must
// run on a fresh teardown context, independent of the clear.
func TestTrialTeardownLogsOutWhenTheClearRanOutOfTime(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{{"guid": gHire, "name": "Bheal", "type": "player", "level": 40,
		"health": 1500, "max_health": 1500, "auras": []any{}}}
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
		TeardownPoll: time.Millisecond, LogoutWait: time.Millisecond}
	r.tbRunner = s.runner
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", TrialID: "t",
		Guids: Guids{Bot: gHire}}
	dead, cancel := context.WithCancel(context.Background())
	cancel() // the live case: the teardown context was already used up
	problems := r.trialTeardown(dead, rep, "t")
	for _, p := range problems {
		if strings.Contains(p, "logout") {
			t.Fatalf("the logout must not depend on the item clear's context: %v", problems)
		}
	}
}

// TestTrialTeardownReleaseBudgetStartsAfterTheClear pins the timing half of
// the same live case: the clear runs on a live teardown deadline until it
// expires, and the release budget must not have been counting down beside it.
// Otherwise stay, free and logout start on an expired context and the hire
// stays online. Here the clear blocks until its deadline; the release must
// then still prove the logout.
func TestTrialTeardownReleaseBudgetStartsAfterTheClear(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{{"guid": gHire, "name": "Bheal", "type": "player", "level": 40,
		"health": 1500, "max_health": 1500, "auras": []any{}}}
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
	// A grant record gives the clear an instance to read; the itemscan below
	// blocks on the clear's own deadline.
	prov := &Provisioner{Server: "test2", GrantPath: filepath.Join(t.TempDir(), "grants.json")}
	writeGrants(t, prov.GrantPath, []ItemGrant{{Server: "test2", Character: "Bheal", Item: 1708,
		GUID: "00000000000000000000000000000001"}})
	inner := s.runner.bot
	s.runner.bot = func(ctx context.Context, line string, waitMS, maxLines int) (BotResult, error) {
		if strings.HasPrefix(line, ".tb itemscan") {
			<-ctx.Done() // the clear uses its whole window
			return BotResult{}, ctx.Err()
		}
		return inner(ctx, line, waitMS, maxLines)
	}
	// The release sends are tokenless and wait a 1.5 s quiet window on the
	// runner's clock; each read here advances it a second, so the release
	// steps still complete inside a short real budget.
	var ticks int
	s.runner.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * time.Second)
	}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2",
		TeardownTimeout: 500 * time.Millisecond, TeardownPoll: time.Millisecond,
		LogoutWait: time.Second}
	r.tbRunner = s.runner
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", TrialID: "t",
		Guids: Guids{Bot: gHire}, GrantPath: prov.GrantPath}
	// The clear starts on a deadline far longer than the release budget and
	// holds it until it expires: a release clock started beside it is dead.
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	problems := r.trialTeardown(ctx, rep, "t")
	uncleared := false
	for _, p := range problems {
		if strings.Contains(p, "items of Bheal not cleared") {
			uncleared = true
		}
		if strings.Contains(p, "logout") {
			t.Fatalf("the release budget must start after the clear: %v", problems)
		}
	}
	if !uncleared {
		t.Fatalf("the clear must have used its whole deadline: %v", problems)
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
	if !strings.Contains(ev, "52507") || !strings.Contains(ev, "Blade Rush") || !strings.Contains(ev, "unknown") || !strings.Contains(ev, "module/core gap") {
		t.Fatalf("the skip must name the missing id and spell, the server's answer, and module/core gap: %q", ev)
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

type gearFakeWorld struct {
	mu          sync.Mutex
	f           *fakeServe
	known       bool
	stale       bool
	staleReason string
	backpack    map[int32]bool
	equipped    map[int]int32
	unconfirmed map[int32]bool
	learned     []int
	added       []int32
	equipCalls  []int32
	dropAddItem bool
	unconfirmEq bool
	equipErr    string
}

func newGearFakeWorld(t *testing.T) *gearFakeWorld {
	t.Helper()
	f := newFakeServe(t)
	gw := &gearFakeWorld{
		f:           f,
		known:       true,
		backpack:    map[int32]bool{},
		equipped:    map[int]int32{},
		unconfirmed: map[int32]bool{},
	}
	target := ""
	f.statusFn = func() map[string]any {
		gw.mu.Lock()
		tg := target
		gw.mu.Unlock()
		return map[string]any{"state": "in_world", "guid": gTester, "character": "Atank", "level": 40, "mono_ms": 1000, "target": tg}
	}
	f.hook = func(method string, params map[string]any) {
		switch method {
		case "target":
			gw.mu.Lock()
			target, _ = params["guid"].(string)
			gw.mu.Unlock()
		case "gm":
			text, _ := params["text"].(string)
			gw.mu.Lock()
			defer gw.mu.Unlock()
			switch {
			case strings.HasPrefix(text, ".learn "):
				var sp int
				if _, err := fmt.Sscanf(text, ".learn %d", &sp); err == nil {
					gw.learned = append(gw.learned, sp)
				}
			case strings.HasPrefix(text, ".additem "):
				var entry int32
				var count int
				if _, err := fmt.Sscanf(text, ".additem %d %d", &entry, &count); err == nil {
					gw.added = append(gw.added, entry)
					if !gw.dropAddItem {
						gw.backpack[entry] = true
					}
				}
			}
		case "inv":
			gw.mu.Lock()
			items := []map[string]any{}
			slot := 35
			for entry := range gw.backpack {
				items = append(items, map[string]any{
					"guid": "0x4000000000000001", "container": 255, "slot": slot,
					"entry": entry, "count": 1, "placed": true, "unconfirmed": gw.unconfirmed[entry],
				})
				slot++
			}
			for eqSlot, entry := range gw.equipped {
				items = append(items, map[string]any{
					"guid": "0x4000000000000002", "container": 255, "slot": eqSlot,
					"entry": entry, "count": 1, "placed": true, "unconfirmed": gw.unconfirmed[entry],
				})
			}
			for entry := range gw.unconfirmed {
				if !gw.backpack[entry] {
					inEq := false
					for _, e := range gw.equipped {
						if e == entry {
							inEq = true
						}
					}
					if !inEq {
						items = append(items, map[string]any{
							"guid": "0x4000000000000003", "container": 255, "slot": 255,
							"entry": entry, "count": 1, "placed": false, "unconfirmed": true,
						})
					}
				}
			}
			ans := map[string]any{
				"known": gw.known, "stale": gw.stale, "stale_reason": gw.staleReason, "items": items,
			}
			f.mu.Lock()
			f.script["inv"] = append(f.script["inv"], ans)
			f.mu.Unlock()
			gw.mu.Unlock()
		case "equipment":
			gw.mu.Lock()
			rows := []map[string]any{}
			for eqSlot, entry := range gw.equipped {
				rows = append(rows, map[string]any{
					"slot": eqSlot, "slot_name": slotNameForTest(eqSlot),
					"guid": "0x4000000000000002", "item_entry": entry,
				})
			}
			f.mu.Lock()
			f.script["equipment"] = append(f.script["equipment"], rows)
			f.mu.Unlock()
			gw.mu.Unlock()
		case "equip":
			entry := int32(numOrZero(params["item_entry"]))
			gw.mu.Lock()
			gw.equipCalls = append(gw.equipCalls, entry)
			if gw.equipErr != "" {
				f.mu.Lock()
				f.errCodes["equip"] = append(f.errCodes["equip"], -32003)
				f.errMsg["equip"] = gw.equipErr
				f.mu.Unlock()
				gw.mu.Unlock()
				return
			}
			delete(gw.backpack, entry)
			if gw.unconfirmEq {
				gw.unconfirmed[entry] = true
				f.mu.Lock()
				f.script["equip"] = append(f.script["equip"], map[string]any{
					"bag": 255, "slot": 255, "item_entry": entry, "placed": false, "unconfirmed": true,
				})
				f.mu.Unlock()
				gw.mu.Unlock()
				return
			}
			var slot int
			if entry == 10758 { // X'caliboar two-hander: clears off-hand
				slot = 15
				delete(gw.equipped, 16)
			} else if entry == 9404 {
				slot = 16
			} else {
				for slot = 0; slot == 15 || slot == 16 || gw.equipped[slot] != 0; slot++ {
				}
			}
			gw.equipped[slot] = entry
			f.mu.Lock()
			f.script["equip"] = append(f.script["equip"], map[string]any{
				"bag": 255, "slot": slot, "item_entry": entry, "placed": true, "unconfirmed": false,
			})
			f.mu.Unlock()
			gw.mu.Unlock()
		}
	}
	return gw
}

func slotNameForTest(slot int) string {
	names := []string{
		"head", "neck", "shoulders", "body", "chest", "waist", "legs", "feet", "wrists", "hands",
		"finger1", "finger2", "trinket1", "trinket2", "back", "main_hand", "off_hand", "ranged", "tabard",
	}
	if slot >= 0 && slot < len(names) {
		return names[slot]
	}
	return fmt.Sprintf("slot_%d", slot)
}

func gearTestSetup(gw *gearFakeWorld) (*LiveSetup, *Repeat) {
	cl := NewClient(gw.f.rpc())
	led := NewLedger("")
	rep := &Repeat{
		Client:  cl,
		Ledger:  led,
		Marks:   NewMarks(""),
		Guard:   NewGuard(cl, GuardOptions{Ledger: led, Marks: NewMarks(""), PlayerGUID: gTester}),
		TrialID: "tank-warrior-defensives",
	}
	s := &LiveSetup{
		Client:    cl,
		Ledger:    led,
		Player:    "Atank",
		EquipWait: 40 * time.Millisecond,
		EquipPoll: 5 * time.Millisecond,
	}
	return s, rep
}

func warriorTierTrial() map[string]any {
	return map[string]any{
		"player": map[string]any{
			"class":       "warrior",
			"level":       float64(40),
			"forced_role": "dps",
			"gear":        "tier",
		},
		"setup_checks": []any{"player_gear", "player_no_shield"},
	}
}

func TestPlayerGearProvisionsKitAndConfirmsEquipment(t *testing.T) {
	gw := newGearFakeWorld(t)
	gw.equipped[16] = 9404 // pre-existing shield displaced by two-hander X'caliboar
	s, rep := gearTestSetup(gw)
	trial := warriorTierTrial()
	rows := s.playerGearRows(context.Background(), rep, trial, wantedChecks(trial))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %v", len(rows), rows)
	}
	if !rowOK(rows[0]) || strOf(rows[0]["id"]) != "player_gear" {
		t.Fatalf("player_gear failed: %v", rows[0])
	}
	if !rowOK(rows[1]) || strOf(rows[1]["id"]) != "player_no_shield" {
		t.Fatalf("player_no_shield failed: %v", rows[1])
	}
	kit, _, _ := gear.PickKit(trial["player"].(map[string]any))
	if len(gw.added) != len(kit) {
		t.Fatalf("added %d items, want %d", len(gw.added), len(kit))
	}
	if len(gw.equipCalls) != len(kit) {
		t.Fatalf("equipped %d items, want %d", len(gw.equipCalls), len(kit))
	}
	wantSpells := gear.ProficiencySpells(kit)
	if len(gw.learned) != len(wantSpells) {
		t.Fatalf("learned %v, want %v", gw.learned, wantSpells)
	}
}

func TestPlayerGearAlreadyEquippedSkipsAdditemAndEquip(t *testing.T) {
	gw := newGearFakeWorld(t)
	trial := warriorTierTrial()
	kit, _, _ := gear.PickKit(trial["player"].(map[string]any))
	for i, item := range kit {
		slot := i
		if item.Slot == "twohand" {
			slot = 15
		}
		gw.equipped[slot] = int32(item.Entry)
	}
	s, rep := gearTestSetup(gw)
	row := s.ensurePlayerGear(context.Background(), rep, trial)
	if !rowOK(row) {
		t.Fatalf("already-equipped kit must pass: %v", row)
	}
	if len(gw.added) != 0 || len(gw.equipCalls) != 0 {
		t.Fatalf("already-equipped kit sent additem=%v equip=%v", gw.added, gw.equipCalls)
	}
}

func TestPlayerGearFailsClosedOnUnconfirmedOrRefused(t *testing.T) {
	t.Run("additem missing from inv", func(t *testing.T) {
		gw := newGearFakeWorld(t)
		gw.dropAddItem = true
		s, rep := gearTestSetup(gw)
		row := s.ensurePlayerGear(context.Background(), rep, warriorTierTrial())
		if rowOK(row) || !strings.Contains(rowEvidence(row), "UNVERIFIED") {
			t.Fatalf("missing additem read-back must fail UNVERIFIED: %v", row)
		}
		if len(gw.equipCalls) != 0 {
			t.Fatalf("equip called without inv confirmation: %v", gw.equipCalls)
		}
	})
	t.Run("equip refused by server", func(t *testing.T) {
		gw := newGearFakeWorld(t)
		gw.equipErr = "equip: the server refused it: cant_equip_level_i (needs level 40)"
		s, rep := gearTestSetup(gw)
		row := s.ensurePlayerGear(context.Background(), rep, warriorTierTrial())
		if rowOK(row) || !strings.Contains(rowEvidence(row), "cant_equip_level_i") {
			t.Fatalf("refused equip must fail with server reason: %v", row)
		}
	})
	t.Run("unconfirmed equip fails both gear and shield closed", func(t *testing.T) {
		gw := newGearFakeWorld(t)
		gw.unconfirmEq = true
		s, rep := gearTestSetup(gw)
		trial := warriorTierTrial()
		rows := s.playerGearRows(context.Background(), rep, trial, wantedChecks(trial))
		if len(rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(rows))
		}
		if rowOK(rows[0]) || !strings.Contains(rowEvidence(rows[0]), "UNVERIFIED") {
			t.Fatalf("unconfirmed equip must fail player_gear UNVERIFIED: %v", rows[0])
		}
		if rowOK(rows[1]) || !strings.Contains(rowEvidence(rows[1]), "UNVERIFIED") {
			t.Fatalf("unconfirmed equip must fail player_no_shield UNVERIFIED: %v", rows[1])
		}
	})
	t.Run("nil gear is no gear requested", func(t *testing.T) {
		gw := newGearFakeWorld(t)
		s, rep := gearTestSetup(gw)
		trial := map[string]any{"player": map[string]any{"class": "warrior", "level": float64(60), "gear": nil}}
		row := s.ensurePlayerGear(context.Background(), rep, trial)
		if !rowOK(row) {
			t.Fatalf("nil gear must pass: %v", row)
		}
	})
}

func TestPlayerNoShieldReadsOffHandSlot(t *testing.T) {
	t.Run("empty off-hand passes", func(t *testing.T) {
		gw := newGearFakeWorld(t)
		gw.equipped[15] = 10758
		s, rep := gearTestSetup(gw)
		row := s.playerNoShield(context.Background(), rep)
		if !rowOK(row) || strOf(row["value"]) != "none" {
			t.Fatalf("empty off-hand must pass with none: %v", row)
		}
	})
	t.Run("shield in slot 16 fails", func(t *testing.T) {
		gw := newGearFakeWorld(t)
		gw.equipped[16] = 9404
		s, rep := gearTestSetup(gw)
		row := s.playerNoShield(context.Background(), rep)
		if rowOK(row) || intOf(row["value"]) != 9404 || !strings.Contains(rowEvidence(row), "9404") {
			t.Fatalf("shield in off-hand must fail naming 9404: %v", row)
		}
	})
	t.Run("stale or unknown inv fails closed even with empty equipment", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			mut  func(*gearFakeWorld)
		}{
			{"unknown", func(gw *gearFakeWorld) { gw.known = false }},
			{"stale", func(gw *gearFakeWorld) { gw.stale = true; gw.staleReason = "lost slot" }},
			{"unconfirmed", func(gw *gearFakeWorld) { gw.unconfirmed[9404] = true }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				gw := newGearFakeWorld(t)
				tc.mut(gw)
				s, rep := gearTestSetup(gw)
				row := s.playerNoShield(context.Background(), rep)
				if rowOK(row) || !strings.Contains(rowEvidence(row), "UNVERIFIED") {
					t.Fatalf("%s inv must fail closed UNVERIFIED: %v", tc.name, row)
				}
			})
		}
	})
}

func TestGuardAllowsSelfAddItem(t *testing.T) {
	f := newFakeServe(t)
	g := NewGuard(NewClient(f.rpc()), GuardOptions{PlayerGUID: gTester})
	if err := g.Send(context.Background(), "additem", TargetSelf, gTester, "7922", "1"); err != nil {
		t.Fatalf("self additem refused: %v", err)
	}
	if lines := f.gmLines(); len(lines) != 1 || lines[0] != ".additem 7922 1" {
		t.Fatalf("gm lines = %v", lines)
	}
	if err := g.Send(context.Background(), "additem", TargetSelf, "0xdead", "7922", "1"); err == nil {
		t.Fatal("wrong self guid additem passed")
	}
}

func TestPlayerGearRowsRetriesOnceOnTransientFailure(t *testing.T) {
	gw := newGearFakeWorld(t)
	s, rep := gearTestSetup(gw)
	trial := warriorTierTrial()

	// Fail the first inv call (player_gear attempt 1) and the first equipment
	// call after player_gear passes (player_no_shield attempt 1) so retryOnce
	// must recover both rows through the same path Run uses.
	gw.f.mu.Lock()
	gw.f.errCodes["inv"] = []int{-32000}
	gw.f.errMsg["inv"] = "transient inv read failure"
	gw.f.mu.Unlock()

	rows := s.playerGearRows(context.Background(), rep, trial, wantedChecks(trial))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %v", len(rows), rows)
	}
	if !rowOK(rows[0]) || strOf(rows[0]["id"]) != "player_gear" {
		t.Fatalf("player_gear failed to recover on retry: %v", rows[0])
	}
	if !rowOK(rows[1]) || strOf(rows[1]["id"]) != "player_no_shield" {
		t.Fatalf("player_no_shield failed: %v", rows[1])
	}

	// Now fail the first equipment read of a fresh player_no_shield check and
	// confirm retryOnce recovers it on the second try.
	beforeEq := gw.f.callCount("equipment")
	gw.f.mu.Lock()
	gw.f.errCodes["equipment"] = []int{-32000}
	gw.f.errMsg["equipment"] = "transient equipment read failure"
	gw.f.mu.Unlock()
	shieldOnly := map[string]bool{"player_no_shield": true}
	shieldRows := s.playerGearRows(context.Background(), rep, trial, shieldOnly)
	if len(shieldRows) != 1 || !rowOK(shieldRows[0]) {
		t.Fatalf("player_no_shield did not recover via retryOnce: %v", shieldRows)
	}
	if got := gw.f.callCount("equipment") - beforeEq; got != 2 {
		t.Fatalf("expected 2 equipment calls across retryOnce, got %d", got)
	}
}

func TestPlayerGearWithNoShieldAvoidsShieldKit(t *testing.T) {
	gw := newGearFakeWorld(t)
	gw.equipped[16] = 9404 // pre-existing shield in off-hand
	s, rep := gearTestSetup(gw)
	trial := map[string]any{
		"player": map[string]any{
			"class":       "warrior",
			"level":       float64(40),
			"forced_role": "tank",
			"gear":        "tier",
		},
		"setup_checks": []any{"player_gear", "player_no_shield"},
	}
	rows := s.playerGearRows(context.Background(), rep, trial, wantedChecks(trial))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %v", len(rows), rows)
	}
	if !rowOK(rows[0]) || strOf(rows[0]["id"]) != "player_gear" {
		t.Fatalf("player_gear failed: %v", rows[0])
	}
	if !rowOK(rows[1]) || strOf(rows[1]["id"]) != "player_no_shield" || strOf(rows[1]["value"]) != "none" {
		t.Fatalf("player_no_shield failed: %v", rows[1])
	}
	for _, entry := range gw.added {
		if entry == 9404 {
			t.Fatalf("added shield 9404 despite player_no_shield: %v", gw.added)
		}
	}
	for _, sp := range gw.learned {
		if sp == 9116 {
			t.Fatalf("learned shield proficiency 9116 despite player_no_shield: %v", gw.learned)
		}
	}
	hasTwoHand := false
	for _, entry := range gw.equipCalls {
		if entry == 10758 {
			hasTwoHand = true
		}
	}
	if !hasTwoHand {
		t.Fatalf("expected two-hander 10758 to be equipped, got %v", gw.equipCalls)
	}
}

// Item (2) of #278: `equip` from a gear kit passes the target slot `dst` so
// AutoEquip does not touch unrelated equipped items (such as entry 38), and a
// transient unconfirmed move on `inv` waits/retries before failing closed.
func TestPlayerGearEquipsWithSlotDstAndConfirmsTransientUnconfirmed(t *testing.T) {
	gw := newGearFakeWorld(t)
	gw.equipped[3] = 38 // starter shirt untouched by any warrior tier kit
	s, rep := gearTestSetup(gw)
	trial := warriorTierTrial()

	rows := s.playerGearRows(context.Background(), rep, trial, wantedChecks(trial))
	if len(rows) != 2 || !rowOK(rows[0]) || !rowOK(rows[1]) {
		t.Fatalf("playerGearRows = %v", rows)
	}
	// Every equip RPC call must include a non-nil dst slot so actEquip uses SwapInvItem/SwapItem
	// rather than CMSG_AUTO_EQUIP_ITEM touching every equipped slot (including shirt 38).
	gw.f.mu.Lock()
	var equipParams []map[string]any
	for _, c := range gw.f.calls {
		if c.method == "equip" {
			var p map[string]any
			_ = json.Unmarshal(c.params, &p)
			equipParams = append(equipParams, p)
		}
	}
	gw.f.mu.Unlock()
	if len(equipParams) == 0 {
		t.Fatal("expected equip RPC calls")
	}
	for _, p := range equipParams {
		if _, hasDst := p["dst"]; !hasDst {
			t.Fatalf("equip call missing dst slot (would touch entry 38 via AutoEquip): %v", p)
		}
	}

	// Simulate a transient unconfirmed entry 38 that clears on the 2nd inv read:
	// player_no_shield must wait/retry once and pass without touching RetryOnce RPC budget for failures.
	gw.mu.Lock()
	gw.unconfirmed[38] = true
	gw.mu.Unlock()
	reads := 0
	prevHook := gw.f.hook
	gw.f.hook = func(method string, params map[string]any) {
		if prevHook != nil {
			prevHook(method, params)
		}
		if method == "inv" {
			reads++
			if reads >= 2 {
				gw.mu.Lock()
				delete(gw.unconfirmed, 38)
				gw.mu.Unlock()
			}
		}
	}
	s2 := &LiveSetup{
		Client:    NewClient(gw.f.rpc()),
		Ledger:    NewLedger(""),
		Player:    "Atank",
		EquipWait: 20 * time.Millisecond,
		EquipPoll: 5 * time.Millisecond,
	}
	row := s2.playerNoShield(context.Background(), rep)
	if !rowOK(row) {
		t.Fatalf("transient unconfirmed item must confirm after wait/retry read: %v", row)
	}
}

// Item (3) of #278: Forever overlay has no blue kits for paladin, druid, or mage
// (and Forever provides no item kit source to verify them against), so ensureBotGear
// skips cleanly with SKIPPED needs=bot_items before sending any additem/itemscan/equip.
func TestBotItemsMissingBlueKitsSkipWithReason(t *testing.T) {
	cases := []struct {
		class string
		level int
	}{
		{"paladin", 40},
		{"druid", 40},
		{"mage", 40},
		{"mage", 45},
		{"mage", 50},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s-%d", tc.class, tc.level), func(t *testing.T) {
			s, _ := newSetupFake(t)
			var seen []string
			s.tbFn = func(line string) []string {
				seen = append(seen, line)
				return nil
			}
			prov := levelProv(s)
			row := prov.ensureBotGear(context.Background(), BotSpec{Class: tc.class, Gear: "blue", Level: tc.level}, "Bbot")
			if row == nil || rowOK(row) {
				t.Fatalf("expected failed SKIPPED row for %+v, got %v", tc, row)
			}
			ev := rowEvidence(row)
			if skippedIn(ev) != "bot_items" {
				t.Fatalf("want SKIPPED needs=bot_items, got %q", ev)
			}
			if !strings.Contains(ev, fmt.Sprintf("no blue kit for %s level %d", tc.class, tc.level)) {
				t.Fatalf("unexpected skip reason: %q", ev)
			}
			for _, line := range seen {
				if strings.HasPrefix(line, ".tb additem") || strings.HasPrefix(line, ".tb itemscan") || strings.HasPrefix(line, ".tb equip") {
					t.Fatalf("unexpected provisioning command before kit check: %q", line)
				}
			}
		})
	}
}

// Every slot a gear kit names must resolve to a real equipment slot: a name
// kitSlotIndex does not know silently falls back to AutoEquip, which touches
// every worn item (the #278 entry-38 unconfirmed move).
func TestEveryKitSlotResolvesToAnEquipmentSlot(t *testing.T) {
	check := func(path string, doc []byte) {
		t.Helper()
		root, err := pyjson.Unmarshal(doc)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		classes, ok := pyjson.Object(root)
		if !ok {
			t.Fatalf("%s: not an object", path)
		}
		for class, gears := range classes {
			byGear, _ := pyjson.Object(gears)
			for gearName, levels := range byGear {
				byLevel, _ := pyjson.Object(levels)
				for level, list := range byLevel {
					items, _ := pyjson.List(list)
					for _, e := range items {
						item, _ := pyjson.Object(e)
						slot, _ := item["slot"].(string)
						if slot == "" {
							continue
						}
						if idx := kitSlotIndex(slot); idx < 0 {
							t.Fatalf("%s: %s/%s/%s names slot %q, which maps to no equipment slot", path, class, gearName, level, slot)
						}
					}
				}
			}
		}
	}
	check("data/gear_kits.json", data.Read("gear_kits.json"))
	if overlay := data.ReadOverlay("gear_kits.json"); len(overlay) > 0 {
		check("forever/gear_kits.json", overlay)
	}
}
