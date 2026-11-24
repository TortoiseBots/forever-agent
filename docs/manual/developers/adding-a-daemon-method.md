---
type: Guide
title: Adding a daemon method
description: How to add a method to the forever serve daemon, from the registration line to the test and the JSON-RPC call that reaches it.
tags: [developers, rpc, daemon, guide]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:38:33Z }
sources:
  - id: reg
    resource: internal/forever/world/serve_session_methods.go
  - id: serve
    resource: internal/forever/world/serve.go
  - id: mount
    resource: internal/forever/world/mount.go
  - id: mounttest
    resource: internal/forever/world/mount_test.go
  - id: servefake
    resource: internal/forever/world/serve_fake_test.go
  - id: rpc
    resource: internal/rpc/server.go
  - id: rpccmd
    resource: cmd/forever-agent/forever_rpc.go
  - id: api
    resource: internal/forever/world/serve_rpc_methods.go
---

`forever serve` is the long-running session daemon. Its methods are what `forever rpc`, the
dashboard and every other local tool call. This page adds one method to it, using `dismount` as
the example: a method with no parameters, a result type and one packet sent.

The steps are short, but two rules are not negotiable: the method body runs on the session
goroutine, and the registration site is the only list of methods.

# 1. Choose the registration site

All serve methods are registered from `registerServeMethods` in
`internal/forever/world/serve_session_methods.go`. A feature with several methods puts them in its
own `register<Feature>Methods` function, which `registerServeMethods` calls; item methods are in
`registerItemMethods` (`serve_item_methods.go`), loot in `registerLootMethods`, and so on.

Pick the helper that matches the method:

| Helper | Runs on | Use it for |
|---|---|---|
| `serveCallTyped(st, fn)` | The session goroutine, queued | Anything that reads or changes session state, or sends a packet. |
| `serveReadTyped(st, fn)` | The session goroutine, even while the world link is down | Read-only answers such as `status`, `chat_log`, `follow_status`. |
| `serveCallTypedFirst(st, first, fn)` | `first` on the RPC goroutine, then `fn` queued | A method that must cancel a running loop before it can get a turn (`follow_stop`, `teardown`). |
| `rpc.Typed(fn)` | The RPC goroutine | A method that needs no session (for example one that only reads a settings value kept by `serveState`). |

Do not register a method outside this site (or the feature functions it calls).
`ServeRPCMethods()` builds the `forever rpc list` answer from this one site, so a second
registration would make the client list lie.

# 2. Write the registration and the types

`dismount` takes no parameters and returns `ServeMount`:

```go
// internal/forever/world/serve_session_methods.go, inside registerServeMethods
srv.Register("dismount", serveCallTyped(st, func(s *session, ctx context.Context, _ serveEmpty) (ServeMount, error) {
	return s.serveDismount(ctx)
}))
```

The pieces:

- The method name is the JSON-RPC name and what `forever rpc DIR dismount` uses. Lower case with
  underscores is the house style.
- Parameters are a struct with `json` tags. `rpc.Typed` decodes them with
  `DisallowUnknownFields`, so an extra field is `-32602 invalid params`, not silently ignored. No
  parameters means `serveEmpty`.
- The result is a struct with `json` tags too. Return typed values, never `map[string]any`, so the
  client and the docs have one shape to read.

A parameter struct from the same feature, for comparison:

```go
// internal/forever/world/mount.go
type serveMountParams struct {
	Spell int32 `json:"spell,omitempty"`
	Entry int32 `json:"entry,omitempty"`
}

type ServeMount struct {
	Mounted   bool  `json:"mounted"`
	DisplayID int32 `json:"display_id"`
	Spell     int32 `json:"spell,omitempty"`
	Entry     int32 `json:"entry,omitempty"`
	Attempts  int   `json:"attempts,omitempty"`
	Learned   int32 `json:"learned_spell,omitempty"`
}
```

# 3. Write the body

The body is a method on the session, in the feature's file. Keep the queue discipline: it may read
and write the model freely because it is running on the session goroutine, and it must not start a
goroutine that touches session state.

```go
// internal/forever/world/mount.go
func (s *session) serveDismount(ctx context.Context) (ServeMount, error) {
	if err := s.refuseMountOnTaxi("dismount"); err != nil {
		return ServeMount{}, err
	}
	if d := s.mountDisplay(); d == 0 {
		return ServeMount{Mounted: false}, nil
	}
	s.mountResultReset()
	if err := s.transport().Send(wire.CMSGCancelMountAura, nil); err != nil {
		return ServeMount{}, fmt.Errorf("dismount: CMSG_CANCEL_MOUNT_AURA: %w", err)
	}
	// … wait for the display field to reach zero within dismountWait …
}
```

Three habits visible there:

- State is checked before anything is sent. A character already on foot is answered without a
  packet.
- Every error names the method and the packet: `dismount: CMSG_CANCEL_MOUNT_AURA: …`.
- Waits are bounded by a named constant or a variable a test can shorten (`dismountWait`,
  `mountWait`), never by an open-ended loop.

# 4. Errors and codes

A body returns a plain Go error. `serveError` (in `serve.go`) maps it:

| Error | Code | Meaning |
|---|---|---|
| `*rpc.Error` returned by the body | as given | Use it when you need a specific code or `data`. |
| `ErrLinkLost`, `context.Canceled` | `-32002` disconnected | The link dropped under the call. |
| `context.DeadlineExceeded` | `-32004` timeout | The caller's deadline passed. |
| Anything else | `-32003` game | The server or the state refused it; the message says what. |

The rest of the code table is in `internal/rpc/server.go`: `-32700` parse, `-32600` invalid request,
`-32601` method not found, `-32602` invalid params, `-32000` internal, `-32001` not in world,
`-32005` reconnecting, `-32006` forbidden.

A method the daemon does not implement is registered from `refusedMethods` (`serve_rpc_methods.go`) with a
documented reason and answers `<name>: <reason>` with `-32003`; the generated
[docs/forever/METHOD-TABLE.md](../../forever/METHOD-TABLE.md) lists every refused name. Remove its
`refusedMethods` row and register the real body when you implement it; do not leave both registrations.

# 5. Gates

Some methods are not for every daemon:

- GM hands that act on one unit (`gm_kill`, `gm_set_hp`, `gm_aura`, `gm_revive` and the unit lines
  of `gm`) are refused with `-32006` unless the daemon was started with `--allow-gm-units`, and
  then only for guids a test registered through `trial_units`. See `ServeConfig.AllowGMUnits`.
- Path methods (`path_to`, `walk_path`, `walk_ray`) need `--navmesh`; without it they answer a typed
  not-configured error.
- Read-only methods are the only ones that answer while the daemon is reconnecting.

If your method acts on the world or on another player's unit, decide which of these applies before
you write it, and refuse rather than guess.

# 6. Test it

The serve tests run against a fake world server. Start a daemon on it, wait for `online`, call the
method through the real JSON-RPC path and assert both the answer and what the fake saw:

```go
// internal/forever/world/mount_test.go
f := newFakeServer(t)
f.serveMode = true
f.serveMounted = true
defer f.close()
steps := &stepLog{}
a := startServeDaemon(t, f, steps)
waitServeState(t, a, "online", steps)

var m ServeMount
json.Unmarshal(a.mustCall(t, "dismount", nil), &m)
if m.Mounted {
	t.Fatalf("dismount answered mounted: %+v", m)
}
```

`mustCall`, `mustFail`, `startServeDaemon` and the fake server live in
`internal/forever/world/serve_fake_test.go`. `mustFail(t, method, params, code, substring)` is the
shape for a refusal test: the code and the message are both part of the promise.

Also add the method to whatever the clients read: a new refusal or a new field usually means a new
row in the daemon's documentation page and, if a script branches on it, a line in the changelog.
`AGENTS.md` states the rule the hard way: update `README.md` for every new RPC or field.

# 7. Call it

```sh
forever-agent forever rpc ./slot-dir list
forever-agent forever rpc ./slot-dir dismount
forever-agent forever rpc ./slot-dir mount '{"spell": 133}'
```

`forever rpc` reads the token from the slot directory (and checks the manifest's paths and the
token's shape first), then prints the JSON result or the typed `error: <code> <message>` with a
nonzero exit. The same call from Go is one `serveclient` method; see
`internal/forever/serveclient`.

# Checklist

- [ ] Registration added inside `registerServeMethods` or the feature's `register…Methods`.
- [ ] Parameter and result structs with `json` tags; no parameters means `serveEmpty`.
- [ ] Body on the session goroutine; no state touched from a goroutine of its own.
- [ ] Every wait bounded; every error names the method and the packet.
- [ ] A gate (GM unit, navmesh, read-only) chosen deliberately.
- [ ] A fake-server test for the answer, and one for the refusal if it can refuse.
- [ ] The daemon documentation page and the method index updated.
- [ ] `make test` green, `make build` green.

# Related pages

- [RPC basics](../daemon/rpc-basics.md) and [Method index](../daemon/method-index.md).
- [Wire and packets](wire-and-packets.md) — the parser your method will call.
- [Adding a command](adding-a-command.md) — the CLI side of a new capability.
