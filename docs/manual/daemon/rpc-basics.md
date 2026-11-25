---
type: Guide
title: RPC basics
description: Call a forever serve daemon's methods with the rpc command or any HTTP client, and understand the request envelope, errors and timeouts.
tags: [daemon, rpc, jsonrpc, client]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:07Z }
sources:
  - id: rpc-cmd
    resource: cmd/forever-agent/forever_rpc.go
  - id: rpc-server
    resource: internal/rpc/server.go
  - id: rpc-token
    resource: internal/rpc/token.go
  - id: serveclient
    resource: internal/forever/serveclient/serveclient.go
---

# What this is

A `forever serve` daemon is a local JSON-RPC 2.0 server: one HTTP endpoint, one method per call. The
`forever-agent forever rpc` command is the supported client; any program that can POST JSON over loopback can
talk to the same socket. This page covers the wire format, the error codes, how to call methods, and what
the `rpc` command prints and returns.

The method list is in the [method index](method-index.md); the daemon itself in
[forever serve](serve.md).

# The socket

| Property | Value |
|---|---|
| URL | `http://127.0.0.1:<port>/rpc` |
| Method | `POST` only; another method answers `405 POST required`, another path `404` |
| `Content-Type` | `application/json` required, else `415 application/json required` |
| `Authorization` | `Bearer <token>` unless the daemon runs with `--no-auth` |
| Body limit | 64 KiB per request |
| Requests per POST | exactly one JSON object; a batch array is `Invalid Request` |
| Host | must be loopback; a request carrying `Origin` or `Sec-Fetch-Site` is refused as a browser request |

The token is generated when the daemon starts and written to the token file (`rpc-<port>.token` in `--dir`,
or where `--token-file` says). Read it from there; do not copy it into scripts. `forever rpc` reads it for
you and never prints it, including in error text.

# Request

```json
{"jsonrpc": "2.0", "id": 1, "method": "status"}
```

```json
{"jsonrpc": "2.0", "id": 1, "method": "units", "params": {"within": 30, "hostile": true, "limit": 20}}
```

- `jsonrpc` must be `"2.0"`.
- `method` must be non-empty.
- `id` may be a string, a number or `null`; it is echoed back. With no `id` at all the call still runs and
  the reply is `204 No Content` (a notification).
- `params` is an object or absent. Every method decodes an object and rejects unknown fields; the result is
  error `-32602 invalid params: json: unknown field "..."`. The `rpc` command also accepts a JSON array, but
  no method takes one.

# Reply

```json
{"jsonrpc": "2.0", "id": 1, "result": {"state": "online", "map": 0, "x": -8949.95}}
```

```json
{"jsonrpc": "2.0", "id": 1, "error": {"code": -32003, "message": "attack: no target selected; call target first"}}
```

Exactly one of `result` and `error` is present.

# Error codes

| Code | Name | Meaning |
|---|---|---|
| `-32700` | Parse error | The body is not valid JSON (or is over the 64 KiB cap). |
| `-32600` | Invalid Request | Not a JSON-RPC 2.0 object: wrong `jsonrpc`, missing `method`, bad `id`, or a batch array. |
| `-32601` | Method not found | No method of that name is registered. `forever rpc DIR list` shows the names. |
| `-32602` | Invalid params | The params do not decode: wrong type, a missing required field, or an unknown field. |
| `-32000` | Internal | The method failed in a way it could not classify (an encoding failure, an unexpected type). |
| `-32002` | Disconnected | The session is gone (the reconnect budget was spent): nothing more goes out. |
| `-32003` | Game | The session or the server refused the call. The message says what and why. |
| `-32004` | Timeout | The call's deadline expired before the server answered. |
| `-32005` | Reconnecting | The world link dropped and is being logged in again. The call did nothing and was not queued. |
| `-32006` | Forbidden | The daemon's boundary refused the call before sending anything, for example a GM hand without `--allow-gm-units`. |

Notes:

- A session refusal and a server refusal both arrive as `-32003`; read the message.
- `-32005` is temporary: wait and call again. The Go client in the repository retries every 2 s for up to
  60 s and then surfaces the code; `forever rpc` prints it at once.
- `-32002` is final for this daemon: the process exits after `--disconnect-grace`.
- A name the old daemon had that this one has no Forever meaning for answers `-32003` with
  `<method>: <reason>`, the reason naming what to use instead; never an empty success. No method is marked
  "not ported" any more (see [methods the daemon refuses](not-ported-methods.md)).

# One call at a time

The daemon owns one session goroutine. Every call that touches the session runs there, one at a time, so a
long call (a fight, a walk, an `act` script) holds the queue behind it while it runs. Consequences:

- Other calls wait; they are not run in parallel with a long action.
- The caller's own cancellation still applies: a client that gives up drops its queued call before it runs.
- `events` and `event_log` long-polls wait outside the queue, so an empty poll never blocks a call.
- Read-only methods (`status`, `units`, `chat_log`, `spells`, `map_snapshot`, `death_status`, ...) are
  still served while the link is reconnecting; actions are not.
- A `dungeon_run` start preflight holds the command channel for each of its own reads (the module probe and each
  `.pinfo`). A `bot`, `gm`, `commander` or `los` call arriving in that window is refused at once with `-32003` `busy: a
  run preflight holds the command channel, retry`, having sent nothing, so the check still finishes and every other
  method (`status` included) keeps answering; retry the call after it. The preflight's waits are bounded: 35 s for
  the channel, and each read's own deadline (plus 2 s) counted from the moment the channel is its own.

Give every call a timeout. `forever rpc --timeout` defaults to `30s`; `attack` defaults its own run to 25 s
so an ordinary call gets an answer instead of a cancellation.

# The rpc command

```
forever-agent forever rpc [--timeout DUR] [--raw] [--leave-party] <dir> <method|list> [<params-json>|-]
```

| Flag | Default | Meaning |
|---|---|---|
| `--timeout DUR` | `30s` | Per-call timeout, a Go duration (`5s`, `1m30s`). |
| `--raw` | off | Print the result compactly instead of indented. |
| `--leave-party` | off | With `commander '{"op":"bind"}'`: leave the party first (a bind from inside a party is refused otherwise). |

`<dir>` is a serve slot directory: `slot.json` must be there and name the port; the token is read from
`<dir>/rpc-<port>.token`. The request always goes to `127.0.0.1:<port>`.

`<params-json>` is a JSON object (or array); `-` reads it from stdin (up to 1 MiB). Anything else is a usage
error. `list` takes no params: the command calls `status` first (so a stopped daemon fails here), then prints
every registered method name, one per line: an implemented method bare, a refused one as
`name (refused: <reason>)`.

Output: the result as indented JSON (`--raw`: one line). An error answer prints `error: <code> <message>` on
stderr.

| Exit code | Meaning |
|---|---|
| `0` | The call succeeded. |
| `1` | The call was cancelled (Ctrl-C) or timed out before an answer. The reply was not printed; an irreversible call may still have run, so do not blind-retry it. |
| `2` | Usage error, or the directory is not a usable slot dir. |
| `15` | Cannot reach the daemon, a bad reply, an unauthorized token, or a missing/unreadable token file. |
| `16` | The daemon answered with a JSON-RPC error (the `error:` line). |

# Examples

```sh
# what the daemon is doing
forever-agent forever rpc ./slot status

# creatures within 40 yards that are hostile to the character
forever-agent forever rpc ./slot units '{"within":40,"hostile":true}'

# a parameter stream from stdin, compact output
echo '{"guid":"0x0000000000000000000000000000ABCD"}' | \
  forever-agent forever rpc --raw ./slot unit -

# every method this daemon registers
forever-agent forever rpc ./slot list

# a longer call than the default timeout
forever-agent forever rpc --timeout 2m ./slot act '{"steps":["goto x=-8949 y=-120 z=0"]}'
```

A raw HTTP call, with the token read from the slot:

```sh
TOKEN=$(cat ./slot/rpc-8765.token)
curl -s -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -d '{"jsonrpc":"2.0","id":1,"method":"status"}' \
  http://127.0.0.1:8765/rpc
```

# Common problems

| Symptom | Cause and fix |
|---|---|
| `forever rpc: cannot reach the agent at 127.0.0.1:8765: ...` | The daemon is not running, or `--dir` points at a slot whose port belongs to another process. |
| `forever rpc: bad reply from the agent: unauthorized: token file no longer matches the agent (restarted?)` | The daemon restarted and wrote a new token. Re-read the slot; do not cache tokens. |
| `forever rpc: <dir>/slot.json missing or unreadable: rpc works on a dir made by slot setup` | `--dir` was never written by `serve` or `slot setup --forever`. |
| `error: -32602 invalid params: json: unknown field "..."` | The daemon rejects unknown fields. Check the method's parameters in the [method index](method-index.md). |
| `error: -32005 world: reconnecting: ...` | The world link is down and being logged in again. Wait and retry; the message names the attempt. |
| `error: -32002 world: offline: ...` | The reconnect budget is spent. Restart the daemon; `status` shows the last error. |
| `error: -32003 <method>: <reason>` | The name belongs to the older daemon and has no Forever meaning here; the reason says what to use instead. |

# Related

- [forever serve](serve.md): flags, slots, reconnecting, exit codes.
- [Method index](method-index.md): every method, its parameters and the page that explains it.
- [Status and world reads](status-and-world-reads.md), [Movement](movement.md),
  [Events and logs](events-and-logs.md).
- [Settings and slots](../getting-started/settings-and-slots.md) for `slot setup --forever`.
