---
type: Guide
title: Editing a route on the dashboard
description: Turn on the dashboard's route editor, save a navmesh-checked version of a plan's route, and move that version into the repository.
tags: [bots-and-raids, dashboard, route-editor, plans]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T21:52:00Z }
sources:
  - id: dashboard-cmd
    resource: cmd/forever-agent/dashboard_cmd.go
  - id: routeedit
    resource: internal/routeedit/store.go
  - id: edit-server
    resource: internal/dashboard/edit.go
  - id: route-tools
    resource: docs/manual/dungeons/route-tools.md
---

# What this is

Without `--edit` the dashboard's page is read-only. With it, the page can write one thing: a checked version of a
plan's route, through `POST /api/edit/<plan>`. The version is checked on the server's own navmesh, stored under a
version number, and later moved into a plan folder or a worktree with `route-edits`. This page covers the dashboard
side; the command that lists, exports and promotes versions is in [Route tools](../dungeons/route-tools.md).

The editor works for both mesh generations: `--mmaps-format turtle` (the default) reads the 1.12 files, and
`--mmaps-format forever` reads the 16-format files, with `--parent MAP=PARENT` for the server's parent-map rule.

# 1. Make a token file

Create a file of `label token` lines, mode `0600`, owned by you. The label is recorded as who made a version.

```text
work <a long random token>
```

The token never reaches the page: the page asks you to type it on Save, and the browser keeps it in the tab only.

# 2. Start the dashboard with the editor on

```sh
forever-agent dashboard --mmaps-dir ./mmaps --plans-dir ./dungeons --edit \
    --edit-dir ./route-edits --edit-tokens ./route-edit-tokens
```

| Flag | Default | What it does |
|---|---|---|
| `--edit` | off | Turns the editor on; takes `=`, for example `--edit=true`. |
| `--mmaps-dir` | none, required with `--edit` | The navmesh of the server the plans run on. Every version records it, and every save is checked on it. |
| `--mmaps-format` | `turtle` | `turtle` (1.12 files) or `forever` (16-format files); the wrong one stops the dashboard at start. |
| `--parent` | none | With `--mmaps-format forever`: `MAP=PARENT` pairs such as `389=0`. |
| `--plans-dir` | none | The plan folders the page lists. The first one is the edit source by default. |
| `--edit-source` | the first `--plans-dir` | The folder whose plan files an edit starts from; it must not lie inside any slot's `dungeons/` folder. |
| `--edit-dir` | `~/forever-agent-route-edits` | Where versions go: `<plan>/vNNNN.json`, `vNNNN.meta.json`, `vNNNN.diff`. |
| `--edit-tokens` | `~/.config/forever-agent/route-edit-tokens` | The `label token` file, mode `0600`. |
| `--edit-allow-default-dir` | none | Settings files (by file name, comma separated) with an `rpc` but no `dungeons_dir` whose default plans folder you vouch is not the edits folder. Any other such file stops the editor. |
| `--edit-trust-local` | off | Saves from this machine need no token typed; read the threat model below first. Takes `=`. |
| `--edit-trust-label` | `owner` | With `--edit-trust-local`: the label in the token file that trusted saves are made as. |

The dashboard also serves the real route on the plan view (`GET /api/plan/<name>/route`); that needs `--mmaps-dir` even
without `--edit`.

# 3. Edit a route on the plan view

Open `#/plan/<name>`. The route is editable at once: there is no Edit button. Drag a point to move it, click a line to
add a point there, click a point and press Delete to remove it, and Ctrl+Z / Ctrl+Y undo and redo. Locked points
(packs, bosses, steps, legs, and `direct` or `check_skip` via points) are grey and cannot be moved.

Save asks for the editing token. The server applies your edits to a copy of the base file it reads itself, snaps each
point to the navmesh floor, and runs the plan check on the whole plan.

# 4. Read the save answer

| Status | Meaning |
|---|---|
| `checked` | The edited plan has no plan-check issue at all. This is the only kind a run may use. |
| `unchecked` | The edit added no new issue, but the plan already had issues of its own, so the edit is not proven. |
| `refused` | The edit made a new issue, or the check could not run. HTTP 422; only the record and the diff are kept. |
| `accepted` | A run walked the version and passed. Set by the run, not by the editor. |

A version covering a plan that already has issues is `unchecked`; fix the plan's own issues and edit again to get a
`checked` version. `route-edits list` prints each version with its status, operations and check numbers.

# 5. Skip typing the token on your own laptop

`--edit-trust-local` (which needs `--edit`) makes a save with no `Authorization` header from a loopback socket owned by
the dashboard's user, or from a literal Tailscale address in `--allow-host`, as the `--edit-trust-label` token's owner.
Names are never trusted as callers, a relay of another user is not trusted, and any request carrying `Authorization` is
judged by that token alone. The token itself never reaches the page. Leave it off on a shared machine.

# 6. Ops, gates and limits

A save posts `{base_sha256, note, ops[{leg, kind: move|insert|delete, index, x, y}]}`. Each point snaps to the navmesh
floor nearest the old height, within a 25-yd window, and must lie within 200 yd of the plan's own points. A snap that
had to pick a floor is flagged. Locked kinds stay grey. A version whose plan already has issues is `unchecked`; an
edit that adds a new issue is refused with HTTP 422.

`route-edits list`, `export` and `promote` move a version into a stopped slot's plan folder or into a worktree. The
export prints a `route_edit=<plan>@vNNNN sha256=...` tag; pass it as the run's `trace_meta.route_edit` so the run
records which version it walked.

# Related

- [Route tools](../dungeons/route-tools.md): `route-edits` and the version store.
- [Watching bots on a dashboard](dashboard.md): the rest of the page, its flags and run controls.
- [Plans](../dungeons/plans.md): what the edited points belong to.
