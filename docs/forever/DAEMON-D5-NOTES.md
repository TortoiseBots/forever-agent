# Forever agent #92 round D5: the loot and object methods

What this round adds: `loot`, `loot_take`, `loot_close`, `open_object`, `use_object`, `gameobjects` and
`roll` in `serve_loot_methods.go`, on the method frame D2 built. Fake world only: no live run yet.

## The held window

The old daemon's loot window lived in the daemon. Here `s.methodLoot` is the window the server granted,
kept across calls: `loot`, `open_object` and `use_object` set it, the packet handler removes a slot from
it when the server sends SMSG_LOOT_REMOVED (and zeroes its money on SMSG_COIN_REMOVED), and a release
clears it: the call's own CMSG_LOOT_RELEASE, the server's SMSG_LOOT_RELEASE for the window's owner (the
#127 fix: the release names the owner, not the loot object), SMSG_LOOT_RELEASE_ALL, or the teardown and
logout path. Every open releases a held window first, so a second corpse never leaves the first one locked.
`teardown` adds a `loot` step only when a window is held, before the GM line and the logout; `serveLogout`
(a signal) releases it too, on a context of its own.

## The methods

| Method | What it does |
|---|---|
| `loot {guid? or name?}` | No `guid` and no `name`: the held window, no packet (none held is an RPC error). Else the unit must be in the model and dead (a live creature is an RPC error before any packet); the window is opened with `lootTarget`, which walks within reach (the old `loot` did not walk; this one does, kept from loot-check). Answer: `{since, ok, event, guid, loot_guid, type, money, items:[{slot, item_entry, count, slot_type}]}`. A refusal or silence is `ok:false` with a `loot_error` event. |
| `loot_take {slot? or all? or money?}` | Needs the held window. `slot` takes one slot, `all` takes every takeable slot plus the money, `money` takes the money. A slot still on a roll, locked or for the master looter is reported in `skipped` and never requested. Answer `{since, ok, event, taken, left?, skipped?, money?, window}`; the window is released when nothing is left (no slots and no money) and `window` is then null. A slot not in the window is an RPC error before any packet. |
| `loot_close` | Releases the held window; none held is `ok:true, closed:false` with no packet. |
| `open_object {guid, take?}` | `spell_id` is refused (objects open with CMSG_GAME_OBJ_USE on this server). Walks, opens the object, answers `{window, taken, money?, left?}`; `take` takes everything takeable and releases. A door or lever (a state change, no window) is `window:null, taken:[]`. |
| `use_object {guid, wait_ms?}` | Sends GAME_OBJ_USE and REPORT_USE without walking and waits (default 4 s) for the first of a loot window, a refusal, a gossip or quest list, a `gameobject` row or a state change. `outcome` is that event's kind, `state_changed` or `none`; `ritual` marks a summoning ritual (game object type 18), whose `none` is the ritual waiting for its other users; `hint` names a distance beyond reach or a locked flag. A window that opens is held. Running out of the wait is the answer `none`, not an error. |
| `gameobjects {within?, radius?, entry?, name?, type?, limit?}` | The game objects in the model, nearest first, with `type` (number) and `type_name` (SharedDefines.h GameobjectTypes), position, distances, `state` and `flags`. `limit` defaults to 100. |
| `roll {looted?, item_slot?, choice}` | `choice` is need, greed or pass. The roll is named by `looted` (the loot GUID) and `item_slot`, or it is the only open roll; none or several is an RPC error that lists them. `roll_id` is refused. A vote the roll's valid-votes mask does not allow, a roll that ended and a second vote are RPC errors before the packet. No wait. |

## Decisions

- `loot_take all` includes the money: the window is then empty and releases itself.
- Opening a window starts a fresh loot watch (`lootTarget` resets it), which forgets every roll. The
  methods carry the open rolls across, so a vote still owed on another corpse's loot survives.
- A release is confirmed by the held window clearing, which `handleLoot` does for the owner's SMSG_LOOT_RELEASE.
  The loot-check report flag is not used: it stays set after the first release in a session.
- The fight code is untouched (open bug #149 is a separate round).

## Not live-proven

Everything here is the handler-boundary fake. The live proof (L1) loots one corpse through the methods
and hands in a quest.
