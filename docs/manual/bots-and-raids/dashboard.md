---
type: Guide
title: Watching bots on a dashboard
description: Read a server's bot roster dashboard, use the agent's live page with its Positions panel, and see why replaying a recording is Turtle-only.
tags: [bots-and-raids, dashboard, roster, replay]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T23:40:00Z }
sources:
  - id: dash-bots
    resource: cmd/forever-agent/forever_dash_bots.go
  - id: roster
    resource: internal/roster/client.go
  - id: roster-watch
    resource: internal/roster/watch.go
  - id: dashboard-cmd
    resource: cmd/forever-agent/dashboard_cmd.go
  - id: forever-control
    resource: internal/dashboard/forever_control.go
  - id: controls-js
    resource: internal/dashboard/static/controls.js
  - id: dash-replay
    resource: cmd/forever-agent/dashreplay_cmd.go
  - id: dashreplay
    resource: internal/dashreplay/send.go
---

Two commands in the Forever stack watch bots on a screen. `forever dash-bots` reads the roster
dashboard a server's bot manager already serves. `forever dashboard` serves the
agent's own live page, which draws the bots of each daemon and — with run controls
on — lets you order and place them. A third command, `dashboard-replay`, is
Turtle-only and is not part of the Forever workflow (see the end of this page).

# dash-bots: the server's roster dashboard

```
forever-agent forever dash-bots --url URL --user NAME [--pass-file FILE]
    [--dash-insecure-http] [--watch [--interval 2s] [--record FILE]]
    [--stuck-after 20s] [--lone-combat-after 5s] [--group-distance 60]
    [--timeout 15s]
```

It logs in as a GM game account and reads the roster the dashboard lists: every
logged-in bot with its class, level, kind and state, health, power, map, zone,
instance, position, target and group, plus the dashboard's heartbeat when it sends
one. The client and the watch are tested against the dashboard's own answer shape;
no live dashboard has been read with them yet, so the first live run is what
proves the login and the record shape against your server's build.

| Flag | Default | Meaning |
|---|---|---|
| `--url` | required | The dashboard's root URL. |
| `--user` | required | The GM game account the dashboard logs in as. |
| `--pass-file` | empty | A private file (mode 0600) holding the password. Without it the password comes from `FOREVER_DASH_PASSWORD`. Never a flag. |
| `--dash-insecure-http` | off | Allow a plain-http dashboard that is not loopback. The password and the session cookie then travel in clear text, and the command prints a warning. |
| `--watch` | off | Poll and report changes instead of printing the roster once. |
| `--interval` | `2s` | With `--watch`: how often the roster is read (100ms-5m). |
| `--stuck-after` | `20s` | A bot whose position stays put this long while its state says moving is reported stuck. |
| `--lone-combat-after` | `5s` | The grace a bot in combat gets for a group mate to join before it is reported fighting alone. |
| `--group-distance` | `60` | How many yards from its group's centre a bot may be before it is reported. |
| `--record` | empty | With `--watch`: append one JSON line per good read (the read's time and the roster as decoded) to this file. |
| `--timeout` | `15s` | The bound on one dashboard request, the login included (1s-2m). |

The URL must be https, or plain http to a loopback address or the exact name
`localhost`: the login carries a GM password and a session cookie, and a hostname
must not be able to move that traffic somewhere else between the check and the
login. Any other plain-http host needs `--dash-insecure-http`.

Without `--watch` it prints the roster and exits. With `--watch` the first read is
the baseline and later reads print only the changes:

| Event | When |
|---|---|
| `death` | A bot's state became dead. |
| `stuck` | A bot that says moving has not moved for `--stuck-after`, and no move of at least half a yard happened. |
| `lone-combat` | A bot has been in combat for `--lone-combat-after` with no group mate of its group in combat. |
| `far-from-group` | A bot is more than `--group-distance` yards from its group's centre. It must come back under 80% of the distance before it can fire again. |

Each condition is reported once and fires again only after it cleared. A dashboard
record that omits or zeroes a field the rules read (`hp`, `max_hp`, `map`,
`instance`, `x`, `y`, `z`, `state`) is left out of the roster and reported on
stderr, never read as a death or a distance. Five failed reads in a row end a
watch.

Exit codes: 0 printed or watched, 1 a login or read failure, 2 usage.

```
roster: 12 bot(s), 2 human(s); states idle 8, moving 3, combat 1
  Tankone  Warrior 60  hired moving  hp 4820/4820  rage 12/100  map 0 Elwynn Forest  (12.1, -35.4, 4.2)  target -  group Tankone
  09:41:12  stuck  Dpsone  no movement for 22s at 12.3, -30.1, 4.0  (map 0 Elwynn Forest)
```

# dashboard: the agent's own live page

```
forever-agent dashboard [--listen 127.0.0.1:8790] [--allow-host HOST[,HOST]]
    [--agents auto|<rpc,...>] [--tests-dir DIR] [--traces-dir DIR]
    [--plans-dir DIR[,DIR]] [--minimap-dir DIR] [--config-dir DIR]
    [--mmaps-dir DIR [--mmaps-format turtle|forever]] [--control [--control-tokens FILE]
    [--control-log FILE] [--control-trust-local [--control-trust-label LABEL]]]
    [--edit ...]
```

It serves a live page of the daemons it can find. `--agents auto` reads the `rpc`
field of the settings files in `--config-dir`; a comma list names ports or
addresses. Each daemon becomes a card: a Forever slot's card is built from the
daemon's status, party, commander card and the units within 100 yards, so it shows
the character, its party, the bots it commands, the mobs around them and the last
event rows. The page is read-only unless `--control` (run controls) or `--edit`
(the route editor, described in [route tools](../dungeons/route-tools.md)) is on.

`--control` adds the run actions: pause, resume, skip and stop a run, a set of
order buttons and the Positions panel. Its rules:

- A control token is required unless `--control-trust-local` lets a save from this
  machine (or from a Tailscale address in `--allow-host` or the `--listen` address)
  use the server's own token.
- The control token file must be its own file, never the route editor's: an
  editing token must not press run buttons.
- The listen address for `--control` must be loopback or a literal Tailscale
  address; a token must not cross a LAN in plain HTTP.
- Every control appends a request line and a result line to `--control-log`
  (default `~/.local/state/forever-agent/dashboard-control.jsonl`, mode 0600). The
  token never reaches the log.

## Order buttons

On an online Forever card the page shows six order buttons — Stay, Follow, Come,
Attack, Pull, Release — and each one is one call to the daemon's `commander`
method with `cmd` and that verb. The call is logged, sent once, and its answer
shown as the reply; a refusal is shown as an error, never as success.

## The Positions panel

The panel holds the party in a formation and manages a fall-back point. Every
field is a fixed choice, so a bad body is refused before anything is sent:

| Pick | Values |
|---|---|
| Shape | `close`, `circle`, `arc`, `spread`, `line`, `column`, `spear`, `arrow` |
| Gap | `tight`, `normal`, `wide`, or a number of yards from 1.5 to 12 |
| Anchor | `here`, `target`, `tank`, or a `point` you click on the map |
| Facing | a number of degrees 0-359, or automatic |
| Who | `all`, `tanks`, `healers`, `melee`, `ranged`, `dps`, a class name, `group1` to `group8`, or any member's name on the card |

The buttons place the party (or release the hold), remember a rally point here or
at a picked map point, clear it, fall the party back on it, and resume following.
A picked point is kept with the map it was picked on, so a point clicked before a
zone change is never sent. A point outside the world's coordinate bound (±17066),
on another map than the card's, or more than 100 yards from the commander (the
module's typed-point reach) is refused. `Place` needs the commander card, which is
also what the panel's Who list checks names against: a card that cannot be read
means nothing is sent. The Who list takes the fixed words above, any member's name
on the card, a class name, or `group1` to `group8`.

The panel sends to `POST /api/control/<id>/place` under the same token and nonce
gate as the run buttons; the request body's verbs are `place`, `place_follow`,
`rally`, `fallback` and `resume`.

# Replaying a recording is Turtle-only

`forever-agent dashboard-replay` feeds a Turtle dashboard recording to a TortoiseBots observability daemon's UDP
port. `forever-agent help` marks it `[Turtle only; Forever: none: Turtle dashboard recordings]`, and the Forever
stack has no replacement: it reads a `replay.jsonl` of datagrams (or converts an API dump folder to one), not a
Forever roster watch. The Forever equivalent for watching a roster is `forever dash-bots --watch --record`, which
writes a roster watch to read, not a replayable recording.

The command names no server, account or slot and logs in to nothing, so it hits no Forever refusal; it is simply
outside the Forever workflow. Do not use it against the agent's own dashboard from this page.

# Examples

```sh
# One look at a roster dashboard behind https.
forever-agent forever dash-bots --url https://example.invalid --user ACCOUNT \
  --pass-file ./dash-password

# Watch the roster for deaths and stuck bots, keeping the history.
forever-agent forever dash-bots --url https://example.invalid --user ACCOUNT \
  --pass-file ./dash-password --watch --record ./roster-watch.jsonl

# The agent's page with controls on, loopback only.
forever-agent dashboard --listen 127.0.0.1:8790 --control \
  --control-tokens ~/.config/forever-agent/dashboard-control-tokens \
  --mmaps-dir ./mmaps --mmaps-format forever
```

# Common problems

| Symptom | What to do |
|---|---|
| `plain http is allowed only to a loopback IP literal or localhost` | Use https, or pass `--dash-insecure-http` knowingly. |
| `dash-bots: no password: --pass-file or FOREVER_DASH_PASSWORD is required` | Set one; the password is never a flag. |
| The roster is missing a bot | A record without the fields the rules read is skipped and reported on stderr. |
| `--record needs --watch` | Recording follows a watch; add `--watch`. |
| A control button answers "Wrong or missing control token" | Type the token from `--control-tokens`, or start the page with `--control-trust-local` for this machine. |
| `the commander card could not be read, so nothing was sent` | The Positions panel needs the daemon's commander card; check the daemon's link and its bindings. |
| `dashboard: --control refuses the listen host` | Listen on loopback or a literal Tailscale address. |
