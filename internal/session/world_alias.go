// Result aliases: the session result types live in internal/world since
// step 4 of docs/forever/SEAM-AUDIT.md (type aliases, no behaviour change).
// The protocol layer keeps naming session.StatusInfo and friends; the upper
// layers name world.StatusInfo and friends.
package session

import (
	"github.com/pjerra/forever-agent/internal/world"
)

type (
	Status         = world.Status
	StatusInfo     = world.StatusInfo
	PartyMember    = world.PartyMember
	PartyInfo      = world.PartyInfo
	LootSlot       = world.LootSlot
	LootWindow     = world.LootWindow
	LootTake       = world.LootTake
	LootTaken      = world.LootTaken
	RollRef        = world.RollRef
	RollInfo       = world.RollInfo
	TradeItemReq   = world.TradeItemReq
	TradeRequest   = world.TradeRequest
	TradeGave      = world.TradeGave
	TradeResult    = world.TradeResult
	TradeFailed    = world.TradeFailed
	TradeStepState = world.TradeStepState
	TradeSetResult = world.TradeSetResult
	PetSpell       = world.PetSpell
	PetInfo        = world.PetInfo
	PetOrder       = world.PetOrder
	DeathInfo      = world.DeathInfo
	MountRef       = world.MountRef
	MountInfo      = world.MountInfo
	EquipRef       = world.EquipRef
	BuyRef         = world.BuyRef
	EffectRow      = world.EffectRow
	ReagentRow     = world.ReagentRow
	SpellRow       = world.SpellRow
	SpellsFilter   = world.SpellsFilter
	MarkResult     = world.MarkResult
	LootedObject   = world.LootedObject
)

const (
	StatusConnecting   = world.StatusConnecting
	StatusCharSelect   = world.StatusCharSelect
	StatusLoggingIn    = world.StatusLoggingIn
	StatusInWorld      = world.StatusInWorld
	StatusLoggingOut   = world.StatusLoggingOut
	StatusLoggedOut    = world.StatusLoggedOut
	StatusDisconnected = world.StatusDisconnected
)
