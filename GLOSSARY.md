# Forever bots

The bot work on ForeverCore: the bot modules that run inside the server and forever-agent, the client tool both
teams use to watch, test and play alongside the bots.

## Bot systems

**TortoiseBots module**:
The playerbot module ported from TortoiseBots to ForeverCore.
_Avoid_: our bots, the port (when you mean the module itself)

**ForeverCore playerbots**:
The bot system that ships with ForeverCore, built by the core's author.
_Avoid_: Karrz's bots, V2, PlayerbotV2

**Merged bot module**:
The single bot module the TortoiseBots module and ForeverCore playerbots are planned to become.
_Avoid_: the merge, the new bots

## The agent

**forever-agent**:
The headless game client both teams use to watch bots, test them and play next to them.
_Avoid_: the client, TA (that is the older Turtle tool)

**Contract**:
A versioned agreement, owned by forever-agent, on how the agent and a bot system talk (messages, replies, status
rows), with golden examples either side can test against.
_Avoid_: protocol spec, interface, format

**Observer run**:
A run where the bots play on their own and the agent only watches and scores them.
_Avoid_: watch run, passive run

**Player run**:
A run where the agent plays a character in the group as a real player would.
_Avoid_: agent-led run (when the agent is a group member)

## Owning and leading bots

**Owner**:
The Battle.net account a bot's character belongs to; any of its game accounts counts.
_Avoid_: master (a master is who a bot follows right now), account (say which one)

**Owned bot**:
A character of the owner's own, logged in as a bot by its owner while the owner is in the world; the owner may give
it every order.
_Avoid_: alt-bot, companion, hire

**Led bot**:
A bot in a player's group that the player does not own; it takes the group leader's group orders (follow, stay,
attack, flee) while the group lasts, and nothing more.
_Avoid_: borrowed bot, pool companion

## Play

**Clear**:
Every boss of a dungeon killed by the group. A clear is either bots-only (an observer run) or with a player in the
group (a real person or the agent in a player run).
_Avoid_: finish, complete, run (for the result)

**Spec**:
The talent build a bot plays: the one its talents point to, or the one its owner orders; below level 10 the class's
levelling spec.
_Avoid_: role, build, kit (a kit is the set of actions a spec uses)

**Combat items**:
Consumables a bot uses in a fight: health and mana potions, healthstones, bandages, poisons, sharpening stones and
oils, soulstones.
_Avoid_: consumables (too wide), buffs
