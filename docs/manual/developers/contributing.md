---
type: Guide
title: Contributing
description: The rules and checks a change to forever-agent must pass before it is merged.
tags: [developers, contributing, tests, ci]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:38:33Z }
sources:
  - id: agents
    resource: AGENTS.md
  - id: makefile
    resource: Makefile
  - id: ci
    resource: docs/CI.md
  - id: testwf
    resource: .github/workflows/test.yml
  - id: testmain
    resource: cmd/forever-agent/main_test.go
  - id: readme
    resource: README.md
---

forever-agent is a client that must behave like a real game client, so a change is judged on two
things: does it do what it claims, and does it keep doing it for the build it claims. This page is
the practical checklist. `AGENTS.md` is the rule text; `docs/CI.md` describes every workflow in
detail.

# Before you start

- A **bug fix** is a change where the agent fails at what it is meant to do. It goes in.
- A **behaviour change** makes the agent or the bots play differently. It needs the maintainer's
  decision first: describe what changes in game and ask. Anything outside the project's vision
  belongs behind a config option that is off by default.
- The agent sends only packets a real client sends, laid out as the server of the claimed build
  reads them. Check a layout against that server's source, not a later expansion, and never copy a
  1.12 layout into the Forever path (or the reverse). A build change is checked against a running
  server, not guessed.
- The agent's own GM commands are test tooling, never bot behaviour: only on your own character and
  bots, never a command that changes the world for everyone, no config edits, no restarts.

# The local gate

```sh
make build
make test
```

`make build` compiles the CLI to `bin/forever-agent`. `make test` is the single-machine equivalent
of CI and fails on the first problem:

| Step | What it checks |
|---|---|
| `make fmt-check` | `gofmt -l .` prints nothing. Run `make fmt` to fix. |
| `make vet` | `go vet ./...`. |
| `make nocgo` | The tree builds with `CGO_ENABLED=0` (without the navmesh engine). |
| `go test -race -count=1 -timeout 20m ./...` | The whole suite under the race detector. The 20-minute package timeout is deliberate: `cmd/forever-agent` alone runs about 1,500 tests for 8–10 minutes. |

CI additionally runs `staticcheck ./...` with a pinned release (`2026.2.2` at the time of
writing), a Windows and macOS cross-build with cgo off, and a cgo build. The repository has no
staticcheck suppressions: fix the finding, or disable one check at a time in a commented
`staticcheck.conf` when it is a false positive. Never disable a whole category.

Tests run on the race detector in CI in nine shards (three groups by three shards); which shard
your test lands in is automatic, by test name or by package. Do not maintain a list.

# Where tests go

- A package's tests sit beside it, `foo_test.go`. Use the fake server or fake session the package
  already has; a test that needs a live server does not belong in the suite.
- A bug fix gets a test that fails before the fix when no existing test covers the path. Do not
  write a test that only re-reads the source or pins an incidental default.
- Contract cases are not tests: they are the agreement, and the shared contract tests read them
  (see [Contracts](contracts.md)).
- Fuzz targets exist for the server parsers. `make fuzz FUZZTIME=5s` runs every target in
  `FUZZPKGS`; a crash is saved under `<package>/testdata/fuzz/<target>/` and is committed together
  with the fix as a regression seed.

# Generated files

Three files say `DO NOT EDIT` and mean it:

| File | Comes from |
|---|---|
| `internal/opcode/opcodes_gen.go` | `make opcodes` (the core's `Opcodes_1_12_1.h`) |
| `internal/forever/wire/clientopcodes_gen.go` | The core's `ClassicClientOpcodes.inc` |
| `internal/forever/wire/servernames_gen.go` | The core's `Opcodes.h` |

The last two are produced by a hand-run script at a pinned core commit; the header names the
commit. Regenerate them from the new core source rather than editing numbers by hand, and say in
the change which commit they came from. The same applies to the embedded spell data and the
knowledge-base rotations (`make spellnames`, `make kb-rotations`).

# What CI runs

`test.yml` is the gate and the only check branch protection requires. It runs on every push to
`dev`, `main` or a `ci/**` branch, and on every non-draft pull request based on `dev`, `main` or a
stacked feature branch. Its jobs:

- `lint`: formatting, vet, a no-cgo build, the shard scripts' self-tests, the cross-build, the
  differential table against the pinned trials reference checkout (Python 3.12), staticcheck, then
  a cgo build.
- `race-shards`: the suite under `-race` in nine jobs, at most six at once.
- `race-shards-check`: every test and package was run exactly once across the shards.
- `test`: the single required check; it fails when any job was skipped, cancelled or failed.

The other workflows run on their own schedules or paths, and a few are report-only:

| Workflow | When | What it does |
|---|---|---|
| `navmesh.yml` | pushes and PRs, plus by hand | Downloads the server's navmesh data and runs the navmesh, dungeon and dashboard tests plus the plan gate. Without the data token (forks) it skips and passes. |
| `contract-check.yml` | nightly and PRs touching `contract/**` | The contract and protocol tests, then the replay through the real add-on. |
| `fuzz.yml` | nightly | `make fuzz FUZZTIME=2m`; a crash fails and uploads the corpus. |
| `privacy-scan.yml` | nightly on `main` | Refuses private-looking paths, host names and addresses in the tree (see [Privacy and captures](privacy-and-captures.md)). |
| `plan-drift.yml` | pushes and nightly | Compares the shipped plans with the reference module's exports. Report-only (`continue-on-error`). |
| `security.yml` | weekly | `govulncheck` at a pinned version, and `go.mod`'s Go version against the latest stable. |
| `flake-hunt.yml` | nightly | Runs the tests with a known flake history ten times and files one “Flaky tests” issue on a failure. |

To run the full CI on a branch without opening a PR, push it to `ci/<name>` and delete the branch
afterwards. A draft PR skips every job, the required check included.

# Pull requests

- Open PRs against **`dev`**. `main` only takes `dev` after the owner's review.
- A change that adds or changes an RPC method, a field or a command updates the manual page that
  describes it. `AGENTS.md` states this as a rule, not a preference.
- Keep the change reviewable: one concern per PR, a description that says what changed in game or
  on the wire, and the evidence you actually ran (the test name, the command, the output line).
- Report only what you observed. Mark anything inferred. Never put account names or passwords in
  a report, a capture, a fixture or a PR.

# Related pages

- [Architecture](architecture.md) — the layers a change will touch.
- [Adding a command](adding-a-command.md) and [adding a daemon method](adding-a-daemon-method.md).
- [Privacy and captures](privacy-and-captures.md).
- [CI and local checks](../testing/ci-and-local-checks.md) — the tester's view of the same jobs.
