# Death recordings as text, round DEATHRECAP: `.deathrecap` in the agent

Scope: when a watched run records a bot death, the agent asks the core for
that death's own recording as text and attaches it to the death in the watch
record and the bug hunter's ticket text. The server side is TrinityCore PR
#380 on Karrz1/TrinityCore (not merged yet): a GM command `.deathrecap [name]`
that prints the last recorded death of the named player as plain text —
a header line, one frame line per sampled frame (the victim's position and
health), then one cast line per cast before the death — at most 60 lines,
"no recording" where the core recorded none. The diff was read with
`gh pr diff 380 -R Karrz1/TrinityCore` for the exact output format; nothing
was captured from a live server.

Code:

* `internal/forever/driver/deathrecap.go` — `ParseDeathRecap` (the parser over
  the reply's Raw lines), `DeathRecapWait`/`MaxDeathRecapLines` (one request's
  deadline and the line cap), the watch's hook (`deathRecapQueue`, called from
  the event poll, only queues one request per death; `deathRecapPump` sends at
  most one per watch tick, after the event handling and the rescue decisions)
  and its memory (`deathRecapState`: state per death, never per bot; an
  unknown-command or no-permission answer stops all asking for the session
  with one log line).
* `internal/forever/driver/actions.go` — `ActionDeathRecap`, a read-class
  action (`deathrecap <bot>`) that goes through `act` and its gate like every
  other GM line; `ActionReport.Lines`/`Err` carry the reply and the port's
  refusal for the hook's unknown-command check.
* `internal/forever/driver/watch.go` + `record.go` — the hook call site (only
  for deaths naming a current party member) and `DeathRecord.Recap` (the
  parsed timeline, as text; empty when the watch never asked).
* `internal/forever/world/dungeon_session.go` — the live send: the line is
  built from the validated name here (the adapter's one command-channel send
  stays the typed client's), tokenless with the session's quiet window, the
  hook's deadline and the 60-line cap; unknown-command and no-recording come
  back as the reply's own outcome.
* `internal/forever/driver/fake.go` — the scripted answer (`DeathRecapLines`,
  `DeathRecapMissing`, `DeathRecapErr`) for the dry watch and the tests.
* `internal/forever/reportcard/card.go` — `Death.Recap` (copied from the
  record) and the card text (each death's own `deathrecap:` lines under it).
* `internal/bughunt` — `Death.Recap` and `Hit.Recap` (the nearest death's
  timeline in the member's window); the loader resolves the summary death
  log's names through the trace's Names/Roles maps to the run's own identity
  and attaches after the deaths are dated; ticket evidence prints up to 5
  lines with the `deathrecap:` prefix. `hits.jsonl` is unchanged (the Recap
  field is `json:"-"`, like `At`).
* `cmd/forever-agent/forever_deathrecap.go` — `forever deathrecap --name NAME`
  (the helper over the command channel: the GM guard, one `.deathrecap` line,
  the parsed timeline or the server's own no-recording / unknown-command
  line), registered in `forever_login.go` usage and dispatch.
* README's Bug hunter and Dungeon watcher rows name this round.

## The GM line rules the hook already had

The hook sends through `act` (the driver's one way into the setup
vocabulary) with a read-class action, so the existing gates apply: nothing
after a stop, nothing on a lost link, only for bots of this session's run
(the name is validated against the run's party and refused otherwise). The
adapter builds the `.deathrecap <name>` line from the validated name only —
no caller hands a command line in — which is what `dungeon_send_test.go`
pins (the method set gains `deathRecap`, no new `.tb` line, no new raw line).

## Bounds

State is per death (the death's index in the death log, its bot and second),
not per bot. The poll only queues; the request goes out off the poll, one per
tick, with one 10 s deadline (`DeathRecapWait`, through `act`'s own context)
and at most 60 lines parsed (`MaxDeathRecapLines`). A transient error (timeout,
garbled reply, a recap of another unit, a stale recap id) is retried up to
`DeathRecapTries` times, `DeathRecapRetryGap` times the try number apart, then
the death reads "not recorded"; a request older than `DeathRecapQueueTTL` is
given up. The core keeps only the last death of a player, so when a bot has
several deaths queued only the newest is asked and the earlier ones read "not
recorded"; the recap attaches to the death asked about and to no other, and a
reply that repeats the bot's previous recap id is not attached to a newer
death. The reply must name the asked unit in its header. When the watch ends,
the queue gets one try each (at most five). An unknown-command or
no-permission answer stops all asking; "no recording" is that death's own note.

The parser skips system lines that are not the recap's (before the header or
among its lines), reads "no recording" only as the exact first line, and splits
a cast line at the first " casts " and the last " on ". `act` returns early on
any `sendLine` reason (a link lost mid-send is never booked as performed, for
every action kind). The CLI's `--name` is normalised like the game does and the
missing command is told by the typed refusal. The bug hunter matches each
death-log recap to one dated death by member and nearest time, each used once,
and a hit takes the nearest death's recap.

## Tests

Hand-written from PR #380's format strings (in `deathrecap_test.go`, never
captures): the reference reply, the no-casts note, the exact no-recording line,
garbled and hostile replies, foreign lines among the recap, ambiguous casts and
signed offsets; in `deathrecap_state_test.go` the per-death watch cases (second
death asked again, stale recap, transient retry and its bound, no-permission
stop, link lost during the ask, a recap of another unit, a non-party unit, one
request per tick, the newest death of a bot only, queue expiry);
`TestLinkLostMidSendIsNeverSuccess` in `actions_test.go` (every action kind);
the bug hunter's nearest-death tests and the CLI name/refusal tests.

## Gates (final code commit)

On commit 9896f1be with a clean worktree, each in the foreground and waited
for:

```
make fmt-check   -> clean (no file listed)
make cross       -> windows/amd64 and darwin/arm64 builds, then the windows vet: rc=0
staticcheck ./...-> rc=0 (no finding)
vm-test ta       -> RESULT: PASS kind=ta sha=9896f1be dirty=0
```

No live run this round (the module build with `.deathrecap` is PR #380,
unmerged): the first live check is `forever deathrecap --name <bot>` against
a core with the PR, then a watched run with one bot death (one `deathrecap`
action in the order log, the timeline under the death in the card and the
ticket evidence). Public repo hygiene: no account or bot names in reports
(the record carries slots, the parser carries the core's own names only in
the timeline text), no git stash, no push, no PR. Plain commits, no trailers.
