package live

// The #115 extras staging: the Forever overlay has no blue gear kit for a
// level-60 bot (no Forever item source: gear_kits.json carries the priest 40
// kit only), so an extra bot asking for gear gets the kit step's SKIPPED
// needs=bot_items row. That SKIP says the overlay has no kit, not that the hire
// is missing: the main hire's bot_items row is a SKIP of its own and a trial
// that does not list it still runs. extra_bots_present is the hire's presence
// row, so a SKIPPED kit is noted there and does not fail it; a real kit
// read-back failure still does.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

func TestExtrasPresenceSurvivesASkippedGearKit(t *testing.T) {
	s, f := newSetupFake(t)
	f.units = []map[string]any{
		{"guid": "0x0000000000000004", "name": "Dru", "type": "player", "level": 60,
			"health": 3000, "max_health": 3000, "power": 2000, "max_power": 2000,
			"in_combat": false, "auras": []any{}, "casting": nil, "target": ""},
	}
	tok := "#T"
	s.scriptTB(".tb login", "TortoiseBots: Dru is logging in.")
	s.scriptTB(".tb engine", "TortoiseBots: engine on.")
	// engineOn reads State once, ensureRole once more.
	state := "state bot=Dru role=dps rolesrc=forced class=druid level=60 hp=3000 hpmax=3000 hppct=100 " +
		"power=mana pw=2000 pwmax=2000 st=non-combat incombat=no tgt= tgtentry= tgthp= victim= hold=no " +
		"mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 dl=self dm=2.4 ord=7:ok lastorder=stay " +
		"co= nc= cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no " +
		"resting=no rest_why= " + tok
	s.scriptTB(".tb state", "state-begin "+tok, state,
		"state-end count=1 records=1 resting=0 ready=yes target= "+tok)
	s.scriptTB(".tb role Dru dps", "role-begin "+tok,
		"role bot=Dru role=dps seq=7 status=queued "+tok,
		"role-end role=dps target=Dru count=1 records=1 queued=1 "+tok)
	partyN(s, 4, "Atank", "Dru")

	trial := map[string]any{"extra_bots": []any{map[string]any{
		"class": "druid", "role": "dps", "level": float64(60), "gear": "blue", "items": []any{},
	}}}
	prov := &Provisioner{Client: s.runner.client, TB: tbcmd.NewClient(s.runner),
		Guard:  NewGuard(s.runner.client, GuardOptions{PlayerGUID: gTester}),
		Ledger: NewLedger(""), Player: "Atank", Pool: map[string][]string{"druid": {"Dru"}}}
	_, rows, err := prov.ProvisionExtras(context.Background(), trial, map[string]any{"player": gTester}, nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[strOf(r["id"])] = r
	}
	present := byID["extra_bots_present"]
	if present == nil {
		t.Fatalf("no extra_bots_present row: %v", rows)
	}
	if !rowOK(present) {
		t.Fatalf("a SKIPPED gear kit failed the hire's presence: %v", present)
	}
	if ev := rowEvidence(present); !strings.Contains(ev, "needs=bot_items") {
		t.Fatalf("the presence row must name the SKIPPED kit: %q", ev)
	}
	role := byID["extra_bots_role"]
	if role == nil || !rowOK(role) {
		t.Fatalf("extra_bots_role: %v", role)
	}
}
