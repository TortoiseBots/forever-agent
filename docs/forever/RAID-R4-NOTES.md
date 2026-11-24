# Raid, round R4: `raid-autopilot --forever` (#108)

Scope: `cmd/forever-agent/raid_autopilot_cmd.go` (the mode flag, the Forever world the targets resolve against, the
launch and its lease) and `internal/raidwalk/autopilot.go` (the Forever targets read). R1 is the party model and the
packets (PR #174), R2 is `convert_raid`/`raid_move` on the serve, R3 is `forever raid-watch`; the autopilot round runs
against the fake binary. Nothing here needs a live raid server (R5 does).

## A target line is the command it launches

| Mode | One attempt |
|---|---|
| Turtle (no flag, unchanged) | `raid-walk <args> --server --slot --holder --until --out [--from --place [--reuse-instance]]` |
| `--forever` | `forever raid-watch --plan <first arg> <rest of args> --rest --rpc --ca --account --character --serve-dir <out>/slot --out` |

The header is the same (`id server slot holder goal -- <args>`), and the plan stays the first argument after `--`, as
raid-walk takes it positionally. `--leader` is required: the watch starts the module's run as that bot.

The Forever line refuses what the watch has no counterpart for, at target load (they are not silently dropped):
`--place`, `--reuse-instance`, `--gm-instance-data`, `--from`, `--boss-wipe-max`, `--boss-wipe-fresh`,
`--wipe-end`, `--sql` and `--until`; `--server`, `--slot`, `--holder`, `--out` and `--account` are managed (the
lease, the slot's account and the attempt's folder). A practice goal is refused: the watch's `--practice` is the wipe
rescue, not a mechanic cycle loop. `--layout`, `--enter`, `--attach`, the budgets and the report-card thresholds pass
through.

## The slots file

`--forever` loads the slots table (`--slots FILE`, the standard file by default) once, at target load:

- a target's server must carry a complete `forever` block (`rest`, `rpc`, `ca`; `build` when the block names one):
  those are the watch's connection flags;
- its slot must exist, not be reserved, and supply the account (and the character, unless the line names
  `--character`);
- a party server (no `forever` block) is refused, as a Forever target without the flag still is by the Turtle path.

Every refusal names the target's line number. `--slots` without `--forever` is a usage error. raid-autopilot is a
both-stack command now (the help no longer marks it Turtle only): a Forever target without the flag is refused with
"pass --forever", the same refusal `slot setup` and `patrol-record` give.

## The lease and the slot dir

Per attempt, as raid-walk does it in-process:

1. `slots.Store.Acquire(server, slot, false, holder, 30m)` — the supervisor is the lease's holder process
   (`HolderPID`), so a supervisor that dies frees its slots at once; the same holder extending an earlier attempt's
   lease renews it (the store refuses a second acquire by the same holder).
2. `slot setup --forever <server> <slot> --holder --dir <out>/slot --binary --slots --character` — the leased dir
   the watch's preflight checks, with no settings file (it holds a secret the serve never reads).
3. The watch starts with `--serve-dir <out>/slot`.

A live attempt renews its lease every 5 minutes (the store's own record names the lease id, so an adopted attempt
renews the same lease). The teardown is the existing one (`<binary> teardown <out>/slot`, twice with
`--release-anyway` when the first is unclean) and releases the lease through the manifest.

A lease held by another holder fails the launch (the launch backoff, and the third failure escalates the id); the
startup reconciliation refuses an id whose slot is held by another holder, as for a Turtle walk. A Forever watch
writes no `walk.pid`, so the checkpoint's child pid is the adopted run's liveness evidence.

## Where the run ends

The module's own run decides. The autopilot keeps its goal decisions from `summary.json` (advance on a met goal,
restart otherwise) and its exit handling, but an alive Forever watch counts as progress: the watch keeps its progress
in the module's run, writes no `run.log` or frames the autopilot reads, and its `--watchdog-minutes`/`--max-minutes`
end a stuck run by themselves — the no-progress rule would only tear down a healthy fight. `--from`, `--place` and
`--reuse-instance` are never sent, so a restart starts the goal's run again.

## Tests

- `TestParseAutoTargetsForever` / `TestParseAutoTargetsForeverHeldKeepsTheSlotRule` (`internal/raidwalk`): the plan
  first, `--leader` required, every raid-walk flag refused, practice refused, the one-walk-per-slot rule kept.
- `TestAutopilotForeverLaunchesRaidWatch` (`cmd/forever-agent`): a `--forever` run builds `<out>/slot` with
  `slot setup --forever` (manifest names the leased server/slot/holder, no settings file) and launches
  `forever raid-watch` with the plan, leader, account, character, connection flags and `--serve-dir`, without
  `--until`/`--place`/`--reuse-instance`/`--from`; the attempt's summary advances the goal to done.
- `TestAutopilotForeverRefusesPlace`: `--place`, `--reuse-instance`, `--gm-instance-data`, `--from` and `--sql` are
  refused on a Forever line; `--slots` without `--forever` is a usage error.
- `TestAutopilotTurtleUnchanged`: the Turtle command is the pinned raid-walk line, and a Turtle line still takes
  `--place`/`--reuse-instance`.
- `command_scope_test`'s raid-autopilot case: a Forever target without `--forever` is refused with "pass --forever"
  (before the dry run's first line), and a Turtle target still reaches its raid-walk command.

Mutations the tests catch: dropping `--leader`; passing `--until`/`--from`/`--place` to the watch; launching
raid-walk on a Forever target; skipping `slot setup --forever` (no manifest in `<out>/slot`); writing settings.json
on a Forever slot; accepting `--slots` without `--forever`.

## Not this round

The live raid (R5): one `raid-watch` on a server whose `forever_status` (#102) is `exported`, then one autopilot
target. Whether the module starts a `kind: raid` run is still open (R3 notes); the watch reports the module's own
refusal text.
