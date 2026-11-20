# cmdctl round C3: the serve daemon's `commander` method

`forever serve` answers `commander`: remote control of the module's TortoiseBots party over the session's own
`.tb` command channel, through `cmdctl` (round C2) and the typed client (`tbcmd`). The old daemon's method was
never ported and was listed in `notPortedMethods`; it is a real method now, and `tbm*` are the only family of the
old control methods still answered with "not ported".

## Transport and the guard

- One runner: every line the typed client composes goes through `serveCommandOptions` (the half of
  `serveCommandLine` that keeps the caller's options), so the GM guard is confirmed once per session exactly as
  for `gm` and `bot`, and the line rides the session's serialised `command.Session`. No method here builds a
  command line; the package's one-way-out test still holds.
- One controller: `cmdctl.NewTB` over a `tbcmd.Client` whose runner reads the *current* session, so the controller
  outlives a re-entry. `install` calls `Reset()` on every session, including the first: the capability answers a
  probe cached belong to the session that ended, and the module may have been rebuilt between the two logins.
  The controller, its controller method and `install` all run on the loop goroutine, so the reset needs no lock.
- Actions are refused while the link is not online (`-32005`/`-32002`) before the controller is reached, and a
  call queued under an older epoch is refused rather than replayed: during a re-entry the fake sees no line.

## Params and ops

`{op, lead_bot, bots, verb, args, wait_ms, pause_run}`; unknown fields are refused with `-32602` like every other
serve method's params.

| op | uses | call |
|---|---|---|
| `bind` | `lead_bot` (required), `bots` | `Bind` |
| `unbind` | `lead_bot` (required) | `Unbind` |
| `status` | — | `Status` |
| `cmd` | `verb` (required), `args`, `wait_ms`, `pause_run` | `Order` |
| `run` | `verb` start\|stop\|pause\|resume\|status\|events, `lead_bot`, `args` | `Run` |
| `view` | — | always `-32003 commander view: not ported on this server` |

- `cmd` maps to `cmdctl.Order`: the verb table decides the path (`.tb order party …` today, `.tb cmd …` for the
  P3/P6 rows), the run guard reads the card first, and `wait_ms` is the per-bot poll (500 ms apart, capped at
  10 s by the controller). The other ops accept `wait_ms` and ignore it, so an old caller's JSON still works.
- `run` joins `args` into the controller's one argument: one word for `start`'s plan and `events`' sequence, the
  reason words for `stop` (the module's grammar is the check). `run status` with no `lead_bot` reads every run
  (`all`).
- A `run` verb is exempt from the run guard (it is the run's own control); `bind`, `unbind` and `status` are the
  controller's local calls.

## Result

`ServeCommander` carries `sent` (the command lines), `bots` (the module's per-bot lines: bot, status, seq, the
refusal code and message), `card` (the card the call read; `cards` and `self` for a status), `final` (each waited
bot's `ok` or `fail:<code>`), and a run verb's `runs`/`events`. A status also makes the controller read
`.tb state party`; that read is not in the answer, because the card's member records already carry each bot's
health, power, role and liveness, and a second typed copy of the state model here would be one more thing to keep
in step with `tbcmd`. Presence flags of the module's records become
absent JSON numbers (`group`, `map`, `pos`, `leg`, `ev`, `step`, `unit`, `sub`), never a zero standing in for
"not known". A refusal is the typed RPC error instead; the message names the bots the module refused.

## Errors

Every refusal is `-32003` with its machine-readable code first in the message:

| source | message start |
|---|---|
| `cmdctl.NotAvailableError` | `not-available: <verb>: needs module package P3` (or the cut/why text) |
| `cmdctl.RunActiveError` | `run-active: <lead>'s run of <plan> is running; …` |
| `cmdctl.NotOwnedError` | `not-owned: <target> is not a <kind> this character commands` (or "…the line already went through") |
| `cmdctl.RefusedError` | `refused: <verb>: nothing was queued (bot: code, …)` |
| `cmdctl.BindRefusedError` | `bind-refused: <name> cannot be bound: <why>` |
| `tbcmd.CommandError` | the module's own code, as `error code` writes it: `bad-card (commander): the text` |

`cmdctl.UsageError` is the caller's mistake and is `-32602` (`commander: …`). The link's own errors keep their
codes: `ErrLinkLost` and a cancelled context are `-32002`, the reconnecting refusal is `-32005`, a deadline is
`-32004`.

## Tests

`serve_fake_test.go`, against the fake world over real sockets (the fake answers `.tb` lines from a per-test
script, with the run's token): a status round trip (two members); `cmd stay` (the exact `.tb order party stay`
line, one queued bot, the guard's card); `view` and bad params refused with nothing sent; `commander` in the
ported list and out of the not-ported one; a call during a re-entry answers `-32005` and the fake sees no line; a
module error line keeps its code first; an order into a running run is refused `run-active` and `pause_run` sends
it; a P6 verb probes once and answers `not-available` with no order; `run status` (no lead) and `run events` carry
`runs` and `events`.
