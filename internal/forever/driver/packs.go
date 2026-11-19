package driver

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Pack matching. A creature's GUID carries no spawn id, so a unit is a member of a plan group by its entry and its
// position: an entry the group lists, within the group's radius of one of the group's positions. A unit that fits two
// groups belongs to the nearer one. A summoned Voidwalker (entry 8996) is a member of the group its warlock stands in,
// when it stands near one of that group's members. Everything else in view is not part of any pack.

// packClaim is one matching pass: the units of each group (indices into the slice the pass was run on) and the group of
// every unit a group claimed.
type packClaim struct {
	units [][]int
	of    map[int]int // unit index -> group id
}

// Members returns the unit indices of one group.
func (c packClaim) Members(group int) []int {
	if group < 0 || group >= len(c.units) {
		return nil
	}
	return c.units[group]
}

// GroupOf returns the group a unit index was claimed by, ok false when none.
func (c packClaim) GroupOf(unit int) (int, bool) {
	v, ok := c.of[unit]
	return v, ok
}

// MatchPacks claims the units of one view to the route's groups. groupCount is the number of groups over all legs.
func MatchPacks(units []Unit, groups []Group, groupCount int) packClaim {
	c := packClaim{units: make([][]int, groupCount), of: map[int]int{}}
	best := make([]float64, len(units))
	for i := range best {
		best[i] = math.Inf(1)
	}
	for gi := range groups {
		g := &groups[gi]
		r := float64(g.RadiusYd())
		for ui := range units {
			u := units[ui]
			if u.Friendly || u.Entry == 0 || u.Entry == VoidwalkerEntry || !entryIn(g.Entries, u.Entry) {
				continue
			}
			d := g.NearestPosition(u.Pos)
			if d > r || d >= best[ui] {
				continue // out of the group, or a nearer group already claimed it
			}
			best[ui] = d
			c.units[g.ID] = append(c.units[g.ID], ui)
			c.of[ui] = g.ID
		}
	}
	// The Voidwalker pass runs after the owners are known: it may only join a group one of its own's members stands in.
	for ui := range units {
		u := units[ui]
		if u.Friendly || u.Entry != VoidwalkerEntry {
			continue
		}
		for gi := range groups {
			g := &groups[gi]
			if g.NearestPosition(u.Pos) > float64(g.RadiusYd()) {
				continue
			}
			near := false
			for _, mi := range c.units[g.ID] {
				if units[mi].Entry != VoidwalkerEntry && dist3(units[mi].Pos, u.Pos) <= VoidwalkerNearYd {
					near = true
					break
				}
			}
			if !near {
				continue
			}
			d := g.NearestPosition(u.Pos)
			if d < best[ui] {
				best[ui] = d
				c.units[g.ID] = append(c.units[g.ID], ui)
				c.of[ui] = g.ID
			}
		}
	}
	return c
}

// entryIn reports whether one of the entries is e.
func entryIn(entries []uint32, e uint32) bool {
	for _, x := range entries {
		if x == e {
			return true
		}
	}
	return false
}

// dist3 is the 3-D distance between two positions.
func dist3(a, b Vec3) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// dist2 is the horizontal distance between two positions.
func dist2(a, b Vec3) float64 {
	dx, dy := float64(a[0]-b[0]), float64(a[1]-b[1])
	return math.Sqrt(dx*dx + dy*dy)
}

// guidText is a GUID as the module writes it on a threat line: 32 upper-case hex digits, high half first.
func guidText(g wire.GUID) string { return fmt.Sprintf("%016X%016X", g.High, g.Low) }

// parseGUID reads a GUID written as 32 hex digits (with or without a 0x prefix); the module's own text first.
func parseGUID(s string) (wire.GUID, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) != 32 {
		return wire.GUID{}, false
	}
	hi, err := strconv.ParseUint(s[:16], 16, 64)
	if err != nil {
		return wire.GUID{}, false
	}
	lo, err := strconv.ParseUint(s[16:], 16, 64)
	if err != nil {
		return wire.GUID{}, false
	}
	return wire.GUID{Low: lo, High: hi}, true
}

// AllGroups flattens a route's groups, in plan order, so pack matching and group IDs share one index.
func (r *Route) AllGroups() []Group {
	var out []Group
	for _, l := range r.Legs {
		out = append(out, l.Groups...)
	}
	return out
}

// GroupByID finds a group by its route-wide id.
func (r *Route) GroupByID(id int) (Group, bool) {
	for _, l := range r.Legs {
		for _, g := range l.Groups {
			if g.ID == id {
				return g, true
			}
		}
	}
	return Group{}, false
}
