# Forever #100 round 2: the dashboard on the Forever mesh

Ticket #100 brings the live map and route editor to the Forever navmesh. Round 1 (PR #170) ported `map_snapshot` to the
Forever serve; round 2 puts the dashboard's plan view and route editor on the v16 mesh. Both rounds branch from
`origin/main` and are mergeable alone: round 2 touches only the dashboard command and its docs.

## The format flag

`forever-agent dashboard` gained `--mmaps-format turtle|forever` (default `turtle`, the old behaviour) and
`--parent MAP=PARENT[,..]`, the server's instance parent rule. One helper, `openDashNav(dir, format, parents)`, opens
the planner for both call sites (the plan view's `--mmaps-dir` and the editor's mesh), so the two can never read
different generations:

- The generation is the one named, never guessed from the folder. The folder must hold at least one tile file that
  generation's own parser accepts (`navmesh.Forever.TileParse` / the MaNGOS one), else the start fails with
  `--mmaps-dir DIR has no Forever tiles` (or `no Turtle tiles`) instead of surfacing later as "every point off the
  mesh".
- `--parent` is refused with the Turtle format (`--parent needs --mmaps-format forever`) and parsed by the same
  `parseParentPairs` the other Forever commands use (`forever_path.go`). Nothing is built in: without it an instance
  reads only its own tiles.
- The planner is `navmesh.NewPlannerFormat(dir, format, parents)`, the same `Planner` the plan tools use; the plan
  view's route (`GET /api/plan/<name>/route`) and the editor's `routeedit.Evaluate` take it through `dashNav`, which
  hands the Turtle planner over as it is and a Forever one behind `dashForeverNav`. That wrapper offers no
  `ServerPath`: a v16 tile set cannot replay the 1.12 core's own path query, and the plan check plans every hop with
  the v16 walk instead of refusing every edit (`plan-check --forever` wraps its planner the same way with
  `foreverNav`). The wrapper carries `Format`, so the page's status line still names the generation.

The Ragefire Chasm example the README carried for the plan tools applies unchanged: Eastern Kingdoms is map 0, so
`--mmaps-format forever --parent 389=0` (the pair `forever_real_test.go` uses).

## The page's status line

The route answer (`RouteDoc`) carries `format` ("turtle"/"forever") when the navmesh names its generation
(`FormatNamer`, which `navmesh.Planner` satisfies), and the plan view's route legend status line ends with
`· navmesh: forever`. No new setting: the label comes from the planner that traced the route, so it cannot disagree
with the mesh drawn on.

## Tests

`cmd/forever-agent/dashboard_forever_test.go`:

- `TestDashboardForeverEditAcceptsOnMesh`: a Ragefire-style plan (map 389) is edited on a Forever fixture whose
  instance tile and parent tile are both present: the move saves a checked `v0001`, the route answer says
  `"format":"forever"`, and a walk on the island that exists only under map 0's tile file is complete (the parent
  rule). Mutations: opening a Turtle planner finds no Forever tile; dropping the parent pairs loses the island walk.
- `TestDashboardForeverEditRejectsOffMesh`: a point 50 yd outside the square is refused with the editor's `off the
  mesh` 400, and no version is written.
- `TestDashboardFormatMismatchFailsAtStart`: each generation's folder is read with its own parser (matching formats
  open), the wrong pair fails in `openDashNav` and through the command with exit 2, and a made-up format is refused.
- `TestDashboardParentNeedsForever`: `--parent` without `--mmaps-format forever` exits 2 naming the flag.

The node check (`testdata/realroute_check.js`) pins the status line's format suffix.

## What round 2 took from #94 and #105

Both are merged into `main`; neither had a symbol this round consumes. #94's world export loader is not needed: the
plan view's pack rings are drawn from the plan files as before. #105's route regenerate and snap are not called: the
editor only checks edits (a later ticket can add "snap this leg").
