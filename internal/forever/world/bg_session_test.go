package world

// Round BG-A1: the typed `.tb bg status` adapter and the BG packet
// classifier. The reply grammar (TBM:BG / TBM:BG_BOT rows) is owned by
// tbcmd.BGStatus; this file proves the adapter maps every field and the
// dispatch owns the right opcodes.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestBGStatusFromTyped(t *testing.T) {
	typed, err := parseTypedBGStatusForTest()
	if err != nil {
		t.Fatal(err)
	}
	got := bgStatusFromTyped(typed)
	if got.Map != "wsg" || got.Status != "in-progress" {
		t.Fatalf("head = %+v", got)
	}
	if got.Alliance.Humans != 1 || got.Alliance.Bots != 4 || got.Horde.Bots != 5 {
		t.Fatalf("sides = %+v/%+v", got.Alliance, got.Horde)
	}
	if got.ScoreA != 1 || got.Horde.Deaths != 3 || got.Alliance.Deaths != 2 {
		t.Fatalf("score/deaths = %+v", got)
	}
	if got.Team != "alliance" || got.FCEscorts != 2 {
		t.Fatalf("team/escorts = %+v", got)
	}
	if !got.HasFC || got.FCX != 1539.9 {
		t.Fatalf("fc = %+v", got)
	}
	if len(got.Bots) != 2 || got.Bots[0].Role != "defender" || got.Bots[1].MoveAgeS != 70 {
		t.Fatalf("bots = %+v", got.Bots)
	}
	if got.Bots[0].Node != "blacksmith" {
		t.Fatalf("bot node = %+v", got.Bots[0])
	}
	if got.Bots[0].Alive != true {
		t.Fatalf("alive = %+v", got.Bots[0])
	}
}

func parseTypedBGStatusForTest() (tbcmd.BGStatusReport, error) {
	c := tbcmd.NewClient(&bgTestRunner{})
	c.Token = func() (string, error) { return "t1", nil }
	return c.BGStatus(context.Background())
}

// bgTestRunner answers `.tb bg status` with a fixed reply.
type bgTestRunner struct{}

func (r *bgTestRunner) Run(_ context.Context, _ string, o command.Options) (*command.Reply, error) {
	rep := &command.Reply{Token: o.Token, Complete: true, End: "bg-end"}
	for _, raw := range []string{
		"bg-begin #T",
		"TBM:BG team=alliance map=wsg status=in-progress alliance_humans=1 alliance_bots=4 horde_humans=0 horde_bots=5 alliance_score=1 horde_score=0 alliance_deaths=2 horde_deaths=3 fc=1 fc_x=1539.9 fc_y=1481.4 fc_escorts=2 #T",
		"TBM:BG_BOT role=defender objective=hold node=blacksmith alive=1 move_age=3 #T",
		"TBM:BG_BOT role=escort objective=follow alive=1 move_age=70 #T",
		"bg-end count=3 records=3 #T",
	} {
		raw = strings.ReplaceAll(raw, "#T", "#"+o.Token)
		rep.Raw = append(rep.Raw, raw)
		rep.Lines = append(rep.Lines, command.ParseReplyLine(raw))
	}
	return rep, nil
}

func TestIsBGPacket(t *testing.T) {
	for _, core := range []uint32{
		wire.SMSGBattlefieldStatusQueued, wire.SMSGBattlefieldStatusNeedConfirmation,
		wire.SMSGBattlefieldStatusActive, wire.SMSGBattlefieldStatusNone,
		wire.SMSGBattlefieldStatusFailed, wire.SMSGBattlefieldList,
		wire.SMSGBattlegroundPlayerPositions, wire.SMSGUpdateCapturePoint,
		wire.SMSGCapturePointRemoved, wire.SMSGBattlegroundPlayerJoined,
		wire.SMSGBattlegroundPlayerLeft, wire.SMSGPVPMatchStatistics,
		wire.SMSGPVPMatchSetState, wire.SMSGPVPMatchComplete,
		wire.SMSGPVPMatchInitialize, wire.SMSGAreaSpiritHealerTime,
		wire.SMSGInitWorldStates, wire.SMSGUpdateWorldState,
	} {
		if !isBGPacket(core) {
			t.Fatalf("%s is not a BG packet", wire.ServerName(core))
		}
	}
	if isBGPacket(wire.SMSGChat) || isBGPacket(wire.SMSGAuraUpdate) {
		t.Fatal("chat/aura claim BG ownership")
	}
}
