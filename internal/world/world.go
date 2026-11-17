// Package world holds the neutral value types the upper layers share: guids,
// snapshots of units, game objects and the quest log, and the result types the
// session returns. Steps 1 to 4 of docs/forever/SEAM-AUDIT.md move the upper
// layers onto these types with Go type aliases, so nothing changes behaviour.
// The protocol layers (internal/session, auth, worldconn, packet, opcode,
// objects, game) keep their own types and stay untouched.
package world

import (
	"github.com/pjerra/forever-agent/internal/packet"
)

// GUID is a 64-bit 1.12 object guid; see internal/packet for the layout.
// The upper layers name this type so the 1.12 width stays in one place.
type GUID = packet.GUID

// ParseGUID accepts "0x"-prefixed or bare hex.
func ParseGUID(s string) (GUID, error) { return packet.ParseGUID(s) }
