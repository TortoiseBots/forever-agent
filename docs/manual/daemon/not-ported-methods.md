---
type: Reference
title: Methods the daemon refuses
description: The old daemon's method names a forever serve session refuses, the reason each gives, and the method or command to use instead.
tags: [daemon, methods, migration]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T23:15:27Z }
sources:
  - id: refusals
    resource: internal/forever/world/serve_rpc_methods.go
  - id: method-table
    resource: docs/forever/METHOD-TABLE.md
---

# What this is

A `forever serve` daemon registers the older session daemon's method names, so a caller written for the older
daemon gets a reason instead of "method not found". There is no "not ported" row left: every old name is either
implemented here or refused with the reason it has no Forever meaning and, where one exists, the method or command
that does the job.

A refusal is the typed `-32003` error `<method>: <reason>` — never an empty success, never `-32601` — and the
params are not decoded, so an old caller gets the reason whatever JSON it sends. `forever rpc DIR list` prints the
running daemon's table: an implemented method bare, a refused one as `name (refused: <reason>)`.

The table lives in [docs/forever/METHOD-TABLE.md](../../forever/METHOD-TABLE.md) and is generated from the daemon's
own registration, so it cannot go stale. The implemented methods are in the [method index](method-index.md).

# The refused names

| Methods | Area | Why it is refused | What to use instead |
|---|---|---|---|
| `addon_click`, `addon_start`, `addon_state`, `addon_stop`, `addon_tick` | The add-on host | Running a real add-on headless is a later ticket, so there is nothing to start, tick, click or read | `addon_send` is the ported half, the add-on message layer ([add-on protocol](../bots-and-raids/addon-protocol.md)) |
| `brain_start`, `brain_status`, `brain_stop` | The client brain | The module's bots fight on the server, so a client brain has nothing to drive | The module's own brain (`commander`, `bot`); a player character in `forever dungeon-play` is driven by the module ([bot control](../bots-and-raids/bot-control.md)) |
| `tune_list`, `tune_set` | Brain tuning | The client brain is retired on Forever, so there is nothing to tune | Nothing: read the module's state with `commander status` or `run status` |
| `dungeon_skip`, `dungeon_stage` | The older dungeon driver's step control | A module-led run has no client-side step control: the module owns its run | `dungeon_pause`, `dungeon_resume` and `dungeon_stop` are its verbs; an agent-led run's start step is `forever dungeon-run --from-leg/--legs` ([dungeon-run](../dungeons/dungeon-run.md)) |
| `lfg_auto`, `lfg_join`, `lfg_leave`, `lfg_listing_role`, `lfg_listings`, `lfg_proposal_answer`, `lfg_role_check_answer`, `lfg_status` | The client dungeon finder | The client-side LFG protocol is not built on Forever: the module's own finder fills the party and queues | `forever fb-dungeon`, and the module's `.bot lfg` and `.bot dungeon` commands ([bot control](../bots-and-raids/bot-control.md)) |
| `tbm_buttons`, `tbm_click` | The TBM buttons | The buttons are the add-on's own UI table, so they need the add-on host | `tbm_handshake` and `tbm_send` speak the protocol without one ([add-on protocol](../bots-and-raids/addon-protocol.md)) |
| `tt` | The Turtle test toolkit | It drives 1.12 test servers | `forever soap` on a Forever server ([safety and GM guards](../getting-started/safety-and-gm-guards.md)) |

The old names that used to be refused and are now implemented — `dungeon_run`, `dungeon_status`, `dungeon_stop`,
`dungeon_pause`, `dungeon_resume`, `plan_check`, `bot_view`, `tbm_handshake`, `tbm_send` and `tbm` — are rows in the
[method index](method-index.md), with their parameters.

# Why it works this way

A caller can tell "this server does not do that" from "that failed": the code is `-32003` (a game/session
refusal), and the message always starts with the method's own name. A method that is missing entirely still
answers `-32601 Method not found`, which reads like a typo.

# Related

- [Method index](method-index.md): every method, implemented or refused
- [RPC basics](rpc-basics.md): the error codes and the `list` command
- [forever serve](serve.md)
