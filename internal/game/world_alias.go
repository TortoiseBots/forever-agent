// Value aliases: the neutral codec results live in internal/world since
// step 4 of docs/forever/SEAM-AUDIT.md (type aliases, no behaviour change).
package game

import (
	"github.com/pjerra/forever-agent/internal/world"
)

type (
	ResurrectRequest = world.ResurrectRequest
	Corpse           = world.Corpse
	ItemInfo         = world.ItemInfo
	InitialCooldown  = world.InitialCooldown
	InitialSpells    = world.InitialSpells
	TradeItem        = world.TradeItem
)

const InfiniteCooldown = world.InfiniteCooldown

// Facing is the orientation from (x0, y0) towards (x1, y1), in [0, 2π).
func Facing(x0, y0, x1, y1 float32) float32 { return world.Facing(x0, y0, x1, y1) }
