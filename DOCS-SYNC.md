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
- Keep the page's frontmatter: `type`, `title`, `description`, and `generated` (when the page was last
  written). `generated.by` is always `forever-agent docs`: it names the project, never the tool, model or
  person that wrote the page. `sources` lists the files the page's facts come from. Do not add `verified`
  unless a person verified the page.
- Add one line to `docs/manual/log.md` under today's date.

## How it is checked

- `make docs-check` runs the manual's checker (`tools/verify_okf.py`) and its tests. It fails on a broken page,
  a CRLF file, a link or `#anchor` that does not resolve, a `sources[].resource` that does not exist, a
  `generated.by` other than `forever-agent docs`, an index entry whose description differs from the page's
  own description, a secret, e-mail address, home path or unknown host name, a registered command that no
  page documents as a command, a daemon method missing from `daemon/method-index.md`, and a method or command
  in the manual that the code no longer registers. It also reads every Markdown file under `docs/`: a test-result
  runner field, or (outside the manual) a private-network address or home path, is a problem. A command or method
  left out on purpose goes in `docs/manual/.undocumented-commands` or `docs/manual/.undocumented-methods` with a
  reason.
- A command is documented as a command: a line in a code block (`forever-agent <word>`, or
  `forever-agent forever <word>` for a subcommand), a heading naming it, or a table row naming it. A bare
  backticked word in prose does not count.
- The CI job `docs-sync` fails a pull request that changes `cmd/forever-agent/`, a serve or session source
  (`internal/forever/world/serve*.go`, `*_session.go`), or an added or removed line that calls a flag constructor
  on any `FlagSet` variable or any `Register` call, and changes no concept page under `docs/manual/`. README.md is
  updated with the manual when it carries the same text, but it does not satisfy the gate alone; the manual's
  `log.md` and the index files are bookkeeping and never do. The checker cannot tell whether the page you changed
  is the right one. Its message names the rule; the pull request checklist carries it too.
- A change with truly no user-visible effect says so in the pull request body, on a line that starts at column 0,
  outside HTML comments, fenced blocks and indented text: `Docs: not needed, because <the reason>`, where the
  reason is at least 15 characters and not the template's placeholder. The pull request template keeps the line
  inside an HTML comment, so a copied template never waives anything.

What the machine cannot see: a change to a plan field, a stop reason, an output field, an error or refusal text,
or a file format that no changed path or added call marks as user-visible. The diff has no trigger for those, so
the author must still open the page the table above names.

## For AI agents

Do this without being asked. A task is not finished until the manual matches the code.
