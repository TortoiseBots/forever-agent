---
type: Guide
title: Trials
description: "Run the behaviour test bench: hire a bot into a scripted fight, score each specified behaviour, and read the card."
tags: [testing, trials, behaviour, verdicts]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:15Z }
sources:
  - id: trials-cmd
    resource: cmd/forever-agent/trials_cmd.go
  - id: trials-run
    resource: cmd/forever-agent/trials_run_cmd.go
  - id: trials-sweep
    resource: cmd/forever-agent/trials_sweep_cmd.go
  - id: trials-cleanup
    resource: cmd/forever-agent/trials_cleanup_cmd.go
  - id: trials-datacheck
    resource: cmd/forever-agent/trials_datacheck_cmd.go
  - id: score
    resource: internal/trials/score/score.go
  - id: scenario-schema
    resource: internal/trials/scenario/scenario.go
  - id: live-run
    resource: internal/trials/live/run.go
  - id: trial-sample
    resource: internal/trials/trialdefs/healers/paladin-holy-heal-latency.json
---

The trials bench is the behaviour test: it leases a server slot, hires one bot of a chosen class and spec, puts it in a scripted fight (a tank that takes damage, a caster mob, a pack, a debuff, a crowd-control target) and checks a named behaviour against the fight's event stream. Each rule gets a `PASS`, `FAIL` or `INCONCLUSIVE`, and a run folder keeps every raw row so the same run can be re-scored later without a server.

The bench lives in `forever-agent trials` and is a Go port of the Python trials tool: the run folder layout, the scoring rules and the exit codes are the same, and a differential test keeps the two scorers equal on a hand-made table.

# Subcommands

| Command | What it does |
|---|---|
| `trials run` | lease a slot, run each trial `--repeats` times, then score the run |
| `trials score` | score an already finished run folder |
| `trials report` | re-render `card.md` and `card.json` from a run's `trial.json` files and print the card |
| `trials compare` | before/after table of two run folders with Fisher's exact p |
| `trials validate` | schema-check trial files (default: the embedded set) |
| `trials sweep` | class sweep: tanks first, one run folder and card per trial, plus `sweep.md` |
| `trials cleanup` | replay a run's ledger: kill its summons, clear auras, free bots, release leases |
| `trials forever-data-check` | ask the live server every spell, item and mob entry the trials name |

# A trial file

A trial is one JSON object. Unknown keys at any level are a validation error, so a typo is caught before a server is touched.

| Field | Meaning |
|---|---|
| `id` | file stem; lower-case letters, digits and dashes |
| `title` | one sentence for the card |
| `group` | `healers`, `tanks-melee`, `ranged` or `raid` |
| `class`, `spec` | the hired bot's class and spec |
| `player` | the human-side character: class, level, talents, stance, forced role, auras, items, gear, mana |
| `bot` | the hire: class, spec, role (`tank`, `healer`, `dps`), level, gear, items, mana |
| `situation` | the fight script; `kind` defaults to `tank_damage` when absent |
| `setup_checks` | the provisioning steps that must succeed before the fight counts |
| `duration_s` | the fight's length |
| `repeats` | how many times to run it (at most 100) |
| `min_valid_s` | a repeat shorter than this is `INVALID` (`cut short`) |
| `rules` | the behaviours judged, each with its own id, check, `pass_if` and `expect_today` |
| `extra_bots` | up to three extra hires (each needs `class`, `spec`, `role` and `level`) |
| `variants` | up to six named overrides of one rule's `pass_if`, `min_n`, `min_reps` or `min_opportunity_s` |

Situation kinds the live runner drives: `tank_damage`, `caster_mob`, `pack`, `cc_target`, `debuff`, `ooc_buffs`, `ooc_hurt`. The dummy spell/damage bench is the separate `dummy` kind.

A rule has `id`, `desc`, `source`, `expect_today` (`PASS`, `FAIL` or `?`), `why_expect`, `check`, and `pass_if`. The check says what is measured (`metric`: `count`, `ratio`, `latency`, `uptime`, `first`, `rate`, `walk_metric`), which trigger and response events it pairs, and `within_s` for a latency. `pass_if` is a comparison such as `>= 0.8`; `min_n` and `min_reps` say when there is too little evidence to judge. `pool: true` scores one verdict from the summed k/n over every valid repeat instead of a majority vote.

```json
{
  "id": "paladin-holy-heal-latency",
  "group": "healers",
  "class": "paladin",
  "bot": {"class": "paladin", "spec": "holy", "role": "healer", "level": 40, "gear": "blue"},
  "situation": {"kind": "tank_damage", "mobs": {"role": "melee", "count": 1, "level_delta": -4, "refill": true}},
  "duration_s": 180,
  "repeats": 4,
  "min_valid_s": 120,
  "rules": [
    {
      "id": "PAL-HOLY-HEAL-LATENCY",
      "expect_today": "PASS",
      "check": {"metric": "latency", "trigger": {"kind": "cross", "unit": "$player", "below": 50},
                "response": {"kind": "cast", "src": "$bot", "dst": "$player", "spell": ["Holy Light", "Flash of Light"]},
                "within_s": 4},
      "pass_if": ">= 0.8",
      "min_n": 5,
      "min_reps": 3
    }
  ]
}
```

# Running the bench

```sh
forever-agent trials run --slots ~/forever-agent/slots.json --server example.invalid \
    --any --repeats 2 --out ./runs/2026-11-24-paladin \
    healers/paladin-holy-heal-latency
```

A trial argument is any of: the embedded path (`healers/paladin-holy-heal-latency`, with or without `.json`), a bare embedded id, a path to a `.json` trial file, or `<trial>@<variant>` to run one variant.

| Flag | Default | Meaning |
|---|---|---|
| `--slots` | the slots file's default path | settings file with the per-server `forever` block |
| `--server` | required | the server to run on |
| `--slot` | any free slot | the slot id to lease |
| `--any` | false | lease any free slot |
| `--repeats` | `0` | repeats per trial; `0` uses each trial's own count |
| `--out` | required | run folder |
| `--dry` | empty | run the scripted setup and situation instead of the live ones |
| `--holder` | `trials` | lease holder name |

The command leases the slot, renews the lease while it runs, provisions the player and bot with read-backs, drives the situation, then scores the folder. A lease renewal failure aborts the run and still tears down. Exit codes: `0` done, `1` run or score error, `2` usage, `5` the server disconnected, `15` the slot was refused or is already held by this holder.

A serve started from a `slot setup --forever` dir holds that slot's lease for its whole run, so `trials run` on
the same slot is refused `held by <holder>` (exit `15`) while the serve runs. Run the bench against a bare
`forever serve --dir` (a `slot.json` with no lease), or a slot no serve leases.

# Scoring a run folder

```sh
forever-agent trials score ./runs/2026-11-24-paladin
```

Scoring writes `verdicts.json` in every repeat folder, `trial.json` in every trial folder, and `card.md` plus `card.json` in the run folder, then prints `scored <dir>: N FAIL, M INVALID`. Exit `0` means no FAIL and no INVALID, `1` any FAIL, `3` any INVALID (only when there is no FAIL).

How a verdict is reached:

1. A repeat whose `setup.json` is not `ok`, or whose scored time is under the trial's `min_valid_s`, is `INVALID` with a reason (`cut short (<n> s, ended: <reason>)`). Every rule in it is INVALID.
2. Otherwise each rule gets `PASS`/`FAIL` from its measured value and `pass_if`, or `INCONCLUSIVE` when there was too little evidence.
3. The trial's verdict is the majority of valid, non-INCONCLUSIVE repeats; a tie is `FAIL`.
4. With zero valid repeats the trial is `INCONCLUSIVE`, unless some repeat was `INVALID` and none scored, in which case it is `INVALID`.

`trials report <run folder>` rebuilds the card from the stored `trial.json` files without scoring again. `trials compare <run folder> --baseline <before folder>` pairs rows by trial id and rule id and prints the before/after table with Fisher's exact p on the k/n metrics.

# Validating trial files

```sh
forever-agent trials validate
forever-agent trials validate my-trial.json other-trial.json
```

Every file prints `path: ok` or its schema errors; the embedded set is validated by default (the fixtures when a build has no embedded set). The check also enforces `id == file stem` and unique rule ids across the set. Exit `0` clean, `1` errors.

# The class sweep

```sh
forever-agent trials sweep --classes warrior,priest,mage,rogue --slots F --server S --any \
    --repeats 1 --out ./sweeps/2026-11-24
```

The sweep picks every trial whose hired bot class is one of `--classes`, runs the tanks first, then by id, into one run folder per trial (one card each) and writes `sweep.md` over the order, the verdicts and the trials the class filter skipped. A trial argument (with `*`/`?` globs) narrows the set further. Exit `0` only when every planned trial ran and PASSed.

# Cleaning up

```sh
forever-agent trials cleanup --slots F ./runs/2026-11-24-paladin
```

Cleanup replays the run's `ledger.jsonl`: it kills every summon it logged (through the GM guard), clears auras, frees bots and releases the slot leases the ledger holds. A missing serve is not fatal — its rows report the skip. Pass `--slots` when the leases must be released. Exit `0` when nothing is left over, `1` when a row could not be completed.

# Checking the server's data

```sh
forever-agent trials forever-data-check --slots F --server S --any --out forever-data-report.json
```

This logs in the server's scratch bot and asks the live server every spell, item and mob entry the trials and kits name, writing `forever-data-report.json` with `ok`, `unknown` and `differs` rows for each. The scratch bot is never a `bot_pool` trial bot (the check persists on it) and is logged out again afterwards. Rows come back `ok`, `unknown` (the server or an unreadable answer did not name it) or `differs` (the server's data disagrees with what the trial expects). Exit `0` all `ok`, `1` any `differs` or leftover unit, or any `unknown` unless `--allow-unknown` is passed.

# What a run folder holds

| Path | Contents |
|---|---|
| `RUN.json` | the run record: label, server, trial ids, repeats run, start and end |
| `ledger.jsonl` | every summon, login, aura and GM line the run added, for `trials cleanup` |
| `<trial>/trial.json` | the trial's aggregated verdicts |
| `<trial>/repN/setup.json` | the provisioning result and its checks |
| `<trial>/repN/raw-ta.jsonl` | raw agent event rows |
| `<trial>/repN/samples.jsonl` | 1 Hz status and unit samples |
| `<trial>/repN/marks.jsonl` | the run's monotonic clock marks |
| `<trial>/repN/events.jsonl` | normalised event rows the scorer reads |
| `<trial>/repN/verdicts.json` | every rule's verdict for this repeat |
| `card.md`, `card.json` | the run card; see [report cards](report-cards.md) |

# Common problems

- **`trials run: no free slot` / exit 15** — every slot of the server is leased. Wait, pass `--slot`, or run `trials cleanup` on a crashed run still holding one.
- **`INVALID (cut short ...)`** — the fight ended early (a death, a wipe, the server dropped). Check `setup.json` and the repeat's `events.jsonl`.
- **Every rule `INCONCLUSIVE`** — the trial's `min_n` was not reached; the situation produced too few opportunities. Run more repeats or lengthen `duration_s`.
- **`unknown --dry script`** — `--dry` takes `script`, `scripted` or a script file path.

# Further reading

- [Report cards](report-cards.md): reading the card the bench writes.
- [Bug hunter](bug-hunter.md): the same repeats can be hunted for behaviour findings.
- [Settings and slots](../getting-started/settings-and-slots.md): the slots file and leases the bench uses.
- [The daemon](../daemon/serve.md): the per-slot serve the runner talks to.
