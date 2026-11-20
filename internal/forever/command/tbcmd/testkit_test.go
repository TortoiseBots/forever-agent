package tbcmd

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// learnReplies answers .tb learn lines with the doc's real records:
// one per spell, end tally learned.
func learnReplies(o command.Options, spells map[string]string, learned int, bot string) *command.Reply {
	raws := []string{"learn-begin #T"}
	for id, result := range spells {
		raws = append(raws, "learn bot="+bot+" spell="+id+" result="+result+" name=Spell%20"+id+" #T")
	}
	raws = append(raws, "learn-end bot="+bot+" count="+testkitItoa(len(spells))+" records="+testkitItoa(len(spells))+" learned="+testkitItoa(learned)+" #T")
	return reply(o, raws...)
}

func testkitItoa(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}

func TestTestkitLearn(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if !strings.HasPrefix(line, ".tb learn Bheal 2060 2050") {
			t.Fatalf("learn line = %q", line)
		}
		return learnReplies(o, map[string]string{"2060": "learned", "2050": "known"}, 1, "Bheal"), nil
	}}
	c := NewClient(r)
	rep, err := c.Learn(context.Background(), "Bheal", 2060, 2050)
	if err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if rep.Bot != "Bheal" || rep.Learned != 1 || len(rep.Results) != 2 {
		t.Fatalf("Learn: %+v", rep)
	}
	for _, res := range rep.Results {
		if res.Result != "learned" && res.Result != "known" {
			t.Fatalf("Learn result: %+v", res)
		}
	}
	// No-name form targets the sender's own character.
	r2 := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if !strings.HasPrefix(line, ".tb learn 2060") {
			t.Fatalf("learn no-name line = %q", line)
		}
		return learnReplies(o, map[string]string{"2060": "learned"}, 1, "Acmd"), nil
	}}
	c2 := NewClient(r2)
	if _, err := c2.Learn(context.Background(), "", 2060); err != nil {
		t.Fatalf("Learn no-name: %v", err)
	}
	// A not-a-bot target is the module's error line.
	r3 := oneReply("error code=not-a-bot cmd=learn msg=Somebody%20is%20not%20a%20bot #T")
	c3 := NewClient(r3)
	if _, err := c3.Learn(context.Background(), "Somebody", 2060); err == nil {
		t.Fatal("Learn not-a-bot passed")
	} else if ce, ok := err.(*CommandError); !ok || ce.Code != "not-a-bot" {
		t.Fatalf("Learn not-a-bot = %v", err)
	}
	// An unknown spell stays in the report (the caller decides): setup fails the row.
	r4 := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return learnReplies(o, map[string]string{"999999": "unknown"}, 0, "Bheal"), nil
	}}
	c4 := NewClient(r4)
	rep4, err := c4.Learn(context.Background(), "Bheal", 999999)
	if err != nil {
		t.Fatalf("Learn unknown: %v", err)
	}
	if rep4.Results[0].Result != "unknown" {
		t.Fatalf("Learn unknown: %+v", rep4.Results[0])
	}
}

func TestTestkitEquip(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if !strings.HasPrefix(line, ".tb equip Bheal 8120 replace") {
			t.Fatalf("equip line = %q", line)
		}
		return reply(o, "equip-begin #T",
			"equip bot=Bheal item=8120 name=Offhand%20thing result=equipped slot=16 why= replaced=yes #T",
			"equip-end bot=Bheal count=1 records=1 #T"), nil
	}}
	c := NewClient(r)
	rep, err := c.Equip(context.Background(), "Bheal", 8120, true)
	if err != nil {
		t.Fatalf("Equip: %v", err)
	}
	if rep.Result != "equipped" || !rep.HasSlot || rep.Slot != 16 || !rep.Replaced {
		t.Fatalf("Equip: %+v", rep)
	}
	// A refused slot answers refused with the core's reason.
	r2 := oneReply("equip-begin #T",
		"equip bot=Bheal item=8120 name=Offhand%20thing result=refused why=EQUIP_ERR_CANT_EQUIP_SKILL code=7 #T",
		"equip-end bot=Bheal count=1 records=1 #T")
	c2 := NewClient(r2)
	rep2, err := c2.Equip(context.Background(), "Bheal", 8120, false)
	if err != nil {
		t.Fatalf("Equip refused: %v", err)
	}
	if rep2.Result != "refused" || rep2.Why == "" || !rep2.HasCode {
		t.Fatalf("Equip refused: %+v", rep2)
	}
}

func TestTestkitItemScan(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if !strings.HasPrefix(line, ".tb itemscan Bheal 8120 999999") {
			t.Fatalf("itemscan line = %q", line)
		}
		return reply(o, "itemscan-begin #T",
			"itemscan bot=Bheal item=8120 exists=yes name=Offhand%20thing class=4:6 inv=14 req=40 quality=2 ilvl=50 canequip=yes slot=16 why= #T",
			"itemscan bot=Bheal item=999999 exists=no #T",
			"itemscan-end bot=Bheal count=2 records=2 exist=1 canequip=1 #T"), nil
	}}
	c := NewClient(r)
	rep, err := c.ItemScan(context.Background(), "Bheal", 8120, 999999)
	if err != nil {
		t.Fatalf("ItemScan: %v", err)
	}
	if rep.Exist != 1 || rep.CanEquip != 1 || len(rep.Results) != 2 {
		t.Fatalf("ItemScan: %+v", rep)
	}
	if !rep.Results[0].Exists || !rep.Results[0].CanEquip || !rep.Results[0].HasSlot {
		t.Fatalf("ItemScan first: %+v", rep.Results[0])
	}
	if rep.Results[1].Exists {
		t.Fatalf("ItemScan second: %+v", rep.Results[1])
	}
}

func TestTestkitKillGM(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case strings.HasPrefix(line, ".tb kill "):
			return reply(o, "kill-begin #T",
				"kill bot=Mmob tag=SETUP status=killed #T",
				"kill-end count=1 records=1 target=Mmob bots=1 killed=1 refused=0 #T"), nil
		case strings.HasPrefix(line, ".tb gm "):
			if line != ".tb gm Acmd on" {
				t.Fatalf("gm line = %q", line)
			}
			return reply(o, "gm-begin #T",
				"gm bot=Acmd tag=SETUP status=ok gm=yes #T",
				"gm-end count=1 records=1 target=Acmd gm=yes #T"), nil
		}
		t.Fatalf("unexpected line %q", line)
		return nil, nil
	}}
	c := NewClient(r)
	kill, err := c.Kill(context.Background(), "Mmob")
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if kill.Killed != 1 || kill.Results[0].Status != "killed" {
		t.Fatalf("Kill: %+v", kill)
	}
	gm, err := c.GameMaster(context.Background(), "Acmd", true)
	if err != nil {
		t.Fatalf("GM: %v", err)
	}
	if !gm.GM || gm.Status != "ok" {
		t.Fatalf("GM: %+v", gm)
	}
	// A refused kill is the module's error, not a kill.
	r2 := oneReply("kill-begin #T",
		"kill bot=Mmob tag=SETUP status=refused code=not-attackable msg=it%20is%20friendly #T",
		"kill-end count=1 records=1 target=Mmob bots=1 killed=0 refused=1 #T")
	c2 := NewClient(r2)
	if _, err := c2.Kill(context.Background(), "Mmob"); err == nil {
		t.Fatal("Kill refused passed")
	} else if ce, ok := err.(*CommandError); !ok || ce.Code != "not-attackable" {
		t.Fatalf("Kill refused = %v", err)
	}
}

func TestTestkitFreeText(t *testing.T) {
	// A ".tb hurt" acceptance is the server's "health is" line.
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if !strings.HasPrefix(line, ".tb hurt ") {
			t.Fatalf("hurt line = %q", line)
		}
		return reply(o, "TortoiseBots: Bheal health is 500/1000."), nil
	}}
	c := NewClient(r)
	if _, err := c.FreeTextHurt(context.Background(), "Bheal", 50); err != nil {
		t.Fatalf("Hurt: %v", err)
	}
	// A refusal reads like one: the caller gets *TestkitRefused.
	r2 := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, "TortoiseBots: Bheal is not a logged-in bot."), nil
	}}
	c2 := NewClient(r2)
	if _, err := c2.FreeTextHurt(context.Background(), "Bheal", 50); !IsRefusal(err) {
		t.Fatalf("Hurt refusal = %v", err)
	}
	// A level reply contradicted by the read-back is the caller's check:
	// free text alone never proves, so the acceptance line without the
	// "is level" words refuses here too.
	r3 := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, "TortoiseBots: something else."), nil
	}}
	c3 := NewClient(r3)
	if _, err := c3.FreeTextLevel(context.Background(), "Bheal", 40); !IsRefusal(err) {
		t.Fatalf("Level without acceptance = %v", err)
	}
	// A core refusal (no permission echo) surfaces as the collector error,
	// not a TestkitRefused: the caller retries or fails loud.
	r4 := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, ".tb hurt Bheal 50"), &command.RefusalError{Err: command.ErrNoPermission, Text: ".tb hurt Bheal 50"}
	}}
	c4 := NewClient(r4)
	if _, err := c4.FreeTextHurt(context.Background(), "Bheal", 50); IsRefusal(err) {
		t.Fatalf("Hurt no-permission wrapped as refusal: %v", err)
	} else if err == nil {
		t.Fatal("Hurt no-permission passed")
	}
}
