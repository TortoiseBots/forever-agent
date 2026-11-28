<p align="center"><img src="docs/assets/forever-agent-banner.jpg" alt="forever-agent: a headless WoW Forever client" width="100%"></p>

# forever-agent

A headless **WoW Forever** client for [ForeverCore](https://github.com/Karrz1/TrinityCore) servers. One process logs one
character into the world with no game window and exposes it over a local JSON-RPC socket, so server-side behaviour and
bot modules can be driven, recorded, and tested from scripts.

> [!WARNING]
> **Test servers only.** Use this only on servers you run or are explicitly allowed to test. The legacy Turtle 1.12
> stack remains in the tree for Turtle-only commands; every Turtle-only command refuses a Forever server.

## Quick start

Requires **Go 1.27.4+**, a **C++17 compiler** (for the cgo navmesh engine; `CGO_ENABLED=0` still builds without path
planning), and Linux for slot/lease tools.

```bash
git clone https://github.com/TortoiseBots/forever-agent
cd forever-agent
make build                       # writes bin/forever-agent
export FOREVER_PASSWORD='<password>'

# Prove login and world connectivity
bin/forever-agent forever login-check --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem --account ACCOUNT
bin/forever-agent forever world-check --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem --account ACCOUNT --character Mychar --stay 20s

# Start a session daemon and query it over JSON-RPC
bin/forever-agent forever serve --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem --account ACCOUNT --character Mychar --password-file ./password.txt --listen 127.0.0.1:8765 --dir ./slot
bin/forever-agent forever rpc ./slot status
```

## Documentation

The full user manual lives under [`docs/manual/`](docs/manual/index.md):

| Area | Topics |
|---|---|
| [Getting started](docs/manual/getting-started/index.md) | [Overview & live status](docs/manual/getting-started/what-is-forever-agent.md), [Install & build](docs/manual/getting-started/install-and-build.md), [First login](docs/manual/getting-started/first-login.md), [Settings & slots](docs/manual/getting-started/settings-and-slots.md), [Troubleshooting](docs/manual/getting-started/troubleshooting.md) |
| [The daemon](docs/manual/daemon/index.md) | [`forever serve`](docs/manual/daemon/serve.md), [RPC basics](docs/manual/daemon/rpc-basics.md), [World reads](docs/manual/daemon/status-and-world-reads.md), [Movement](docs/manual/daemon/movement.md), [Method index](docs/manual/daemon/method-index.md), [Events & logs](docs/manual/daemon/events-and-logs.md) |
| [Player actions](docs/manual/player-actions/index.md) | [Spells & combat](docs/manual/player-actions/spells-and-combat.md), [Items](docs/manual/player-actions/items-and-inventory.md), [Quests, sharing & loot](docs/manual/player-actions/quests-and-loot.md), [Vendors](docs/manual/player-actions/vendors-and-gossip.md), [`act` steps](docs/manual/player-actions/act-steps.md) |
| [Dungeons, bots & raids](docs/manual/dungeons/index.md) | [Dungeon plans](docs/manual/dungeons/plans.md), [Preflight](docs/manual/dungeons/preflight.md), [Play](docs/manual/dungeons/dungeon-play.md) / [Watch](docs/manual/dungeons/dungeon-watch.md), [Bot control](docs/manual/bots-and-raids/bot-control.md), [Raid watch](docs/manual/bots-and-raids/raid-watch.md) |
| [Testing & developers](docs/manual/developers/index.md) | [Testing overview](docs/manual/testing/overview.md), [Architecture](docs/manual/developers/architecture.md), [Contributing](docs/manual/developers/contributing.md), [Turtle 1.12 stack](docs/manual/turtle/index.md) |

## Contributing

- Open PRs against **`dev`** (`main` only takes `dev` after review).
- Run `make test`, `gofmt`, `staticcheck ./...`, and `make docs-check` before pushing (see [`DOCS-SYNC.md`](DOCS-SYNC.md)).
- File issues in the [issue tracker](https://github.com/TortoiseBots/forever-agent/issues) with the command line, output, and exit code.

## Licence

Licensed under **AGPL-3.0-or-later** ([`LICENSE`](LICENSE)). Earlier MIT-licensed versions remain under MIT
([`LICENSES/MIT-turtle-agent-before-agpl.txt`](LICENSES/MIT-turtle-agent-before-agpl.txt)). Third-party notices
(`agent-wow`, Detour/recastnavigation, `mod-dungeon-clear`) are listed in [`NOTICE`](NOTICE).

This is an unofficial community project and is not affiliated with Blizzard Entertainment.
