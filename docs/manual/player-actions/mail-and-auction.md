---
type: Method Reference
title: Mail and auction house
description: Read, take, return and send mail, and browse, buy and sell at the auction house.
tags: [player-actions, mail, auction, economy]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: mail-methods
    resource: internal/forever/world/serve_mail_methods.go
  - id: mail-act
    resource: internal/forever/world/mail_act.go
  - id: auction-methods
    resource: internal/forever/world/serve_auction_methods.go
---

> **Not yet proven on a live server.** The mail act steps and the auction methods are exercised against the scripted world; no live run of them has been made yet. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

This page covers the two mailbox-style services: mail (a mailbox creature or mailbox game object) and the auction
house (an auctioneer). Both use the same pattern: a call sends one request and waits for the server's answer, and a
read that gets no answer is a result with `ok: false`, while a command that gets no answer is an error that says its
outcome is unknown (so a retry could repeat it).

# Mail

Every mail call names a mailbox in `guid`: a creature with the mailbox flag (within about 4 yards) or a game object
of the mailbox type (within about 9 yards). Methods never walk.

| Method | Parameters | Meaning |
|---|---|---|
| `mail_list` | `guid` | Ask the mailbox for its list and answer every mail and its attachments. |
| `mail_read` | `guid`, `mail_id` | Mark one mail read and confirm it with a fresh list. |
| `mail_take_item` | `guid`, `mail_id`, `index`, `force` | Take one attachment (by its index, or the only one). |
| `mail_take_money` | `guid`, `mail_id` | Take the money the mail holds. |
| `mail_delete` | `guid`, `mail_id`, `force` | Delete a mail. **Always needs `force: true`.** |
| `mail_return` | `guid`, `mail_id` | Return a player's mail to its sender. |
| `mail_send` | `guid`, `to`, `subject`, `body`, `money`, `cod`, `attachments`, `force` | Send one mail. |
| `mail_status` | none | Seconds until the next mail and who has unread mail for the character. |

The first call at a mailbox is `mail_list`. Every command after it addresses a mail the character has seen in that
mailbox's last list; a mail from another mailbox, or one the list did not hold, is refused before any packet.

## The list

Each row:

| Field | Meaning |
|---|---|
| `mail_id` | The mail's id, what the other calls name. |
| `sender_type`, `sender`, `sender_guid`, `sender_id` | Who sent it: 0 and a player name/GUID for a player, the core's mail type and id for an auction house, creature, object or calendar. |
| `subject`, `body` | The text. |
| `money`, `cod` | Coppers enclosed, and coppers the receiver pays on delivery (cash on delivery). |
| `read`, `flags`, `days_left` | Read flag, the raw flags and how long before it expires. |
| `items` | Attachments: `[{index, attach_id, item_entry, count, charges, durability, max_durability, enchants, gems}]`. |

## Sending

`mail_send` sends text, money, an optional COD and up to 12 attachments. Attachments are named like every item
selector: `item_entry` (exactly one stack), or `bag` and `slot` (255 is the backpack, 30-33 a bag). Worn items and
worn bags cannot be attached, and an item can be named only once.

- `cod` needs at least one attachment (the server ignores a COD on a mail without items).
- The postage is 30 copper for a mail with no attachments, or 30 copper per attachment, plus the money enclosed; the
  purse must hold it. The answer's `copper` is `{before, after}`, an estimate read from the inventory.
- If a previous `mail_send` went out and no answer came, the next send is refused until `force: true`, because the
  first may have been delivered and charged.

## Taking and deleting

- A COD mail is paid for when its item is taken: `mail_take_item` refuses it unless `force: true`, so a script never
  pays a sender by accident. The answer's `copper` shows the payment.
- `mail_take_money` refuses a mail that holds no money.
- `mail_delete` always requires `force: true`: the core deletes a mail with whatever it still holds, and the list
  cannot prove a mail empty (an attachment whose item the server could not load is left out of the row). Read the
  mail first, take what matters, then delete.

`mail_return` refuses mail from an auction house or a creature, a mail already returned, and one the sender made
unreturnable.

## mail_status

`mail_status` names no mailbox: it asks for the next-mail timer and answers
`{next_mail_time, senders: [{sender, time_left, sender_id, sender_type, stationery_id}]}`.

# Auction house

| Method | Parameters | Meaning |
|---|---|---|
| `auction_hello` | `guid` | Open the house; answers `house_id` and whether it is open. |
| `auction_browse` | `guid`, `name`, `min_level`, `max_level`, `offset` | One page of item/level buckets. |
| `auction_items` | `guid`, `item_id`, `item_level`, `offset` | The auctions of one bucket. |
| `auction_owned` | `guid`, `offset` | The character's own auctions and the ones that sold. |
| `auction_sell` | `guid`, item selector, `min_bid`, `buyout`, `commodity`, `unit_price`, `count`, `hours`, `force` | Post an item or a commodity stack. |
| `auction_bid` | `guid`, `auction_id`, `amount`, `force` | Bid, or buy out when the amount is the buyout. |
| `auction_cancel` | `guid`, `auction_id`, `item_id`, `force` | Cancel one of the character's auctions. |
| `auction_quote` | `guid`, `item_id`, `quantity` | Ask what a quantity of a commodity costs now. |
| `auction_buy` | `guid`, `item_id`, `quantity`, `force` | Buy the quantity the last quote named. |

The auctioneer must carry the auctioneer flag and be within reach.

## Browsing and buying

`auction_browse` searches by name and level range (`min_level` default 1, `max_level` default 60) and answers one
page of buckets: `{item_id, item_level, total_quantity, required_level, min_price, contains_owner_item}`, plus
`total` and `more`. Page with `offset`: the core returns at most 500 buckets per search.

`auction_items` lists the auctions of one bucket: `{auction_id, item_entry, count, owner, min_bid, min_increment,
buyout, unit_price, bid, bidder, duration_left_ms, end_time}`. `commodities: true` means the bucket's items are
stackable and bought by quote.

A stackable item is bought in two calls: `auction_quote` for the quantity, then `auction_buy` with the same item and
quantity before the quote runs out (the quote's `valid_ms`). Buying without a matching, unexpired quote is refused.

`auction_bid` takes an explicit `amount` in copper; an amount equal to the buyout buys the auction. `auction_cancel`
cancels one of the character's own auctions (`item_id` is optional). A cancel while someone has bid costs 5 percent
of the bid, and the server answers `not_enough_money` when the purse cannot pay it.

## Selling

An item is posted with a minimum bid and/or a buyout, for `hours` 2, 8 or 24 (default 24):

- `commodity: true` posts a stack at `unit_price` per unit; `count` defaults to the whole stack; `min_bid` and
  `buyout` must be 0.
- Otherwise one item is posted with `min_bid` or `buyout` (at least one), and `min_bid` may not exceed the buyout.

The item must be in the bags and not worn. The server charges the deposit; the answer carries `auction_id` and the
purse estimate.

## The unknown-result rule

An auction command whose result never came leaves `force` required on every later command for the rest of the
session: the earlier answer may still arrive and cannot be told from the next one's. Check `auction_owned` and the
purse, then pass `force: true`.

# Examples

Read a mailbox:

```
forever-agent forever rpc ./slots/ACCOUNT mail_list '{"guid": "0x0000000000000b20"}'
```

Take the attachment at index 0 of mail 12345:

```
forever-agent forever rpc ./slots/ACCOUNT mail_take_item '{"guid": "0x0000000000000b20", "mail_id": 12345, "index": 0}'
```

Send 50 silver with a note:

```
forever-agent forever rpc ./slots/ACCOUNT mail_send '{"guid": "0x0000000000000b20", "to": "Otherchar", "subject": "repair bill", "body": "thanks", "money": 5000}'
```

Open the house and search for "linen":

```
forever-agent forever rpc ./slots/ACCOUNT auction_hello '{"guid": "0x0000000000000c30"}'
forever-agent forever rpc ./slots/ACCOUNT auction_browse '{"guid": "0x0000000000000c30", "name": "linen"}'
```

Quote and buy 20 of item 2589:

```
forever-agent forever rpc ./slots/ACCOUNT auction_quote '{"guid": "0x0000000000000c30", "item_id": 2589, "quantity": 20}'
forever-agent forever rpc ./slots/ACCOUNT auction_buy '{"guid": "0x0000000000000c30", "item_id": 2589, "quantity": 20}'
```

Post a green item for 2 gold buyout:

```
forever-agent forever rpc ./slots/ACCOUNT auction_sell '{"guid": "0x0000000000000c30", "slot": 35, "min_bid": 500, "buyout": 2000, "hours": 24}'
```

From a script, the mail steps `mail`, `mail-take`, `mail-money`, `mail-delete` and `send-mail` cover the mailbox;
`forever act` needs `--mail-to` and `--mail-money-cap` to send anything. See [act steps](act-steps.md).

# What you see

Mail calls answer `{since, ok, event, total, mails, mail, item, copper, next}` with only the fields the call
produced filled. Auction calls answer `{since, ok, event, house_id, total, more, buckets, auctions, sold,
commodities, quote, auction_id, copper}`. A read that got no answer is `ok: false` with a `mail_result` or
`auction_result` event whose text says the server was silent (out of range, throttled). Refusals after a command
packet are `ok: false` with the server's reason.

# Common problems

- `no mailbox <guid> in the world model`; `the mailbox is 12.0 yd away (walk_to it first)`; `game object ... is not
  a mailbox (type 3, the mailbox is type 19)`.
- `mail 12345 is not in a list from this mailbox (mail_list comes first)`.
- `mail 12345 is cash on delivery: taking its item pays 300 copper to the sender (force: true to pay)`.
- `mail 12345 would be deleted with whatever it still holds (...): force: true to delete`.
- `the last mail_send went out and no result came, so it may have been delivered and charged: ... then force: true
  to send again`.
- `entry 1234 is not an auctioneer (NpcFlags 0x... has no auctioneer bit)`.
- `auction_sell: an item post needs min_bid or buyout`; `a commodity post needs unit_price (copper per unit) and
  takes no min_bid or buyout`.
- `no quote for 20 of item 2589 (auction_quote comes first)`; `the quote for item 2589 has run out (auction_quote
  again)`.
- `the last auction command went out and no result came, ... then force: true to send this one`.
