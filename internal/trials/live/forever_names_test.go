package live

// The Forever serve's names for the same things the bench read from the turtle agent: the status state of a session in
// the world, and the core's answer to a `.learn` of a spell the character already has (live dummy bench).

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// TestCollectorFollowsTheSessionState drives the collector through a whole fight on each status state a serve can
// report: the turtle agent's in_world and a Forever serve's online keep the fight going to its duration; a session that
// is reconnecting or disconnected ends it as disconnected, naming the state.
func TestCollectorFollowsTheSessionState(t *testing.T) {
	for state, want := range map[string]string{
		"in_world": "duration", "online": "duration", "reconnecting": "disconnected", "disconnected": "disconnected",
	} {
		t.Run(state, func(t *testing.T) {
			h := newDummyHarness(t, 1)
			inner := h.world.status
			h.fake.statusFn = func() map[string]any {
				st := inner()
				st["state"] = state
				return st
			}
			h.sit.Now = jumpClock(700 * time.Millisecond)
			out, err := h.run(nil)
			if err != nil {
				t.Fatalf("situation: %v", err)
			}
			if got := strOf(out["ended"]); got != want {
				t.Fatalf("state %q ended %q, want %q (outcome %v)", state, got, want, out)
			}
			if want == "disconnected" && !strings.Contains(strOf(out["invalid_reason"]), state) {
				t.Fatalf("the reason %q does not name the state %q", out["invalid_reason"], state)
			}
		})
	}
}

// TestLearnOfAKnownSpellIsSuccess is the rerun finding: `.learn` of a spell the character already has is answered with
// the core's error text, and the setup took it for a refusal. The spell is known, which is what the line is for; any
// other refusal is still one.
func TestLearnOfAKnownSpellIsSuccess(t *testing.T) {
	h := newDummyHarness(t, 1)
	h.fake.errCodes["gm"] = []int{-32003}
	h.fake.errMsg["gm"] = "command: the server answered a command that prints nothing when it works: You already know that spell."
	if err := h.rep.Guard.Send(context.Background(), "learn", TargetSelf, gTester, "16462"); err != nil {
		t.Fatalf("learn of a known spell: %v", err)
	}
	h.fake.errCodes["gm"] = []int{-32003}
	h.fake.errMsg["gm"] = "command: Spell 99999999 not found."
	err := h.rep.Guard.Send(context.Background(), "learn", TargetSelf, gTester, "16463")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("a refusal that is not 'already know' passed: %v", err)
	}
}

// TestHireEngineIsProvedByTheBotsState is the bench's engine finding: a hire whose engine is off never attacks (its
// attack order is refused no-engine), and the bench only logged that. After the hires stand in the arena the bench
// sets the engine on and reads each bot's own `.tb state`: engine=yes passes, engine=no / stuck=noengine ends the repeat
// INVALID naming what the module reported.
func TestHireEngineIsProvedByTheBotsState(t *testing.T) {
	for _, tc := range []struct {
		name, flags string
		wantWhy     bool
	}{
		{"engine on", "stuck= engine=yes", false},
		{"no engine", "stuck=noengine engine=no", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newSetupFake(t)
			var sent []string
			s.tbFn = func(line string) []string {
				sent = append(sent, line)
				switch {
				case strings.HasPrefix(line, ".tb engine"):
					return []string{"TortoiseBots: Bmage engine on."}
				case strings.HasPrefix(line, ".tb state"):
					return []string{"state-begin #T",
						"state bot=Bmage role=dps rolesrc=forced class=mage level=40 hp=1500 hpmax=1500 hppct=100 " +
							"power=mana pw=2000 pwmax=2000 st=non-combat incombat=no tgt= tgtentry= tgthp= victim= hold=no mv=follow " + tc.flags +
							" pos=1.0,2.0,3.0 map=0 inst=0 dl=self dm=2.4 ord= lastorder= co= nc= " +
							"cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= #T",
						"state-end count=1 records=1 resting=0 ready=yes target= #T"}
				}
				return nil
			}
			dir := t.TempDir()
			marks := NewMarks(filepath.Join(dir, "marks.jsonl"))
			marks.Start("")
			sit := &LiveSituation{Now: jumpClock(5 * time.Second), Log: func(line string) { t.Log(line) }}
			rep := &Repeat{BotName: "Bmage", Marks: marks}
			why := ensureHireEngines(context.Background(), tbcmd.NewClient(s.runner), rep, sit)
			if (why != "") != tc.wantWhy {
				t.Fatalf("reason %q, want a reason: %v", why, tc.wantWhy)
			}
			if tc.wantWhy && !strings.Contains(why, "needs=bot_engine") {
				t.Fatalf("the reason %q does not name the missing engine", why)
			}
			if len(sent) == 0 || !strings.HasPrefix(sent[0], ".tb engine Bmage on") {
				t.Fatalf("the engine was not asked on first: %q", sent)
			}
		})
	}
}

// TestEverySituationProvesItsHiresEngine: the dummy, the tank (cc_target) and the out-of-combat situations all stop the
// repeat as no_engine, naming the reason, when a hire's engine is off after the move into the arena; none of them
// carries on into a fight nobody fights.
func TestEverySituationProvesItsHiresEngine(t *testing.T) {
	for _, kind := range []string{"cc_target", "ooc_buffs", "ooc_hurt"} {
		t.Run(kind, func(t *testing.T) {
			fake := newFakeServe(t)
			hire := "0x0000000000000002"
			fake.unitsFn = func() []map[string]any {
				return []map[string]any{{"guid": hire, "name": "Bheal", "type": "player", "distance": float64(2),
					"level": float64(40), "max_power": float64(2000), "health": float64(1000), "max_health": float64(1000)}}
			}
			trial := trialA5(kind, 1, map[string]any{"ooc_wait_s": float64(0)})
			rep, _, _ := newRepA5(t, fake, trial, "0x0000000000000001", hire)
			rep.BotName = "Bheal"
			called := 0
			proveHireEngines = func(context.Context, *tbcmd.Client, *Repeat, *LiveSituation) string {
				called++
				return "SKIPPED needs=bot_engine: Bheal has no engine"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
				Log: func(line string) { t.Log(line) }}
			out, err := sit.Run(ctx, rep)
			if err != nil {
				t.Fatalf("situation: %v", err)
			}
			if called != 1 || strOf(out["ended"]) != "no_engine" || !strings.Contains(strOf(out["invalid_reason"]), "needs=bot_engine") {
				t.Fatalf("%s: engine proof called %d times, outcome %v", kind, called, out)
			}
		})
	}
}
