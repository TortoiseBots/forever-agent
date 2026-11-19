// Package botctl is the interface a Bot module's controller offers to the agent: add a bot, read the roster and one
// bot's state, give an order, dismiss. The concrete controller for a WoW Forever Bots server is
// internal/forever/command/fbcmd; the interface exists so the dungeon and party code of a later round can drive
// either module without knowing which one answered.
package botctl

import "context"

// Ref names one bot. ID is the module's numeric id when it is known; Name is always set.
type Ref struct {
	ID   uint32
	Name string
}

// State is one bot's state, as far as the controller could read it.
type State struct {
	Name     string
	Class    string
	Race     string
	Level    int
	Role     string
	Zone     string
	HP       int
	HPMax    int
	Power    int
	PowerMax int
	MapID    uint32
	X, Y, Z  float64
	Online   bool
	Grouped  bool
	// Raw keeps every field of the module's own answer that State does not type, so a newer module loses nothing.
	Raw map[string]string
}

// Controller is what a bot module's adapter implements.
type Controller interface {
	// Kind names the module family ("forever-bots" for a WoW Forever Bots server).
	Kind() string
	// Add asks for one bot and returns it once the server accepted the request. The caller waits for the bot to
	// join the group separately (the server groups it itself).
	Add(ctx context.Context, class, role string) (Ref, error)
	// Roster lists the account's bots.
	Roster(ctx context.Context) ([]State, error)
	// State reads one bot's state.
	State(ctx context.Context, ref Ref) (State, error)
	// Order gives one bot an order. The return is the bot's acknowledgement text.
	Order(ctx context.Context, ref Ref, verb, arg string) (string, error)
	// Dismiss dismisses one bot, or every bot when ref names "all".
	Dismiss(ctx context.Context, ref Ref) error
}
