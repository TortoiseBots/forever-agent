# MAP-M2: `forever run-map` and `dungeon-watch --map`

Round M2 of the control and replay spec. The adapter (`runmap`, round M1) and the drawing (`mapview`: SVG per floor, the
single-file HTML with a time slider) already exist; M2 is the command around them.

## What was added

* `forever run-map DIR [flags]` (`cmd/forever-agent/forever_runmap.go`). `DIR` is a watch folder or one trace file; the
  files are written into that folder (beside the trace). `--plan` (else the summary's plan name under `dungeons/`),
  `--navmesh` with `--parent` for the floor background, `--out NAME.svg`, `--html`, `--names`, `--floors`, `--width`,
  `--from-s` / `--to-s` (one stretch of the trace; the run's zero becomes `--from-s` and events outside are counted in the
  notes) and `--force`.
* `forever dungeon-watch --map` (needs `--out`): the same two files (SVG and HTML) after the summary, from the watch's own
  plan and `--navmesh`. Any failure is one `map: not written: ...` line; the exit code is the watch's.
* `runmap.LoadRun` (the run's id for naming), `Options.FromS/ToS`, `bughunt.Run.PartyOrder`, `RunWatch.LoosePulls`.

## Decisions

1. **Names.** `--out` is one file name, never a path: no separator, no `..`, no leading dot, characters `[A-Za-z0-9._-]`,
   a lowercase `.svg` extension (added when missing). It is checked before the folder is read, so a refused name writes
   nothing. Default name: the run id with unsafe characters made `_`.
2. **No partial writes.** All targets are checked first; if any exists (without `--force`) nothing is written.
3. **Floors.** Several floors write `NAME-f1.svg`, `NAME-f2.svg`; the HTML holds all of them.
4. **No navmesh** (flag absent, folder without tiles, or a `CGO_ENABLED=0` build, `ErrNoEngine`): the map is drawn without a
   background and the image's notes say why.
5. **Offline.** The HTML has no external script, stylesheet, font or image; a test searches for any reference to a URL.
6. **Names off by default.** The SVG and HTML hold slots, not character names, unless `--names`.

## Tests

`cmd/forever-agent/forever_runmap_test.go` over a hand-made record in `testdata/runmap` (two members, a boss kill, one pull,
one pause) with golden SVG and HTML (`go test -update-runmap-golden` rewrites them and still fails, so the diff is read). Failing case: `--out ../x.svg` and names with
a separator are refused before any write. The M1 fixes and the per-window wipe inference have their tests in
`runmap_test.go` and `driver/watch_test.go`; see `MAP-M1-NOTES.md` for the list.

## Output safety and bounds (fix round)

* Output goes to temp files in the resolved `DIR`, then into place only when every file was written. A symlink or any
  non-regular file at an output name is refused, `--force` included; a refused set changes nothing.
* `--force` also removes this run name's stale `NAME-fN.svg` (and `NAME.svg` after a split); other files stay.
* `dungeon-watch --map` runs with a 60 s deadline, honours Ctrl-C and turns a panic into one log line; the watch's exit
  code is unchanged.
* The map reads no combat log and thins records past 20000 frames (a note says so).
* The summary's `plan` must be a plan name (no separator, no `..`); `--from-s/--to-s` must be finite.

## Limits

* No live run yet (round M4): the HTML has been checked as text and by the golden files, not in a browser here.
* The floor split `auto` needs a navmesh background; without one the map is one floor.

## Known limits

* A forced multi-file write can still leave a mixed set if the process dies between renames (cmd/forever-agent/forever_runmap.go:308-318).
* Frame thinning bounds the drawn frames but not the memory used while loading a trace (internal/forever/runmap/runmap.go:67-71).
* The watch's map-step timeout abandons the generation goroutine instead of stopping it (cmd/forever-agent/forever_watch.go:252-273).
