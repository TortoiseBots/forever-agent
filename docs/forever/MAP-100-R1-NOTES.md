# Forever #100 round 1: `map_snapshot` on the Forever serve

Ticket #100 brings the live map and route editor to the Forever navmesh. Round 1 ports the serve method the
dashboard's map view reads; round 2 puts the dashboard itself on the Forever mesh.

## The method

`map_snapshot` is registered with `serveReadTyped` (it answers while the link is down, like `status`), so the
picture stays readable through a reconnect. Its arguments and answer are `ServeMapParams` / `ServeMapResult`
(`internal/forever/world/serve_map.go`), with the same JSON names the Turtle serve's `mapParams` / `mapResult`
carry: `floor`, `plan`, `floors`, `background`, `html`, `pad` in; `map`, `floor`, `floors`, `seconds`, `members`,
`creatures`, `events`, `background`, `notes`, `svg`, `html` out. It left `notPortedMethods`; the two tests that
pinned the old answer now pin the port (`serve_fake_test.go`, `serve_npc_methods_test.go`).

- `plan` is checked with `routeedit.PlanName`, the one rule the dashboard and `dungeon_run` use, before any file is
  read; the plan then loads from `--plans-dir` (new flag, default `dungeons`) and is drawn as the overlay.
- The background comes from the session's own planner (`navPlanner`, built from `--navmesh` and the `--parent`
  rule). Without a navmesh folder the answer carries `background: false` and a note saying so.
- The Forever session keeps no events log in `mapview`'s shape (its combat ring is another type), so the run's
  markers stay empty in round 1.

## The recorder

`serveMapRecorder` samples the session every 2 s (`serveMapSampleEvery`, shortened by the tests) and feeds one
bounded `mapview.Builder`; nothing else grows. It is started by `install` (the session entering the world) and
stopped by `teardown`, so a reconnect leaves no goroutine behind (counted by a test). Each tick posts its read to
the session goroutine through the loop's own call queue (`callDead`): the frame reads the session's fields on the
goroutine that owns them, so the recorder adds no lock and no lock order.

## The frame

`mapFrame` reads what `serveStatus` reads for the map and the character's position, and returns false when the
session is not in the world (`selfSeen`).

- Party members: the roster names them, but the full-state packet carries no map id, so a member is placed only
  when this map's object store holds them — the store holds the objects of the current map only, which leaves a
  member on another map out (this is the round's "other-map member" rule). The store's position is the member's
  last known position on this map.
- Hostiles: the store's units, hostile by the model's faction lookup (the same one `units` uses, cached per
  template); dead units carry `dead`. Players are never hostiles here: they are party members or strangers.
- Game objects: the store's objects, with the GameObjectTypes name.

## Tests

`serve_map_test.go`: `TestServeMapSnapshotDrawsPartyAndHostiles` (two members, one hostile, an SVG),
`TestServeMapSnapshotNoNavmeshNote`, `TestServeMapSnapshotRejectsBadPlanName`,
`TestServeMapSnapshotSkipsOtherMapMembers`, `TestServeMapRecorderSamplesBetweenTicks` (a member moved between two
ticks is a second sample, so a recorder that never ran is caught) and `TestServeMapRecorderStopsOnTeardown`
(goroutine count and the recorder's done channel).
