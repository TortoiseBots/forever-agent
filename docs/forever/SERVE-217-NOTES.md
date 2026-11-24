# `forever serve --settings`: a Forever slot in one file, and the serve's lease

Closes **#217** (parity re-score T3: the `lease` and settings-file rows). A Forever slot can be
started from one settings file instead of a long command line, and the serve now holds and renews
the lease its `slot.json` names.

## The settings file

```text
forever-agent forever serve --settings /srv/forever/slot1/settings.json
```

A flag given on the command line always wins; the file fills the flags the command line left
unset; a flag that neither names keeps its default. `--create`, `--create-choice`,
`--create-no-appearance` and `--login-budget` stay command-line only (a one-off character or a
fleet child's wiring).

Example (no real host, account or path; `example.invalid` is reserved):

```json
{
  "account": "slot1@example.invalid",
  "character": "Slotchar",
  "rest": "login.example.invalid:1119",
  "rpc": "login.example.invalid:1118",
  "ca": "config/ca.pem",
  "server_name": "example.invalid",
  "build": 70959,
  "game_account": 1,
  "locale": "enUS",
  "listen": "127.0.0.1:28771",
  "dir": ".",
  "navmesh": "~/forever/maps/mmaps",
  "plans_dir": "dungeons",
  "addon": "on",
  "disconnect_grace": "30s",
  "password_file": "password"
}
```

| Key | serve flag | Notes |
|---|---|---|
| `account`, `character` | `--account`, `--character` | The slot's account and character. |
| `rest`, `rpc`, `ca` | `--rest`, `--rpc`, `--ca` | The Forever login endpoints and the root CA file. |
| `server_name` | `--server-name` | Name to verify in the login certificate. |
| `build` | `--build` | Client build; `0` (absent) is the automatic pick. |
| `game_account` | `--game-account` | Join ticket `#N`; absent is the first listed. |
| `locale` | `--locale` | One of the client languages. |
| `allow_instance_redirect` | `--allow-instance-redirect` | Boolean. |
| `world_checks_build_auth_key` | `--world-checks-build-auth-key` | Boolean. |
| `listen` | `--listen` | RPC listen address. |
| `dir` | `--dir` | The slot dir: `slot.json`, pid and token files. `.` is the settings file's folder. |
| `token_file` | `--token-file` | Default: `rpc-<port>.token` in `dir`. |
| `no_auth` | `--no-auth` | Boolean. |
| `navmesh` | `--navmesh` | The mmaps folder. |
| `plans_dir` | `--plans-dir` | Folder `map_snapshot` reads a plan from. |
| `parents` | `--parent` | `{"36": 43}` is `--parent 36=43`; the file replaces the whole map, a `--parent` flag replaces it too. |
| `addon` | `--addon` | `"on"` or `"off"`. |
| `capture_dir` | `--capture-dir` | Packet capture parent (keep it outside the repository). |
| `disconnect_grace` | `--disconnect-grace` | Duration string, `0` to `10m`. |
| `allow_gm_units` | `--allow-gm-units` | Boolean, test bench only. |
| `password_file` | `--password-file` | A file holding the password (one trailing newline is dropped). A settings file's path must be a regular, non-symlinked `chmod 600` file of at most 4096 bytes; the `--password-file` flag itself keeps its old, looser read. |
| `password` | — | The password inline; prefer `password_file`. |

Rules:

- **Unknown keys are refused** and the error names the key, so a misspelt key cannot silently
  leave a default in place.
- The file must be a **regular, non-symlinked file only its owner can read** (`chmod 600`): it may
  name the account's password and the slot's account. The read checks the opened file against
  the one it inspected, so a file swapped in between is refused rather than run.
- A `password_file` the settings file names is held to the same rule — regular, not a symlink,
  `chmod 600`, at most 4096 bytes — because it is the slot's bearer secret; a loose mode or a
  symlink is a usage error naming the path and the problem, never the content. The
  `--password-file` **flag** keeps its historical, looser read (any file the user can read): to get
  the check, name the file in the settings file instead of on the command line.
- A **relative path is relative to the settings file's folder**, not the shell's working folder;
  a leading `~/` is the home folder. That makes `"dir": "."` and `"ca": "config/ca.pem"` sensible
  inside a slot dir.
- **Password precedence:** an explicit `--password-file` (an empty one means the environment), then
  `password_file`, then `password`, then `FOREVER_PASSWORD`.
- **The password is never printed**, on any path: not in a log line, an error, an event or a
  capture. `password` and `password_file` together are refused rather than guessed between.
- A missing file, a loose mode, malformed JSON, an unknown key and an out-of-range value are each a
  usage error (exit 2) before any login.

## The serve's lease

`slot setup --forever` records the lease it took in the slot dir's `slot.json` (`server`, `slot`,
`holder`, `lease_id`, `slots_path`). `forever serve --dir` now holds that lease for the whole run,
with the old agent's walk heartbeat rule:

- it renews the lease **at start** and then **every 5 minutes**; the lease is **15 minutes** long,
  so a live serve never loses it and a dead one frees its slot at once (the lease records the
  serve's pid; see "A dead serve frees its slot at once" below);
- a failed renewal that may pass (a lock timeout, an I/O error) is logged loudly and retried every
  minute — never silently dropped;
- an **expired lease refuses the start** with a line naming the slot and its expiry and the two
  ways out (renew it with `lease renew`, or pass `--force`). `--force` overrides only the clock: it
  never takes a live lease from another holder, and a `slot.json` whose lease id changed (the slot
  was re-acquired or released) is refused with or without it;
- a renewal that finds the lease **taken by another holder** prints
  `serve: lease <server>/<slot> is not ours any more (...): stopping the serve` and the serve stops
  cleanly with **exit code 13** — it never keeps running on another holder's slot;
- a bare `forever serve --dir` (no slot setup, no lease in its `slot.json`) holds no lease, as
  before; `teardown` still releases a setup slot's lease.

The renewal log lines are, per renewal, `serve: lease <server>/<slot> renewed until <time>`.
`teardown` remains the command that ends the game presence and frees the lease; the serve itself
does not release a lease it did not take (the lease's recorded pid makes it stale the moment the
serve is gone).

### A dead serve frees its slot at once; its `slot.json` keeps a stale lease id

Every renewal stamps the lease with the serve's process: its pid, host, pid namespace and start time
(the same record a walk's lease carries). A reader on the same host (`lease list`, `lease reap`, a
`lease acquire` or `slot setup`) sees a pid that is gone or was reused and treats the lease as stale
at once, so **a serve that dies frees its slot immediately**, not after the 15-minute expiry:
`lease reap` (and any acquire) can take the slot over the moment the process is gone. This is
intended; the expiry is the fallback for a holder on another host or one this platform cannot check.

The dead serve's `slot.json` is not rewritten, so it still names the dead serve's lease id while the
slots file's lease has been taken over (or released) by someone else. That too is intended: the
manifest is a record of the setup (server, slot, holder, lease id, dir paths), not a claim, and the
lease file is the truth. The stale manifest is harmless — a serve started from it refuses, and names
the way out:

- `lease <server>/<slot> is not this slot's any more (held by <holder> since ...)` when the slot was
  taken over by another holder;
- `lease <server>/<slot> was re-acquired (the slot.json holds another lease id)` when the same holder
  re-acquired or the lease was released and taken again;
- `lease <server>/<slot> is gone` when the lease was only released.

Run `slot setup --forever` again for that dir to record the new lease, or clear the dir and let serve
write a fresh bare manifest.

### One serve per slot dir

The dir's `slot.json`, token file, pid file and lease are one serve's, so a second `forever serve`
on the same `--dir` is refused **whatever its `--listen` host and port** would be: the serve takes
`serve.lock` in the dir for its whole run with a non-blocking exclusive lock (flock). The lock is
released by the kernel when the process ends, however it ends, so a leftover `serve.lock` never
blocks a later serve (the file is not removed; the lock, not the file, is the claim). The refusal is
`another forever serve is already running in <dir> (pid <n>): stop it or serve another --dir` with
**exit code 15**, before the lease is touched and before anything is written to the server. (A
second serve with a *different* port on an existing manifest is refused earlier, by the manifest's
port check, with the same exit code.)

The lock is `flock(2)`, so every Unix build (`serve_lock_unix.go`) has it and a build without it
(`serve_lock_other.go`, Windows) **refuses `--dir`** with a clear line naming the platform instead of
serving the dir unlocked — half a rule is not the rule. A serve without `--dir` writes no manifest,
token file or lease, so it has no dir to exclude and still runs there:
`one serve per --dir needs a file lock (flock), which this build does not provide: <dir> cannot be
served here; serve without --dir, or run forever serve on Linux or macOS` with exit 15.

The lock is a **kernel** flock, so a slot dir on a network filesystem is only as good as that
filesystem's flock: NFSv4 emulates it and a stalled server can block the lock for minutes. The serve
survives that: its keeper renews under the lease store's lock, the keeper's wait for that lock ends
with the serve's context (`slots.Store.RenewContext`, a poll of `LOCK_NB`), and the command's wait
for the keeper is bounded (`serveLeaseStopWait`, 5 s): a renewal stuck in a lock or in a write is
logged (`serve: the lease keeper did not stop within 5s; the dir stays locked until it does`) and the
command returns. Keep a slot dir on a local disk when the runs are timed; the flock rules above are
stated for a local filesystem.

### The lease keeper's lifetime

`lease.keep` returns a done channel, and the command cancels the keeper and waits for it before it
returns, so no renewal line reaches stdout after the command has returned (a test holds a renewal
inside its write and requires the command to stay blocked until the write finishes). The wait is
bounded by `serveLeaseStopWait`: a keeper that cannot leave a write is named in one line and left to
the process exit rather than holding the command forever. That line goes out on its **own goroutine**:
the output is a reason the keeper can be stuck (a full pipe), so writing it on the shutdown path could
block there again and make the bound meaningless. While the keeper is alive the dir stays **locked**:
the bounded branch hands `serve.lock` to the keeper, which releases it as it ends, so a second serve
cannot start on the dir and adopt a lease the old keeper could still stamp as its own process. (The
process exit releases the lock too; the hand-off is what keeps a serve inside a longer-lived process
honest.) The keeper's own lock wait is the run's context, so the common case (another lease command
holds the store's lock) ends the keeper at once and needs no bound.

The exit code is chosen **after** the keeper has been joined: a lease lost during the run is recorded
by then, and it outranks the code the login or the world produced (`serveVerdict`). A supervisor
therefore never reads a lost lease as a clean stop or as an auth failure.

A renewal refusal is the lease's verdict **whatever the run's context says**: it is recorded and
reported (exit 13) before the shutdown is taken into account, so a lease lost as the serve is
cancelled is not reported as a clean stop. Only the error of a lock wait that ended with the run is
read as the shutdown, and it is neither a loss nor a `renew failed` line.

### Interaction: `trials run` on a leased serve slot

`trials run` takes its own lease on the slot (holder `trials` by default,
`internal/trials/live/slot.go`) before it opens the serve dir. Now that a setup dir's serve holds
that slot's lease, `trials run` on such a dir is refused with `held by <holder>`. The two designs
had not met before because a serve held no lease. This needs a decision: either `trials run` adopts
the serve's lease (and something else has to guard against two trials on one serve), or the serve
stops renewing while a trial holds the slot (which reopens the dead-holder hole this change
closes). Until then, `trials run` works against a bare `forever serve --dir` (no lease in its
`slot.json`) or when the serve's slot is not leased.
