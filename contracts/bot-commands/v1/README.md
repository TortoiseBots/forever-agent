# bot-commands v1

The player side of the bot module: the `.bot` lines a player sends on the command channel and the replies the
module sends back as system chat. It is a contract in the ADR 0001 sense: versioned here, with golden examples,
and the module (TortoiseBots) proves itself against the same files. The player run in forever-agent reaches the
module only through it.

Version 1. A change of any line shape (a request, a reply, a `who` row) is version 2 and a new directory; a v1
file is never edited in place.

## Requests

```
.bot <verb> <Bot> [arg]
```

Say chat on the command channel, one line. The verbs are a closed set: `help who add logout follow stay attack
flee summon stats`. `help` and `who` name no bot; every other verb names one. No v1 verb takes an argument.
Names are the server's normalised form (first letter upper case, the rest lower) and are compared
case-insensitively. The module's `\\` chain separator and newlines never appear in a v1 line. Any other `.` line
(`.tb`, `.gm`, ...) is outside this contract and the player run never sends one.

## Replies

One reply is one or more system chat lines, each starting with `TortoiseBots: `.

```
TortoiseBots: <verb> <Bot>: ok <text>
TortoiseBots: <verb> <Bot>: refused <code>: <text>
TortoiseBots: who: <n> bot(s)
TortoiseBots: who: <Bot> level=<n> class=<word> map=<id> move=<follow|stay|free> mode=<engine|spike>
```

`<text>` is free human text: it may hold spaces and `=`, and nothing in it is parsed. A refusal never names an
account, an account id or another character of the owner. `who` is one header line and then one row per bot, in
table order; `n` is the number of rows that follow.

Refusal codes:

```
usage unknown-verb no-such-player not-a-bot not-yours in-run no-engine players-off too-many
already-online not-found not-leader on-cooldown no-selection not-master queue-full target-dead
in-combat dead teleporting not-in-group already-on-map teleport-refused
```

A code outside this list is still a refusal, with that code: a reply is never read as `ok` on the strength of
its code being familiar.

## No module

On a server where the module is not loaded the core answers its own `There is no such command`. That line is not
a reply and is never read as `ok` or as a refusal: it means the module is not there.

## Whisper words

Two whisper words reach the module, and they are not `.bot` lines: `release` and `corpse run`. The module answers
them in its own chat, which this contract does not shape.

## Golden cases

`golden/cases.txt` holds one block per case:

```
# case: <name>
> <request line>
< <reply line>          one line per reply line, in order
= <expected parse>
```

The expected parse is one of:

- `verb=<v> bot=<Bot> ok=true|false code=<code> text=<text>` — a reply. The fields come in that order and the
  text is everything after `text=`, so it may hold spaces and `=`.
- `who n=<n>` and, for each row, `row=<Bot>,<level>,<class>,<map>,<move>,<mode>`.
- `no-module` — the core's unknown-command line; `botcmd.ParseReply` must refuse it and `botcmd.Classify` must
  answer "no module".

The request line of a block is also checked: `botcmd.Line` must build it byte for byte. Names in the files are
invented.
