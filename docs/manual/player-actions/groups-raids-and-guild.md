---
type: Method Reference
title: Groups, raids and guilds
description: Invite and leave a group, read the party roster, lead a party (promote, loot method, marks, disband), convert it to a raid, and handle guild invites.
tags: [player-actions, party, raid, guild, group-control]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: serve-session
    resource: internal/forever/world/serve_session.go
  - id: serve-session-methods
    resource: internal/forever/world/serve_session_methods.go
  - id: groupctl
    resource: internal/forever/world/groupctl.go
  - id: raidctl
    resource: internal/forever/world/raidctl.go
  - id: guild-session
    resource: internal/forever/world/guild_session.go
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
| `assistant` | `name`, `clear` | Set (or with `clear: true` take away) a member's assistant flag. |
| `loot_method` | `method`, `master`, `threshold` | Set the loot method. |
| `mark` | `guid` or `name`, `icon` | Put a raid target icon on a unit. |
| `marks` | none | Read the icons back. |
| `disband` | none | End the party. |

Only the leader may `promote`, `assistant`, `loot_method` and `disband`; in a party any member may `mark`. These
calls are refused in a raid (`raid group control is not ported yet`), because the raid ranks differ and the raid
operations come with the raid runs.

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
proposal answers and `lfg_listing_role`) are not ported by `forever serve`: they answer "not ported on this server".

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

An invite is held from the guild-invite message; the accept consumes it, so a second accept has nothing to take and
a decline after an accept has nothing to decline. `guild_leave` on a character that is not in a guild is refused
with `guild_leave: not in a guild`.

# Examples

Invite a player:

```
forever rpc ./slots/ACCOUNT invite '{"name": "Otherchar"}'
```

On the invited character's session, accept and read the roster:

```
forever rpc ./slots/OTHER accept_invite '{}'
forever rpc ./slots/OTHER party '{}'
```

Promote a member, then set need-before-greed from uncommon up:

```
forever rpc ./slots/ACCOUNT promote '{"name": "Otherchar"}'
forever rpc ./slots/ACCOUNT loot_method '{"method": "need-before-greed", "threshold": 2}'
```

Mark the skull and read the icons:

```
forever rpc ./slots/ACCOUNT mark '{"name": "Kobold Vermin", "icon": "skull"}'
forever rpc ./slots/ACCOUNT marks '{}'
```

Convert to a raid and move a member to subgroup 3:

```
forever rpc ./slots/ACCOUNT convert_raid '{}'
forever rpc ./slots/ACCOUNT raid_move '{"name": "Otherchar", "subgroup": 3}'
```

Leave the group:

```
forever rpc ./slots/ACCOUNT leave_group '{}'
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
- `accept_guild_invite: no pending guild invite`.
- `leave_group: not in a group`; `no valid party roster (...)` when the roster lost an update.
