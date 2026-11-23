# Raid, round R3: `forever raid-watch` (#108)

Scope: `internal/forever/driver` (the raid plan mode, the party size, the pre-PREPARE raid step, the raid summary),
`internal/forever/world` (the two port calls over the session's group layer, the raid plan load) and
`cmd/forever-agent` (`raid-watch` on the watch command's own flags). R1 is the party model and the packets (PR #174),
R2 is `convert_raid`/`raid_move` on the serve; nothing here runs a live raid yet (R5 does).

## A raid watch is the watch machine, not a port of `raid-walk`

`forever raid-watch` is `forever dungeon-watch` over a raid plan: PREPARE, ENTER, the module's own run, the five
safety budgets, the practice rescue, the trace and the run summary are unchanged, and this round adds only what the
group being a raid changes. Everything else `raid-walk` does (hire, place, GM clear, practice, prepull auras, boss
decisions) is out of scope and stays Turtle-side.

## The plan (`driver/plan.go`)

| Piece | What it does |
|---|---|
| `LoadRaidWatchRoute` / `NewRaidWatchRoute` | a watch route (`modeRaidWatch`): the plan's in-fight fields are the module's own inputs, as for `dungeon-watch` |
| `Route.Raid` | the plan's raid block, copied (size 10/20/40 and the role split) |
| the kind gate | a raid plan is refused by every other mode (`dungeon-run`, `dungeon-watch`, `dungeon-play`); `raid-watch` refuses a plan that is not kind `raid` or has no raid block |

`dungeon.Load` already requires the raid block and a split summing to the size; the route keeps the block so the
watch can check the group against it.

## The party and its size (`driver/run.go`, `driver/states.go`)

`Config.Raid` (implied by `New` to mean `Watch` too) makes the watch's group the plan's raid:

- `Run.partySize()` is the plan's raid size for a raid watch, five otherwise.
- The five-member checks read it: a complete state report, the roster's size, "everyone at the portal", "everyone
  outside" and the wipe verdict (the whole party dead).
- `adoptRoster` adopts the plan's raid size, orders it tank first, healer next, then the remaining tanks, healers and
  damage dealers (so `Tank()`/`Healer()` keep their meaning), and needs **at least** one tank, one healer and one
  damage dealer. It does not require the plan's exact split: the plan's split is a placeholder until a raid has been
  run (`dungeon.RaidSpec`'s own comment). A split mismatch is a note for R5, not a refusal.
- The scripted world's raid party follows the same shape (`Fake`'s `watchPartyNames`), ten bots for a 10-raid.

## The pre-PREPARE step (`driver/raid_watch.go`)

Before `prepare`, `raidSetup` reads the module's commander card (`CommanderStatus`, the card of the roster's
commander):

1. Not a raid or short of the plan's size, and this character leads the group (`playerled` on the card), and the card
   holds enough members: one `convert_raid` goes out (the group layer waits for its roster readback, R2), then the
   card is read again. The layout never goes on the pre-convert card.
2. Still short of the plan's size: stop `raid:roster` (the two numbers are logged). Still not a raid: stop
   `raid:not-raid`. No card: `raid:card`. The group layer's refusal text on `convert_raid` is logged and the end is
   `raid:convert`.
3. `--layout` entries then go out with `raid_move` in file order; a member already in its subgroup sends nothing, a
   name the card does not hold stops the watch (`raid:layout`) without sending, and a refused move stops it with the
   group layer's text. Subgroups are 1-8 as the raid frame numbers them.

`--attach` skips this step: a watch that attaches to a run another client commands must not convert or move its
group.

The layout file: `NAME SUBGROUP` or `NAME = SUBGROUP`, `#` comments and blank lines, one entry per bot, a repeated
name and a subgroup outside 1-8 refused with the line number.

## The two port calls (`driver/ports.go`, `world/dungeon_session.go`)

`Commander` gains `ConvertRaid(ctx)` and `RaidMove(ctx, name, subgroup)`. The live adapter calls the session's group
layer directly (`convertRaid`, `raidMove`); they are the client's own packets, not `.tb` lines, and they run before the
module's run starts, so the watch's "no order after the start" rule is untouched. The fake scripts them (`Raid`,
`PlayerLed`, `Subgroups`, `CardLimit`, `ConvertErr`, `MoveErr`) and records every card read, convert and move in
`RaidOps`, which is how the tests pin the order.

## The summary (`driver/raid_watch.go`)

Every end of a raid watch with `--out` writes `<out>/summary.json` in `internal/raidwalk.Summary`'s tb-raid-walk v1
shape: `format` (`tb-raid-walk`), `version` 1, `plan`, `map`, `server` (empty: a Forever summary names no server),
`slot` (0), `character`, `started`, `ended`, `end_reason`, `steps_reached`/`steps_total`, `roster` (mt/ot/rest from
the roster's roles), `layout` (mt/tanks), `boss_kills` (entry, killed from the instance report, the evidence text),
`deaths.total` and `notes`. The type is redeclared in the driver because `raidwalk` imports the world session, which
imports the driver: an absent field reads as its zero value, so the Turtle tools decode the file with their own type
(`TestRaidWatchSummaryReadsAsRaidwalkSummary` is that decode). The file carries the raid's bot names (the tools match
their rows by them); no host, account or path ever reaches it.

## Tests

`internal/forever/driver/raid_watch_test.go`, dry mode over `driver.NewFake` with a ten-bot raid plan:

- `TestRaidWatchDryRunReachesEnd`: ten bots, the group already a raid, the scripted run to its end; no convert, no
  move, and PREPARE accepts the ten-member report (a five-member check would stop it at `prepare:report`).
- `TestRaidWatchConvertsWhenNotRaid`: `playerled`, not a raid yet, a layout file; exactly one `convert_raid`, the card
  re-read after it, then the one move the layout needs (`Tank` is already in subgroup 1). The recorded order is
  `card, convert, card, move` — a layout sent before the re-read fails the test.
- `TestRaidWatchRefusesShortRoster`: an eight-member card stops the watch at `raid:roster` with both numbers logged,
  and no run start goes out.
- `TestRaidWatchAppliesLayout`: two of three entries move (the third is already in place), in file order.

`cmd/forever-agent/forever_raid_watch_test.go`:

- `TestRaidWatchSummaryReadsAsRaidwalkSummary`: the command in dry mode, a layout file, `--out`; the layout is logged,
  and `summary.json` decodes into `raidwalk.Summary` with the plan, map, `end_reason` = done, steps, started/ended,
  roster and layout.

Mutations this round catches: the size check against 5; a layout sent before the convert's readback; a summary
without `end_reason`; the raid plan accepted by `dungeon-watch` (the kind gate); a group already a raid converted
again.

## Not this round

`raid-autopilot --forever` (R4) builds the command; the live raid (R5) needs a module that starts a `kind: raid` run
(`.tb run start`). If the module refuses, the watch reports its refusal text as `start:<code>` and the run waits for
module support.
