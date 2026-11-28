package world

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	shareSenderGUID = wire.GUID{Low: 0xAA01, High: 0x0800000000000000}
	sharePeer2GUID  = wire.GUID{Low: 0xAA02, High: 0x0800000000000000}
	sharePartyGUID  = wire.GUID{Low: 0xBB01, High: 0x0800000000000000}
)

func setupShareParty(t *testing.T, q *questWorld) {
	t.Helper()
	q.s.initChat()
	q.s.party.InParty = true
	q.s.party.RosterValid = true
	q.s.party.PartyGUID = sharePartyGUID
	q.s.party.SelfGUID = itemSelf
	q.s.party.LeaderGUID = itemSelf
	q.s.party.Order = []wire.GUID{itemSelf, shareSenderGUID, sharePeer2GUID}
	q.s.party.Members = map[wire.GUID]*party.Member{
		itemSelf:        {GUID: itemSelf, Name: "Self", Connected: true},
		shareSenderGUID: {GUID: shareSenderGUID, Name: "Mateone", Connected: true},
		sharePeer2GUID:  {GUID: sharePeer2GUID, Name: "Matetwo", Connected: true},
	}
	q.s.world.SetName(shareSenderGUID, "Mateone")
	q.s.world.SetName(sharePeer2GUID, "Matetwo")
}

func questPushResultPayload(sender wire.GUID, result uint32, title string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, sender)
	b.WriteU32(result)
	b.WriteBits(uint64(len(title)), 9)
	b.FlushBits()
	b.WriteString(title)
	return b.Bytes()
}

func questConfirmAcceptPayload(questID uint32, initiatedBy wire.GUID, title string) []byte {
	b := wire.NewWriter()
	b.WriteU32(questID)
	wire.WritePackedGUID(b, initiatedBy)
	b.WriteBits(uint64(len(title)), 10)
	b.FlushBits()
	b.WriteString(title)
	return b.Bytes()
}

func questDetailsSharedFrom(giver, informUnit wire.GUID, questID int32, title string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	wire.WritePackedGUID(b, informUnit)
	b.WriteI32(questID)
	b.WriteI32(0) // quest package
	b.WriteI32(0) // portrait giver
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	for range 4 {
		b.WriteU32(0)
	}
	b.WriteI32(0) // suggested
	b.WriteU32(0) // learn spells
	writeEmptyRewards(b, 0, 100)
	b.WriteU32(0) // desc emotes
	b.WriteU32(0) // objectives
	b.WriteI32(0) // start item
	b.WriteI32(0) // quest info
	b.WriteI32(0) // quest session bonus
	b.WriteI32(0) // giver creature
	b.WriteU32(0) // conditional
	b.WriteBits(uint64(len(title)), 9)
	b.WriteBits(4, 12)
	b.WriteBits(4, 12)
	b.WriteBits(0, 10)
	b.WriteBits(0, 8)
	b.WriteBits(0, 10)
	b.WriteBits(0, 8)
	b.WriteBit(true)  // AutoLaunched
	b.WriteBit(false) // FromContentPush
	b.WriteBit(false) // ReplayQuest
	b.WriteBit(false) // ResetByScheduler
	b.WriteBit(false) // StartCheat
	b.WriteBit(false) // DisplayPopup
	b.FlushBits()
	b.WriteString(title)
	b.WriteString("desc")
	b.WriteString("log!")
	return b.Bytes()
}

// TestQuestSharePreconditionsSendNothing: invalid quest id, bad wait_ms, stale log, unheld quest, and solo state
// all refuse before sending any packet.
func TestQuestSharePreconditionsSendNothing(t *testing.T) {
	q := questMaskWorld(t)
	ctx := context.Background()

	if _, err := q.s.serveQuestShare(ctx, serveQuestShareParams{QuestID: 0}); err == nil {
		t.Fatal("quest_id 0 passed")
	}
	if _, err := q.s.serveQuestShare(ctx, serveQuestShareParams{QuestID: questTaken, WaitMS: -1}); err == nil {
		t.Fatal("negative wait_ms passed")
	}
	if _, err := q.s.serveQuestShare(ctx, serveQuestShareParams{QuestID: questTaken, WaitMS: 60001}); err == nil {
		t.Fatal("over-max wait_ms passed")
	}
	if _, err := q.s.serveQuestShare(ctx, serveQuestShareParams{QuestID: questTaken}); err == nil || !strings.Contains(err.Error(), "not in a group") {
		t.Fatalf("not in group answered %v", err)
	}
	setupShareParty(t, q)
	if _, err := q.s.serveQuestShare(ctx, serveQuestShareParams{QuestID: questNew}); err == nil || !strings.Contains(err.Error(), "not in the log") {
		t.Fatalf("quest not in log answered %v", err)
	}
	q.s.questStale = "stale log"
	if _, err := q.s.serveQuestShare(ctx, serveQuestShareParams{QuestID: questTaken}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale log answered %v", err)
	}
	if len(q.sent) != 0 {
		t.Fatalf("refused shares sent %d packets", len(q.sent))
	}
}

// TestQuestShareSendsPacketAndReportsPerMemberResults: shares a held quest and reports per-member SMSG_QUEST_PUSH_RESULT
// answers plus event_log rows.
func TestQuestShareSendsPacketAndReportsPerMemberResults(t *testing.T) {
	q := questMaskWorld(t)
	setupShareParty(t, q)

	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGPushQuestToParty {
			q.deliver(wire.SMSGQuestPushResult, questPushResultPayload(shareSenderGUID, quest.PushReasonSuccess, ""))
			q.deliver(wire.SMSGQuestPushResult, questPushResultPayload(sharePeer2GUID, quest.PushReasonOnQuest, ""))
			return
		}
		inner(f, core, payload)
	}

	res, err := q.s.serveQuestShare(context.Background(), serveQuestShareParams{QuestID: questTaken})
	if err != nil {
		t.Fatalf("quest_share: %v", err)
	}
	if q.sends(wire.CMSGPushQuestToParty) != 1 {
		t.Fatalf("sent %d CMSG_PUSH_QUEST_TO_PARTY, want 1", q.sends(wire.CMSGPushQuestToParty))
	}
	wantPayload := quest.BuildPushQuestToParty(uint32(questTaken))
	for _, s := range q.sent {
		if s.core == wire.CMSGPushQuestToParty && !bytes.Equal(s.payload, wantPayload) {
			t.Fatalf("payload % X, want % X", s.payload, wantPayload)
		}
	}
	if !res.OK {
		t.Fatalf("expected OK=true when one member succeeded, got %+v", res)
	}
	if len(res.Results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(res.Results), res.Results)
	}
	if res.Results[0].Name != "Mateone" || res.Results[0].Result != quest.PushReasonSuccess || res.Results[0].Reason != "success" {
		t.Fatalf("member 0 result = %+v", res.Results[0])
	}
	if res.Results[1].Name != "Matetwo" || res.Results[1].Result != quest.PushReasonOnQuest || res.Results[1].Reason != "on_quest" {
		t.Fatalf("member 1 result = %+v", res.Results[1])
	}
	rows, _, _, _ := q.s.EventLog().Since(res.Since, 10)
	if len(rows) != 2 || rows[0].Kind != events.KindQuestPushResult || rows[1].Kind != events.KindQuestPushResult {
		t.Fatalf("event_log rows = %+v", rows)
	}
	if rows[0].Source != "Mateone" || rows[0].Data["reason"] != "success" || !rows[0].Party {
		t.Fatalf("row 0 = %+v", rows[0])
	}
}

// TestQuestShareSelfRefusalIsNotOK: when the core answers NotAllowed / NotDaily / NotInParty with SenderGUID == self,
// quest_share reports OK=false and surfaces the refusal result.
func TestQuestShareSelfRefusalIsNotOK(t *testing.T) {
	q := questMaskWorld(t)
	setupShareParty(t, q)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGPushQuestToParty {
			q.deliver(wire.SMSGQuestPushResult, questPushResultPayload(itemSelf, quest.PushReasonNotAllowed, ""))
			return
		}
		inner(f, core, payload)
	}
	res, err := q.s.serveQuestShare(context.Background(), serveQuestShareParams{QuestID: questTaken})
	if err != nil {
		t.Fatalf("quest_share: %v", err)
	}
	if res.OK {
		t.Fatalf("expected OK=false on self refusal, got %+v", res)
	}
	if len(res.Results) != 1 || res.Results[0].Result != quest.PushReasonNotAllowed || res.Results[0].Reason != "not_allowed" {
		t.Fatalf("results = %+v", res.Results)
	}
}

// TestQuestShareAcceptAndDeclinePushFlow: receiving SMSG_QUEST_GIVER_QUEST_DETAILS with InformUnit != 0 records a
// pending shared quest; accept sends CMSG_QUEST_GIVER_ACCEPT_QUEST naming InformUnit (not Giver) and waits for the
// quest log; decline sends CMSG_QUEST_PUSH_RESULT with Declined.
func TestQuestShareAcceptAndDeclinePushFlow(t *testing.T) {
	t.Run("accept", func(t *testing.T) {
		q := questMaskWorld(t)
		setupShareParty(t, q)
		if _, err := q.s.serveQuestShareAccept(context.Background(), serveQuestShareAcceptParams{}); err == nil || !strings.Contains(err.Error(), "no pending shared quest") {
			t.Fatalf("accept with no offer answered %v", err)
		}
		// Core sends QuestGiverGUID=receiver and InformUnit=sender on party quest share.
		q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsSharedFrom(itemSelf, shareSenderGUID, questNew, "Shared Quest"))

		// Mismatched quest_id is refused before sending anything.
		if _, err := q.s.serveQuestShareAccept(context.Background(), serveQuestShareAcceptParams{QuestID: questTaken}); err == nil || !strings.Contains(err.Error(), "not 700") {
			t.Fatalf("mismatched quest_id answered %v", err)
		}
		if len(q.sent) != 0 {
			t.Fatalf("precondition failure sent %d packets", len(q.sent))
		}

		res, err := q.s.serveQuestShareAccept(context.Background(), serveQuestShareAcceptParams{})
		if err != nil {
			t.Fatalf("quest_share_accept: %v", err)
		}
		if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestAccepted {
			t.Fatalf("accept result = %+v", res)
		}
		wantAccept := quest.BuildAcceptQuest(shareSenderGUID, questNew, false)
		if !bytes.Equal(q.accept, wantAccept) {
			t.Fatalf("CMSG_QUEST_GIVER_ACCEPT_QUEST = % X, want % X (must name InformUnit)", q.accept, wantAccept)
		}
		if slot, ok := q.s.questBook().Find(questNew); !ok || slot != 0 {
			t.Fatalf("quest %d not in log after accept (slot=%d, ok=%v)", questNew, slot, ok)
		}
		// Consumed: a second accept has no pending offer.
		if _, err := q.s.serveQuestShareAccept(context.Background(), serveQuestShareAcceptParams{}); err == nil {
			t.Fatal("second accept succeeded")
		}
	})

	t.Run("decline", func(t *testing.T) {
		q := questMaskWorld(t)
		setupShareParty(t, q)
		var pushSent []byte
		inner := q.onSend
		q.onSend = func(f *itemWorld, core uint32, payload []byte) {
			if core == wire.CMSGQuestPushResult {
				pushSent = append([]byte(nil), payload...)
				return
			}
			inner(f, core, payload)
		}
		q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsSharedFrom(itemSelf, shareSenderGUID, questNew, "Shared Quest"))
		res, err := q.s.serveQuestShareDecline(serveQuestShareDeclineParams{})
		if err != nil {
			t.Fatalf("quest_share_decline: %v", err)
		}
		if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestShareDeclined {
			t.Fatalf("decline result = %+v", res)
		}
		want := quest.BuildQuestPushResult(shareSenderGUID, uint32(questNew), quest.PushReasonDeclined)
		if !bytes.Equal(pushSent, want) {
			t.Fatalf("CMSG_QUEST_PUSH_RESULT = % X, want % X", pushSent, want)
		}
		if _, err := q.s.serveQuestShareDecline(serveQuestShareDeclineParams{}); err == nil {
			t.Fatal("second decline succeeded")
		}
	})
}

// TestQuestShareConfirmAcceptFlow: receiving SMSG_QUEST_CONFIRM_ACCEPT records a confirm offer; accept sends
// CMSG_QUEST_CONFIRM_ACCEPT and waits for the quest log; decline clears it cleanly without sending a packet.
func TestQuestShareConfirmAcceptFlow(t *testing.T) {
	t.Run("accept", func(t *testing.T) {
		q := questMaskWorld(t)
		setupShareParty(t, q)
		var confirmSent []byte
		inner := q.onSend
		q.onSend = func(f *itemWorld, core uint32, payload []byte) {
			if core == wire.CMSGQuestConfirmAccept {
				confirmSent = append([]byte(nil), payload...)
				r := wire.NewReader(payload)
				id := r.ReadI32()
				q.deliver(wire.SMSGUpdateObject, takeQuestState(0, id, 0))
				return
			}
			inner(f, core, payload)
		}
		q.deliver(wire.SMSGQuestConfirmAccept, questConfirmAcceptPayload(uint32(questNew), shareSenderGUID, "Escort Quest"))
		res, err := q.s.serveQuestShareAccept(context.Background(), serveQuestShareAcceptParams{})
		if err != nil {
			t.Fatalf("quest_share_accept confirm: %v", err)
		}
		if !res.OK || res.ShareKind != "confirm" || res.Event == nil || res.Event.Kind != events.KindQuestAccepted {
			t.Fatalf("confirm accept result = %+v", res)
		}
		want := quest.BuildQuestConfirmAccept(questNew)
		if !bytes.Equal(confirmSent, want) {
			t.Fatalf("CMSG_QUEST_CONFIRM_ACCEPT = % X, want % X", confirmSent, want)
		}
	})

	t.Run("decline", func(t *testing.T) {
		q := questMaskWorld(t)
		setupShareParty(t, q)
		q.deliver(wire.SMSGQuestConfirmAccept, questConfirmAcceptPayload(uint32(questNew), shareSenderGUID, "Escort Quest"))
		res, err := q.s.serveQuestShareDecline(serveQuestShareDeclineParams{})
		if err != nil {
			t.Fatalf("quest_share_decline confirm: %v", err)
		}
		if !res.OK || res.ShareKind != "confirm" || res.Event == nil || res.Event.Kind != events.KindQuestShareDeclined {
			t.Fatalf("confirm decline result = %+v", res)
		}
		if len(q.sent) != 0 {
			t.Fatalf("confirm decline sent %d packets, want 0", len(q.sent))
		}
	})
}

// TestQuestShareOneShotModes verifies the CLI one-shot session runner for share, accept, and decline.
func TestQuestShareOneShotModes(t *testing.T) {
	t.Run("share", func(t *testing.T) {
		q := questMaskWorld(t)
		setupShareParty(t, q)
		q.s.opts.QuestShareCheck = true
		q.s.opts.QuestShareMode = "share"
		q.s.opts.QuestShareID = questTaken
		q.s.opts.QuestShareWait = 500 * time.Millisecond
		inner := q.onSend
		q.onSend = func(f *itemWorld, core uint32, payload []byte) {
			if core == wire.CMSGPushQuestToParty {
				q.deliver(wire.SMSGQuestPushResult, questPushResultPayload(shareSenderGUID, quest.PushReasonSuccess, ""))
				q.deliver(wire.SMSGQuestPushResult, questPushResultPayload(sharePeer2GUID, quest.PushReasonSuccess, ""))
				return
			}
			inner(f, core, payload)
		}
		if err := q.s.questShareCheck(context.Background()); err != nil {
			t.Fatalf("questShareCheck: %v", err)
		}
		if q.s.report.QuestShare == nil || !q.s.report.QuestShare.OK || len(q.s.report.QuestShare.Results) != 2 {
			t.Fatalf("report = %+v", q.s.report.QuestShare)
		}
	})

	t.Run("accept-from", func(t *testing.T) {
		q := questMaskWorld(t)
		setupShareParty(t, q)
		q.s.opts.QuestShareCheck = true
		q.s.opts.QuestShareMode = "accept"
		q.s.opts.QuestShareFrom = "Mateone"
		q.s.opts.QuestShareWait = 500 * time.Millisecond
		q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsSharedFrom(itemSelf, shareSenderGUID, questNew, "Shared Quest"))
		if err := q.s.questShareCheck(context.Background()); err != nil {
			t.Fatalf("questShareCheck accept: %v", err)
		}
		if q.s.report.QuestShare == nil || !q.s.report.QuestShare.OK || q.s.report.QuestShare.QuestID != questNew {
			t.Fatalf("report = %+v", q.s.report.QuestShare)
		}
	})
}

func TestServeQuestShareMethodsAreRegistered(t *testing.T) {
	ported, refusedNames := ServeRPCMethods()
	for _, name := range []string{"quest_share", "quest_share_accept", "quest_share_decline"} {
		if slices.Contains(refusedNames, name) {
			t.Fatalf("%s is listed as refused", name)
		}
		if !slices.Contains(ported, name) {
			t.Fatalf("%s is not registered", name)
		}
	}
}
