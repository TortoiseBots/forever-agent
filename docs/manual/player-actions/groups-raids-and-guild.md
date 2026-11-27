---
type: Method Reference
title: Groups, raids and guilds
description: Invite and leave a group, read the party roster, lead a party (promote, loot method, marks, disband), convert it to a raid, handle guild invites, and check the group code live.
tags: [player-actions, party, raid, guild, group-control]
status: stable
generated: { by: forever-agent docs, at: 2026-11-27T02:00:00Z }
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
| `disband` | none | End the party. |

Only the leader may `promote`, `assistant`, `loot_method` and `disband`; in a party any member may `mark`, and in a
raid only the leader or an assistant (`mark: needs the group leader or an assistant`, refused before any packet).
Every one of these calls takes a party and a raid: the core reads the group's leader and members whatever the group's
size, and the roster the readback comes from carries a raid's members and flags. `assistant` is the raid rank alone:
the core sets the flag only in a raid and only from the leader, so a party reads
`assistant: needs a raid: convert_raid first` (a non-leader reads `assistant: only the leader can (the leader is
NAME)`).

`loot_method.method` is one of `free-for-all`, `round-robin`, `master-loot`, `group-loot`, `need-before-greed`,
`personal-loot` (or its number 0-5; the short forms `ffa`, `round`, `master`, `group`, `need`, `personal` are
accepted too). `master` names the master looter for master loot. `threshold` is the item quality 2-6
(uncommon-artifact), default 2. A call whose settings the roster already holds sends no packet, and a real change is
paced below the core's three a second: the core counts `CMSG_SET_LOOT_METHOD` on its own three-a-second limit and
closes the connection on the fourth inside one second, so the fourth change waits a second plus a margin.

The pace reads the roster, and the loot change is re-checked against the group it was planned for before its packet
goes out. The core resolves the packet's group by the party index it carries, so a roster that arrived during the
wait — another group in the same slot, the lead taken, an LFG group, a named master who left, the group destroyed —
is refused with `loot_method: the group changed while the packet was paced`, `... the leader changed while the packet
was paced (the leader is NAME)`, `... the group became LFG while the packet was paced` or
`loot_method: NAME left the roster while the packet was paced` (a destroyed or left group reads
`loot_method: not in a group`), and no packet is sent. The confirmation is the same group's roster: another group's
matching loot block never counts as the change having landed.

`mark.icon` is one of `star`, `circle`, `diamond`, `triangle`, `moon`, `square`, `cross`, `skull`. `mark` names the
unit by `guid` or by a roster member's `name` and sets that icon on it; the method has no way to clear an icon.
`marks` answers every icon the group holds, in slot order.

`disband` uninvites every other member in roster order; the last uninvite leaves this character alone, which is the
group's end.

The older daemon's group-finder methods (`lfg_join`, `lfg_leave`, `lfg_listings`, `lfg_status`, `lfg_auto`, the
proposal answers and `lfg_listing_role`) are refused by `forever serve` with a `-32003` error: the client dungeon
finder is not built on Forever, and the module's own finder fills the party and queues (`forever fb-dungeon`, and
the module's `.bot lfg` and `.bot dungeon` commands).

# Raid control

| Method | Parameters | Meaning |
|---|---|---|
| `convert_raid` | none | Turn the leader's party into a raid. |
| `raid_move` | `guid` or `name`, `subgroup`, `swap_with` | Move a member to a raid subgroup 1-8. |

`convert_raid` needs the leader and a group of two or more members. `raid_move` needs a raid and the leader or an
assistant. A subgroup holds five members: moving into a full one is refused, and `swap_with` names the member of the
target subgroup who trades places instead. A swap needs a member of the target subgroup who is in another subgroup
than the one being moved.

The sub-group packets are paced at 8 a second (130 ms apart), and so are `convert_raid` and `assistant`, which the
core counts in the same ten-a-second bucket: the core closes the connection on the 11th of them inside one second,
which a raid layout of nineteen moves would reach in a moment. A layout therefore takes a few seconds, and the
trace's `raid_move` events carry its moves as they go out.

The pace reads the roster while it waits, and `raid_move` re-reads it right before the packet goes out. Another
assistant can move a member meanwhile, and `CMSG_SWAP_SUB_GROUPS` names no expected subgroup: the core trades
whatever the two members hold when it arrives, so a swap planned on a roster that moved would land them in subgroups
the call never asked for. A swap whose two members no longer hold the planned subgroups is refused with
`raid_move: the roster moved while the packet was paced: ... retry the move` and sends nothing; one whose outcome the
roster already holds (the two members in each other's planned subgroups) answers without a packet. Both a swap and a
plain move re-check the group as well: another group's roster reads `raid_move: the group changed while the packet
was paced`, an ended raid reads `raid_move: the group is no longer a raid`, and a rank lost during the wait reads
`raid_move: the rank changed while the packet was paced: needs the group leader or an assistant`; none of them sends
a packet. A plain move also re-checks the member's presence, the target subgroup's room and the move another packet
already made, and `convert_raid` re-checks the lead and the member count.

# Guild

| Method | Parameters | Meaning |
|---|---|---|
| `accept_guild_invite` | none | Accept the held guild invite and wait for the join. |
| `decline_guild_invite` | none | Decline the held invite (nothing to wait for). |
| `guild_leave` | none | Leave the guild and wait for the guild field to clear. |

An invite is held from the guild-invite message; the accept consumes it, so a second accept has nothing to take and
a decline after an accept has nothing to decline. `guild_leave` on a character that is not in a guild is refused
with `guild_leave: not in a guild`.

# Check a party live: forever party-check

One-shot live check of the group code, with no daemon: it logs in like `forever world-check`, enters the world, does
what you asked (invite, accept or decline an invite, uninvite, promote, set the loot method, mark a creature), prints
what came back and logs out.

```bash
forever-agent forever party-check [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME [--stay 5s] [--invite NAME | --accept-from NAME | --decline-from NAME] \
    [--uninvite NAME] [--promote NAME [--promote-back DURATION]] [--loot METHOD[:THRESHOLD]] \
    [--mark ICON] [--wait-join DURATION] [--settle 4s] [--capture-dir DIR]
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
| `--loot METHOD[:THRESHOLD]` | none | Set the loot method and read the roster back: `free-for-all`, `round-robin`, `master-loot`, `group-loot`, `need-before-greed`, `personal-loot`, or `0..5`; threshold 2-6, default 2. |
| `--mark ICON` | none | Set this raid target icon on the nearest attackable creature and read the icon list back: `star`, `circle`, `diamond`, `triangle`, `moon`, `square`, `cross`, `skull`. |
| `--wait-join DURATION` | `0` | With `--invite`: wait up to this long for the group to form (stats, party line, stay); at most 2m. |
| `--capture-dir DIR` | none | Write every packet the combat, chat and party handlers see into a new run directory under this path; keep it outside the repository. |

The summary is the roster the session saw, in the same fields as the `party` method:

```text
party-check: in a group, 2 member(s), leader Mychar, loot free-for-all
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

- `promote: not in a group`.
- `mark: needs the group leader or an assistant` in a raid this character neither leads nor assists.
- `mark: "skull " is not a raid target icon (star circle diamond triangle moon square cross skull)`.
- `raid_move: subgroup 9: use 1 to 8`; `raid_move: subgroup 3 is full (5 members): pass swap_with to trade places
  with one of them`.
- `raid_move: needs the group leader or an assistant`.
- `convert_raid: the group has 1 member(s): the core converts a group of two or more`.
- `accept_guild_invite: no pending guild invite`.
- `leave_group: not in a group`; `no valid party roster (...)` when the roster lost an update.
