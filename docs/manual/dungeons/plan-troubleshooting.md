---
type: Troubleshooting
title: Plan tools troubleshooting
description: The errors the plan check, export, probe, scout, oracle and route-edit tools print, what they mean, and the fix.
tags: [dungeons, plans, troubleshooting]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T21:48:00Z }
sources:
  - id: plan-check
    resource: cmd/forever-agent/plan_check_cmd.go
  - id: plan-export
    resource: cmd/forever-agent/plan_export_cmd.go
  - id: plan-probe
    resource: cmd/forever-agent/plan_probe_cmd.go
  - id: plan-scout
    resource: cmd/forever-agent/plan_scout_cmd.go
  - id: route-oracle
    resource: cmd/forever-agent/route_oracle_cmd.go
  - id: route-edits
    resource: cmd/forever-agent/route_edits_cmd.go
  - id: trash-check
    resource: cmd/forever-agent/trash_check_cmd.go
---

# What this is

The plan tools read the plan file, the navmesh and the world export; none of them logs in except `plan-scout --live`
and `plan-scout --forever --live`. This page lists the messages you are most likely to meet and what to change. The
flags and outputs are in [Plan tools](plan-tools.md); the plan fields are in [Plans](plans.md).

# plan-check and plan-probe

| Message | Cause and fix |
|---|---|
| `plan-check: --from-pull-group needs --from-leg ...` | A pull group number means nothing without its leg. Add `--from-leg N` before `--from-pull-group M`. |
| `plan-check: "X" is not a plan name (a-z, 0-9, _ and -)` | The argument has characters outside the plan name set. Rename the file and the plan's `name` to lower-case letters, digits, `_` and `-`. |
| `plan-check: name the plans or pass --sql, not both` | You mixed plan files and a migration. Name plan files as arguments, or pass `--sql FILE` (and `--plan NAME` only when the file holds several plans). |
| `plan-check: --plan picks a plan in the --sql file; ...` | `--plan` is for a migration with several plans. With plan files, name the plans instead. |
| `plan-probe: --mmaps is required` | The probe needs the meshes. Pass the server's mmaps folder. |
| `<plan>: <k> of <N> points fail ...` (exit 11) | A point is unreachable, has no line of sight, is off the mesh or sits in an avoid box. The line names the leg, the part and the reason; fix the plan or exempt the hop with `check_skip` and a reason. |
| `plan-check` says the mesh cannot answer (exit 2) | The build has no cgo engine or the folder has no tiles for the map. Rebuild with cgo and the C++ compiler, or point `--mmaps` at the server's own folder. |
| `plan-probe` says the navmesh cannot answer (exit 1) | Same cause; the command cannot run its queries. |
| `plan-probe` exits 20 | A segment is `bad`: the server's own path query or the floor lookup disagrees with the row. Read the report's per-segment verdict and fix the point. |

# plan-export

| Message | Cause and fix |
|---|---|
| `plan <name>: ... is a draft ... --allow-draft` | A draft plan is refused without opting in. Pass `--allow-draft` for a named plan, or finish the mapping walk and mark it mapped first. With `--all-live`, drafts are skipped with a note. |
| `plan <name>: companions_loot is false ... plan not exported` | The rows cannot carry a leg that turns looting off. Pass `--drop-companions-loot` and turn the bots' looting off in the run setup; add `--carrier-chests` to export that leg's chests as carrier rows. |
| `plan <name>: skip-legs names "X", which is no leg of the plan` (and the `with-optional` and `with-legs` forms) | A name in one of the leg filters matches no leg, or the wrong kind: `--with-optional` needs an `optional` leg, `--with-legs` a `skip_export` leg. Check the spelling against the leg names. |
| `... exists: a migration that is applied is never edited` | `--out` refuses to replace a file. Export a new timestamped name, or pass `--force` when the file was never applied. |
| `plan-export: a step the run leader cannot do` / a `CUT:` line | An agent-only step (an agent gossip, a `unit` wait without a hold, an agent area trigger) cut the export at its leg. Give the step a run leader form (a `hold`), or accept the cut: everything before it was exported. A plan whose first leg holds such a step is refused. |
| The migration's base name is not `YYYYMMDDHHMMSS_world.sql` | The server's updater will not take it. Rename it with a timestamp. |

The written migration is read back and compared with the built rows before it is left on disk, so a failed read-back
means an exporter bug, not a plan problem: keep the plan and the output.

# plan-scout and trash-check

| Message | Cause and fix |
|---|---|
| `--fail-on takes fail or warn` | `plan-scout` accepts exactly those two words. The default is `fail`. |
| `plan-scout: slot 3 is never used` (exit 15) | Slot 3 is reserved. Pick another slot for the live pass. |
| `plan-scout: "X" is not a test server ...` (exit 18) | The live pass runs only on the named test servers. Run the offline pass anywhere; for a Forever server use `--forever --live --slot DIR`. |
| A `plan-scout` step fails or warns (exit 27) | The report lists the step, its leg and the rule; `--fix-out` writes the proposed fixes, `--apply-fixes DIR` writes a patched copy. |
| The live pass stopped part-way (exit 28) | The report and fixes are still written; read `scout.log` for the point it stopped at. |
| `trash-check: --fail-on %q: danger, patrol or any` | `--fail-on` takes exactly those three words, or nothing for a report-only run. |
| `trash-check` exits 21 | A finding of the named class exists. Read `trash-<plan>.txt`: `danger` is a pack no row pulls, `patrol` is a loop that crosses the route. |

# route-oracle and route-edits

| Message | Cause and fix |
|---|---|
| `route-oracle: unknown --mode ...` / `unknown --objectives ...` | `--mode` takes `walk` or `bosses`; `--objectives` takes `plan` or `bosses`, and `bosses` objectives need `bosses` mode. |
| `route-oracle` exits 12 | Not every plan could be reported. The others' files are still written; fix the plan or mesh the message names. |
| `<plan> has no checked version in <dir>` | The route editor has no version for that plan, or none is `checked`. Edit and save a version on the dashboard first. |
| `<plan> vNNNN is unchecked: ... no edit of it is proven` | The plan already had plan-check issues of its own, so the edit added none but proved nothing. Fix the plan's own issues and record a new version. |
| `the plan file is not the one its record names (sha256 differs)` | The version file changed after it was written. Record a new version from the current file instead of exporting or promoting the changed one. |
| `the worktree's <plan>.json ... is not the file this edit started from` | The plan changed in the worktree while the edit was based on an older version. Edit again on the new base. |

# plan-diff

| Message | Cause and fix |
|---|---|
| `plan-diff: ... exists: refusing to overwrite a diff` (exit 2) | `--out` never replaces files. Diff into a fresh folder. |
| `plan-diff: the plans are on different maps` | A route diff is one map. Pick two exports of the same plan on the same map. |
| `plan-diff --fail-on-change` exits 25 | Something other than a comment changed. Read `diff-<plan>.txt`; use it as the gate your CI expects. |

# Related

- [Plan tools](plan-tools.md): every flag and exit code.
- [Plans](plans.md): the fields and statuses.
- [Stop reasons](stop-reasons.md): a run's own ends, which are not plan-tool errors.
