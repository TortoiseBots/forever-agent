package bg

// The regression tests of the review round: every one fails on the code
// before its fix (the header bound, the invite wait, the match-complete log
// tail, the winner/duration units, the idle rules, the WSG event sources,
// the AB transition table, the stop card, the leave send, the verdict case,
// the chat text, the untouched latch).

import (
	"errors"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// hostileStatus builds a NEED_CONFIRMATION/ACTIVE packet whose header count
// is 9 with the nine ids present: a real header carries one.
func hostileStatus(t *testing.T, active bool) []byte {
	t.Helper()
	b := wire.NewWriter()
	writeTicket(b, Ticket{Requester: testGUID(0x1000), ID: 1, Type: 1, Time: 1700000000})
	b.WriteU32(9)
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU32(0)
	for range 9 {
		b.WriteU64(PackQueueType(ListWSG))
	}
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	if active {
		b.WriteU32(MapWSG)
		b.WriteU32(0)
		b.WriteU32(0)
		b.WriteU8(0)
		b.WriteBit(false)
		b.WriteBit(false)
		b.FlushBits()
	} else {
		b.WriteU32(MapWSG)
		b.WriteU32(100000)
		b.WriteU8(0)
	}
	return b.Bytes()
}

func TestStatusHeaderRefusesExtraQueueIDs(t *testing.T) {
	if _, err := ParseStatusNeedConfirmation(hostileStatus(t, false)); err == nil {
		t.Fatal("need-confirmation accepted a queue-id count past the bound")
	}
	if _, err := ParseStatusActive(hostileStatus(t, true)); err == nil {
		t.Fatal("active accepted a queue-id count past the bound")
	}
}

// TestStatusHeaderRefusesHugeCount is the hostile packet of the review: a
// count of 0xFFFFFFFF with no ids behind it must fail at once, not allocate.
func TestStatusHeaderRefusesHugeCount(t *testing.T) {
	b := wire.NewWriter()
	writeTicket(b, Ticket{ID: 1, Type: 1})
	b.WriteU32(0xFFFFFFFF)
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU32(0)
	if _, err := ParseStatusQueued(b.Bytes()); err == nil {
		t.Fatal("queued accepted the huge count")
	}
}

func TestInviteWaitsForTheStatusPolls(t *testing.T) {
	// The invite is in the third status answer: the watch must keep polling
	// and sleeping, not read the invite once and give up.
	f := &fakePorts{
		states:      map[int32]int32{WSFlagAlliance: 0, WSFlagHorde: 0},
		pos:         [][3]float32{{1540, 1481, 352}},
		died:        []bool{false},
		invite:      &ConfirmStatus{Header: StatusHeader{Ticket: Ticket{ID: 0, Type: 1, Time: 1700000000}, QueueIDs: []uint64{PackQueueType(ListWSG)}, InstanceID: 7}, Map: MapWSG},
		inviteAfter: 2,
		end:         &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter:    6,
		status:      []StatusReport{{}},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "match-end" {
		t.Fatalf("stop = %q, want match-end (the invite arrives after polls)", res.Stop)
	}
	if len(f.sent) < 2 {
		t.Fatalf("sent %d packets, want the join and the accept", len(f.sent))
	}
}

func TestMatchEndWithLogDataIsAnEnd(t *testing.T) {
	// The core always attaches the log data (Battleground.cpp:804): the head
	// must be decoded and the tail consumed, not refused.
	b := wire.NewWriter()
	b.WriteI32(1) // PVP_TEAM_ALLIANCE
	b.WriteU64(900)
	b.WriteBit(true) // log data present
	b.WriteBits(0, 2)
	b.FlushBits()
	// The log block is PVPMatchStatistics: the ratings bit, the row count,
	// the two player counts, then the rows (none here).
	b.WriteBit(false)
	b.WriteU32(0)
	b.WriteU8(0)
	b.WriteU8(0)
	m, err := ParseMatchEnd(b.Bytes())
	if err != nil {
		t.Fatalf("ParseMatchEnd: %v", err)
	}
	if m.Winner != 1 || m.Duration != 900 {
		t.Fatalf("end = %+v", m)
	}
}

func TestObserverIsNotFlaggedIdle(t *testing.T) {
	// The default role is a passive observer: standing still is the job.
	f := &fakePorts{
		states:   map[int32]int32{WSFlagAlliance: 0, WSFlagHorde: 0},
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 7}, Map: MapWSG},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 16,
		status:   []StatusReport{{Status: "in-progress"}},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	for _, fl := range res.Card.Flags {
		if fl.ID == "idle" {
			t.Fatalf("the passive observer was flagged idle: %+v", fl)
		}
	}
}

func TestBotIdleIsKeptOverTheMatch(t *testing.T) {
	// A bot idle 90 s mid-match and moving again at the next poll stays a
	// finding: the rule looks at every in-progress poll.
	f := &fakePorts{
		states:   map[int32]int32{WSFlagAlliance: 0, WSFlagHorde: 0},
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 7}, Map: MapWSG},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 4,
		status: []StatusReport{
			{Status: "in-progress", Bots: []BotRow{{Role: "attacker", Alive: true, MoveAgeS: 90}}},
			{Status: "in-progress", Bots: []BotRow{{Role: "attacker", Alive: true, MoveAgeS: 3}}},
			{Status: "in-progress", Bots: []BotRow{{Role: "attacker", Alive: true, MoveAgeS: 3}}},
		},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	found := false
	for _, fl := range res.Card.Flags {
		if fl.ID == "idle" && fl.Key == "attacker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the 90 s idle bot was missed: %+v", res.Record.Bots)
	}
}

func TestWSGCaptureCountedOnce(t *testing.T) {
	// One scoring play: the Horde flag goes carried -> base and the Alliance
	// capture counter rises in the same poll. The counter is the only source.
	f := &fakePorts{
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 7}, Map: MapWSG},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 6,
		status:   []StatusReport{{Status: "in-progress"}},
	}
	f.useScript([]map[int32]int32{
		{WSFlagAlliance: 0, WSFlagHorde: 0, WSCapAlliance: 0, WSCapHorde: 0},
		{WSFlagAlliance: 0, WSFlagHorde: 1, WSCapAlliance: 0, WSCapHorde: 0},
		{WSFlagAlliance: 0, WSFlagHorde: 0, WSCapAlliance: 1, WSCapHorde: 0},
		{WSFlagAlliance: 0, WSFlagHorde: 0, WSCapAlliance: 1, WSCapHorde: 0},
	})
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Record.Captures != 1 {
		t.Fatalf("captures = %d, want 1: %+v", res.Record.Captures, res.Record.Flags)
	}
	picks := 0
	for _, e := range res.Record.Flags {
		if e.Kind == EvFlagPick {
			picks++
		}
	}
	if picks != 1 {
		t.Fatalf("picks = %d, want 1", picks)
	}
}

func TestFlagPickCountedOnce(t *testing.T) {
	// The carried flag state stays 1 for the whole carry: one pick, not one
	// per poll.
	f := &fakePorts{
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 7}, Map: MapWSG},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 6,
		status:   []StatusReport{{Status: "in-progress"}},
	}
	f.useScript([]map[int32]int32{
		{WSFlagHorde: 0},
		{WSFlagHorde: 1},
		{WSFlagHorde: 1},
		{WSFlagHorde: 1},
	})
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	picks := 0
	for _, e := range res.Record.Events {
		if e.Kind == EvFlagPick {
			picks++
		}
	}
	if picks != 1 {
		t.Fatalf("picks = %d, want 1: %+v", picks, res.Record.Events)
	}
}

func TestUntouchedCountsEveryDrop(t *testing.T) {
	// Two separate drops of the flag, each untouched: two findings.
	f := &fakePorts{
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 7}, Map: MapWSG},
		end:      &WatchEnd{Winner: "horde", DurationS: 900, Text: "horde wins"},
		endAfter: 8,
		status:   []StatusReport{{Status: "in-progress", Team: "horde"}},
	}
	f.useScript([]map[int32]int32{
		{WSFlagHorde: -1},
		{WSFlagHorde: -1},
		{WSFlagHorde: 0},
		{WSFlagHorde: -1},
		{WSFlagHorde: -1},
	})
	cfg := baseCfg(Map(MapWSG))
	cfg.TouchS = 1
	res := runWatcher(cfg, f)
	if res.Record.TouchedDropped != 2 {
		t.Fatalf("touched_dropped = %d, want 2", res.Record.TouchedDropped)
	}
}

func TestStopReasonAndCardOnSendFailure(t *testing.T) {
	f := &fakePorts{sendErr: errors.New("closed")}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "link-lost" {
		t.Fatalf("stop = %q", res.Stop)
	}
	if res.Record.StopReason != "link-lost" {
		t.Fatalf("record stop reason = %q, want link-lost", res.Record.StopReason)
	}
	if res.Card.Verdict != "FAILED" {
		t.Fatalf("card verdict = %q (%s), want FAILED", res.Card.Verdict, res.Card.VerdictText)
	}
}

func TestStatusRefusedIsNotALostLink(t *testing.T) {
	// A server without the module refuses the status poll: the stop reason
	// says so, and the card is STOPPED, not FAILED.
	f := &fakePorts{
		statusErr: ErrStatusRefused,
		invite:    &ConfirmStatus{Header: StatusHeader{InstanceID: 7}, Map: MapWSG},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "status-refused" {
		t.Fatalf("stop = %q, want status-refused", res.Stop)
	}
	if res.Card.Verdict != "STOPPED" {
		t.Fatalf("verdict = %q, want STOPPED", res.Card.Verdict)
	}
}

func TestQueueRefusalStopsWithItsReason(t *testing.T) {
	// A refused join (deserter, out of bracket) stops at once and keeps the
	// server's reason in the record.
	f := &fakePorts{refused: "the server refused the join (reason 2)"}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "queue-refused: the server refused the join (reason 2)" {
		t.Fatalf("stop = %q", res.Stop)
	}
	if res.Record.StopReason != res.Stop {
		t.Fatalf("record stop reason = %q", res.Record.StopReason)
	}
	if res.Card.Verdict != "STOPPED" {
		t.Fatalf("verdict = %q", res.Card.Verdict)
	}
}

func TestLeaveSentAfterMatchEnd(t *testing.T) {
	f := &fakePorts{
		states:   map[int32]int32{WSFlagAlliance: 0, WSFlagHorde: 0},
		pos:      [][3]float32{{1540, 1481, 352}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 7}, Map: MapWSG},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 2,
		status:   []StatusReport{{Status: "in-progress"}},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if len(f.sent) != 3 {
		t.Fatalf("sent %d packets, want join, port and leave", len(f.sent))
	}
	if f.sent[2].core != wire.CMSGBattlefieldLeave {
		t.Fatalf("third send core = 0x%X, want the leave", f.sent[2].core)
	}
	found := false
	for _, e := range res.Record.Events {
		if e.Kind == EvLeave {
			found = true
		}
	}
	if !found {
		t.Fatalf("no leave event: %+v", res.Record.Events)
	}
}

func TestFlagVerdictIsUpperCase(t *testing.T) {
	rec := NewRecord("x", Map(MapWSG))
	rec.Clear = true
	rec.DurationS = 900
	c := Build(rec, DefaultThresholds())
	if c.Verdict != "QUIET" {
		t.Fatalf("verdict = %q, want QUIET", c.Verdict)
	}
}

func TestABTransitionsFromTheCoreTable(t *testing.T) {
	// neutral -> contested: assault; contested -> same-side occupied: the
	// timer's occupation; occupied -> enemy contested: assault; enemy
	// contested -> the original owner's occupied: defend; contested A ->
	// contested H: a re-assault. The world-state offsets are
	// StateWorldStateOffset (battleground_arathi_basin.cpp:718). The loss
	// count waits for the enemy's occupation: the node assaulted at poll 4
	// and defended back at poll 5 was never lost, and only the Horde
	// occupation at the end (no bot of the team at the stables) counts.
	f := &fakePorts{
		pos:      [][3]float32{{1166, 1200, -56}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 12}, Map: MapAB},
		end:      &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter: 8,
		status:   []StatusReport{{Status: "in-progress", Team: "alliance"}},
	}
	f.useScript([]map[int32]int32{
		{},
		{1767 + 2: 1}, // stables contested Alliance
		{1767: 1},     // timer: occupied Alliance
		{1767 + 3: 1}, // contested Horde
		{1767: 1},     // defended back to Alliance
		{1767 + 2: 1}, // contested Alliance again
		{1767 + 3: 1}, // re-assault to Horde
		{1767 + 1: 1}, // Horde occupies: the loss
	})
	cfg := baseCfg(Map(MapAB))
	res := runWatcher(cfg, f)
	var kinds []EventKind
	for _, e := range res.Record.Nodes {
		if e.Node == "stables" {
			kinds = append(kinds, e.Kind)
		}
	}
	want := []EventKind{EvNodeAssault, EvNodeOccupied, EvNodeAssault, EvNodeDefend, EvNodeAssault, EvNodeAssault, EvNodeOccupied}
	if len(kinds) != len(want) {
		t.Fatalf("stables events = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("stables events = %v, want %v", kinds, want)
		}
	}
	if res.Record.LostNoDefend != 1 {
		t.Fatalf("lost_no_defend = %d, want 1 (the Horde occupation with no bot of the team at the stables)", res.Record.LostNoDefend)
	}
}

func TestABFirstSnapshotIsABaseline(t *testing.T) {
	// A match loaded mid-flight already shows contested and occupied nodes:
	// the first snapshot of each is the baseline, and no event is derived
	// from it. Only the blacksmith's later timer occupation is a change.
	f := &fakePorts{
		pos:      [][3]float32{{1166, 1200, -56}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 13}, Map: MapAB},
		end:      &WatchEnd{Winner: "alliance", DurationS: 600, Text: "alliance wins"},
		endAfter: 3,
		status:   []StatusReport{{Status: "in-progress", Team: "alliance"}},
	}
	f.useScript([]map[int32]int32{
		{1767: 1, 1782 + 2: 1}, // baseline: stables occupied, blacksmith contested
		{1767: 1, 1782: 1},     // blacksmith timer: occupied
		{1767: 1, 1782: 1},
	})
	cfg := baseCfg(Map(MapAB))
	res := runWatcher(cfg, f)
	if len(res.Record.Nodes) != 1 {
		t.Fatalf("node events = %+v, want only the blacksmith occupation", res.Record.Nodes)
	}
	e := res.Record.Nodes[0]
	if e.Kind != EvNodeOccupied || e.Node != "blacksmith" {
		t.Fatalf("node event = %+v, want the blacksmith occupation", e)
	}
}

func TestDefendedNodeIsNotLost(t *testing.T) {
	// A node the enemy assaults and the team defends back was never lost:
	// the undefended count waits for the enemy's occupation.
	f := &fakePorts{
		pos:      [][3]float32{{1166, 1200, -56}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 17}, Map: MapAB},
		end:      &WatchEnd{Winner: "alliance", DurationS: 600, Text: "alliance wins"},
		endAfter: 3,
		status:   []StatusReport{{Status: "in-progress", Team: "alliance"}},
	}
	f.useScript([]map[int32]int32{
		{1767: 1},     // baseline: stables held by the Alliance
		{1767 + 3: 1}, // the Horde assaults it
		{1767: 1},     // defended back
	})
	cfg := baseCfg(Map(MapAB))
	res := runWatcher(cfg, f)
	if res.Record.LostNoDefend != 0 {
		t.Fatalf("lost_no_defend = %d, want 0 (the node was defended back)", res.Record.LostNoDefend)
	}
}

func TestResultlessEndNamesNoWinner(t *testing.T) {
	// A complete-state initialize end has no result packet, so the last
	// score sample must not be used to name a winner.
	f := &fakePorts{
		states:   map[int32]int32{ABResourcesAlliance: 1600, ABResourcesHorde: 400},
		pos:      [][3]float32{{1166, 1200, -56}},
		died:     []bool{false},
		invite:   &ConfirmStatus{Header: StatusHeader{InstanceID: 18}, Map: MapAB},
		end:      &WatchEnd{DurationS: 1200, NoResult: true, Text: "match complete (initialize state, no result packet)"},
		endAfter: 2,
		status:   []StatusReport{{Status: "in-progress", Team: "alliance"}},
	}
	cfg := baseCfg(Map(MapAB))
	res := runWatcher(cfg, f)
	if !res.Clear {
		t.Fatalf("stop = %q, want a match end", res.Stop)
	}
	if res.Record.Winner != "" {
		t.Fatalf("winner = %q, want none from a result-less end", res.Record.Winner)
	}
	if res.Record.DurationS != 1200 {
		t.Fatalf("duration = %v, want the initialize state's 1200 s", res.Record.DurationS)
	}
}

func TestCardNotesTheTeamDependentRules(t *testing.T) {
	// Without the module's team the untouched-flag and undefended-node
	// rules cannot run: the card says so instead of passing silently.
	rec := NewRecord("x", Map(MapWSG))
	rec.Clear = true
	rec.DurationS = 900
	if len(Build(rec, DefaultThresholds()).Notes) == 0 {
		t.Fatal("a card without a team carried no note")
	}
	rec.Team = "alliance"
	if notes := Build(rec, DefaultThresholds()).Notes; len(notes) != 0 {
		t.Fatalf("a card with a team carried notes: %v", notes)
	}
}

func TestLateInviteIsAcceptedInsideItsWindow(t *testing.T) {
	// An invite that arrives with little window left is accepted before it
	// closes, not after the full human delay: the port packet goes out
	// inside the 1.5 s that is left.
	f := &fakePorts{
		states:       map[int32]int32{WSFlagAlliance: 0, WSFlagHorde: 0},
		pos:          [][3]float32{{1540, 1481, 352}},
		died:         []bool{false},
		invite:       &ConfirmStatus{Header: StatusHeader{InstanceID: 14}, Map: MapWSG},
		inviteWindow: 1500 * time.Millisecond,
		end:          &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter:     2,
		status:       []StatusReport{{Status: "in-progress"}},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "match-end" {
		t.Fatalf("stop = %q", res.Stop)
	}
	if f.portAt.IsZero() {
		t.Fatal("the accept was never sent")
	}
	if waited := f.portAt.Sub(time.Unix(1700000000, 0)); waited > 1500*time.Millisecond {
		t.Fatalf("the accept went out %v after the invite, past its 1.5 s window", waited)
	}
}

func TestExpiredInviteIsNotAccepted(t *testing.T) {
	// An invite whose window has closed is an expiry: the accept is not
	// sent and the run stops with that reason, not after the queue budget.
	f := &fakePorts{
		states:       map[int32]int32{},
		pos:          [][3]float32{{1540, 1481, 352}},
		died:         []bool{false},
		invite:       &ConfirmStatus{Header: StatusHeader{InstanceID: 15}, Map: MapWSG},
		inviteWindow: -time.Second,
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "invite-expired" {
		t.Fatalf("stop = %q, want invite-expired", res.Stop)
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent %d packets, want only the join", len(f.sent))
	}
}

func TestJoinNeedsTheActiveStatus(t *testing.T) {
	// Sending the accept is not entering: without the server's ACTIVE the
	// run stops as invite-expired and no join is recorded.
	f := &fakePorts{
		states:      map[int32]int32{},
		pos:         [][3]float32{{1540, 1481, 352}},
		died:        []bool{false},
		invite:      &ConfirmStatus{Header: StatusHeader{InstanceID: 16}, Map: MapWSG},
		activeAfter: 1 << 20,
		end:         &WatchEnd{Winner: "alliance", DurationS: 900, Text: "alliance wins"},
		endAfter:    1,
		status:      []StatusReport{{Status: "in-progress"}},
	}
	cfg := baseCfg(Map(MapWSG))
	res := runWatcher(cfg, f)
	if res.Stop != "invite-expired" {
		t.Fatalf("stop = %q, want invite-expired", res.Stop)
	}
	for _, e := range res.Record.Events {
		if e.Kind == EvJoin {
			t.Fatalf("a join was recorded without ACTIVE: %+v", e)
		}
	}
}

func TestAcceptDelayIsARandomTwoToEight(t *testing.T) {
	w := NewWatcher(DefaultWatcherConfig(), &fakePorts{}, nil)
	seen := map[time.Duration]bool{}
	for range 32 {
		d := w.acceptDelay()
		if d < 2*time.Second || d > 8*time.Second {
			t.Fatalf("accept delay = %v, want 2-8 s", d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatalf("accept delay is constant at %v", seen)
	}
}
