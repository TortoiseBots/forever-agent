# Forever trainer round, R4 notes

Scope: the trainer verbs of `forever act` (`trainer`, `learn spell=`), so a character standing next to the bots can
open a trainer and learn from it (W2 town visits from the player side). Trainers open only through the "Train me."
gossip option on this server (F1 of the plan); the build and parse layer follows that.

Code: `internal/forever/npc` (`trainer.go` builders and parsers), `internal/forever/world` (`act_trainer.go` steps,
`npc.go` dispatch, session state), `internal/forever/wire` (the five opcodes with round-trip rows).

Everything below was checked in ForeverCore at 25fc7ca. No packet capture was read for this round (the plan names
none); the tests use hand-made fixtures.

## What the server does

Class trainers open only through a gossip option (Player.cpp:15160-15181): the menu carries the Trainer row
(GossipOptionNpc 3), or the server adds "Train me." when the menu lacks it. Selecting it calls
`SendTrainerList` from the gossip select (Player.cpp:15315-15323), which opens the trainer interaction and sends
`SMSG_TRAINER_LIST` with the spells this character may see.

The buy (`CMSG_TRAINER_BUY_SPELL`, TrainerGUID, TrainerID of the open list, SpellID — NPCPackets.cpp:253-258) is
checked against the open interaction: a trainer without `UNIT_NPC_FLAG_TRAINER` answers only while the interaction
is open on its GUID (NPCHandler.cpp:130-158). Success learns the spell (Player::LearnSpell sends
`SMSG_LEARNED_SPELLS`, Player.cpp:3527-3541) and a refusal sends `SMSG_TRAINER_BUY_FAILED` with the reason
(Trainer::FailReason, Trainer.h:47-50: 0 unavailable, 1 not enough money).

## The new packets

* `CMSG_TRAINER_LIST` (Opcodes.h:1020): the trainer's packed GUID (Hello::Read, NPCPackets.cpp:94-97), the same
  shape as the talk — included for the opcode round trip, never sent (the list opens from the gossip select).
* `CMSG_TRAINER_BUY_SPELL` (Opcodes.h:1019): the trainer's packed GUID, the int32 trainer id, the int32 spell id.
* `SMSG_TRAINER_LIST` (NPCPackets.cpp:166-194): the trainer's packed GUID, the int8 trainer kind, the int32 trainer
  id, the uint32 spell count, each spell as int32 SpellID, int32 0, uint32 MoneyCost, u8 ReqLevel, uint32
  ReqSkillLine, uint32 ReqSkillRank and three int32 ReqAbility (33 bytes per spell, Classic layout), then the
  11-bit greeting length (flushed) and the greeting bytes.
* `SMSG_TRAINER_BUY_FAILED` (NPCPackets.cpp:260-266): the trainer's packed GUID, the int32 spell id, the int32
  failure reason.
* `SMSG_LEARNED_SPELLS` (SpellPackets.cpp:506-522): the uint32 spell count, the uint32 specialization id, the int32
  action bar slot, each spell as int32 SpellID, the favourite and three optional-link bits (flushed) and the
  present int32 links, then the suppress-messaging and trait bits (flushed).

## The steps

`trainer` (no keys) selects the menu's Trainer option and waits up to 5 s for the list; `learn spell=` sends the
buy with the list's trainer id and passes only when the `SMSG_LEARNED_SPELLS` naming the spell arrives within 3 s
(the plan's success rule). A buy failure fails the step with its reason; the menu is spent after the trainer
select, as after any select. Every learn also feeds `session.known` (and the report), like the login burst's known
spells.

The failing case pins the second half: a `SMSG_TRAINER_BUY_FAILED` for the spell fails `learn`, and the spell never
reaches `session.known` — a runner that passes on any learn packet fails it. The list's own failing case
(`TestTrainerListThirtyThreeBytesPerSpell`) reads two spells and pins the second id: a retail-layout reader ends the
first spell at the wrong offset and fails it.
