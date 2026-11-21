package tbcmd

// The `.tb bg status` fixtures: hand-made replies in the TBM:BG / TBM:BG_BOT
// grammar the battleground module answers with (it does not exist yet, so
// this client owns the parse side). They prove the end marker, the per-side
// counters, the team and escort fields, the per-bot rows (role, node), the
// missing-block refusal and the malformed-number failure.

import (
	"context"
	"strings"
	"testing"

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
