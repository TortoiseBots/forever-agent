package bg

// The dry scenarios for the bg-watch machine: a scripted WSG run (queue,
// invite, flag pick/drop/return/capture from world states, deserter-free end)
// and a scripted AB run (five nodes claimed, assaults, an occupation, a node
// lost), plus the card rules. The fake owns the ports' clock, so the watch's
// idle and escort rules run deterministically; the spec's budgets are the
// thresholds under test.

import (
	"strings"
	"testing"
	"time"
)

// fakePorts is the dry script: world states, status polls, chat and movement
// advance one poll per Sleep.
type fakePorts struct {
	now         time.Time
	sent        []sentPacket
	states      map[int32]int32
	pos         [][3]float32
	died        []bool
	status      []StatusReport
	invite      *ConfirmStatus
	inviteAfter int
	// inviteWindow, when non-zero, stamps the invite's Expires on its first
	// serve as the fake clock plus this window (negative = already expired).
	inviteWindow time.Duration
	served       bool
	portAt       time.Time
	activeAfter  int
	end          *WatchEnd
	endAfter     int
	script       *scriptStates
	scursor      int
	polls        int
	sendErr      error
	statusErr    error
	refused      string
}
type sentPacket struct {
	core    uint32
	payload []byte
}

func (f *fakePorts) Send(core uint32, payload []byte) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	if core == coreBattlefieldPort() {
		f.portAt = f.now
	}
	f.sent = append(f.sent, sentPacket{core, payload})
	return nil
}

func (f *fakePorts) Status() (StatusReport, error) {
	if f.statusErr != nil {
		return StatusReport{}, f.statusErr
	}
	if len(f.status) == 0 {
		return StatusReport{}, nil
	}
	s := f.status[0]
	if len(f.status) > 1 {
		f.status = f.status[1:]
	} else {
		f.status[0] = s
	}
	return s, nil
}

func (f *fakePorts) WorldStates() map[int32]int32 {
	// Consumed per poll (clamped at the end): the k-th observe poll reads
	// the k-th script entry. The accept-delay sleep reads no states, so
	// the script stays aligned with the observe polls.
	if f.script != nil {
		m := f.script.maps[f.scursor]
		if f.scursor < len(f.script.maps)-1 {
			f.scursor++
		}
		return m
	}
	return f.states
}

func (f *fakePorts) SelfPos() (float32, float32, float32) {
	if f.polls < len(f.pos) {
		return f.pos[f.polls][0], f.pos[f.polls][1], f.pos[f.polls][2]
	}
	last := f.pos[len(f.pos)-1]
	return last[0], last[1], last[2]
}

func (f *fakePorts) Died() bool {
	if f.polls < len(f.died) {
		return f.died[f.polls]
	}
	return false
}

func (f *fakePorts) PollStatus() error { return nil }

// Active serves the queue-entry confirmation: scripted to arrive after
// activeAfter polls (0 = at once) so a test can hold the join open.
func (f *fakePorts) Active() bool { return f.activeAfter == 0 || f.polls >= f.activeAfter }

func (f *fakePorts) Sleep(d time.Duration) error {
	f.now = f.now.Add(d)
	f.polls++
	return nil
}

func (f *fakePorts) Invite(time.Time) (ConfirmStatus, bool) {
	if f.invite == nil || f.polls < f.inviteAfter {
		return ConfirmStatus{}, false
	}
	if f.inviteWindow != 0 && !f.served {
		f.invite.Expires = f.now.Add(f.inviteWindow)
		f.served = true
	}
	return *f.invite, true
}

// Refused is the invite/join failure the fake is scripted to report.
func (f *fakePorts) Refused() string { return f.refused }

func (f *fakePorts) Ended() (WatchEnd, bool) {
	if f.end == nil {
		return WatchEnd{}, false
	}
	if f.polls >= f.endAfter {
		return *f.end, true
	}
	return WatchEnd{}, false
}

// scriptStates serves one world-state map per poll (index = polls).
type scriptStates struct {
	maps []map[int32]int32
}

func (f *fakePorts) useScript(maps []map[int32]int32) {
	f.script = &scriptStates{maps: maps}
}

func runWatcher(cfg WatcherConfig, f *fakePorts) Result {
	w := NewWatcher(cfg, f, nil)
	base := time.Unix(1700000000, 0)
	f.now = base
	w.now = func() time.Time { return f.now }
	res := w.Run(base.Add(time.Duration(cfg.MaxMin+5) * time.Minute))
	return res
}

func baseCfg(m Map) WatcherConfig {
	cfg := DefaultWatcherConfig()
	cfg.Map = m
	cfg.RunID = "bg-test-1"
	cfg.PollS = 5
	cfg.MaxMin = 45
	return cfg
}

func TestDryWSGFullMatch(t *testing.T) {
	// A WSG match: queue, invite, accept, flag pick/drop/return/capture from
	// 1545/1546, captures on 1581, escorted FC, alliance win. The dry fake
	// serves one world-state map per poll; the watcher derives the events.
	f := &fakePorts{
		pos:      [][3]float32{{1540, 1481, 352}, {1540, 1481, 352}, {1540, 1481, 352}, {1540, 1481, 352}, {1540, 1481, 352}, {1540, 1481, 352}},
		died:     []bool{false, false, false, false, false, false},
		invite:   &ConfirmStatus{Header: StatusHeader{Ticket: Ticket{ID: 0, Type: 1, Time: 1700000000}, QueueIDs: []uint64{PackQueueType(ListWSG)}, InstanceID: 7}, Map: MapWSG, Timeout: 100000},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 5,
		status: []StatusReport{
			{Status: "in-progress", Team: "alliance", Alliance: Side{Humans: 1, Bots: 4}, Horde: Side{Bots: 5}, Bots: []BotRow{{Role: "defender", Objective: "hold", Alive: true, MoveAgeS: 3}}, HasFC: true, FCEscorts: 2, FCX: 1539, FCY: 1481},
			{Status: "in-progress", Team: "alliance", Alliance: Side{Humans: 1, Bots: 4}, Horde: Side{Bots: 5}, Bots: []BotRow{{Role: "defender", Objective: "hold", Alive: true, MoveAgeS: 3}}, HasFC: true, FCEscorts: 2, FCX: 1539, FCY: 1481},
			{Status: "in-progress", Team: "alliance", Alliance: Side{Humans: 1, Bots: 4}, Horde: Side{Bots: 5}, Bots: []BotRow{{Role: "defender", Objective: "hold", Alive: true, MoveAgeS: 3}}, HasFC: true, FCEscorts: 2, FCX: 1539, FCY: 1481},
		},
	}
	f.useScript([]map[int32]int32{
		{WSFlagAlliance: 0, WSFlagHorde: 0, WSCapAlliance: 0, WSCapHorde: 0},
		{WSFlagAlliance: 0, WSFlagHorde: 1, WSCapAlliance: 0, WSCapHorde: 0},
		{WSFlagAlliance: 0, WSFlagHorde: -1, WSCapAlliance: 0, WSCapHorde: 0},
		{WSFlagAlliance: 0, WSFlagHorde: 0, WSCapAlliance: 0, WSCapHorde: 0},
		{WSFlagAlliance: 0, WSFlagHorde: 0, WSCapAlliance: 1, WSCapHorde: 0},
	})
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "match-end" || !res.Clear {
		t.Fatalf("stop = %q clear = %v", res.Stop, res.Clear)
	}
	if res.Record.Winner != "alliance" {
		t.Fatalf("winner = %q", res.Record.Winner)
	}
	// The three sends: battlemaster join with the packed WSG queue id, the
	// port accept with the invite's ticket, then the leave.
	if len(f.sent) != 3 {
		t.Fatalf("sent %d packets, want 3", len(f.sent))
	}
	if f.sent[0].core != 0x3E00BD {
		t.Fatalf("join core = 0x%X", f.sent[0].core)
	}
	b := f.sent[0].payload
	if len(b) < 8 {
		t.Fatalf("join payload %d bytes", len(b))
	}
	if f.sent[1].core != 0x3E00C7 {
		t.Fatalf("port core = 0x%X", f.sent[1].core)
	}
	if f.sent[2].core != 0x3D001F {
		t.Fatalf("leave core = 0x%X", f.sent[2].core)
	}
	// Flags: one pick, one drop, one return, one capture, all Horde-flag rows.
	kinds := map[EventKind]int{}
	for _, e := range res.Record.Flags {
		kinds[e.Kind]++
	}
	if kinds[EvFlagPick] != 1 || kinds[EvFlagDrop] != 1 || kinds[EvFlagReturn] != 1 || kinds[EvFlagCapture] != 1 {
		t.Fatalf("flag events = %+v", kinds)
	}
	// The card of a match with one capture and no weak spots is clean.
	if len(res.Card.Flags) != 0 {
		t.Fatalf("flags = %+v", res.Card.Flags)
	}
	if res.Card.Verdict != "CLEARED" {
		t.Fatalf("verdict = %q (%s)", res.Card.Verdict, res.Card.VerdictText)
	}
}

func TestDryWSGQuiet(t *testing.T) {
	// No capture in a full-length match: the quiet flag fires.
	f := &fakePorts{
		states:   map[int32]int32{WSFlagAlliance: 0, WSFlagHorde: 0, WSCapAlliance: 0, WSCapHorde: 0},
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 8}, Map: MapWSG},
		end:      &WatchEnd{Winner: "horde", DurationS: 1500, Text: "horde wins"},
		endAfter: 2,
		status:   []StatusReport{{Bots: []BotRow{{Role: "attacker", Alive: true, MoveAgeS: 3}}}},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	found := false
	for _, fl := range res.Card.Flags {
		if fl.ID == "quiet" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no quiet flag: %+v", res.Card.Flags)
	}
	if res.Card.Verdict != "QUIET" {
		t.Fatalf("verdict = %q", res.Card.Verdict)
	}
}

func TestDryWSGUntouched(t *testing.T) {
	// A dropped own flag the team never touches: the untouched flag fires.
	// The threshold is shrunk so the dry run stays short.
	f := &fakePorts{
		states:   map[int32]int32{WSFlagAlliance: -1, WSFlagHorde: 0},
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 9}, Map: MapWSG},
		end:      &WatchEnd{Winner: "horde", DurationS: 1200, Text: "horde wins"},
		endAfter: 4,
		status:   []StatusReport{{Status: "in-progress", Team: "alliance", Bots: []BotRow{{Role: "defender", Alive: true, MoveAgeS: 3}}}},
	}
	cfg := baseCfg(Map(MapWSG))
	cfg.TouchS = 1
	res := runWatcher(cfg, f)
	found := false
	for _, fl := range res.Card.Flags {
		if fl.ID == "untouched" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no untouched flag: record %+v flags %+v", res.Record, res.Card.Flags)
	}
}

func TestDryIdleAndEscort(t *testing.T) {
	// A bot idle 60 s and an FC escorted half the time: idle and
	// unescorted both fire.
	f := &fakePorts{
		states:   map[int32]int32{WSFlagHorde: 1, WSCapAlliance: 1},
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 10}, Map: MapWSG},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 2,
		status: []StatusReport{
			{Status: "in-progress", Team: "alliance", HasFC: true, FCEscorts: 1, FCX: 1539, FCY: 1481, Bots: []BotRow{{Role: "escort", Alive: true, MoveAgeS: 70}}},
			{Status: "in-progress", Team: "alliance", HasFC: true, FCEscorts: 1, FCX: 1539, FCY: 1481, Bots: []BotRow{{Role: "escort", Alive: true, MoveAgeS: 70}}},
		},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	ids := map[string]bool{}
	for _, fl := range res.Card.Flags {
		ids[fl.ID] = true
	}
	if !ids["idle"] {
		t.Fatalf("no idle flag: %+v", res.Card.Flags)
	}
	if !ids["unescorted"] {
		t.Fatalf("no unescorted flag (share %.2f): %+v", res.Record.EscortShare, res.Card.Flags)
	}
}

func TestDryDeserterAndPremature(t *testing.T) {
	// Deserter on a bot and a premature end both flag.
	f := &fakePorts{
		states:   map[int32]int32{},
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 11}, Map: MapWSG},
		end:      &WatchEnd{DurationS: 300, Premature: true, Text: "premature finish"},
		endAfter: 1,
		status:   []StatusReport{{Deserter: true, Bots: []BotRow{{Role: "attacker", Alive: true, MoveAgeS: 3}}}},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	ids := map[string]bool{}
	for _, fl := range res.Card.Flags {
		ids[fl.ID] = true
	}
	if !ids["deserter"] || !ids["premature"] {
		t.Fatalf("flags = %+v", res.Card.Flags)
	}
}

func TestDryABClaimAll(t *testing.T) {
	// An AB match: the five nodes claimed and then occupied, one fallback,
	// resources to the end.
	f := &fakePorts{
		pos:      [][3]float32{{1166, 1200, -56}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{Ticket: Ticket{ID: 0, Type: 1}, InstanceID: 12}, Map: MapAB},
		end:      &WatchEnd{Winner: "alliance", DurationS: 1200, Text: "alliance wins 2000"},
		endAfter: 4,
		status: []StatusReport{
			{Status: "in-progress", Team: "alliance", Fallback: false, Bots: []BotRow{{Role: "attacker", Alive: true, MoveAgeS: 3}}},
			{Status: "in-progress", Team: "alliance", Fallback: true, Bots: []BotRow{{Role: "attacker", Alive: true, MoveAgeS: 3}}},
			{Status: "in-progress", Team: "alliance", Bots: []BotRow{{Role: "defender", Alive: true, MoveAgeS: 3}}},
		},
	}
	f.useScript([]map[int32]int32{
		{},
		{1769: 1, 1784: 1, 1774: 1, 1794: 1, 1789: 1}, // claimed by the Alliance
		{1767: 1, 1782: 1, 1772: 1, 1792: 1, 1787: 1}, // occupied by the Alliance
		{1767: 1, 1782: 1, ABResourcesAlliance: 400, ABResourcesHorde: 200},
	})
	cfg := baseCfg(Map(MapAB))
	res := runWatcher(cfg, f)
	if res.Record.Assaults < 5 {
		t.Fatalf("assaults = %d events %+v", res.Record.Assaults, res.Record.Events)
	}
	if res.Record.Fallbacks != 1 {
		t.Fatalf("fallbacks = %d", res.Record.Fallbacks)
	}
	if res.Record.Occupied < 1 {
		t.Fatalf("occupied = %d", res.Record.Occupied)
	}
	// With five assaults there is no quiet flag.
	for _, fl := range res.Card.Flags {
		if fl.ID == "quiet" {
			t.Fatalf("quiet fired with %d assaults", res.Record.Assaults)
		}
	}
}

func TestCardUndefended(t *testing.T) {
	// A node lost with 0 defenders near flags.
	rec := NewRecord("x", Map(MapAB))
	rec.Clear = true
	rec.DurationS = 1200
	rec.Assaults = 3
	rec.LostNoDefend = 1
	c := Build(rec, DefaultThresholds())
	found := false
	for _, fl := range c.Flags {
		if fl.ID == "undefended" {
			found = true
		}
	}
	if !found {
		t.Fatalf("flags = %+v", c.Flags)
	}
}

func TestVerdictStopped(t *testing.T) {
	// A watch that never reached a result is STOPPED by its stop reason.
	rec := NewRecord("x", Map(MapWSG))
	rec.StopReason = "time-budget"
	c := Build(rec, DefaultThresholds())
	if c.Verdict != "STOPPED" {
		t.Fatalf("verdict = %q", c.Verdict)
	}
	if !strings.Contains(c.VerdictText, "time-budget") {
		t.Fatalf("text = %q", c.VerdictText)
	}
	lost := NewRecord("y", Map(MapWSG))
	lost.StopReason = "link-lost"
	if got := Build(lost, DefaultThresholds()).Verdict; got != "FAILED" {
		t.Fatalf("link-lost verdict = %q", got)
	}
}
