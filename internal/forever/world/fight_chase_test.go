package world

// forever-agent#149: the fighter against a creature that stays where it is. The fake server is the one of
// fight_test.go (the swing tick enforces the server's reach and arc); these cases add what the live run showed: a
// fighter that is already inside the server's reach must not chase, one that is outside walks in once and stops short
// of the creature, and the creature's own step back (ChaseMovementGenerator.cpp:46-48, :111-134: a target closer than
// CONTACT_DISTANCE, 0.5 yd, makes the chasing creature move away) must never be provoked.

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// distToTarget is the fighter-to-target distance now.
func (f *fightWorld) distToTarget() float32 {
	target, _ := f.world.Object(f.target)
	tx, ty, tz := target.Position(f.now)
	mx, my, mz := f.moverPos()
	return objects.Distance(mx, my, mz, tx, ty, tz)
}

// TestAttackInsideTheServersReachDoesNotChase: the wolf stands 4.8 yd away. The server's reach is 5 yd
// (Unit::GetMeleeRange, Unit.cpp:749-753: max(reaches + 4/3, NOMINAL_MELEE_RANGE 5)), so the first swing lands from
// where the fighter stands; the old rule walked to 4.7 first and again after every drift of the wolf past 4.7.
func TestAttackInsideTheServersReachDoesNotChase(t *testing.T) {
	f := newFightWorld(t, 4.8)
	f.killAfter = 3
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed || f.rounds != 3 {
		t.Fatalf("killed %v after %d round(s), want the kill on the third", f.fight.killed, f.rounds)
	}
	if n := f.countCore(wire.CMSGMoveStartForward); n != 0 {
		t.Fatalf("the fighter chased %d time(s) a target already inside the server's reach", n)
	}
	if f.swingErrs != 0 {
		t.Fatalf("%d swing error(s)", f.swingErrs)
	}
}

// TestAttackDoesNotRechaseWhileTheWolfDriftsInsideReach: the wolf shuffles between 4 and 4.85 yd during the fight, all
// inside the server's 5 yd. No re-path: the swings keep landing.
func TestAttackDoesNotRechaseWhileTheWolfDriftsInsideReach(t *testing.T) {
	f := newFightWorld(t, 4)
	f.walkAwayAt = 1
	f.walkAwayTo = 1004.85
	f.killAfter = 4
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed || f.rounds != 4 {
		t.Fatalf("killed %v after %d round(s)", f.fight.killed, f.rounds)
	}
	if n := f.countCore(wire.CMSGMoveStartForward); n != 0 {
		t.Fatalf("the fighter re-chased %d time(s) while the target stayed in the server's reach", n)
	}
}

// TestAttackSlightlyOutOfReachApproachesOnceAndStopsShort: the wolf is 5.6 yd away, 0.6 outside the server's reach.
// One approach, ending between 3 and 4.1 yd (inside reach, not on top of the wolf), then the swing; the wolf's
// step-back rule is modelled (a fighter closer than 0.5 yd makes it back off 3 yd) and must never fire.
func TestAttackSlightlyOutOfReachApproachesOnceAndStopsShort(t *testing.T) {
	f := newFightWorld(t, 5.6)
	f.killAfter = 3
	s := f.session()
	backOffs := 0
	f.beforeWait = func(now time.Time) {
		if f.distToTarget() < 0.5 {
			backOffs++
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if !f.fight.killed {
		t.Fatal("the target did not die")
	}
	if f.legs != 1 {
		t.Fatalf("%d approach leg(s), want one", f.legs)
	}
	d := objects.Distance(f.swingFrom[0], f.swingFrom[1], f.swingFrom[2], f.swingTargetAt[0], f.swingTargetAt[1], f.swingTargetAt[2])
	if d < 3.0 || d > 4.1 {
		t.Fatalf("the first swing came from %.2f yd, want 3.0-4.1 (inside reach, short of the wolf)", d)
	}
	if backOffs != 0 {
		t.Fatalf("the fighter stood on the wolf %d time(s)", backOffs)
	}
}

// TestAttackFarTargetOneContinuousChase: a wolf 25 yd away is one approach, however many heartbeats it takes, and the
// fighter ends inside reach but not on the wolf.
func TestAttackFarTargetOneContinuousChase(t *testing.T) {
	f := newFightWorld(t, 25)
	f.killAfter = 2
	s := f.session()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Attack(ctx, f.target); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if f.legs != 1 {
		t.Fatalf("%d leg(s), want one", f.legs)
	}
	d := objects.Distance(f.swingFrom[0], f.swingFrom[1], f.swingFrom[2], f.swingTargetAt[0], f.swingTargetAt[1], f.swingTargetAt[2])
	if d < 3.0 || d > 4.1 {
		t.Fatalf("the first swing came from %.2f yd, want 3.0-4.1", d)
	}
}

// TestFightPlanUsesTheCastersSpells: a character whose book holds no warrior ability is not melee-only when it knows
// a class's ranged bolt (forever-agent#149, fight-check "melee only" with 189 known spells). The highest known rank
// of the class's first bolt is picked, from the generated spell data, and a book of other classes' or passive
// spells stays melee-only.
func TestFightPlanUsesTheCastersSpells(t *testing.T) {
	tab := foreverspells.Default()
	if !tab.Loaded() {
		t.Skip("no generated spell data in this build")
	}
	cases := []struct {
		name  string
		known []int32
		want  int32
	}{
		{"mage picks the highest Fireball", []int32{116, 133, 143, 145, 205}, 145},
		{"mage without Fireball uses Frostbolt", []int32{116, 205}, 205},
		{"warlock", []int32{686, 695}, 695},
		{"priest", []int32{585, 591, 598}, 598},
		{"shaman", []int32{403, 529}, 529},
		{"druid", []int32{5176, 5177}, 5177},
		{"hunter stays melee-only: Arcane Shot has an 8 yd minimum, the fighter closes to melee", []int32{3044}, 0},
		{"warrior keeps the melee plan", []int32{772, 1464}, 772},
		{"nothing ranged", []int32{6603}, 0},
		{"empty", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := planSpellsFor(c.known, tab)
			if plan.Hostile != c.want {
				t.Fatalf("hostile spell %d, want %d", plan.Hostile, c.want)
			}
			if c.want != 0 && plan.HostileNote == "" {
				t.Fatal("the plan has no note for its log line")
			}
		})
	}
	if plan := planSpellsFor([]int32{133}, tab); !plan.Repeat || plan.HostileWindow.Max <= 5 {
		t.Fatalf("a caster's plan is a repeated cast from range, got %+v", plan)
	}
	if plan := planSpellsFor([]int32{772}, tab); plan.Repeat {
		t.Fatalf("the warrior's plan is not a rotation: %+v", plan)
	}
}

// TestCasterPlanCastsWithoutRageAndRepeats: the plan's bolt is cast whatever the rage (a mage has none), and again
// after a success, every retry window, while the target lives.
func TestCasterPlanCastsWithoutRageAndRepeats(t *testing.T) {
	f := newFightWorld(t, 4)
	f.castStartTime = 500
	s := f.session()
	plan := spellPlan{Hostile: 133, HostileWindow: RangeWindow{0, 28}, HostileNote: "Fireball (ranged)", Repeat: true}
	hook := s.castBetweenSwings(&plan, f.target)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	link := s.transport()
	for range 3 {
		if err := link.WaitUntil(ctx, link.Now().Add(2100*time.Millisecond)); err != nil {
			t.Fatalf("wait: %v", err)
		}
		if err := hook(ctx, s); err != nil {
			t.Fatalf("the hook: %v", err)
		}
	}
	if got := f.countCore(wire.CMSGCastSpell); got != 3 {
		t.Fatalf("CMSG_CAST_SPELL sent %d times, want 3 (one per window, no rage needed)", got)
	}
	if plan.Hostile != 133 {
		t.Fatalf("a repeated plan was spent: %+v", plan)
	}
}
