package tbcmd

// The `.tb bg status` fixtures: hand-made replies in the TBM:BG / TBM:BG_BOT
// grammar the battleground module answers with (it does not exist yet, so
// this client owns the parse side). They prove the end marker, the per-side
// counters, the team and escort fields, the per-bot rows (role, node), the
// missing-block refusal and the malformed-number failure.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

type bgRunner struct {
	lines []string
}

func (r *bgRunner) Run(_ context.Context, line string, _ command.Options) (*command.Reply, error) {
	_ = line
	rep := &command.Reply{Raw: append([]string(nil), r.lines...), Lines: make([]command.ReplyLine, 0, len(r.lines)), Complete: true, End: "bg-end", Token: "t1"}
	for _, l := range r.lines {
		rl := command.ParseReplyLine(l)
		rep.Lines = append(rep.Lines, rl)
	}
	return rep, nil
}

func bgClient(lines ...string) *Client {
	c := NewClient(&bgRunner{lines: lines})
	c.Token = func() (string, error) { return "t1", nil }
	_ = strings.TrimSpace
	return c
}

func TestBGStatusFull(t *testing.T) {
	c := bgClient(
		"bg-begin #t1",
		"TBM:BG team=alliance map=wsg status=in-progress alliance_humans=1 alliance_bots=4 horde_humans=0 horde_bots=5 alliance_score=1 horde_score=0 alliance_deaths=2 horde_deaths=3 fc=1 fc_x=1539.9 fc_y=1481.4 fc_escorts=2 #t1",
		"TBM:BG_BOT role=defender objective=hold node=blacksmith alive=1 move_age=3 #t1",
		"TBM:BG_BOT role=escort objective=follow alive=1 move_age=70 #t1",
		"bg-end count=3 records=3 #t1",
	)
	out, err := c.BGStatus(context.Background())
	if err != nil {
		t.Fatalf("BGStatus: %v", err)
	}
	if out.Team != "alliance" || out.FCEscorts != 2 {
		t.Fatalf("team/escorts = %+v", out)
	}
	if out.Bots[0].Node != "blacksmith" {
		t.Fatalf("bot node = %+v", out.Bots[0])
	}
	if out.Map != "wsg" || out.Status != "in-progress" {
		t.Fatalf("head = %+v", out)
	}
	if out.AllH != 1 || out.AllB != 4 || out.HordeB != 5 {
		t.Fatalf("sides = %+v", out)
	}
	if out.ScoreA != 1 || out.DeathsH != 3 {
		t.Fatalf("score/deaths = %+v", out)
	}
	if !out.HasFC || out.FCX != 1539.9 {
		t.Fatalf("fc = %+v", out)
	}
	if len(out.Bots) != 2 || out.Bots[0].Role != "defender" || out.Bots[1].MoveAgeS != 70 {
		t.Fatalf("bots = %+v", out.Bots)
	}
}

func TestBGStatusFlags(t *testing.T) {
	rep := &command.Reply{
		Raw: []string{
			"TBM:BG map=ab deserter=yes fallback=1 fc=no #t1",
			"TBM:BG_BOT role=attacker alive=no move_age=5 #t1",
		},
		Token: "t1", Complete: true, End: "bg-end",
	}
	for _, l := range rep.Raw {
		rep.Lines = append(rep.Lines, command.ParseReplyLine(l))
	}
	out, err := parseBGStatus(rep)
	if err != nil {
		t.Fatalf("parseBGStatus: %v", err)
	}
	if !out.Deserter || !out.Fallback || out.HasFC {
		t.Fatalf("flags = %+v", out)
	}
	if out.Bots[0].Alive {
		t.Fatalf("dead bot reads alive: %+v", out.Bots[0])
	}
}

func TestBGStatusEmpty(t *testing.T) {
	rep := &command.Reply{Raw: []string{"TBM:RUN state=running"}, Token: "t1", Complete: true, End: "run-end"}
	for _, l := range rep.Raw {
		rep.Lines = append(rep.Lines, command.ParseReplyLine(l))
	}
	if _, err := parseBGStatus(rep); err == nil {
		t.Fatal("a reply with no BG block parsed")
	}
	empty := &command.Reply{Token: "t1", Complete: true, End: "bg-end"}
	if _, err := parseBGStatus(empty); err == nil {
		t.Fatal("an empty reply parsed")
	}
}

func TestBGStatusBadNumber(t *testing.T) {
	rep := &command.Reply{
		Raw:   []string{"TBM:BG map=wsg alliance_humans=many #t1"},
		Token: "t1", Complete: true, End: "bg-end",
	}
	for _, l := range rep.Raw {
		rep.Lines = append(rep.Lines, command.ParseReplyLine(l))
	}
	if _, err := parseBGStatus(rep); err == nil {
		t.Fatal("a malformed number parsed as zero")
	}
}

// The module's real `.tb bg status` rows (grammar of the battleground round, docs section 22): a config row, a maps
// row with repeated keys, an instance row, bot rows with `move_age=2s`, a queued row and the end row. No names or
// hosts: the bot names are the module's documentation names.
var bgModuleReply = []string{
	"TortoiseBots: bg: enabled=1 maps=wsg,ab fill=1 autonomous=0 min_per_team=5 max_instances=2 max_bots=50 max_minutes=45 queued=1 inside=2 events=15",
	"TortoiseBots: bg maps: wsg map=489 list=2 enabled=1 stands=2 ab map=529 list=3 enabled=1 nodes=5",
	"TortoiseBots: bg instance=3 map=wsg status=wait-join score=0-0 alliance=1h+5b horde=0h+6b",
	"TortoiseBots: bg bot=Mwarrior role=- objective=- map=wsg instance=3 team=alliance alive=1 move_age=2s",
	"TortoiseBots: bg bot=Mpriest role=defender objective=hold node=blacksmith map=wsg instance=3 team=alliance alive=0 move_age=1m10s future_key=x",
	"TortoiseBots: bg bot=Mrogue role=- objective=- map=wsg instance=4 team=horde alive=1 move_age=-",
	"TortoiseBots: bg queued bot=Mother map=wsg bracket=10-19 since=3s invited=0",
	"TortoiseBots: bg-end verb=status lines=7 #s1",
}

func TestBGStatusModuleReply(t *testing.T) {
	c := bgClient(bgModuleReply...)
	out, err := c.BGStatus(context.Background())
	if err != nil {
		t.Fatalf("BGStatus: %v", err)
	}
	if out.Map != "wsg" || out.Status != "wait-join" || out.AllH != 1 || out.AllB != 5 || out.HordeH != 0 || out.HordeB != 6 {
		t.Fatalf("instance = %+v", out)
	}
	if out.Team != "alliance" || out.ScoreA != 0 || out.ScoreH != 0 {
		t.Fatalf("team/score = %+v", out)
	}
	// Only the watcher's instance: the instance 4 bot and the queued bot are not rows.
	if len(out.Bots) != 2 {
		t.Fatalf("bots = %+v", out.Bots)
	}
	if out.Bots[0].MoveAgeS != 2 || !out.Bots[0].Alive || out.Bots[0].Role != "-" || out.Bots[0].Objective != "" {
		t.Fatalf("bot 0 = %+v", out.Bots[0])
	}
	if out.Bots[1].MoveAgeS != 70 || out.Bots[1].Alive || out.Bots[1].Node != "blacksmith" || out.Bots[1].Objective != "hold" {
		t.Fatalf("bot 1 = %+v", out.Bots[1])
	}
}

// Through the real collector: the rows have no token, every row has the module prefix, and the end row ends it.
func TestBGStatusModuleReplyThroughCollector(t *testing.T) {
	var lines []chat.Line
	for _, l := range bgModuleReply {
		lines = append(lines, chat.Line{Type: chat.TypeSystem, Text: l})
	}
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	tr := &bgTransport{lines: lines, clock: &clock}
	c := NewClient(command.NewSession(tr))
	c.Token = func() (string, error) { return "s1", nil }
	c.Base.Now = func() time.Time { return clock }
	out, err := c.BGStatus(context.Background())
	if err != nil {
		t.Fatalf("BGStatus: %v", err)
	}
	if len(out.Bots) != 2 || out.Bots[1].MoveAgeS != 70 || out.Team != "alliance" {
		t.Fatalf("report = %+v", out)
	}
}

func TestBGStatusModuleOffAndEmpty(t *testing.T) {
	off := &command.Reply{Token: "s1", Complete: true, End: "bg-end", Raw: []string{
		"TortoiseBots: bg: enabled=0 maps=- fill=0 queued=0 inside=0 events=0",
		"TortoiseBots: bg-end verb=status lines=1 #s1",
	}}
	for _, l := range off.Raw {
		off.Lines = append(off.Lines, command.ParseReplyLine(l))
	}
	if _, err := parseBGStatus(off); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a disabled module: %v", err)
	}
	// Enabled with nobody inside yet (the human is still queued): an empty, not-in-progress report.
	idle := &command.Reply{Token: "s1", Complete: true, End: "bg-end", Raw: []string{
		"TortoiseBots: bg: enabled=1 maps=wsg fill=1 queued=1 inside=0 events=1",
		"TortoiseBots: bg queued bot=Mother map=wsg bracket=10-19 since=3s invited=0",
		"TortoiseBots: bg-end verb=status lines=2 #s1",
	}}
	for _, l := range idle.Raw {
		idle.Lines = append(idle.Lines, command.ParseReplyLine(l))
	}
	out, err := parseBGStatus(idle)
	if err != nil || out.Status != "" || len(out.Bots) != 0 {
		t.Fatalf("idle = %+v err %v", out, err)
	}
}

func TestBGStatusBadModuleValues(t *testing.T) {
	for _, row := range []string{
		"bg instance=3 map=wsg status=in-progress score=x-y alliance=1h+5b horde=0h+6b",
		"bg instance=3 map=wsg status=in-progress score=0-0 alliance=many horde=0h+6b",
		"bg bot=Mwarrior role=- map=wsg instance=3 alive=1 move_age=soon",
	} {
		rep := &command.Reply{Token: "s1", Complete: true, End: "bg-end", Raw: []string{row, "bg-end verb=status lines=1 #s1"}}
		for _, l := range rep.Raw {
			rep.Lines = append(rep.Lines, command.ParseReplyLine(l))
		}
		if _, err := parseBGStatus(rep); !errors.Is(err, ErrBadValue) {
			t.Errorf("%q: err = %v, want ErrBadValue", row, err)
		}
	}
}

func TestBGAgeForms(t *testing.T) {
	for in, want := range map[string]float64{"2s": 2, "1m30s": 90, "70": 70, "2.5": 2.5, "-": 0, "": 0, "250ms": 0.25} {
		row := command.ParseReplyLine("bg bot=M move_age=" + in)
		if in == "" {
			row = command.ParseReplyLine("bg bot=M")
		}
		got, err := bgAge(row, "move_age")
		if err != nil || got != want {
			t.Errorf("move_age=%q: %v, %v (want %v)", in, got, err, want)
		}
	}
}

// bgTransport replays scripted system lines through the collector, one clock tick per line.
type bgTransport struct {
	lines []chat.Line
	clock *time.Time
}

func (t *bgTransport) SendCommand(context.Context, string) error { return nil }

func (t *bgTransport) Pump(_ context.Context, feed func(chat.Line), done func() bool) error {
	for _, l := range t.lines {
		*t.clock = t.clock.Add(100 * time.Millisecond)
		feed(l)
		if done() {
			return nil
		}
	}
	return errors.New("the script ended before the reply did")
}
