// Snapshot aliases: the snapshot types live in internal/world since step 3
// of docs/forever/SEAM-AUDIT.md (type aliases, no behaviour change). The
// protocol layer and its tests keep naming objects.Unit and friends; the
// upper layers name world.Unit and friends.
package objects

import (
	"github.com/pjerra/forever-agent/internal/world"
)

type (
	Unit          = world.Unit
	Aura          = world.Aura
	Cast          = world.Cast
	Item          = world.Item
	GameObject    = world.GameObject
	DynamicObject = world.DynamicObject
	QuestSlot     = world.QuestSlot
	Filter        = world.Filter
	GOFilter      = world.GOFilter
)

// GOTypeByName is the id of a game-object type given by name; see world.
func GOTypeByName(name string) (uint8, bool) { return world.GOTypeByName(name) }

// GOTypeName names a game object type id; see world.
func GOTypeName(t uint8) string { return world.GOTypeName(t) }
