# Running a dungeon

* [forever dungeon-run](dungeon-run.md) - Run one dungeon plan with the agent as commander of five bots, and read the order list, trace and summary it leaves.
* [forever dungeon-watch](dungeon-watch.md) - Watch a dungeon run that a server module leads by itself, keep its safety budgets, rescue a wipe and record the run.
* [forever dungeon-play](dungeon-play.md) - Play one dungeon plan as a real character leading four bots, and read the run's record and its pass or fail verdict.
* [forever preflight](preflight.md) - Check the serve slot, lease, plan, export, module, SOAP console and bots before a Forever dungeon run starts.

# The plan and its checks

* [Dungeon plans](plans.md) - What a dungeon plan file is, every field it can carry, and what its status and forever_status mean.
* [Dungeon notes](dungeon-notes.md) - What the five oldest hand-written plans contain, how far each has been checked, and the facts a first run should know.
* [Plan tools](plan-tools.md) - Check, lint, probe, stage, export, scout and compare a dungeon plan offline, before it costs a run.
* [From plan to run leader rows](run-leader-rows.md) - What plan-export writes into the bot module's run leader tables, which plan fields the rows carry, and which they cannot.
* [Route tools](route-tools.md) - Score a plan's route against the navmesh, walk it on a live server, edit and promote routes, rebuild a leg's waypoints, record patrol loops and record a GM's walk as world data.
* [How the aggro estimate works](aggro-estimate.md) - The aggro radius, packs, call-for-help ring and drag sweep an offline check counts, and what the estimate cannot see.
* [World exports and spawns](world-exports.md) - The files that describe a map's spawns, packs, patrols, game objects and entrances, and how the checks read them.
* [Navmesh and maps](navmesh-and-maps.md) - The mmap files every check and walk plans on, how the two generations differ, and the commands that draw maps, extract minimaps and read client data.

# Reference

* [Plan tools troubleshooting](plan-troubleshooting.md) - The errors the plan check, export, probe, scout, oracle and route-edit tools print, what they mean, and the fix.
* [Stop reasons and verdicts](stop-reasons.md) - Every reason a Forever dungeon run, watch or walk can end with, and the verdict each kind of report carries.
