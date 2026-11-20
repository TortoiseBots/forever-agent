# Round A1: the combat log, auras and GM on one unit

`forever serve` now answers the three inputs the trials runner needs from a running client: the session's combat
log, a unit's auras, and GM hands that act on one named unit. The ring itself is the one round B1 put in the world
session (decision 2); this round exposes it and adds what the runner reads beside it. No second ring was built.

## `events`

`events {since, limit, wait_ms}` → `{events, next, oldest, dropped}`.

- The rows are the session's own `CombatLog`: exactly the trials runner's raw row (`seq kind time mono_ms
  source{guid,name} target{guid,name} spell_id amount`, plus `reason` on a `spell_fail`). The same ring `forever
  dungeon-watch` drains into `<run_id>.combatlog.jsonl`, so one file and the RPC cannot disagree.
- `since` is exclusive; `next` is the last row's seq (or `since` back when nothing is above it, so a caught-up
  poller never re-reads a page); `oldest` is the lowest seq the 4,096-row ring still holds and `dropped` how many
  rows fell out above `since`.
- `limit` defaults to and is clamped at 1,000. `wait_ms` is a long poll (0-2,000): an empty page waits for a row
  instead of answering at once, the same shape `chat_log` uses.
- It is a read: it answers while the link is `reconnecting` and `offline`, from the last session's ring.

**The ring outlives a reconnect.** The old session is torn down, so the first login of the replacement session
cannot write to it; before the new session is installed the loop hands it the dropped session's ring
(`serve.go`, the reconnect path). A reader's seq from before the drop still points into the same log and no seq is
ever reused.

## Auras and the clock on the read models

- `ServeUnit.auras` is `[{spell_id, caster, stacks, remaining_ms}]`, in slot order, from
  `objects.World.AurasOf` at the read's moment: only auras still up, `remaining_ms` 0 for one with no duration.
  `caster` is the same `0x` hex text as every other guid. An empty list is `[]`, never `null`.
- `status.mono_ms` is `events.MonoMS()` at the answer: the steady axis every combat row's `mono_ms` is on, so a
  sampler can stamp its samples on the rows' clock.
- The aura rows themselves were already fed by the dispatch in B1 (`aura_applied` on a gained slot,
  `aura_removed` on a cleared one, nothing on a refresh). This round pins that with a dispatch test
  (`TestAuraDiffRows`).

## GM on one unit

`gm_kill {guid}`, `gm_set_hp {guid, hp}`, `gm_aura {guid, spell, remove}`.

Each is one session-goroutine call: resolve the unit in the model, send `CMSG_SET_SELECTION`, wait up to 1 s until
this client's model shows it in the character's own target field (the server's echo of the selection; on timeout the
call is `-32003 selection not confirmed` and no line is sent), then run one line through the existing GM guard and
command channel:

| method | line | notes |
|---|---|---|
| `gm_kill` | `.die` | a player guid is refused: the line kills a character, and this hand exists for summons and creatures |
| `gm_set_hp` | `.modify hp <hp>` | `hp` is required, 1 to 2,147,483,647; the core's command sets the selected player's current **and maximum** health to that one value (`cs_modify.cpp:137-149`), so passing the unit's own maximum is a refill that leaves the maximum alone |
| `gm_aura` | `.aura <spell>` / `.unaura <spell>` | `spell` must be positive; `remove` picks `.unaura` |

The result is `ServeCommand` (`sent`, the server's `lines`, `complete`, `end`), so a caller can log the line it
caused and what the server said. An unknown guid sends nothing at all, not even the guard's `.gm` probe.

The same boundary covers the raw `gm` method (and `gm_revive`, which sends `.revive <name>`). A raw line the core
runs as one of the unit commands — `.die`, `.kill`, `.damage`, `.modify hp/mana/rage/energy`, `.aura`, `.unaura`,
`.revive` — is checked before it is sent: the `--allow-gm-units` flag, the `trial_units` allowlist, and the player
rule, with a typed `-32006` and nothing sent on refusal. The classification is the core's own (case-insensitive,
the core's command-name abbreviations, one command per line), and every selection the line could act on is checked:
the one this session last sent and the one the server last echoed, because the core applies those on different
threads. Ordinary lines (`.tb ...`, `.gps`, `.server info`) and plain chat keep working with no flag. `.revive`
refuses every name but the session's own character, through `gm_revive` too.

The select-then-line wait is the old daemon's rule (the core applies a selection on the map thread and a GM line on
the world thread): a line sent immediately behind the select could act on whatever the server had selected before.

## Tests

- `TestServeEventsKinds`: the fake sends one packet of each kind (spell_start, spell_go, spell_fail, melee,
  spell_damage, periodic, heal, aura_applied, aura_removed, death); the rows come back in order with their kinds,
  guids, spell ids, amounts, a reason on the failure and a `mono_ms` on every row. The first failing test: before
  this round `events` answered `-32003 not ported`.
- `TestServeEventsPaging`: 5,000 rows against the 4,096-row ring → the first page starts at 905, reports 904
  dropped and 1,000 rows, and the pages end at 5,000; a limit above 1,000 is clamped, a negative limit and a
  `wait_ms` above 2,000 are refused.
- `TestServeEventsAcrossReconnect`: a scripted reconnect; the row after the re-entry is seq 2, never 1.
- `TestServeEventsKeepTheLoginBurst`: a melee row written before `SMSG_LOGIN_VERIFY_WORLD` (inside the login burst)
  is kept across the reconnect at the next seq, beside the row the session writes after its login.
- `TestServeRawGMRefusesUnitCommands`, `TestServeRawGMPassesOrdinaryCommands`, `TestServeRawGMRevive`: the raw `gm`
  boundary; `.die` on an unregistered unit was sent before the fix and is refused after it.
- `TestServeUnitAuras`: the aura's spell, caster, stacks and a remaining time inside the record's window.
- `TestAuraDiffRows`: gain is one row, refresh none, removal one.
- `TestServeGMKillSelectsFirst`: an unknown guid sends nothing; on a real unit the selection packet precedes the
  `.die` line; the four hands select what they name; missing `hp`, a zero spell and a player guid are refused.
- `TestTrialAuraBuildersParse` keeps the hand-built packets on the parser's own shape.

## What to check on the first live run

1. That the server echoes a selection in `UNIT_FIELD_TARGET` for a creature the character selects by guid; without
   the echo every GM hand stops at `selection not confirmed` after 1 s.
2. That `.die` kills a summoned creature (not just a player) and that the death row arrives in the same page as the
   GM answer.
3. That a bot's `.modify hp <max>` (the refill) leaves its maximum where the model shows it; the core sets both to
   the argument.
4. That the event page keeps up with a real fight: ten rows a second is a third of the ring per minute.
