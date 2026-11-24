# The Turtle stack

This repository started as [turtle-agent](https://github.com/pjerra/turtle-agent), the 1.12 / Turtle WoW client, and
still carries that stack beside the Forever one. Issue #102 deleted nothing: these packages are imported by the Turtle
raid tools and the dungeon runner, and #108 builds the Forever raid path beside them. A follow-up ticket deletes them
once #108 round 4 is merged and live-proved.

| Package | Non-test importers outside the package |
|---|---|
| `internal/session` | 26 |
| `internal/worldconn` | 8 |
| `internal/auth` | 3 |
| `internal/opcode` | 36 |
| `internal/packet` | 76 |
| `internal/capture` | 6 |
| `internal/game` | 33 |
| `internal/world` | 125 |
| `internal/raidwalk` | 25 |

The `.bot` / `tbm` grammar is Turtle-stack too (the TurtleBots module's chat commands and add-on messages).

## Turtle-only commands

`forever-agent help` marks every command the Turtle stack serves alone with its Forever replacement
(`[Turtle only; Forever: <x>]`; `none` and a reason where there is none yet). The one map behind the marks and the
refusals is `commandScopes` in `cmd/forever-agent/command_scope.go`. A Turtle-only command refuses a Forever server
before any lease, login, file write or child process.

## Turtle documents

These stay in the tree, under their own names, because plans, scripts and code cite them:

- `docs/LIVE-CHECKS.md` — the Turtle live checks still open (cited by `dungeons/uldaman.json`, `tools/run-uldaman.sh`
  and `docs/ROTATIONS.md`).
- `docs/ROTATIONS.md` — the rotation file format and how to A/B one (cited by `internal/brain/rotation.go`).
- `docs/turtle/DUNGEON-PROGRESS.md` — the Turtle dungeon tracker (moved here by #102).
- `docs/turtle/history/` — the 2026-10-12 turtle-agent plan and design (Turtle design history, moved here by #102).

Forever's own notes live in `docs/forever/`, and the manual in `docs/manual/`. The Turtle pages of that manual are
under `docs/manual/turtle/`.
