# WoW Forever Bots: round A2 notes (dungeons)

Scope: the agent runs a dungeon with a WoW Forever Bots module: `.bot lfg` fills the party to five, `.bot dungeon`
queues a dungeon, entry is confirmed by **both** the module's finder state and the client's own map change,
`.bot lead on` hands the dungeon to the tank bot, the finder is polled through its add-on protocol, and the run
watches health, deaths, the tank's party lines and the module's event pushes before leading off, leaving, dismissing
and logging out.

Upstream source was read anonymously: `.bot lfg`/`.bot dungeon`/`.bot lead` handlers (`Commands/
cs_playerbot_v2.cpp`), the finder (`DungeonFinder.cpp`, `DungeonFinderPlan.h`), the fill (`LfgBotFill.cpp`), the
lead (`Bot/BotCommandParser.cpp` `ApplyLeadOrder`, `Bot/States/State_Idle.cpp`), the protocol (`Session/
AddonControl.cpp` `HandleLfgReq`), and the public conf defaults (`conf/zz-forever-lfg.conf`). Nothing was sent to
the upstream repositories.

## Commands, as the module defines them

| Feature | Text | The module's answer | Done when |
|---|---|---|---|
| Fill | `.bot lfg [tank\|healer\|dps]` (the word is the player's own role) | one system line: `<N> bot(s) are joining (<gap> missing).` with an optional report appended, or `Your group is already full: N players.`; `Only the group leader can fill the group.` on a refusal | the **party model** lists 5 members (`State.RosterValid`), with the line parsed |
| Queue | `.bot dungeon <name\|any> [role]` | `Dungeon finder: <name> (levels a-b).` plus `N bot(s) are joining ... You go in when the group is complete.` or `Your group is complete: in you go.` | the queue's line parsed; the entry's two signals |
| Cancel | `.bot dungeon cancel` | `Dungeon finder: <name> cancelled. The bots that joined stay in your group (.bot dismiss sends them off).` | the line |
| Leave | `.bot dungeon leave` | `Dungeon finder: out you go. Your bots follow.` | the line; the client's map change out is reported separately (bounded) |
| Lead | `.bot lead on\|pause\|off` | `<Tank> leads the dungeon now: ...` / `<Tank> holds here and pulls nothing until you say 'go'.` / `You lead again: your bots follow you.` | the line; the tank's name is read from the reply's prefix |

The module's refusals are matched by their stable fragments (`Dungeon finder: not now, `, `you are already in an
instance`, `only the group leader`, `no dungeon called "`, `which one?`, `does not let you in:`, `The dungeon finder
is off on this realm`, `You are not queued for a dungeon.`, `You are not in a dungeon.`, `release or be resurrected
first`, `not while you are in combat`, `no way out of this dungeon is known`, and the lead refusals). A usage line is
a refusal too: the module wrote it because the client sent a word it does not take.

## The LFG protocol

`LFG_REQ` carries no fields; the module reads the caller's group and finder state itself. The answer is
`LFG_RESP|<phase>|<dungeon name>|<seconds waited>|<form wait seconds>|<members>|<party size>|<can leave 0/1>`
(`Session/AddonControl.cpp` `HandleLfgReq`). The phases are the module's `DungeonFinderState` words: `idle`,
`forming`, `entering`, `leaving`, `inside`. `inside` comes from the server's map being a dungeon; while a finder run
is in flight (`g_runs`) that run's phase overrides it, so `entering` is seen until the server's tick has brought the
bots and erased the run.

Drift rule, the same shape-as-version rule as round A1:

- fewer than 7 fields, a phase outside the five words, a number that is not a non-negative integer, or a can-leave
  that is not `0`/`1` is `ErrProtoMismatch`; extra fields are kept in `Raw`;
- an unreadable answer fails **that request**; the protocol stays on and the run says what could not be read;
- no answer within 6 s turns the protocol off for the session (`fbcmd.ErrPBCOff`); the run says so and does not
  invent a finder state. Either way the entry is **not** confirmed.

`EVENT_PUSH` frames seen while a finder request waits are collected (bounded, 128) and drained by the watch; those
that arrived between requests are parsed out of the add-on inbox by the optional `Events()` transport method.

## The inside rule

`inside` is confirmed only by both signals:

1. the module's `LFG_RESP` phase is `inside`; and
2. the client's own map change (the merged `SMSG_NEW_WORLD` handling) put the character on a different map from the
   one it queued on, and — when the dungeon word is one of the module's default dungeons — on that dungeon's map id
   (Ragefire Chasm is 389; the table carries the module's public default activity→map entries).

Neither alone confirms: `inside` with no map change (the client is still loading, or the module is wrong), a map
change with no `inside` (the module has not caught up, or another teleport), and a map change to the wrong instance
map are all reported and never treated as entry. Both waits are bounded (form 3 min, entry 3 min, way out 30 s; the
tests shorten them through `Options`), so a transfer that never comes ends the run with a named reason instead of
hanging.

## The player's place in the lead

The module's own answer to `lead on` spells it out: the tank "pulls pack by pack and waits for mana and for you", and
its lead code holds at `kTankLeadPlayerHoldYd` (60 yards) when the player falls behind. So the module expects the
**player to follow** the tank. This client does not walk in a dungeon; it stays where it entered, says so, and the
watch reports the tank's own party lines (`waiting for mana`, `waiting for you`, said on a 45 s repeat; `State_Idle.
cpp`) together with the finder state, so a stall is visible instead of silent.

## The run

`forever fb-dungeon` (flags as fb-check plus `--dungeon NAME`, `--role`, `--watch SECONDS`, `--leave`):

1. settle, register `PBC`, probe `.bot about`, `.bot language en`;
2. `.bot lfg [role]`, then wait for the party model to list five;
3. `.bot dungeon <name> [role]`, then poll `LFG_REQ` every 2 s until both signals;
4. `.bot lead on`, print the answer and the follow note;
5. watch: finder phase on each change, the protocol's roster health for every grouped online bot (hp %, mana %,
   zone, last rule), a death when a grouped bot reads 0 % hp, each `EVENT_PUSH`, and every party line; a health line
   every 30 s and at the end;
6. `.bot lead off` (when lead on was accepted), `.bot dungeon leave` (when the character is inside; `--leave=false`
   keeps it there), wait for the client's map change out, `.bot dismiss all`, wait for the party to drop the bots,
   log out.

If the entry is not confirmed:
- the finder still `forming`/`entering`: `.bot dungeon cancel`;
- the client did change map (so it is physically inside even without the module's word): leave;
- otherwise: dismiss only.

A refusal at a step is reported and the run goes on to the cleanup it can still do. A lost link ends the run at
once, in every wait.

## Tests

`internal/forever/pbc`: `LFG_RESP` (the seven fields, a case-insensitive phase, extras kept, short answers, an
unknown phase, non-numeric and negative numbers, a can-leave outside 0/1).

`internal/forever/command/fbcmd`: each command's exact line and answer; the fill's joining/full/refusal/unknown
lines; the queue's success (with and without "in you go") and each refusal; cancel and leave; every lead answer
(on, in a dungeon, outside, pause, off, go) and refusal; the `LFG_REQ` request has no fields and expects
`LFG_RESP`; the default dungeon table's lookup (short word, full name, partial, ambiguous, unknown).

`internal/forever/world` (fake server, real sockets): the happy path (`.bot lfg` party of five, `forming` →
`entering` → a real transfer pending/suspend/new-world/resume to map 389 → `inside`, lead on, a party line and an
`EVENT_PUSH`, a roster death, lead off, a real map change out on leave, dismiss, logout); the both-signal rule in
three shapes (`inside` without a map change, a map change without `inside`, no entry); refusals at lfg, queue, lead
and leave; a six-field `LFG_RESP` (drift: protocol stays on, entry not confirmed, run cleans up); and a link death
in each wait (fill, entry, watch, way out).

## Live proof

One run of about 10 minutes against the module's server (world bots off, containers started by hand and stopped again
afterwards), from a throwaway account and an Orc warrior levelled to 13 with the core's console command
(`character level <char> 13`; the DB then read `name, level 13, race 2, class 1`). The character was created with
`forever char-create --no-appearance --race 2 --class 1`. The transcript is identifier-free: the account, the
character and the four bots' names are replaced.

```
forever fb-dungeon ... --dungeon rfc --role tank --watch 600
fb-dungeon: module: WoW Forever Bots by ...
fb-dungeon: language: Bot replies: English (your choice). ...
fb-dungeon: lfg: 4 bot(s) are joining (1 healer, 3 damage missing).
party: 2 member(s), leader <char>            (then 3, 4, 5 as the bots joined)
party:  <bot1> joined you: level 13.  (priest; then <bot2> mage, <bot3> rogue, <bot4> hunter)
fb-dungeon: party: 5 member(s): <char>, <bot1>, <bot2>, <bot3>, <bot4>
fb-dungeon: queue: Dungeon finder: Ragefire Chasm (levels 13-18). Your group is complete: in you go.
world: map change: SMSG_TRANSFER_PENDING for map 389 (from map 1)
world: map change: SMSG_NEW_WORLD map 389 at ...; the world model is cleared, ...
fb-dungeon: finder: inside (Ragefire Chasm), 5/0 member(s), waited 0s
fb-dungeon: inside confirmed: the module says inside (Ragefire Chasm) and the client's map change put it on map 389
fb-dungeon: command: .bot lead on
fb-dungeon: lead on: fbcmd: the bot module refused: Possible subcommands:
fb-dungeon: watching without the tank lead (the module refused it)
fb-dungeon: party: <bot1>: need to drink
fb-dungeon: watch: t+31s finder inside (Ragefire Chasm) 5/0;
      <bot2> 96%h 80%m [idle:classic_conjure], <bot3> 91%h 0%m [ingroup:formation_slot],
      <bot4> 89%h 100%m [ingroup:formation_slot], <bot1> 100%h 72%m [idle:classic_raid_buffs]
fb-dungeon: watch: t+1m2s ... all four at 100% hp (mana recovered) ...
... (every 31 s, the health unchanged) ...
fb-dungeon: watch: t+10m0s finder inside (Ragefire Chasm) 5/0; <bot2> 100%h 100%m, <bot3> 100%h 0%m,
      <bot4> 100%h 100%m, <bot1> 100%h 100%m
fb-dungeon: watched 10m0s: 273 sample(s), 0 death(s), 0 event(s), 1 party line(s)
fb-dungeon: leave: Dungeon finder: out you go. Your bots follow.
world: map change: SMSG_TRANSFER_PENDING for map 1 (from map 389)
fb-dungeon: the client's map change left the instance: map 1
fb-dungeon: dismiss all: Dismissed 4 companion(s).
party: group destroyed
world logout: SMSG_LOGOUT_RESPONSE (result 0) and SMSG_LOGOUT_COMPLETE
fb-dungeon: pbc: ok
```

A second, short run with `--role dps --watch 0` filled with a tank bot (`4 bot(s) are joining (1 tank, 1 healer, 2
damage missing).`), queued, entered and left; `.bot lead on` got the same usage block.

**Finding: the installed package's binary is older than its own source for the lead switch.** The client sent
`.bot lead on` exactly (the run logs each command). The running worldserver answers with the core's
`### USAGE: .bot ...` / `Possible subcommands:` block, and that list has no `lead` (it does have `lfg` and
`dungeon`). The package's `version.json` names commit `8e679df0`, whose source at
`src/modules/PlayerbotV2/Commands/cs_playerbot_v2.cpp:711` registers
`{ "lead", HandleBotLead, ... }`, and its `GroupOrderText.cpp` lists the party words `lead on|pause|off`; the
binary also lacks those strings but has `pulls the boss` and `Dungeon finder:`. The two package folders on the box
(v0.1.38 and v0.1.99) carry the **same** worldserver binary (same md5), so this is a release/source mismatch in the
package, not the client. What the bots did without the lead switch: the module suspended its dungeon autopilot for
every bot ("a human leads" in its log) and they stood at the entrance with the player; no fight started, no death,
no event push. The client's side of the lead is covered by the fake tests (on, in a dungeon, pause, off, go, and
every refusal), but the live tank lead could not be proven against this package. The run still proves the fill, the
queue, the both-signal entry (module `inside` + the client's map change to 389), the protocol watch (health from the
roster, 273 samples), the way out (map 389 to 1), the dismiss and the logout.
