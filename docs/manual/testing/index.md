# Testing

* [How a run is judged](overview.md) - The four ways the agent turns a recording or a test run into a verdict, and which one answers which question.

# Running tests

* [Trials](trials.md) - Run the behaviour test bench: hire a bot into a scripted fight, score each specified behaviour, and read the card.
* [Scenarios](scenarios.md) - Write, check and run scenario files: scripted live tests that drive one or more agent sessions and check what came back.
* [Bug hunter](bug-hunter.md) - Find bot misbehaviour in recorded runs and write the ticket board, with stable ticket ids and a baseline's new/known/gone life.

# Scoring fights

* [forever spell-score](spell-score.md) - Judge a Forever class fight from its watch combat log: each party member's casts against the oracle of its class, failing only wrong-class abilities.
* [Realism checks](realism.md) - Take a bot snapshot with forever bot-snapshot, judge it against the aggregate player data with trials realism, and read the before/after of a realism batch.
* [fight-score](fight-score.md) - Score a finished fight JSON from the dashboard with the fight debugger's two tank checks and the class spell oracle.

# Results and records

* [Traces and records](traces-and-records.md) - What a run writes (trace, summary, combat log, run record, trials and scenario folders) and how trace-score re-reads a trace.
* [Report cards](report-cards.md) - Read the two report cards: the trials card for a behaviour run and the dungeon-run card with its verdict, legs and flags.
* [CI and local checks](ci-and-local-checks.md) - What runs on every push and nightly, and how to run the same checks, shards, capture scrub and gates locally.
