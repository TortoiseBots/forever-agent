# Report card, round S7-B: what a run did, without reading the log

Scope: after every `forever dungeon-watch` run, and from a saved run record offline (`forever run-report RECORD`),
write one card that finds the run's weak spots: per leg the time, pulls, re-pulls, deaths (member and step), wipes
and pauses with reasons; the flags with their evidence lines; a one-line verdict; a stable JSON beside the text; and
`forever run-report --compare A B` for what got better or worse per leg and per flag. The supervisor round's record
and summary are the inputs; the card is a second reader of them, never a second recorder.

## What was reused, and what not

Looked for first: `internal/trials/report` (the trials bench's `card.md` + `card.json` + baseline compare),
`internal/raidwalk/summary.go` (the raid walk's `summary.json` and its `Text()`), and the `reportcard` / `anomaly`
names across the tree.

* Reused: the run record and its summary (`driver.RunSummary`, `driver.RunWatch`) exactly as round S7 wrote them, the
  trace and summary file names, the command layer's strict record parsing, and the report conventions the trials
  card established (`verdict`, one row per item, evidence lines, JSON beside the text, keyed comparison).
* Not reused: `internal/trials/report` is a different protocol (trial directories, rule ids, PASS/FAIL verdicts,
  Fisher's pooled repeats) and `internal/raidwalk/summary.go` is another (raid-walk rows, boss kill summaries,
  `tb-raid-walk` versions). Neither carries a module run's legs, steps, events or pauses, so importing either would
  have dragged its old shapes in. There is no generic anomaly helper in the tree. The new package is
  `internal/forever/reportcard`; it depends on `driver` for the record types only.

## The record

A watch already wrote the module's final counters and its pause log. Three additions carry the evidence the card
needs; all are optional, so a record from round S7 still loads (its per-leg sections are empty).

| Field | Content |
|---|---|
| `watch.legs[]` | `leg name first_s last_s seconds pulls steps deaths wipes pauses [plan_s]` — the leg's span in the module's own event seconds, closed at the next leg's first event (the last leg at the run's final elapsed second) |
| `watch.step_log[]` | `leg step pulls pull_events[{seq at_s}] deaths` — one entry per step that had a pull or a death; a second pull at one step is a re-pull |
| `watch.death_log[]` | `at_s leg step [unit] [who]` — `who` is the party slot (`tank`, `healer`, `dps1`, `dps2`, `dps3`), never a character name; `unit` is the module's own id when it sent one |
| `watch.rescue_log[]` | `at_s leg step why` — each practice rescue the watcher sent, with the pause it answered |
| `watch.pause_log[]` | now also `leg`, `step` and `cleared` (a resume followed it) |

The watch folds each event it reads into these records as it polls; it writes them once at the end (or on a lost
link, so a dead watch still leaves what it saw). The watcher's own watchdog pause is recorded directly: the watch
ends at once after sending it, so the module's ring is not read again. Leg 0's time starts at the run's zero (the
start record names the first leg), so the leg table adds up to the run.

The event ring holds 200 events; the watch reads only events after the start reply's `ev`, at `--poll` seconds. A
run whose ring overflows between two polls loses the oldest events it had not read, and the card says only what the
record has. `unit` is the module's id, matched to a party slot through `PlayerGUID`'s low word; an id the party does
not name stays `unit <n>`.

## The card

Files: `<run_id>.report-card.txt` and `<run_id>.report-card.json` beside `<run_id>.forever-run.json` (same folder as
`--out`). Without `--out` the watch prints the whole card instead of writing it. The JSON's `schema` is
`forever-report-card`, `version` 1; field names are the ones above plus `verdict`, `verdict_text`, `thresholds`,
`legs`, `death_log`, `pause_log` and `flags[]` (`id`, `key`, `text`, `evidence[]`).

The text: the run line, the verdict, the thresholds it was built with, the leg table, the deaths, the pauses, then
one block per flag with its evidence lines.

**Thresholds** (each a flag, defaults stated):

| Flag | Default | Rule |
|---|---|---|
| `--repulls N` | 2 | flag a step whose pulls minus one is more than N |
| `--member-deaths N` | 3 | flag a member with at least N deaths and at least 2 more than the next member |
| `--slow-factor X` | 2 | flag a leg over X times its plan's recorded time; only when the record carries `plan_s` |

**Flags** (stable ids, `key` names the leg/step/member):

| id | Rule | Evidence |
|---|---|---|
| `repulls` | a step re-pulled more than `--repulls` times | one `pull seq= t= leg= step=` line per pull |
| `pack-deaths` | one member died twice at the same step | one `death t= leg= step= who= unit=` line per death |
| `member-deaths` | one member died far more than the others (rule above) | the member's death lines |
| `slow-leg` | the leg exceeded `--slow-factor` times its plan's time | the leg's `seconds` and `plan_s` |
| `watchdog-pause` | a pause whose reason names the watchdog | the pause line with its reason |
| `uncleared-pause` | a pause no resume followed | the pause line and the record's final state |
| `rescue` | a practice rescue was sent | the rescue line and the pause it answered |
| `stop-reason` | the run's own end, always present | the record's `stop_reason`, state and position |

The plan's recorded leg time: the plan format this round reads records none (its legs carry geometry and rows, not
a duration), so `plan_s` stays absent and the `slow-leg` rule is quiet. The field and the rule are in the record and
the card so a later plan or module build that records a leg time needs no card change; the saved-record test sets one.

**Verdict**: `CLEARED` when the module reached `done`; otherwise `STOPPED AT leg N "name"/step S (<reason>)` when the
run stopped where it stood (budget, wipes, watchdog, a module stop, a spent rescue budget, `stopped`), and
`FAILED (<reason>)` when it never got there (lost link, blind watch, a refused start/revive/resume, an unfinished
entry).

**Compare**: `forever run-report --compare A B` (each a card or a record). It matches legs by number and flags by
`(id, key)`, prints verdict changes, one row per changed leg metric (seconds, pulls, re-pulls, deaths, wipes) with
the delta and `better`/`worse`, and `+`/`-`/`~` rows for flags that appeared, went away or changed text, then the
counts.

## Tests

`internal/forever/reportcard`:

| Test | What it pins |
|---|---|
| clear | a done run: CLEARED, one leg with time/pulls/steps, no weak-spot flag, the stop-reason flag |
| re-pull stop | the run held on a pack, six pulls of it, then stopped: STOPPED at the leg/step with the module's reason, `repulls` with one evidence line per pull |
| deaths | the tank twice at one step and once later: `pack-deaths` (2 evidence lines) and `member-deaths`, the death table's leg/step/time/slot |
| wipe rescue | the wipe pause and the watcher's rescue: `rescue`, the pause cleared by the resumed run |
| watchdog | a silent run: `watchdog-pause` and `uncleared-pause`, STOPPED with the watchdog reason, although the event was never polled |
| saved record | a record on disk, read through the CLI types: every flag including `slow-leg`, verdict, the card's JSON round trip |
| compare | two cards: the verdict, metrics and flags that got worse or better, and the counts |

`cmd/forever-agent`:

| Test | What it pins |
|---|---|
| report from a record | a dry watch's `--out` record read by `forever run-report`: the text, `--json` (schema, verdict, legs), the card written after the watch |
| `--out` | both card files written to the given folder |
| compare | two hand-written records: verdict, seconds/pulls/re-pulls/deaths/wipes deltas, the repulls flag |
| thresholds | `--repulls 0` flags one re-pull; the default does not |
| refusals | no argument, a missing file, a foreign schema, one `--compare` argument, three, a threshold out of range: exit 2 |

`internal/forever/driver` adds `TestWatchRecordEvidence` (legs/steps/deaths with the slot word) and
`TestWatchWatchdogRecordsItsPause` (the watcher's own pause reaches the record).

## Hygiene

The new code and these notes were checked for private names before commit.

The new code and notes say "the module"; no capture file name, live name, host, address or private document is
named. Cards name party slots, never characters; records and summaries already carry no people or servers.

## Gates

(Filled on the final commit; see the final report.)
