---
okf_version: 0.2
---

# Start here

This is the user manual for forever-agent, a headless client for ForeverCore servers. Every page below has a
description copied from its own frontmatter, so this index says what the page lets you do before you open it.

Start with [what forever-agent is](getting-started/what-is-forever-agent.md), then build it and prove a login.
When you want to drive a character from a script, read the daemon area; for a dungeon, the dungeons area.

# Getting started

* [getting-started/](getting-started/) - The first pages: what the agent is, how to build it, a first login, configuration and the rules for testing on a server.
* [What forever-agent is](getting-started/what-is-forever-agent.md) - What the agent does, which server it speaks to, and which parts have been run against a live server.
* [Install and build](getting-started/install-and-build.md) - Build forever-agent from source, run its checks, and know what a build without cgo can still do.
* [Your first login](getting-started/first-login.md) - Prove that the agent can reach a ForeverCore server with login-check and world-check, create or delete a character, then start a session.
* [Settings, slots and leases](getting-started/settings-and-slots.md) - The files that tell the agent which server and account to use, how slot directories are laid out, and how leases keep one worker per slot.
* [Safety and GM guards](getting-started/safety-and-gm-guards.md) - The rules for testing on a server, the game-master hands and the boundaries that keep them off other players, and the console and capture limits.
* [Troubleshooting](getting-started/troubleshooting.md) - The errors the agent prints most often, what causes them, and the fix.

# The session daemon

* [daemon/](daemon/) - Run one character in the world as a daemon and drive it over JSON-RPC.
* [forever serve](daemon/serve.md) - Run the agent as a session daemon so other programs can drive one logged-in character over a local JSON-RPC socket.
* [RPC basics](daemon/rpc-basics.md) - Call a forever serve daemon's methods with the rpc command or any HTTP client, and understand the request envelope, errors and timeouts.
* [Status and world reads](daemon/status-and-world-reads.md) - Read the character's own state and the units, game objects, ground spells, item data and map picture around it.
* [Movement](daemon/movement.md) - Move the character with straight walks, navmesh routes and follow loops, ask the navmesh what ground is walkable, and check a walk live.
* [Method index](daemon/method-index.md) - Every method a forever serve daemon registers, with its parameters, what it does and the page that explains it.
* [Events and logs](daemon/events-and-logs.md) - Read the combat log, the session's ordered event stream and the chat log, with cursors and long-poll waits.

# Player actions

* [player-actions/](player-actions/) - Everything the character itself does: spells and combat, items, vendors, quests, trade, mail, chat, groups, pets and the act step runner.
* [Spells and combat](player-actions/spells-and-combat.md) - Read the spellbook and cooldowns, cast and cancel spells, and fight a unit with melee or spells.
* [Items and inventory](player-actions/items-and-inventory.md) - Read the character's inventory and equipment, move items between slots and bags, use an item, and mount or dismount.
* [Quests and loot](player-actions/quests-and-loot.md) - Take, hand in and abandon quests, read the quest log, loot corpses, chests and game objects including group rolls, and run the loot and object checks live.
* [Vendors and gossip](player-actions/vendors-and-gossip.md) - Open a vendor, buy and sell items, and read and choose the options of an NPC's gossip menu.
* [Scripted actions with act](player-actions/act-steps.md) - Drive the character through a list of scripted steps - talk, buy, quest, loot, train, fly, mail and the rest - with forever act or the act method.
* [Mail and auction house](player-actions/mail-and-auction.md) - Read, take, return and send mail, and browse, buy and sell at the auction house.
* [Chat and channels](player-actions/chat.md) - Send say, yell, party, raid, guild and whisper lines, join and leave custom channels, and read the chat log.

# Dungeons

* [dungeons/](dungeons/) - Plan, check, run and watch a dungeon, and read why a run stopped.
* [Dungeon plans](dungeons/plans.md) - What a dungeon plan file is, every field it can carry, and what its status and forever_status mean.
* [Plan tools](dungeons/plan-tools.md) - Check, lint, probe, stage, export, scout and compare a dungeon plan offline, before it costs a run.
* [forever preflight](dungeons/preflight.md) - Check the serve slot, lease, plan, export, module, SOAP console and bots before a Forever dungeon run starts.
* [forever dungeon-play](dungeons/dungeon-play.md) - Play one dungeon plan as a real character leading four bots, and read the run's record and its pass or fail verdict.
* [forever dungeon-watch](dungeons/dungeon-watch.md) - Watch a dungeon run that a server module leads by itself, keep its safety budgets, rescue a wipe and record the run.
* [Stop reasons and verdicts](dungeons/stop-reasons.md) - Every reason a Forever dungeon run, watch or walk can end with, and the verdict each kind of report carries.

# Bots and raids

* [bots-and-raids/](bots-and-raids/) - Order the server-side bots, watch a raid, keep a fleet online and read a bot dashboard.
* [Ordering bots](bots-and-raids/bot-control.md) - How the agent talks to a bot module, which commands it sends, which of your bots it may order, and the live checks that drive the module end to end.
* [The add-on protocol](bots-and-raids/addon-protocol.md) - How the agent exchanges add-on messages with a server's bot module and how to check that exchange.
* [forever raid-watch](bots-and-raids/raid-watch.md) - Watch a raid the server's own bot module leads, from outside, with budgets and a raid summary at the end.
* [forever raid-autopilot](bots-and-raids/raid-autopilot.md) - Keep the Forever raids of a walk folder running and restarting without a person watching each one.
* [Battlegrounds](bots-and-raids/battlegrounds.md) - Queue a character for Warsong Gulch or Arathi Basin with the scripted act steps, or watch a whole match and read its report card.
* [forever fleet](bots-and-raids/fleet.md) - Keep several Forever daemons online at once, one per account, from a single supervisor.
* [Watching bots on a dashboard](bots-and-raids/dashboard.md) - Read a server's bot roster dashboard, use the agent's live page with its Positions panel, and see why replaying a recording is Turtle-only.

# Testing

* [testing/](testing/) - Judge a run: trials, scenarios, bug hunting, scoring, report cards and what a run writes.
* [How a run is judged](testing/overview.md) - The four ways the agent turns a recording or a test run into a verdict, and which one answers which question.
* [Trials](testing/trials.md) - Run the behaviour test bench: hire a bot into a scripted fight, score each specified behaviour, and read the card.
* [Scenarios](testing/scenarios.md) - Write, check and run scenario files: scripted live tests that drive one or more agent sessions and check what came back.
* [Report cards](testing/report-cards.md) - Read the two report cards: the trials card for a behaviour run and the dungeon-run card with its verdict, legs and flags.
* [forever spell-score](testing/spell-score.md) - Judge a Forever class fight from its watch combat log: each party member's casts against the oracle of its class, failing only wrong-class abilities.
* [Traces and records](testing/traces-and-records.md) - What a run writes (trace, summary, combat log, run record, trials and scenario folders) and how trace-score re-reads a trace.

# Developers

* [developers/](developers/) - The architecture, the package map, the contracts, and how to add a command, a method or a test.
* [Architecture](developers/architecture.md) - The layers forever-agent is built from, from a command line down to the world socket, and which package holds each layer.
* [Package map](developers/package-map.md) - Every Go package in forever-agent with one line saying what it is for.
* [Contracts](developers/contracts.md) - The versioned agreements between forever-agent and a bot module, where they live, and how each side proves it speaks them.
* [Adding a command](developers/adding-a-command.md) - How to add a top-level or subcommand to the forever-agent CLI, with the help line, the scope, the exit codes and the tests it must pass.
* [Adding a daemon method](developers/adding-a-daemon-method.md) - How to add a method to the forever serve daemon, from the registration line to the test and the JSON-RPC call that reaches it.
* [Contributing](developers/contributing.md) - The rules and checks a change to forever-agent must pass before it is merged.
* [Privacy and captures](developers/privacy-and-captures.md) - What must never be committed or shared from a run, what the packet captures contain, and how to check and scrub one.

# History

* [Update log](log.md) - When each area of the manual was written and what changed since.

# The old Turtle stack

* [turtle/](turtle/) - The pages for the 1.12 Turtle stack the repository still carries, and how they relate to the Forever manual.
* [Where the old bundle went](developers/old-bundle-map.md) - Every page of the former `docs/okf` bundle, its class, and the page that now holds it.
