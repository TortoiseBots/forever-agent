package quest

// Quest sharing and confirm-accept packets (QuestPackets.cpp:646-682, :713-716; QuestDef.h:74-120).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// QuestPushReason values (QuestDef.h:74-120).
const (
	PushReasonSuccess                         uint32 = 0
	PushReasonInvalid                         uint32 = 1
	PushReasonInvalidToRecipient              uint32 = 2
	PushReasonAccepted                        uint32 = 3
	PushReasonDeclined                        uint32 = 4
	PushReasonBusy                            uint32 = 5
	PushReasonDead                            uint32 = 6
	PushReasonDeadToRecipient                 uint32 = 7
	PushReasonLogFull                         uint32 = 8
	PushReasonLogFullToRecipient              uint32 = 9
	PushReasonOnQuest                         uint32 = 10
	PushReasonOnQuestToRecipient              uint32 = 11
	PushReasonAlreadyDone                     uint32 = 12
	PushReasonAlreadyDoneToRecipient          uint32 = 13
	PushReasonNotDaily                        uint32 = 14
	PushReasonTimerExpired                    uint32 = 15
	PushReasonNotInParty                      uint32 = 16
	PushReasonDifferentServerDaily            uint32 = 17
	PushReasonDifferentServerDailyToRecipient uint32 = 18
	PushReasonNotAllowed                      uint32 = 19
	PushReasonPrerequisite                    uint32 = 20
	PushReasonPrerequisiteToRecipient         uint32 = 21
	PushReasonLowLevel                        uint32 = 22
	PushReasonLowLevelToRecipient             uint32 = 23
	PushReasonHighLevel                       uint32 = 24
	PushReasonHighLevelToRecipient            uint32 = 25
	PushReasonClass                           uint32 = 26
	PushReasonClassToRecipient                uint32 = 27
	PushReasonRace                            uint32 = 28
	PushReasonRaceToRecipient                 uint32 = 29
	PushReasonLowFaction                      uint32 = 30
	PushReasonLowFactionToRecipient           uint32 = 31
	PushReasonHighFaction                     uint32 = 32
	PushReasonHighFactionToRecipient          uint32 = 33
	PushReasonExpansion                       uint32 = 34
	PushReasonExpansionToRecipient            uint32 = 35
	PushReasonNotGarrisonOwner                uint32 = 36
	PushReasonNotGarrisonOwnerToRecipient     uint32 = 37
	PushReasonWrongCovenant                   uint32 = 38
	PushReasonWrongCovenantToRecipient        uint32 = 39
	PushReasonNewPlayerExperience             uint32 = 40
	PushReasonNewPlayerExperienceToRecipient  uint32 = 41
	PushReasonWrongFaction                    uint32 = 42
	PushReasonWrongFactionToRecipient         uint32 = 43
	PushReasonCrossFactionRestricted          uint32 = 44
)

var pushReasonNames = map[uint32]string{
	PushReasonSuccess:                         "success",
	PushReasonInvalid:                         "invalid",
	PushReasonInvalidToRecipient:              "invalid_to_recipient",
	PushReasonAccepted:                        "accepted",
	PushReasonDeclined:                        "declined",
	PushReasonBusy:                            "busy",
	PushReasonDead:                            "dead",
	PushReasonDeadToRecipient:                 "dead_to_recipient",
	PushReasonLogFull:                         "log_full",
	PushReasonLogFullToRecipient:              "log_full_to_recipient",
	PushReasonOnQuest:                         "on_quest",
	PushReasonOnQuestToRecipient:              "on_quest_to_recipient",
	PushReasonAlreadyDone:                     "already_done",
	PushReasonAlreadyDoneToRecipient:          "already_done_to_recipient",
	PushReasonNotDaily:                        "not_daily",
	PushReasonTimerExpired:                    "timer_expired",
	PushReasonNotInParty:                      "not_in_party",
	PushReasonDifferentServerDaily:            "different_server_daily",
	PushReasonDifferentServerDailyToRecipient: "different_server_daily_to_recipient",
	PushReasonNotAllowed:                      "not_allowed",
	PushReasonPrerequisite:                    "prerequisite",
	PushReasonPrerequisiteToRecipient:         "prerequisite_to_recipient",
	PushReasonLowLevel:                        "low_level",
	PushReasonLowLevelToRecipient:             "low_level_to_recipient",
	PushReasonHighLevel:                       "high_level",
	PushReasonHighLevelToRecipient:            "high_level_to_recipient",
	PushReasonClass:                           "class",
	PushReasonClassToRecipient:                "class_to_recipient",
	PushReasonRace:                            "race",
	PushReasonRaceToRecipient:                 "race_to_recipient",
	PushReasonLowFaction:                      "low_faction",
	PushReasonLowFactionToRecipient:           "low_faction_to_recipient",
	PushReasonHighFaction:                     "high_faction",
	PushReasonHighFactionToRecipient:          "high_faction_to_recipient",
	PushReasonExpansion:                       "expansion",
	PushReasonExpansionToRecipient:            "expansion_to_recipient",
	PushReasonNotGarrisonOwner:                "not_garrison_owner",
	PushReasonNotGarrisonOwnerToRecipient:     "not_garrison_owner_to_recipient",
	PushReasonWrongCovenant:                   "wrong_covenant",
	PushReasonWrongCovenantToRecipient:        "wrong_covenant_to_recipient",
	PushReasonNewPlayerExperience:             "new_player_experience",
	PushReasonNewPlayerExperienceToRecipient:  "new_player_experience_to_recipient",
	PushReasonWrongFaction:                    "wrong_faction",
	PushReasonWrongFactionToRecipient:         "wrong_faction_to_recipient",
	PushReasonCrossFactionRestricted:          "cross_faction_restricted",
}

// PushReasonName returns a QuestPushReason name for a report line or event.
func PushReasonName(reason uint32) string {
	if name, ok := pushReasonNames[reason]; ok {
		return name
	}
	return fmt.Sprintf("push result %d", reason)
}

// BuildPushQuestToParty is CMSG_PUSH_QUEST_TO_PARTY (PushQuestToParty::Read, QuestPackets.cpp:713-716).
func BuildPushQuestToParty(questID uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(questID)
	return b.Bytes()
}

// BuildQuestPushResult is CMSG_QUEST_PUSH_RESULT (QuestPushResult::Read, QuestPackets.cpp:677-682).
func BuildQuestPushResult(sender wire.GUID, questID, result uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, sender)
	b.WriteU32(questID)
	b.WriteU32(result)
	return b.Bytes()
}

// BuildQuestConfirmAccept is CMSG_QUEST_CONFIRM_ACCEPT (QuestConfirmAccept::Read, QuestPackets.cpp:659-662).
func BuildQuestConfirmAccept(questID int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(questID)
	return b.Bytes()
}

// PushResult is SMSG_QUEST_PUSH_RESULT (QuestPushResultResponse::Write, QuestPackets.cpp:664-675).
type PushResult struct {
	SenderGUID wire.GUID
	Result     uint32
	QuestTitle string
}

// ParsePushResult decodes one SMSG_QUEST_PUSH_RESULT and requires the payload to be consumed exactly.
func ParsePushResult(p []byte) (PushResult, error) {
	b := wire.NewReader(p)
	var r PushResult
	r.SenderGUID = wire.ReadPackedGUID(b)
	r.Result = b.ReadU32()
	title, err := sizedString(b, 9, "quest title")
	if err != nil {
		return r, err
	}
	r.QuestTitle = title
	if err := exactly(b); err != nil {
		return r, err
	}
	return r, nil
}

// ConfirmAccept is SMSG_QUEST_CONFIRM_ACCEPT (QuestConfirmAcceptResponse::Write, QuestPackets.cpp:646-657).
type ConfirmAccept struct {
	QuestID     uint32
	InitiatedBy wire.GUID
	QuestTitle  string
}

// ParseConfirmAccept decodes one SMSG_QUEST_CONFIRM_ACCEPT and requires the payload to be consumed exactly.
func ParseConfirmAccept(p []byte) (ConfirmAccept, error) {
	b := wire.NewReader(p)
	var c ConfirmAccept
	c.QuestID = b.ReadU32()
	c.InitiatedBy = wire.ReadPackedGUID(b)
	title, err := sizedString(b, 10, "quest title")
	if err != nil {
		return c, err
	}
	c.QuestTitle = title
	if err := exactly(b); err != nil {
		return c, err
	}
	return c, nil
}
