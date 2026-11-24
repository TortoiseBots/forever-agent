# Documentation follows the code

Every pull request that adds or changes something a user can see updates the matching page of the user manual
(`docs/manual/`) in the same pull request. This file is the rule; `make docs-check` and the CI job `docs-sync`
check it.

## What counts as user-visible

A command; a flag or a flag's default; a daemon method or one of its parameters; an output field; an error or
refusal text; a file format; a config or settings key; a plan field; a stop reason; an install or build step.

A new feature gets a page or a section. A removed or limited feature is fixed everywhere a sentence still
promises it. When the code and the manual disagree, the code wins: fix the manual.

## Where a change goes

| What you changed | The page that changes with it |
|---|---|
| A top-level command (`forever-agent <word>`) | The area page that documents its neighbours: `dungeons/plan-tools.md`, `dungeons/route-tools.md`, `dungeons/navmesh-and-maps.md`, `testing/`, or the `turtle/` page for a Turtle-only command |
| A `forever` subcommand | `daemon/serve.md` or the area page (`dungeons/`, `bots-and-raids/`, `getting-started/first-login.md`) |
| A daemon method or its parameters | `daemon/method-index.md` plus the page that explains it: `daemon/status-and-world-reads.md`, `daemon/movement.md`, `daemon/travel.md`, `daemon/events-and-logs.md`, or a `player-actions/` page |
| An `act` step | `player-actions/act-steps.md` |
| A plan field or a plan tool | `dungeons/plans.md`, `dungeons/plan-tools.md` |
| A stop reason or a verdict | `dungeons/stop-reasons.md` |
| A settings key, a slot, a lease | `getting-started/settings-and-slots.md` (and `daemon/serve.md` for the serve's own flags) |
| A bench rule, scoring, a trace or report format | `testing/` |
| A package, a contract, the wire format | `developers/` |
| The Turtle-only stack | `turtle/` |
| An install or build step | `getting-started/install-and-build.md` |
| The feature list, the quick start or the status | `README.md` (and `getting-started/what-is-forever-agent.md` for the status) |

`docs/manual/index.md` lists every page; an area's `index.md` lists its pages, and `docs/manual/log.md` gets one
line saying what changed and when.

## How to write it

- Copy names, flags, defaults and error texts from the code you opened. Never invent a flag or a message.
- Placeholders only: no hosts, accounts, character names or private paths. `--host example.invalid` and
  `--password <password>` are the forms the checker accepts.
- Keep the page's frontmatter: `type`, `title`, `description`, and `generated` (who and when). `sources` lists
  the files the page's facts come from. Do not add `verified` unless a person verified the page.
- Add one line to `docs/manual/log.md` under today's date.

## How it is checked

- `make docs-check` runs the manual's checker (`tools/verify_okf.py`) and its tests. It fails on a broken page,
  a link that does not resolve, a secret or host in the manual, a registered command that no page mentions, a
  daemon method missing from `daemon/method-index.md`, and a method or command in the manual that the code no
  longer registers. A command or method left out on purpose goes in `docs/manual/.undocumented-commands` or
  `docs/manual/.undocumented-methods` with a reason.
- The CI job `docs-sync` fails a pull request that changes `cmd/forever-agent/`, a serve method file
  (`internal/forever/world/serve*_methods.go`) or adds a flag, and changes nothing under `docs/manual/` or
  `README.md`. Its message names the rule; the pull request checklist carries it too.
- A change with truly no user-visible effect says so in the pull request body, on its own line:
  `Docs: not needed, because <the reason>`.

## For AI agents

Do this without being asked. A task is not finished until the manual matches the code.
