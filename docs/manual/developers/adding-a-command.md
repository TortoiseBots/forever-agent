---
type: Guide
title: Adding a command
description: How to add a top-level or subcommand to the forever-agent CLI, with the help line, the scope, the exit codes and the tests it must pass.
tags: [developers, cli, commands, guide]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:38:33Z }
sources:
  - id: main
    resource: cmd/forever-agent/main.go
  - id: scope
    resource: cmd/forever-agent/command_scope.go
  - id: scopetest
    resource: cmd/forever-agent/command_scope_test.go
  - id: dispatchtest
    resource: cmd/forever-agent/main_dispatch_test.go
  - id: pathcheck
    resource: cmd/forever-agent/path_check_cmd.go
  - id: pathchecktest
    resource: cmd/forever-agent/path_check_cmd_test.go
  - id: fortests
    resource: cmd/forever-agent/forever_login.go
---

Every command is a function in `cmd/forever-agent` that takes the arguments after its word, an
`io.Writer` for stdout and one for stderr, and returns a process exit code. `main` calls `run`,
`run` dispatches on `args[0]`, and the command owns its flags.

This page adds one: `path-check` is the worked example because it is short and shows every
pattern (required flags, positionals, a `--json` mode, two exit codes). A subcommand (`forever …`,
`slot …`, `scenario …`) is the same thing one level down.

# 1. The dispatcher case

Add the word to the `switch args[0]` in `run` (`cmd/forever-agent/main.go`):

```go
case "path-check":
	return pathCheckCmd(args[1:], stdout, stderr)
```

Commands that need a context (they can be interrupted or start a session) take `ctx` first:
`return foreverServeCmd(ctx, args[1:], stdout, stderr)`.

# 2. The help line

Add a line to `usageText` in `main.go`, beginning with two spaces and `forever-agent <word>`.
`TestHelpMarksEveryTurtleOnlyCommand` requires every command that has a scope to have at least one
such line, so this is mandatory, not a nicety:

```
  forever-agent path-check --mmaps DIR --map N [--json] -- x y z x y z
```

The line carries the whole synopsis: every flag with its default in square brackets, positionals,
and the alternatives for mutually exclusive flags. `usage()` runs each line through `markScope`,
which appends the Turtle-only mark from `commandScopes`; you do not write the mark yourself.

# 3. The scope

Add the word to `commandScopes` in `command_scope.go`, one entry per command:

| Case | Entry |
|---|---|
| Works on both stacks (the default) | `"path-check": {},` |
| Served by the Turtle stack alone | `"login": {Turtle: true, Forever: "forever serve"},` |
| Serves both stacks and has a `--forever` mode | `"slot": {}` (no mark; the command checks the server itself) |

Two tests hold this map to the switch: `TestEveryCommandHasAScope` fails when a case has no scope
or a scope has no case, so a command added without an entry cannot merge.

# 4. The command function

Put it in its own file, `<name>_cmd.go`, next to `<name>_cmd_test.go`. The shape:

```go
const pathCheckUsage = "usage: forever-agent path-check --mmaps DIR --map N [--json] -- x y z x y z"

// exitCheckFailed is the exit code when a query itself fails (no tiles, no engine):
// usage errors stay exitUsage (2).
const exitCheckFailed = 1

func pathCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("path-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	mapID := fs.String("map", "", "map id (numeric); required")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if len(fs.Args()) != pathCheckCoords || *mmaps == "" || *mapID == "" {
		fmt.Fprintln(stderr, pathCheckUsage)
		return exitUsage
	}
	// … parse the numbers, run the query, print …
}
```

The rules that come out of the existing commands:

- `flag.NewFlagSet(name, flag.ContinueOnError)` and `fs.SetOutput(stderr)`. Never `flag.Parse`.
- Every flag has a real default and a short help string. The help string is what `-h` prints.
- Required flags are plain strings defaulting to `""` and checked after `Parse`, so the command
  prints its own usage and returns `exitUsage`.
- A repeatable flag uses a small `flag.Value` (`stringList`, defined in `raid_walk_cmd.go`;
  `scrubList` in `forever_scrub.go`). A `K=V` map uses a `flag.Value` too (`mapPairs` in
  `forever_serve.go`).
- Negative positionals need `--` before them: without it the flag package treats `-1` as a flag.
  The path-check test covers exactly that case.
- Results go to stdout; errors, usage and progress go to stderr.
- `--json` prints one JSON object (or array) and nothing else on stdout, so a script can pipe it.
  A `--out DIR` writes files and prints a one-line summary with the path.

# 5. Exit codes

The codes in `main.go` are the contract with scripts:

| Code | Constant | Meaning |
|---|---|---|
| 0 | `exitOK` | Success. |
| 2 | `exitUsage` | Bad flags, arguments or settings file. |
| 3 | `exitAuth` | The login server rejected the login. |
| 4 | `exitRealm` | Realm missing or offline. |
| 5 | `exitWorld` | The world server rejected the session or the character. |
| 6 | `exitDisconnected` | The server closed the connection. |
| 7 | `exitScrub` | `scrub-check` found a secret in a capture. |
| 8 | `exitLogoutRefused` | The logout was still refused (in combat) when the retries ran out. |
| 9 | `exitFleet` | A fleet child failed, or the fleet never got into world. |
| 10 | `exitLogoutIncomplete` | The logout ended without an answer; the character may stay in world. |
| 11 | `exitDead` | The character died during the run. |
| 12 | `exitAmbiguous` | Several candidates and none named; nothing was sent. |
| 13 | `exitPreflight` | A preflight check failed; the run did not start. |

A command that needs its own code defines a constant in its file (`exitCheckFailed` 1 in
path-check, `exitSlotRefused` 15 in the lease and slot commands, `exitReplayRefused` 17 in
`dashboard-replay`). One overlap exists today: 12 is also `exitRouteWalk`, a route that walked but
found a leg that did not pass. Do not add another meaning to a code that already has one.

# 6. Turtle-only commands

A command that only the 1.12 stack serves must refuse a Forever server before it takes a lease,
logs in, writes a file or starts a child process:

```go
if refused, code := refuseTurtleOnly("raid-walk", server, slotsPath, scopeForever("raid-walk"), stderr); refused {
	return code
}
```

`refuseTurtleOnlyIn` does the same for a filter that names several servers. A command that has a
Forever mode of its own under a `--forever` flag uses `refusePassForever` instead, whose message
is `pass --forever`. The replacement text comes from `scopeForever(cmd)`, the same map the help
uses, so the mark and the refusal cannot disagree.

# 7. Tests

Three tests are about the CLI as a whole:

- `TestEveryCommandHasAScope` — the switch and `commandScopes` agree (both directions).
- `TestHelpMarksEveryTurtleOnlyCommand` — every scoped command has a usage line, and the marks are
  right.
- `TestRunDispatchesEveryUsageCommand` — runs the dispatcher with failing arguments for each
  command in its map and asserts that none falls through to the global usage. It does not discover
  commands, so add the new word there with arguments that fail.

Then the command's own test file. For path-check, the usage test is a table: every bad flag,
count and non-number must return `exitUsage` with nothing on stdout. A query error is a separate
case with its own code. The same two-part shape suits any command: one table for the usage, one or
more cases for each real failure mode. When the command talks to a server, use the existing fake
server or fake session instead of a live one.

# 8. Finish

- Update the manual page for the feature the command belongs to, and the changelog.
- Run the build and the suite: `make build` and `make test`.
- `AGENTS.md` adds the rule for anything user-visible: update the manual for every new RPC or
  field (and `README.md` where it still carries the same text). A command is user-visible.

# Related pages

- [Adding a daemon method](adding-a-daemon-method.md) — the daemon side of a new capability.
- [Contributing](contributing.md) — formatting, static analysis and the pull-request rules.
- [RPC basics](../daemon/rpc-basics.md) — what `rpc` commands talk to.
