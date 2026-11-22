# A bot's owner is its Battle.net account, not its game account

The older Turtle bots let a player command only characters on the same game account. On ForeverCore one Battle.net
account holds several game accounts, players keep alts across them, and the core lets characters from different game
accounts of one Battle.net account be online together, so ownership is the Battle.net account: any character on any of
its game accounts can be an owned bot. Bots in your group that you do not own are led, not owned.

Owned bots are online only while their owner is in the world: they log out with the owner, and adding one needs the
owner in the world. The cap is four per Battle.net account (`Player.MaxBots`), counted over every game account.
Owned bots of one owner may share a game account, but only when that game account belongs to the owner's Battle.net
account and every bot already on it is theirs; pool bots and anyone else's bots keep the one-bot-per-game-account rule.
