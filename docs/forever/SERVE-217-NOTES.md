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
  so a live serve never loses it and a dead one frees its slot within minutes (the lease also
  records the serve's pid, so the reap frees it at once);
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

### Interaction: `trials run` on a leased serve slot

`trials run` takes its own lease on the slot (holder `trials` by default,
`internal/trials/live/slot.go`) before it opens the serve dir. Now that a setup dir's serve holds
that slot's lease, `trials run` on such a dir is refused with `held by <holder>`. The two designs
had not met before because a serve held no lease. This needs a decision: either `trials run` adopts
the serve's lease (and something else has to guard against two trials on one serve), or the serve
stops renewing while a trial holds the slot (which reopens the dead-holder hole this change
closes). Until then, `trials run` works against a bare `forever serve --dir` (no lease in its
`slot.json`) or when the serve's slot is not leased.
