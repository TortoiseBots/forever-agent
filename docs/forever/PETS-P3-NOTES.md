# Forever pets, round P3 notes

Scope: pets in the trace (`pt`) and bug-hunter rule 7 (`pet attacks unit master is not fighting`) on a Forever watch.

Code:
- `internal/trace/trace.go`: `Pet` (`g`, `ow`, `hp`, `cb`, `tg,omitempty`, `x`, `y`, `z`) and `Frame.Pets` (`pt,omitempty`).
- `internal/forever/driver/ports.go`, `internal/forever/world/dungeon_session.go`, `internal/forever/driver/watch.go`, `internal/forever/driver/record.go`: `driver.Unit` gains `Combat` and `Owner` (filled by `dungeonWorld.Units` using P2's `unitOwner` rule: `SummonedBy`, else `CreatedBy`, else `CharmedBy`), `watchFrame` passes `r.world.Units()` into `Recorder.WatchFrame`, and `framePets` writes `pt` for units whose owner is a member of the frame (`Frame` also fills `pt` and skips party pets from `mobs`).
- `internal/bughunt/model.go`, `load_forever.go`, `const.go`, `rules_frames.go`, `rules_pets.go`: `Frame.Pets` loaded from `trace.Frame.Pets`, `rule7MinS = 5`, `ruleNames[7]`, and `Rule7` registered in `FrameRules()`.

## Decisions

- **Frame pets (`pt`)**: `framePets` records only units whose `Owner` (from `unitOwner`) matches a party member's `G` in the frame; unowned units and non-party pets are omitted. An empty target omits `tg`, and an empty `Pets` slice omits `pt`.
- **Rule 7**: runs over consecutive frames (resetting across gaps over `MAX_GAP_S`) for each pet whose owner is a subject member of the frame and alive (`owner.Alive && lifeOK(owner)`), where `pet.Combat` is true and `pet.Target` is a non-empty mob target (not a party member or a pet), while the owner is not in combat or has an empty target, for strictly longer than `rule7MinS` (5 s). One hit per episode, keyed `(7, owner class)`, attributed to the owner (`Bot`, `Cls`, `Role`). Pets with an unknown owner are never judged.

## What is not live-proven

- No live watch run with a hunter or warlock bot pet yet: L1 runs a dungeon watch with a hunter bot and runs `bughunt` over the recording.
