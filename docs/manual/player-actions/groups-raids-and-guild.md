---
type: Method Reference
title: Groups, raids and guilds
description: Invite and leave a group, read the party roster, lead a party (promote, ready check, loot method, marks, disband), convert it to a raid, invite a character to a guild, offer a charter, handle guild invites, and check the group code live.
tags: [player-actions, party, raid, guild, group-control]
status: stable
generated: { by: forever-agent docs, at: 2026-11-27T00:00:00Z }
sources:
  - id: serve-session
    resource: internal/forever/world/serve_session.go
  - id: serve-session-methods
    resource: internal/forever/world/serve_session_methods.go
  - id: groupctl
    resource: internal/forever/world/groupctl.go
  - id: raidctl
    resource: internal/forever/world/raidctl.go
  - id: readycheck
    resource: internal/forever/world/readycheck.go
  - id: guild-session
    resource: internal/forever/world/guild_session.go
  - id: guild-offer
    resource: internal/forever/world/guild_offer.go
  - id: guild-packets
    resource: internal/forever/guild/guild.go
  - id: party-check
    resource: cmd/forever-agent/forever_chat.go
---

These methods cover the group a character is in (a party, or a raid converted from it) and the guild: inviting,
joining, leaving, reading the roster, the leader's controls, raid subgroups, and guild invites.

All of them answer once the state they change has been read back from the server, not when the packet went out. A
refused group command in this server is silent, so a command whose readback never comes is an error that says the
readback did not come; it does not prove the command was refused.

# Membership

| Method | Parameters | Returns |
|---|---|---|
| `invite` | `name` | empty object |
| `accept_invite` | none | `{inviter, inviter_guid}` |
| `leave_group` | none | empty object |
| `party` | none | the roster |

`invite` invites one player by name and answers the server's result for that name (an accepted invite, or the
reason it was refused). `accept_invite` answers the held invite and waits for the roster that names the inviter; with
no pending invite it is `no pending group invite`. `leave_group` needs a valid roster and waits for the party to end.

`party` answers:

| Field | Meaning |
|---|---|
| `in_group` | In a group. |
| `raid` | The group is a raid. |
| `assistant`, `subgroup` | This character's raid assistant flag and subgroup (1-8; 0 in a party). |
| `leader` | The leader's GUID. |
| `members` | `[{name, guid, online, dead, leader, assistant, subgroup, health, max_health, power_type, power, max_power, level, zone}]`. |
| `pending_invite` | The name of the player who invited this character, while the invite is open. |
| `loot_method`, `loot_threshold`, `loot_master` | The group's loot settings, from the last roster that carried them. |
| `marks` | The icons the group holds: `[{icon, guid}]`. |

# Party leader controls

| Method | Parameters | Meaning |
|---|---|---|
| `promote` | `name` | Make a member the leader. |
| `assistant` | `name`, `clear` | Set (or with `clear: true` take away) a member's assistant flag. The flag is a raid rank, so this call takes a raid only. |
| `loot_method` | `method`, `master`, `threshold` | Set the loot method. |
| `mark` | `guid` or `name`, `icon` | Put a raid target icon on a unit. |
| `marks` | none | Read the icons back. |
| `ready_check` | none | Start a ready check as the leader. |
| `disband` | none | End the party. |

Only the leader may `promote`, `assistant`, `loot_method`, `ready_check` and `disband`; in a party any member may
`mark`. `promote`, `loot_method` and `disband` are refused in a raid (`raid group control is not ported yet`),
because the raid ranks differ and the raid operations come with the raid runs. `assistant` is the first exception:
the flag is a raid rank the core sets only in a raid and only from the leader, so `assistant` takes a raid and
refuses a party with
`assistant: needs a raid: convert_raid first` (a non-leader reads `assistant: only the leader can (the leader is
NAME)`). `ready_check` is the second: it takes a party or a raid, as long as this character leads it.

`loot_method.method` is one of `free-for-all`, `round-robin`, `master-loot`, `group-loot`, `need-before-greed`,
`personal-loot` (or its number 0-5; the short forms `ffa`, `round`, `master`, `group`, `need`, `personal` are
accepted too). `master` names the master looter for master loot. `threshold` is the item quality 2-6
(uncommon-artifact), default 2.

`mark.icon` is one of `star`, `circle`, `diamond`, `triangle`, `moon`, `square`, `cross`, `skull`. `mark` names the
unit by `guid` or by a roster member's `name` and sets that icon on it; the method has no way to clear an icon.
`marks` answers every icon the group holds, in slot order.

`disband` uninvites every other member in roster order; the last uninvite leaves this character alone, which is the
group's end.

The older daemon's group-finder methods (`lfg_join`, `lfg_leave`, `lfg_listings`, `lfg_status`, `lfg_auto`, the
proposal answers and `lfg_listing_role`) are refused by `forever serve` with a `-32003` error: the client dungeon
finder is not built on Forever, and the module's own finder fills the party and queues (`forever fb-dungeon`, and
the module's `.bot lfg` and `.bot dungeon` commands).

# Ready check

`ready_check` sends the client's ready check start (`CMSG_DO_READY_CHECK`, the core's `0x43005E`, wire `0x44005E`)
as the playing character and answers once the core's broadcast start arrived. The character must lead the group; a
raid is allowed. (The core also takes the start from an assistant, but this method sends only as the leader.)
Outside a group, without a valid roster, or as a non-leader the call is refused before anything is sent
(`ready_check: not in a group`, `ready_check: no valid party roster (...)`, `ready_check: only the leader can (the
leader is NAME)`). A start the core refuses (for example another check already runs) gets no answer, so the call
fails with `ready_check: the server did not confirm it within 5s (...)`.

The members' answers and the finish are packets the core broadcasts; they arrive as `party` events in the session's
event stream (an offline member's answer, or a one-member check's finish, can arrive before the call returns):

| `data.state` | Fields | Meaning |
|---|---|---|
| `ready_check_started` | `index`, `party_guid`, `duration_ms` | The broadcast start; `source_guid` is the member who started it. |
| `ready_check_response` | `ready`, `party_guid` | One member's answer; `source_guid` is that member. |
| `ready_check_completed` | `index`, `party_guid`, `ready`, `not_ready` | The finish, with the answers seen so far; the two counts are absent when the finish arrived before its start. |

The core ends a check itself once every member has answered or its 35 s timer runs out. It broadcasts the offline
members' answers (and a one-member check's finish) before the `ready_check_started` row, so the rows keep that order;
the session applies those early answers and that finish to the check this client asked for, and to no later one. A
finish the character saw for a check it never started (it joined or reconnected during the check) stays an event: it
never ends the next check. Checks are keyed by party, so a home check and an instance check reported to the same
member at once never replace each other. The core marks the member who starts the check ready at the start and
broadcasts no answer row for them, so a two-member check with one answer reads one answer.

Read the rows with the `events` method (`forever rpc DIR events '{"since": 0}'`) or the event stream on
[Events and logs](../daemon/events-and-logs.md).

# Raid control

| Method | Parameters | Meaning |
|---|---|---|
| `convert_raid` | none | Turn the leader's party into a raid. |
| `raid_move` | `guid` or `name`, `subgroup`, `swap_with` | Move a member to a raid subgroup 1-8. |

`convert_raid` needs the leader and a group of two or more members. `raid_move` needs a raid and the leader or an
assistant. A subgroup holds five members: moving into a full one is refused, and `swap_with` names the member of the
target subgroup who trades places instead. A swap needs a member of the target subgroup who is in another subgroup
than the one being moved.

# Guild

| Method | Parameters | Meaning |
|---|---|---|
| `accept_guild_invite` | none | Accept the held guild invite and wait for the join. |
| `decline_guild_invite` | none | Decline the held invite (nothing to wait for). |
| `guild_leave` | none | Leave the guild and wait for the guild field to clear. |
| `guild_invite` | `name` | Invite a character to this character's guild and wait for the command result. |
| `offer_charter` | `guid` or `name`, `item` | Offer a guild charter to a player for signing. |

An invite is held from the guild-invite message; the accept consumes it, so a second accept has nothing to take and
a decline after an accept has nothing to decline. `guild_leave` on a character that is not in a guild is refused
with `guild_leave: not in a guild`.

## Invite a character and offer a charter

`guild_invite` sends the client's guild invite (`CMSG_GUILD_INVITE_BY_NAME`, the core's `0x430034`, wire `0x440034`)
as the playing character and answers the core's guild command result for `GUILD_COMMAND_INVITE_PLAYER` and that name:
`ERR_GUILD_COMMAND_SUCCESS` (`0`) is the invite; another result is an error naming the number (`guild_invite: the
server refused it: result 11 for command 1`). The character must be in a guild (`guild_invite: not in a guild`). The
command result is also a `guild` `result` event.

`offer_charter` sends `CMSG_OFFER_PETITION` (the core's `0x3D0289`, wire `0x3E028C`) with the charter item's packed
GUID and the target player's packed GUID. `item` names a charter in the inventory; without it the call uses the only
item of entry 5863 (the guild charter) and refuses zero or several. The target is a GUID or a name in view and must
be a player who is not in a guild (`offer_charter: ... is not a player`, `... is already in a guild`). The core sends
no success packet for an offer; the refusals it answers arrive as the same `guild` `result` event the guild window reads.

The answers reach the event stream as `guild` rows:

| `text` / `data.state` | Fields | Meaning |
|---|---|---|
| `joined` | `state` (`accepted`), `guild_name` | A member joined: the accepted invite. The row already existed for every join. |
| `declined` | none | `SMSG_GUILD_INVITE_DECLINED`: a server that announces a declined invite. This core's guild handler clears the invite silently and never sends this packet. |
| `expired` | none | `SMSG_GUILD_INVITE_EXPIRED`, likewise. |
| `signed` | `item`, `error` (`0`) | `SMSG_PETITION_SIGN_RESULTS`: the named player signed the charter. |
| `sign_refused` | `item`, `error` (nonzero) | The same packet with a PetitionSigns failure (`1` already signed, `2` already in a guild, ...). |

The signature result goes to the signer and to the charter's owner, so the offering character sees `signed` with the
signer in `source` and `source_guid`. Read the rows with the `events` method (`forever rpc DIR events '{"since": 0}'`),
or the event stream on [Events and logs](../daemon/events-and-logs.md).

# Check a party live: forever party-check

One-shot live check of the group code, with no daemon: it logs in like `forever world-check`, enters the world, does
what you asked (invite, accept or decline an invite, uninvite, promote, set the loot method, mark a creature), prints
what came back and logs out.

```bash
forever-agent forever party-check [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME [--stay 5s] [--invite NAME | --accept-from NAME | --decline-from NAME] \
    [--uninvite NAME] [--promote NAME [--promote-back DURATION]] [--ready-check [--ready-wait DURATION]] \
    [--loot METHOD[:THRESHOLD]] [--mark ICON] [--wait-join DURATION] [--settle 4s] [--capture-dir DIR]
```

| Flag | Default | Meaning |
|---|---|---|
| `--settle` | `4s` | How long to settle in the world before acting, 0-1m. |
| `--stay` | `5s` | How long to stay in the group before leaving, 0-2m. |
| `--invite NAME` | none | Invite this character name. |
| `--accept-from NAME`, `--decline-from NAME` | none | Wait for an invite from this name and accept or decline it. Choose one side: `--invite` and these are exclusive. |
| `--uninvite NAME` | none | After the group formed, uninvite this member, `--invite` only (only the inviter can). |
| `--promote NAME` | none | Promote this member to leader and wait for the roster that shows it. |
| `--promote-back DURATION` | `0` | With `--promote`: then wait up to this long for the new leader to promote this character back (an agent session on the other side); at most 2m. |
| `--ready-check` | off | Start a ready check as the group leader and log every member's answer; the same packets the `ready_check` method sends. |
| `--ready-wait DURATION` | `40s` | With `--ready-check`: wait up to this long for the members' answers and the finish of the check it started (the core ends the check at 35 s; `0` returns after the start); an earlier or a later check's finish never ends the wait. At most 2m. |
| `--loot METHOD[:THRESHOLD]` | none | Set the loot method and read the roster back: `free-for-all`, `round-robin`, `master-loot`, `group-loot`, `need-before-greed`, `personal-loot`, or `0..5`; threshold 2-6, default 2. |
| `--mark ICON` | none | Set this raid target icon on the nearest attackable creature and read the icon list back: `star`, `circle`, `diamond`, `triangle`, `moon`, `square`, `cross`, `skull`. |
| `--wait-join DURATION` | `0` | With `--invite`: wait up to this long for the group to form (stats, party line, stay); at most 2m. |
| `--capture-dir DIR` | none | Write every packet the combat, chat and party handlers see into a new run directory under this path; keep it outside the repository. |

The summary is the roster the session saw, in the same fields as the `party` method, plus the last ready check:

```text
party-check: in a group, 2 member(s), leader Mychar, loot free-for-all
party-check: ready check finished, 1 answer(s), 1 ready, 0 not ready
  Mychar connected=true subgroup=0 class=1 roles=0 assistant 1500/1500 hp 100/100 power level 12 auras 2
  Otherchar connected=true subgroup=0 class=4 roles=0
```

Exit `0` when the run completes; `5` when the world server refuses the session or a step fails after login; `2` a
usage error (both invite sides given, `--promote-back` without `--promote`, a word that is not a loot method or icon).

# Examples

Invite a player:

```
forever-agent forever rpc ./slots/ACCOUNT invite '{"name": "Otherchar"}'
```

On the invited character's session, accept and read the roster:

```
forever-agent forever rpc ./slots/OTHER accept_invite '{}'
forever-agent forever rpc ./slots/OTHER party '{}'
```

Promote a member, then set need-before-greed from uncommon up:

```
forever-agent forever rpc ./slots/ACCOUNT promote '{"name": "Otherchar"}'
forever-agent forever rpc ./slots/ACCOUNT loot_method '{"method": "need-before-greed", "threshold": 2}'
```

Mark the skull and read the icons:

```
forever-agent forever rpc ./slots/ACCOUNT mark '{"guid": "0x00000000000000A1", "icon": "skull"}'
forever-agent forever rpc ./slots/ACCOUNT marks '{}'
```

Convert to a raid and move a member to subgroup 3:

```
forever-agent forever rpc ./slots/ACCOUNT convert_raid '{}'
forever-agent forever rpc ./slots/ACCOUNT raid_move '{"name": "Otherchar", "subgroup": 3}'
```

Start a ready check and read the answers from the event stream:

```
forever-agent forever rpc ./slots/ACCOUNT ready_check '{}'
forever-agent forever rpc ./slots/ACCOUNT events '{"since": 0}'
```

Or start it from a one-shot live check and log every answer:

```
forever-agent forever party-check --account EMAIL --character Mychar --ready-check
```

Leave the group:

```
forever-agent forever rpc ./slots/ACCOUNT leave_group '{}'
```

# What you see

Membership and control calls answer `{}` on success. `party` answers the roster object, `marks` `{marks: [...]}`,
`mark` the icon and the unit it went on, `accept_invite` the inviter. Failures are JSON-RPC errors whose text names
the reason: `promote: only the leader can (the leader is Otherchar)`, `raid_move: needs a raid: convert_raid first`,
`disband: the group has no other member`, and a readback timeout `X: the server did not confirm it within 5s (a
refused group command gets no answer)`.

# Common problems

- `promote: not in a group`; `promote: raid group control is not ported yet`.
- `mark: "skull " is not a raid target icon (star circle diamond triangle moon square cross skull)`.
- `raid_move: subgroup 9: use 1 to 8`; `raid_move: subgroup 3 is full (5 members): pass swap_with to trade places
  with one of them`.
- `raid_move: needs the group leader or an assistant`.
- `convert_raid: the group has 1 member(s): the core converts a group of two or more`.
- `ready_check: only the leader can (the leader is Otherchar)`; `ready_check: the server did not confirm it within
  5s (...)`, which is what a core that refused the start (another check running) answers.
- `accept_guild_invite: no pending guild invite`.
- `leave_group: not in a group`; `no valid party roster (...)` when the roster lost an update.
