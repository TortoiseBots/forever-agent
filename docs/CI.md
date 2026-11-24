# CI jobs

The workflows under `.github/workflows/` run four checks beside the `test` gate.

## Running CI on a branch without a PR

Push a branch to `ci/<name>` to run the full CI on GitHub without a PR.
Delete the `ci/` branch after the result.
Push-only: PR gating is unchanged (`test` and `navmesh` run on pushes to `ci/**`, same as on `dev`/`main`).

## `test.yml` — the PR gate

It runs on every push to `dev`/`main`/`ci/**` and every non-draft PR whose base is either branch (or a stacked feature
branch), as parallel jobs that end in one job named `test` (12 jobs a run, at most 8 of them running at once; the org runs at most 20 jobs at once across its repos, see *The race shards*):

| Job | What it runs |
|---|---|
| `lint` | `make fmt-check vet nocgo` (gofmt, `go vet`, a no-cgo build), `make docs-check` (the manual under `docs/manual` as an OKF bundle: `tools/verify_okf.py` and its tests), the shard scripts' self-tests, `make cross`, the differential table against the pinned `TortoiseBots/tortoise-trials` checkout (Python 3.12; a skip or fewer than 50 passed sub-tests fails the step, so the table cannot go green without Python), `make tools-test` (the automatic label rules and the label sync), `staticcheck ./...` with the pinned release **2026.2.2**, then a cgo build |
| `race-shards` | `go test -race -count=1 -timeout 20m` for the whole module in 9 jobs, at most 6 running at once, matrix `group` (`cmd`, `world`, `rest`) x `shard` (`0`..`2`), see below |
| `race-shards-check` | fails unless what the 3 shards of each group ran, together, is exactly the group's full list, each once |
| `test` | `needs` all three jobs, runs with `if: always()` and fails unless every one of them succeeded; this is the check branch protection requires (a skipped or cancelled job fails it too). A draft PR skips every job, `test` included, as before |

The staticcheck binary is cached (`actions/cache`) on the pinned version and the hash of `go.mod` (the Go version it is
built with), so a warm run skips the `go install`. The repo has no suppressions: a staticcheck finding is fixed in the
code or, when a check is a false positive, disabled one check at a time in a commented `staticcheck.conf` (never a whole
category). Changing the pin means changing `STATICCHECK_VERSION` in the workflow.

### The race shards

Two packages are far longer than the rest: `cmd/forever-agent` (about 1500 top-level tests, 8-10 minutes under `-race`)
and `internal/forever/world` (about 430 tests, about 10 minutes). Each of them is a group of 3 shards split **by test
name**; every other package (about 120) is the `rest` group, split **by package**.

- **By test name** (`group` `cmd` and `world`): `tools/ci/shard-tests.sh` lists the package's `Test*` and `Fuzz*` names
  (`go test -list`, built with `-race` like the run, so a race-only file counts and a `!race` file does not; benchmarks
  are not run by `go test` and are left out), sorts them and deals them to the shards round robin (name 0 to shard 0,
  name 1 to shard 1, ...). A shard runs `go test -race -v -run '^(A|B|...)$'`; sub-tests run with their parent. A new test
  lands in a shard by itself, there is no list to maintain. The deal is by count, not by time. `go test -list` does not
  print Example functions, so `list` fails on a package that has one rather than shard without it.
- **By package** (`group` `rest`): `tools/ci/shard-pkgs.sh` takes `go list -race ./...` minus the two packages above and
  packs the rest by the seconds in `tools/ci/pkg-times.txt` (greedy, longest first). A package that is missing from that
  file (a new one) counts as 1 s and goes to the lightest shard, so it is never dropped; `shard` and `load` warn about each
  package that has tests but no line there. A name in the file that no longer exists is ignored.
  `shard-pkgs.sh load 3 <list> tools/ci/pkg-times.txt` prints the estimated seconds per shard. When a package grows a lot,
  update its line (the `ok ... 12.3s` lines of a CI run give the numbers); a stale file only makes the shards uneven, it
  cannot drop a package.
- **No test can be dropped**: each shard writes down what its `go test` log shows as run (top-level test names, or the
  packages with a result line) and uploads it. A package whose test binary ran no test (`[no tests to run]`) or did not
  build does not count as run. `race-shards-check` downloads the lists and runs
  `shard-tests.sh check <full list> <shard lists>` per group, which fails on a test or package that no shard ran, one that
  ran more than once (in one shard or in two: a repeat inside a shard is reported, not merged), a name outside the full
  list, or an empty full list. The full list is computed fresh in that job: `go test -list` for the two packages,
  `shard-pkgs.sh list` for `rest` (which itself fails if one of the two packages stops existing, so a rename cannot
  silently move it). An empty shard runs nothing: `regex` and `shard` print nothing and say so on stderr, and the job
  skips `go test` rather than give it an empty `-run` or no package. Both scripts have a `selftest` that runs in `lint`:
  partitions for several shard counts, a dropped name, a duplicate, an unknown name, an empty shard, Unicode test names,
  a small module built with `-race` (a race-only file listed, a `!race` one not), and the `[no tests to run]` and
  build-failure cases.

The shard count is 3 everywhere (`SHARDS` and the `shard` matrix in `test.yml`, and the `3` in the check step): one more
shard per group is nine more jobs, so stay inside the 12 jobs a run that the org limit leaves room for.

**Concurrency cap: six race shards at once.** The org runs at most 20 jobs at once across all its repos. Before the cap,
one run started `lint` and all nine race shards together, so it held 10 of those slots, and other repos' runs queued
behind it. `race-shards` stays one matrix of nine with `strategy.max-parallel: 6`: at most six race shards run at once,
so one run takes at most 8 slots (six race shards, `lint` and `race-shards-check`). The price is wall time: the last
three shards start only when earlier shards finish. `race-shards-check` still `needs` the whole matrix, so the check
waits for every shard.

**Known limit: the `!cgo` tests run in no job.** `cmd/forever-agent/forever_runmap_nocgo_test.go` and
`internal/navmesh/raycast_nocgo_test.go` have `//go:build !cgo`. The race shards need cgo, `make nocgo` only builds and
`make cross` only builds and vets, so nothing runs them. This was already so before the sharding.

Locally, one shard of a long package:

```sh
tools/ci/shard-tests.sh list ./cmd/forever-agent >all-tests.txt
re="$(tools/ci/shard-tests.sh regex 0 3 all-tests.txt)"
if [ -n "$re" ]; then
    go test -race -count=1 -timeout 20m -run "$re" ./cmd/forever-agent
fi
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

### Forever navmesh subset

The same workflow's `forever-navmesh` job runs `internal/navmesh`'s Forever tests (`forever_real_test.go`). They need
the Forever repack, a different generation from the Turtle mmaps (four-digit names, its own header and flag bits), so
the job downloads a subset from the same private release under its own tag (`FOREVER_DATA_TAG`, currently
`forever-mmaps-subset-1`), sets `FOREVER_MMAPS_DIR` and `FOREVER_MMAPS_SUBSET=1`, and runs
`go test -v -run 'TestForeverReal' ./internal/navmesh/`. A second step then fails unless the log carries
`--- PASS:` for `TestForeverRealMap0HumanStart`, `TestForeverRealMap389Ragefire` and `TestForeverRealEveryTileLoads`:
without those lines the package skipped and proved nothing. `FOREVER_MMAPS_SUBSET` tells the every-tile test that
the folder is the subset rather than the whole repack, so it loads every tile present but does not demand the
repack's 74 maps and 3000+ tiles. `TestForeverRealRoadRoute` may skip with its own message (the repack has no
`NAV_ROAD` polygon).

The subset is the whole of Ragefire Chasm plus one block of map 0:

- `0389.mmap` and every `0389_*.mmtile` (Ragefire Chasm);
- `0000.mmap` and the tiles within one tile of the tile holding the human start (`-8913,-118`), a 3 x 3 block.

`tools/forevermeshsubset` builds it; nothing is committed (the files are derived from client data):

```sh
go run ./tools/forevermeshsubset --src PATH --dst forever-mmaps --map 389 --near 0:-8913,-118:1
tar --zstd -cf forever-mmaps.tar.zst -C forever-mmaps .
sha256sum forever-mmaps.tar.zst > SHA256SUMS
```

Upload both files to a new release tag of the data repository and set `FOREVER_DATA_TAG` in `navmesh.yml` to it. The
archive is well under 50 MB, and the cache key is the tag plus the `SHA256SUMS` hash, so a new tag downloads once.
`tools/forevermeshsubset` refuses a destination inside the source, a map without its `.mmap`, and a symlink (it never
follows one). Without `TB_DATA_TOKEN` the job prints `forever navmesh checks skipped: TB_DATA_TOKEN not set` and
passes.

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

## Automatic labels

Two workflows label work by itself, one for pull requests and one for issues. Both only ADD, and the people-owned
labels (the priority labels `Must have`, `Should have`, `Nice to have`, `Maybe later`, and `TortoiseBots`) are never
touched. The labels the automation owns and their colours and descriptions are in `.github/labels.yml`;
`tools/sync_labels.py` creates and updates them (it never deletes or renames one).

| Workflow | Trigger | Rules | Labels |
|---|---|---|---|
| `label-pr.yml` | `pull_request_target`: opened, synchronize, reopened | `.github/labeler.yml` (changed files and branch name), through the official `actions/labeler` | the area labels, `bug` on `fix/*`, `enhancement` on `feat/*`, `documentation` on `docs/*`, `data` on `data/*`, `ci` on `ci/*` |
| `label-issues.yml` | `issues`: opened only | `tools/label_issue.py` (`RULES`, whole words, case-insensitive) | e.g. "dungeon run" -> `dungeon`; "plan file" or "navmesh" -> `plans`; "vendor" -> `player-actions`; "live run" -> `live-check`; a bug word in the title -> `bug`; otherwise an `Agent:`/`[agent]` title -> `enhancement` |

**Issue keyword labels are computed once, when the issue opens.** The workflow does not run on `edited`: a later title
or body change neither re-adds a label a person removed nor refreshes the labels the new text no longer matches, so a
person's correction stands (remove the stale label by hand). The script only adds, from its own rule table.

**Pull-request labels run on every push** (opened, synchronize, reopened). `sync-labels: false` keeps the action
add-only: it never removes a label, and a label a person removed comes back only if a later push still matches the
rule that sets it.

A keyword rule is matched on the **title** for `raid`, `battleground`, `dungeon`, `dashboard`, `guild` and
`documentation`: those words turn up in bodies in ticket lists and field lists ("raids last", "a settings file
(..., navmesh, plans)", "the docs say so") without making the issue about that area. `plans` needs a plan phrase —
"dungeon plan", "plan file", "plan-check", "plan-probe", "plan tools", "route tools" or "navmesh" — not the bare word
"plan"; `daemon` needs the word "daemon", not "serve", "rpc" or "method" alone. The other rules read the title and
body.

`label-pr.yml` runs on `pull_request_target` and never checks out the pull request's code: labeler reads the
changed-file list from the event and `.github/labeler.yml` from the **base ref** through the API, then labels through
the API. There is deliberately no `actions/checkout` step; a fork's branch cannot change what the job runs. Its
permissions are `contents: read` and `pull-requests: write` only. `label-issues.yml` passes the title and body to the
script as environment variables, never on the command line, so an issue text with shell syntax is only text; its only
write permission is `issues: write` (`contents: read` is for checking out the script itself from the default branch).
Both workflows pin their actions to a full commit SHA with the version in a trailing comment; update a pin only in a
reviewed PR.

### Adding a rule

- **A path or branch rule:** add the label block to `.github/labeler.yml`. `any:` ORs its matchers, a plain list ANDs
  them; `**/` spans zero or more directories and `*` never crosses `/`. The backfill reads the same file, so nothing else
  changes.
- **A keyword rule:** add `("<label>", r"<alternatives>", "both")` to `RULES` in `tools/label_issue.py` (`"title"` for
  a word too common in bodies, as the five area rules above do). The alternatives are matched case-insensitively between
  word boundaries (`braid` is not `raid`).
- **A new label:** add its `name`/`color`/`description` block to `.github/labels.yml`, then run
  `python3 -I tools/sync_labels.py`; the workflows pick it up from the rule files.

### The backfill

`tools/label_issue.py --backfill` labels every open issue (by its title and body, on each rule's scope) and every open
pull request (by its changed files, with the path rules of `.github/labeler.yml`; branch-name rules apply only to new
pushes, not to the backfill). It skips items that already have a label and only adds the missing ones:

```sh
python3 -I tools/label_issue.py --backfill --dry-run   # "issue 12: +dungeon,bug", changes nothing
python3 -I tools/label_issue.py --backfill             # applies them
python3 -I tools/sync_labels.py --dry-run              # the labels first: create/update, never delete
```

`--prune` (with `--backfill`) is the one-time clean-up after a rule is narrowed. Besides adding the missing labels, it
removes from each open **issue** every label the automation can apply that the current rules no longer give. It only
touches the labels the automation applies (the `RULES` labels and `bug`/`enhancement`), never a label outside
`.github/labels.yml` (the people-owned priority labels and `TortoiseBots`), and never a label a rule still gives. The
workflow never uses it:

```sh
python3 -I tools/label_issue.py --backfill --prune --dry-run   # "issue 115: -raid", changes nothing
python3 -I tools/label_issue.py --backfill --prune             # applies the clean-up
```

Both scripts take `--repo OWNER/REPO` (default `$REPO`, else the current repository) and need the `gh` CLI with a token
that can edit the repository. Their tests are part of `make tools-test` (`make test` runs it too) and of the `lint` job,
so a rule change cannot merge without them:

```sh
python3 -m unittest tools/test_label_issue.py tools/test_sync_labels.py
```
