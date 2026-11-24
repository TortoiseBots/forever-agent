# Forever act runner, round R0 notes

Scope: one scriptable driver for every world action (`forever act`), so the trials bench, the bug hunter and a
later RPC adapter share the same step lines. This round covers the step runner itself, finding an NPC by entry,
walking within talk range, and opening its menu; later rounds add their own verbs. The gossip menu parse moved from
`internal/forever/death` to `internal/forever/npc`, so one parser serves death, vendors, trainers, taxi and quests.

Code: `internal/forever/npc` (the NPC wire layer: talk, quest hello, select, close, and the menu, complete,
option-interaction and interaction-open-result parsers), `internal/forever/world` (`act.go` step runner, `npc.go`
dispatch, `ActStep` in `Options`, the `enter` branch), `cmd/forever-agent` (`forever act`).

Everything below was checked in ForeverCore at 25fc7ca. No packet capture was read for this round (the plan needs
none); the tests use hand-made fixtures.

## What opens an NPC

Classic opens vendors and trainers only through a gossip option (Player.cpp:15160-15181), so the runner must send
what the NPC's flags say: NpcFlags GOSSIP 0x1 means `CMSG_TALK_TO_GOSSIP` (Hello::Read, NPCPackets.cpp:94-97); a
quest giver without the gossip flag answers only `CMSG_QUEST_GIVER_HELLO` (QuestGiverHello::Read,
QuestPackets.cpp:69-72). The failing case pins the second: a quest-only NPC (flags 0x2) must get the hello, and a
runner that always sends TALK_TO_GOSSIP fails it. Both sends are select-then-talk, like the spirit healer flow.

## The new packets

* `CMSG_QUEST_GIVER_HELLO` (Opcodes.h:809): the giver's packed GUID, like `CMSG_TALK_TO_GOSSIP`.
* `SMSG_GOSSIP_COMPLETE` (NPCPackets.cpp:218-222): one suppress-sound bit, flushed.
* `SMSG_GOSSIP_OPTION_NPC_INTERACTION` (NPCPackets.cpp:205-213): packed GUID, int32 option NPC id, the faction
  presence bit (flushed), then the optional int32.
* `SMSG_NPC_INTERACTION_OPEN_RESULT` (NPCPackets.cpp:99-107): packed NPC GUID, int32 interaction type
  (PlayerInteractionType, DBCEnums.h), the success bit, flushed.
* `GossipOptionNpc` (GossipDef.h:37-82): None 0, Vendor 1, Taxinode 2, Trainer 3, SpiritHealer 4, Binder 5,
  Banker 6, Petition 7, GuildTabard 8, Battlemaster 9, Auctioneer 10, TalentMaster 11, Stablemaster 12,
  GuildBanker 14, Spellclick 15, Mailbox 18.

## The steps

R0 verbs: `wait dur=` (refused when longer than what is left of the run deadline), `gm line=` (through the existing
command channel and its GM guard; only a clear success passes, so a refused, unknown-command, syntax, no-reply,
incomplete or echoed result fails the step), `goto x= y= z=` and `npc entry=` (nearest unit of that entry in the
world model, walked within 4 yd, INTERACTION_DISTANCE 5.0 minus a step, ObjectDefines.h:24): both walk on the
navmesh when `--navmesh` is set and are refused without it unless the step carries `straight=1` (a straight line at
an invented height is the repo's no-go, so it is an explicit choice), `open` (talk or quest hello by flags, prints
the menu or the quest list), `select option=`, `close` (`CMSG_CLOSE_INTERACTION`). Each step prints one line:
`step N verb ok|fail reason`. Every refused step gets its `fail` line, whichever verb it is: the step runner adds it
for a verb that returns its refusal without printing one (#236 point 4: the item verbs, split and destroy among them,
used to answer `ok:false` with no line at all), and a verb that prints its own (wait, gm, goto) keeps one. `--steps
FILE` reads lines; `--step 'verb k=v'` repeats on the command line.

Step syntax: a value is one word, or double-quoted (`note="a b c"`, `\"` and `\\` escapes) when it has spaces;
`line=` takes the whole rest of the step, so `gm line=.additem 6948 1` works.

Open and select: the stored answer is cleared before each open and each select, and only an answer from the current
NPC (matching GUID) counts. A quest-only NPC answers `CMSG_QUEST_GIVER_HELLO` with
`SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE` (0x65001A, QuestPackets.cpp:619-635: packed giver GUID, two uint32 emote
words, uint32 count, that many ClientGossipText, 11-bit greeting length flushed, greeting bytes), which is the open
result. After a `select` the runner waits one second for a submenu from the same NPC and adopts it, so the next
select checks its options; with no submenu the menu is spent. The shared gossip dispatch also hands every menu to
the spirit healer's wait (`death.gossip`), which the first cut had cut off.

The interaction result, complete and option-interaction packets are parsed and counted (Report.ParseErrors on a
short read), but no step needs them yet.
