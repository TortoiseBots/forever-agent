# Trials setup toolkit and provisioning (round A3)

`internal/trials/live` gains the real setup: the `.tb` test-kit client,
the hire-pool provisioner, the player-side setup and the 22-row check
round. `--dry` keeps the scripted setup/situation; without `--dry` the run
uses the live A3 setup with a nil situation (A4 ports tank_damage).
`trials run` without `--dry` now provisions for real against a live serve.

## Layout

| file | mirrors | what |
|---|---|---|
| `tbrunner.go` | `trl/live/agent.py` (transport) | `.tb` Runner over serve RPC: `bot` sends the line, `chat_log` drains to the token's end marker; tokenless reads to the quiet window |
| `bots.go` | `trl/live/setup.py` hire path | pool login, engine on, level/train/talent-kit/gear/role/party/mana, every mutation proved by a typed read-back |
| `player.go` | `trl/live/setup.py` player path | `.levelup` delta, `.learn` kit, stance by `cast` (aura held), `.tb gm off` + `.gm visible on`, forced role through TB-1 no-name `RoleSelf` |
| `setup.go` | `trl/live/setup.py` `run_setup` | one function per setup-check id (22 ids), failed rows retried once, `config_read` false with 20/50/70 defaults; `not-a-bot` SKIPs the trial |
| `../tbcmd/testkit.go` | TB-COMMANDS §6+§8 | typed `Learn Equip ItemScan Kill GM` over the v2 grammar (mirrors `Order`/`Role`/`State` parsing, counts checked) |
| `../tbcmd/testkit_free.go` | TB-COMMANDS §2 | free-text `Create Login Logout Group Ungroup Engine Level Train Hurt Power AddItem`, each trusted only after a typed read-back |
| `data/specs.json` | trial `player.talents` + markers | talent spell ids per class/spec (warrior/arms + druid/balance pinned by trials; other specs their marker rank-1 spell) |

## Seams

- ` TBRunner` implements `tbcmd.Runner`: the serve `bot` RPC both sends
  and answers; when its lines lack this run's end it drains `chat_log`
  from the cursor. Method errors (`-32601`) fail loud, never silent.
- `tbcmd.Client.GameMaster` is the typed `.tb gm` (named so the live
  package's "one `.GM(` call site" test keeps pinning `Client.GM` as the
  guard's only send path).
- `LiveSetup.TBRunner` overrides the transport in tests; production uses
  the serve-backed runner over `Client`.
- `RoleSelf` sends the no-name form (`.tb role tank`): `Role` refuses an
  empty target through `targetWords`, and `clear` is accepted (TB-1 doc).

## Decisions

- Bot talent kits are learned spell ids through typed `Learn`
  (`learned|known` ok), not the Python whisper-premade path: the bench
  needs a typed record, and TB-1's `.tb learn` learns on the named
  character with the same core path as `.learn`.
- `player_gear` is not provisioned this round (no Forever item source for
  the carry-over/strip path): the row is ok=false/INVALID with the reason,
  never a silent pass. `player_no_shield` fails closed (ok=false,
  UNVERIFIED): the serve unit row carries no equipment slots and
  `equipment` is NOT-PORTED, so an unreadable off-hand is an unverifiable
  REQUIRED row — INVALID, never PASS.
- The `blue` bot kit has no Forever item source (gear_kits.json covers
  warriors only): a spec that wants worn gear returns ok=false with SKIPPED
  needs=bot_items (a required row that is SKIPPED makes the trial SKIPPED,
  never PASS). Consumables still grant then prove exists=yes through typed
  `ItemScan`. On an `extra_bots` hire the SKIPPED kit is noted on
  `extra_bots_present` and does not fail it (2026-11-24: a level-60 druid's
  missing kit failed a hire that was logged in, grouped and at the trial
  level; the main hire's `bot_items` row is a SKIP of its own and a trial
  that does not list it still runs).
- `player_visible` proves against a named bot's `.tb state` with a numeric
  `dm` after the `.tb gm off` + `.gm visible on` commands; without a hire
  the row stays failed (UNVERIFIED) and the setup round finalizes it after
  provisioning.

## Tests

Fake answers serve `bot` with the doc's real v2 records (token stamped per
call) and `chat_log` with the free-text acceptances; `status`/`units`/`unit`
carry the read-backs (level, auras, hp).

| test | pins |
|---|---|
| `TestSetupPriestHealLatencyRows` | bench rule end to end: the kit-less doc SKIPs with needs=bot_items, never a pass without a scan/equip read-back |
| `TestBotGearNeedsScanAndEquip` | gear passes only on a scan/equip read-back; no kit source SKIPs |
| `TestShieldUnreadableIsNotOk` | unreadable shield state is ok=false (UNVERIFIED), never a pass |
| `TestVisibleNeedsStateReadBack` | commands alone fail; no numeric `dm` in State keeps the row failed |
| `TestVisibleFinalizesAfterHire` | Run re-proves visibility against the hire: numeric `dm` passes |
| `TestEquipRefusedRow` | a refused equip is row evidence (core's `why`), never a silent pass |
| `TestLearnKnownIsOk` | `learned\|known` ok from specs.json |
| `TestFreeTextReadBack` | a `level` reply contradicted by units fails the row |
| `TestSkipWhenNotABot` | `not-a-bot` on the own-character role SKIPs with needs=player_forced_role |
| `TestTestkitParse` | each call incl. `error` lines |
| `tbcmd` `TestTestkit*` | learn/equip/itemscan/kill/gm records, no-name forms, refusal shapes |

## Out of scope (A4-A5)

tank_damage and other situations, arenas/mobs overlays, teardown kills
through `gm_kill` selection; live runs (needs a `forever serve` per slot,
which the runner does not start).
