# Plans S9 notes (Deadmines + Wailing Caverns for a second server)

Public repo: plan data and the plan checker only. No live names, hosts,
addresses, captures, private documents.

## What changed and why

The two plans were written from one server's world export. On the second
server (measurements in the brief's facts file) four Deadmines legs and two
Wailing Caverns legs have no spawns at all, and two Deadmines door objects
have different entries at the same positions.

Dropped legs are deleted from the JSON: `skip_export` has the wrong polarity
(default-in, opt-out per export call) and `--skip-legs` is an exporter flag
with no record in the file. A leg that exists on no target server does not
belong in the file at all.

Door entries that differ per server stay in the file: the route (stands,
radii, patrols) is identical, only the entry number differs. The plan format
had no per-server field, so this round adds the smallest one: an object ref
may carry `alt_entries`, a map from a server name to the entry that server
uses at the same position. `plan-check --forever` selects the server's
entry; every other reader keeps the base entry, so the old server's plan is
unchanged.

## Deadmines

- Deleted legs `lab_tunnel`, `laboratory` (entries 61959-61961), `harvester`
  (boss 61963, members 61962) and the 61962 members of `hall` group 2.
- `rhahk_zor` `via` trimmed to the mine return (the lab-branch points are
  gone with the branch).
- `sneed` and `gilnid` door waits keep base entry 17153 and carry
  `alt_entries: {"forever": 16400}` (Sneed, Mast Room Door) and
  `{"forever": 16399}` (Gilnid, Foundry Door). Positions, states, timeouts and
  fail texts unchanged.
- Notes updated: the route note lists 14 legs, the door note names both
  entries per position, leg-specific custom-boss notes removed.

## Wailing Caverns

- Deleted legs `zandara` (boss 61968, members 61966-61967) and `vangros`
  (boss 61965, members 61964, hatchling 61218).
- Plan `kill_first` drops 61966. `pythas_c` now leads to `skum`;
  `west_column` now leads to `serpentis`.
- Base plan has no object steps; the extra file (Naralex event) is untouched
  (a draft, never exported).

## plan-check --forever --spawns

- The TSV gains an optional `kind` column (`creature` default, `object`):
  `guid entry x y z movetype [kind]`. Old six-column files still read.
- Game-object spawns load into `World.Objects`; creature rows are unchanged.
- New `objects` lint (error): every `open`/`use_object`, `wait_object` and
  `loot` row's entry (the `--forever` entry: base, or the `alt_entries`
  value when the plan names the server) must have an object spawn of that
  entry within the row's radius (default 25 yd) of the row's point. A missing
  object fails the check like a missing creature pack member fails the
  export-match tests.
- The creature side is unchanged: `--forever --spawns` still feeds the aggro
  and patrol lints only. Creature presence is pinned by the dungeon tests
  (below), not by a new lint.

## Tests

- `internal/dungeon/deadmines_test.go`, `wailingcaverns_test.go`: leg lists,
  boss lists, door entries (both servers), world-export match updated to the
  trimmed plans; the old-server-only entries must appear nowhere.
- `internal/worldspawn/foreverspawns_test.go`: the `kind` column, old files.
- `internal/planlint/objects_test.go`: object lint (hit, miss, alt entry).
- Fixtures are built from the facts file's numbers (spawns, entries,
  positions), no live server.
