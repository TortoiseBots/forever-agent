---
type: Guide
title: Trials
description: "Run the behaviour test bench: hire a bot into a scripted fight, score each specified behaviour, and read the card."
tags: [testing, trials, behaviour, verdicts]
status: stable
generated: { by: forever-agent docs, at: 2026-11-26T22:30:00Z }
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
  - id: playdata
    resource: internal/trials/playdata/playdata.go
  - id: classrules
    resource: internal/trials/classrules/classrules.go
  - id: classrules-heal
    resource: internal/trials/classrules/heal.go
  - id: rules-engine
    resource: internal/trials/rules/rules.go
  - id: trial-innervate
    resource: internal/trials/trialdefs/edge/priest-holy-low-mana-innervate-ally.json
  - id: playdata-gear
    resource: internal/trials/playdata/gear.go
  - id: aotc
    resource: data/aotc/aotc.go
  - id: realism
    resource: internal/trials/realism/snapshot.go
  - id: scenario-schema
    resource: internal/trials/scenario/scenario.go
  - id: live-run
    resource: internal/trials/live/run.go
  - id: live-items
    resource: internal/trials/live/items.go
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
| `trials realism` | judge a bot snapshot against the player data, per bot and per spec and level band, with a baseline's before/after |

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

A count check may carry `where_fallback` beside `where`: when the recording has no aura rows at all (an agent whose combat log never writes aura events cannot answer an aura check), the fallback filter is read instead and the result's evidence says which form was read. `min_opportunity_s` then applies to the trial's `opportunity` block, turning a `PASS` or `FAIL` into `INCONCLUSIVE` when the chance never lasted that long.

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

**Pool setup rule:** every character in a server's `bot_pool` must live on its own game account, and the tester slot's character needs its own as well. Two characters of one account cannot be logged in at the same time — a second `.tb login` either fails or boots the first one off, breaking trials that hire `bot` and `extra_bots` together.

# Setup read-backs and server gaps

Every setup row is a read-back of the server, and a missing server capability is never turned into a `PASS`. Where the server's own answer names a gap the trial cannot cross, the row is `SKIPPED needs=<check>` and the trial ends `SKIPPED`:

- **`needs=bot_spec`**: the spec kit's ids come from `specs.json` and the Forever overlay, and when the server's own `.tb learn` answers `unknown` for an id, the row is `SKIPPED needs=bot_spec: module/core gap — ...` naming the missing spell id(s), with the spell name when the copied spell table holds it, as a module/core gap (without altering the spec's spell lists), instead of an `INVALID` from a learn this server cannot hold.
- **`needs=bot_items`**: the Forever gear overlay (`internal/trials/data/forever/gear_kits.json`) carries a verified level-40 `blue` bot kit for `priest` only (player tier kits in `gear_kits.json` cover `warrior` only, and Forever provides no item source to derive kits without a live read-back). Trials that require a `blue` bot kit for `paladin`, `druid`, or `mage` (`40`/`45`/`50`) therefore skip with `SKIPPED needs=bot_items` before scanning or equipping anything.

Two reads are taken so that a live server's snapshots cannot fail a good setup or pass a bad one. The player's level is read from the server's own unit row (`status` is no fallback: a core can keep the login level there after `.levelup`, so a stale status that happens to equal the target is no proof), and the power set is polled in the units model for a moment before it is judged. Player tier gear (`player_gear`) learns the kit's proficiencies, grants missing items with `.additem` (verified in `inv`), equips them with `equip` using each piece's target slot `dst` (`0-18` so `AutoEquip` does not touch unrelated equipped items such as the shirt slot), waits/retries once for `inv` and `equipment` to confirm every piece is worn, and still fails closed (`UNVERIFIED`) if an item move remains unconfirmed (choosing a shield-free kit when `player_no_shield` is requested). `player_no_shield` checks slot 16 (`off_hand`) through the same wait/retry `inv` + `equipment` read and fails closed (`UNVERIFIED`) whenever the inventory stays unknown, stale, or unconfirmed. Situation arrival (`bringHires`) teleports each hire to the arena, retries `.tb tele` once after 15 s if the hire is not yet within 10 yd, and reports any observed distance or absence on timeout (`hire_not_brought`). Teardown unequips any trial-added worn bot items via `.bot inv` and `.bot item <bot> unequip 255 <slot>` and removes only trial-added copies above the pre-trial baseline (freeing non-worn bag items first, leaving pre-existing worn/bag items untouched), then sends `.tb logout` and waits until the hire is off the party roster **and** the server no longer answers its unit row, so neither a slow logout nor a bot that merely left the party can pass for one.

A module limit sits behind the wait: this core answers an equipment move with an `ActivePlayerData` update behind a `PlayerData` one that the client does not decode, so the moved piece and any displaced piece stay unconfirmed until the next own create (a relog or a map change). The bench does not retry the read forever and never turns that state into a `PASS`: targeting the slot with `dst` keeps unrelated worn items out of the move, the read waits the client's confirmation window and retries once, then the row stays `UNVERIFIED`.

Before a trial gives its hire items, setup takes back the item instances earlier grants on that pool bot recorded, and teardown does the same before the hire logs out. A grant is the item's own instance GUID as the server answers it: every give reads the instances the hire holds (the `instances=` list of the `.tb itemscan` record), sends `.tb additem`, reads them again, and records every GUID the give added together with the character it went to (and that character's GUID when the party roster has proved it). A give whose answer cannot be read, or that created no instance of its own (a stack that merged into one the hire carried), records nothing and reports it: no copy there can be proved the trial's. A clear deletes each recorded GUID with `.tb removeitem <character> guid <32-hex>`, one copy at a time, only on the character the record names. A worn copy is unequipped first, through the equipment slot the instance list named, and that slot is proved by a complete `.bot inv` reply (`TBM:INV_END` present) before the unequip; a slot that is not proved is never touched. A recorded copy the character no longer holds is retired. A deletion the module refuses, or one whose read-back cannot prove the GUID gone, is reported and left in place: the clear never retries it, never deletes another copy in its place, and never restores a budget; its grant stays for the next run. No count is used as provenance, because a stack moves under the trial (the live bench ate a Sweet Nectar between a read and a removal on 2026-11-27): the instance list is the only evidence, so a copy no record names (the hire's own stack, a merged copy) is never touched and must be cleared by hand. This needs a server module that names item instances in `.tb itemscan` (`instances=<32-hex guid>:<bag>:<slot>:<count>;…`) and removes one named instance (`.tb removeitem <character> guid <32-hex>`, answering `removeitem bot= item= guid= removed=<0|1> left=`); a module that answers neither makes every give fail closed before `.tb additem` (the row is `SKIPPED needs=bot_items: module/core gap`), so nothing is given into bags whose copies can never be proved. The teardown release list is the characters acquired at login, keyed by name, never the filtered setup document: a hire that logged in and then failed its party identity check is still logged out and its recorded items are still cleared. The release steps (stay, free and logout) run on a teardown budget of their own that starts only after the item cleanup, so a clear that uses its whole window still ends with the hire offline.

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

# Judging against the player data

```sh
forever-agent trials score ./runs/2026-11-24-paladin --player-data
```

`--player-data` adds two bench readings, off by default, from the run's own record and the embedded aggregate of WoW Forever player statistics (read 2026-11-24; player choices, not game facts, so the server's own data wins on any item or spell):

| Rule | What it reads |
|---|---|
| `AOTC-GEAR-FITS-SPEC` | each equipped item against its spec's level 60 gear tables and the stat families its players wear in that slot |
| `AOTC-TALENTS-IMPLY-SPELLS` | the talents most players of the spec take that grant an active spell, against what the bot learned and cast in its combat windows |

The gear rule is data-driven. An item any row of the bot's spec's own level 60 gear tables names (any population, share or slot) is fine. An item outside that data is judged by its stat family: its own primary stats when the record carries them, otherwise the families of the specs that commonly wear it, each spec read through the families in its top three stat-priority rows. The families the spec's own players wear in that slot are summed by wear share, and a family is acceptable when it holds at least 20% of the slot's summed family share or is the single largest family. An item whose family is unknown is `INCONCLUSIVE`, never `FAIL`; stamina, crit and hit name no family, item level is never a reason, and a record with no item data is `INCONCLUSIVE`.

Which specs read as hybrids is the data's answer: protection and retribution paladins keep strength beside a caster family, while an enhancement shaman's three rows are stamina, intellect and spell power, so it reads as a caster — the mail its own players wear, and what keeps a shaman in warrior plate contrary. The largest-family fallback ("or the single largest family") adds no family on the shipped data: with at most three families and each share counted once per family it names, the largest family always holds at least a third of the summed share, above the 20% floor.

Known limits: the tables carry no item stats or armour type, only wearer choices. A feral druid in strength plate is fine, because strength is one of its own families; and an enhancement shaman in hunter-worn agility items fails in Wrist, Hands, Waist and Feet, where its own slot data names a caster family above the 20% floor — 4 of the 8 armour slots, 4 of the 7 that have such an item at all (the Hunter-worn Head pieces are also in the shaman's own tables, so they are fine by the own-data rule); the same kind of mail is fine in the other four armour slots, and the agility mail its own players wear is fine in every slot. The outfit test pins those verdicts.

The talent rule expects only a talent the record proves learned, judges only a combat window of at least 30 s and spells whose cooldown fits the window, and never fails a spell the spec's spell lists call situational (it reports it as held). A partial talent read-back can turn a `PASS` into `INCONCLUSIVE`, never into a `FAIL`; no talent data or no proven combat is `INCONCLUSIVE`.

The rows print under `## Player data` (the heading names the data source) with the source credit line, `card.json` carries `player_data` and `player_data_credit`, and `trials report` re-renders them. They are bench readings, not part of the trial verdict: they never change the exit code, and `--player-data` adds a second line with the player-data `PASS`, `FAIL` and `INCONCLUSIVE` counts.

# Judging the class rules

```sh
forever-agent trials score ./runs/2026-11-25-priest --class-rules
```

`--class-rules` adds the class rules, off by default: they read the run's own record (the trial's party roles, the recording's marks and samples, and the events), not a spell list, so a default score stays exactly the bench's.

| Rule | What it reads |
|---|---|
| `HEAL-ASSIGNMENT` | the party's heal rows by caster and target, against the roles the trial names (the player's role or forced role, the bot's role, the extras' roles) |

The rule measures one fight per repeat: `fight_start` (the pull, the fight's first hostile action) to `fight_end`, or to the last in-combat sample when the end mark is missing. Only heal rows inside that window and outside the recording's `excluded` spans (GM heals and the like, written by the normaliser from the marks) count: a heal before the pull, after the fight or inside an excluded span never sets a share. A fight shorter than 30 s is `INCONCLUSIVE`, because too few heals fit in it to read a distribution.

A healer is the party member whose role is `healer`; it is judged only with at least 10 **placed** heal events of its own (a placed heal is one on a party member the setup named). The tanks are the party members whose role is `tank`. The main healer is the judged healer with the most healing (a tie at the top is no main healer). It passes when all three hold:

- the main healer puts at least 60% of its healing on the tanks;
- every other judged healer puts at most 40% of its healing on the tanks;
- the other healers healed at least 2 distinct non-tank party members between them.

Why those numbers: 60% is a clear majority for the healer whose job is the tank, and 40% is its mirror (the pair sums to 100), so the two lines describe one split between the tank healer and the group healer. Ten placed heals is about one every 3 s in the 30 s fight the rule needs, the least that has a distribution; two distinct allies is a spread, where one could be a single off-target.

Heals on pets or units the setup did not name are left out of the shares and reported in an `other` count, so one pet heal cannot silence the rule; they never make it `INCONCLUSIVE` by themselves. It reports `INCONCLUSIVE`, never `FAIL`, when the recording cannot carry the verdict: no `events.jsonl`, no usable fight window, a fight under 30 s, no heal rows, no tank role in the party, no healer role, a tank or healer the setup proves no guid for, fewer than 10 placed heals by every healer ("too few placed heals"), a single judged healer ("one healer only" — a split needs a second healer), a placed heal row with no readable amount, or two healers tied on healing. A healer's heals on itself count in its own total but neither for the tank nor as cover of the rest.

The shares are of **gross** healing: the heal log's `amount` is the heal's original amount (`SMSG_SPELL_HEAL_LOG`'s `OriginalHeal`) and the combat log carries no overheal, so a heal that mostly overheated still counts in full. What the rule cannot see: overheal, absorb or shield healing (a shield is not a heal row), the mana spent per heal, whether a heal was needed at the time, a pet's own healing, or a heal on a unit the setup did not name.

The `edge` trial `priest-holy-low-mana-innervate-ally` stages the case the class rules were written for: a level 60 Holy priest hire at 15% mana in a 90 s fight, with a Balance druid hire. Its rules are the ordinary bench rules, not the class ones: `DRU-BAL-INNERVATE-LOWMANA-TARGET` counts the landed Innervate aura on the priest while the priest's own mana reads below 35% (`dst_power_lt`); a recording with no aura rows at all cannot answer an aura check, so it counts the cast instead and says which form it read in the evidence (`where_fallback`). A cast on the druid itself or on a target at or above 35% does not count, and `min_opportunity_s` 5 makes the rule `INCONCLUSIVE` ("opportunity 0 s < 5 s") when the priest never sat that low, instead of failing it from an empty opportunity. `DRU-BAL-INNERVATE-NOT-SELF-TARGET` is the druid's self-casts over its own casts (`ratio`, `pass_if "== 0"`): a druid that cast nothing is `INCONCLUSIVE` (no denominator), a self-cast while the priest sat low is `FAIL`, and the same self-cast in a fight where the priest never sat low is `INCONCLUSIVE` from the opportunity floor. `PRI-LOWMANA-ACTED` is the precondition that the priest cast something (3 casts). What the two Innervate rules cannot see: whether Innervate was off its cooldown, the mana the spell actually returned, or a landing on a unit the client never saw (a recording without that aura row cannot show it).

No shipped trial stages two healers yet (the live damage situations hurt one victim), so on today's trial set the rule reports "one healer only" and stays `INCONCLUSIVE`; it judges any run whose party does name two healers.

The rows print under `## Class rules` (trial, repeat, rule, verdict, the main healer's heals on the tanks, and the reason), `card.json` carries `class_rules`, and `--class-rules` adds a second count line with the class rules' `PASS`, `FAIL` and `INCONCLUSIVE` counts. They are bench readings, not part of the trial verdict: they never change the exit code.

# Judging a bot snapshot

```sh
forever-agent forever bot-snapshot --dir ./serve --out bots.json
forever-agent trials realism --snapshot bots.json [--baseline before.json] [--json]
```

`trials realism` judges one snapshot of what bots have — their talents, worn items and enchants — against the same player data: talents by level group, gear realism (required level and item levels against the level's allowance, raid, PvP and reputation sources), the spec's stat priority through the player-data gear rule (one judgement; the priority is reported, never judged), and the level 60 enchants, with a per-spec-and-level-band table and a before/after when a baseline is given. Every verdict is `PASS`, `FAIL` or `INCONCLUSIVE`, and none fails on data the snapshot does not carry; a check whose decisive part is unread is `INCONCLUSIVE`, never a `PASS`. The snapshot format, what the taker fills today, each check's starting thresholds and its `INCONCLUSIVE` cases are in [realism checks](realism.md).

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
