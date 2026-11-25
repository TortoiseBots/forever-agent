# Bots

* [Ordering bots](bot-control.md) - How the agent talks to a bot module, which commands it sends, which of your bots it may order, and the live checks that drive the module end to end.
* [The add-on protocol](addon-protocol.md) - How the agent exchanges add-on messages with a server's bot module and how to check that exchange.
* [Run an add-on headless](addon-host.md) - Run a real add-on's Lua on a forever serve daemon with addon_start, read what it did with addon_state, tick a manual clock with addon_tick, and stop it with addon_stop.

# Raids

* [forever raid-watch](raid-watch.md) - Watch a raid the server's own bot module leads, from outside, with budgets and a raid summary at the end.
* [forever raid-autopilot](raid-autopilot.md) - Keep the Forever raids of a walk folder running and restarting without a person watching each one.
* [raid-suite --forever](raid-suite.md) - Walk a suite of Forever raid plans with forever raid-watch, each on its own leased slot, and judge each walk against its history.

# Battlegrounds and fleets

* [Battlegrounds](battlegrounds.md) - Queue a character for Warsong Gulch or Arathi Basin with the scripted act steps, or watch a whole match and read its report card.
* [fleet](fleet.md) - Keep several Forever daemons online at once, one per account, from a single supervisor.

# Dashboards

* [Watch the agent in 3D](3d-view.md) - Open the dashboard's 3D view, follow a running serve or replay a recorded run, understand the markers and the camera, and know what stage 1 does not draw yet.
* [Watching bots on a dashboard](dashboard.md) - Read a server's bot roster dashboard, use the agent's live page with its Positions panel, and see why replaying a recording is Turtle-only.
* [Editing a route on the dashboard](dashboard-route-editing.md) - Turn on the dashboard's route editor, save a navmesh-checked version of a plan's route, and move that version into the repository.
