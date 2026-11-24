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
