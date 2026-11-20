package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

func levelProv(s *setupFake) *Provisioner {
	return &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner),
		Guard:  NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"}),
		Ledger: NewLedger(""), Player: "Atank"}
}

func partyN(s *setupFake, n int, names ...string) {
	for i := 0; i < n; i++ {
		s.scriptParty(names...)
	}
}

// Defect 2: on the live client player unit rows carry name "". The level
// comes from the bot's own row by guid (guid from the party roster), never 0
// from a nameless row.
func TestBotLevelReadByGuidFromNamelessRow(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{
		{"guid": "0x0000000000000003", "name": "", "type": "player", "level": 12},
		{"guid": "0x0000000000000002", "name": "", "type": "player", "level": 40},
	}
	partyN(s, 6, "Atank", "Bheal")
	s.scriptTB(".tb level Bheal 40", "TortoiseBots: Bheal is level 40.")
	row := levelProv(s).ensureLevel(context.Background(), BotSpec{Level: 40}, "Bheal")
	if row == nil || !rowOK(row) {
		t.Fatalf("level must read 40 by guid: %v", row)
	}
}

// A level that cannot be read is UNVERIFIED, never a 0 and never ok.
func TestBotLevelUnreadableIsUnverified(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{{"guid": "0x0000000000000009", "name": "", "type": "player", "level": 40}}
	partyN(s, 6, "Atank", "Bheal")
	s.scriptTB(".tb level Bheal 40", "TortoiseBots: Bheal is level 40.")
	row := levelProv(s).ensureLevel(context.Background(), BotSpec{Level: 40}, "Bheal")
	if row == nil || rowOK(row) {
		t.Fatalf("unreadable level must fail the row: %v", row)
	}
	if !strings.Contains(rowEvidence(row), "UNVERIFIED") {
		t.Fatalf("evidence must say UNVERIFIED: %q", rowEvidence(row))
	}
	if v, _ := row["value"].(int); v == 0 && row["value"] != "unknown" {
		t.Fatalf("value must be unknown, not 0: %v", row["value"])
	}
}

// Defect 3: the arena quiet check must not count the tester itself, its own
// pet or the hired bots of this trial; a stranger still makes it not quiet.
func TestArenaQuietIgnoresTesterPetAndHires(t *testing.T) {
	f := newFakeServe(t)
	f.units = []map[string]any{
		{"guid": "0x0000000000000001", "name": "", "type": "active player", "distance": 0},
		{"guid": "0x0000000000000002", "name": "", "type": "player", "distance": 3},
		{"guid": "0x0000000000000004", "name": "", "type": "player", "distance": 4},
		{"guid": "0xF140000000000005", "name": "", "type": "pet", "distance": 2},
	}
	rep := &Repeat{Client: NewClient(f.rpc()), Guids: Guids{Player: "0x0000000000000001",
		Bot: "0x0000000000000002", Extras: []string{"0x0000000000000004"}, Pet: "0xF140000000000005"}}
	tr := &tankRun{r: rep}
	if !tr.quiet(context.Background()) {
		t.Fatal("the tester, its pet and the hires must not break the quiet check")
	}
	// The tester known only by guid (row type not "active player").
	f.units[0]["type"] = "player"
	if !tr.quiet(context.Background()) {
		t.Fatal("the tester's own guid must not count")
	}
	f.units = append(f.units, map[string]any{"guid": "0x0000000000000099", "name": "", "type": "player", "distance": 20})
	if tr.quiet(context.Background()) {
		t.Fatal("a stranger must still make the arena not quiet")
	}
}

// stanceFake is a player whose stance aura 71 appears only after `.learn 71`
// (the spell was unknown), like the live rerun's character.
func stanceFake(t *testing.T) (*PlayerSetup, *fakeServe) {
	t.Helper()
	f := newFakeServe(t)
	var mu sync.Mutex
	learned := false
	f.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001", "character": "Atank", "level": 40, "mono_ms": 1000}
	f.unitsFn = func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		auras := []any{}
		if learned {
			auras = append(auras, map[string]any{"spell_id": 71})
		}
		return []map[string]any{{"guid": "0x0000000000000001", "name": "", "type": "active player", "level": 40, "auras": auras}}
	}
	f.hook = func(method string, params map[string]any) {
		if method == "gm" && strings.HasPrefix(strAny(params["text"]), ".learn") {
			mu.Lock()
			learned = true
			mu.Unlock()
		}
	}
	client := NewClient(f.rpc())
	ps := &PlayerSetup{Client: client, TB: tbcmd.NewClient(NewTBRunner(client)),
		Guard:  NewGuard(client, GuardOptions{PlayerGUID: "0x0000000000000001"}),
		Ledger: NewLedger(""), Player: "Atank", Timeout: 1500 * time.Millisecond, Poll: 20 * time.Millisecond}
	return ps, f
}

func stanceTrial() map[string]any {
	return map[string]any{"player": map[string]any{"level": float64(40), "stance": float64(71)}}
}

// Defect 5: slow aura reads before the .learn must not use up the step's
// budget: the cast, the .learn and the second cast each get their own wait.
func TestStanceWaitsAreNotOneSharedBudget(t *testing.T) {
	ps, _ := stanceFake(t)
	// Each aura read is slow enough that three failed casts (4 reads each)
	// pass the 1.5 s the old code gave the whole step.
	orig := ps.Poll
	ps.Poll = 150 * time.Millisecond // 3 casts x 4 reads x 150 ms = 1.8 s > 1.5 s
	defer func() { ps.Poll = orig }()
	row, err := ps.ensureStance(context.Background(), stanceTrial())
	if err != nil {
		t.Fatal(err)
	}
	if !rowOK(row) {
		t.Fatalf("stance must be held after .learn despite 1.8 s of earlier waits: %v", rowEvidence(row))
	}
}

// A cast that really hangs stays a typed timeout, not a "refused".
func TestStanceRealTimeoutIsTyped(t *testing.T) {
	ps, f := stanceFake(t)
	ps.Timeout = 150 * time.Millisecond
	prev := f.hook
	f.hook = func(method string, params map[string]any) {
		if method == "cast" {
			time.Sleep(600 * time.Millisecond)
		}
		prev(method, params)
	}
	cctx, cancel := ps.ctxOf(context.Background())
	defer cancel()
	err := ps.timedOut("cast", ps.Client.Cast(cctx, 71, "0x0000000000000001"))
	var to *OpTimeout
	if !errors.As(err, &to) || to.Op != "cast" {
		t.Fatalf("a hung cast must be an *OpTimeout, got %v", err)
	}
	row, _ := ps.ensureStance(context.Background(), stanceTrial())
	if rowOK(row) && !strings.Contains(rowEvidence(row), "after .learn") {
		t.Fatalf("unexpected row %v", row)
	}
	if strings.Contains(rowEvidence(row), "refused") {
		t.Fatalf("a timeout must not be reported as refused: %q", rowEvidence(row))
	}
}

func TestMain(m *testing.M) {
	// The recovery marks and arena locks live under the state dir: keep every
	// test off the real one.
	dir, err := os.MkdirTemp("", "trials-state-")
	if err == nil {
		os.Setenv("XDG_STATE_HOME", dir)
		defer os.RemoveAll(dir)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const itemScanInv = "itemscan bot=Bheal item=1708 exists=yes name=Sweet%%20Nectar class=0:5 inv=%d req=0 quality=1 ilvl=5 canequip=no slot=0 why= #T"

func scanReply(inv int) []string {
	return []string{"itemscan-begin #T", fmt.Sprintf(itemScanInv, inv),
		"itemscan-end bot=Bheal count=1 records=1 exist=1 canequip=0 #T"}
}

// Defect 4: what the trial gave is taken back out of the bags; the clear is
// proved by an itemscan read-back and the mark is dropped only then.
func TestClearGivenRemovesAndReadsBack(t *testing.T) {
	s, _ := newSetupFake(t)
	prov := levelProv(s)
	prov.RecoveryPath = filepath.Join(t.TempDir(), "mark.json")
	if err := prov.marks().add(givenItem{Bot: "Bheal", Item: 1708, Count: 20}); err != nil {
		t.Fatal(err)
	}
	s.scriptTB(".tb itemscan Bheal 1708", scanReply(20)...)
	s.scriptTB(".tb additem Bheal 1708 -20", "TortoiseBots: Bheal now has 0 of item 1708.")
	// the read-back after the removal: the scripted prefix answers again with 20
	// unless re-scripted; the fake answers by prefix, so switch the reply.
	calls := 0
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb itemscan") {
			calls++
			if calls == 1 {
				return scanReply(20)
			}
			return scanReply(0)
		}
		return nil
	}
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if !ok {
		t.Fatalf("clear must be proved: %s", ev)
	}
	if got := prov.marks().forBot("Bheal"); len(got) != 0 {
		t.Fatalf("the mark must be dropped after a proved clear: %v", got)
	}
}

// A removal the read-back does not show stays marked and is reported.
func TestClearGivenUnprovedStaysMarked(t *testing.T) {
	s, _ := newSetupFake(t)
	prov := levelProv(s)
	prov.RecoveryPath = filepath.Join(t.TempDir(), "mark.json")
	_ = prov.marks().add(givenItem{Bot: "Bheal", Item: 1708, Count: 20})
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb itemscan") {
			return scanReply(20) // still 20 after the removal
		}
		return nil
	}
	s.scriptTB(".tb additem Bheal 1708 -20", "TortoiseBots: ok.")
	ev, ok := prov.ClearGiven(context.Background(), "Bheal")
	if ok || !strings.Contains(ev, "UNVERIFIED") {
		t.Fatalf("an unproved clear must not read ok: ok=%v %s", ok, ev)
	}
	if len(prov.marks().forBot("Bheal")) != 1 {
		t.Fatal("the mark must stay for the next run")
	}
}

// A killed run's mark (a file) is cleaned by the next run before it gives
// new items; a give writes the mark before it sends.
func TestGiveWritesMarkAndNextRunClearsIt(t *testing.T) {
	s, _ := newSetupFake(t)
	path := filepath.Join(t.TempDir(), "mark.json")
	first := levelProv(s)
	first.RecoveryPath = path
	s.scriptTB(".tb additem Bheal 1708 20", "TortoiseBots: Bheal now has 20 of item 1708.")
	if _, err := first.giveItem(context.Background(), "Bheal", 1708, 20); err != nil {
		t.Fatal(err)
	}
	// the run is killed here: a brand new Provisioner reads the file.
	next := levelProv(s)
	next.RecoveryPath = path
	got := next.marks().forBot("Bheal")
	if len(got) != 1 || got[0].Item != 1708 || got[0].Count != 20 {
		t.Fatalf("the killed run's mark must be on disk: %v", got)
	}
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb itemscan") {
			return scanReply(map[bool]int{true: 0, false: 20}[len(s.removed) > 0])
		}
		if strings.HasPrefix(line, ".tb additem Bheal 1708 -20") {
			s.removed = append(s.removed, line)
			return []string{"TortoiseBots: Bheal now has 0 of item 1708."}
		}
		return nil
	}
	if _, ok := next.ClearGiven(context.Background(), "Bheal"); !ok {
		t.Fatal("the next run must clear the killed run's items")
	}
	if len(s.removed) != 1 {
		t.Fatalf("removal sent %d times", len(s.removed))
	}
}

// "bags full" that clearing does not free: SKIPPED with the reason, never a
// silent failure and never ok.
func TestBagsFullIsSkippedWithReason(t *testing.T) {
	s, _ := newSetupFake(t)
	prov := levelProv(s)
	spec := BotSpec{Class: "priest", Level: 40, Items: []TrialItem{{Name: "Sweet Nectar", Count: 20}}}
	s.scriptTB(".tb additem Bheal", "TortoiseBots: Bheal: bags full, the item was not stored.")
	row := prov.ensureBotGear(context.Background(), spec, "Bheal")
	if row == nil || rowOK(row) {
		t.Fatalf("a refused give must not be ok: %v", row)
	}
	if skippedIn(rowEvidence(row)) != "bot_bag_space" {
		t.Fatalf("bags full must be SKIPPED needs=bot_bag_space with a reason, got %q", rowEvidence(row))
	}
}

// The trial's teardown clears what it gave while the hire is still online.
func TestTrialTeardownClearsGivenItems(t *testing.T) {
	s, f := newSetupFake(t)
	_ = f
	path := filepath.Join(t.TempDir(), "mark.json")
	prov := levelProv(s)
	prov.RecoveryPath = path
	_ = prov.marks().add(givenItem{Bot: "Bheal", Item: 1708, Count: 20})
	var seen []string
	s.tbFn = func(line string) []string {
		seen = append(seen, line)
		switch {
		case strings.HasPrefix(line, ".tb itemscan"):
			return scanReply(map[bool]int{true: 0, false: 20}[strings.Contains(strings.Join(seen, "|"), "-20")])
		case strings.HasPrefix(line, ".tb additem Bheal 1708 -20"):
			return []string{"TortoiseBots: Bheal now has 0 of item 1708."}
		}
		return nil
	}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger("")}
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal", RecoveryPath: path, TrialID: "t"}
	r.tbRunner = s.runner
	r.trialTeardown(context.Background(), rep, "t")
	idx := func(prefix string) int {
		for i, l := range seen {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	rm, logout := idx(".tb additem Bheal 1708 -20"), idx(".tb logout")
	if rm < 0 || (logout >= 0 && rm > logout) {
		t.Fatalf("items must be cleared before the logout: %v", seen)
	}
	if len(prov.marks().forBot("Bheal")) != 0 {
		t.Fatal("the mark must be gone after the teardown clear")
	}
}
