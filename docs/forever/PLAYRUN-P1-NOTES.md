# The agent's player run, round P1: the bot-commands v1 contract

Scope: the text contract between forever-agent and the bot module for the player run (forever-agent #88), the
parser and client over the command channel, and the `Owner` port with its golden fake that the run's later phases
talk to. No play machine, no live run: rounds P2a-P5b build those.

## The contract

`contracts/bot-commands/v1/README.md` and `golden/cases.txt` are ADR 0001's golden examples for the player side:
the request lines `.bot <verb> <Bot>` for the ten verbs, the reply grammar
`TortoiseBots: <verb> <Bot>: ok <text>` / `...: refused <code>: <text>`, the 24 refusal codes, the `who` header
`TortoiseBots: who: <n> bot(s)` and its row (`<Bot> level <n> <Class> map <id> <move> <mode>`, as TortoiseBotsForever R1 #195 prints it), the refusal that names no bot (`<verb>: refused <code>: <text>`: usage, unknown-verb, players-off), the two whisper words `release` and `corpse run`, and the rule that
a change of any line shape is version 2. `golden/cases.txt` covers each reply verb, each B-1 and B-2 refusal code
plus `on-cooldown`, `who` with zero and two rows, and the core's own `There is no such command`. The module side
proves itself against these files (the TBF #129 review copies them into its wiring test).

## Code

* `internal/forever/command/botcmd` — `Verb` and `ParseVerb`, `Line` (closed verb set, names through
  `chat.NormaliseName`, the line through `chat.ValidateCommandText`, no `\\`), `Reply` and `ParseReply` (the prefix
  is required), `Reply.Err` (`ErrRefused` wrapped with the code), `WhoRow` and `ParseWho` (the header's count must
  equal the rows; the row's `move` and `mode` words are closed), `Classify` (the line for the verb and bot sent;
  the core's unknown-command answer is `ErrNoModule`, a missing line `ErrNoReply`), and `Client` over
  `*command.Session` with `Add Logout Follow Stay Attack Flee Summon Stats Who`.
* `internal/forever/driver/owner.go` — the `Owner` port (`Add Logout Follow Stay Attack Summon Who`, each
  returning `botcmd.Reply, error`): the only way the player run reaches the module, so its methods are the run's
  closed vocabulary for `.bot` lines. The `who` rows are read through `botcmd.Client.Who`; the port's `Who` is the
  header answer.
* `internal/forever/driver/owner_fake.go` — `GoldenOwner`: the fake module over a scripted bot table (online, in
  the party, map, summon cooldown until, dead) on the driver's `VClock`. It answers with the contract's reply
  lines, records every line it was asked, and cannot answer a case the contract cannot express (it builds each
  line with `botcmd.Line`).

## Tests

`TestGoldenCases` reads `golden/*.txt` from the package's path and checks every block: the request is what `Line`
builds, and the reply lines parse to the block's `=` line (or, for `no-module`, refuse to parse and classify as
`ErrNoModule`). Also `TestParseReplyNeedsPrefix`, `TestClassifyMatchesVerbAndBot`, `TestModuleAbsentIsError`,
`TestLineClosedVocabulary`, `TestWhoRows` and (driver) `TestGoldenOwnerAnswers`. The focused run is
`go test ./internal/forever/command/... ./internal/forever/driver/ -run "Golden|Reply|Classify|Module|Line|Who|Owner"`.

Mutations applied by hand, each caught by a test and reverted:

| mutation | caught by |
|---|---|
| the prefix is optional in `ParseReply` | `TestParseReplyNeedsPrefix` |
| the first line is taken regardless of the bot named | `TestClassifyMatchesVerbAndBot` |
| a refusal's code is dropped | `TestGoldenCases` (the refusal blocks) and `TestParseReplyNeedsPrefix` |
| `Line` builds a verb outside the set | `TestLineClosedVocabulary` |
| the core's absent line is read as ok | `TestModuleAbsentIsError` and `TestGoldenCases/module-absent` |

## What is not there

The module side is TortoiseBots' player commands (TBF #129 rounds 1-3, `PLAYER-COMMANDS-SPEC.md`), which is not
merged. `GoldenOwner` stands in for it until it ships; the run's live phases start at P2a, so no live run happened
here.

Limits:

* `.bot help` prints the caller's usage rows; no reply shape is stated for them, so the goldens cover the
  eight reply verbs and `who`, not `help`'s text.
* `usage` and `who`'s refusal have no golden (their request cannot be built by `Line`); both are unit-tested
  (`TestRefusalWithoutBot`). Verified against the module's R1 source (TortoiseBotsForever #195 `TbPlayerCmd.cpp`):
  the `who` row and the no-bot refusal shape are its real replies; its `help` rows are free text.
