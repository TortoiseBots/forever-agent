package world

// Quest sharing and shared-quest responses (issue #293):
//   - quest_share sends CMSG_PUSH_QUEST_TO_PARTY for a quest in the character's log and collects the server's
//     SMSG_QUEST_PUSH_RESULT answers per party member (or self-refusal such as not_allowed / not_daily / not_in_party);
//   - quest_share_accept accepts a pending shared quest (CMSG_QUEST_GIVER_ACCEPT_QUEST naming InformUnit for a pushed
//     quest, or CMSG_QUEST_CONFIRM_ACCEPT for a confirm-accept popup) and waits for the quest to appear in the log;
//   - quest_share_decline declines a pending shared quest (CMSG_QUEST_PUSH_RESULT with Declined for a pushed quest, or
//     clears the confirm-accept offer).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	questShareDefaultWait     = 2 * time.Second
	questShareSoloDefaultWait = 500 * time.Millisecond
	maxQuestPushHistory       = 256
)

type questShareOffer struct {
	Kind    string // "push" or "confirm"
	QuestID int32
	From    wire.GUID
	Title   string
}

type questPushResultEntry struct {
	Seq    uint64
	Result quest.PushResult
}

type serveQuestShareParams struct {
	QuestID int32 `json:"quest_id"`
	WaitMS  int   `json:"wait_ms,omitempty"`
}

type serveQuestShareAcceptParams struct {
	QuestID int32 `json:"quest_id,omitempty"`
	WaitMS  int   `json:"wait_ms,omitempty"`
}

type serveQuestShareDeclineParams struct {
	QuestID int32 `json:"quest_id,omitempty"`
}

// ServeQuestPushMember is one SMSG_QUEST_PUSH_RESULT returned for a shared quest.
type ServeQuestPushMember struct {
	GUID   GUID   `json:"guid"`
	Name   string `json:"name,omitempty"`
	Result uint32 `json:"result"`
	Reason string `json:"reason"`
}

// ServeQuestShareResult is quest_share's answer.
type ServeQuestShareResult struct {
	Since   uint64                 `json:"since"`
	QuestID int32                  `json:"quest_id"`
	OK      bool                   `json:"ok"`
	Results []ServeQuestPushMember `json:"results"`
	Event   *Event                 `json:"event,omitempty"`
}

// ServeQuestShareAnswerResult is quest_share_accept and quest_share_decline's answer.
type ServeQuestShareAnswerResult struct {
	Since     uint64 `json:"since"`
	OK        bool   `json:"ok"`
	QuestID   int32  `json:"quest_id"`
	Title     string `json:"title,omitempty"`
	ShareKind string `json:"share_kind"`
	From      string `json:"from,omitempty"`
	FromGUID  GUID   `json:"from_guid,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
	Event     *Event `json:"event,omitempty"`
}

// QuestShareReport is the one-shot CLI summary of `forever quest-share`.
type QuestShareReport struct {
	Mode      string
	QuestID   int32
	Title     string
	ShareKind string
	From      string
	OK        bool
	Slot      *int
	Results   []ServeQuestPushMember
	Reason    string
}

func (s *session) recordSharedQuest(kind string, questID int32, from wire.GUID, title string) {
	s.quest.shared = &questShareOffer{
		Kind:    kind,
		QuestID: questID,
		From:    from,
		Title:   title,
	}
	who := s.eventName("", from)
	s.event(Event{
		Kind:       events.KindQuestShared,
		Source:     who,
		SourceGUID: eventGUID(from),
		Party:      s.eventParty(from),
		Text:       title,
		Data: map[string]any{
			"quest_id":   questID,
			"title":      title,
			"share_kind": kind,
		},
	})
	if who != "" {
		s.opts.logf("world: quest %d (%q) shared (%s) by %s", questID, title, kind, who)
	} else {
		s.opts.logf("world: quest %d (%q) shared (%s)", questID, title, kind)
	}
}

func (s *session) recordQuestPushResult(v quest.PushResult) {
	s.quest.pushSeq++
	if len(s.quest.pushResults) >= maxQuestPushHistory {
		copy(s.quest.pushResults, s.quest.pushResults[1:])
		s.quest.pushResults = s.quest.pushResults[:maxQuestPushHistory-1]
	}
	s.quest.pushResults = append(s.quest.pushResults, questPushResultEntry{Seq: s.quest.pushSeq, Result: v})

	reason := quest.PushReasonName(v.Result)
	who := s.eventName("", v.SenderGUID)
	data := map[string]any{
		"result": v.Result,
		"reason": reason,
	}
	if v.QuestTitle != "" {
		data["quest_title"] = v.QuestTitle
	}
	s.event(Event{
		Kind:       events.KindQuestPushResult,
		Source:     who,
		SourceGUID: eventGUID(v.SenderGUID),
		Party:      s.eventParty(v.SenderGUID),
		Text:       reason,
		Data:       data,
	})
	if who != "" {
		s.opts.logf("world: quest push result from %s: %s (%d)", who, reason, v.Result)
	} else {
		s.opts.logf("world: quest push result: %s (%d)", reason, v.Result)
	}
}

// expectedShareRecipients returns the other connected members in the group from a valid roster.
func (s *session) expectedShareRecipients() map[wire.GUID]bool {
	out := make(map[wire.GUID]bool)
	if s.party == nil || !s.party.InParty || !s.party.RosterValid {
		return out
	}
	for g, m := range s.party.Members {
		if g == s.party.SelfGUID || !m.Connected {
			continue
		}
		out[g] = true
	}
	return out
}

func (s *session) pushResultsSince(sinceSeq uint64) []quest.PushResult {
	var out []quest.PushResult
	for _, r := range s.quest.pushResults {
		if r.Seq > sinceSeq {
			out = append(out, r.Result)
		}
	}
	return out
}

func (s *session) serveQuestShare(parent context.Context, p serveQuestShareParams) (ServeQuestShareResult, error) {
	if p.QuestID <= 0 {
		return ServeQuestShareResult{}, fmt.Errorf("quest_id %d is not a positive quest id", p.QuestID)
	}
	if err := s.questLogTrusted("quest_share"); err != nil {
		return ServeQuestShareResult{}, err
	}
	book := s.questBook()
	if book.Slots() == 0 {
		return ServeQuestShareResult{}, errors.New("quest_share: the quest log is not known yet (the character's own create has not arrived)")
	}
	if _, ok := book.Find(p.QuestID); !ok {
		return ServeQuestShareResult{}, fmt.Errorf("quest %d is not in the log (quest_log shows what is)", p.QuestID)
	}
	s.initChat()
	if !s.party.InParty {
		return ServeQuestShareResult{}, errors.New("quest_share: not in a group")
	}
	expected := s.expectedShareRecipients()
	defMS := int(questShareDefaultWait.Milliseconds())
	if len(expected) == 0 {
		defMS = int(questShareSoloDefaultWait.Milliseconds())
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, defMS, methodWaitMaxMS)
	if err != nil {
		return ServeQuestShareResult{}, err
	}
	defer cancel()

	res := ServeQuestShareResult{
		Since:   s.EventLog().Last(),
		QuestID: p.QuestID,
		Results: []ServeQuestPushMember{},
	}
	beforeSeq := s.quest.pushSeq
	if err := s.transport().Send(wire.CMSGPushQuestToParty, quest.BuildPushQuestToParty(uint32(p.QuestID))); err != nil {
		return res, fmt.Errorf("CMSG_PUSH_QUEST_TO_PARTY: %w", err)
	}

	self := s.selfOrMover()
	_, err = s.actUntil(ctx, time.Duration(methodWaitMaxMS)*time.Millisecond, func() bool {
		got := s.pushResultsSince(beforeSeq)
		if len(got) == 0 {
			return false
		}
		seen := make(map[wire.GUID]bool, len(got))
		for _, r := range got {
			if !self.IsZero() && r.SenderGUID == self {
				return true
			}
			seen[r.SenderGUID] = true
		}
		if len(expected) == 0 {
			return false
		}
		for g := range expected {
			if !seen[g] {
				return false
			}
		}
		return true
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return res, err
	}
	if err != nil && parent.Err() != nil {
		return res, err
	}

	got := s.pushResultsSince(beforeSeq)
	selfRefused := false
	anyShared := false
	for _, r := range got {
		if !self.IsZero() && r.SenderGUID == self {
			selfRefused = true
		} else if r.Result == quest.PushReasonSuccess || r.Result == quest.PushReasonAccepted {
			anyShared = true
		}
		res.Results = append(res.Results, ServeQuestPushMember{
			GUID:   GUID(r.SenderGUID),
			Name:   s.eventName("", r.SenderGUID),
			Result: r.Result,
			Reason: quest.PushReasonName(r.Result),
		})
	}
	res.OK = anyShared && !selfRefused
	res.Event = s.firstEventSince(res.Since, events.KindQuestPushResult)
	return res, nil
}

func (s *session) serveQuestShareAccept(parent context.Context, p serveQuestShareAcceptParams) (ServeQuestShareAnswerResult, error) {
	if p.QuestID < 0 {
		return ServeQuestShareAnswerResult{}, fmt.Errorf("quest_id %d is not a positive quest id", p.QuestID)
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeQuestShareAnswerResult{}, err
	}
	defer cancel()
	if err := s.questLogTrusted("quest_share_accept"); err != nil {
		return ServeQuestShareAnswerResult{}, err
	}
	book := s.questBook()
	if book.Slots() == 0 {
		return ServeQuestShareAnswerResult{}, errors.New("quest_share_accept: the quest log is not known yet (the character's own create has not arrived)")
	}
	offer := s.quest.shared
	if offer == nil {
		return ServeQuestShareAnswerResult{}, errors.New("no pending shared quest")
	}
	if p.QuestID > 0 && offer.QuestID != p.QuestID {
		return ServeQuestShareAnswerResult{}, fmt.Errorf("pending shared quest is %d, not %d", offer.QuestID, p.QuestID)
	}
	if slot, ok := book.Find(offer.QuestID); ok {
		return ServeQuestShareAnswerResult{}, fmt.Errorf("quest %d is already in slot %d", offer.QuestID, slot)
	}
	if _, ok := book.FirstFree(); !ok {
		return ServeQuestShareAnswerResult{}, fmt.Errorf("the quest log has no free slot (%d slots)", book.Slots())
	}

	s.quest.shared = nil
	s.quest.invalid, s.quest.failed, s.quest.logFull = nil, nil, false
	res := ServeQuestShareAnswerResult{
		Since:     s.EventLog().Last(),
		QuestID:   offer.QuestID,
		Title:     offer.Title,
		ShareKind: offer.Kind,
		From:      s.eventName("", offer.From),
		FromGUID:  GUID(offer.From),
	}

	switch offer.Kind {
	case "confirm":
		if err := s.transport().Send(wire.CMSGQuestConfirmAccept, quest.BuildQuestConfirmAccept(offer.QuestID)); err != nil {
			return res, fmt.Errorf("CMSG_QUEST_CONFIRM_ACCEPT: %w", err)
		}
	default:
		if err := s.transport().Send(wire.CMSGQuestGiverAcceptQuest, quest.BuildAcceptQuest(offer.From, offer.QuestID, false)); err != nil {
			return res, fmt.Errorf("CMSG_QUEST_GIVER_ACCEPT_QUEST: %w", err)
		}
	}

	_, err = s.actUntil(ctx, actAcceptWait, func() bool {
		if _, in := s.questBook().Find(offer.QuestID); in {
			return true
		}
		return s.quest.invalid != nil || s.questFailedIs(offer.QuestID) || s.quest.logFull || s.questStale != ""
	})
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if slot, in := s.questBook().Find(offer.QuestID); in {
		res.OK = true
		res.Slot = &slot
		res.Event = s.methodEvent(events.KindQuestAccepted, offer.From, map[string]any{
			"quest_id":   offer.QuestID,
			"slot":       slot,
			"shared":     true,
			"share_kind": offer.Kind,
		}, "")
		return res, nil
	}

	var refusal string
	switch {
	case s.quest.invalid != nil:
		refusal = fmt.Sprintf("accept shared quest %d: refused: %s", offer.QuestID, acceptRefusal(s.quest.invalid))
	case s.questFailedIs(offer.QuestID):
		refusal = fmt.Sprintf("accept shared quest %d: refused: %s", offer.QuestID, s.quest.failed.Name())
	case s.quest.logFull:
		refusal = fmt.Sprintf("accept shared quest %d: the server answered that the quest log is full", offer.QuestID)
	case s.questStale != "":
		refusal = fmt.Sprintf("accept shared quest %d: unconfirmed, the quest log became stale (%s)", offer.QuestID, s.questStale)
	default:
		refusal = fmt.Sprintf("accept shared quest %d: not confirmed within %s (the server's quest log does not show the quest)", offer.QuestID, actAcceptWait)
	}
	res.Event = s.methodEvent(events.KindQuestError, offer.From, map[string]any{"quest_id": offer.QuestID}, refusal)
	return res, nil
}

func (s *session) serveQuestShareDecline(p serveQuestShareDeclineParams) (ServeQuestShareAnswerResult, error) {
	if p.QuestID < 0 {
		return ServeQuestShareAnswerResult{}, fmt.Errorf("quest_id %d is not a positive quest id", p.QuestID)
	}
	offer := s.quest.shared
	if offer == nil {
		return ServeQuestShareAnswerResult{}, errors.New("no pending shared quest")
	}
	if p.QuestID > 0 && offer.QuestID != p.QuestID {
		return ServeQuestShareAnswerResult{}, fmt.Errorf("pending shared quest is %d, not %d", offer.QuestID, p.QuestID)
	}
	s.quest.shared = nil
	res := ServeQuestShareAnswerResult{
		Since:     s.EventLog().Last(),
		OK:        true,
		QuestID:   offer.QuestID,
		Title:     offer.Title,
		ShareKind: offer.Kind,
		From:      s.eventName("", offer.From),
		FromGUID:  GUID(offer.From),
	}
	if offer.Kind == "push" {
		if err := s.transport().Send(wire.CMSGQuestPushResult, quest.BuildQuestPushResult(offer.From, uint32(offer.QuestID), quest.PushReasonDeclined)); err != nil {
			return res, fmt.Errorf("CMSG_QUEST_PUSH_RESULT: %w", err)
		}
	}
	res.Event = s.methodEvent(events.KindQuestShareDeclined, offer.From, map[string]any{
		"quest_id":   offer.QuestID,
		"share_kind": offer.Kind,
	}, "")
	return res, nil
}

func (s *session) questShareMatches(offer *questShareOffer) bool {
	if offer == nil {
		return false
	}
	if s.opts.QuestShareID > 0 && offer.QuestID != s.opts.QuestShareID {
		return false
	}
	if s.opts.QuestShareFrom != "" {
		fromName := s.eventName("", offer.From)
		if !strings.EqualFold(fromName, s.opts.QuestShareFrom) {
			return false
		}
	}
	return true
}

// questShareCheck runs the one-shot `forever quest-share` command inside the session.
func (s *session) questShareCheck(ctx context.Context) error {
	settle := s.opts.Settle
	if settle > 0 {
		if err := s.collect(ctx, settle); err != nil {
			return err
		}
	}

	mode := s.opts.QuestShareMode
	rep := &QuestShareReport{Mode: mode, QuestID: s.opts.QuestShareID}
	s.report.QuestShare = rep

	wait := s.opts.QuestShareWait
	if wait <= 0 {
		if mode == "share" {
			wait = questShareDefaultWait
		} else {
			wait = 30 * time.Second
		}
	}

	switch mode {
	case "share":
		res, err := s.serveQuestShare(ctx, serveQuestShareParams{
			QuestID: s.opts.QuestShareID,
			WaitMS:  int(wait.Milliseconds()),
		})
		rep.Results = res.Results
		rep.OK = res.OK
		if err != nil {
			rep.Reason = err.Error()
			return fmt.Errorf("world: quest-share: %w", err)
		}
		if !res.OK {
			rep.Reason = "quest was not shared with any party member"
			return fmt.Errorf("world: quest-share: %s", rep.Reason)
		}
	case "accept", "decline":
		ok, err := s.actUntil(ctx, wait, func() bool {
			return s.questShareMatches(s.quest.shared)
		})
		if err != nil {
			rep.Reason = err.Error()
			return fmt.Errorf("world: quest-share: %w", err)
		}
		if !ok {
			rep.Reason = fmt.Sprintf("no matching shared quest within %s", wait)
			return fmt.Errorf("world: quest-share: %s", rep.Reason)
		}
		if mode == "accept" {
			res, err := s.serveQuestShareAccept(ctx, serveQuestShareAcceptParams{
				QuestID: s.opts.QuestShareID,
			})
			rep.QuestID = res.QuestID
			rep.Title = res.Title
			rep.ShareKind = res.ShareKind
			rep.From = res.From
			rep.Slot = res.Slot
			rep.OK = res.OK
			if err != nil {
				rep.Reason = err.Error()
				return fmt.Errorf("world: quest-share: %w", err)
			}
			if !res.OK {
				if res.Event != nil && res.Event.Text != "" {
					rep.Reason = res.Event.Text
				} else {
					rep.Reason = "shared quest accept failed"
				}
				return fmt.Errorf("world: quest-share: %s", rep.Reason)
			}
		} else {
			res, err := s.serveQuestShareDecline(serveQuestShareDeclineParams{
				QuestID: s.opts.QuestShareID,
			})
			rep.QuestID = res.QuestID
			rep.Title = res.Title
			rep.ShareKind = res.ShareKind
			rep.From = res.From
			rep.OK = res.OK
			if err != nil {
				rep.Reason = err.Error()
				return fmt.Errorf("world: quest-share: %w", err)
			}
		}
	default:
		return fmt.Errorf("world: quest-share: unknown mode %q", mode)
	}

	if s.opts.QuestShareStay > 0 {
		if err := s.collect(ctx, s.opts.QuestShareStay); err != nil {
			return err
		}
	}
	return nil
}
