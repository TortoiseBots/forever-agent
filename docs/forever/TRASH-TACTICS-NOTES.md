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

`dungeon` names the key's dungeon word (rule 19's key; empty reads `tactics`) and `comment` is an optional note.
`cc_spells` maps a CC spell id to its kind(s); `creatures` maps a creature entry to its row: the display `name`, the
`must_interrupt` spell ids, the `cc_immune` kinds, the `must_dispel` rows (`spell`, `type` in the lexicon's words or
its `dispel` alias, `within_s` seconds the aura may stay) and `priority` (1 killed first, 0 unordered). Ids and
entries are decimal or `0x`-prefixed hex, as the lexicon reads them; an entry is a full uint32, and the same entry
(or spell) spelled two ways (`16` and `0x10`) is a duplicate, not an overwrite. A hostile or mistyped file is an
error naming the file and the line: an unknown or duplicate field at any level (`must_interupt`, `cc_imune`), a
file over 1 MiB, more than 512 creatures or `cc_spells`, more than 64 `must_interrupt` ids or `must_dispel` rows on
one creature, more than 8 kinds on one CC spell, an unknown CC kind (`sap`, `polymorph`, `fear`, `root`, `stun`,
`shackle`, `sleep` are the set), a `must_dispel` of type `never` (which must stay, so it cannot be a must-dispel),
the same spell twice in one row, a non-positive spell, entry or `within_s`, a negative or fractional priority, a
non-string `dungeon`, an empty `creatures` object. An unlisted entry is unknown, never a finding.

**The rules** (`internal/bughunt/rules_tactics.go`, thresholds in `const.go`): 16-19 join `TacticsRules(t)` and
need `--tactics F` on `forever-agent bughunt`.

| Rule | Forever shape | Key |
|---|---|---|
| 16 missed interrupt | a listed `must_interrupt` spell's `spell_go` completion with no `interrupt` row for the same mob and spell in [t-1, t+3] (a later cast of the spell started before the interrupt makes that row its answer) while a free interrupter stood by. Free is a member of a class with an interrupt (warrior Pummel/Shield Bash 5 yd, paladin Hammer of Justice 10 yd, rogue Kick 5 yd, priest Silence 30 yd, shaman Earth Shock 20 yd, mage Counterspell 30 yd), its interrupt spell off cooldown from its own `spell_start`/`spell_go` rows, alive, not casting, with no `spell_fail` naming a stun, silence, pacify, fear or other mechanic in the 2 s before, and in that spell's range of the caster. The caster mob must be in combat with the group and not `cc`/flee. A cast's `spell_go` rows (one per target) are one hit | `(16, entry, spell)` |
| 17 CC on an immune mob | a member's cast of a `cc_spells` spell (its `spell_go`, `miss`, `resist` and `immune` rows within 1 s of the first are one cast) on a mob whose entry lists a kind immune. A cast that hit more than one target is an AoE and is never blamed; a single-target cast is blamed when its `immune` row is present, or when the mob is immune to every kind the spell carries (nothing could land) | `(17, entry, kind)` |
| 18 listed debuff left on | a `spell_go` from a non-member or an `aura_applied` row for the victim and spell, judged by that caster entry's own `within_s` (an `aura_applied` row with no listed caster takes the file's loosest listing, so an unlisted source never tightens another entry's limit). The episode ends at the first dispel row, `aura_removed` row, natural expiry from the aura row's `remaining_ms`, or the victim's `death` row; with none of those and the log carrying `aura_removed` rows it is still up at the end. Past the limit with a rule-15 able dispeller free | `(18, spell, type)` |
| 19 kill order | a mob's own `death` row (no death kind: UNKNOWN) while a higher-priority mob stood in reach of the group. Both are `combat` mobs in a frame with the group in combat, and an alive member is within 40 yd of the higher mob; kills and living mobs are judged per guid, so three same-entry mobs are three mobs | `(19, dungeon, low guid, high guid)` |

Every hit cites the combat rows. Rule 16 unknown: no `spell_go` kind, no `interrupt` kind (an older recording),
a cast whose source has no creature entry, an uncovered window, no frame near the cast, a member whose class is
unknown while no one is able, or a caster mob the frames do not show in combat. Rule 17 unknown: no
spell_go/miss/resist/immune kind; a row whose target has no entry, or whose caster is not a member, is outside
the check. Rule 18 unknown: no spell_go/aura_applied kind, an applying cast whose source has no creature entry,
an episode with no removal, expiry or death while the log has no `aura_removed` rows, or an uncovered window.
Rule 19 unknown: no death kind, a killed mob the frames never show in combat with the group, a higher mob the
frames never show in combat with the group, an NP-flagged position, or a higher mob whose fate past the kill
cannot be told. A listed priority 0, an entry the file does not list, an unlisted CC spell (polymorph on a mob
immune only to stun), and two entries with the same priority (one wave) are all outside the order.

**The command.** `forever-agent bughunt --tactics F` loads the file; rules 16-19 join the registry only when it
does, and `--rules 16` without it is a usage error naming the flag. The README's bughunt paragraph names the
flag, the four rules and the note that a recording without the kinds a rule reads is UNKNOWN.

## Tests

Hand-made combat rows (never captures); the example file loads with both entries:

- `TestLoadTacticsExample`, `TestLoadTacticsHostile` (eleven hostile files, each an error naming the file),
  `TestLoadTacticsCaps` (file bytes, creatures, cc spells and every list cap), `TestLoadTacticsEntryKeys`
  (full uint32 entries, `16`/`0x10` collisions, overflow), `TestLoadTacticsStrictFields` (unknown, duplicate and
  mistyped fields), `TestLoadTacticsDispelAlias`, and `TestLoadTacticsDocsExample`, which parses the example above
  so the doc and the parser stay one format.
- `TestRule16MissedInterrupt` (one finding with a kind-carrying unrelated interrupt row beside it; an answering
  interrupt row clears it), `TestRule16InterrupterCooldown` (a Kick inside its 10 s cooldown is not free),
  `TestRule16NotFreeWhenStunned`, `TestRule16PerClassRange`, `TestRule16MobNotInCombat` (UNKNOWN, counted),
  `TestRule16OneHitPerCast`, and `TestRule16UnknownWithoutInterruptRows` (UNKNOWN, counted).
- `TestRule17CCOnAnImmune` (the shackle is a hit, the polymorph and the unlisted spell are not),
  `TestRule17OneHitPerCast` (a spell_go and its immune row are one hit), `TestRule17AoECastInnocent` (a two-target
  cast is never blamed), and `TestRule17UnknownWithoutCCKinds` (UNKNOWN, counted).
- `TestRule18ListedDebuffLeftOn` (14 s past an 8 s limit is one 12 s hit), `TestRule18AuraRemovedEndsEpisode`,
  `TestRule18NaturalExpiry` (the aura row's `remaining_ms`), `TestRule18VictimDeathEndsEpisode`,
  `TestRule18CasterOwnLimit` (a 30 s caster is not judged by the file's 8 s strictest), `TestRule18StillUpAtEnd`,
  and `TestRule18UnknownWithoutAuraRemovals` (UNKNOWN, counted).
- `TestRule19KillOrder` (the priority-2 mob dies at t=10 while priority 1 lives), `TestRule19SameEntryPerGUID`
  (two enforcer guids are two kills), `TestRule19UnpulledHigherMob`, `TestRule19HigherOutOfGroupReach`, and
  `TestRule19UnknownWithoutDatedKill` (UNKNOWN, counted).
- `TestRuleNamesPinned` and `TestRegistryRulesNamed` pin every rule name (rule 14's included) and every group's
  order; the command's `TestSelectedRulesAreInRuleOrder` pins the registry's rule order.
- `TestTacticsSkipWithoutFile` (all four skip) and the command's `TestBughuntCommandRefusesBadInput` (rule 16
  without `--tactics`, a hostile tactics file).
- `TestAuraAppliedRowCarriesRemaining` in `internal/forever/world` pins the aura row's `remaining_ms`.

## Gates

Run on a clean committed tree at `e4c90a3d`:

```
make fmt-check            -> (no output) rc=0
make cross                -> windows/amd64 and darwin/arm64 build, windows vet: rc=0
staticcheck ./...         -> rc=0 (no finding)
go vet ./...              -> rc=0
```

The VM suite:

```
RESULT: PASS  kind=ta wt=wt-fa-trash sha=e4c90a3d dirty=0 files=2390
rc=0  vm_duration=976s  laptop_wall=983s
```

A first run at `c6e5f9be` failed on two gate findings, both fixed in `e4c90a3d`: the rule-order test sliced its own
expected list from the wrong end, and the daemon's new `remaining_ms` row key had no field in the daemon client's
type (its type-match test caught it). No live run was made this round.

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
- Rule 16's interrupt list is the untalented 1.12 class table (Pummel/Shield Bash, Hammer of Justice, Kick,
  Silence, Earth Shock, Counterspell) with its base cooldowns and ranges: a talent or a spec that changes them is
  not read. A stun, silence or incapacitation is known from the member's own `spell_fail` reasons; the frames carry
  no party CC flag, so a controlled bot that never attempted an action leaves no trace.
- Rule 18's start is the first applying row the log holds; a debuff already up when the recording starts is dated
  from its first row, not from its real application. Its natural expiry needs the aura row's `remaining_ms` or an
  `aura_removed` row; an older log with neither is UNKNOWN for an aura no dispel or death ended.
- The combat log carries no schema or capability field, so "the log records this kind" is read from the recording
  never carrying a row of that kind: that is a rule's UNKNOWN, never a finding. A run where nobody interrupted is
  therefore UNKNOWN for rule 16 rather than a silent "no missed interrupt" (and, by the same rule, a run with one
  unrelated interrupt row answers); a future log schema that names its kinds would replace this signal.
