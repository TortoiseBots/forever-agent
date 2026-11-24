# The daemon

`forever serve` keeps one character in the world and answers JSON-RPC calls. These pages cover starting it,
calling it, what it can tell you, how it moves the character, and the two logs it keeps.

# Start here

* [forever serve](serve.md) - Run the agent as a session daemon so other programs can drive one logged-in character over a local JSON-RPC socket.
* [RPC basics](rpc-basics.md) - Call a forever serve daemon's methods with the rpc command or any HTTP client, and understand the request envelope, errors and timeouts.

# Reading the world

* [Status and world reads](status-and-world-reads.md) - Read the character's own state and the units, game objects, ground spells, item data and map picture around it.
* [Events and logs](events-and-logs.md) - Read the combat log, the session's ordered event stream and the chat log, with cursors and long-poll waits.

# Moving

* [Movement](movement.md) - Move the character with straight walks, navmesh routes and follow loops, and ask the navmesh what ground is walkable.
* [Teleports and travel](travel.md) - Move the character with the GM teleport, area triggers, mounts and server-flown taxi flights.

# Reference

* [Method index](method-index.md) - Every method a forever serve daemon registers, with its parameters, what it does and the page that explains it.
* [Methods not ported](not-ported-methods.md) - The older daemon's method names that a forever serve session registers but does not implement, and what to use instead.
