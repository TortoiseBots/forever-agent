# Trash tactics checked in the combat log

Module issue TortoiseBotsForever#23 asks for per-mob trash tactics generated from the core's creature data: what
each mob casts and what the party should do about it (interrupt it, never CC it, dispel it, kill it first). The
bug hunter now checks a watched run against a hand-written tactics file in the generator's shape, so the four
judgements the issue lists run before the generator exists. The tactics rows a Ragefire Chasm and Wailing Caverns
file would need (entries, spells, immunities written from the server's creature data the agent can already export)
are out of scope: this round ships the format doc, the loader, the four rules and an example file with two
hand-made entries.

## What was built

**The tactics file** (`internal/bughunt/tactics.go`, example `internal/bughunt/testdata/tactics-example.json`).
One JSON object per dungeon:

```json
{
  "dungeon": "example",
  "cc_spells": {"118": "polymorph", "5782": "fear", "20066": "shackle"},
  "creatures": {
    "113": {
      "name": "Searing Blade Cultist",
      "must_interrupt": [34887],
      "cc_immune": [],
      "must_dispel": [{"spell": 34888, "type": "magic", "within_s": 8}],
      "priority": 1
    },
    "114": {"name": "Searing Blade Enforcer", "cc_immune": ["stun"], "priority": 2}
  }
}
```

`dungeon` names the key's dungeon word (rule 19's key; empty reads `tactics`). `cc_spells` maps a CC spell id to
its kind(s); `creatures` maps a creature entry to its row: the display `name`, the `must_interrupt` spell ids,
the `cc_immune` kinds, the `must_dispel` rows (`spell`, `type` in the lexicon's words, `within_s` seconds the aura
may stay) and `priority` (1 killed first, 0 unordered). Ids and entries are decimal or `0x`-prefixed hex, as the
lexicon reads them. A hostile or mistyped file is an error naming the file and the line: an unknown CC kind
(`sap`, `polymorph`, `fear`, `root`, `stun`, `shackle`, `sleep` are the set), a `must_dispel` of type `never`
(which must stay, so it cannot be a must-dispel), a non-positive spell, entry or `within_s`, a negative or
fractional priority, an empty `creatures` object. An unlisted entry is unknown, never a finding.

**The rules** (`internal/bughunt/rules_tactics.go`, thresholds in `const.go`): 16-19 join `TacticsRules(t)` and
need `--tactics F` on `forever-agent bughunt`.

| Rule | Forever shape | Key |
|---|---|---|
| 16 missed interrupt | a listed `must_interrupt` spell's `spell_go` completion with no `interrupt` row for the same mob and spell in [t-1, t+3] while a free interrupter stood within 30 yd (rule 15's range; the set is every class table row with an interrupt, so a hunter, warlock, druid or unknown class is never able) | `(16, entry, spell)` |
| 17 CC on an immune mob | a `spell_go`, `miss`, `resist` or `immune` row for a `cc_spells` spell whose target's entry lists that kind immune, cast by a member | `(17, entry, kind)` |
| 18 listed debuff left on | the first `spell_go` from a non-member (the cast that applied it) or `aura_applied` row for the victim and spell, ending at rule 15's per-episode dispel; past the `within_s` limit with a rule-15 able dispeller free | `(18, spell, type)` |
| 19 kill order | both mobs shared one frame (the pull) and a lower-priority entry died (a `death` row, else the frame after its last alive frame) while the higher one was still up and within 40 yd | `(19, dungeon, low entry, high entry)` |

Every hit cites the combat rows. Rule 16 unknown: no `spell_go` kind, no `interrupt` kind (an older recording),
a cast whose source has no creature entry, an uncovered window, or no frame near the cast. Rule 17 unknown: no
spell_go/miss/resist/immune kind; a row whose target has no entry, or whose caster is not a member, is outside
the check. Rule 18 unknown: no spell_go/aura_applied kind, no dispel kind, or an uncovered window. Rule 19
unknown: an undated kill after a shared pull, an NP-flagged position, or a higher mob whose fate past the kill
cannot be told. A member listed priority 0, an entry the file does not list, an unlisted CC spell (polymorph on a
mob immune only to stun), and two entries with the same priority (one wave) are all outside the order.

**The command.** `forever-agent bughunt --tactics F` loads the file; rules 16-19 join the registry only when it
does, and `--rules 16` without it is a usage error naming the flag. The README's bughunt paragraph names the
flag, the four rules and the note that a recording without the kinds a rule reads is UNKNOWN.

## Tests

Hand-made combat rows (never captures); the example file loads with both entries:

- `TestLoadTacticsExample` and `TestLoadTacticsHostile` (eleven hostile files, each an error naming the file).
- `TestRule16MissedInterrupt` (one finding with a kind-carrying unrelated interrupt row beside it; an answering
  interrupt row clears it) and `TestRule16UnknownWithoutInterruptRows` (UNKNOWN, counted).
- `TestRule17CCOnAnImmune` (the shackle is a hit, the polymorph and the unlisted spell are not) and
  `TestRule17UnknownWithoutCCKinds` (UNKNOWN, counted).
- `TestRule18ListedDebuffLeftOn` (14 s past an 8 s limit is one 12 s hit) and `TestRule18UnknownWithoutDispelRows`.
- `TestRule19KillOrder` (the priority-2 mob dies at t=10 while priority 1 lives) and
  `TestRule19UnknownWithoutDatedKill` (UNKNOWN, counted).
- `TestTacticsSkipWithoutFile` (all four skip) and the command's `TestBughuntCommandRefusesBadInput` (rule 16
  without `--tactics`, a hostile tactics file).

## Gates

Run on a clean committed tree at `54bd2809`, with no review round in between:

```
make fmt-check            -> (no output) rc=0
make cross                -> windows/amd64 and darwin/arm64 build, windows vet: rc=0
staticcheck ./...         -> rc=0 (no finding)
go vet ./...              -> rc=0
```

The VM suite:

```
RESULT: PASS  kind=ta wt=wt-fa-trash sha=54bd2809 dirty=0 files=2349
rc=0  vm_duration=749s  laptop_wall=770s
```

No live run was made this round.

## Live checks for the lead's live queue

No live run was made this round. The first session that carries a tactics file should check, on a test server:

1. **One missed interrupt.** Pull the cultist's pack with a rogue free: the log's `spell_go` for the listed heal
   with no `interrupt` row for it is one rule-16 hit citing the row; kicking it clears the hit.
2. **One immune CC.** Polymorph the listed-immune mob: one rule-17 hit citing the cast row. A CC the mob is not
   listed immune to is no hit.
3. **The example file is not a dungeon file.** Rules 16-19 against real Ragefire Chasm rows answer from the
   example's two entries only; a real run needs the generated file (the out-of-scope generator), not this one.

## Known limits

- The example file judges two invented entries; a real RFC/WC file needs the core-data generator (the issue's
  proposal), which this round does not build.
- Rule 16's 30 yd interrupter range is rule 15's dispel range, not a per-spell melee range; a generator row may
  narrow it per spell.
- Rule 18's start is the first applying row the log holds; a debuff already up when the recording starts is dated
  from its first row, not from its real application.
