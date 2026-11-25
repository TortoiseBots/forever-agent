# MAP-M1: the run-map adapter

`internal/forever/runmap` turns one watch folder into what `internal/mapview` draws. `runmap.Load(dir, Options)` reads the
trace (`bughunt.LoadWatch`), the `<run_id>.forever-run.json` summary and the plan, and returns a `mapview.Run`, the plan's
overlay and notes about what the record did not carry. `LoadRun` is the same with the run's id.

* **Tracks** are the party members from the frames, labelled by slot (`tank`, `healer`, `dpsN`) or, with `Names`, by the
  party record's character name. The slot comes from the member's role word, else from the party index in the trace's party
  records (leader first); damage dealers are numbered in party order.
* **Creatures** are the frames' hostile mobs (a mob with no known position is skipped), keyed by `GUIDKey`: FNV-1a 64 of
  the upper-case 32-hex guid, so two guids that share their low 64 bits stay two creatures.
* **Clock.** The module's `at_s` counts from its run start; the trace's `run_start` event is the anchor. A record without
  it is aligned at the trace's zero and a note says so.
* **Markers** are legs, pulls, deaths, wipes, pauses, rescues and boss kills. A wipe is the trace's own `wipe` event;
  a burst of deaths of the whole party that no event covers is inferred (M2 fix, below).
* The summary gained `loose_pulls`: pull events the module sent without a step (or leg), which the step log cannot hold.

## Review notes fixed in M2

| Note | Fix |
|---|---|
| Markers with no leader track landed at the origin | The nearest known position (the first party member with a route); with no route at all the marker is dropped and a note counts it |
| Markers outside the run were dropped silently | Counted by kind in the run's notes |
| Pull events without a step were lost | `loose_pulls` in the summary, drawn as pull markers |
| Wipe threshold shrank to the members seen in frames | It is the party size the party records name (5 when none) |
| Damage-dealer slots followed first appearance in frames | They follow the party records' order |
| One recorded wipe disabled inferring every later wipe (the review) | Inference is per burst (distinct members within 15 s); a burst is skipped only when a recorded wipe lies within 5 s of it |
