---
type: Guide
title: Install and build
description: Build forever-agent from source, run its checks, and know what a build without cgo can still do.
tags: [getting-started, install, build, go]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:31:22Z }
sources:
  - id: gomod
    resource: go.mod
  - id: makefile
    resource: Makefile
  - id: ci
    resource: docs/CI.md
  - id: main
    resource: cmd/forever-agent/main.go
  - id: slot-linux
    resource: cmd/forever-agent/slot_linux.go
  - id: slot-other
    resource: cmd/forever-agent/slot_other.go
  - id: planner
    resource: internal/navmesh/planner.go
  - id: make-go
    resource: Makefile
---

forever-agent is built from the repository with Go. There is no installer and no release package documented
here: you clone the repository, build one binary, and run it. Most commands need nothing but the binary, a
ForeverCore server you may connect to, and the account's password.

# Requirements

| Need | Why |
|---|---|
| Go 1.27.4 or newer | `go.mod` declares `go 1.27.4` |
| A C++17 compiler (`g++` or `clang`) | the navmesh engine is cgo (Detour); every default build uses it |
| Linux | the slot tools (`lease`, `slot setup`, `teardown`, `forever rpc`'s slot dir) need `/proc` and `flock`; elsewhere they answer `needs Linux (/proc and flock)` and exit 2 |
| A ForeverCore server you may connect to | the login server (REST and RPC ports), its certificate authority file, an account, and a character |

The Makefile runs the Go toolchain from `$(HOME)/.local/go/bin/go` and sets `GOTOOLCHAIN=local`. A Go installed
elsewhere needs either a symlink there or an override, for example:

```sh
make GO=/usr/local/go/bin/go build
```

# Build

```sh
git clone https://github.com/TortoiseBots/forever-agent
cd forever-agent
make build
```

`make build` writes `bin/forever-agent`. Check it runs:

```sh
bin/forever-agent version
```

```text
forever-agent 0.1.0
```

With no arguments the binary prints its command list to stderr and exits 2, so a bare `bin/forever-agent` is not
a smoke test.

# Checks

```sh
make test
```

`make test` runs, in order:

1. `fmt-check`: `gofmt -l .` must print nothing;
2. `vet`: `go vet ./...`;
3. `nocgo`: a full `CGO_ENABLED=0 go build ./...`, which must still compile;
4. `go test -race -count=1 -timeout 20m ./...`.

The 20-minute package timeout exists because `cmd/forever-agent` runs about 1500 tests under `-race` and takes
8–10 minutes on a loaded machine. CI runs the same checks as parallel jobs and also runs `staticcheck ./...`
with the pinned release 2026.2.2, but `make test` does not run staticcheck — run it yourself when you touched
code that CI will see.

Other targets:

| Target | What it does |
|---|---|
| `make build` | `bin/forever-agent`, cgo on |
| `make nocgo` | `CGO_ENABLED=0 go build ./...` — compiles, keeps no binary |
| `make fuzz FUZZTIME=5s` | fuzz every `Fuzz*` target of the packet parsers for 5 s each |
| `make cross` | compiles for `windows/amd64` and `darwin/arm64` with cgo off, then vets the packages with process and socket tests |
| `make addon-host` | runs the add-on host end-to-end tests against the real add-on (fetches it first; needs `lua5.1`) |
| `make tbm-contract`, `make tbm-contract-forever` | replay the bot-module protocol cases through the real Lua module |

The add-on and contract targets are not part of `make test`; they skip with a message when `lua5.1` or their
fetched module is missing.

# A build without cgo

`CGO_ENABLED=0` builds and runs, but the binary has no navmesh engine. Anything that plans a route then fails
with:

```text
navmesh: this forever-agent was built without cgo, so it has no Detour engine; rebuild with CGO_ENABLED=1 and a C++ compiler
```

That covers `path_to` and `walk_path` on the session daemon, `walk-check`'s navmesh walk, `forever path-check`,
and every run command given `--navmesh`. Straight-line walking still works. Build with cgo if you intend to
walk plans or dungeons.

# Navmesh data

Path planning needs a Forever mmaps folder: the server's repacked navmesh files (four-digit map names, its own
format). Point the tool at the folder; nothing is committed to the repository because the files are derived
from client data.

```sh
bin/forever-agent forever serve ... --navmesh /path/to/forever-mmaps
bin/forever-agent forever path-check --navmesh /path/to/forever-mmaps --map 389 \
    --from 10,10,10 --to 20,20,10
```

`--parent MAP=PARENT` teaches the planner that an instance map's missing tiles come from a parent map
(`--parent 389=1` for Ragefire Chasm on map 1). The daemon takes the same flag. See
[Navmesh and maps](../dungeons/navmesh-and-maps.md).

# Installing into a slot

`slot setup` copies a binary into the slot directory; pass the one you built:

```sh
bin/forever-agent lease acquire <server> --slot 1 --holder work
bin/forever-agent slot setup --forever <server> 1 --holder work --binary bin/forever-agent
```

The slot runs that copy, so after a rebuild either copy the new binary over it while no daemon runs, or set up a
new slot (setup never overwrites an existing directory). See [Settings and slots](./settings-and-slots.md).
