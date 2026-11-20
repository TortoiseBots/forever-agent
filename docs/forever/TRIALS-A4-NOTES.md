# Trials round A4: tank_damage, arenas, teardown and the first live trial

`internal/trials/live` now runs a real `tank_damage` repeat on a live
`forever serve`: the situation takes an arena, summons, pulls, governs the
victim's health, stamps the fight's marks and tears the repeat down. The
Forever data overlays (`internal/trials/data/forever/`) carry the rows the
live server proved. Priest-holy-heal-latency ran twice on the test server;
the verdicts are in this file's last section.

## Layout

| file | mirrors | what |
|---|---|---|
| `situations.go` | `trl/live/situations.py` (`Situation`, `Governor`, `summon_mob`, `pick_entry`) | `LiveSituation` dispatches on `situation.kind`; `tank_damage` only this round. Arena, summon/claim, level window, pull, refill, the HP governor, `fight_start`/`fight_end`. |
| `arena.go` | `trl/live/arena_lock.py` | arena rows with the Forever overlay merge, `pickEntries`, the level window ranking, and one flock per (server, arena) under `$XDG_STATE_HOME/forever-trials` |
| `teardown.go` | `trl/live/teardown.py` | repeat teardown (role clear, `gm_kill` the ledger, `.unaura all` on self, out-of-combat wait, heal full), trial teardown (stay/free/logout the hires) |
| `internal/trials/data/forever/*.json` | — | overlay rows merged over the copied tables: `arenas.json`, `mobs.json`, `gear_kits.json` (bot kits), `specs.json` (spec markers), `items.json` (name -> id) |

`guard.go` gained the typed unit hands (`Kill`, `SetHP`, `RemoveAura`) and a
unit scope: every hand registers the trial's guids with `trial_units` first,
a kill only finds ledgered summons, and each raw line the core reads off the
selection selects its target and waits for the server's echo.

`client.go` gained `TrialUnits`, `GMKill`, `GMSetHP`, `GMAura`, `Target`,
`Attack`, `StopAttack` and the units filter (`entry`, `name`).

## Decisions

- `.modify hp <hp>` sets current AND maximum health on this core, so a player
  floor rescue passes the character's own maximum (a full refill) and never a
  band percentage. A hire is refilled and damaged through the module's own
  `.tb hurt <bot> <pct>`, which leaves the maximum alone (the serve refuses
  GM hands on another player in any case).
- The arena's quiet probe uses `units within 40` (the serve's unit row has no
  faction): a live creature or a stranger in reach is "not quiet". Stricter
  than the module's own probe, and it never trusts a refused read.
- `mobs_ready` is taken after the pull: alive, the summoned entry, level in
  the window, and in combat. Combat is the hostility proof (the row has no
  faction field).
- A `situation` option this round does not port (`debuff`,
  `prepull_unaura`, `reset_hire_cooldowns`, `energy_drain`,
  `min_rep_gap_s`) makes the repeat INVALID with the option's name, never a
  silent fight.
- The lease store's port probe is off for the Forever runner
  (`live.ServeProbe`): the slot's port is the serve's own RPC port and the
  serve is started before the run, so a listening port is the slot being
  ready. The serve dir's `slot.json` and token remain the gate.

## Forever data the live server proved (2026-11-20)

| Row | Value | Proof |
|---|---|---|
| item "Sweet Nectar" | entry 1708 (the copied table's 17083 does not exist here) | `.lookup item Sweet Nectar`, `.tb itemscan Bheal 1708` exists=yes |
| priest/holy spec marker | talent 14889 "Holy Specialization" rank 1 | `.lookup spell Holy Specialization`, `.tb learn Bheal 14889` result=learned |
| priest `blue` kit (level 40) | 9 cloth pieces (7720, 19507, 9434, 10771, 9407, 13100, 274380, 9390, 10776) | `.tb itemscan Bheal ...` exists=yes canequip=yes, `.tb equip ... replace` result=equipped |
| mob entry 2734 | "Ridge Stalker Patriarch", level 40 (one level under the copied table's 40-41) | `.npc add temp 2734`, `units entry=2734` level 40 |
| test account | the account's RBAC role needed level 3 (see below) | the GM probes |

## The first live trial (2026-11-20, forever-test1)

`priest-holy-heal-latency` ran twice, three repeats asked, against a live
`forever serve` on the test server. Both runs end **SKIPPED
needs=bot_engine**: the module acknowledges `.tb engine <bot> on`, but the
hire's state stays `engine=no, stuck=noengine`, so the bot runs no
strategies and would never heal. The trial stops after the setup round with
`setup.json` `skipped {needs: bot_engine}` and `RUN.json` naming it; a
verdict table with a PASS or FAIL would be a claim the server cannot back.

What the runs did prove, from the setup rows:

| row | result |
|---|---|
| `player_level` | ok, level 40 read back |
| `player_talents` | ok, `.learn` accepted 16462 and 12286, `.tb learn` read back `learned\|known` |
| `player_stance` | ok, aura 71 held after the cast |
| `player_visible` | ok, `.tb state Bpriest` reads a numeric `dm` after `.tb gm <player> off` and `.gm visible on` |
| `player_forced_role` | ok, `.tb role clear` reads back `role=none` |
| `bot_present`, `bot_level`, `bot_role`, `bot_spec`, `bot_items` | ok (party roster guid, level 40, role healer, talent 14889 learned, the kit scanned and worn) |
| `config_read` | false (no threshold source; defaults 20/50/70), non-fatal as in the Python |

The fight never started: the engine gap ends the setup before the arena.
`forever-agent trials compare` over the two runs prints one row:

```
| priest-holy-heal-latency | PRI-HEAL-LATENCY | INVALID | INVALID | - -> - | 1.0000 | no evidence |
```

Both runs match. `run-a/RUN.json` carries `skipped: {"priest-holy-heal-latency": "needs=bot_engine"}`;
nothing in either card claims a PASS or a FAIL.

## What went live, not in a fake (and had to be fixed)

- The lease store's port probe refused the slot the serve was serving on
  (`live.ServeProbe`).
- The chat log's tokenless replies started at seq 0 and read the whole
  session's chat, so an old answer looked like the current command's
  refusal; the runner now keeps one shared cursor, advances it on every
  reply (the `bot` RPC's own lines have no seq) and drains it.
- The module's answers name the bot; a reply is narrowed to the lines that
  name the command's subject, and the GM probe's help block is dropped.
- Unit rows carry no names: the hire's guid comes from the serve's `party`
  roster.
- A pool bot reused across runs can be left dead: the setup revives it
  (`.tb revive`) and refills it before anything is measured.
- `.levelup` moves the level BY its argument and answers no line: the row
  re-reads the level, polls for the move and never re-applies a delta a
  retry already applied.
- One game account cannot hold two sessions on this core: a bot logged in
  on the master's account kicks the master off, and a kicked session's
  reconnect cannot reuse its join ticket. The bot pool must live on its own
  account (the slot config's pool names), which the live run used.

## Environment findings (2026-11-20, forever-test1)

- The console's `.commands` list is a reduced table (no `npc`, `go`,
  `modify`, `learn`, `levelup`, `damage`, `die`, `aura`). A *player* session
  still runs them: `.learn`, `.levelup`, `.cast`, `.go xyz`, `.npc add temp`,
  `.revive <name>` all worked from the session. `.npc add temp` and
  `.learn` answer no line (the serve reports "the server sent no reply") but
  have their effect, proved by `units` and `.tb learn`'s read-back.
- `.learn` takes one spell id; `.learn 16462 12286` answers "Expected 'all',
  got '122'". Two ids need two lines (the runner already sends one each).
- The RBAC role for security level 2 on this box carries only a handful of
  `Command:` permissions (`.npc add temp`, `.gm visible`, `.modify hp`,
  `.modify mana`); `.learn`, `.levelup`, `.damage`, `.die`, `.aura` and
  `.additem` are in the level-3 role only. The test account was raised to
  level 3 for the run (console `account set gmlevel <account> 3 -1`, then a
  world restart so the cached rank loads). The rank is the test account's
  alone.
- Unit rows carry no `name` for creatures (the model has no creature-query
  names), so report lines name an entry, never a creature name.

## Out of scope (A5)

The other situation kinds (`CasterMob OOCBuffs OOCHurt Debuff CCTarget
Pack`), their options, the adaptive ladder, `trials forever-data-check` and
`trials sweep`.
