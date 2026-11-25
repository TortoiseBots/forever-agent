package worldspawn

import (
	"sort"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// PackOptions tunes the pull model. AssistYd is the family-assist hop's radius (the core's CreatureFamilyAssistanceRadius is
// 10 yd, TortoiseBots kAggroFamilyAssistYd); Packs itself does not use it — a pack is the authored grouping, and the assist
// hop happens per pull — but it rides here so a caller keeps the pull model's knobs in one struct.
type PackOptions struct {
	AssistYd float32
}

// Pack is one set of hostile spawns that wake as one: a creature_groups group with AGGRO_TOGETHER, or the spawns a chain of
// creature_linking edges joins (in either direction, since the pack is who comes, not who wakes whom).
type Pack struct {
	ID       uint64        // the smallest member's guid
	Members  []uint64      // the members' guids, sorted
	Centroid dungeon.Point // the members' mean position
	Radius   float32       // the farthest member from the centroid, 3D yd
	// Patrol says a member walks waypoints, or follows one that does (a formation follower).
	Patrol bool
	// Pooled says a member is in a spawn pool: the pack may not be there at all.
	Pooled bool
}

// Packs is the world's hostile spawns grouped into packs. Only hostile spawns are members (faction 35 or npc flags never
// aggro); a NO_AGGRO member still belongs to its pack (it is dragged along by a link or a group), it just never wakes one.
func (w *World) Packs(opt PackOptions) []Pack {
	index := make(map[uint64]int, len(w.Spawns))
	for i, s := range w.Spawns {
		if s.Hostile() {
			index[s.GUID] = i
		}
	}
	return w.packs(index)
}

// packSets is every spawn in index under one pack label: the roots of the authored pack rules. A creature_groups group
// links its leader and members when the group's rows (ORed) carry AGGRO_TOGETHER; a creature_linking edge links the two
// spawns when it carries an aggro flag (both directions). A spawn linked to nothing is its own set. leaderFallback links
// every non-zero group leader's members whatever the flags, for an export that carries no group_flags column at all: it
// names who leads whom and nothing else, and there the leader row is the only evidence a group exists.
func (w *World) packSets(index map[uint64]int, leaderFallback bool) map[uint64]uint64 {
	uf := newUnion(len(w.Spawns))

	// (a) creature_groups rows with AGGRO_TOGETHER: leader and every member are one pack.
	groups := map[uint64][]uint64{}
	groupFlags := map[uint64]uint32{}
	for _, s := range w.Spawns {
		if _, ok := index[s.GUID]; !ok || s.GroupLeader == 0 {
			continue
		}
		groups[s.GroupLeader] = append(groups[s.GroupLeader], s.GUID)
		groupFlags[s.GroupLeader] |= s.GroupFlags
	}
	for leader, members := range groups {
		if !leaderFallback && groupFlags[leader]&AggroTogether == 0 {
			continue
		}
		first := -1
		for _, guid := range append([]uint64{leader}, members...) {
			i, ok := index[guid]
			if !ok {
				continue
			}
			if first < 0 {
				first = i
				continue
			}
			uf.union(first, i)
		}
	}

	// (b) link edges; both directions join the same pack.
	for _, l := range w.Links {
		if l.Flag&(linkAggro|linkToAggro) == 0 {
			continue
		}
		si, sok := index[l.Slave]
		mi, mok := index[l.Master]
		if sok && mok {
			uf.union(si, mi)
		}
	}

	out := make(map[uint64]uint64, len(index))
	for guid, i := range index {
		out[guid] = uint64(uf.find(i))
	}
	return out
}

// PackIndex is every spawn's pack label under the core's pack model: Pack's own (a creature_groups group that aggros
// together, or a creature_linking aggro edge), with one reading for an older export. An export whose creature rows carry
// group leaders but no group_flags column at all says only who leads whom: with no flags to read, a shared non-zero leader
// is the group those rows describe. A spawn in no group is its own label.
func (w *World) PackIndex() map[uint64]uint64 {
	index := make(map[uint64]int, len(w.Spawns))
	flagsSeen := false
	for i, s := range w.Spawns {
		index[s.GUID] = i
		if s.GroupFlags != 0 {
			flagsSeen = true
		}
	}
	return w.packSets(index, !flagsSeen)
}

// packs groups the hostile spawns found in index. A pack's identity is the authored one: the group's leader and members, and
// both directions of every aggro link (spec 5.2 (a) and (b); nothing else — proximity and family assist happen per pull).
func (w *World) packs(index map[uint64]int) []Pack {
	if len(index) == 0 {
		return nil
	}
	sets := w.packSets(index, false)
	components := map[uint64][]uint64{}
	for guid := range index {
		components[sets[guid]] = append(components[sets[guid]], guid)
	}
	patrollers := w.patrollers()
	out := make([]Pack, 0, len(components))
	for _, members := range components {
		sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
		p := Pack{ID: members[0], Members: members}
		var sum dungeon.Point
		for _, guid := range members {
			s := w.Spawns[index[guid]]
			sum.X += s.Pos.X
			sum.Y += s.Pos.Y
			sum.Z += s.Pos.Z
			if patrollers[s.GUID] {
				p.Patrol = true
			}
			if s.Pool != 0 {
				p.Pooled = true
			}
		}
		n := float32(len(members))
		p.Centroid = dungeon.Point{X: sum.X / n, Y: sum.Y / n, Z: sum.Z / n}
		for _, guid := range members {
			if d := dist3(w.Spawns[index[guid]].Pos, p.Centroid); d > p.Radius {
				p.Radius = d
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// patrollers is the spawns that walk a waypoint loop, and the formation followers of one: they move, so their position is a
// guess and a pull estimate treats them as a patrol (spec 5.2).
func (w *World) patrollers() map[uint64]bool {
	byGUID := make(map[uint64]Spawn, len(w.Spawns))
	for _, s := range w.Spawns {
		byGUID[s.GUID] = s
	}
	out := map[uint64]bool{}
	for _, s := range w.Spawns {
		if s.MovementType == 2 {
			out[s.GUID] = true
			continue
		}
		if s.GroupFlags&FormationMove == 0 || s.GroupLeader == 0 || s.GroupLeader == s.GUID {
			continue
		}
		if leader, ok := byGUID[s.GroupLeader]; ok && leader.MovementType == 2 {
			out[s.GUID] = true
		}
	}
	return out
}

// union is a disjoint-set over spawn indexes, for the pack components.
type union struct{ parent []int }

func newUnion(n int) *union {
	u := &union{parent: make([]int, n)}
	for i := range u.parent {
		u.parent[i] = i
	}
	return u
}

func (u *union) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

func (u *union) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}
