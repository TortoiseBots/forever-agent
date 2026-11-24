# Directory Update Log

## 2026-11-24

* **Creation**: Established the [getting-started](getting-started/) area: what the agent is, how to build it, a first login, settings and slots, the safety and GM guard rules, the glossary and troubleshooting.
* **Creation**: Established the [daemon](daemon/) area: `forever serve`, RPC basics, the world reads, movement and travel, the method index and the events and logs.
* **Creation**: Established the [player-actions](player-actions/) area: spells and combat, items, vendors, quests, loot, trade, mail, auction, trainers, flight paths, chat, groups, pets and the `act` step runner.
* **Creation**: Established the [dungeons](dungeons/) area: plans, the plan and route tools, the navmesh and map files, preflight, `dungeon-play`, `dungeon-run`, `dungeon-watch` and the stop reasons.
* **Creation**: Established the [bots-and-raids](bots-and-raids/) area: ordering bots, the add-on protocol, `raid-watch`, `raid-autopilot`, battlegrounds, fleets and the bot dashboard.
* **Creation**: Established the [testing](testing/) area: how a run is judged, trials, scenarios, the bug hunter, the spell and fight scores, the report cards and the files a run writes.
* **Creation**: Established the [developers](developers/) area: architecture, the package map, the contracts, the wire and packet rules, adding a command or a daemon method, privacy and captures, and contributing.
* **Creation**: Wrote the bundle root [index](index.md) and this log, and added `tools/verify_okf.py`, the checker that keeps the frontmatter, the indexes, the links and the privacy rules of every page in shape.
* **Update**: Replaced the long repository README with a short front page; the reference material it carried now lives in the area pages it belongs to.
* **Update**: Corrected command examples, flags, method parameters and quoted messages against the code; the checker now requires an index in every directory, rejects non-Markdown files, catches quoted secret values and rejects an unknown `forever-agent` command in a fenced example.
* **Creation**: Established the [Turtle section](turtle/index.md): the 72 pages of the old stack (the client fight brain and rotations, the old dungeon driver, LFT, Eluna, the add-on host, the raid walk and the old capture tools) now live under `turtle/`, each starting with the line that its commands refuse a ForeverCore server.
* **Deprecation**: Folded the former `docs/okf` bundle (167 pages, written for the old Turtle agent) into this manual and removed it. Every page is classified in [Where the old bundle went](developers/old-bundle-map.md): 72 moved to `turtle/`, 7 were rewritten as Forever pages, 75 were already covered here, and 13 described the removed bundle itself.
* **Creation**: Added the Forever pages the old bundle held alone: death and the corpse run, the aggro estimate, world exports, the run leader rows, plan-tool troubleshooting, the dungeon notes and the dashboard route editor.
* **Update**: After #225 merged, carried its facts into the manual: a Forever slot's settings file (`forever serve --settings`) with its keys, private-file rule and password precedence; the slot lease a serve holds and renews (15-minute TTL, 5-minute renew, 1-minute retry), its `--force` and the `trials run` interaction; and exit code 13 when the lease goes to another holder (also 15 when a lease refuses the start).
