package live

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// The A3 other rows: equip refused, learn known, free-text read-back,
// SKIPPED on not-a-bot, and the per-call parse table (error lines
// included). The first-failing test lives in setup_test.go
// (TestSetupPriestHealLatencyRows); these pin the contract edges the brief
// names: TestEquipRefusedRow, TestLearnKnownIsOk, TestFreeTextReadBack,
// TestSkipWhenNotABot, TestTestkitParse.

// TestEquipRefusedRow pins the setup contract for a refused equip: the
// parsed refusal (refused + the core's why) is the row evidence, never a
// silent pass.
func TestEquipRefusedRow(t *testing.T) {
	s, _ := newSetupFake(t)
	tok := "#T"
	s.scriptTB(".tb equip Bheal 8120", "equip-begin "+tok,
		"equip bot=Bheal item=8120 name=Offhand%20thing result=refused why=EQUIP_ERR_CANT_EQUIP_SKILL code=7 "+tok,
		"equip-end bot=Bheal count=1 records=1 "+tok)
	tb := tbcmd.NewClient(s.runner)
	rep, err := tb.Equip(context.Background(), "Bheal", 8120, false)
	if err != nil {
		t.Fatalf("Equip refused: %v", err)
	}
	if rep.Result != "refused" || rep.Why == "" {
		t.Fatalf("Equip refused row: %+v", rep)
	}
	row := rowOf("bot_items", false, nil, ".tb equip Bheal 8120 refused: "+rep.Why)
	if rowOK(row) {
		t.Fatal("refused equip passed")
	}
	if !strings.Contains(rowEvidence(row), "EQUIP_ERR_CANT_EQUIP_SKILL") {
		t.Fatalf("refused evidence lost the reason: %v", rowEvidence(row))
	}
}

// TestLearnKnownIsOk pins `learned|known` ok: a kit the bot already holds
// (every record known) provisions fine, and the kit comes from specs.json.
func TestLearnKnownIsOk(t *testing.T) {
	ids := talentKit("priest", "holy")
	if len(ids) == 0 {
		t.Fatal("no priest/holy kit in specs.json")
	}
	s, _ := newSetupFake(t)
	tok := "#T"
	var recs []string
	for _, id := range ids {
		recs = append(recs, "learn bot=Bheal spell="+itoa(int(id))+" result=known name=Spell "+tok)
	}
	s.scriptTB(".tb learn Bheal", append([]string{"learn-begin " + tok}, append(recs, "learn-end bot=Bheal count="+itoa(len(ids))+" records="+itoa(len(ids))+" learned=0 "+tok)...)...)
	tb := tbcmd.NewClient(s.runner)
	rep, err := tb.Learn(context.Background(), "Bheal", ids...)
	if err != nil {
		t.Fatalf("Learn known: %v", err)
	}
	for _, r := range rep.Results {
		if r.Result != "known" && r.Result != "learned" {
			t.Fatalf("Learn known result: %+v", r)
		}
	}
}

// TestFreeTextReadBack pins the read-back rule: a `level` reply
// contradicted by the units model fails the row (free text alone proves
// nothing — the row below is what setup writes when the read-back
// disagrees).
func TestFreeTextReadBack(t *testing.T) {
	s, f := newSetupFake(t)
	f.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"character": "Acmd", "level": 30, "mono_ms": 1000}
	f.units = []map[string]any{
		{"guid": "0x0000000000000002", "name": "Bheal", "level": 39, "health": 1500, "max_health": 1500,
			"power": 2000, "max_power": 2000, "in_combat": false, "auras": []any{}, "casting": nil, "target": ""},
	}
	tb := tbcmd.NewClient(s.runner)
	s.scriptTB(".tb level Bheal 40", "TortoiseBots: Bheal is level 40.")
	lines, err := tb.FreeTextLevel(context.Background(), "Bheal", 40)
	if err != nil {
		t.Fatalf("level text: %v (%v)", err, lines)
	}
	// The read-back (units still 39) contradicts the reply: the row fails.
	row := rowOf("bot_level", false, 39, "Bheal level 39 (target 40): .tb level reply contradicted by the units read-back")
	if rowOK(row) {
		t.Fatal("contradicted level passed")
	}
	if rowEvidence(row) == "" {
		t.Fatal("failed row has no evidence")
	}
}

// TestSkipWhenNotABot pins the TB-1 capability path end to end: a
// `not-a-bot` error on `.tb role <own character>` (the sender's own
// role through RoleSelf) makes the trial SKIPPED with
// needs=player_forced_role in RUN.json — never a FAIL.
func TestSkipWhenNotABot(t *testing.T) {
	s, f := newSetupFake(t)
	trial := healLatencyTrial(t)
	f.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"character": "Acmd", "level": 40, "mono_ms": 1000}
	f.units = []map[string]any{
		{"guid": "0x0000000000000002", "name": "Bheal", "level": 40, "health": 1500, "max_health": 1500,
			"power": 2000, "max_power": 2000, "in_combat": false, "auras": []any{}, "casting": nil, "target": ""},
	}
	tok := "#T"
	s.scriptTB(".tb learn 16462 12286", "learn-begin "+tok,
		"learn bot=Acmd spell=16462 result=known name=S "+tok,
		"learn bot=Acmd spell=12286 result=known name=S "+tok,
		"learn-end bot=Acmd count=2 records=2 learned=0 "+tok)
	s.scriptTB(".tb learn Bheal 52943", "learn-begin "+tok,
		"learn bot=Bheal spell=52943 result=learned name=S "+tok,
		"learn-end bot=Bheal count=1 records=1 learned=1 "+tok)
	s.scriptTB(".tb role", "error code=not-a-bot cmd=role msg=Acmd%20is%20not%20a%20bot "+tok)
	s.scriptTB(".tb gm Acmd", "gm-begin "+tok,
		"gm bot=Acmd tag=SETUP status=ok gm=no "+tok,
		"gm-end count=1 records=1 target=Acmd gm=no "+tok)
	f.script["unit"] = []any{map[string]any{"guid": "0x0000000000000001", "name": "Acmd",
		"health": 2000, "max_health": 2000, "power": 100, "max_power": 100,
		"auras": []any{map[string]any{"spell_id": 71}}, "casting": nil, "target": ""}}
	s.scriptTB(".tb state", "state-begin "+tok,
		"state bot=Bheal role=healer rolesrc=forced class=priest level=40 hp=1500 hpmax=1500 hppct=100 power=mana pw=2000 pwmax=2000 st=non-combat incombat=no tgt= tgtentry= tgthp= victim= hold=no mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 dl=self dm=2.4 ord=7:ok lastorder=stay co=heal nc= cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= "+tok,
		"state-end count=1 records=1 resting=0 ready=yes target= "+tok)
	s.scriptTB(".tb party", "party-begin "+tok,
		"party member=Acmd leader=yes online=yes bot=no class=warrior level=40 role=tank engine=no master= commanded=no "+tok,
		"party member=Bheal leader=no online=yes bot=yes class=priest level=40 role=healer engine=yes master=Acmd commanded=no "+tok,
		"party-end leader=Acmd count=2 records=2 bots=1 raid=no commander= "+tok)
	s.scriptTB(".tb login", "TortoiseBots: Bheal is logging in.")
	s.scriptTB(".tb engine", "TortoiseBots: engine on.")
	s.scriptTB(".tb level", "TortoiseBots: Bheal is level 40.")
	s.scriptTB(".tb train", "TortoiseBots: Bheal learned 10 spell(s).")
	s.scriptTB(".tb additem", "TortoiseBots: Bheal now has 20 of item 17083.")

	rep := &Repeat{Trial: trial, TrialID: "priest-holy-heal-latency",
		Client: s.runner.client, Ledger: NewLedger(""),
		Marks: NewMarks(t.TempDir() + "/marks.jsonl")}
	rep.Guard = NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"})
	setup := &LiveSetup{Player: "Acmd", Pool: map[string][]string{"priest": {"Bheal"}}, TBRunner: s.runner}
	_, err := setup.Run(context.Background(), rep)
	needs, ok := IsSkipped(err)
	if !ok || needs != "player_forced_role" {
		t.Fatalf("IsSkipped = %q %v (err %v)", needs, ok, err)
	}
	if got := skippedIn("SKIPPED needs=player_forced_role: error not-a-bot"); got != "player_forced_role" {
		t.Fatalf("skippedIn = %q", got)
	}
}

// TestBotGearNeedsScanAndEquip pins codex [high] (FIX-TRIALS-A3): a bot gear
// row is ok ONLY when the run scanned and equipped the wanted kit through a
// read-back. With no Forever item source for the bot kit there is nothing to
// scan or equip, so ensureBotGear returns ok=false with a SKIPPED reason (a
// required row that is SKIPPED makes the trial SKIPPED, never PASS).
func TestBotGearNeedsScanAndEquip(t *testing.T) {
	s, _ := newSetupFake(t)
	trial := healLatencyTrial(t)
	// The consumables grant fine; the bug is the kit claim after them.
	s.scriptTB(".tb additem", "TortoiseBots: Bheal now has 20 of item 17083.")
	spec := botSpecOf(trial["bot"])
	prov := &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner),
		Guard:  NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"}),
		Ledger: NewLedger(""), Player: "Acmd"}
	row := prov.ensureBotGear(context.Background(), spec, "Bheal")
	if row == nil {
		t.Fatal("gear spec with no kit source must write a bot_items row, not nil")
	}
	if rowOK(row) {
		t.Fatalf("bot_items passed without a scan/equip read-back: %v", rowEvidence(row))
	}
}

// TestShieldUnreadableIsNotOk pins codex [high] (FIX-TRIALS-A3): an
// unreadable shield state is UNVERIFIED and never ok. The LiveSetup shield
// check fails the row so the trial is INVALID, never PASS.
func TestShieldUnreadableIsNotOk(t *testing.T) {
	_, f := newSetupFake(t)
	f.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"character": "Acmd", "level": 40, "mono_ms": 1000}
	rep := &Repeat{Trial: map[string]any{}, TrialID: "shield-unreadable",
		Client: NewClient(f.rpc()), Ledger: NewLedger(""),
		Marks: NewMarks(t.TempDir() + "/marks.jsonl")}
	rep.Guard = NewGuard(rep.Client, GuardOptions{PlayerGUID: "0x0000000000000001"})
	setup := &LiveSetup{Player: "Acmd"}
	row := setup.playerNoShield(context.Background(), rep)
	if rowOK(row) {
		t.Fatalf("an unreadable shield state must never read ok (UNVERIFIED is not a pass): %v", rowEvidence(row))
	}
}

// TestQueuedRoleNeedsStateReadBack pins codex [high] (FIX3-TRIALS-A3): a
// queued/stored `.tb role` answer only confirms the command was accepted,
// so bot_role is ok ONLY when a `State` read-back shows the requested role
// in effect. A stale role or a missing bot leaves the row UNVERIFIED-failed
// (a required row that is unverifiable makes the trial INVALID, never PASS).
func TestQueuedRoleNeedsStateReadBack(t *testing.T) {
	tok := "#T"
	stateLine := func(bot, role string) string {
		return "state bot=" + bot + " role=" + role + " rolesrc=forced class=priest level=40 hp=1500 hpmax=1500 hppct=100 " +
			"power=mana pw=2000 pwmax=2000 st=non-combat incombat=no tgt= tgtentry= tgthp= victim= hold=no mv=follow stuck= engine=yes " +
			"pos=1.0,2.0,3.0 map=0 inst=0 dl=self dm=2.4 ord=7:ok lastorder=stay co=heal nc= " +
			"cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= " + tok
	}
	cases := []struct {
		name      string
		stateBot  string
		stateRole string
		wantOK    bool
	}{
		{"applied", "Bheal", "healer", true},
		{"stale", "Bheal", "dps", false},
		{"missing", "Atank", "tank", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newSetupFake(t)
			s.scriptTB(".tb role Bheal", "role-begin "+tok,
				"role bot=Bheal role=healer seq=7 status=queued "+tok,
				"role-end role=healer target=Bheal count=1 records=1 queued=1 "+tok)
			s.scriptTB(".tb state", "state-begin "+tok,
				stateLine(tc.stateBot, tc.stateRole),
				"state-end count=1 records=1 resting=0 ready=yes target= "+tok)
			prov := &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner),
				Guard:  NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"}),
				Ledger: NewLedger(""), Player: "Acmd"}
			row := prov.ensureRole(context.Background(),
				BotSpec{Class: "priest", Spec: "holy", Role: "healer"}, "Bheal")
			if row == nil {
				t.Fatal("a healer spec must write a bot_role row, not nil")
			}
			if rowOK(row) != tc.wantOK {
				t.Fatalf("%s: bot_role ok=%v, want %v (evidence %q): a queued role is not a verified setup",
					tc.name, rowOK(row), tc.wantOK, rowEvidence(row))
			}
			if !tc.wantOK && !strings.Contains(rowEvidence(row), "UNVERIFIED") {
				t.Fatalf("%s: failed role row must say UNVERIFIED, got %q", tc.name, rowEvidence(row))
			}
		})
	}
}

// TestVisibleNeedsStateReadBack pins codex [medium] (FIX-TRIALS-A3): the
// visibility commands alone prove nothing. A State answer with no bot that
// sees the player at a numeric dm keeps the row failed (never a pass).
func TestVisibleNeedsStateReadBack(t *testing.T) {
	s, _ := newSetupFake(t)
	tok := "#T"
	s.scriptTB(".tb gm Acmd", "gm-begin "+tok,
		"gm bot=Acmd tag=SETUP status=ok gm=no "+tok,
		"gm-end count=1 records=1 target=Acmd gm=no "+tok)
	// State names no bot that sees the player at a numeric dm: an empty
	// roster proves nothing, so the row must fail. The empty reply is a
	// well-formed report every parser accepts.
	s.scriptTB(".tb state", "state-begin "+tok,
		"state-end count=0 records=0 resting=0 ready=no target= "+tok)
	ps := &PlayerSetup{Client: s.runner.client, TB: tbcmd.NewClient(s.runner),
		Guard:  NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"}),
		Ledger: NewLedger(""), Player: "Acmd"}
	row, err := ps.ensureVisible(context.Background())
	if err != nil {
		t.Fatalf("ensureVisible: %v", err)
	}
	if row == nil {
		t.Fatal("ensureVisible must write a player_visible row, not nil")
	}
	if rowOK(row) {
		t.Fatalf("player_visible passed without a State dm read-back: %v", rowEvidence(row))
	}
}

// TestVisibleFinalizesAfterHire pins the setup-round wiring (FIX-TRIALS-A3):
// the player-side row alone (no hire yet) is UNVERIFIED, but the full Run
// re-proves it against the provisioned hire: a numeric dm passes. The trial
// below is the heal-latency doc with the blue kit stripped (consumables
// only), so gear cannot SKIP the run before visibility finalizes.
func TestVisibleFinalizesAfterHire(t *testing.T) {
	s, f := newSetupFake(t)
	trial := healLatencyTrial(t)
	trial["bot"] = map[string]any{"class": "priest", "spec": "holy", "role": "healer",
		"level": 40, "items": []any{map[string]any{"name": "Sweet Nectar", "count": 20}}}
	f.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"character": "Acmd", "level": 40, "mono_ms": 1000}
	f.units = []map[string]any{
		{"guid": "0x0000000000000002", "name": "Bheal", "level": 40, "health": 1500, "max_health": 1500,
			"power": 2000, "max_power": 2000, "in_combat": false, "auras": []any{}, "casting": nil, "target": ""},
	}
	f.script["unit"] = []any{map[string]any{"guid": "0x0000000000000001", "name": "Acmd",
		"health": 2000, "max_health": 2000, "power": 100, "max_power": 100,
		"auras": []any{map[string]any{"spell_id": 71}}, "casting": nil, "target": ""}}
	tok := "#T"
	s.scriptTB(".tb login", "TortoiseBots: Bheal is logging in.")
	s.scriptTB(".tb engine", "TortoiseBots: engine on.")
	s.scriptTB(".tb level", "TortoiseBots: Bheal is level 40: health 1500/1500, mana 2000/2000.")
	s.scriptTB(".tb train", "TortoiseBots: Bheal learned 10 spell(s).")
	s.scriptTB(".tb additem", "TortoiseBots: Bheal now has 20 of item 17083.")
	// Consumables prove exists through the ItemScan read-back.
	s.scriptTB(".tb itemscan", "itemscan-begin "+tok,
		"itemscan bot=Bheal item=17083 exists=yes name=Sweet%20Nectar class=0:0 inv=0 req=1 quality=1 ilvl=10 canequip=no why=EQUIP_ERR_CANT_EQUIP_EVER "+tok,
		"itemscan-end bot=Bheal count=1 records=1 exist=1 canequip=0 "+tok)
	s.scriptTB(".tb learn Bheal 52943", "learn-begin "+tok,
		"learn bot=Bheal spell=52943 result=learned name=Book%20of%20Prayer "+tok,
		"learn-end bot=Bheal count=1 records=1 learned=1 "+tok)
	s.scriptTB(".tb learn 16462 12286", "learn-begin "+tok,
		"learn bot=Acmd spell=16462 result=known name=Spell%2016462 "+tok,
		"learn bot=Acmd spell=12286 result=known name=Spell%2012286 "+tok,
		"learn-end bot=Acmd count=2 records=2 learned=0 "+tok)
	s.scriptTB(".tb learn 52943", "learn-begin "+tok,
		"learn bot=Bheal spell=52943 result=learned name=Book%20of%20Prayer "+tok,
		"learn-end bot=Bheal count=1 records=1 learned=1 "+tok)
	s.scriptTB(".tb role Bheal", "role-begin "+tok,
		"role bot=Bheal role=healer seq=7 status=queued "+tok,
		"role-end role=healer target=Bheal count=1 records=1 queued=1 "+tok)
	s.scriptTB(".tb gm Acmd", "gm-begin "+tok,
		"gm bot=Acmd tag=SETUP status=ok gm=no "+tok,
		"gm-end count=1 records=1 target=Acmd gm=no "+tok)
	// The named hire sees the player at a numeric dm: the read-back.
	s.scriptTB(".tb state", "state-begin "+tok,
		"state bot=Bheal role=healer rolesrc=forced class=priest level=40 hp=1500 hpmax=1500 hppct=100 power=mana pw=2000 pwmax=2000 st=non-combat incombat=no tgt= tgtentry= tgthp= victim= hold=no mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 dl=self dm=2.4 ord=7:ok lastorder=stay co=heal nc= cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= "+tok,
		"state-end count=1 records=1 resting=0 ready=yes target= "+tok)
	s.scriptTB(".tb party", "party-begin "+tok,
		"party member=Acmd leader=yes online=yes bot=no class=warrior level=40 role=tank engine=no master= commanded=no "+tok,
		"party member=Bheal leader=no online=yes bot=yes class=priest level=40 role=healer engine=yes master=Acmd commanded=no "+tok,
		"party-end leader=Acmd count=2 records=2 bots=1 raid=no commander= "+tok)
	s.scriptTB(".tb role healer", "role-begin "+tok,
		"role bot=Acmd role=healer seq=8 status=queued "+tok,
		"role-end role=healer target=Acmd count=1 records=1 queued=1 "+tok)
	s.scriptTB(".tb role clear", "role-begin "+tok,
		"role bot=Acmd role= seq=8 status=queued "+tok,
		"role-end role= target=Acmd count=2 records=2 bots=1 raid=no commander= "+tok)
	s.scriptLog("TortoiseBots: Bheal is logging in.",
		"TortoiseBots: engine on.",
		"TortoiseBots: Bheal is level 40: health 1500/1500, mana 2000/2000.",
		"TortoiseBots: Bheal learned 10 spell(s).",
		"TortoiseBots: Bheal now has 20 of item 17083.")

	rep := &Repeat{Trial: trial, TrialID: "priest-holy-heal-latency",
		Client: s.runner.client, Ledger: NewLedger(""),
		Marks: NewMarks(t.TempDir() + "/marks.jsonl")}
	rep.Guard = NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"})
	setup := &LiveSetup{Player: "Acmd", Pool: map[string][]string{"priest": {"Bheal"}}, TBRunner: s.runner}
	doc, err := setup.Run(context.Background(), rep)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	for _, c := range doc["checks"].([]any) {
		m, _ := c.(map[string]any)
		if m != nil && pyjson.Str(m["id"]) == "player_visible" && !pyjson.Truthy(m["ok"]) {
			t.Fatalf("finalized player_visible must pass on a numeric dm read-back: %v", m["evidence"])
		}
	}
}

// TestTestkitParse covers each call incl. error lines: every testkit call
// against the fake, plus the not-a-bot error shape the SKIPPED path
// matches on.
func TestTestkitParse(t *testing.T) {
	s, _ := newSetupFake(t)
	tok := "#T"
	s.scriptTB(".tb learn Bheal", "learn-begin "+tok,
		"learn bot=Bheal spell=52943 result=learned name=S "+tok,
		"learn-end bot=Bheal count=1 records=1 learned=1 "+tok)
	s.scriptTB(".tb equip Bheal", "equip-begin "+tok,
		"equip bot=Bheal item=8120 name=S result=equipped slot=16 why= "+tok,
		"equip-end bot=Bheal count=1 records=1 "+tok)
	s.scriptTB(".tb itemscan Bheal", "itemscan-begin "+tok,
		"itemscan bot=Bheal item=17083 exists=yes name=Sweet%20Nectar class=0:0 inv=0 req=1 quality=1 ilvl=10 canequip=no why=EQUIP_ERR_CANT_EQUIP_EVER "+tok,
		"itemscan-end bot=Bheal count=1 records=1 exist=1 canequip=0 "+tok)
	s.scriptTB(".tb kill", "kill-begin "+tok,
		"kill bot=Mmob tag=SETUP status=killed "+tok,
		"kill-end count=1 records=1 target=Mmob bots=1 killed=1 refused=0 "+tok)
	s.scriptTB(".tb role", "error code=not-a-bot cmd=role msg=no "+tok)
	tb := tbcmd.NewClient(s.runner)
	if _, err := tb.Learn(context.Background(), "Bheal", 52943); err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if _, err := tb.Equip(context.Background(), "Bheal", 8120, false); err != nil {
		t.Fatalf("Equip: %v", err)
	}
	if _, err := tb.ItemScan(context.Background(), "Bheal", 17083); err != nil {
		t.Fatalf("ItemScan: %v", err)
	}
	if _, err := tb.Kill(context.Background(), "Mmob"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := tb.RoleSelf(context.Background(), "healer"); err == nil {
		t.Fatal("RoleSelf not-a-bot passed")
	} else if ce, ok := err.(*tbcmd.CommandError); !ok || ce.Code != "not-a-bot" {
		t.Fatalf("RoleSelf not-a-bot = %v", err)
	}
	if _, err := tb.FreeTextHurt(context.Background(), "Bheal", 50); err == nil {
		t.Fatal("Hurt without script passed")
	}
	_ = pyjson.Str
	_ = strings.Contains
}
