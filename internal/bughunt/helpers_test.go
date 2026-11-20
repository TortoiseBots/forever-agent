package bughunt

import "fmt"

// gid is a guid text in the trace's one form, for the fixtures' invented members.
func gid(n uint64) string { return fmt.Sprintf("0x%032X", n) }

var (
	gTank = gid(0xA01)
	gHeal = gid(0xA02)
	gDps  = gid(0xA03)
	gDps2 = gid(0xA04)
	gDps3 = gid(0xA05)
)

// testUnit is a healthy member at the origin; a test changes what its rule needs. It carries the module's meal
// report, as a watch's frame does.
func testUnit(g string) *Unit {
	return &Unit{G: g, Alive: true, HP: 100, MaxHP: 100, Power: 50, MaxPower: 100, Z: 50, HasMoving: true, HasMeal: true}
}

// frameAt builds an in-memory frame and derives its group-combat flag from the members, as the trials loader does.
func frameAt(t float64, units ...*Unit) *Frame {
	f := &Frame{T: t, Units: map[string]*Unit{}}
	for _, u := range units {
		f.addUnit(u.G, u)
		if u.Combat {
			f.GC = true
		}
	}
	return f
}

// testRun is the Python mkrun: roles and classes for the members, every subject, a fixed clock.
func testRun(kind string, roles map[string]string, frames ...*Frame) *Run {
	r := &Run{
		Kind:   kind,
		DS:     "testds",
		Name:   "testrun",
		T0:     1_700_000_000,
		Frames: frames,
		Roles:  map[string]string{}, Classes: map[string]string{}, Subjects: map[string]bool{},
		CastTimes: map[string][]float64{}, BossEntries: map[uint32]string{},
	}
	for n, role := range roles {
		r.Roles[n], r.Classes[n], r.Subjects[n] = role, "priest", true
	}
	r.finalise()
	return r
}

// allHarmful is a harmful set of the given spell ids, as a names file's harmful marks give.
func allHarmful(ids ...int) map[int]bool {
	m := map[int]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// hitsFor is the hits of one rule against one member.
func hitsFor(hits []Hit, rule int, bot string) []Hit {
	var out []Hit
	for _, h := range hits {
		if h.Rule == rule && h.Bot == bot {
			out = append(out, h)
		}
	}
	return out
}

// completeOf is the complete-rule test BuildTickets takes, from a hunt's counts.
func completeOf(c Counts) func(int) bool { return c.Complete }
