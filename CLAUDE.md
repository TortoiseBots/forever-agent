# CLAUDE.md

Docs follow the code: read [DOCS-SYNC.md](DOCS-SYNC.md). Every change a user can see (a command, a flag or its
default, a daemon method or its parameters, an output field, an error text, a file format, a settings key, a plan
field, a stop reason, an install or build step) updates `docs/manual/` in the same PR; a change with no user-visible
effect says `Docs: not needed, because ...` in the PR body. `make docs-check` checks it.

@AGENTS.md
