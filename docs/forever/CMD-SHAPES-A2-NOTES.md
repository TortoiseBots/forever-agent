# Commander: place in a shape, rally and fall back — agent round (A2)

The agent side of COMMANDER-SHAPES-SPEC §3: it sends the module's words and
reads the module's records, never computes a slot itself. The wire format was
checked against the module's final M2 shape (`forever/docs/PLACE-RALLY-NOTES.md`
and `forever/src/host/cmd/TbPlace.cpp` + `TbPlacePlan.h` on branch
`cmd/shapes-m2`, head `4212ef4b1`), whose section 20 of `TB-COMMANDS.md` is the
placement replies.

## What was built

| File | What |
|---|---|
| `internal/forever/command/tbcmd/place.go` | the typed `cmd` placement answer: `PlaceReport`/`PlaceBot`, `Client.Place`, the head/row/end parsers with the `Has*` presence flags, `ErrPending` for `status=waiting`. The collector cap is a raid's answer (256 lines), and the words go through `CmdLine`. Tests drive the M2 emitters. |
| `internal/forever/command/tbcmd/commander.go` | card keys: `Card` gains `Hold`, `Rally`, `RallyMap`, `RallyInst` with separate presence flags; `CardMember` gains `Hold`, `Slot`. An absent key stays empty/nil, and `rmap=`/`rinst=` never make a point of their own. |
| `internal/forever/cmdctl/verbs.go`, `backend.go`, `cmdctl.go` | the PL package rows (`place` 1-12 guarded, `rally` 0-1 unguarded, `fallback` 0-1 guarded, `resume` 0-0 guarded; arity pinned against `TbPlacePlan.h ParseCommand`), the real typed `placePath` beside C5/C6's `cmdPath`, the pure `placeArgs` word check (at: points use the module's `ParsePointNumber` grammar), the PL probe (`.tb cmd place` → `missing-argument`), the who-name ownership check against every card of the character, the post-line ownership re-check of queued rows only plus the answer-lead check, `Result.Place`, `placeBots`. |
| `internal/forever/world/serve_commander.go`, `internal/forever/serveclient/serveclient.go` | `ServeCommanderPlace`/`CommanderPlace` (snake_case head, absent numbers omitted), `ServeCommanderBot2`/`CommanderPlaceBot` rows (the refusal key is `code=`), `hold`/`rally`/`rally_map`/`rally_inst` on the card, `hold`/`slot` on the member. The C5/C6 `fields`, task `target`/`extra` and `lead-mismatch` mapping are kept. |
| `internal/dashboard/forever_control.go`, `control.go` | `POST /api/control/<id>/place` (Forever only, same token/nonce gate and control log as `order`; the handler reads the card, builds the words with the pure `placeRequestArgs`, 400 sends nothing). A map point carries the `map` it was picked on and is checked finite, inside the world bounds, on the card's map and within the module's 100 yd reach. `foreverSummary` for place/fallback is `placed N, refused M` plus `bot: code` per refused row. |
| `internal/dashboard/forever_card.go`, `card.go` | `BotView` gains `Hold`, `SlotX/SlotY`, `HasSlot` (projected, and only for a member on the card's map); `Card` gains `Rally` (projected, only when `rally_map` equals the card's map) and `Elsewhere` (the members and the rally point on another map, as text: listed, never drawn). |
| `static/controls.js`, `layers.js`, `app.css`, `unitinfo.js`, `app.js` | the Positions panel (Shape, Gap, Anchor, Facing, Who, Place/Release/Rally here/Rally at point/Clear rally/Fall back/Resume; map point mode attaches a click handler and a crosshair to the card's svg, Esc cancels, and a point is dropped when the card's map changes), the `slot`/`slot-line`/`rally` layers (default on, with legend entries), the map drawing (hollow square, dashed >3 yd line, flag). Testdata in `controls_check.js` asserts the exact request body and the arm/pick/disarm flow; `layers_check.js` pins the two layers on by default. |

## Decisions worth keeping

1. The agent never sends `cursor`: `placeArgs` refuses it before any line goes out (`place: cursor needs the game client's minimap ping; use at:<x>,<y>`). A `status=waiting` answer is `ErrPending`, never a result — it means someone else's words.
2. `rally` skips the run guard (memory only, nothing moves); `place`, `fallback` and `resume` keep it, over every card of the character (the module acts on every bot bound to the caller). PL's probe is its own verb: `.tb cmd place` answers `error code=missing-argument cmd=cmd` when the package is there; another explicit module error means it is not; a timeout or transport error is not remembered.
3. The ownership check runs on the guard's fresh cards before the line (no card → `NotOwnedError{Kind:"order"}`; a named bot outside the cards → `NotOwnedError{Target, Kind:"bot"}` with nothing sent), and again after the line for queued rows only: a refused row moved nothing, and the module names an absent bound bot by its guid (`code=no-character`). Only place has who words; `rally`/`fallback`/`resume` take option words only.
4. End-count reconciliation is `placed + refused == rows` for place/fallback and `released + refused == rows` for follow/resume (a `to=` row counts as placed: it was queued). `rally` read-back and `rally clear` with no rows are a normal result, never `RefusedError`.
5. The dashboard never duplicates the geometry: it checks shapes, gaps (1.5-12, one fraction digit), anchors, face 0-359, who keywords/member names, and the clicked point's map/bounds/reach against the card; the module does the rest (gap pairing, face range vs anchor, shape/anchor pairs, floor, path and liquid).
6. A rally point on another map is still printed with its `rmap`, but the card's flag is drawn only when `rally_map` equals the card's map.
7. The Positions send buttons (Place, Release, the three rally buttons, Fall back, Resume) are held while any `place:` request is in flight. `paintPlace` disables them while the busy set holds the key, and the answer or an error repaints them enabled, so a second click sends nothing (`controls_check.js` drives the held buttons and a failed send).
8. Markers for another map are not drawn. A member whose `map` is not the card's map has no position and no slot on the card, and a rally point whose `rally_map` is not the card's map gets no flag. Each one is listed in `Elsewhere`, which the Positions panel shows as `Elsewhere (not drawn here): ...`.

## Verification

Unit and wiring tests (narrowest checks that exercise each change):

```
$ go test ./internal/forever/command/tbcmd/ -count=1
ok
$ go test ./internal/forever/cmdctl/ -count=1
ok
$ go test ./internal/forever/world/ -run 'TestServeCommander' -count=1
ok
$ go test ./internal/dashboard/ -count=1
ok
$ node internal/dashboard/testdata/controls_check.js <controls.js>   # ok (view_test.go, skipped without node)
$ node internal/dashboard/testdata/layers_check.js <layers.js>       # layers.js ok
```

Mutations run once, each turning its named test red, then reverted:

- A-1 `placeArgs` lets `cursor` through → `TestPlaceCursorSendsNothing` fails.
- A-2 the who-name ownership check skipped → `TestPlaceStrangerSendsNothing` fails.
- A-3 `rally` given `Guard: true` → `TestPlaceGuard` fails.
- A-4 the end-count check removed → `TestPlaceFailures/count mismatch` fails.
- A-5 `unproject` swaps x/y → the controls node check fails.
- A-6 the released/placed tally mix-up back → `TestPlaceFollowAnswer` and `TestResumeAnswer` fail.
- A-7 the head's `lead=` wiped by the end line → `TestRallyHereKeepsHeadLead` fails.
- A-8 the click listener left unattached → the controls node check fails.
- A-9 the rally copied unprojected → `TestForeverCardSlotAndRally` fails.
- A-10 the summary reads `error` instead of `code` → `TestForeverSummaryPlaceRefusalCode` fails.

Gates on a clean committed tree: `make fmt-check`, `make cross`, `staticcheck`, and the `ta` VM test's `RESULT: PASS` line (see the commit that records them).

## Documented limits

- Instance (the instance half of the round-2 finding on markers). The card has no instance to compare with: the wire carries the commander's `map` and the rally's `rally_inst`, but neither the commander nor a member has an instance (the serve status and the member records carry none). So a rally stored in another instance of the shown map is still drawn, and a member that stands in another instance of the shown map is still drawn. Follow-up: the serve status and the member record must carry the instance before the card can compare it.
- The round-1 lows (the cold review and the hydrogen review) are fixed on this branch: the slot and rally hover text, the place words checked through `CmdLine`, the nil check before the reply is read, `inst=` kept apart from the map, a typed facing number taking precedence over Auto, and the controls check asserting the exact request body. None is left open.

## Live checks (not run in this round — no live server)

Section 4 row 10, through a running serve (needs the M2 module on the server): the Positions panel — Place (here, circle), the map shows a slot square per bot and the lines vanish as the bots arrive; Map point → Place at a clicked point; Rally at point (flag drawn); Fall back; Resume; a `place` while a run is running answers `run-active` on the page. Rows 1-9 belong to the M2 round.

## Gate lines

The gates for the round-2 fixes are on the PR: its `RESULT: PASS kind=ta sha=...` line names the tree that ran. The first gate run, on tree `ed514cd3`:

```
$ make fmt-check
(no output)
$ make cross
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./cmd/... ./internal/forever/...
$ staticcheck ./...
(no output)
$ vm-test ta
RESULT: PASS kind=ta sha=ed514cd3 dirty=0
```
