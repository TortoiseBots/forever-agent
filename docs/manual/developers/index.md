# Developers

For people reading or changing forever-agent: the layers and packages, the packet and contract
rules, and the practical steps for a new command or daemon method.

* [Architecture](architecture.md) - The layers forever-agent is built from, from a command line down to the world socket, and which package holds each layer.

# The code

* [Package map](package-map.md) - Every Go package in forever-agent with one line saying what it is for.
* [Wire and packets](wire-and-packets.md) - How a byte on the world socket becomes a typed message in the agent, where a parser belongs, and the rules every parser follows.
* [Contracts](contracts.md) - The versioned agreements between forever-agent and a bot module, where they live, and how each side proves it speaks them.
* [The 3D view data format](view3d-data-format.md) - The four dashboard routes that feed the 3D view, the scene and frame JSON they answer, the limits each route enforces, and how another dashboard embeds the viewer.

# How to change it

* [Adding a command](adding-a-command.md) - How to add a top-level or subcommand to the forever-agent CLI, with the help line, the scope, the exit codes and the tests it must pass.
* [Adding a daemon method](adding-a-daemon-method.md) - How to add a method to the forever serve daemon, from the registration line to the test and the JSON-RPC call that reaches it.

# Rules

* [Privacy and captures](privacy-and-captures.md) - What must never be committed or shared from a run, what the packet captures contain, and how to check and scrub one.
* [Contributing](contributing.md) - The rules and checks a change to forever-agent must pass before it is merged.

# History

* [Where the old bundle went](old-bundle-map.md) - Every page of the former docs/okf bundle, its class, and the page in this manual that now holds it.
