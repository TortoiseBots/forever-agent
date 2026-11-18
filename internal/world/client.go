package world

import (
	"context"
	"time"
)

// Client is the session as the upper layers use it: cmd/forever-agent and
// internal/mapview call exactly these methods of *session.Session today, with
// these signatures. internal/session asserts that *Session implements it.
//
// A method whose signature would name a type this package cannot import is left
// out for now, so a holder that needs one keeps *session.Session: Log
// (*events.Log: events imports world for its GUIDs), Logout (session.LogoutOutcome),
// SpellState (*spellstate.State: spellstate imports world), CastState
// (session.CastStateInfo embeds spellstate.CastState), PathTo (session.PathInfo
// embeds navmesh.Path) and WalkPath (session.WalkReport).
type Client interface {

	// Lifecycle: the login and logout flow, the readiness signals and the session's counters.
	// Run drives the character from the character list to logout.
	Run(ctx context.Context) error
	// Ready closes once SMSG_LOGIN_VERIFY_WORLD arrives.
	Ready() <-chan struct{}
	// Done closes when Run returns.
	Done() <-chan struct{}
	// Redact scrubs the session's secrets (Options.Redact, Options.RedactAnywhere) out of text.
	Redact(text string) string
	// State is where the session is in the login and world flow.
	State() Status
	// Status is a snapshot of the player: position, health, power, target, its spell state and the session's counters.
	Status() StatusInfo

	// World view: what the server has told the session about the world around the player.
	// Units snapshots the units around the player (not the player itself), nearest first, with names and party membership filled in.
	Units(f Filter) []Unit
	// Unit is one unit, as Units gives it, by guid (the player itself too) or by name: the nearest living unit whose name contains it, else the nearest such corpse; the player's own name gives the player, at the session's own position (status x/y/z).
	Unit(g GUID, name string) (Unit, error)
	// GameObjects snapshots the game objects around the player, nearest first, with names filled in from the query responses.
	GameObjects(f GOFilter) []GameObject
	// DynamicObjects lists the ground areas in view (Blizzard, fire patches), nearest first.
	DynamicObjects() []DynamicObject
	// Team is the player's side, from its race ("" while the race is unknown).
	Team() string
	// RaceOf is a unit's race id (UNIT_FIELD_BYTES_0, low byte), 0 while the unit is not in the object store: a hired bot's race, which the perception feed does not carry.
	RaceOf(g GUID) uint8

	// Movement: walking, facing, teleports and the navmesh queries.
	// WalkTo walks in a straight line to a point at most maxWalk yards away and returns on arrival.
	WalkTo(ctx context.Context, x float32, y float32, z float32) error
	// Face turns the player towards a known unit (MSG_MOVE_SET_FACING).
	Face(ctx context.Context, g GUID) error
	// Teleport sends ".go xyz x y z [map]" (Commands.cpp:8347-8367) and waits up to 10 s for the server's teleport (MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD).
	Teleport(ctx context.Context, x float32, y float32, z float32, mapID int) error
	// LineClear tells whether the straight line between two points of the player's map stays on the navmesh (see navmesh.Planner.Raycast: the agent's stand-in for line of sight, and for a straight walk being possible).
	LineClear(a [3]float32, b [3]float32) (clear bool, known bool)
	// SnapToMesh is the walkable point of the navmesh nearest to p (within a few yards); false when there is none.
	SnapToMesh(p [3]float32) ([3]float32, bool)

	// Combat and casting: selection, melee, standing and the player's own spells.
	// Target selects a unit by guid, or by name, and returns it.
	Target(ctx context.Context, g GUID, name string) (Unit, error)
	// SelectConfirmed selects the unit g (0: none) and waits, up to wait in all, until the server is known to have it in force (SelectionConfirmed).
	SelectConfirmed(ctx context.Context, g GUID, wait time.Duration) error
	// Attack starts the auto-attack on the selected unit (CMSG_ATTACKSWING); no selection is an error.
	Attack(ctx context.Context) error
	// StopAttack stops the auto-attack (CMSG_ATTACKSTOP).
	StopAttack(ctx context.Context) error
	// Stand stands the player up (CMSG_STANDSTATECHANGE, state 0), as a real client does when a sitting player clicks or casts: the core refuses a cast from a seated player (SPELL_FAILED_NOT_STANDING) and does not stand the player by itself.
	Stand(ctx context.Context) error
	// CancelAutoRepeat stops the player's auto-repeat spell (Auto Shot, Shoot): CMSG_CANCEL_AUTO_REPEAT_SPELL, an empty body, which the core answers with InterruptSpell(CURRENT_AUTOREPEAT_SPELL) (SpellHandler.cpp HandleCancelAutoRepeatSpellOpcode).
	CancelAutoRepeat(ctx context.Context) error
	// Cast casts spell on target, or on the current target when target is 0, or on the player when nothing is selected.
	Cast(ctx context.Context, spell uint32, target GUID) error
	// CancelCast cancels the cast, channel or auto-repeat of spell, or of any spell when spell is 0 (CMSG_CANCEL_CAST, SpellHandler.cpp:411-426, Object.cpp:5428-5440).
	CancelCast(ctx context.Context, spell uint32) error
	// CancelAura removes one of the player's own auras, as a right-click on the buff does (CMSG_CANCEL_AURA, SpellHandler.cpp HandleCancelAuraOpcode): a druid leaves its shapeshift form with it, a mount aura is the same packet (Dismount).
	CancelAura(ctx context.Context, spell uint32) error
	// CastOnMainHand casts an enchanting spell (a shaman's weapon imbue) at the weapon in the main hand: CMSG_CAST_SPELL with TARGET_FLAG_ITEM and the weapon's guid, as the client sends it for a spell that needs an item.
	CastOnMainHand(ctx context.Context, spell uint32) error
	// CastOnTradeSlot casts spell at the partner's item in trade slot slot (CMSG_CAST_SPELL with TARGET_FLAG_TRADE_ITEM, see game.BuildCastSpellOnTradeItem): an enchant for the item the partner put in slot 6, which the core applies when the trade completes.
	CastOnTradeSlot(ctx context.Context, spell uint32, slot uint8) error
	// Spells lists the player's known spells with their metadata, cooldowns and whether each can be cast now, sorted by name and rank.
	Spells(f SpellsFilter) ([]SpellRow, error)

	// Chat and addon messages: the chat lines and the addon channel.
	// Chat sends one chat line; typ is a sendable chat type name ("say", "party", "whisper", …).
	Chat(ctx context.Context, typ string, text string, to string) error
	// JoinChannel joins a chat channel (CMSG_JOIN_CHANNEL); the server answers with SMSG_CHANNEL_NOTIFY, logged as channel_notify events.
	JoinChannel(ctx context.Context, name string, password string) error
	// LeaveChannel leaves a chat channel (CMSG_LEAVE_CHANNEL).
	LeaveChannel(ctx context.Context, name string) error
	// AddonSend sends prefix and text as an addon message on channel (party, raid, guild, officer, raid_leader, raid_warning, battleground, battleground_leader, or channel with to naming the chat channel).
	AddonSend(ctx context.Context, channel string, prefix string, text string, to string) error

	// GM text commands: the server's dot commands, sent as chat; only test tooling uses them.
	// GM sends a GM or .bot command as say chat, adding the leading "." when it is missing.
	GM(ctx context.Context, text string) error
	// GMOn selects the unit g, waits until the server is known to have it in force (SelectConfirmed), and sends the GM command text with GMOnConfirmed: only while that confirmed selection still stands, with no selection packet of its own.
	GMOn(ctx context.Context, g GUID, text string) error
	// GMOnConfirmed sends the GM command text, and nothing else, as ONE operation of the session's owner goroutine, if the unit g is the selection the caller confirmed: the session believes g is selected (no request selected another unit since) AND the server is known to have g in force (SelectionConfirmed: an echo that cannot answer an older select, selection.go).
	GMOnConfirmed(ctx context.Context, g GUID, text string) error
	// GMKill selects the creature g, waits until the server shows the selection (SelectConfirmed) and sends ".die" while that confirmed selection still stands (GMOn).
	GMKill(ctx context.Context, g GUID) error
	// GMAura selects the PLAYER g, waits until the server shows that selection (SelectConfirmed) and sends ".aura <spell>" while the confirmed selection still stands (GMOn).
	GMAura(ctx context.Context, g GUID, spell uint32) error
	// GMEvade sends `.npc evade` on the selected creature g through GMOn.
	GMEvade(ctx context.Context, g GUID) error
	// GMSetHP restores the creature g to its OWN max health through GMOn.
	GMSetHP(ctx context.Context, g GUID) error
	// GMRespawn respawns the creature g through GMOn, only after the client shows it dead: an alive unit, or one the client no longer holds, is refused (the unselected `.respawn` would respawn every dead creature in range).
	GMRespawn(ctx context.Context, g GUID) error
	// GMRevive resurrects the named character through the plain GM path (no selection): `.revive <name>`.
	GMRevive(ctx context.Context, name string) error

	// Group and raid: the group, its leadership and its raid icons.
	// Party returns a copy of the group; Members is never nil, so the RPC sends [].
	Party() PartyInfo
	// Invite invites a character to the group by name (CMSG_GROUP_INVITE).
	Invite(ctx context.Context, name string) error
	// AcceptInvite accepts the group invitation the server sent (CMSG_GROUP_ACCEPT); no pending invite is an error.
	AcceptInvite(ctx context.Context) error
	// LeaveGroup sends CMSG_GROUP_DISBAND, which removes the player from the group (GroupHandler.cpp:354-370).
	LeaveGroup(ctx context.Context) error
	// Promote makes a group member the leader (CMSG_GROUP_SET_LEADER, GroupHandler.cpp:334-352).
	Promote(ctx context.Context, g GUID, name string) error
	// Assistant sets or, with clear, takes away a raid member's assistant flag (CMSG_GROUP_ASSISTANT_LEADER).
	Assistant(ctx context.Context, g GUID, name string, clear bool) error
	// ConvertRaid turns the leader's party into a raid (CMSG_GROUP_RAID_CONVERT, GroupHandler.cpp:505-522).
	ConvertRaid(ctx context.Context) error
	// RaidMove puts a raid member in subgroup (1-8, as the raid frame numbers them).
	RaidMove(ctx context.Context, g GUID, name string, subgroup int, swapWith string) error
	// Disband ends the group for everyone.
	Disband(ctx context.Context) error
	// SetLootMethod sets the group's loot method by name (game.LootMethodNames), its master looter (master_loot only: a member's guid or name; the player when both are empty) and its threshold (an item quality 1-5; 0 keeps the group's).
	SetLootMethod(ctx context.Context, method string, master GUID, masterName string, threshold uint8) error
	// Mark sets a raid icon (skull, cross, square, moon, triangle, diamond, circle, star) on a unit given by guid or name, or with icon "clear" takes the unit's icon off, as the 1.12 client's SetRaidTarget does.
	Mark(ctx context.Context, g GUID, name string, icon string) (MarkResult, error)

	// Loot: windows, taking, rolls and objects opened for their loot.
	// Roll votes need, greed or pass on an open group-loot roll with CMSG_LOOT_ROLL, as the roll frame's buttons do.
	Roll(ctx context.Context, ref RollRef, choice string) (RollInfo, error)
	// LootWindow is the window now open, nil when there is none.
	LootWindow(ctx context.Context) (*LootWindow, error)
	// OpenLoot opens the loot of a dead creature (CMSG_LOOT), given by guid or by name (the nearest dead lootable unit whose name contains it), and waits up to wait for the server's window.
	OpenLoot(ctx context.Context, g GUID, name string, wait time.Duration) (*LootWindow, error)
	// TakeLoot takes items out of the open window with CMSG_AUTOSTORE_LOOT_ITEM and the money with CMSG_LOOT_MONEY, then waits up to wait for the server to remove them.
	TakeLoot(ctx context.Context, take LootTake, wait time.Duration) (LootTaken, error)
	// CloseLoot closes the open window (CMSG_LOOT_RELEASE) and waits up to wait for the server's SMSG_LOOT_RELEASE_RESPONSE.
	CloseLoot(ctx context.Context, wait time.Duration) error
	// OpenObject opens a chest or a node the way the client does: it casts an Opening spell at it (CMSG_CAST_SPELL with TARGET_FLAG_OBJECT, game.BuildCastSpellOnObject) and waits up to wait for the server's loot window (SMSG_LOOT_RESPONSE).
	OpenObject(ctx context.Context, g GUID, spell uint32, wait time.Duration) (*LootWindow, error)
	// LootObject opens the object (OpenObject), takes everything the player may take (TakeLoot) and closes the window.
	LootObject(ctx context.Context, g GUID, spell uint32, openWait time.Duration, takeWait time.Duration) (LootedObject, error)
	// LootCorpse opens the corpse (OpenLoot), takes everything the player may take and closes the window.
	LootCorpse(ctx context.Context, g GUID, wait time.Duration) (LootedObject, error)

	// Gossip and NPCs: NPC menus, vendors, trainers and flight masters.
	// UseObject uses a game object the way a right-click does (CMSG_GAMEOBJ_USE) and returns it as the store holds it.
	UseObject(ctx context.Context, g GUID) (GameObject, error)
	// GossipHello talks to an NPC (CMSG_GOSSIP_HELLO).
	GossipHello(ctx context.Context, npc GUID) error
	// GossipSelect picks an option of the open menu (CMSG_GOSSIP_SELECT_OPTION).
	GossipSelect(ctx context.Context, npc GUID, option uint32, code string) error
	// ActivateTaxi asks the flight master npc for a flight along nodes (source first): two nodes send CMSG_ACTIVATETAXI, more send CMSG_ACTIVATETAXIEXPRESS, as the client does.
	ActivateTaxi(ctx context.Context, npc GUID, nodes []uint32) error
	// VendorList asks a vendor for its list (CMSG_LIST_INVENTORY).
	VendorList(ctx context.Context, vendor GUID) error
	// Buy buys an item from a vendor (CMSG_BUY_ITEM, or CMSG_BUY_ITEM_IN_SLOT with the Bag and Slot of its BuyRef); the result is a vendor_buy or vendor_buy_failed event.
	Buy(ctx context.Context, vendor GUID, ref BuyRef) error
	// Sell sells an item to a vendor (CMSG_SELL_ITEM) and returns it as it was.
	Sell(ctx context.Context, vendor GUID, ref EquipRef, count uint32) (Item, error)
	// TrainerList asks a trainer for its list (CMSG_TRAINER_LIST).
	TrainerList(ctx context.Context, trainer GUID) error
	// Train buys a spell from a trainer (CMSG_TRAINER_BUY_SPELL).
	Train(ctx context.Context, trainer GUID, spell uint32) error

	// Quests: the quest log and the quest giver's menu.
	// QuestLog lists the quests in the character's log, from its own update fields.
	QuestLog() []QuestSlot
	// QuestHello talks to a quest giver (CMSG_QUESTGIVER_HELLO).
	QuestHello(ctx context.Context, npc GUID) error
	// QuestQuery asks the NPC for a quest's panel (CMSG_QUESTGIVER_QUERY_QUEST): a quest_details event.
	QuestQuery(ctx context.Context, npc GUID, quest uint32) error
	// QuestAccept accepts a quest from an NPC (CMSG_QUESTGIVER_ACCEPT_QUEST).
	QuestAccept(ctx context.Context, npc GUID, quest uint32) error
	// QuestComplete starts handing in a quest the character is on (CMSG_QUESTGIVER_COMPLETE_QUEST, what clicking the quest in the NPC's list sends).
	QuestComplete(ctx context.Context, npc GUID, quest uint32) error
	// QuestReward takes the reward of a finished quest (CMSG_QUESTGIVER_CHOOSE_REWARD).
	QuestReward(ctx context.Context, npc GUID, quest uint32, choice *uint32) error
	// QuestAbandon drops a quest from the log (CMSG_QUESTLOG_REMOVE_QUEST).
	QuestAbandon(ctx context.Context, quest uint32) error

	// Items and queries: the bags, the worn gear and item prototypes.
	// Items lists every item in the backpack and the bags, backpack first.
	Items() []Item
	// Inventory lists the backpack (bag 255, slots 23-38) from the player's own update fields.
	Inventory() []Item
	// Equipment lists the worn items (inventory slots 0-18), occupied slots only, from the player's own update fields.
	Equipment() []Item
	// MainHandItem is the weapon in the main hand, with its temporary enchantment, false when the hand is empty.
	MainHandItem() (Item, bool)
	// Equip equips an item with CMSG_AUTOEQUIP_ITEM, as a right-click in the bags does.
	Equip(ctx context.Context, ref EquipRef) (Item, error)
	// UseItem uses an item from the bags with CMSG_USE_ITEM, as a right-click does: food, drink, potions, scrolls, a mount item.
	UseItem(ctx context.Context, ref EquipRef, target GUID) (Item, error)
	// Mount mounts with an item (CMSG_USE_ITEM) or a spell (CMSG_CAST_SPELL on the player) and waits up to wait for the mount display.
	Mount(ctx context.Context, ref MountRef, wait time.Duration) (MountInfo, error)
	// Dismount cancels the mount aura with CMSG_CANCEL_AURA (1.12 has no separate dismount packet) and waits up to wait for the display to clear.
	Dismount(ctx context.Context, spell uint32, wait time.Duration) (MountInfo, error)
	// ItemInfos returns the prototypes of the entries.
	ItemInfos(ctx context.Context, entries []uint32, wait time.Duration) (map[uint32]ItemInfo, error)

	// Trade: the one-shot and step-by-step trade windows.
	// Trade hands items and gold to another player in trade range and reports the outcome.
	Trade(ctx context.Context, req TradeRequest) (TradeResult, error)
	// TradeOpen starts a step trade with the player to (a guid) or name (a player in view) and returns once the window is open, which it leaves open.
	TradeOpen(ctx context.Context, to GUID, name string, openWait time.Duration) (TradeStepState, error)
	// TradeExpect arms the accept of one incoming trade request, from the player from (a guid) or name, for wait (0: 15 s), and returns once the window is open.
	TradeExpect(ctx context.Context, from GUID, name string, wait time.Duration) (TradeStepState, error)
	// TradeSetItem puts an item of the bags into trade slot tradeSlot (0-6; slot 6 is the one that is not traded, where an enchant is cast at the partner's item) with CMSG_SET_TRADE_ITEM.
	TradeSetItem(ctx context.Context, tradeSlot uint8, ref EquipRef) (TradeSetResult, error)
	// TradeAccept accepts the open step trade after Options.TradeSettle and waits up to wait (0: 12 s) for the partner's accept to complete it.
	TradeAccept(ctx context.Context, wait time.Duration) (TradeResult, error)
	// TradeCancel closes a step trade (CMSG_CANCEL_TRADE, then up to a second for the core's closing status), drops an armed accept and returns the last status by name.
	TradeCancel(ctx context.Context) (string, error)
	// TradeStatus is a snapshot of the trade; it never fails (a session that is not running shows no trade).
	TradeStatus() TradeStepState

	// Pets: the player's pet and another player's.
	// Pet is the state of the player's pet: Present is false when it has none.
	Pet() PetInfo
	// PetAction sends an order to the pet and applies it to the copy of the pet's state; it fails without a pet, with a dead pet, while the pet's bar is off, and for a spell that is not on the bar.
	PetAction(ctx context.Context, o PetOrder) error
	// PetOfPlayer is the pet another player has out, as the object store shows it: the unit its summon field names, else a pet-class creature in view that names it as its summoner or creator (objects.Store.PetOf).
	PetOfPlayer(owner GUID) (Unit, bool)

	// Death: dying, the corpse run and area triggers.
	// Death returns the player's death state and any open resurrection offer.
	Death() DeathInfo
	// AcceptResurrect answers the open resurrection offer with yes (CMSG_RESURRECT_RESPONSE).
	AcceptResurrect(ctx context.Context) (ResurrectRequest, error)
	// ReleaseSpirit sends CMSG_REPOP_REQUEST: the spirit leaves the body and the player is moved to the graveyard as a ghost.
	ReleaseSpirit(ctx context.Context) error
	// CorpseQuery asks where the corpse is (MSG_CORPSE_QUERY) and waits for the answer.
	CorpseQuery(ctx context.Context) (Corpse, error)
	// ReclaimCorpse sends CMSG_RECLAIM_CORPSE.
	ReclaimCorpse(ctx context.Context) error
	// AreaTrigger sends CMSG_AREATRIGGER for trigger id, as the client does when the player steps into its volume.
	AreaTrigger(ctx context.Context, id uint32) error
}
