package world

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// The character screen: the realm socket after the world auth and before any character is logged in. A real client
// lists the account's characters here (CMSG_ENUM_CHARACTERS), creates and deletes them, and checks a name, all on
// that one socket (a local capture of a real session: the CMSG_CREATE_CHARACTER is followed by SMSG_CREATE_CHAR on
// connection 0). This round adds those three exchanges; the session type below is
// a small loop of its own, because the world session (session.go) owns a logged-in character and its two sockets.

// defaultCharScreenDeadline bounds one character-screen run when the spec sets none.
const defaultCharScreenDeadline = 90 * time.Second

// The character screen's timing bounds. They are variables, not constants, so a test can shrink them and still run
// the real paths in milliseconds; a run never writes them.
var (
	// charScreenWait is how long one expected answer may take. The server answers the list, a create, a delete and a
	// name check from its asynchronous queries in milliseconds; the bound is what turns a silent peer into an error.
	charScreenWait = 30 * time.Second

	// charDeleteWait bounds the delete answer on its own. The server may hold a delete while the character's own bot
	// leaves the world: HandleCharDeleteOpcode sets _pendingCharDeleteTimer = 30 * IN_MILLISECONDS
	// (CharacterHandler.cpp:1253), which equals charScreenWait, so a legal delete could outlast the general bound and
	// be reported as silence. 60 s is twice the server's 30 s bound.
	charDeleteWait = 60 * time.Second

	// charDeleteServerBound is the server's own bound on a pending delete: HandleCharDeleteOpcode sets
	// _pendingCharDeleteTimer = 30 * IN_MILLISECONDS (CharacterHandler.cpp:1253), whose update finishes the delete
	// when it runs out. After that the delete is settled, so a run that sends the packet with this much time left
	// can wait it out instead of reporting silence while the server still intends to act.
	charDeleteServerBound = 30 * time.Second

	// charSendMargin is the room a sent create or delete is given beyond its bound: the server's clock starts when
	// the packet arrives, not when it is sent, and the answer has to travel back. A run with less than the bound plus
	// this margin left cannot settle the request before its own deadline ends, so it refuses to send at all.
	charSendMargin = 10 * time.Second
)

// The connection fields of Options are what these calls read: Address, RealmID, RealmAddress, RealmJoinTicket,
// ClientSecret, JoinSecret, ClientBuild, RealmBuild, BuildAuthKey, AllowInstanceRedirect, Deadline and Logf. The
// character and stay fields are ignored: nothing is logged in.

// CreateSpec is CreateCharacter's input.
type CreateSpec struct {
	Options
	Name    string
	Surname string // Classic 1.60 character surname; empty is normal
	Race    uint8
	Class   uint8
	Sex     uint8
	// Choices are the customizations to send, as the caller gives them (the command's --choice OPTION:CHOICE). No
	// pair is checked against a table here: the core validates each one (ValidateAppearance,
	// CharacterHandler.cpp:769-832) and the display model comes from ChrModel.db2 (Player::InitDisplayIds,
	// Player.cpp:24797-24806).
	Choices []CustomizationChoice
	// NoAppearance asks for the zero-choice create. The core accepts it (ValidateAppearance loops over an empty
	// list and returns true), but no real game client has been seen creating such a character, so it must be asked
	// for by name, never defaulted to: create needs Choices or NoAppearance.
	NoAppearance bool
}

// DeleteSpec is DeleteCharacter's input: the GUID of the character, or a name to resolve against the account's
// character list in the same session.
//
// A delete is destructive and the server's handler has no confirmation step of its own (HandleCharDeleteOpcode,
// CharacterHandler.cpp:1233-1263): Confirm must repeat the resolved character's full name, or nothing is sent.
type DeleteSpec struct {
	Options
	GUID    wire.GUID
	Name    string
	Surname string // the surname on its own, when Name carries only the first name
	Confirm string // the resolved character's full name; empty or different sends no delete
}

// NameCheckSpec is CheckCharacterName's input.
type NameCheckSpec struct {
	Options
	Sequence uint32
	Name     string
	Surname  string
}

// CreateReport is what CreateCharacter observed: the list before, the name's answer, and the list after (which the
// caller uses to prove the character is there).
type CreateReport struct {
	Name     string
	Before   []Character
	Code     uint32
	CodeName string
	GUID     wire.GUID
	After    []Character
	// RefreshErr is set when the create succeeded but the list after it could not be read: the character exists,
	// so this is not an error, and the caller's next list is the proof.
	RefreshErr error
}

// DeleteReport is what DeleteCharacter observed.
type DeleteReport struct {
	Name     string
	GUID     wire.GUID
	Before   []Character
	Code     uint32
	CodeName string
	After    []Character
	// RefreshErr is set when the delete succeeded but the list after it could not be read, like CreateReport's.
	RefreshErr error
}

// CreateCharacter creates one character on the character screen. A refusal is a *CreateRefusedError carrying the
// server's code name; the report is still returned, with the list the refusal belongs to. When the create succeeds,
// the character list is requested again, so a caller can prove the character appears in it.
func CreateCharacter(ctx context.Context, spec CreateSpec) (*CreateReport, error) {
	if spec.Name == "" {
		return nil, errors.New("world: no character name")
	}
	switch {
	case len(spec.Choices) == 0 && !spec.NoAppearance:
		return nil, errors.New("world: no appearance: give Choices or NoAppearance (a zero-choice create is a test character's, not a real client's)")
	case len(spec.Choices) > 0 && spec.NoAppearance:
		return nil, errors.New("world: Choices and NoAppearance are exclusive")
	}
	started := time.Now()
	ctx, cancel := charScreenContext(ctx, spec.Deadline)
	defer cancel()
	payload, err := BuildCreateCharacter(spec.Name, spec.Surname, spec.Race, spec.Class, spec.Sex, spec.Choices)
	if err != nil {
		return nil, err
	}
	report := &CreateReport{Name: spec.Name}
	s, err := openCharScreen(ctx, spec.Options)
	if err != nil {
		return report, err
	}
	defer s.Close()

	if report.Before, err = s.list(ctx); err != nil {
		return report, err
	}
	// The answer bound plus the margin has to fit in the run: a create that timed out after its packet went out
	// would leave the character's existence unknown for the rest of the Deadline.
	if err := requireSendWindow(ctx, started, "CMSG_CREATE_CHARACTER", "create", "the answer bound", charScreenWait); err != nil {
		return report, err
	}
	if err := s.realm.Send(ctx, wire.CMSGCreateCharacter, payload); err != nil {
		return report, err
	}
	s.logf("char create: CMSG_CREATE_CHARACTER sent (%s, race %d, class %d, sex %d, %d customization(s))",
		bounded(spec.Name), spec.Race, spec.Class, spec.Sex, len(spec.Choices))
	answer, err := s.waitFor(ctx, wire.SMSGCreateChar, "SMSG_CREATE_CHAR", charScreenWait)
	if err != nil {
		if errors.Is(err, errNoAnswer) {
			return report, &SentNoAnswerError{Action: "create", Character: spec.Name, Answer: "SMSG_CREATE_CHAR", Wait: err}
		}
		if errors.Is(err, ErrLinkLost) {
			return report, &SendLinkLostError{Action: "create", Character: spec.Name, Wait: err}
		}
		return report, err
	}
	res, err := ParseCreateChar(answer)
	if err != nil {
		return report, err
	}
	report.Code = res.Code
	report.CodeName = CharCreateCodeName(res.Code)
	report.GUID = res.GUID
	s.logf("char create: SMSG_CREATE_CHAR %s", report.CodeName)
	if res.Code != charCreateSuccess {
		return report, &CreateRefusedError{Code: res.Code}
	}
	// The new character has to show up in the list; the report keeps both so a caller can compare them.
	if report.After, err = s.list(ctx); err != nil {
		// The server answered CHAR_CREATE_SUCCESS and the character exists; only the follow-up list failed. That is
		// the caller's confirmation step, not the create's, so the create is reported as done.
		report.RefreshErr = err
		s.logf("char create: done; the list could not be refreshed: %v", err)
		return report, nil
	}
	return report, nil
}

// DeleteCharacter deletes one character by GUID (or by name, resolved from the account's list). A refusal is a
// *DeleteRefusedError carrying the server's code name; the report is still returned. A delete that Confirm does not
// confirm is a *DeleteUnconfirmedError and sends nothing.
func DeleteCharacter(ctx context.Context, spec DeleteSpec) (*DeleteReport, error) {
	if spec.GUID.IsZero() && spec.Name == "" {
		return nil, errors.New("world: no character GUID or name to delete")
	}
	started := time.Now()
	ctx, cancel := charScreenContext(ctx, spec.Deadline)
	defer cancel()
	report := &DeleteReport{Name: spec.Name, GUID: spec.GUID}
	s, err := openCharScreen(ctx, spec.Options)
	if err != nil {
		return report, err
	}
	defer s.Close()

	if report.Before, err = s.list(ctx); err != nil {
		return report, err
	}
	character, err := resolveDeleteCharacter(report.Before, spec.GUID, spec.Name, spec.Surname)
	if err != nil {
		return report, err
	}
	report.GUID = character.GUID
	report.Name = character.DisplayName()
	if spec.Confirm != report.Name {
		return report, &DeleteUnconfirmedError{Character: character, Confirm: spec.Confirm}
	}
	// A delete is the one request the server may hold (its own 30 s pending-delete bound), so the run must have
	// that bound plus the margin left: a timeout after the packet went out would leave the character deleted or not,
	// with nothing to tell the caller which.
	if err := requireSendWindow(ctx, started, "CMSG_CHAR_DELETE", "delete", "the server's pending-delete bound", charDeleteServerBound); err != nil {
		return report, err
	}
	if err := s.realm.Send(ctx, wire.CMSGCharDelete, BuildCharDelete(report.GUID)); err != nil {
		return report, err
	}
	s.logf("char delete: CMSG_CHAR_DELETE sent for %s (%s)", bounded(report.Name), report.GUID)
	answer, err := s.waitFor(ctx, wire.SMSGDeleteChar, "SMSG_DELETE_CHAR", charDeleteWait)
	if err != nil {
		if errors.Is(err, errNoAnswer) {
			return report, &SentNoAnswerError{Action: "delete", Character: report.Name, Answer: "SMSG_DELETE_CHAR", Wait: err}
		}
		if errors.Is(err, ErrLinkLost) {
			return report, &SendLinkLostError{Action: "delete", Character: report.Name, Wait: err}
		}
		return report, err
	}
	code, err := ParseDeleteChar(answer)
	if err != nil {
		return report, err
	}
	report.Code = code
	report.CodeName = CharDeleteCodeName(code)
	s.logf("char delete: SMSG_DELETE_CHAR %s", report.CodeName)
	if code != charDeleteSuccess {
		return report, &DeleteRefusedError{Code: code}
	}
	if report.After, err = s.list(ctx); err != nil {
		// As for a create: the server answered CHAR_DELETE_SUCCESS, so the character is gone and only the list the
		// caller uses as proof failed.
		report.RefreshErr = err
		s.logf("char delete: done; the list could not be refreshed: %v", err)
		return report, nil
	}
	return report, nil
}

// resolveDeleteCharacter finds the one character a delete names and refuses none or several: every match is
// collected, and an entry without a GUID cannot be acted on and is skipped. A GUID is the exact alternative to a
// name.
func resolveDeleteCharacter(list []Character, guid wire.GUID, name, surname string) (Character, error) {
	if !guid.IsZero() {
		for _, c := range list {
			if c.GUID == guid {
				return c, nil
			}
		}
		return Character{}, &CharacterLookupError{GUID: guid}
	}
	first, inName := splitFullName(name)
	want := strings.TrimSpace(name)
	if inName != "" && surname != "" {
		return Character{}, errors.New("world: the surname is in the name and in the surname field; give it once")
	}
	if surname == "" {
		surname = inName
	} else if want != "" {
		want += " " + surname
	}
	var matches []Character
	for _, c := range list {
		if c.GUID.IsZero() {
			continue
		}
		if nameMatchesCharacter(c, first, surname) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return Character{}, &CharacterLookupError{Name: want}
	case 1:
		return matches[0], nil
	}
	return Character{}, &CharacterLookupError{Name: want, Candidates: matches}
}

// splitFullName splits a requested name at its first blank: "First Last" gives the first name and the surname. A
// one-word name has no surname.
func splitFullName(name string) (first, surname string) {
	name = strings.TrimSpace(name)
	if i := strings.IndexFunc(name, unicode.IsSpace); i >= 0 {
		return name[:i], strings.TrimSpace(name[i:])
	}
	return name, ""
}

// normalizePlayerName is the server's normalizePlayerName (ObjectMgr.cpp:157-181): trim the surrounding blanks,
// keep the first word, lower-case every letter and then upper-case the first. Names that differ only in case are the
// same name to the server, so they are the same here.
func normalizePlayerName(name string) string {
	first, _ := splitFullName(name)
	first = strings.ToLower(first)
	if first == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(first)
	return string(unicode.ToUpper(r)) + first[size:]
}

// nameMatchesCharacter reports whether c answers to the first name and the surname: the first name under the
// server's normalisation; the surname, which the server's player lookup discards (it keeps the first word), compares
// case-insensitively when one was asked for.
func nameMatchesCharacter(c Character, first, surname string) bool {
	if normalizePlayerName(c.Name) != normalizePlayerName(first) {
		return false
	}
	return surname == "" || strings.EqualFold(strings.TrimSpace(c.Surname), surname)
}

// CharacterLookupError is a delete that matched no character or several. Candidates lists the several, so a caller
// can print which names it saw; the entries can differ only in the surname.
type CharacterLookupError struct {
	GUID       wire.GUID
	Name       string
	Candidates []Character
}

func (e *CharacterLookupError) Error() string {
	if !e.GUID.IsZero() {
		return "world: the account has no character with guid " + e.GUID.String()
	}
	if len(e.Candidates) == 0 {
		return fmt.Sprintf("world: the account has no character named %q", bounded(e.Name))
	}
	names := make([]string, len(e.Candidates))
	for i, c := range e.Candidates {
		names[i] = fmt.Sprintf("%s (level %d, %s)", c.DisplayName(), c.Level, c.GUID)
	}
	return fmt.Sprintf("world: %d characters match %q: %s; pass the full name or the guid",
		len(e.Candidates), bounded(e.Name), strings.Join(names, ", "))
}

// DeleteUnconfirmedError is a delete that resolved a character but was not confirmed: Confirm did not repeat the
// character's full name, so no CMSG_CHAR_DELETE was sent. Character is the resolved one, for the caller's prompt.
type DeleteUnconfirmedError struct {
	Character Character
	Confirm   string
}

func (e *DeleteUnconfirmedError) Error() string {
	if e.Confirm == "" {
		return fmt.Sprintf("world: would delete %s (level %d, %s); pass --yes %s",
			e.Character.DisplayName(), e.Character.Level, e.Character.GUID, e.Character.DisplayName())
	}
	return fmt.Sprintf("world: --yes %q does not match %q; nothing was deleted", e.Confirm, e.Character.DisplayName())
}

// DeadlineTooShortError is a create or delete that was not sent: the run had less than the request's bound plus the
// send margin left, so the answer could have arrived after the caller's deadline ended and the outcome would have
// been unknown. Nothing was sent, so the request did not happen and a retry is safe.
type DeadlineTooShortError struct {
	What      string        // the packet that was not sent
	Verb      string        // "create" or "delete": the request in the message
	Bound     time.Duration // the server's bound on the answer, or the client's answer bound for a create
	BoundName string        // what Bound is, for the message
	Margin    time.Duration // charSendMargin: the room the send needs beyond Bound
	Remaining time.Duration // the run's time left when the send was due
	Deadline  time.Duration // the least whole-run Deadline that would have allowed the send
}

func (e *DeadlineTooShortError) Error() string {
	need := e.Bound + e.Margin
	return fmt.Sprintf("world: %s not sent: %s left and a %s needs %s after the list (%s %s plus a %s margin); nothing was sent; give a Deadline of at least %s",
		e.What, e.Remaining.Round(time.Millisecond), e.Verb, need,
		e.BoundName, e.Bound, e.Margin, e.Deadline.Round(time.Millisecond))
}

// UncertainSendError is a create or delete whose outcome is unknown: the request was sent and either no answer came
// or the link died before one did. Outcome is the whole answer a person and a script get; list again, never retry
// blind.
type UncertainSendError interface {
	error
	Outcome() string
}

// SentNoAnswerError is a create or delete that was sent and whose answer did not arrive before the run ended. The
// server may have carried the request out after the client gave up, so the outcome is unknown: the caller must list
// again, never retry blind.
type SentNoAnswerError struct {
	Action    string // "create" or "delete"
	Character string // the requested name (create) or the resolved one (delete)
	Answer    string // the answer that did not come: SMSG_CREATE_CHAR or SMSG_DELETE_CHAR
	Wait      error  // the wait that ended, for the log
}

func (e *SentNoAnswerError) Error() string { return "world: " + e.Outcome() }

// Outcome is exactly what is known, and what settles it.
func (e *SentNoAnswerError) Outcome() string {
	switch e.Action {
	case "delete":
		return "delete sent, no answer: the character may or may not be deleted: list again"
	case "create":
		return "create sent, no answer: the character may or may not have been created: list again"
	}
	return fmt.Sprintf("%s sent, no answer: list again", e.Action)
}

func (e *SentNoAnswerError) Unwrap() error { return e.Wait }

// SendLinkLostError is a create or delete that was sent and whose link died before the answer came. The request may
// have reached the server, so its outcome is unknown too; it is named apart from a lost link with nothing sent.
type SendLinkLostError struct {
	Action    string // "create" or "delete"
	Character string // the requested name (create) or the resolved one (delete)
	Wait      error  // the lost link, still ErrLinkLost for errors.Is
}

func (e *SendLinkLostError) Error() string { return "world: " + e.Outcome() }

// Outcome is exactly what is known, and what settles it.
func (e *SendLinkLostError) Outcome() string {
	switch e.Action {
	case "delete":
		return "delete sent, then the link was lost: the character may or may not be deleted: list again"
	case "create":
		return "create sent, then the link was lost: the character may or may not have been created: list again"
	}
	return fmt.Sprintf("%s sent, then the link was lost: list again", e.Action)
}

func (e *SendLinkLostError) Unwrap() error { return e.Wait }

// CheckCharacterName asks the server whether a name can be used (CMSG_CHECK_CHARACTER_NAME_AVAILABILITY). A taken
// or invalid name is a *NameRefusedError with the server's code name.
func CheckCharacterName(ctx context.Context, spec NameCheckSpec) (NameCheckResult, error) {
	ctx, cancel := charScreenContext(ctx, spec.Deadline)
	defer cancel()
	payload, err := BuildCheckCharacterNameAvailability(spec.Sequence, spec.Name, spec.Surname)
	if err != nil {
		return NameCheckResult{}, err
	}
	s, err := openCharScreen(ctx, spec.Options)
	if err != nil {
		return NameCheckResult{}, err
	}
	defer s.Close()

	if err := s.realm.Send(ctx, wire.CMSGCheckCharacterNameAvailability, payload); err != nil {
		return NameCheckResult{}, err
	}
	s.logf("char name: CMSG_CHECK_CHARACTER_NAME_AVAILABILITY sent (%s)", bounded(spec.Name))
	answer, err := s.waitFor(ctx, wire.SMSGCheckCharacterNameAvailabilityResult, "SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT", charScreenWait)
	if err != nil {
		return NameCheckResult{}, err
	}
	res, err := ParseCheckCharacterNameAvailabilityResult(answer)
	if err != nil {
		return NameCheckResult{}, err
	}
	s.logf("char name: SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT %s", CharNameCodeName(res.Code))
	if res.Code != responseSuccess {
		return res, &NameRefusedError{Code: res.Code}
	}
	return res, nil
}

// The success codes. CHAR_CREATE_SUCCESS and CHAR_DELETE_SUCCESS are the enum's values; RESPONSE_SUCCESS is the
// name check's "available" (CharacterHandler.cpp:2057-2066).
const (
	charCreateSuccess uint32 = 24 // CHAR_CREATE_SUCCESS, SharedDefines.h:6805
	charDeleteSuccess uint32 = 67 // CHAR_DELETE_SUCCESS, SharedDefines.h:6849
	responseSuccess   uint32 = 0  // RESPONSE_SUCCESS
)

// charScreen is one authenticated realm socket on the character screen.
type charScreen struct {
	opts  Options
	realm *worldconn.Realm
}

// charScreenContext bounds one run: a caller's deadline when it sets one, the default otherwise.
func charScreenContext(ctx context.Context, deadline time.Duration) (context.Context, context.CancelFunc) {
	if deadline <= 0 {
		deadline = defaultCharScreenDeadline
	}
	return context.WithTimeout(ctx, deadline)
}

// requireSendWindow refuses a create or delete before it is sent unless the run still has its bound plus
// charSendMargin left. A send made with less could get its answer after the run's deadline ended, and a timeout with
// the packet already out is an outcome the client cannot settle; refusing here keeps a retry safe. started is when
// the run's context was made, so the error can name the whole-run Deadline that would have been enough.
func requireSendWindow(ctx context.Context, started time.Time, what, verb, boundName string, bound time.Duration) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil // no deadline to outlive
	}
	remaining := time.Until(deadline)
	if remaining >= bound+charSendMargin {
		return nil
	}
	if remaining < 0 {
		remaining = 0
	}
	return &DeadlineTooShortError{
		What:      what,
		Verb:      verb,
		Bound:     bound,
		BoundName: boundName,
		Margin:    charSendMargin,
		Remaining: remaining,
		Deadline:  time.Since(started) + bound + charSendMargin,
	}
}

func openCharScreen(ctx context.Context, opts Options) (*charScreen, error) {
	if opts.Deadline <= 0 {
		opts.Deadline = defaultCharScreenDeadline
	}
	realm, err := worldconn.Authenticate(ctx, worldconn.Config{
		Address:               opts.Address,
		RealmID:               opts.RealmID,
		RealmAddress:          opts.RealmAddress,
		RealmJoinTicket:       opts.RealmJoinTicket,
		ClientSecret:          opts.ClientSecret,
		JoinSecret:            opts.JoinSecret,
		ClientBuild:           opts.ClientBuild,
		RealmBuild:            opts.RealmBuild,
		BuildAuthKey:          opts.BuildAuthKey,
		AllowInstanceRedirect: opts.AllowInstanceRedirect,
		Deadline:              opts.Deadline,
		Logf:                  opts.Logf,
	})
	if err != nil {
		return nil, err
	}
	return &charScreen{opts: opts, realm: realm}, nil
}

func (s *charScreen) Close() error { return s.realm.Close() }

func (s *charScreen) logf(format string, args ...any) { s.opts.logf(format, args...) }

// list sends CMSG_ENUM_CHARACTERS and parses the answer: the list the client shows (the regionwide one when the
// server put the characters there, which it does for every login character — CharacterHandler.cpp:494-538).
func (s *charScreen) list(ctx context.Context) ([]Character, error) {
	if err := s.realm.Send(ctx, wire.CMSGEnumCharacters, nil); err != nil {
		return nil, err
	}
	s.logf("char screen: CMSG_ENUM_CHARACTERS sent")
	answer, err := s.waitFor(ctx, wire.SMSGEnumCharactersResult, "SMSG_ENUM_CHARACTERS_RESULT", charScreenWait)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseEnumCharactersResult(answer)
	if err != nil {
		return nil, err
	}
	characters := parsed.All()
	s.logf("char screen: %d character(s), max level %d, %d trailing bytes", len(characters), parsed.MaxLevel, parsed.TrailingBytes)
	for _, c := range characters {
		s.logf("  character %s: level %d, class %d, race %d, map %d", bounded(c.DisplayName()), c.Level, c.Class, c.Race, c.MapID)
	}
	return characters, nil
}

// errNoAnswer marks a wait that ended because its bound or the run's deadline ran out, as opposed to a lost link or a
// cancelled run: only the first is a timeout after a request was sent.
var errNoAnswer = errors.New("no answer")

// isTimeout reports whether err is a read that hit its deadline. The socket's timer and the context's are armed for
// the same instant and either can win, so a raw i/o timeout here is this loop's wait ending, not a lost link.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// waitFor reads server packets until the wanted one arrives, answering the time syncs the server sends on either
// socket (the only packet of the character screen that expects an answer) and ending at once on the packets that
// mean the session is over. Every other packet is skipped: the auth answer's companions and the character list's own
// companions arrive unasked and mean nothing here.
func (s *charScreen) waitFor(ctx context.Context, core uint32, what string, wait time.Duration) ([]byte, error) {
	deadline := time.Now().Add(wait)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("world: waiting for %s: %w within %s", what, errNoAnswer, wait)
		}
		opCtx, cancel := context.WithTimeout(ctx, remaining)
		p, err := s.realm.Next(opCtx)
		cancel()
		if err != nil {
			switch {
			case ctx.Err() != nil:
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, fmt.Errorf("world: waiting for %s: %w: %w", what, errNoAnswer, ctx.Err())
				}
				return nil, fmt.Errorf("world: waiting for %s: %w", what, ctx.Err())
			case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
				// The socket's timer can fire just before the context's for the same instant, so a read timeout is
				// this wait ending. When the run's own deadline has passed, keep that in the error.
				if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
					return nil, fmt.Errorf("world: waiting for %s: %w: %w", what, errNoAnswer, context.DeadlineExceeded)
				}
				return nil, fmt.Errorf("world: waiting for %s: %w within %s", what, errNoAnswer, wait)
			}
			return nil, fmt.Errorf("%w: waiting for %s: %v", ErrLinkLost, what, err)
		}
		if p.Core == core {
			return p.Payload, nil
		}
		switch p.Core {
		case wire.SMSGCharacterLoginFailed:
			// AbortLogin sends this when a login cannot proceed (CharacterHandler.cpp:1400-1409): the session is
			// going nowhere, so waiting out the bound would only cost the caller the wait.
			return nil, fmt.Errorf("world: waiting for %s: the server ended the session: SMSG_CHARACTER_LOGIN_FAILED (%s)",
				what, loginFailureReason(p.Payload))
		case wire.SMSGLogoutComplete:
			// a kick or a logout the server completed (WorldSession::LogoutPlayer, WorldSession.cpp:767): the realm
			// socket has nothing left to answer.
			return nil, fmt.Errorf("world: waiting for %s: the server ended the session: SMSG_LOGOUT_COMPLETE", what)
		}
		if p.Core == wire.SMSGTimeSyncRequest {
			seq, err := ParseTimeSyncRequest(p.Payload)
			if err != nil {
				return nil, err
			}
			if err := s.realm.Send(ctx, wire.CMSGTimeSyncResponse, BuildTimeSyncResponse(seq, clientTimeMs())); err != nil {
				return nil, err
			}
			s.logf("char screen: SMSG_TIME_SYNC_REQUEST answered")
		}
	}
}

// loginFailureReasons names the one byte SMSG_CHARACTER_LOGIN_FAILED carries (LoginFailureReason,
// CharacterPackets.h:611-621).
var loginFailureReasons = map[byte]string{
	0: "Failed",
	1: "NoWorld",
	2: "DuplicateCharacter",
	3: "NoInstances",
	4: "Disabled",
	5: "NoCharacter",
	6: "LockedForTransfer",
	7: "LockedByBilling",
	8: "LockedByMobileAH",
}

func loginFailureReason(payload []byte) string {
	if len(payload) == 0 {
		return "no reason byte"
	}
	if name, ok := loginFailureReasons[payload[0]]; ok {
		return name
	}
	return fmt.Sprintf("reason %d", payload[0])
}

// bounded keeps a name short enough for one log line; character names are already bounded, a command's input is
// not.
func bounded(s string) string {
	if len(s) > 64 {
		return s[:64] + "..."
	}
	return s
}
