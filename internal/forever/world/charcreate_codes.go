package world

import "fmt"

// The response codes of the character screen: SMSG_CREATE_CHAR, SMSG_DELETE_CHAR and
// SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT carry one of them (SharedDefines.h:6804-6897, ResponseCodes). The
// names are the core's enum names, so a refusal prints what the server meant instead of a number.

// charCreateCodes is every CHAR_CREATE_* code the create handler can send (CharacterHandler.cpp:833-1231).
var charCreateCodes = map[uint32]string{
	23: "CHAR_CREATE_IN_PROGRESS",
	24: "CHAR_CREATE_SUCCESS",
	25: "CHAR_CREATE_ERROR",
	26: "CHAR_CREATE_FAILED",
	27: "CHAR_CREATE_NAME_IN_USE",
	28: "CHAR_CREATE_DISABLED",
	29: "CHAR_CREATE_PVP_TEAMS_VIOLATION",
	30: "CHAR_CREATE_SERVER_LIMIT",
	31: "CHAR_CREATE_ACCOUNT_LIMIT",
	32: "CHAR_CREATE_SERVER_QUEUE",
	33: "CHAR_CREATE_ONLY_EXISTING",
	34: "CHAR_CREATE_EXPANSION",
	35: "CHAR_CREATE_EXPANSION_CLASS",
	36: "CHAR_CREATE_CHARACTER_IN_GUILD",
	37: "CHAR_CREATE_RESTRICTED_RACECLASS",
	38: "CHAR_CREATE_CHARACTER_CHOOSE_RACE",
	39: "CHAR_CREATE_CHARACTER_ARENA_LEADER",
	40: "CHAR_CREATE_CHARACTER_ARENA_TEAM",
	41: "CHAR_CREATE_CHARACTER_DELETE_MAIL",
	42: "CHAR_CREATE_CHARACTER_SWAP_FACTION",
	43: "CHAR_CREATE_CHARACTER_RACE_ONLY",
	44: "CHAR_CREATE_CHARACTER_GOLD_LIMIT",
	45: "CHAR_CREATE_FORCE_LOGIN",
	46: "CHAR_CREATE_TRIAL",
	47: "CHAR_CREATE_TIMEOUT",
	48: "CHAR_CREATE_THROTTLE",
	49: "CHAR_CREATE_ALLIED_RACE_ACHIEVEMENT",
	50: "CHAR_CREATE_RACECLASS_ACHIEVEMENT",
	51: "CHAR_CREATE_CHARACTER_IN_COMMUNITY",
	52: "CHAR_CREATE_NEW_PLAYER",
	53: "CHAR_CREATE_NAME_RESERVATION_FULL",
	54: "CHAR_CREATE_DRACTHYR_DUPLICATE",
	55: "CHAR_CREATE_DRACTHYR_LEVEL_REQUIREMENT",
	56: "CHAR_CREATE_DEATHKNIGHT_DUPLICATE",
	57: "CHAR_CREATE_DEATHKNIGHT_LEVEL_REQUIREMENT",
	58: "CHAR_CREATE_CLASS_TRIAL_NEWCOMER",
	59: "CHAR_CREATE_CLASS_TRIAL_THROTTLE_HOUR",
	60: "CHAR_CREATE_CLASS_TRIAL_THROTTLE_DAY",
	61: "CHAR_CREATE_CLASS_TRIAL_THROTTLE_WEEK",
	62: "CHAR_CREATE_CLASS_TRIAL_THROTTLE_ACCOUNT",
	63: "CHAR_CREATE_FACTION_BALANCE",
	64: "CHAR_CREATE_TIMERUNNING",
	65: "CHAR_CREATE_NEED_ENTITLEMENT",
}

// charDeleteCodes is every CHAR_DELETE_* code DeleteCharacter can send (CharacterHandler.cpp:1233-1327).
var charDeleteCodes = map[uint32]string{
	66: "CHAR_DELETE_IN_PROGRESS",
	67: "CHAR_DELETE_SUCCESS",
	68: "CHAR_DELETE_FAILED",
	69: "CHAR_DELETE_FAILED_CHARACTER_SERVICE_PENDING",
	70: "CHAR_DELETE_FAILED_GUILD_LEADER",
	71: "CHAR_DELETE_FAILED_ARENA_CAPTAIN",
	72: "CHAR_DELETE_FAILED_HAS_HEIRLOOM_OR_MAIL",
	73: "CHAR_DELETE_FAILED_DEPRECATED1",
	74: "CHAR_DELETE_FAILED_HAS_WOW_TOKEN",
	75: "CHAR_DELETE_FAILED_DEPRECATED2",
	76: "CHAR_DELETE_FAILED_COMMUNITY_OWNER",
	77: "CHAR_DELETE_FAILED_NEIGHBORHOOD_OWNER",
	78: "CHAR_DELETE_FAILED_HOUSE_OWNER",
}

// charNameCodes is the CHAR_NAME_* block ObjectMgr::CheckPlayerName answers with (ObjectMgr.cpp:9172-9193); the name
// check also answers RESPONSE_SUCCESS and CHAR_CREATE_NAME_IN_USE (CharacterHandler.cpp:2032-2067).
var charNameCodes = map[uint32]string{
	0:   "RESPONSE_SUCCESS",
	27:  "CHAR_CREATE_NAME_IN_USE",
	96:  "CHAR_NAME_SUCCESS",
	97:  "CHAR_NAME_FAILURE",
	98:  "CHAR_NAME_NO_NAME",
	99:  "CHAR_NAME_TOO_SHORT",
	100: "CHAR_NAME_TOO_LONG",
	101: "CHAR_NAME_INVALID_CHARACTER",
	102: "CHAR_NAME_MIXED_LANGUAGES",
	103: "CHAR_NAME_PROFANE",
	104: "CHAR_NAME_RESERVED",
	105: "CHAR_NAME_INVALID_APOSTROPHE",
	106: "CHAR_NAME_MULTIPLE_APOSTROPHES",
	107: "CHAR_NAME_THREE_CONSECUTIVE",
	108: "CHAR_NAME_INVALID_SPACE",
	109: "CHAR_NAME_CONSECUTIVE_SPACES",
	110: "CHAR_NAME_RUSSIAN_CONSECUTIVE_SILENT_CHARACTERS",
	111: "CHAR_NAME_RUSSIAN_SILENT_CHARACTER_AT_BEGINNING_OR_END",
	112: "CHAR_NAME_DECLENSION_DOESNT_MATCH_BASE_NAME",
	113: "CHAR_NAME_SPACES_DISALLOWED",
}

// CharCreateCodeName names a CHAR_CREATE_* code. The create handler also sends the CHAR_NAME_* codes that
// ObjectMgr::CheckPlayerName answers with (CharacterHandler.cpp:972-979), so those are named here too; a code from
// neither family prints as a number.
func CharCreateCodeName(code uint32) string {
	if name, ok := charCreateCodes[code]; ok {
		return name
	}
	if name, ok := charNameCodes[code]; ok {
		return name
	}
	return fmt.Sprintf("CHAR_CREATE code %d", code)
}

// CharDeleteCodeName names a CHAR_DELETE_* code, or prints the number of a code the pin does not define.
func CharDeleteCodeName(code uint32) string { return codeName(charDeleteCodes, "CHAR_DELETE", code) }

// CharNameCodeName names a name-check result: RESPONSE_SUCCESS, CHAR_CREATE_NAME_IN_USE or a CHAR_NAME_* code.
func CharNameCodeName(code uint32) string { return codeName(charNameCodes, "CHAR_NAME", code) }

func codeName(codes map[uint32]string, family string, code uint32) string {
	if name, ok := codes[code]; ok {
		return name
	}
	return fmt.Sprintf("%s code %d", family, code)
}

// CreateRefusedError is SMSG_CREATE_CHAR with a code other than CHAR_CREATE_SUCCESS. It carries the named code so a
// caller can print it and branch on it.
type CreateRefusedError struct {
	Code uint32
}

func (e *CreateRefusedError) Error() string {
	return "world: character creation refused: " + CharCreateCodeName(e.Code)
}

// DeleteRefusedError is SMSG_DELETE_CHAR with a code other than CHAR_DELETE_SUCCESS.
type DeleteRefusedError struct {
	Code uint32
}

func (e *DeleteRefusedError) Error() string {
	return "world: character deletion refused: " + CharDeleteCodeName(e.Code)
}

// NameRefusedError is SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT with a code other than RESPONSE_SUCCESS.
type NameRefusedError struct {
	Code uint32
}

func (e *NameRefusedError) Error() string {
	return "world: the name is not available: " + CharNameCodeName(e.Code)
}
