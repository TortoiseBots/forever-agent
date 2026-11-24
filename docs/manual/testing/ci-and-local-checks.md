---
type: Guide
title: CI and local checks
description: What runs on every push and nightly, and how to run the same checks, shards, capture scrub and gates locally.
tags: [testing, ci, make, checks, captures]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:15Z }
sources:
  - id: ci-md
    resource: docs/CI.md
  - id: makefile
    resource: Makefile
  - id: workflow-test
    resource: .github/workflows/test.yml
  - id: workflow-navmesh
    resource: .github/workflows/navmesh.yml
  - id: workflow-fuzz
    resource: .github/workflows/fuzz.yml
  - id: workflow-contract
    resource: .github/workflows/contract-check.yml
  - id: workflow-flake
    resource: .github/workflows/flake-hunt.yml
  - id: workflow-privacy
    resource: .github/workflows/privacy-scan.yml
  - id: workflow-security
    resource: .github/workflows/security.yml
  - id: scrub-cmd
    resource: cmd/forever-agent/forever_scrub.go
---

The repository runs a small set of checks on every change and a wider set nightly. The PR gate is one job named `test`; everything else either guards a data-dependent area (navmesh, plans) or hunts a class of problem on a schedule (flakes, fuzzing, privacy, vulnerabilities). This page says what each one covers and how to reproduce it on one machine.

# Local: the Makefile

The build uses the Go in `~/.local/go` unless you override `GO`; the toolchain is local (`GOTOOLCHAIN=local`).

| Target | What it runs |
|---|---|
| `make test` | `fmt-check`, `vet`, `nocgo`, then `go test -race -count=1 -timeout 20m ./...` (the package timeout is 20 minutes because the command package takes 8-10 under `-race`) |
| `make fmt` | `gofmt -w .` |
| `make fmt-check` | fails when `gofmt -l .` prints anything |
| `make vet` | `go vet ./...` |
| `make nocgo` | a build with `CGO_ENABLED=0`; `path_to` and `walk_path` then answer that the binary has no navmesh engine |
| `make cross` | builds Windows/amd64 and macOS/arm64 (no cgo) and vets the command and Forever packages for Windows |
| `make build` | `bin/forever-agent` |
| `make fuzz` | every `Fuzz*` target of the parser packages for `FUZZTIME` each (default 5 s) |
| `make tbm-contract` | replays TBM: lines through the real add-on (Lua 5.1, network on first run) |
| `make tbm-contract-forever` | replays `contract/tbm/v1/cases/*.case` through the Forever manager add-on |
| `make addon-host` | the add-on host's end-to-end tests against the real add-on |
| `make kb-rotations` | regenerates the KB rotations and spell oracle (needs PyYAML and a KB checkout) |

`make test` is the single-machine equivalent of the PR gate, not of the nine shards: it is one `go test` over the whole module.

# CI: the workflows

| Workflow | When | What it does |
|---|---|---|
| `test` | every push to `dev`, `main`, `ci/**`; every non-draft PR | the PR gate (below) |
| `navmesh` | same pushes and PRs, and by hand | the real map files: navmesh, dungeon and dashboard tests with cgo, then `plan-check-all` on every live or mapped plan |
| `plan-drift` | pushes to `dev`/`main`, nightly 04:17 UTC | plans versus the upstream TortoiseBots exports (report-only) |
| `fuzz` | nightly 03:17 UTC | `make fuzz FUZZTIME=2m` over the parsers; a crash fails and its corpus file is uploaded |
| `contract-check` | nightly 04:29 UTC, PRs touching `contract/**` | the add-on protocol contract tests and the Lua replay |
| `flake-hunt` | nightly 02:17 UTC | tests with a known flake history run 10 times under `-race`; one issue is created or updated |
| `privacy-scan` | nightly 03:43 UTC on `main` | greps the tree for private paths, hosts, e-mails and addresses |
| `security` | weekly, Monday 05:07 UTC | `govulncheck` (pinned) and a Go-version notice |

To run the full CI on a branch without a PR, push it as `ci/<name>`; delete the branch afterwards.

## The PR gate

`test` is one required check; it `needs` three jobs and fails unless all succeed:

| Job | Contents |
|---|---|
| `lint` | `make fmt-check vet nocgo`, the shard scripts' self-tests, `make cross`, `make docs-check` (the user manual as an OKF bundle), `make tools-test` (the automatic label rules and the label sync), the differential table against a pinned `tortoise-trials` checkout (Python 3.12), `staticcheck ./...` pinned to 2026.2.2, then a cgo build |
| `race-shards` | `go test -race -count=1 -timeout 20m` over the whole module in nine jobs |
| `race-shards-check` | fails unless the nine shards, together, ran every test and package exactly once |
| `test` | the required check: passes only when the three above did; a skipped or cancelled job fails it |

A draft PR runs nothing, `test` included.

The differential table compares the Go scorers with the Python reference on a hand-made table. It is pinned to a commit of the public reference repository; a skip (the reference absent) or fewer than 50 passed sub-tests fails the step, so the table cannot go green without Python.

## The race shards

Two packages are far longer than the rest: `cmd/forever-agent` and `internal/forever/world`. They are split **by test name** into three shards each; every other package is the `rest` group, split **by package** using the seconds in `tools/ci/pkg-times.txt`. The shard count is 3 everywhere, at most six race shards run at once, and no test can be dropped: each shard uploads the names it ran and the check job fails on a missing, duplicated, unknown or empty list.

Locally, one shard of a long package:

```sh
tools/ci/shard-tests.sh list ./cmd/forever-agent > all-tests.txt
re="$(tools/ci/shard-tests.sh regex 0 3 all-tests.txt)"
if [ -n "$re" ]; then
    go test -race -count=1 -timeout 20m -run "$re" ./cmd/forever-agent
fi
```

## The navmesh gate

The real map files live in a private data repository and are downloaded by `tools/ci/fetch-mmaps.sh` (split archives, SHA256SUMS verified). The gate runs the navmesh, dungeon and dashboard tests with cgo and then `tools/ci/plan-check-all.sh`, which checks every plan whose status starts with `live-checked` or `mapped`. Only a point failure (an unreachable or too-far point, or one needing a mapping walk) fails; every placement lint prints as a `WARN` count and stays green unless `--strict-lints` is passed. Without the `TB_DATA_TOKEN` secret the job prints a skip line and passes.

The `forever-navmesh` job in the same workflow runs the Forever subset (Ragefire Chasm plus one block of map 0) and fails unless three named tests report `--- PASS:`, so a package that skipped cannot pass for proof. Uploading a new subset is done with `tools/forevermeshsubset`; nothing derived from client data is committed.

# Scrubbing a capture before sharing it

A `forever serve --capture-dir DIR` capture holds raw server packets. Before it leaves your machine:

```
forever-agent forever scrub-check [--slots FILE] [--account EMAIL]... [--password-file FILE]
                                  [--session-key-file FILE]... [--auth-token-file FILE]... [--redact DIR] <dir>...
```

The needles are every account of a slot under a server whose slots entry has a `forever` block, each `--account` (raw and upper case), the password (`--password-file`, else `FOREVER_PASSWORD`), each session key (raw and its hex text, which is decoded too) and each auth token. Every file of every run is read in every form a server can send a needle (as is, reversed, upper/lower case, hex) and additionally scanned for any IPv4 address. A finding names the run, the file and the class, never the secret.

| Exit | Meaning |
|---|---|
| `0` | clean |
| `7` | a secret or address was found |
| `2` | usage, settings, secret-file or read error (also: no needles, a symlink, a file past the bounds, a run over 33 MiB) |

`--redact DIR` writes a cleaned copy of one capture directory instead: every match is masked by one `*` per byte (manifests keep their lengths), the copy is read back to prove it clean, and the original is untouched. The same class of rule is enforced nightly by `privacy-scan` over the whole tree.

# Reproducing a nightly job

| Job | Local equivalent |
|---|---|
| `fuzz` | `make fuzz FUZZTIME=2m` |
| `navmesh` | `GH_TOKEN=... tools/ci/fetch-mmaps.sh <tag> <repo> /tmp/tb-data /tmp/mmaps`, then `TURTLE_AGENT_MMAPS=/tmp/mmaps tools/ci/plan-check-all.sh --mmaps /tmp/mmaps` |
| `plan-drift` | `go run ./tools/plandrift --plans dungeons --sql <checkout>/data/sql/world` |
| `contract-check` | `go test -race ./contract/... ./internal/forever/tbmproto/...` and `TBM_CONTRACT_STRICT=1 sh tools/tbmcontract/forever/run.sh` |
| `flake-hunt` | `go test -race -count=10 -run '<the known flaky tests>' ./...` |
| `security` | `govulncheck ./...` |

# What a failure means

- **`test` red, one race shard red** — a real failure or a race in that shard; run its `-run` regex locally as shown above.
- **`race-shards-check` red with everything green** — a test or package ran nowhere or twice; the check prints the names.
- **`lint` red on the differential table** — the Go scorer and the Python reference disagree; the table names the case.
- **`navmesh` red on a plan** — a point failed the navmesh check; the placement lints alone do not fail it.
- **`privacy-scan` red** — the tree names a private path, host or address; the job lists file and line.
- **`scrub-check` exit 7** — a capture still holds a secret or an IP address; use `--redact` to produce a shareable copy, and never commit the original.

# Further reading

- [Privacy and captures](../developers/privacy-and-captures.md): what must never be shared.
- [Contributing](../developers/contributing.md): formatting, the test suite and PR rules.
- [Traces and records](traces-and-records.md): the recordings these checks protect.
- [Plan tools](../dungeons/plan-tools.md): what `plan-check` reports point by point.
