# Forever agent #92 round D4: the quest methods

What this round adds: `quest_hello`, `quest_query`, `quest_accept`, `quest_complete`, `quest_reward`,
`quest_abandon` and `quest_log` in `serve_quest_methods.go`, on the method frame D2 built and the
answer type D3 defined (`ServeNPCResult`). Fake world only: no live run yet.

## The answer shape

Every method except `quest_log` answers `ServeNPCResult` (`since`, `ok`, `event`, `item`, `menu`,
`lines`; see D3). Parameters are TA's: `guid`, `quest_id`, `choice`, `wait_ms` (default 15000, at
most 60000). `quest_abandon` and `quest_log` take no `guid`.

A call that fails before any packet is an RPC error: a bad parameter, a unit that is not in the
world model, an NPC further than 4 yards (methods never walk), an NPC without the quest-giver
flag, a quest the log already holds (`quest_accept`), a full or stale log, a quest that is not in
the log (`quest_abandon`), a reward screen that is not open for this giver and quest, a `choice`
the screen did not offer, or no `choice` when it offers some. Once a packet went out the call
answers a result: `ok: false` with a `quest_error` event whose `text` is the act's (or the
server's) error. A cancel by the caller and a dead link stay errors. For the calls that change
state (`quest_accept`, `quest_reward`, `quest_abandon`) a `wait_ms` that ran out after the packet
is an error saying the outcome is unknown, with the event-log cursor the call started from: the
server may still complete it, and a retried "failure" would do it twice.

## The methods

| Method | What it does |
|---|---|
| `quest_hello {guid}` | The gossip hello flow (the act `open` step: quest hello for a quest-only NPC, gossip talk otherwise) with the quest-giver flag as the precondition. The event is the first `gossip` or `quest_list` row; a quest-only NPC has no handler event, so the method writes `quest_list {quests}`. `menu` is the open menu or quest list. |
| `quest_query {guid, quest_id}` | Clears the details, sends CMSG_QUEST_GIVER_QUERY_QUEST (respond-to-giver set) and waits for this quest's details: event `quest_details {quest_id, title, objectives: [{id, type, object_id, amount}]}`. A turn-in quest is answered by the request-items screen (QuestHandler.cpp:221-222): event `quest_request_items {quest_id, completable}`. The giver's refusal (SMSG_QUEST_GIVER_INVALID_QUEST) or silence for 5 s is `ok: false`, `quest_error`. Another quest's details do not answer it. |
| `quest_accept {guid, quest_id}` | The act `quest-accept` step. The server's success answer is the gossip closing and most refusals are silent, so the proof is the quest appearing in the character's own quest-log update fields; silence is `ok: false`, `quest_error` ("not confirmed"), never an accepted quest. Success: `quest_accepted {quest_id, slot}`. |
| `quest_complete {guid, quest_id}` | The act `quest-complete` step: the request-items screen, then (when it says the quest is ready, StatusFlags 0xFF) CMSG_QUEST_GIVER_REQUEST_REWARD and the reward screen. Success: `quest_offer_reward {quest_id, choices: [item entries], money}`. A quest that is not ready: `ok: false`, `quest_request_items {quest_id, completable: false}` (the text is the act's). Anything else: `quest_error`. |
| `quest_reward {guid, quest_id, choice?}` | The act `quest-reward` step. `choice` is TA's reward row index (0-5) or the item **entry** of one of the offered choices: an entry is never 0-5, so the two never collide. An index names a row of the screen (an empty row is refused by name); the call resolves it to the entry before the act runs, and the event names the entry. A choice the screen did not offer, an entry on two rows, or no choice when the screen offers some are refused before any packet. Success: `quest_rewarded {quest_id, choice_item?}`; `item` is the new inventory row when the reward arrived in a new stack. |
| `quest_abandon {quest_id}` | The act `quest-abandon` step over the shared state. It needs no NPC (quest_act.go reads no giver): the packet names the log slot, the server answers nothing, and the proof is the log showing the quest gone. Success: `quest_abandoned {quest_id, slot}`; a quest still in its slot after 5 s (QUEST_FLAGS_EX_NO_ABANDON_ONCE_BEGUN) is `quest_error`. |
| `quest_log {}` | `{quests: [{slot, quest_id, state_flags, complete, failed, progress}], stale?, stale_reason?}` from the character's own log, occupied slots in slot order. |

## Decisions

`complete` and `failed` are the two bits of the slot's StateFlags mask: QUEST_STATE_COMPLETE 0x1
and QUEST_STATE_FAIL 0x2 (QuestSlotStateMask, Player.h:689-694). An accepted quest carries
neither bit (Player::AddQuest sets only the quest id), so it reads `complete: false,
failed: false` while in progress. These are not the QuestStatus numbers `quest.StatusIncomplete`
(3) and the like: those name the DB status, and older fixtures in `quest_fake_test.go` still send 3
for a taken quest; this round's tests build the slot state in the mask's terms. `Log.Failed` (the
log's reaction to SMSG_QUEST_GIVER_QUEST_FAILED) now sets the fail bit instead of writing 5, which
the mask would read as complete.

`progress` has one counter per objective when the quest's details named its objectives, and runs
to the last non-zero counter otherwise (an objective list is not known for a quest taken before
this session).

`quest_accepted`, `quest_details`, `quest_offer_reward`, `quest_request_items`, `quest_rewarded`,
`quest_abandoned` and `quest_error` are written by the method (`methodEvent`): no packet handler
in this client emits them. They share the event log's seq axis, so `event_log` from `since` shows
them with the gossip rows.

## Not live-proven

Everything here is the handler-boundary fake. The live proof (L1) hands in one quest through the
methods alone (hello, accept, a GM `.quest complete`, complete, reward) and reads every answer.
