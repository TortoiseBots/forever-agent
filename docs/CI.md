# CI jobs

The workflows under `.github/workflows/` run four checks beside the `test` gate.

## Running CI on a branch without a PR

Push a branch to `ci/<name>` to run the full CI on GitHub without a PR.
Delete the `ci/` branch after the result.
Push-only: PR gating is unchanged (`test` and `navmesh` run on pushes to `ci/**`, same as on `dev`/`main`).

## `test.yml` — the PR gate

It runs on every push to `dev`/`main`/`ci/**` and every non-draft PR whose base is either branch (or a stacked feature
branch), as parallel jobs that end in one job named `test`:

| Job | What it runs |
|---|---|
| `lint` | `make fmt-check vet nocgo` (gofmt, `go vet`, a no-cgo build), the shard script's self-test, `make cross`, the differential table against the pinned `TortoiseBots/tortoise-trials` checkout (Python 3.12; a skip or fewer than 50 passed sub-tests fails the step, so the table cannot go green without Python), `staticcheck ./...` with the pinned release **2026.2.2**, then a cgo build |
| `race-rest` | `go test -race -count=1 -timeout 20m` on every package except `./cmd/forever-agent` (`go list ./... \| grep -v '/cmd/forever-agent$'`) |
| `race-cmd` | 4 shards (matrix `0`..`3`) of the top-level tests of `./cmd/forever-agent`, each `go test -race -count=1 -timeout 20m -v -run '^(A\|B\|...)$'` |
| `race-cmd-check` | fails unless the tests the 4 shards ran, together, are exactly the package's full `go test -list` output, each once |
| `test` | `needs` all four jobs, runs with `if: always()` and fails unless every one of them succeeded; this is the check branch protection requires (a skipped or cancelled job fails it too). A draft PR skips every job, `test` included, as before |

The staticcheck binary is cached (`actions/cache`) on the pinned version and the hash of `go.mod` (the Go version it is
built with), so a warm run skips the `go install`. The repo has no suppressions: a staticcheck finding is fixed in the
code or, when a check is a false positive, disabled one check at a time in a commented `staticcheck.conf` (never a whole
category). Changing the pin means changing `STATICCHECK_VERSION` in the workflow.

### The `cmd/forever-agent` shards

`cmd/forever-agent` has about 1500 top-level tests and takes 8-10 minutes under `-race`, so it is split.
`tools/ci/shard-tests.sh` lists the package's `Test*`, `Fuzz*` and `Example*` names (`go test -list`; fuzz targets run
their seeds and examples run too; benchmarks are not run by `go test` and are left out), sorts them and deals them to the
shards round robin (name 0 to shard 0, name 1 to shard 1, ..., name 4 to shard 0). Sub-tests run with their parent.
Each shard prints its `go test -v` log, writes the top-level names that log shows as started (`ran`) and uploads them as
an artifact; `race-cmd-check` downloads the four lists and runs `shard-tests.sh check`, which fails on a test that no
shard ran, a test that ran twice, or a name outside the full list. A new test lands in a shard by itself, no list to
maintain. `tools/ci/shard-tests.sh selftest` covers the script (partitions for several shard counts, a dropped name, a
duplicate, an unknown name) and runs in `lint`.

The deal is by count, not by time: one slow test makes its shard slow. If the shards drift apart, the fix is a change of
the shard count (`SHARDS` and the matrix in `test.yml` and the `4` in the `race-cmd-check` step), not a hand-kept list.

Locally, one shard:

```sh
tools/ci/shard-tests.sh list ./cmd/forever-agent >all-tests.txt
go test -race -count=1 -timeout 20m -run "$(tools/ci/shard-tests.sh regex 0 4 all-tests.txt)" ./cmd/forever-agent
```

## `navmesh.yml` — the real map files

Runs on pushes to `dev`/`main`/`ci/**`, on non-draft PRs (`opened`, `synchronize`, `reopened`, `ready_for_review`) and by hand.
It downloads the server's navmesh files from the **private** `pjerra/tb-server-data` repository, release tag
`data-2026-10-29`, into `$RUNNER_TEMP/mmaps`, then:

- runs `go test -count=1 ./internal/navmesh/ ./internal/dungeon/ ./internal/dashboard/` with `CGO_ENABLED=1`: the
  `*_test.go` files of `internal/navmesh` and `internal/dungeon` that skip without `TURTLE_AGENT_MMAPS`, plus
  `internal/dashboard` (its dashboards read the same plans and routes);
- builds `turtle-agent` with cgo and runs `tools/ci/plan-check-all.sh`, which checks every plan whose status starts with
  `live-checked` or `mapped` (“live or prep”: a `draft` has not had its mapping walk and is left out of the navmesh gate).

The gate fails only on a point failure (plan-check's navmesh FAIL: a point unreachable, too far, or needing a mapping
walk). Every error-severity placement lint (`water`, `off-floor`, `edge`, `ledge`, `short hop`, `aggro`, `boss_aggro`, `patrol`,
`joiners`, `reach`, `arrival`, `hop_len`, `climb_anchor`) is printed as `WARN <plan>: N placement lint finding(s) do not
fail the gate` with a count per kind, and the job stays green (lead's decisions, 2026-11-03). `--strict-lints` on
`plan-check-all.sh` makes every lint error fail the job, which is how to see them as blockers before a plan's fix round.

The download is `tools/ci/fetch-mmaps.sh`: it joins split assets (`<name>.tar.zst.part-aa`, `.part-ab`, ...`) into
`<name>.tar.zst`, verifies the release's `SHA256SUMS`, unpacks the archives into one flat folder and checks at least one
checksum. `actions/cache` keeps the folder keyed on the release tag and the SHA256SUMS hash.

If the `TB_DATA_TOKEN` secret is absent (forks, or before the owner adds it), the job prints
`navmesh checks skipped: TB_DATA_TOKEN not set` and succeeds without running the navmesh tests or the plan check.

### The `TB_DATA_TOKEN` secret

Create a **fine-grained personal access token** with:

- repository access: only `pjerra/tb-server-data`;
- permission: **Contents — Read-only** (nothing else; no metadata beyond what GitHub requires).

Store it as an Actions secret named `TB_DATA_TOKEN` on this repository. The workflows pass it to `gh` as `GH_TOKEN`;
it is never written to a file or printed. A fork's PRs do not receive repository secrets, so they take the skip path.

Local equivalent:

```sh
GH_TOKEN=... tools/ci/fetch-mmaps.sh data-2026-10-29 pjerra/tb-server-data /tmp/tb-data /tmp/mmaps
TURTLE_AGENT_MMAPS=/tmp/mmaps tools/ci/plan-check-all.sh --bin ./bin/turtle-agent --dir dungeons --mmaps /tmp/mmaps
```

## `plan-drift.yml` — plans versus TortoiseBots exports

Runs on pushes to `dev`/`main`, nightly at 04:17 UTC, and by hand. It checks out the public
`Sagiroth/TortoiseBots` repository at `dev` (read-only, no token) under `tb/` and runs:

```sh
go run ./tools/plandrift --plans dungeons --sql tb/data/sql/world
```

`tools/plandrift` finds the newest migration with a plan's `ai_dungeon_plan` row (a migration that only replaces one
leg's steps is an update, not an export), builds the rows the current exporter writes from `dungeons/<plan>.json`, and
reports per plan: `in sync`, `differs` (row counts and the first differing step), `not exported upstream`, or
`current export refused` (with the exporter's reason). The plan row's `source` and `comment` are not compared: the
source names the exporting commit and the comments carry export-time notes. The output is a table in the job's step
summary. The first version is **report-only** (`continue-on-error: true`): a diverge is expected while a plan is edited
before its next export, so the job never blocks a PR. A local run is the same `go run` line with `--sql
~/TortoiseBots/data/sql/world`.

## `fuzz.yml` — nightly parser fuzzing

Runs nightly at 03:17 UTC and by hand: `make fuzz FUZZTIME=2m` (2 minutes per `Fuzz*` target of `internal/game`,
`internal/objects` and `internal/tbm`), with a 60-minute job timeout. A crash fails the job and the new corpus files
under `**/testdata/fuzz/**` are uploaded as the `fuzz-corpus` artifact (14 days) so the seed can be committed with the
fix.
