# Forever #109 round 1: the core's new helpers

The pinned core (6517af05) had no GM combat-log watch, no current-health-only modify and no accepted-builds list; the
0.1.38/0.1.39 line (branch `forever` at `25be61f58f`) has all three. This round uses them, one helper per section.

## GM combat log watch

- Serve method `gm_combatlog {guid?, name?, mode: "unit"|"group"|"off"}`. `off` needs no unit and sends
  `.gm combatlog off`; `unit` and `group` select the unit first (the command acts on the selected unit) and send
  `.gm combatlog` / `.gm combatlog group`.
- Result `{mode, watching, text}`. `watching` is true only when the core's answer starts `Combat log watch on`;
  any other answer is an error carrying the reply. A core without the command answers "no such command", which is
  the `unsupported-core` code (`data.code`), so a caller can log once and go on.
- `forever dungeon-watch --combatlog-watch group|unit|off` (default `group`) starts the watch on the tank once the
  party is in and stops it in the cleanup phase, including the error ends. The summary's `combatlog_watch` records
  `group`, `unit`, `off` or `unsupported`.
- Bug hunt reads the field: with `off` or `unsupported` the log holds only in-range rows, so the rules that need the
  whole fight skip with `combat log in range only`. An older summary without the field keeps judging its log.
- The combat ring already keeps a row whose caster or target is not in the client's object store (the watch sees the
  party from any distance); a test now pins that (guid kept, name empty).

## Current health only

- `gm_set_hp` takes `current_only`: with it the line is `.modify currenthp <hp>` and the maximum is untouched;
  without it the line is `.modify hp <hp>` as before.
- `.modify currenthp` is a selected-unit line in the raw `gm` classifier, so the trial-units boundary holds it like
  `.modify hp`.
- Trials live client: `GMSetCurrentHP` (and the guard's `SetCurrentHP`). No trial situation made a hurt unit through
  `GMSetHP` below the unit's maximum at this revision: the two call sites (`situations.go` rescue, `teardown.go`
  heal) are refills and stay on `GMSetHP`.

## Accepted builds at login

- The realm join's `ERROR_USER_SERVER_VERSION_MISMATCH` (`0x800000DF`) is a build mismatch: in automatic mode the
  login tries the next known build (newest first), and an explicit build returns it.
- `ERROR_USER_SERVER_NOT_PERMITTED_ON_REALM` (`0x800000E1`, a closed realm or too low a security level) is
  `ErrRealmNotPermitted` and is never retried.
- Status constants are named after the core's enum (`BattlenetRpcErrorCodes.h:648,650`).
