package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/bughunt"
)

// playRecordRig is a fight rig whose fake also casts (a scripted combat log) and answers `.bot who`.
func playRecordRig(t *testing.T) *playRig {
	t.Helper()
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.SpawnBosses()
		f.EmitCasts = true
	})
	rig.cfg.Out = t.TempDir()
	rig.ports.Combat = rig.fake
	return rig
}

// TestPlaySummaryCarriesPlayRecord: the run's summary says how long it took, how many lines it sent by prefix and
// that none of them was a GM line, and the budget it ran under.
func TestPlaySummaryCarriesPlayRecord(t *testing.T) {
	rig := playRecordRig(t)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	pr := res.Summary.Play
	if pr == nil {
		t.Fatal("the summary has no play record")
	}
	if pr.GMLines != 0 {
		t.Fatalf("gm_lines = %d; want 0", pr.GMLines)
	}
	if pr.LinesByPrefix[".bot add"] != 4 {
		t.Fatalf("lines_by_prefix = %v; want four .bot add", pr.LinesByPrefix)
	}
	if pr.Minutes <= 0 || pr.Minutes != res.Summary.DurationS/60 {
		t.Fatalf("minutes = %v for a run of %v s", pr.Minutes, res.Summary.DurationS)
	}
	if pr.MaxWipes != 2 || pr.MaxMinutes != 120 {
		t.Fatalf("budget = %d wipes, %d min; want 2 and the test plan's 120", pr.MaxWipes, pr.MaxMinutes)
	}
}

// TestPlayCountsLinesByPrefix: a whisper has its own prefix and a line outside the contract never counts as a bot
// line; a gm line is the only thing gm_lines counts.
func TestPlayCountsLinesByPrefix(t *testing.T) {
	by, gm := playLineCounts([]string{".bot add Botone", ".bot add Bottwo", ".bot attack Botone", "release", "corpse run", ".tb run", ".gm on"})
	if gm != 2 {
		t.Fatalf("gm = %d; want 2 (.tb and .gm)", gm)
	}
	want := map[string]int{".bot add": 2, ".bot attack": 1, "whisper release": 1, "whisper corpse run": 1, ".tb run": 1, ".gm on": 1}
	for k, v := range want {
		if by[k] != v {
			t.Fatalf("lines_by_prefix[%q] = %d; want %d (all %v)", k, by[k], v, by)
		}
	}
}

// TestPlayRecordsJudgeEvidence: what the spell judge reads is in the run's files: the four bots as party records with
// their class and level (from `.bot who`), frames with each bot alive and in combat during the fights, and the
// session's combat-log rows.
func TestPlayRecordsJudgeEvidence(t *testing.T) {
	rig := playRecordRig(t)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if !slicesContains(rig.fake.GoldenOwner.Lines(), ".bot who") {
		t.Fatalf("the run never read .bot who (lines %v)", rig.fake.GoldenOwner.Lines())
	}
	r, err := bughunt.LoadWatch(rig.cfg.Out, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(r.PartyOrder) != 4 {
		t.Fatalf("party = %v; want the four bots", r.PartyOrder)
	}
	for _, m := range r.PartyOrder {
		if r.Class(m) != "Warrior" || r.Levels[m] != 15 {
			t.Fatalf("member %s: class %q level %d; want the who row's Warrior 15", m, r.Class(m), r.Levels[m])
		}
		if strings.Contains(strings.ToLower(r.Names[m]), "bot") {
			t.Fatalf("member name %q carries a character name; want the role", r.Names[m])
		}
	}
	fought := 0
	for _, f := range r.Frames {
		for _, m := range r.PartyOrder {
			if u, ok := f.Units[m]; ok && u.Alive && u.Combat {
				fought++
			}
		}
	}
	if fought == 0 {
		t.Fatalf("no frame has a bot alive and in combat (%d frames)", len(r.Frames))
	}
	if len(r.Combat) == 0 || res.Summary.Play.CombatRows != len(r.Combat) {
		t.Fatalf("combat rows: file %d, summary %d; want them equal and non-zero", len(r.Combat), res.Summary.Play.CombatRows)
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// droppingLog is a combat log whose ring lost rows before the run read them.
type droppingLog struct{ *PlayFake }

func (d droppingLog) CombatRows(seq, limit int) ([]json.RawMessage, int, int, int) {
	rows, next, oldest, _ := d.PlayFake.CombatRows(seq, limit)
	if seq == 0 {
		return rows, next, oldest, 5 // the hole above the first cursor, reported once as the session's ring does
	}
	return rows, next, oldest, 0
}

// TestPlaySummaryCountsDroppedCombatRows: a hole the ring reports reaches the summary, so a verdict can refuse a
// log that is known to be incomplete.
func TestPlaySummaryCountsDroppedCombatRows(t *testing.T) {
	rig := playRecordRig(t)
	rig.ports.Combat = droppingLog{rig.fake}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Summary.Play == nil || res.Summary.Play.CombatDropped != 5 {
		t.Fatalf("play record = %+v; want 5 dropped rows", res.Summary.Play)
	}
}
