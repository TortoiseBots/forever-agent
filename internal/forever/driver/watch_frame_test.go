package driver

// The watch's record of the fight: the frames it takes from the module's state and threat reads, the party records
// those frames carry, and the session's combat log written beside the trace. The scripted world is the fake; no test
// here needs a server.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/trace"
)

// watchRecord is one line of the watch's trace, cut to the records these tests read.
type watchRecord struct {
	Kind   string          `json:"k"`
	T      int64           `json:"t"`
	Bots   []trace.Bot     `json:"bots"`
	Mobs   []trace.Mob     `json:"mobs"`
	Member trace.Member    `json:"-"`
	Raw    json.RawMessage `json:"-"`
}

// readTrace parses a trace file into its records, keeping each line's raw JSON for the party records.
func readTrace(t *testing.T, path string) []watchRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	defer f.Close()
	var out []watchRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		line := sc.Bytes()
		var rec watchRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("trace line %q: %v", line, err)
		}
		rec.Raw = append(json.RawMessage(nil), line...)
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("trace: %v", err)
	}
	return out
}

// newFrameWatch is the watch over the scripted module with a recorder in dir and frames every two seconds.
func newFrameWatch(t *testing.T, dir string, sc FakeRunScript) (*Run, *Fake, *[]string) {
	t.Helper()
	route := testRoute(t, oneGroupPlan())
	cfg := watchConfig()
	cfg.Out = dir
	cfg.RunID = "r-frames"
	cfg.FrameEvery = 2 * time.Second
	logs := &[]string{}
	cfg.Logf = func(format string, args ...any) {
		*logs = append(*logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
	f := NewFake(route, cfg)
	cfg.Party = f.PartyNames()
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewWatchRecorder(dir, cfg.RunID, route.Name, route.Map, 1, f.Start, nil)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	run.SetCombatLog(f)
	f.ScriptRun(sc)
	return run, f, logs
}

// TestWatchWritesFrames: a scripted 60 s run watched with a two-second frame interval writes 30 frames (the start
// and each tick), every frame carrying the party from `.tb state` and the threat mobs from `.tb threat`.
func TestWatchWritesFrames(t *testing.T) {
	dir := t.TempDir()
	run, f, logs := newFrameWatch(t, dir, FakeRunScript{
		Steps: 20, Pulls: 4, ProgressEvery: 2 * time.Second, DoneAt: 60 * time.Second, Mobs: true,
	})
	// A member with a real distance to the leader and an aura the client's model holds.
	f.bots["Watchdps1"].dlKind, f.bots["Watchdps1"].dlYards = "away", 12.5
	f.SetAuras("Watchheal", 139, 17)
	f.bots["Watchheal"].move = "stay"
	f.bots["Watchheal"].life = "alive"
	f.bots["Watchheal"].eating = true
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	if res.Trace == "" {
		t.Fatal("no trace file")
	}
	recs := readTrace(t, res.Trace)
	frames := 0
	var first *watchRecord
	parties := map[string]trace.Member{}
	mobs := map[string]trace.MobInfo{}
	for i := range recs {
		switch recs[i].Kind {
		case "frame":
			frames++
			if first == nil {
				first = &recs[i]
			}
		case "party":
			var pr trace.PartyRecord
			if err := json.Unmarshal(recs[i].Raw, &pr); err != nil {
				t.Fatalf("party record %q: %v", recs[i].Raw, err)
			}
			parties[pr.G] = pr.Member
		case "mobs":
			var mr trace.MobRecord
			if err := json.Unmarshal(recs[i].Raw, &mr); err != nil {
				t.Fatalf("mobs record %q: %v", recs[i].Raw, err)
			}
			mobs[mr.G] = mr.MobInfo
		}
	}
	// One frame per two-second tick of the 60 s run: thirty, give or take the tick the run ends on.
	if frames < 29 || frames > 31 {
		t.Fatalf("%d frames for a 60 s run at 2 s, want 30±1", frames)
	}
	if len(parties) != 5 {
		t.Fatalf("%d party records, want 5: %v", len(parties), parties)
	}
	for g, m := range parties {
		if m.Name == "" || m.Role == "" || m.Class == "" || g == "" {
			t.Fatalf("party record %+v lacks g/name/role/class", m)
		}
	}
	if first == nil || len(first.Bots) != 5 {
		t.Fatalf("first frame %+v, want five bots", first)
	}
	roles := map[string]bool{}
	for _, b := range first.Bots {
		if b.G == "" || b.Role == "" || b.Move == "" {
			t.Fatalf("bot frame %+v lacks g/role/mvm", b)
		}
		if !canonicalGUID(b.G) {
			t.Fatalf("frame identity %q is not 0x and 32 upper-case digits", b.G)
		}
		if _, ok := parties[b.G]; !ok {
			t.Fatalf("frame identity %q has no party record", b.G)
		}
		roles[b.Role] = true
	}
	if !roles["tank"] || !roles["heal"] || !roles["dps"] {
		t.Fatalf("roles %v, want tank, heal and dps", roles)
	}
	byGUID := map[string]trace.Bot{}
	for _, b := range first.Bots {
		byGUID[b.G] = b
	}
	dps := byGUID[f.guidOf["Watchdps1"].String()]
	if dl := dps.DL; dl == nil || *dl != 12.5 {
		t.Fatalf("dps distance to the leader %v, want 12.5 (%+v)", dl, dps)
	}
	h := byGUID[f.guidOf["Watchheal"].String()]
	if h.Move != "stay" || !h.Eat || h.Life != "alive" {
		t.Fatalf("healer frame %+v lacks mvm=stay, eat and life", h)
	}
	if len(h.Auras) != 2 || h.Auras[0] != 139 || h.Auras[1] != 17 {
		t.Fatalf("healer auras %v, want [139 17]", h.Auras)
	}
	if len(first.Mobs) == 0 {
		t.Fatalf("first frame has no threat mob: %+v", first)
	}
	mob := first.Mobs[0]
	if mob.G == "" || mob.HPPct != 80 {
		t.Fatalf("mob frame %+v", mob)
	}
	if mi := mobs[mob.G]; mi.Entry != 1234 || mi.Name != "mob" {
		t.Fatalf("mob registry %+v, want entry 1234", mi)
	}
	if mob.Victim == "" || len(mob.Top) == 0 || mob.Top[0].Threat != 100 {
		t.Fatalf("mob frame %+v lacks victim/top", mob)
	}
	if mob.NP || mob.X == 0 {
		t.Fatalf("mob frame %+v lacks its position", mob)
	}
	if res.Summary.Watch == nil || res.Summary.Watch.Frames != frames {
		t.Fatalf("summary frames %+v, want %d", res.Summary.Watch, frames)
	}
}

// TestWatchFramesAreReadsOnly: a frame is two reads. With frames on, the watch still sends no order and no action
// after the run start, and no `.tb` line is built in the driver (the package's own source test covers the second
// half).
func TestWatchFramesAreReadsOnly(t *testing.T) {
	dir := t.TempDir()
	run, f, _ := newFrameWatch(t, dir, FakeRunScript{
		Steps: 10, Pulls: 2, ProgressEvery: 2 * time.Second, DoneAt: 20 * time.Second, Mobs: true,
	})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	if res.Summary.Watch == nil || res.Summary.Watch.Frames == 0 {
		t.Fatal("no frames written")
	}
	started := false
	for _, o := range f.Orders() {
		if o.Verb == "order" {
			t.Fatalf("order %+v in a watch", o)
		}
		if o.Verb == "action" && strings.HasPrefix(o.Arg, "run start") {
			started = true
			continue
		}
		if started && o.Verb != "action" {
			t.Fatalf("order %+v after the run start", o)
		}
	}
	if !started {
		t.Fatal("no run start")
	}
	// The frames' reads are the ones PREPARE already makes: one Threat read per frame, and the module's answer
	// never turns into a send. A watch that sent a `.tb` order would show in the fake's order list, which is empty
	// of orders above.
	if n := f.Ordered("stay") + f.Ordered("follow"); n != 0 {
		t.Fatalf("%d movement order(s)", n)
	}
}

// TestCombatLogFile: every tick drains the session's combat log into <run_id>.combatlog.jsonl, one JSON row per
// line, in seq order and across the session's page bound.
func TestCombatLogFile(t *testing.T) {
	dir := t.TempDir()
	run, f, _ := newFrameWatch(t, dir, FakeRunScript{
		Steps: 10, Pulls: 2, ProgressEvery: 2 * time.Second, DoneAt: 20 * time.Second,
	})
	// More rows than one page holds: the drain pages until the log is caught up.
	const rows = combatPageMax + 137
	want := make([]string, 0, rows)
	for i := 1; i <= rows; i++ {
		line := fmt.Sprintf(`{"seq":%d,"kind":"melee","time":"2026-11-20T12:00:00.000Z","mono_ms":%d,`+
			`"source":{"guid":"0x0000000000000011","name":"Atank"},"target":{"guid":"0x0000000000000022","name":""},`+
			`"spell_id":0,"amount":%d}`, i, i*10, i)
		f.AddCombat(line)
		want = append(want, line)
	}
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	rec := filepath.Join(dir, "r-frames.combatlog.jsonl")
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("combat log: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(got) != rows {
		t.Fatalf("%d combat rows written, want %d", len(got), rows)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("row %d written as %q, want %q", i+1, got[i], want[i])
		}
	}
	if res.Summary.Watch == nil || res.Summary.Watch.CombatRows != rows {
		t.Fatalf("summary combat rows %+v, want %d", res.Summary.Watch, rows)
	}
}

// canonicalGUID reports whether s is the trace's one guid text: 0x and 32 upper-case hex digits, the same text a
// combat row's guid carries.
func canonicalGUID(s string) bool {
	if len(s) != 34 || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, c := range s[2:] {
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// TestBotFrameNeverUsesAName: a frame entry whose guid the model does not know carries no identity at all. The
// character name never becomes the `g` field, and an unresolved member gains its guid, and its one party record,
// only when the model knows it.
func TestBotFrameNeverUsesAName(t *testing.T) {
	fb := botFrame(tbcmd.BotState{Name: "Watchdps3", Class: "warrior"}, "", "dps", nil)
	if fb.G != "" {
		t.Fatalf("frame identity %q is not a guid", fb.G)
	}
	if fb.Role != "dps" {
		t.Fatalf("frame role %q, want the party slot's", fb.Role)
	}
}

// TestWatchFrameUnresolvedMember: a member this client's model has not resolved is a row without a guid in every
// frame, its role still naming its slot, and it writes no party record.
func TestWatchFrameUnresolvedMember(t *testing.T) {
	dir := t.TempDir()
	run, f, _ := newFrameWatch(t, dir, FakeRunScript{
		Steps: 4, Pulls: 0, ProgressEvery: 2 * time.Second, DoneAt: 6 * time.Second,
	})
	const name = "Watchdps3"
	if f.guidOf[name].IsZero() {
		t.Fatal("the fake knows the member's guid")
	}
	delete(f.guidOf, name)
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	recs := readTrace(t, res.Trace)
	frames := 0
	for _, r := range recs {
		switch r.Kind {
		case "frame":
			frames++
			if len(r.Bots) != 5 {
				t.Fatalf("frame %d has %d bots, want the party's five", r.T, len(r.Bots))
			}
			unresolved := 0
			for _, b := range r.Bots {
				if b.G == name {
					t.Fatalf("frame %d names the member by its character name", r.T)
				}
				if b.G == "" {
					unresolved++
					if b.Role == "" {
						t.Fatalf("frame %d has an unidentified bot without a role: %+v", r.T, b)
					}
					continue
				}
				if !canonicalGUID(b.G) {
					t.Fatalf("frame %d has identity %q, not 0x and 32 upper-case digits", r.T, b.G)
				}
			}
			if unresolved != 1 {
				t.Fatalf("frame %d has %d bots without a guid, want the one unresolved member", r.T, unresolved)
			}
		case "party":
			var pr trace.PartyRecord
			if err := json.Unmarshal(r.Raw, &pr); err != nil {
				t.Fatalf("party record %q: %v", r.Raw, err)
			}
			if pr.G == name || pr.G == "" {
				t.Fatalf("party record %q for the unresolved member", pr.G)
			}
			if !canonicalGUID(pr.G) {
				t.Fatalf("party record %q is not 0x and 32 upper-case digits", pr.G)
			}
		}
	}
	if frames == 0 {
		t.Fatal("no frames written")
	}
}

// TestCombatWriteFailureHoldsTheCursor: a page the file does not take leaves the cursor where it was, so the same
// rows are written once the write answers, and the failure is in the summary. The run's record never skips them.
func TestCombatWriteFailureHoldsTheCursor(t *testing.T) {
	dir := t.TempDir()
	run, f, logs := newFrameWatch(t, dir, FakeRunScript{
		Steps: 4, Pulls: 0, ProgressEvery: 2 * time.Second, DoneAt: 6 * time.Second,
	})
	for i := 1; i <= 3; i++ {
		f.AddCombat(fmt.Sprintf(`{"seq":%d,"kind":"melee","time":"2026-11-20T12:00:00.000Z","mono_ms":%d,`+
			`"source":{"guid":"0x0000000000000011","name":"Atank"},"target":{"guid":"0x0000000000000022","name":""},`+
			`"spell_id":0,"amount":%d}`, i, i*10, i))
	}
	good := run.rec.combatAt
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	run.rec.combatAt = filepath.Join(blocked, "r-frames.combatlog.jsonl")
	w := &watchRun{}
	run.drainCombat(w)
	if w.combatSeq != 0 || w.combatRows != 0 {
		t.Fatalf("cursor %d with %d rows written, want it held at 0", w.combatSeq, w.combatRows)
	}
	if w.combatErr == "" {
		t.Fatal("the write failure left no error")
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "the write failed") {
		t.Fatalf("no line for the failure; logs:\n%s", strings.Join(*logs, "\n"))
	}
	// The same rows land once the file answers, and the cursor moves through them.
	run.rec.combatAt = good
	run.drainCombat(w)
	if w.combatSeq != 3 || w.combatRows != 3 {
		t.Fatalf("after the retry: cursor %d, %d rows, want 3 and 3", w.combatSeq, w.combatRows)
	}
	if w.combatErr != "" {
		t.Fatalf("error %q kept after the write answered", w.combatErr)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "the writes answer again") {
		t.Fatalf("no line for the write answering; logs:\n%s", strings.Join(*logs, "\n"))
	}
	b, err := os.ReadFile(good)
	if err != nil {
		t.Fatalf("combat log: %v", err)
	}
	if got := strings.Count(string(b), "\n"); got != 3 {
		t.Fatalf("%d rows on disk, want 3", got)
	}
}

// TestCombatLogBoundStopsTheDrain: a page that would cross the file's bound is not written at all, the recorder says
// so, and a watch's summary carries combat_truncated with what the file holds.
func TestCombatLogBoundStopsTheDrain(t *testing.T) {
	dir := t.TempDir()
	run, _, _ := newFrameWatch(t, dir, FakeRunScript{
		Steps: 4, Pulls: 0, ProgressEvery: 2 * time.Second, DoneAt: 6 * time.Second,
	})
	row := func(i int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"seq":%d,"kind":"melee","time":"2026-11-20T12:00:00.000Z","mono_ms":%d}`,
			i, i*10))
	}
	page := []json.RawMessage{row(1), row(2)}
	var size int64
	for _, r := range page {
		size += int64(len(r) + 1)
	}
	run.rec.SetCombatMaxBytes(size)
	if _, err := run.rec.Combat(page); err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := run.rec.CombatBytes(); got != size {
		t.Fatalf("%d bytes written, want %d", got, size)
	}
	if _, err := run.rec.Combat([]json.RawMessage{row(3), row(4)}); !errors.Is(err, errCombatFull) {
		t.Fatalf("second page error %v, want errCombatFull", err)
	}
	if _, err := run.rec.Combat([]json.RawMessage{row(5)}); !errors.Is(err, errCombatFull) {
		t.Fatalf("after the bound the recorder answered %v, want errCombatFull", err)
	}
	b, err := os.ReadFile(run.rec.combatAt)
	if err != nil {
		t.Fatalf("combat log: %v", err)
	}
	if got := strings.Count(string(b), "\n"); got != 2 {
		t.Fatalf("%d rows on disk, want the two that fit", got)
	}
	if !run.rec.CombatTruncated() {
		t.Fatal("the bound did not mark the log truncated")
	}
}

// TestWatchSummarySaysTheCombatLogIsTruncated: a watch bound small enough to refuse every page ends with an empty
// combat file and the summary's truncation marker, not a file that looks complete.
func TestWatchSummarySaysTheCombatLogIsTruncated(t *testing.T) {
	dir := t.TempDir()
	run, f, _ := newFrameWatch(t, dir, FakeRunScript{
		Steps: 4, Pulls: 0, ProgressEvery: 2 * time.Second, DoneAt: 6 * time.Second,
	})
	run.rec.SetCombatMaxBytes(8)
	for i := 1; i <= 5; i++ {
		f.AddCombat(fmt.Sprintf(`{"seq":%d,"kind":"melee","time":"2026-11-20T12:00:00.000Z","mono_ms":%d}`, i, i*10))
	}
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	w := res.Summary.Watch
	if w == nil || !w.CombatTruncated || w.CombatRows != 0 || w.CombatBytes != 0 {
		t.Fatalf("summary watch %+v, want combat_truncated with no rows", w)
	}
	if _, err := os.Stat(filepath.Join(dir, "r-frames.combatlog.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a refused page left a file: %v", err)
	}
}

// TestWatchSummarySaysTheCombatLogDidNotWrite: a combat log the filesystem refuses ends with the failure in the
// summary, without the operator's path, and with no rows claimed.
func TestWatchSummarySaysTheCombatLogDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	run, f, _ := newFrameWatch(t, dir, FakeRunScript{
		Steps: 4, Pulls: 0, ProgressEvery: 2 * time.Second, DoneAt: 6 * time.Second,
	})
	for i := 1; i <= 3; i++ {
		f.AddCombat(fmt.Sprintf(`{"seq":%d,"kind":"melee","time":"2026-11-20T12:00:00.000Z","mono_ms":%d}`, i, i*10))
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	run.rec.combatAt = filepath.Join(blocked, "r-frames.combatlog.jsonl")
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	w := res.Summary.Watch
	if w == nil || w.CombatError == "" || w.CombatRows != 0 || w.CombatBytes != 0 {
		t.Fatalf("summary watch %+v, want the write error with no rows", w)
	}
	if strings.Contains(w.CombatError, dir) {
		t.Fatalf("the summary carries the operator's path: %q", w.CombatError)
	}
}

// gapCombatStep is one read of a combat log that has lost rows: the page it answers and the hole it reports above the
// cursor it was given.
type gapCombatStep struct {
	rows    []json.RawMessage
	next    int
	oldest  int
	dropped int
}

// gapCombatLog is a scripted CombatLog: each read answers the next step, and the last one repeats, so a page the
// writer retries sees what it saw before. cursors is the seq each read was given.
type gapCombatLog struct {
	steps   []gapCombatStep
	cursors []int
	calls   int
}

func (g *gapCombatLog) CombatRows(seq, limit int) ([]json.RawMessage, int, int, int) {
	s := g.steps[min(g.calls, len(g.steps)-1)]
	g.cursors = append(g.cursors, seq)
	g.calls++
	return s.rows, s.next, s.oldest, s.dropped
}

// TestCombatEachGapIsCounted: a hole the session's ring reports is added to the watch's total and logged, even when a
// later hole is smaller than an earlier one, and a page the writer refused does not count its hole twice when the
// same cursor is retried.
func TestCombatEachGapIsCounted(t *testing.T) {
	dir := t.TempDir()
	run, _, logs := newFrameWatch(t, dir, FakeRunScript{
		Steps: 4, Pulls: 0, ProgressEvery: 2 * time.Second, DoneAt: 6 * time.Second,
	})
	row := func(seq int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"seq":%d,"kind":"melee","time":"2026-11-20T12:00:00.000Z","mono_ms":%d}`,
			seq, seq*10))
	}
	// 100 rows are lost before seq 101. That first page is refused once and lands on the retry, which must not count
	// the same hole twice; 50 rows are then lost before 151, and 20 more before 173.
	cl := &gapCombatLog{steps: []gapCombatStep{
		{rows: []json.RawMessage{row(101)}, next: 101, oldest: 101, dropped: 100},
		{rows: []json.RawMessage{row(101)}, next: 101, oldest: 101, dropped: 100},
		{rows: []json.RawMessage{row(151), row(152)}, next: 152, oldest: 151, dropped: 50},
		{rows: []json.RawMessage{row(173)}, next: 173, oldest: 173, dropped: 20},
	}}
	run.SetCombatLog(cl)
	good := run.rec.combatAt
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	run.rec.combatAt = filepath.Join(blocked, "r-frames.combatlog.jsonl")
	w := &watchRun{}
	run.drainCombat(w)
	if w.combatLost != 100 {
		t.Fatalf("after the first hole the total is %d, want 100", w.combatLost)
	}
	if w.combatSeq != 0 || w.combatRows != 0 {
		t.Fatalf("after the refused page: cursor %d, %d rows, want it held at 0 with none", w.combatSeq, w.combatRows)
	}
	run.rec.combatAt = good
	run.drainCombat(w)
	run.drainCombat(w)
	run.drainCombat(w)
	if w.combatLost != 170 {
		t.Fatalf("total loss %d, want 170: a repeated or later smaller hole was not counted right", w.combatLost)
	}
	if w.combatSeq != 173 || w.combatRows != 4 {
		t.Fatalf("after the drains: cursor %d, %d rows, want 173 and 4", w.combatSeq, w.combatRows)
	}
	// The refused page is read again at the cursor it was refused at; the rest move on.
	wantSeq := []int{0, 0, 101, 152}
	if len(cl.cursors) != len(wantSeq) {
		t.Fatalf("%d reads, want %d: %v", len(cl.cursors), len(wantSeq), cl.cursors)
	}
	for i, seq := range wantSeq {
		if cl.cursors[i] != seq {
			t.Fatalf("read %d asked seq %d, want %d: %v", i, cl.cursors[i], seq, cl.cursors)
		}
	}
	want := []string{
		"watch: the combat log dropped 100 row(s) above seq 0 (oldest held 101)",
		"watch: the combat log dropped 50 row(s) above seq 101 (oldest held 151)",
		"watch: the combat log dropped 20 row(s) above seq 152 (oldest held 173)",
	}
	all := strings.Join(*logs, "\n")
	if got := strings.Count(all, "the combat log dropped"); got != len(want) {
		t.Fatalf("%d drop lines, want %d:\n%s", got, len(want), all)
	}
	for _, line := range want {
		if !strings.Contains(all, line) {
			t.Fatalf("no line %q; logs:\n%s", line, all)
		}
	}
}

// TestWatchFrameObserverOutOfView is the live watch's shape: the observer runs the watch with --enter none, so it is
// neither in the party nor in view of it, and this client's model names no member (PlayerGUID answers nothing, as on
// the server, where every member of every frame was written with "g":"" and run-map drew no member). The
// commander's binding card names each bound bot by its database counter, and with the observer's own player guid
// that is the member's full guid: every frame carries the five real guids, each with its party record, after one
// card read.
func TestWatchFrameObserverOutOfView(t *testing.T) {
	dir := t.TempDir()
	run, f, logs := newFrameWatch(t, dir, FakeRunScript{
		Steps: 6, Pulls: 1, ProgressEvery: 2 * time.Second, DoneAt: 12 * time.Second, Mobs: true,
	})
	// A ForeverCore realm's player guids: type 2 and realm 1 in the high half, the counter in the low half.
	const high = uint64(2)<<58 | uint64(1)<<42
	f.SelfGUID = wire.GUID{High: high | 0xFFFF, Low: 9} // bits below the realm are not part of a member's guid
	want := map[string]string{}
	for _, name := range f.PartyNames() {
		b := f.bots[name]
		b.guid = wire.GUID{High: high, Low: b.guid.Low}
		f.guidOf[name] = b.guid
		want[b.guid.String()] = name
	}
	f.OutOfView = true
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done; logs:\n%s", res.Stop, strings.Join(*logs, "\n"))
	}
	frames, parties := 0, map[string]bool{}
	for _, r := range readTrace(t, res.Trace) {
		switch r.Kind {
		case "frame":
			frames++
			if len(r.Bots) != 5 {
				t.Fatalf("frame %d has %d bots, want five", r.T, len(r.Bots))
			}
			for _, b := range r.Bots {
				if _, ok := want[b.G]; !ok {
					t.Fatalf("frame %d has member identity %q, want one of the party's guids %v", r.T, b.G, want)
				}
			}
			for _, m := range r.Mobs {
				if m.Victim != "" {
					if _, ok := want[m.Victim]; !ok {
						t.Fatalf("frame %d mob victim %q is not a member guid", r.T, m.Victim)
					}
				}
			}
		case "party":
			var pr trace.PartyRecord
			if err := json.Unmarshal(r.Raw, &pr); err != nil {
				t.Fatalf("party record %q: %v", r.Raw, err)
			}
			parties[pr.G] = true
		}
	}
	if frames == 0 {
		t.Fatal("no frames written")
	}
	if len(parties) != 5 {
		t.Fatalf("%d party records, want 5", len(parties))
	}
	if f.CardReads != 1 {
		t.Fatalf("%d card reads, want 1 (every member learned from the first)", f.CardReads)
	}
}

// TestWatchFrameCardReadIsBounded: a card that cannot name the members (its read fails) is read at most once per
// cardReadEvery, the failure is one line, and the members stay without a guid rather than the watch waiting.
func TestWatchFrameCardReadIsBounded(t *testing.T) {
	dir := t.TempDir()
	run, f, logs := newFrameWatch(t, dir, FakeRunScript{
		Steps: 20, Pulls: 0, ProgressEvery: 2 * time.Second, DoneAt: 70 * time.Second,
	})
	f.OutOfView = true
	f.SelfGUID = wire.GUID{High: uint64(2) << 58, Low: 9}
	f.FailCard = errors.New("no-binding")
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	if f.CardReads < 2 || f.CardReads > 4 {
		t.Fatalf("%d card reads over a 70 s watch, want one per %s", f.CardReads, cardReadEvery)
	}
	n := 0
	for _, l := range *logs {
		if strings.Contains(l, "card read failed") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d failure lines, want 1", n)
	}
}

// TestPlayerGUIDOnRealm: a member's guid is the observer's type and realm bits with the counter; a self that is not
// a player guid (not known yet) or a zero counter gives none.
func TestPlayerGUIDOnRealm(t *testing.T) {
	self := wire.GUID{High: uint64(2)<<58 | uint64(3)<<42 | 0x1234, Low: 77}
	g, ok := wire.PlayerGUIDOnRealmOf(self, 0x105)
	if !ok || g.High != uint64(2)<<58|uint64(3)<<42 || g.Low != 0x105 {
		t.Fatalf("guid %v %v", g, ok)
	}
	if _, ok := wire.PlayerGUIDOnRealmOf(wire.EmptyGUID, 0x105); ok {
		t.Fatal("an unknown self gave a guid")
	}
	if _, ok := wire.PlayerGUIDOnRealmOf(self, 0); ok {
		t.Fatal("a zero counter gave a guid")
	}
}
