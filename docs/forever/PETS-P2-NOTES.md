# Forever pets, round P2 notes

Scope: the wiring on top of P1. The character's pet as its own `Summon` names it, the `pet` and `pet_action`
methods, the pet fields and the owner rule on `units`/`unit` rows, and the pet events. Round P3 puts the pets in the
trace and the bug hunter's rule 7; L1 proves the round live.

Code: `internal/forever/world/pet.go` (the session's pet watch, `handlePet`, `petCheck`, the owner rule and the `pet`
result), `internal/forever/world/serve_pet_methods.go` (the two methods and their registration),
`internal/forever/world/session.go` (the one dispatch line for the four pet packets and the `petCheck` call after
every applied `SMSG_UPDATE_OBJECT`), `internal/forever/world/serve.go` and `serve_session_methods.go` (the owner and
pet fields on `ServeUnit`, the `owner` parameter of `units`), the `pet`/`pet_action` names leave
`notPortedMethods`.

## The decisions this round implements

- **Which unit is the pet**: the character's own `UF::UnitData Summon`. The pet's health, power, target, combat flag
  and position come from its ordinary unit row (the model already merges it), so `pet` reuses `serveUnit`.
- **Owner rule**: `SummonedBy`, else `CreatedBy`, else `CharmedBy`, with `owner_kind` naming the field. The precedence
  is pinned by a unit that carries both `SummonedBy` and `CreatedBy` (the answer is the first one). The same rule
  fills `ServeUnit.Owner` and backs the `units owner=` filter, which is how a party's hunter pets are listed.
- **The bar** (`SMSG_PET_SPELLS_MESSAGE`): the 18-button body P1 parses, the ten `Bar` buttons shown, the first spell
  at slot 3 and the last at 6, spell buttons of type Enabled (0xC1), Disabled (0x81) or Passive (0x01). `spells` is
  those buttons plus every `Actions` entry no button shows (slot -1). A name is the server's only when its SpellName
  record is already cached: `pet` never fetches.
- **`SMSG_PET_MODE`** updates the bar's command and react when its GUID matches; **the remove body** (an empty pet
  GUID, `Player::SendRemoveControlBar`) clears the bar.
- **`enabled`** is the core's own test: the bar is known and the character's own `MountDisplayID` is zero. The core
  sends no disable on mount and `HandlePetAction` drops the orders while mounted, so `pet_action` refuses a mounted
  character before anything is sent.
- **Orders** (`pet_action`): `attack` (target, else the current selection, else an error), `follow`, `stay`,
  `passive`, `defensive`, `aggressive`, `cast` (slot 3-6, spell id or name, the bar's own button sent back),
  `autocast_on`, `autocast_off` (refused for a passive spell), the same nine actions the Turtle client had. An
  order that needs the bar (`cast`, `autocast_*`) refuses before sending when no bar has arrived ("the pet's bar has
  not arrived"); the command and reaction buttons are built from the constants the way the client's own bar does, so
  `follow`/`stay`/the reactions are allowed before the bar lands (the Turtle client allowed them too). After a
  react/command order the session applies it to its copy of the bar; the next `SMSG_PET_MODE` overrides it.
- **Events** (`internal/events` kinds, already defined): `pet_summoned` (source the pet, target the character,
  `data.entry`, `data.dead`), `pet_dismissed` (`data.was_dead`, emitted for the old pet when the Summon changes),
  `pet_died`, `pet_revived`, and `pet_error` from both refusals (`data.reason` + `data.code` for a cast failure,
  `data.feedback` for an order). A pet announced already dead also gets a `pet_died` row (the Turtle client did the
  same). The last refusal is the `pet` result's `refused` until a new pet is summoned.
- **Not reported**: `happiness`, `happiness_state` and `loyalty`. The Foreverse model has no power-slot mapping for
  a hunter pet's happiness, so the fields are absent instead of guessed (the spec's own decision).

## Tests

`pet_fake_test.go` drives the session's own handler on the handler-boundary fake (`newItemWorld`): the pet packets go
through `handlePet`, the unit blocks through the dispatch's `applyUpdate` + `World.Apply` + `petCheck` path, the
orders are compared byte for byte with the `pet` package's builders, and the event rows are read from the session's
own ring. Cases: the owner listing and the `owner` filter; the bar, its three spell buttons and each order's packet;
the refusals (no pet, mounted, a passive spell autocast, an unknown action, attack with no target, an unknown spell);
the events; the cleared bar; a malformed pet body counted as a parse error and merged into nothing.

Mutations run by hand, one at a time, each caught by a named test:

| Mutation | Caught by |
|---|---|
| owner read from `CreatedBy` before `SummonedBy` | `TestServePetListsOwnerInUnits` |
| no `pet_dismissed` when the Summon changes to another GUID | `TestServePetEvents` |
| `enabled` ignoring the mount | `TestServePetRefusals` |
| a cast allowed on a passive button | `TestServePetRefusals` |

## What is not live-proven

- No live run yet: L1 is the round that puts a hunter's pet through `pet`, `pet_action` and `units owner=` on a real
  realm.
- No recording has the agent's own pet bar yet (the lead's set-8 request lists what to capture). The bar, its
  button order and the orders' bytes are the core's writers (`PetPackets.cpp:23-66`, `:140-148`, `:173-178`), not a
  capture.
- The orders are applied to the session's copy because the core sends nothing back for a successful order
  (`HandlePetAction`); the next `SMSG_PET_MODE` is the server's word.
- `pet_action` does not add `stop_attack` (the Turtle client had none) even though P1 built the packet; the builder
  waits for a round that needs it.
