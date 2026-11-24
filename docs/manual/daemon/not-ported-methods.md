---
type: Reference
title: Methods not ported
description: The older daemon's method names that a forever serve session registers but does not implement, and what to use instead.
tags: [daemon, methods, migration]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:07Z }
sources:
  - id: not-ported-list
    resource: internal/forever/world/serve_session_methods.go
  - id: method-table
    resource: internal/forever/world/serve_rpc_methods.go
---

# What this is

A `forever serve` daemon registers the older session daemon's method names even when it does not implement
them, so a caller written for the older daemon gets a reason instead of "method not found". Each of these
answers error `-32003` with `<method>: not ported on this server`; none of them is an empty success.

`forever rpc DIR list` prints them with ` (not ported)` next to the name. The implemented methods are in the
[method index](method-index.md).

# The names

| Methods | Area | What to use instead |
|---|---|---|
| `addon_click`, `addon_start`, `addon_state`, `addon_stop`, `addon_tick` | The add-on host | `addon_send` for messages; the add-on host itself is not served (see [add-on protocol](../bots-and-raids/addon-protocol.md)). |
| `bot_view` | The bot module's perception feed | Not served (the `commander` `view` op answers not ported too); `bot` and the other `commander` ops work (see [bot control](../bots-and-raids/bot-control.md)). |
| `brain_start`, `brain_status`, `brain_stop` | The bot brain | Not served. |
| `dungeon_pause`, `dungeon_resume`, `dungeon_run`, `dungeon_skip`, `dungeon_stage`, `dungeon_status`, `dungeon_stop` | The older daemon's dungeon driver | The command-line drivers: [dungeon-watch](../dungeons/dungeon-watch.md), [dungeon-play](../dungeons/dungeon-play.md), `dungeon-run`. |
| `lfg_auto`, `lfg_join`, `lfg_leave`, `lfg_listing_role`, `lfg_listings`, `lfg_proposal_answer`, `lfg_role_check_answer`, `lfg_status` | Looking-for-group | Not served. |
| `plan_check` | Plan checking | The `forever-agent plan-check` command (see [plan tools](../dungeons/plan-tools.md)). |
| `tbm`, `tbm_buttons`, `tbm_click`, `tbm_handshake`, `tbm_send` | The team-bot-module transport | `bot` for a `.tb` command line, `addon_send` for raw add-on messages (see [bot control](../bots-and-raids/bot-control.md)). |
| `tt`, `tune_list`, `tune_set` | Tuning | Not served. |

# Why it works this way

A caller can tell "this server does not do that" from "that failed": the code is `-32003` (a game/session
refusal), and the message always starts with the method's own name. A method that is missing entirely would
answer `-32601 Method not found`, which reads like a typo.

# Related

- [Method index](method-index.md): the methods the daemon does implement
- [RPC basics](rpc-basics.md): the error codes
- [forever serve](serve.md)
