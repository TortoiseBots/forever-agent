---
type: Guide
title: Scripted actions with act
description: Drive the character through a list of scripted steps - talk, buy, quest, loot, train, fly, mail and the rest - with forever act or the act method.
tags: [player-actions, act, scripting, steps]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T02:05:00Z }
sources:
  - id: act
    resource: internal/forever/world/act.go
  - id: inventory-act
    resource: internal/forever/world/inventory_act.go
  - id: vendor-act
    resource: internal/forever/world/vendor_act.go
  - id: quest-act
    resource: internal/forever/world/quest_act.go
  - id: trainer-act
    resource: internal/forever/world/trainer_act.go
  - id: taxi-act
    resource: internal/forever/world/taxi_act.go
  - id: mail-act
    resource: internal/forever/world/mail_act.go
  - id: bg-act
    resource: internal/forever/world/bg_act.go
  - id: act-cmd
    resource: cmd/forever-agent/forever_act.go
---

`act` is a small scripting language for one character: a list of steps, each one world action, run in order. It is
how the one-shot `forever act` command works (log in, settle, run the steps, log out) and it is also a method of a
running `forever serve` session (`act`), so a test can drive a character that is already in the world.

Every step is one word and `key=value` arguments:

```
npc entry=1234
vendor
buy entry=2589 n=5
```

Each step prints exactly one line: `step N verb ok <detail>` or `step N verb fail <reason>`. Every refused step gets
its `fail` line, whichever verb it is: a verb that returns its refusal without printing one gets the line from the
step runner, and a verb that prints its own (`wait`, `gm`, `goto`) keeps the one it printed. A failed step ends the
run; the steps after it do not run. A step that opens a menu prints its options first
(`menu option 12 npc 1234 "I want to browse your goods."`), a vendor prints its rows, a quest list its quests.

# Running a script

With the one-shot command:

```
forever-agent forever act --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
  --account ACCOUNT --character Mychar \
  --step 'npc entry=1234' --step 'vendor' --step 'buy entry=2589 n=5'
```

| Flag | Default | Meaning |
|---|---|---|
| `--step 'verb k=v'` | - | One step; repeatable, run in order. |
| `--steps FILE` | - | A file of steps, one per line (blank lines skipped). |
| `--stay DUR` | 3s | How long to settle in the world before the first step (0 to 10m). |
| `--capture-dir DIR` | - | Write the packets this run's handlers saw into a new run directory under DIR. |
| `--mail-to NAME=GUID` | - | Allow-list one `send-mail` recipient (repeatable). |
| `--mail-money-cap N` | 10000 | The most copper one `send-mail` may carry. |
| `--pay-cod` | off | Let `mail-take` pay a COD mail's copper to its sender. |

The command needs `--rest`, `--rpc`, `--ca`, `--account` and `--character`; the password comes from
`FOREVER_PASSWORD`. Only `forever serve` reads `--password-file`; see [first login](../getting-started/first-login.md).

The whole run has a deadline of five minutes plus the settle time plus a full flight budget for every `fly` step, so
a long flight is never cut mid-air.

As a daemon method:

```
forever-agent forever rpc ./slots/ACCOUNT act '{"steps": ["npc entry=1234", "vendor"], "timeout_ms": 30000}'
```

`timeout_ms` 0 or absent is the default (60 seconds, or the flights' budget for a script with `fly`); the most is ten
minutes (or the flights' budget when that is longer). The answer is `{lines, ok}`: the same lines `forever act`
prints, `ok` true only when every step succeeded. The run holds the session while it goes, so every other call waits
behind it.

A few steps need session settings the daemon may not have: `goto`, `npc`'s walk and `follow` need the serve session
to run with `--navmesh` (otherwise `goto` needs `straight=1`), `gm` needs the daemon to allow GM unit lines, and
`send-mail` needs a `--mail-to` allow-list, which only `forever act` sets (so `send-mail` is refused through the
`act` method). `mail-take` on a COD mail needs `--pay-cod`, which likewise only `forever act` sets.

# The steps

## Basic steps

| Step | Arguments | What it does |
|---|---|---|
| `wait` | `dur=` (default 1s, at most 10m) | Waits, letting the link's packets in. |
| `gm` | `line=TEXT` | Sends one GM command line and passes only on a clear success. |
| `goto` | `x=`, `y=`, `z=`, `straight=1` | Walks to a point: on the navmesh, or in a straight line when `straight=1`. |
| `inv` | - | Prints the inventory (items and money) as an `inv` method answer. |
| `npc` | `entry=N`, `straight=1` | Finds the nearest creature of that entry, walks within about 4 yards, and makes it the current NPC. A new `npc` step clears the menu, vendor, trainer and taxi state. |
| `open` | - | Talks to the current NPC and prints its menu (or its quest list). A quest giver with one quest opens that quest directly. |
| `open` | `entry=` or `slot=` | Opens an item (a bag, a wrapped gift, a lootable) instead. |
| `select` | `option=N` | Chooses an option of the open menu; a submenu it opens becomes the open menu. |
| `close` | - | Closes the interaction with the current NPC. |
| `mount` | `spell=N` or `entry=N spell=M` | Mounts with a spell, or with a mount item and its on-use spell. |
| `dismount` | - | Cancels the mount. |
| `follow` | `lead=NAME`, `dur=` (required), `near=`, `far=`, `lost_s=`, `tick_ms=` | Follows a player for up to `dur` (needs `--navmesh`). |

`gm` prints the line as its detail. `goto` prints the point it reached. `npc` prints `entry N within reach`. `open`
prints `menu N with K option(s)` or `quest list with K quest(s)`. `follow` prints its reason and walk/stop counts.

## Item steps

Every item step names the item by `entry=` (only when it is in one stack) or by `slot=` (a position:
`45` for a backpack slot, `30:3` for the fourth slot of the bag in bag slot 30). Destructive steps refuse an item
whose position the server has not confirmed.

| Step | Arguments | What it does |
|---|---|---|
| `equip` | `slot=` or `entry=`, `dst=0-18` | Equips the item. Without `dst` the server chooses the slot; with `dst` the item is swapped into that worn slot. |
| `store` | `slot=` or `entry=`, `bag=30-33` | Puts the item in a bag (default: the first bag carried). |
| `swap` | `a=POS`, `b=POS` | Swaps two positions. Two stacks of the same item are refused (the server merges those). |
| `split` | `from=POS` or `slot=`/`entry=`, `n=`, `to=POS` | Splits `n` off a stack; `to` defaults to autostore. |
| `destroy` | `slot=` or `entry=`, `n=` | Destroys `n` (whole stack when absent) and checks the count dropped. |
| `use` | `slot=` or `entry=`, `spell=N`, `target=self|npc` | Uses an item's on-use spell at yourself (default) or the current NPC. |
| `open` | `slot=` or `entry=` | Opens an item; a loot window it produces is released again. |

`equip`/`store`/`swap` print where the item went (or that the server must still confirm it); `split` prints the
counts; `destroy` the count left; `use` whether the server cast the spell.

## Vendor steps

| Step | Arguments | What it does |
|---|---|---|
| `vendor` | - | Opens the current vendor and prints its rows (`vendor item muid 3: entry 2589, 13 copper for 5, stock 4`). |
| `buy` | `entry=` or `muid=`, `n=` (default 1) | Buys, waiting for the server's answer and the item in the inventory. |
| `sell` | `slot=` or `entry=`, `n=` (0: whole stack), `force=1` | Sells. Worn items are refused; a quest item needs `force=1`. |
| `sell-junk` | - | Sells every poor-quality item the vendor takes and counts what left. |
| `buyback` | `slot=72-83` | Buys one item back from the vendor's buyback slots. |
| `repair` | - | Repairs everything worn and carried at the current repairer. |

Money lines are estimates read back from the inventory: `<before> -> <after> copper (est., N spent)`.

## Quest steps

| Step | Arguments | What it does |
|---|---|---|
| `quest-status` | - | Prints the current giver's status mask in words. |
| `quests` | - | Prints the character's own quest log (slot, quest, state, objectives). |
| `quest-accept` | `id=` | Accepts the quest and passes only when the log shows it. |
| `quest-progress` | `id=` | Prints the quest's progress from the log and the last details the giver sent. |
| `quest-complete` | `id=` | Hands the quest in; prints the reward screen when it opens. |
| `quest-reward` | `id=`, `choice=ITEM` | Takes the reward; `choice` is the item entry of a reward row and is required when the screen offers choices. |
| `quest-abandon` | `id=` | Abandons the quest and passes when the log no longer holds it. |

A quest step needs the current NPC (an `npc` step first) and a giver with the quest-giver flag for the giver steps.

## Trainer and flight steps

| Step | Arguments | What it does |
|---|---|---|
| `trainer` | `option=N` (optional) | Opens the current trainer and prints its spells. `option=` selects that gossip "Train me." row; a creature with several trainer rows needs it to name one (the first is taken otherwise). |
| `learn` | `spell=N` | Buys one spell from the open trainer list, checks the fare left the purse. Passes on the server's learned-spell answer for the spell or for the rank it teaches (the answer whose superseded spell is the bought one). |
| `taxi-nodes` | - | Opens the current flight master and prints its current, known and usable nodes. |
| `taxi-status` | - | Prints the flight master's own node status. |
| `fly` | `node=N` | Flies to a node the window offers and waits for the landing (across maps). |

`option=` is one of the menu's gossip option ids, not a trainer id. When the menu holds several "Train me." rows and
no `option=` is given, a log line names the ids and the first row is taken. A bad pick is refused before the list is
asked for:

- `option="two" is not a gossip option id` — the value is not a number;
- `the menu's 2 trainer option(s) [7 8] carry no option 9` — the id is not one of the menu's trainer rows, and the
  refusal lists the ids it has;
- `entry 1234 has no gossip menu, so option= cannot pick a trainer row (CMSG_TRAINER_LIST asks it directly)` — a
  trainer without a gossip menu is asked for its list directly, so there is no row to pick.

`learn` needs the `trainer` step's list for the same NPC first. It passes on the server's learned-spell answer that
names the bought spell, or on the answer for a castable spell's next rank: that answer names the taught rank, and its
superseded spell is the bought one. The fare must leave the purse in the update that follows the answer.

## Battleground steps

| Step | Arguments | What it does |
|---|---|---|
| `bg-queue` | - | Probes the current battlemaster's list and queues. A battlemaster that lists no supported battleground answers ok with a line that says there is no support, and the steps after it will have no queue to act on. |
| `bg-accept` | - | Ports in on the invite of the queue `bg-queue` joined. |
| `bg-leave` | `match=yes` | Leaves the queue; inside a started match only with `match=yes` (leaving casts the Deserter debuff). |

The battleground workflow around these steps is in [battlegrounds](../bots-and-raids/battlegrounds.md).

## Mail steps

| Step | Arguments | What it does |
|---|---|---|
| `mail` | - | Lists the current mailbox (one line per mail and attachment). The current NPC must carry the mailbox flag. |
| `mail-take` | `id=` | Takes a mail's attachments (a COD mail needs `--pay-cod`). |
| `mail-money` | `id=` | Takes a mail's money. |
| `mail-delete` | `id=` | Deletes an empty, moneyless, COD-free mail (the mailbox is re-read first). |
| `send-mail` | `to=`, `money=`, `stationery=` (default 41), `subject=`, `body=` | Sends text and money to an allow-listed recipient. |

The mail steps act on the mailbox the last `mail` step opened; a `mail-delete` refuses a mail that still holds
anything. `send-mail` requires the recipient to be on `--mail-to`, the money to fit `--mail-money-cap`, and the
server's name query to confirm the GUID; the wallet must read back as the exact debit.

# Step syntax

- A value is one word, or a double-quoted string when it holds spaces: `note="a b c"` (with `\"` and `\\` escapes).
- `line=` (only the `gm` step) takes the rest of the line, so `gm line=.additem 6949 1` sends the command with its
  arguments.
- A duplicate key is an error. A step with an unknown verb is an error that lists every verb.

# Examples

A vendor run: walk to the vendor, open it, sell the junk, repair, buy potions.

```
forever-agent forever act --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
  --account ACCOUNT --character Mychar \
  --step 'npc entry=1234' \
  --step 'vendor' \
  --step 'sell-junk' \
  --step 'repair' \
  --step 'buy entry=118 n=5'
```

A quest round trip, then a flight from the same script:

```
forever-agent forever act --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
  --account ACCOUNT --character Mychar \
  --step 'npc entry=823' --step 'open' --step 'select option=1' \
  --step 'quest-accept id=7' --step 'quests' \
  --step 'npc entry=1571' --step 'taxi-nodes' --step 'fly node=4'
```

Training:

```
forever-agent forever act --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
  --account ACCOUNT --character Mychar \
  --step 'npc entry=911' --step 'trainer' --step 'learn spell=19740'
```

# What you see

```
step 1 npc ok entry 1234 within reach
step 2 vendor ok entry 1234, 6 item(s)
vendor item muid 1: entry 2589, 13 copper for 5, stock 4
step 3 buy ok muid 1 entry 2589 x5, slot 36; 50 -> 37 copper (est., 13 spent)
```

A failure prints `step N verb fail <reason>` and ends the run; `forever act` exits nonzero (the world error code).
At most 500 lines are kept in an `act` method answer (`act: N more line(s) were not kept`), and a line is cut at 512
bytes (menu text is the server's).

# Common problems

- `no current NPC (an npc step comes first)` — the step needs an `npc` step before it.
- `no --navmesh: a walk needs straight=1 (a straight line at an invented height) or a navmesh`.
- `no --navmesh: following walks on the navmesh`.
- `entry 1234 is 9.4 yd away after the walk` — the NPC moved or is behind geometry.
- `buy: the open vendor list is not this NPC's (a vendor step on entry N comes first)`.
- `learn: no open trainer list for this NPC (a trainer step on entry N comes first)`.
- `fly: no taxi window (a taxi-nodes step comes first)`; `bg-accept: no battleground queue was selected (a
  bg-queue step comes first)`.
- `mail-take: mail 12 asks 300 copper COD; paying it needs --pay-cod`.
- `send-mail: NAME is not on the caller's --mail-to allow-list`.
- `act: 12 more line(s) were not kept (the answer keeps 500)` — a long menu or list outgrew the answer; the lines
  are still in the daemon log.
