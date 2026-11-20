package live

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/trialdefs"
)

// The A3 first-failing test: the doc's real v2 records drive the fake, and
// the setup writes all 17 rows ok for priest-holy-heal-latency. The fake
// answers `bot` with the module's real records (learn/equip/itemscan/role/
// state/party/gm/kill) and `chat_log` with the free-text acceptances;
// status/units carry the read-backs (level, auras, hp, grouped).

// setupFake answers the serve methods the A3 setup uses: scripted `bot`
// lines by prefix, scripted chat_log cursors, status/units/events.
type setupFake struct {
	fake    *fakeServe
	tb      map[string][]string
	log     []chatLinePage
	pos     int
	seqBase int
	runner  *TBRunner
}

type chatLinePage struct {
	lines []ChatLine
	next  uint64
}

func newSetupFake(t *testing.T) (*setupFake, *fakeServe) {
	t.Helper()
	f := newFakeServe(t)
	s := &setupFake{fake: f, tb: map[string][]string{}}
	runner := NewTBRunner(NewClient(f.rpc()))
	runner.chatLog = s.chatLog
	runner.bot = s.bot
	s.runner = runner
	return s, f
}

func (s *setupFake) chatLog(ctx context.Context, since uint64, waitMS int) (ChatPage, error) {
	if s.pos < len(s.log) {
		page := s.log[s.pos]
		s.pos++
		return ChatPage{Lines: page.lines, Next: page.next}, nil
	}
	return ChatPage{Next: since}, nil
}

func (s *setupFake) bot(ctx context.Context, line string, waitMS int) (BotResult, error) {
	// The runner sends the line with the run token appended ("line #token"):
	// match the scripted prefix against the un-tokened line, and stamp the
	// scripted records with this call's token.
	token := ""
	if i := strings.LastIndex(line, " #"); i >= 0 {
		token, line = line[i+2:], line[:i]
	}
	prefixes := make([]string, 0, len(s.tb))
	for prefix := range s.tb {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	sort.SliceStable(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	for _, prefix := range prefixes {
		lines := s.tb[prefix]
		if strings.HasPrefix(line, prefix) {
			stamped := make([]string, 0, len(lines))
			for _, l := range lines {
				if strings.HasSuffix(l, " #T") {
					l = strings.TrimSuffix(l, " #T") + " #" + token
				}
				stamped = append(stamped, l)
			}
			return BotResult{Sent: line, Lines: stamped, Complete: true}, nil
		}
	}
	return BotResult{Sent: line, Lines: []string{}, Complete: true}, nil
}

// scriptTB queues the visible lines for a `.tb` prefix.
func (s *setupFake) scriptTB(prefix string, lines ...string) {
	s.tb[prefix] = append(s.tb[prefix], lines...)
}

func (s *setupFake) scriptLog(lines ...string) {
	var cls []ChatLine
	for i, l := range lines {
		cls = append(cls, ChatLine{Seq: uint64(s.seqBase + i + 1), Text: l})
	}
	s.log = append(s.log, chatLinePage{lines: cls, next: uint64(s.seqBase + len(lines) + 1)})
	s.seqBase += len(lines) + 10
}

// healLatencyTrial loads the trial doc as map[string]any.
func healLatencyTrial(t *testing.T) map[string]any {
	t.Helper()
	raw, err := trialdefs.Read("healers/priest-holy-heal-latency.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := pyjson.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := doc.(map[string]any)
	return m
}

func testkitToken(t *testing.T, lines ...string) []string {
	t.Helper()
	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

// TestSetupPriestHealLatencyRows wires the whole A3 setup against the fake:
// the doc's real v2 records drive the fake, and the bench rule holds end to
// end (FIX-TRIALS-A3): a setup row is ok ONLY on a server read-back. The
// `blue` bot kit has no Forever item source, so the trial cannot provision
// gear and ends SKIPPED with needs=bot_items (never PASS) — the false
// positive the codex review flagged. Consumables prove through `ItemScan`
// (exists=yes) and visibility finalizes against the hire's State dm.
func TestSetupPriestHealLatencyRows(t *testing.T) {
	s, f := newSetupFake(t)
	trial := healLatencyTrial(t)

	// status: the player at trial level, hp full.
	f.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"character": "Acmd", "level": 40, "health": 2000, "max_health": 2000,
		"power": 100, "max_power": 100, "mono_ms": 1000, "target": "",
		"x": 0, "y": 0, "z": 0}
	// units: the hire in view at trial level with full hp.
	f.units = []map[string]any{
		{"guid": "0x0000000000000002", "name": "Bheal", "level": 40, "health": 1500, "max_health": 1500,
			"power": 2000, "max_power": 2000, "in_combat": false, "auras": []any{}, "casting": nil, "target": ""},
	}
	f.script["unit"] = []any{map[string]any{"guid": "0x0000000000000001", "name": "Acmd",
		"health": 2000, "max_health": 2000, "power": 100, "max_power": 100,
		"auras": []any{map[string]any{"spell_id": 71}}, "casting": nil, "target": ""}}

	// Typed `.tb` answers (the doc's real records, token ends every line).
	tok := "#T"
	s.scriptTB(".tb login", "TortoiseBots: Bheal is logging in.")
	s.scriptTB(".tb engine", "TortoiseBots: engine on.")
	s.scriptTB(".tb level", "TortoiseBots: Bheal is level 40: health 1500/1500, mana 2000/2000.")
	s.scriptTB(".tb train", "TortoiseBots: Bheal learned 10 spell(s).")
	s.scriptTB(".tb additem", "TortoiseBots: Bheal now has 20 of item 17083.")
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
	s.scriptTB(".tb itemscan", "itemscan-begin "+tok,
		"itemscan bot=Bheal item=17083 exists=yes name=Sweet%20Nectar class=0:0 inv=0 req=1 quality=1 ilvl=10 canequip=no why=EQUIP_ERR_CANT_EQUIP_EVER "+tok,
		"itemscan-end bot=Bheal count=1 records=1 exist=1 canequip=0 "+tok)
	s.scriptTB(".tb role Bheal", "role-begin "+tok,
		"role bot=Bheal role=healer seq=7 status=queued "+tok,
		"role-end role=healer target=Bheal count=1 records=1 queued=1 "+tok)
	s.scriptTB(".tb role healer", "role-begin "+tok,
		"role bot=Acmd role=healer seq=8 status=queued "+tok,
		"role-end role=healer target=Acmd count=1 records=1 queued=1 "+tok)
	s.scriptTB(".tb role clear", "role-begin "+tok,
		"role bot=Acmd role= seq=8 status=queued "+tok,
		"role-end role= target=Acmd count=1 records=1 queued=1 "+tok)
	s.scriptTB(".tb gm Acmd", "gm-begin "+tok,
		"gm bot=Acmd tag=SETUP status=ok gm=no "+tok,
		"gm-end count=1 records=1 target=Acmd gm=no "+tok)
	s.scriptTB(".tb state", "state-begin "+tok,
		"state bot=Bheal role=healer rolesrc=forced class=priest level=40 hp=1500 hpmax=1500 hppct=100 power=mana pw=2000 pwmax=2000 st=non-combat incombat=no tgt= tgtentry= tgthp= victim= hold=no mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 dl=self dm=2.4 ord=7:ok lastorder=stay co=heal nc= cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= "+tok,
		"state-end count=1 records=1 resting=0 ready=yes target= "+tok)
	s.scriptTB(".tb party", "party-begin "+tok,
		"party member=Acmd leader=yes online=yes bot=no class=warrior level=40 role=tank engine=no master= commanded=no "+tok,
		"party member=Bheal leader=no online=yes bot=yes class=priest level=40 role=healer engine=yes master=Acmd commanded=no "+tok,
		"party-end leader=Acmd count=2 records=2 bots=1 raid=no commander= "+tok)

	// chat_log: free-text acceptances arrive as system lines.
	s.scriptLog("TortoiseBots: Bheal is logging in.",
		"TortoiseBots: engine on.",
		"TortoiseBots: Bheal is level 40: health 1500/1500, mana 2000/2000.",
		"TortoiseBots: Bheal learned 10 spell(s).",
		"TortoiseBots: Bheal now has 20 of item 17083.")

	_ = testkitToken
	_ = json.Marshal

	rep := &Repeat{Trial: trial, TrialID: "priest-holy-heal-latency",
		Client: s.runner.client, Ledger: NewLedger(""),
		Marks: NewMarks(t.TempDir() + "/marks.jsonl")}
	rep.Guard = NewGuard(s.runner.client, GuardOptions{PlayerGUID: "0x0000000000000001"})
	setup := &LiveSetup{Player: "Acmd", Pool: map[string][]string{"priest": {"Bheal"}}, Timeout: 0, TBRunner: s.runner}
	// The bot kit has no Forever item source: the trial SKIPs with
	// needs=bot_items instead of reporting a success it never read back.
	_, err := setup.Run(context.Background(), rep)
	needs, ok := IsSkipped(err)
	if !ok || needs != "bot_items" {
		t.Fatalf("IsSkipped = %q %v (err %v), want needs=bot_items", needs, ok, err)
	}
}
