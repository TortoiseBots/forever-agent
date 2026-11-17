package objects

import (
	"math"
	"sort"
	"strconv"

	"github.com/pjerra/forever-agent/internal/packet"
)

// GOStateName names a GOState; the client's own wording for a door is open (active) and closed (ready).
func GOStateName(st uint32) string {
	switch st {
	case GOStateActive:
		return "active"
	case GOStateReady:
		return "ready"
	case GOStateActiveAlt:
		return "active_alt"
	}
	return strconv.FormatUint(uint64(st), 10)
}

// GOUsable is a hint, from what the update fields say, that CMSG_GAMEOBJ_USE can do something: the
// core ignores a use of a generic object or one flagged no-interact (SpellHandler.cpp:286-318, the
// HandleGameObjectUseOpcode checks), and the client does not offer one flagged in use or with an
// unmet interact condition. Reach and the object's own conditions (key, quest, faction) are checked
// by the server alone, without an answer.
func GOUsable(typ uint8, flags uint32) bool {
	return typ != GOTypeGeneric && flags&(GOFlagNoInteract|GOFlagInUse|GOFlagInteractCond) == 0
}

// IsGameObject reports whether g is a game object the store holds, or knows only from values.
func (s *Store) IsGameObject(g packet.GUID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.objs[g]
	return ok && isGameObject(o)
}

func isGameObject(o *object) bool {
	if o.typ == TypeGameObject {
		return true
	}
	return o.typ == 0 && o.guid.IsGameObject() // values seen before a create block
}

// GameObjects returns the game objects matching f, nearest first, objects without a position last.
func (s *Store) GameObjects(f GOFilter, ox, oy, oz float32) []GameObject {
	s.mu.RLock()
	var out []GameObject
	for _, o := range s.objs {
		if !isGameObject(o) {
			continue
		}
		g := toGameObject(o, ox, oy, oz)
		if (f.Near > 0 && (g.Distance < 0 || g.Distance > f.Near)) || (f.Entry != 0 && g.Entry != f.Entry) || (f.Type != nil && g.Type != *f.Type) {
			continue
		}
		out = append(out, g)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if ni, nj := out[i].Distance < 0, out[j].Distance < 0; ni != nj {
			return nj
		}
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].GUID < out[j].GUID
	})
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// GameObject is one game object by guid.
func (s *Store) GameObject(g packet.GUID, ox, oy, oz float32) (GameObject, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.objs[g]
	if !ok || !isGameObject(o) {
		return GameObject{}, false
	}
	return toGameObject(o, ox, oy, oz), true
}

func toGameObject(o *object, ox, oy, oz float32) GameObject {
	v := o.values
	g := GameObject{
		GUID: o.guid, Entry: v[FieldEntry], Type: uint8(v[GOTypeID]), State: GOStateName(v[GOState]),
		Flags: v[GOFlags], DynFlags: v[GODynFlags], DisplayID: v[GODisplayID], CreatedBy: guidAt(v, GOCreatedBy),
	}
	if g.Entry == 0 {
		g.Entry = o.guid.Entry()
	}
	g.TypeName = GOTypeName(g.Type)
	g.Locked = g.Flags&GOFlagLocked != 0
	g.Usable = GOUsable(g.Type, g.Flags)
	if o.posAt.IsZero() {
		g.Distance, g.Distance2D = -1, -1
		return g
	}
	g.X, g.Y, g.Z, g.O = o.x, o.y, o.z, o.o
	dx, dy, dz := float64(g.X-ox), float64(g.Y-oy), float64(g.Z-oz)
	g.Distance = float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
	g.Distance2D = float32(math.Hypot(dx, dy))
	return g
}
