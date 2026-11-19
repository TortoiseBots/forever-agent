package planlint

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/planexport"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

func objectPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Name: "t", Map: 36,
		Legs: []dungeon.Leg{{
			Name: "door",
			To:   dungeon.Point{X: 0, Y: 0, Z: 0},
			Steps: []dungeon.Step{
				{UseObject: &dungeon.UseObject{ObjectRef: dungeon.ObjectRef{Entry: 17153, Near: &dungeon.Point{X: -262.7, Y: -482.4, Z: 49.4}}}},
			},
			AfterBoss: []dungeon.Step{{
				WaitFor: &dungeon.WaitFor{GameObject: &dungeon.ObjectState{
					ObjectRef: dungeon.ObjectRef{Entry: 17153, Near: &dungeon.Point{X: -290.3, Y: -537.0, Z: 49.4}, AltEntries: map[string]uint32{"forever": 16400}},
					State:     "active",
				}},
			}},
		}},
	}
}

func objectWorld() *worldspawn.World {
	return &worldspawn.World{Map: 36, Objects: []worldspawn.Object{
		{GUID: 30026183, Entry: 17153, Pos: dungeon.Point{X: -262.7, Y: -482.4, Z: 49.4}},
		{GUID: 30026185, Entry: 16400, Pos: dungeon.Point{X: -290.3, Y: -537.0, Z: 49.4}},
	}}
}

func objectRows(p *dungeon.Plan) (planexport.Rows, []planexport.RowOrigin) {
	res, err := planexport.Build(p, planexport.Options{Source: "test"})
	if err != nil {
		panic(err)
	}
	return res.Rows, res.Origins
}

// The base entries pass against the old server's objects (17153 at both
// doors); the forever server resolves the wait to its alternate entry 16400.
func TestCheckObjectsPasses(t *testing.T) {
	p := objectPlan()
	rows, origins := objectRows(p)
	old := &worldspawn.World{Map: 36, Objects: []worldspawn.Object{
		{GUID: 30026183, Entry: 17153, Pos: dungeon.Point{X: -262.7, Y: -482.4, Z: 49.4}},
		{GUID: 30026185, Entry: 17153, Pos: dungeon.Point{X: -290.3, Y: -537.0, Z: 49.4}},
	}}
	if fs := CheckObjects(rows, origins, p, old, ""); len(fs) != 0 {
		t.Fatalf("findings %v, want none", fs)
	}
	if fs := CheckObjects(rows, origins, p, objectWorld(), "forever"); len(fs) != 0 {
		t.Fatalf("findings %v, want none", fs)
	}
}

// A missing door is an error, like a missing creature pack member.
func TestCheckObjectsMissingIsAnError(t *testing.T) {
	p := objectPlan()
	rows, origins := objectRows(p)
	w := &worldspawn.World{Map: 36, Objects: []worldspawn.Object{
		{GUID: 30026183, Entry: 17153, Pos: dungeon.Point{X: -262.7, Y: -482.4, Z: 49.4}},
	}}
	fs := CheckObjects(rows, origins, p, w, "forever")
	if len(fs) != 1 || fs[0].Lint != LintObjects || fs[0].Severity != SeverityError {
		t.Fatalf("findings %v, want one objects error", fs)
	}
}

// A spawn list with no game objects fails a plan with an object step (an
// old or broken creature-only export must not approve doors unchecked) and
// passes a plan with none (Wailing Caverns has no object step). Nil is still
// nothing to judge by: no spawn list was supplied at all.
func TestCheckObjectsEmptyListFailsPlansWithObjectSteps(t *testing.T) {
	t.Run("plan with objects fails", func(t *testing.T) {
		p := objectPlan()
		rows, origins := objectRows(p)
		fs := CheckObjects(rows, origins, p, &worldspawn.World{Map: 36}, "forever")
		if len(fs) == 0 {
			t.Fatalf("no findings, want an error per object row")
		}
		for _, f := range fs {
			if f.Lint != LintObjects || f.Severity != SeverityError {
				t.Fatalf("finding %+v, want an objects error", f)
			}
		}
		if !strings.Contains(fs[0].Detail, "no game objects") {
			t.Fatalf("finding %+v names no missing object list", fs[0])
		}
	})
	t.Run("plan without objects passes", func(t *testing.T) {
		p := &dungeon.Plan{Name: "t", Map: 43,
			Legs: []dungeon.Leg{{Name: "plain", To: dungeon.Point{X: -3.64, Y: 229.27, Z: -106.19}}}}
		res, err := planexport.Build(p, planexport.Options{Source: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if fs := CheckObjects(res.Rows, res.Origins, p, &worldspawn.World{Map: 43}, "forever"); len(fs) != 0 {
			t.Fatalf("findings %v, want none", fs)
		}
	})
	t.Run("nil world skips", func(t *testing.T) {
		p := objectPlan()
		rows, origins := objectRows(p)
		if fs := CheckObjects(rows, origins, p, nil, "forever"); len(fs) != 0 {
			t.Fatalf("findings %v, want none", fs)
		}
	})
}
