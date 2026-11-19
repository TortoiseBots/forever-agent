package world

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// A fake character screen for round 6. It is the same fake server as round 2 for the handshake (banners, CMSG_AUTH_SESSION
// with a digest it really verifies, encryption, the compressed auth response), and from SMSG_AUTH_RESPONSE on it
// speaks only the character screen: the list, create, delete and the name check. The wire numbers are literals
// (70707 client on a 70707 realm, hand-computed like the existing fake's), and the create packet is decoded by
// serverReadCreateCharacter, the transcription of CreateCharacter::Read, not by the builder.

const (
	fakeCMSGCreateCharacter = 0x440070
	fakeCMSGCheckName       = 0x440071
	fakeCMSGCharDelete      = 0x4400CB

	fakeSMSGCreateChar = 0x4701AC
	fakeSMSGDeleteChar = 0x4701AD
	fakeSMSGCheckName  = 0x47001B

	fakeCharCreateSuccess = 24 // CHAR_CREATE_SUCCESS
	fakeCharDeleteSuccess = 67 // CHAR_DELETE_SUCCESS
)

type fakeChar struct {
	GUID    wire.GUID
	Name    string
	Surname string
	Race    uint8
	Class   uint8
	Sex     uint8
}

// The synthetic GUID shape the fixtures use: a player GUID (high type 2), mask 0xE003 and five non-zero bytes, so
// the packed reader returns it.
func fakeCharGUID(n uint64) wire.GUID {
	return wire.GUID{Low: 0x2211 + n, High: 0x0A44330000000000}
}

type fakeCharScreen struct {
	f *fakeServer

	mu       sync.Mutex
	chars    []fakeChar
	create   uint32 // a create code to send regardless of the list; 0 derives it
	delete   uint32 // a delete code to send; 0 is CHAR_DELETE_SUCCESS
	nameCode uint32 // a name-check code to send; 0 derives it
	// sessions is how many realm logins this fake serves. One is the default of newFakeCharScreen; a test that calls
	// the client twice (a create and then a delete of the same run) raises it.
	sessions int
	// closeAt drops the socket at one step, to prove every wait ends on a lost link: "list" (the first list), "create"
	// (after the create packet was read), "delete", "name", "list-after" (the second list of a create).
	closeAt            string
	timeSyncBeforeList bool
	// silent reads the list request and never answers: the silent peer the 30 s bound turns into an error.
	silent bool
	// listDelay holds the list answer back: a test that spends most of the run's deadline on the auth and the list.
	listDelay time.Duration
	// silentCreate reads CMSG_CREATE_CHARACTER and never answers: a create that timed out after the send.
	silentCreate bool
	// silentDelete reads CMSG_CHAR_DELETE and never answers: a delete that timed out after the send.
	silentDelete bool
	// endWith sends "login_failed" (SMSG_CHARACTER_LOGIN_FAILED, reason NoWorld) or "logout_complete"
	// (SMSG_LOGOUT_COMPLETE) before the list answer and then goes on reading: the session is over, and the wait must
	// end on that packet rather than on the bound.
	endWith string

	// what the client sent, in order
	enums      int
	creates    []serverCreateRequest
	deletes    []wire.GUID
	nameChecks []string
	timeSyncs  int
}

func newFakeCharScreen(t *testing.T, chars ...fakeChar) (*fakeServer, *fakeCharScreen) {
	t.Helper()
	f := newFakeServer(t)
	cs := &fakeCharScreen{f: f, chars: chars, sessions: 1}
	f.charScreen = cs
	f.wg.Add(1)
	go f.serveRealm()
	return f, cs
}

// moreSessions is the accept loop's question after one connection ended: one more session to serve, or none.
func (cs *fakeCharScreen) moreSessions() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.sessions--
	return cs.sessions > 0
}

func (cs *fakeCharScreen) options(f *fakeServer) Options {
	return Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Deadline:        defaultCharScreenDeadline,
		BuildAuthKey:    f.buildKey[:],
	}
}

// serve is the realm socket from SMSG_AUTH_RESPONSE on.
func (cs *fakeCharScreen) serve(c *fakeConn) {
	for {
		w, payload, err := c.readFrame()
		if err != nil {
			return // the client closed, or this fake dropped it on purpose
		}
		switch w {
		case fakeCMSGEnumCharacters:
			if len(payload) != 0 {
				cs.f.failf("char screen: CMSG_ENUM_CHARACTERS carries %d bytes", len(payload))
				return
			}
			cs.mu.Lock()
			cs.enums++
			count := cs.enums
			closeNow := cs.closeAt == "list" || (cs.closeAt == "list-after" && count >= 2)
			timeSync := cs.timeSyncBeforeList
			silent := cs.silent
			delay := cs.listDelay
			endWith := cs.endWith
			cs.mu.Unlock()
			if silent {
				continue // read the client's close, answer nothing
			}
			switch endWith {
			case "login_failed":
				c.writeFrame(fakeSMSGCharacterLoginFailed, []byte{1}) // NoWorld
				continue
			case "logout_complete":
				c.writeFrame(fakeSMSGLogoutComplete, []byte{0}) // OptionalInit(SwitchGameMode) false, flushed
				continue
			}
			if closeNow {
				c.nc.Close()
				return
			}
			if timeSync {
				c.writeFrame(fakeSMSGTimeSyncRequest, []byte{9, 0, 0, 0})
				w2, p2, err := c.readFrame()
				if err != nil {
					return
				}
				if w2 != fakeCMSGTimeSyncResponse || len(p2) != 8 {
					cs.f.failf("char screen: expected CMSG_TIME_SYNC_RESPONSE 8 bytes, got 0x%06X %d bytes", w2, len(p2))
					return
				}
				cs.mu.Lock()
				cs.timeSyncs++
				cs.mu.Unlock()
			}
			if delay > 0 {
				time.Sleep(delay) // spend the run's time the way a slow auth and list would
			}
			c.writeFrame(fakeSMSGEnumCharacters, cs.listPayload())
		case fakeCMSGCreateCharacter:
			req, err := serverReadCreateCharacter(payload)
			if err != nil {
				cs.f.failf("char screen: CMSG_CREATE_CHARACTER: %v", err)
				return
			}
			if req.Trailing != 0 {
				cs.f.failf("char screen: CMSG_CREATE_CHARACTER has %d trailing bytes", req.Trailing)
				return
			}
			cs.mu.Lock()
			cs.creates = append(cs.creates, req)
			closeNow := cs.closeAt == "create"
			silentCreate := cs.silentCreate
			code, guid := cs.createAnswerLocked(req)
			cs.mu.Unlock()
			if silentCreate {
				continue // the client's timeout, not an answer
			}
			if closeNow {
				c.nc.Close()
				return
			}
			c.writeFrame(fakeSMSGCreateChar, createCharPayload(code, guid))
		case fakeCMSGCharDelete:
			b := wire.NewReader(payload)
			guid := wire.ReadPackedGUID(b)
			if err := b.Err(); err != nil || b.Remaining() != 0 {
				cs.f.failf("char screen: CMSG_CHAR_DELETE % X", payload)
				return
			}
			cs.mu.Lock()
			cs.deletes = append(cs.deletes, guid)
			closeNow := cs.closeAt == "delete"
			silentDelete := cs.silentDelete
			code := cs.delete
			if code == 0 {
				code = fakeCharDeleteSuccess
			}
			if code == fakeCharDeleteSuccess {
				cs.removeLocked(guid)
			}
			cs.mu.Unlock()
			if silentDelete {
				continue // the client's timeout, not an answer
			}
			if closeNow {
				c.nc.Close()
				return
			}
			c.writeFrame(fakeSMSGDeleteChar, []byte{byte(code), byte(code >> 8), byte(code >> 16), byte(code >> 24)})
		case fakeCMSGCheckName:
			seq, name, _, trailing, err := serverReadNameCheck(payload)
			if err != nil || trailing != 0 {
				cs.f.failf("char screen: CMSG_CHECK_CHARACTER_NAME_AVAILABILITY: %v (%d trailing)", err, trailing)
				return
			}
			cs.mu.Lock()
			cs.nameChecks = append(cs.nameChecks, name)
			closeNow := cs.closeAt == "name"
			code := cs.nameAnswerLocked(name)
			cs.mu.Unlock()
			if closeNow {
				c.nc.Close()
				return
			}
			c.writeFrame(fakeSMSGCheckName, []byte{byte(seq), byte(seq >> 8), byte(seq >> 16), byte(seq >> 24), byte(code), byte(code >> 8), byte(code >> 16), byte(code >> 24)})
		default:
			cs.f.failf("char screen: unexpected client wire 0x%06X", w)
			return
		}
	}
}

// createAnswerLocked mirrors the create handler's decisions this round needs: the configured refusal, a name already
// on the account (CHAR_CREATE_NAME_IN_USE, CharacterHandler.cpp:990-1000) and a name CheckPlayerName would refuse
// (CHAR_NAME_INVALID_CHARACTER, ObjectMgr.cpp:9172-9193). A created character joins the list with the GUID the
// success answer names.
func (cs *fakeCharScreen) createAnswerLocked(req serverCreateRequest) (uint32, wire.GUID) {
	if cs.create != 0 {
		return cs.create, wire.EmptyGUID
	}
	if !nameIsLetters(req.Name) {
		return 101, wire.EmptyGUID // CHAR_NAME_INVALID_CHARACTER
	}
	for _, c := range cs.chars {
		if strings.EqualFold(c.Name, req.Name) {
			return 27, wire.EmptyGUID // CHAR_CREATE_NAME_IN_USE
		}
	}
	guid := fakeCharGUID(uint64(len(cs.chars) + 1))
	cs.chars = append(cs.chars, fakeChar{GUID: guid, Name: req.Name, Surname: req.Surname, Race: req.Race, Class: req.Class, Sex: req.Sex})
	return fakeCharCreateSuccess, guid
}

func (cs *fakeCharScreen) nameAnswerLocked(name string) uint32 {
	if cs.nameCode != 0 {
		return cs.nameCode
	}
	if !nameIsLetters(name) {
		return 101 // CHAR_NAME_INVALID_CHARACTER
	}
	for _, c := range cs.chars {
		if strings.EqualFold(c.Name, name) {
			return 27 // CHAR_CREATE_NAME_IN_USE
		}
	}
	return 0 // RESPONSE_SUCCESS
}

func (cs *fakeCharScreen) removeLocked(guid wire.GUID) {
	for i, c := range cs.chars {
		if c.GUID == guid {
			cs.chars = append(cs.chars[:i], cs.chars[i+1:]...)
			return
		}
	}
}

func nameIsLetters(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < 'A' || (r > 'Z' && r < 'a') || r > 'z' {
			return false
		}
	}
	return true
}

func (cs *fakeCharScreen) listPayload() []byte {
	cs.mu.Lock()
	chars := append([]fakeChar(nil), cs.chars...)
	cs.mu.Unlock()
	return buildEnumCharactersPayload(chars)
}

// createCharPayload is CreateChar::Write (CharacterPackets.cpp:523-529): uint32 code, packed GUID.
func createCharPayload(code uint32, guid wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteU32(code)
	wire.WritePackedGUID(b, guid)
	return b.Bytes()
}

// buildEnumCharactersPayload writes SMSG_ENUM_CHARACTERS_RESULT for regionwide entries, following
// EnumCharactersResult::Write and the CharacterInfoBasic operator<< (CharacterPackets.cpp:217-340) and the
// regionwide trailer (CharacterPackets.cpp:373-384). The parser was pinned against the real capture already; this
// is the writer that lets a test change the server's list between two requests.
func buildEnumCharactersPayload(chars []fakeChar) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)  // Success
	b.WriteBit(true)  // Realmless (every login character is regionwide, CharacterHandler.cpp:494-538)
	b.WriteBit(false) // IsDeletedCharacters
	b.WriteBit(false) // IgnoreNewPlayerRestrictions
	b.WriteBit(false) // IsRestrictedNewPlayer
	b.WriteBit(false) // IsNewcomerChatCompleted
	b.WriteBit(false) // IsRestrictedTrial
	b.WriteBit(false) // IsAccountLapsedPlayer
	b.WriteBit(false) // OptionalInit(ClassDisableMask)
	b.WriteBit(false) // ForceCharacterListSort
	b.WriteU32(0)     // realm Characters
	b.WriteU32(uint32(len(chars)))
	b.WriteI32(1) // MaxCharacterLevel
	b.WriteU32(0) // RaceUnlockData
	b.WriteU32(0) // UnlockedConditionalAppearances
	b.WriteU32(0) // RaceLimitDisables
	b.WriteU32(0) // WarbandGroups
	for i, c := range chars {
		wire.WritePackedGUID(b, c.GUID)
		b.WriteU32(0x46010046) // VirtualRealmAddress
		b.WriteU16(uint16(i))
		b.WriteU8(c.Race)
		b.WriteU8(c.Sex)
		b.WriteU8(c.Class)
		b.WriteU16(0) // SpecID
		b.WriteU32(0) // Customizations
		b.WriteU8(1)  // ExperienceLevel
		b.WriteI32(0) // MapID
		b.WriteI32(0) // ZoneID
		b.WriteFloat32(1.25)
		b.WriteFloat32(2.5)
		b.WriteFloat32(3.75)
		b.WriteU64(0)                           // GuildClubMemberID
		wire.WritePackedGUID(b, wire.EmptyGUID) // GuildGUID
		b.WriteU32(0)                           // Flags
		b.WriteU32(0)                           // Flags2
		b.WriteU32(0)                           // Flags3
		b.WriteU32(0)                           // Flags4
		b.WriteU8(0)                            // CantLoginReason
		b.WriteU32(0)                           // PetCreatureDisplayID
		b.WriteU32(0)                           // PetExperienceLevel
		b.WriteU32(0)                           // PetCreatureFamilyID
		b.WriteBytes(make([]byte, 19*23))       // VisualItems
		b.WriteI32(49)                          // SaveVersion
		b.WriteU64(1700000000)                  // CreateTime
		b.WriteU64(1700000000)                  // LastActiveTime
		b.WriteI32(0)                           // LastLoginVersion
		b.WriteBytes(make([]byte, 5*4))         // PersonalTabard
		b.WriteU32(0)                           // ProfessionIds[0]
		b.WriteU32(0)                           // ProfessionIds[1]
		b.WriteI32(0)                           // TimerunningSeasonID
		b.WriteU32(0)                           // OverrideSelectScreenFileDataID
		b.WriteU32(0)                           // RealmQueue
		b.WriteI32(0)                           // SuperDistrictID
		b.WriteBits(uint64(len(c.Name)), 6)
		b.WriteBits(uint64(len(c.Surname)), 6)
		b.WriteBit(false) // FirstLogin
		b.WriteBit(true)  // RealmInfoFound
		b.WriteBit(false) // IsRealmOffline
		b.FlushBits()
		b.WriteString(c.Name)
		b.WriteString(c.Surname)
		b.WriteU64(0) // Money
		b.WriteFloat32(0)
		b.WriteFloat32(0)
		b.WriteI32(0) // CurrentSeasonBestPvpRating
		b.WriteU8(0)  // PvpRatingBracket
		b.WriteU16(0) // PvpRatingAssociatedSpecID
	}
	return b.Bytes()
}

// ---- tests ----

func TestCreateCharacterAgainstFakeServer(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "XXXXXX", Surname: "XXXX", Race: 1, Class: 1, Sex: 0})
	defer f.close()

	spec := CreateSpec{
		Options:      cs.options(f),
		Name:         "Agtalpha",
		Race:         1,
		Class:        1,
		Sex:          0,
		NoAppearance: true,
	}
	report, err := CreateCharacter(t.Context(), spec)
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	f.wait(t)

	if report.Code != fakeCharCreateSuccess || report.CodeName != "CHAR_CREATE_SUCCESS" {
		t.Fatalf("code %d %s", report.Code, report.CodeName)
	}
	if len(report.Before) != 1 || report.Before[0].Name != "XXXXXX" {
		t.Fatalf("before: %+v", report.Before)
	}
	if len(report.After) != 2 {
		t.Fatalf("after: %d characters", len(report.After))
	}
	found := false
	for _, c := range report.After {
		if c.Name == "Agtalpha" && c.GUID == report.GUID {
			found = true
			if c.Race != 1 || c.Class != 1 || c.Sex != 0 {
				t.Fatalf("the list entry is race %d class %d sex %d", c.Race, c.Class, c.Sex)
			}
		}
	}
	if !found {
		t.Fatalf("the new character is not in the second list: %+v", report.After)
	}
	if len(cs.creates) != 1 {
		t.Fatalf("%d create packets", len(cs.creates))
	}
	got := cs.creates[0]
	if got.Name != "Agtalpha" || got.Race != 1 || got.Class != 1 || got.Sex != 0 || len(got.Choices) != 0 || got.Trailing != 0 {
		t.Fatalf("the server read %+v", got)
	}
	if got.Skip != -1 || got.Season != 0 {
		t.Fatalf("skip %d season %d", got.Skip, got.Season)
	}
}

func TestCreateCharacterRefusals(t *testing.T) {
	existing := fakeChar{GUID: fakeCharGUID(0), Name: "Agttaken", Race: 1, Class: 1}
	cases := []struct {
		name     string
		charName string
		create   uint32
		wantCode uint32
		wantName string
	}{
		{"name taken", "Agttaken", 0, 27, "CHAR_CREATE_NAME_IN_USE"},
		{"name invalid", "Agt1", 0, 101, "CHAR_NAME_INVALID_CHARACTER"},
		{"account limit", "Agtlimit", 31, 31, "CHAR_CREATE_ACCOUNT_LIMIT"},
		{"realm limit", "Agtrealm", 30, 30, "CHAR_CREATE_SERVER_LIMIT"},
		{"race disabled", "Agtrace", 28, 28, "CHAR_CREATE_DISABLED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, cs := newFakeCharScreen(t, existing)
			defer f.close()
			cs.mu.Lock()
			cs.create = c.create
			cs.mu.Unlock()
			spec := CreateSpec{Options: cs.options(f), Name: c.charName, Race: 1, Class: 1, NoAppearance: true}
			report, err := CreateCharacter(context.Background(), spec)
			if err == nil {
				t.Fatal("the refusal was accepted")
			}
			var refused *CreateRefusedError
			if !errors.As(err, &refused) || refused.Code != c.wantCode {
				t.Fatalf("error %v (%T)", err, err)
			}
			if !strings.Contains(err.Error(), c.wantName) {
				t.Fatalf("the error does not name %s: %v", c.wantName, err)
			}
			if report == nil || report.CodeName != c.wantName || len(report.After) != 0 {
				t.Fatalf("report %+v", report)
			}
			f.wait(t)
		})
	}
}

func TestDeleteCharacterAgainstFakeServer(t *testing.T) {
	victim := fakeChar{GUID: fakeCharGUID(3), Name: "Agtgone", Race: 1, Class: 1}
	other := fakeChar{GUID: fakeCharGUID(4), Name: "Agtkeep", Race: 1, Class: 1}
	f, cs := newFakeCharScreen(t, victim, other)
	defer f.close()
	cs.mu.Lock()
	cs.sessions = 2 // one session per call below
	cs.mu.Unlock()
	report, err := DeleteCharacter(t.Context(), DeleteSpec{Options: cs.options(f), Name: "Agtgone", Confirm: "Agtgone"})
	if err != nil {
		t.Fatalf("DeleteCharacter: %v", err)
	}

	if report.Code != fakeCharDeleteSuccess || report.CodeName != "CHAR_DELETE_SUCCESS" {
		t.Fatalf("code %d %s", report.Code, report.CodeName)
	}
	if report.GUID != victim.GUID {
		t.Fatalf("resolved guid %s, want %s", report.GUID, victim.GUID)
	}
	cs.mu.Lock()
	deletes := append([]wire.GUID(nil), cs.deletes...)
	cs.mu.Unlock()
	if len(deletes) != 1 || deletes[0] != victim.GUID {
		t.Fatalf("the server read deletes %v", deletes)
	}
	if len(report.Before) != 2 || len(report.After) != 1 || report.After[0].Name != "Agtkeep" {
		t.Fatalf("before %+v after %+v", report.Before, report.After)
	}

	// by GUID, the form the brief names
	report, err = DeleteCharacter(t.Context(), DeleteSpec{Options: cs.options(f), GUID: other.GUID, Confirm: "Agtkeep"})
	if err != nil {
		t.Fatalf("DeleteCharacter by GUID: %v", err)
	}
	f.wait(t)
	if len(report.After) != 0 {
		t.Fatalf("after: %+v", report.After)
	}
}

func TestDeleteCharacterRefusals(t *testing.T) {
	cases := []struct {
		name     string
		code     uint32
		wantName string
	}{
		{"failed", 68, "CHAR_DELETE_FAILED"},
		{"guild leader", 70, "CHAR_DELETE_FAILED_GUILD_LEADER"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			victim := fakeChar{GUID: fakeCharGUID(5), Name: "Agtstuck", Race: 1, Class: 1}
			f, cs := newFakeCharScreen(t, victim)
			defer f.close()
			cs.mu.Lock()
			cs.delete = c.code
			cs.mu.Unlock()
			report, err := DeleteCharacter(t.Context(), DeleteSpec{Options: cs.options(f), GUID: victim.GUID, Confirm: "Agtstuck"})
			if err == nil {
				t.Fatal("the refusal was accepted")
			}
			var refused *DeleteRefusedError
			if !errors.As(err, &refused) || refused.Code != c.code {
				t.Fatalf("error %v (%T)", err, err)
			}
			if !strings.Contains(err.Error(), c.wantName) || report.CodeName != c.wantName {
				t.Fatalf("report %+v, error %v", report, err)
			}
			if len(report.After) != 0 {
				t.Fatalf("a refused delete listed the characters again: %+v", report.After)
			}
			f.wait(t)
		})
	}
}

// TestDeleteCharacterNeedsConfirmation: the delete resolves the character but sends nothing unless Confirm repeats
// its full name. A first name alone does not match a character that has a surname.
func TestDeleteCharacterNeedsConfirmation(t *testing.T) {
	victim := fakeChar{GUID: fakeCharGUID(6), Name: "Agtguard", Surname: "Keep", Race: 1, Class: 1}
	f, cs := newFakeCharScreen(t, victim)
	defer f.close()
	cs.mu.Lock()
	cs.sessions = 3 // one session per call below
	cs.mu.Unlock()

	report, err := DeleteCharacter(t.Context(), DeleteSpec{Options: cs.options(f), Name: "Agtguard"})
	var unconfirmed *DeleteUnconfirmedError
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("a delete without a confirmation: %v", err)
	}
	if unconfirmed.Character.GUID != victim.GUID || report.CodeName != "" {
		t.Fatalf("resolved %+v, report %+v", unconfirmed.Character, report)
	}
	want := "would delete Agtguard Keep (level 1, " + victim.GUID.String() + "); pass --yes Agtguard Keep"
	if unconfirmed.Error() != "world: "+want {
		t.Fatalf("the unconfirmed error is %q", unconfirmed.Error())
	}

	// a short --yes does not match the full name either
	_, err = DeleteCharacter(t.Context(), DeleteSpec{Options: cs.options(f), GUID: victim.GUID, Confirm: "Agtguard"})
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("a short confirmation: %v", err)
	}

	report, err = DeleteCharacter(t.Context(), DeleteSpec{Options: cs.options(f), Name: "agtguard", Surname: "KEEP", Confirm: "Agtguard Keep"})
	if err != nil {
		t.Fatalf("the confirmed delete: %v", err)
	}
	if report.CodeName != "CHAR_DELETE_SUCCESS" || len(report.After) != 0 {
		t.Fatalf("report %+v", report)
	}
	cs.mu.Lock()
	deletes := append([]wire.GUID(nil), cs.deletes...)
	cs.mu.Unlock()
	if len(deletes) != 1 || deletes[0] != victim.GUID {
		t.Fatalf("the server read deletes %v", deletes)
	}
	f.wait(t)
}

// TestCharacterScreenRefreshFailure: the server's SUCCESS already decided the operation, so a failed follow-up list is
// a note on a successful report, not an error a caller retries (the retry would then say "no character named").
func TestCharacterScreenRefreshFailure(t *testing.T) {
	base := fakeChar{GUID: fakeCharGUID(0), Name: "XXXXXX", Race: 1, Class: 1}

	t.Run("create", func(t *testing.T) {
		f, cs := newFakeCharScreen(t, base)
		defer f.close()
		cs.mu.Lock()
		cs.closeAt = "list-after"
		cs.mu.Unlock()
		report, err := CreateCharacter(t.Context(), CreateSpec{Options: cs.options(f), Name: "Agtrefresh", Race: 1, Class: 1, NoAppearance: true})
		if err != nil {
			t.Fatalf("a successful create with a failed list: %v", err)
		}
		if report.CodeName != "CHAR_CREATE_SUCCESS" || !errors.Is(report.RefreshErr, ErrLinkLost) {
			t.Fatalf("report %+v", report)
		}
		if report.After != nil {
			t.Fatalf("after: %+v", report.After)
		}
		f.wait(t)
	})

	t.Run("delete", func(t *testing.T) {
		victim := fakeChar{GUID: fakeCharGUID(7), Name: "Agtrefresh", Race: 1, Class: 1}
		f, cs := newFakeCharScreen(t, victim)
		defer f.close()
		cs.mu.Lock()
		cs.closeAt = "list-after"
		cs.mu.Unlock()
		report, err := DeleteCharacter(t.Context(), DeleteSpec{Options: cs.options(f), Name: "Agtrefresh", Confirm: "Agtrefresh"})
		if err != nil {
			t.Fatalf("a successful delete with a failed list: %v", err)
		}
		if report.CodeName != "CHAR_DELETE_SUCCESS" || !errors.Is(report.RefreshErr, ErrLinkLost) {
			t.Fatalf("report %+v", report)
		}
		f.wait(t)
	})
}

func TestCheckCharacterNameAgainstFakeServer(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "Agttaken", Race: 1, Class: 1})
	defer f.close()
	cs.mu.Lock()
	cs.sessions = 2 // one session per check below
	cs.mu.Unlock()
	res, err := CheckCharacterName(t.Context(), NameCheckSpec{Options: cs.options(f), Sequence: 4, Name: "Agtfree"})
	if err != nil || res.Sequence != 4 || res.Code != 0 {
		t.Fatalf("check: %+v, %v", res, err)
	}
	if _, err := CheckCharacterName(t.Context(), NameCheckSpec{Options: cs.options(f), Sequence: 5, Name: "Agttaken"}); err == nil {
		t.Fatal("a taken name was reported available")
	} else {
		var refused *NameRefusedError
		if !errors.As(err, &refused) || refused.Code != 27 {
			t.Fatalf("error %v (%T)", err, err)
		}
	}
	f.wait(t)
	if len(cs.nameChecks) != 2 || cs.nameChecks[0] != "Agtfree" || cs.nameChecks[1] != "Agttaken" {
		t.Fatalf("the server read %v", cs.nameChecks)
	}
}

// TestCharacterScreenLinkDeath drops the socket at each wait: the list, the create answer, the list after a
// successful create, the delete answer and the name answer. Every one must end with ErrLinkLost, not a timeout and
// not a panic; the two drops after a send wrap it in SendLinkLostError, which keeps errors.Is(ErrLinkLost) true.
func TestCharacterScreenLinkDeath(t *testing.T) {
	base := fakeChar{GUID: fakeCharGUID(0), Name: "XXXXXX", Race: 1, Class: 1}
	cases := []struct {
		name  string
		close string
	}{
		{"list", "list"},
		{"create", "create"},
		{"list after create", "list-after"},
		{"delete", "delete"},
		{"name", "name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, cs := newFakeCharScreen(t, base)
			defer f.close()
			cs.mu.Lock()
			cs.closeAt = c.close
			cs.mu.Unlock()
			opts := cs.options(f)
			var err error
			switch c.close {
			case "delete":
				_, err = DeleteCharacter(t.Context(), DeleteSpec{Options: opts, GUID: base.GUID, Confirm: "XXXXXX"})
			case "name":
				_, err = CheckCharacterName(t.Context(), NameCheckSpec{Options: opts, Name: "Agtname"})
			case "list-after":
				// the create already succeeded, so the lost list is the report's note, not the call's error (its own
				// case is TestCharacterScreenRefreshFailure)
				report, cerr := CreateCharacter(t.Context(), CreateSpec{Options: opts, Name: "Agtlink", Race: 1, Class: 1, NoAppearance: true})
				if cerr != nil {
					t.Fatalf("the list after a successful create: %v", cerr)
				}
				err = report.RefreshErr
			default:
				_, err = CreateCharacter(t.Context(), CreateSpec{Options: opts, Name: "Agtlink", Race: 1, Class: 1, NoAppearance: true})
			}
			if !errors.Is(err, ErrLinkLost) {
				t.Fatalf("error %v, want ErrLinkLost", err)
			}
			f.wait(t)
		})
	}
}

// TestCharacterScreenAnswersTimeSync: the server may send SMSG_TIME_SYNC_REQUEST before the list answer; the client
// must answer it and still parse the list (the round-2 stay loop's rule, applied to the character screen).
func TestCharacterScreenAnswersTimeSync(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "XXXXXX", Race: 1, Class: 1})
	defer f.close()
	cs.mu.Lock()
	cs.timeSyncBeforeList = true
	cs.mu.Unlock()
	report, err := CreateCharacter(t.Context(), CreateSpec{Options: cs.options(f), Name: "Agtsync", Race: 1, Class: 1, NoAppearance: true})
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	f.wait(t)
	if cs.timeSyncs != 2 { // the list before and the list after the create
		t.Fatalf("%d time syncs answered, want 2", cs.timeSyncs)
	}
	if len(report.After) != 2 {
		t.Fatalf("after: %+v", report.After)
	}
}

// TestCharacterScreenSilentPeer: a peer that accepts the list request and never answers turns into an error at the
// run's deadline (a short one here), not a hang, and the error says so.
func TestCharacterScreenSilentPeer(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "XXXXXX", Race: 1, Class: 1})
	defer f.close()
	cs.mu.Lock()
	cs.silent = true
	cs.mu.Unlock()
	opts := cs.options(f)
	opts.Deadline = 250 * time.Millisecond
	start := time.Now()
	_, err := CreateCharacter(t.Context(), CreateSpec{Options: opts, Name: "Agtsilent", Race: 1, Class: 1, NoAppearance: true})
	if err == nil {
		t.Fatal("a silent server was accepted")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the wait took %s", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v, want the run's deadline", err)
	}
	f.wait(t)
}

// shrinkCharTiming changes the character screen's bounds for one test and restores them when it ends.
func shrinkCharTiming(t *testing.T, set func()) {
	t.Helper()
	oldScreenWait, oldDeleteWait := charScreenWait, charDeleteWait
	oldBound, oldMargin := charDeleteServerBound, charSendMargin
	set()
	t.Cleanup(func() {
		charScreenWait, charDeleteWait, charDeleteServerBound, charSendMargin = oldScreenWait, oldDeleteWait, oldBound, oldMargin
	})
}

// TestDeleteCharacterRefusedWhenTheListSpendsTheDeadline: authentication and the list take most of the run, so what
// is left cannot cover the server's pending-delete bound plus the margin. The delete must be refused before its
// packet, never sent into a run that will time out while the server may still act.
func TestDeleteCharacterRefusedWhenTheListSpendsTheDeadline(t *testing.T) {
	victim := fakeChar{GUID: fakeCharGUID(8), Name: "Agtspend", Race: 1, Class: 1}
	f, cs := newFakeCharScreen(t, victim)
	defer f.close()
	cs.mu.Lock()
	cs.listDelay = 1500 * time.Millisecond
	cs.mu.Unlock()
	shrinkCharTiming(t, func() {
		charDeleteServerBound = 400 * time.Millisecond
		charSendMargin = 200 * time.Millisecond
	})
	opts := cs.options(f)
	opts.Deadline = 2 * time.Second
	report, err := DeleteCharacter(t.Context(), DeleteSpec{Options: opts, GUID: victim.GUID, Confirm: "Agtspend"})

	var tooShort *DeadlineTooShortError
	if !errors.As(err, &tooShort) {
		t.Fatalf("the delete was not refused before its send: %v", err)
	}
	if tooShort.Remaining >= tooShort.Bound+tooShort.Margin {
		t.Fatalf("refused with %s left and a %s window", tooShort.Remaining, tooShort.Bound+tooShort.Margin)
	}
	for _, want := range []string{"400ms", "200ms", "600ms", "Deadline", "nothing was sent"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error does not say %q: %v", want, err)
		}
	}
	cs.mu.Lock()
	deletes := len(cs.deletes)
	cs.mu.Unlock()
	if deletes != 0 {
		t.Fatalf("%d delete packets were sent", deletes)
	}
	if len(report.Before) != 1 || report.Before[0].GUID != victim.GUID {
		t.Fatalf("the list before the refusal: %+v", report.Before)
	}
	f.wait(t)
}

// TestCharacterScreenRefusedByAShortDeadline: with the real bounds, an explicit Deadline shorter than the delete's
// 30 s server bound plus the 10 s margin is refused after the list, before any packet.
func TestCharacterScreenRefusedByAShortDeadline(t *testing.T) {
	victim := fakeChar{GUID: fakeCharGUID(9), Name: "Agtshort", Race: 1, Class: 1}
	base := fakeChar{GUID: fakeCharGUID(10), Name: "XXXXXX", Race: 1, Class: 1}
	f, cs := newFakeCharScreen(t, victim, base)
	defer f.close()
	cs.mu.Lock()
	cs.sessions = 2 // one session per call below
	cs.mu.Unlock()
	opts := cs.options(f)
	opts.Deadline = 5 * time.Second

	_, err := DeleteCharacter(t.Context(), DeleteSpec{Options: opts, GUID: victim.GUID, Confirm: "Agtshort"})
	var tooShort *DeadlineTooShortError
	if !errors.As(err, &tooShort) {
		t.Fatalf("the delete was not refused: %v", err)
	}
	if tooShort.Bound != 30*time.Second || tooShort.Margin != 10*time.Second {
		t.Fatalf("the refusal names a %s bound and a %s margin", tooShort.Bound, tooShort.Margin)
	}
	if tooShort.Deadline < 40*time.Second {
		t.Fatalf("the refusal asks for a Deadline of %s, under the 30 s bound plus the 10 s margin", tooShort.Deadline)
	}
	for _, want := range []string{"CMSG_CHAR_DELETE not sent", "30s", "10s", "40s"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error does not say %q: %v", want, err)
		}
	}

	_, err = CreateCharacter(t.Context(), CreateSpec{Options: opts, Name: "Agtshortc", Race: 1, Class: 1, NoAppearance: true})
	if !errors.As(err, &tooShort) {
		t.Fatalf("the create was not refused: %v", err)
	}
	if !strings.Contains(err.Error(), "CMSG_CREATE_CHARACTER not sent") {
		t.Fatalf("the create refusal does not name its packet: %v", err)
	}

	cs.mu.Lock()
	deletes, creates := len(cs.deletes), len(cs.creates)
	cs.mu.Unlock()
	if deletes != 0 || creates != 0 {
		t.Fatalf("%d delete and %d create packets were sent", deletes, creates)
	}
	f.wait(t)
}

// TestCharacterScreenTimeoutAfterTheSend: the run has the window, so the packet goes out; the answer never comes. The
// outcome is not a plain failure: the request may have happened, and only a list settles it. The fake answers
// nothing, and the loop's own timer and the socket's deadline fire for the same instant, so both are classified as
// no answer; which one wins cannot change the outcome.
func TestCharacterScreenTimeoutAfterTheSend(t *testing.T) {
	base := fakeChar{GUID: fakeCharGUID(11), Name: "XXXXXX", Race: 1, Class: 1}
	cases := []struct {
		name   string
		create bool
		want   string
	}{
		{"delete", false, "delete sent, no answer: the character may or may not be deleted: list again"},
		{"create", true, "create sent, no answer: the character may or may not have been created: list again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			shrinkCharTiming(t, func() {
				// The send window is the bound plus the margin; the answer wait is the bound, inside the run. The
				// shortened bound also keeps a -count=50 run quick.
				charScreenWait = 500 * time.Millisecond
				charDeleteServerBound = 500 * time.Millisecond
				charDeleteWait = 500 * time.Millisecond
				charSendMargin = 250 * time.Millisecond
			})
			f, cs := newFakeCharScreen(t, base)
			defer f.close()
			cs.mu.Lock()
			cs.silentCreate = c.create
			cs.silentDelete = !c.create
			cs.mu.Unlock()
			opts := cs.options(f)
			opts.Deadline = 3 * time.Second
			var err error
			if c.create {
				_, err = CreateCharacter(t.Context(), CreateSpec{Options: opts, Name: "Agtsilentc", Race: 1, Class: 1, NoAppearance: true})
			} else {
				_, err = DeleteCharacter(t.Context(), DeleteSpec{Options: opts, GUID: base.GUID, Confirm: "XXXXXX"})
			}

			var noAnswer *SentNoAnswerError
			if !errors.As(err, &noAnswer) {
				t.Fatalf("a timeout after the send is a plain failure: %v", err)
			}
			if noAnswer.Outcome() != c.want {
				t.Fatalf("the outcome is %q, want %q", noAnswer.Outcome(), c.want)
			}
			if noAnswer.Error() != "world: "+c.want {
				t.Fatalf("the error is %q", noAnswer.Error())
			}
			if noAnswer.Character == "" {
				t.Fatal("the outcome does not name the character")
			}
			cs.mu.Lock()
			sent := len(cs.deletes)
			if c.create {
				sent = len(cs.creates)
			}
			cs.mu.Unlock()
			if sent != 1 {
				t.Fatalf("%d packets were sent, want the one whose answer did not come", sent)
			}
			f.wait(t)
		})
	}
}

// TestCharacterScreenDisconnectAfterTheSend: the socket dies (EOF) after the create or delete packet went out. The
// request may have reached the server, so the outcome is named and unknown, never a plain lost link.
func TestCharacterScreenDisconnectAfterTheSend(t *testing.T) {
	base := fakeChar{GUID: fakeCharGUID(12), Name: "XXXXXX", Race: 1, Class: 1}
	cases := []struct {
		name   string
		create bool
		want   string
	}{
		{"delete", false, "delete sent, then the link was lost: the character may or may not be deleted: list again"},
		{"create", true, "create sent, then the link was lost: the character may or may not have been created: list again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, cs := newFakeCharScreen(t, base)
			defer f.close()
			cs.mu.Lock()
			if c.create {
				cs.closeAt = "create"
			} else {
				cs.closeAt = "delete"
			}
			cs.mu.Unlock()
			opts := cs.options(f)
			var err error
			if c.create {
				_, err = CreateCharacter(t.Context(), CreateSpec{Options: opts, Name: "Agtlinkc", Race: 1, Class: 1, NoAppearance: true})
			} else {
				_, err = DeleteCharacter(t.Context(), DeleteSpec{Options: opts, GUID: base.GUID, Confirm: "XXXXXX"})
			}

			var lost *SendLinkLostError
			if !errors.As(err, &lost) {
				t.Fatalf("a disconnect after the send is a plain failure: %v", err)
			}
			if !errors.Is(err, ErrLinkLost) {
				t.Fatalf("the outcome no longer wraps ErrLinkLost: %v", err)
			}
			if lost.Outcome() != c.want {
				t.Fatalf("the outcome is %q, want %q", lost.Outcome(), c.want)
			}
			if lost.Error() != "world: "+c.want {
				t.Fatalf("the error is %q", lost.Error())
			}
			cs.mu.Lock()
			sent := len(cs.deletes)
			if c.create {
				sent = len(cs.creates)
			}
			cs.mu.Unlock()
			if sent != 1 {
				t.Fatalf("%d packets were sent, want the one whose answer did not come", sent)
			}
			f.wait(t)
		})
	}
}

// TestCharacterScreenEndsOnSessionPackets: AbortLogin's SMSG_CHARACTER_LOGIN_FAILED and a logout's SMSG_LOGOUT_COMPLETE
// mean the session is over, so the wait ends on that packet instead of costing the full bound.
func TestCharacterScreenEndsOnSessionPackets(t *testing.T) {
	cases := []struct {
		name, end, want string
	}{
		{"login failed", "login_failed", "SMSG_CHARACTER_LOGIN_FAILED (NoWorld)"},
		{"logout complete", "logout_complete", "SMSG_LOGOUT_COMPLETE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "XXXXXX", Race: 1, Class: 1})
			defer f.close()
			cs.mu.Lock()
			cs.endWith = c.end
			cs.mu.Unlock()
			opts := cs.options(f)
			opts.Deadline = 10 * time.Second // the packet must end the wait well before this
			start := time.Now()
			_, err := CreateCharacter(t.Context(), CreateSpec{Options: opts, Name: "Agtend", Race: 1, Class: 1, NoAppearance: true})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want %q", err, c.want)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("the wait ran %s instead of ending on the packet", elapsed)
			}
			f.wait(t)
		})
	}
}

// TestCharacterScreenWaits pins the bounds: the server's pending delete may take 30 s
// (CharacterHandler.cpp:1253), so the delete answer waits longer than that, the other answers keep 30 s, and a send
// needs the server's bound plus a 10 s margin left, which the default run of 90 s always has room for.
func TestCharacterScreenWaits(t *testing.T) {
	if charScreenWait != 30*time.Second {
		t.Fatalf("charScreenWait %s", charScreenWait)
	}
	if charDeleteWait <= 30*time.Second {
		t.Fatalf("charDeleteWait %s is not longer than the server's 30 s pending-delete bound", charDeleteWait)
	}
	if charDeleteServerBound != 30*time.Second {
		t.Fatalf("charDeleteServerBound %s, want the server's 30 s pending-delete bound", charDeleteServerBound)
	}
	if charSendMargin != 10*time.Second {
		t.Fatalf("charSendMargin %s", charSendMargin)
	}
	if window := charDeleteServerBound + charSendMargin; defaultCharScreenDeadline <= window {
		t.Fatalf("the default run of %s does not leave room for the %s send window", defaultCharScreenDeadline, window)
	}
}

// TestCreateCharacterNeedsExplicitAppearance: the zero-choice create is not a silent default; the spec must ask for it
// (NoAppearance) or give choices, and never both, before any packet is built.
func TestCreateCharacterNeedsExplicitAppearance(t *testing.T) {
	_, err := CreateCharacter(context.Background(), CreateSpec{Name: "Agtplain", Race: 1, Class: 1})
	if err == nil || !strings.Contains(err.Error(), "no appearance") {
		t.Fatalf("a create with no appearance field: %v", err)
	}
	_, err = CreateCharacter(context.Background(), CreateSpec{
		Name: "Agtboth", Race: 1, Class: 1,
		Choices:      []CustomizationChoice{{OptionID: 1, ChoiceID: 2}},
		NoAppearance: true,
	})
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("a create with both forms: %v", err)
	}
}

// TestCreateCharacterSendsTheChoicesAsGiven: each --choice pair reaches the server unchanged, in order, decoded by the
// server reader's transcription.
func TestCreateCharacterSendsTheChoicesAsGiven(t *testing.T) {
	f, cs := newFakeCharScreen(t, fakeChar{GUID: fakeCharGUID(0), Name: "XXXXXX", Race: 1, Class: 1})
	defer f.close()
	choices := []CustomizationChoice{{OptionID: 0x0E, ChoiceID: 0x57}, {OptionID: 0x0F, ChoiceID: 0x72}}
	_, err := CreateCharacter(t.Context(), CreateSpec{
		Options: cs.options(f), Name: "Agtchoice", Race: 1, Class: 1, Choices: choices,
	})
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	f.wait(t)
	if len(cs.creates) != 1 || len(cs.creates[0].Choices) != 2 {
		t.Fatalf("the server read %+v", cs.creates)
	}
	for i, want := range choices {
		if got := cs.creates[0].Choices[i]; got != want {
			t.Fatalf("choice %d is %+v, want %+v", i, got, want)
		}
	}
}

// TestCharacterScreenArgumentChecks: nothing is sent before the local checks pass.
func TestCharacterScreenArgumentChecks(t *testing.T) {
	if _, err := CreateCharacter(context.Background(), CreateSpec{}); err == nil || !strings.Contains(err.Error(), "no character name") {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := DeleteCharacter(context.Background(), DeleteSpec{}); err == nil {
		t.Fatal("an empty delete was accepted")
	}
	if _, err := CheckCharacterName(context.Background(), NameCheckSpec{Name: string(bytes.Repeat([]byte{'A'}, 64))}); err == nil {
		t.Fatal("a 64-byte name was encoded")
	}
}
