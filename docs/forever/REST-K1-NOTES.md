# Forever CLI round K1 notes: fleet and slot setup on Forever

Scope: `fleet --forever` (one `forever serve` per fleet-file member) and
`slot setup --forever` (lease, dir, plans as today; no settings file;
prints the `forever serve --dir` line). Code: `cmd/forever-agent/fleet_forever.go`
(+ tests), `fleet.go` (the `--forever` branch and the shared ready check),
`slot_setup_forever.go` (+ tests), `cli_args.go` (the `--forever` slot flag),
usage lines, README.

## Design

* A `fleet --forever` member is `{account, character, dir, listen,
  password_file}`. The `forever serve` command line is `forever serve
  --rest/--rpc/--ca/--build` from the slots file's per-server `forever` block
  (the same block the trials runner reads: `internal/trials/live.ServerExt`,
  read only) plus the member's own `--account/--character/--password-file/
  --listen/--dir`. The password travels by file, never on the command line;
  the fleet file carries no secret (a `password` key is an unknown field and
  is refused, like any other).
* Ready is the serve link lifecycle: `online` counts as in world (the K1
  failing case — the old check waited for the Turtle daemon's `in_world` and
  timed out). The rule is per mode (`fleetOpts.ready`): `fleet --forever`
  uses `fleetReady` (`online` only); the Turtle fleet keeps
  `fleetReadyTurtle` (`in_world` only, exactly as before; `online` never
  passes there). `--slots` and `--server` without `--forever` are refused.
* Starts are paced by the same login budget as the old fleet, one login per
  5 s (`fleetForeverPace`): `newLoginBudget(1, 5s)`. The budget is local to
  the run (no shared port: serve logins share realmd's per-IP limit like
  any login). `--reconnect` is refused with `--forever`: a serve reconnects
  within its own budget.
* `slot setup --forever` runs the same helpers as the old path
  (`slot_setup_common.go`: lease check, dir claim with symlink checks, 0700,
  never-overwrite and cleanup, binary and plans copy, the lease/dir re-check
  and the manifest write), so the safeguards cannot drift. `slot.json` holds
  the real server, slot, holder and lease id and the derived `settings` path
  (no file is written there), so `teardown` and `rpc` accept a fresh slot and
  teardown releases the real lease. `--mmaps-dir` is refused. It writes no
  `settings.json`, and prints the `forever serve --dir` line (with the
  `<password-file>` placeholder, never a secret) instead of the old `login
  --config` line. The K1 failing case (`TestSlotSetupForeverNoSettings`)
  pins exactly this: three lines, no settings file, the serve line, the
  manifest marker.
* `fleet --forever` needs `--slots` (the `forever` block) and `--server`
  when the slots file names more than one block; one block is unambiguous.
* Review round (all findings fixed):
  * `forever serve --dir` (`serveCheckDir`) keeps an existing `slot.json`
    that matches (dir, port, character, account when named), so the lease
    identity from setup survives; it refuses a non-matching `slot.json`, an
    unreadable one, or a non-empty dir with none. A fresh dir gets serve's
    own manifest (now with the account).
  * Before any login `fleetForeverCheck` clears every member: the account is
    a slot account of the server, the character is allowed, no live lease of
    another holder holds the account, and a member dir whose manifest names a
    lease needs that lease live and equal (server, slot, holder, lease id,
    port, account, character). A held account exits 15 before one serve
    starts. A dir with no manifest (or a bare serve's) passes only when no
    slot of the account is leased.
  * Fleet files with a repeated account (any case) are refused.
  * Pass 2: a non-blocking flock per server and account, held for the fleet's life and taken before the check (`fleetForeverLockAccounts`), so two fleets started at once cannot both log in on one account; a crash frees it. A second fleet exits 15 "another fleet". Leases are not renewed by the fleet (same as `login`): the holder renews with `lease renew`.

## Tests

* `TestFleetForeverOnlineCountsAsInWorld` (failing case): a fake answering
  `online` is ready; `reconnecting` is not.
* `TestFleetForeverPacing`: two logins per window, the third waits and says
  so; the second does not wait.
* `TestFleetFileRejectsPassword`, `TestFleetForeverChildArgs` (the serve
  line, no secret, `--build` only when the block names one),
  `TestFleetForeverInWorldStillCounts`.
* `TestSlotSetupForeverNoSettings` (failing case), plus the `--forever`
  row in `TestSlotSetupFlagOrderSameOptions`.
* Review round: `TestSlotSetupForeverManifestAcceptedByTeardown`,
  `TestSlotSetupForeverRefusesNonEmptyDir`, `TestSlotSetupForeverTakeoverLeavesNothing`,
  `TestSlotSetupForeverRefusesMmapsDir`, `TestServeKeepsSetupManifest`,
  `TestServeRefusesForeignDir`, `TestFleetForeverCheck*`,
  `TestFleetForeverRefusesBeforeAnyLogin`, `TestFleetForeverRefusesDuplicateAccount`,
  `TestFleetTurtleReadyRuleUnchanged`, `TestFleetSlotsServerNeedForever`.
  `TestSlotSetupForeverNoSettings` now uses a settings_from file with a real
  password string and asserts it is in no output and no file in the dir;
  mutation check: printing that password in the serve line made it fail
  (reverted).
* Old slot setup tests pass unchanged through the shared helpers.

## Gates (tree at 108b492f, clean and committed, dirty=0)

* `make fmt-check`, `make vet`, `make cross`: clean.
* `vm-test ta <local worktree>` (go test -count=1 ./... && go vet ./...):
  `RESULT: PASS  kind=ta sha=108b492f dirty=0 files=2312`, rc=0.
* Race run, `vm-test ta <local worktree> -- go test -race -count=1 -timeout 20m
  ./cmd/forever-agent/... ./internal/slots/... ./internal/forever/...`:
  `ok github.com/pjerra/forever-agent/cmd/forever-agent 730.132s`, every
  other package ok, except `internal/forever/world`, where
  `TestRouteWalkCancelBeforeFirstLeg` failed twice on the VM with `fake
  server write: connection reset by peer` (VM load average about 80, other
  sessions' jobs). That package is untouched by this round. On the laptop
  `go test -race -count=1 ./internal/forever/world/` passed (`ok 580.286s`)
  and that test alone passed under `-race -count=3`.
  A plain default `-timeout` (10 m) is too short for cmd/forever-agent under
  `-race` on a loaded VM; the Makefile's 20 m is used.

## Live steps

Not run (no live server in this round). When one is up:

```
# slots file with one forever block: rest, rpc, ca, build
forever-agent fleet --forever --config fleet.json --slots slots.json --server s1
forever-agent slot setup --forever s1 1 --holder me --dir /tmp/serve-1 --slots slots.json
```

A successful fleet prints one table with every member `online` and
`N character(s) in world`; a setup prints the three lines and no
`settings.json` lands in the dir.
