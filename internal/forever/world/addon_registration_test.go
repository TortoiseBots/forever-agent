package world

// Round A0's registration-state tests: registration is two packets, and a failure between them must not leave the
// old prefixes exposed as registered. A failed second send leaves the state unknown (no prefix reported, arrived
// messages counted as unregistered, sends that wait for an answer refused) until a full unregister+register
// succeeds; a failed first send leaves everything as it was.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// errAddonSend is the write error the fake transport returns. It is not a socket error, so sendOn hands it back
// unchanged: the tests see exactly the error a refused send produces.
var errAddonSend = errors.New("test: the instance socket refused the write")

// addonTransport is the session's transport without sockets: it records every client opcode and fails the send
// indices the test names.
type addonTransport struct {
	sent []uint32
	fail map[int]error
}

func (t *addonTransport) Send(core uint32, _ []byte) error {
	i := len(t.sent)
	t.sent = append(t.sent, core)
	if err, ok := t.fail[i]; ok {
		return err
	}
	return nil
}

func (t *addonTransport) WaitUntil(context.Context, time.Time) error { return nil }
func (t *addonTransport) Now() time.Time                             { return time.Now() }

func (t *addonTransport) count(core uint32) int {
	n := 0
	for _, c := range t.sent {
		if c == core {
			n++
		}
	}
	return n
}

// addonTestSession is a session without sockets whose transport the test can fail.
func addonTestSession() (*session, *addonTransport) {
	tr := &addonTransport{}
	s := &session{
		opts:        Options{Logf: func(string, ...any) {}},
		ctx:         context.Background(),
		world:       objects.NewWorld(0),
		report:      &Report{Counts: map[string]int{}},
		fightSender: tr,
	}
	return s, tr
}

// addonArrived is one delivered add-on line for prefix, the shape the chat handler hands to offerAddon.
func addonArrived(prefix, text string) chat.Line {
	return chat.Line{
		Type:       chat.TypeParty,
		Language:   addonmsg.LanguageAddon,
		SenderGUID: wire.GUID{Low: 0x77},
		Prefix:     prefix,
		Text:       text,
	}
}

// TestAddonRegistrationSecondSendFails: the unregister-all packet leaves and the registration packet does not.
func TestAddonRegistrationSecondSendFails(t *testing.T) {
	s, tr := addonTestSession()
	if err := s.RegisterAddonPrefixes("Old:"); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if got := s.addon.Registered(); !slices.Equal(got, []string{"Old:"}) {
		t.Fatalf("registered list %v", got)
	}
	s.offerAddon(addonArrived("Old:", "BEFORE"))
	if m, ok := s.addon.Next(); !ok || m.Text != "BEFORE" {
		t.Fatalf("a message before the failure was not delivered: %+v ok=%v", m, ok)
	}

	// The next two sends are the unregister-all (index len(sent)) and the registration (index len(sent)+1).
	tr.fail = map[int]error{len(tr.sent) + 1: errAddonSend}
	if err := s.RegisterAddonPrefixes("New:"); !errors.Is(err, errAddonSend) {
		t.Fatalf("failed registration returned %v, want the send error", err)
	}
	if got := s.addon.Registered(); len(got) != 0 {
		t.Fatalf("a failed registration still reports %v as registered", got)
	}
	if s.addon.Has("Old:") {
		t.Fatal("the old prefix is still on the local list")
	}
	if s.addonState != addonRegistrationUnknown {
		t.Fatalf("registration state %v, want unknown", s.addonState)
	}

	// A send that waits for an answer is refused by name, and nothing reaches the transport.
	sends := tr.count(wire.CMSGChatAddonMessage) + tr.count(wire.CMSGChatAddonMessageTargeted)
	if err := s.SendAddon(addonmsg.Party, "", "New:", "SENT", false); !errors.Is(err, addonmsg.ErrRegistrationUnknown) {
		t.Fatalf("a send with an unknown registration returned %v", err)
	}
	if n := tr.count(wire.CMSGChatAddonMessage) + tr.count(wire.CMSGChatAddonMessageTargeted); n != sends {
		t.Fatalf("%d add-on message(s) reached a socket with an unknown registration", n-sends)
	}

	// An arrived message is counted with the unregistered ones and never delivered.
	unregistered := s.addon.Unregistered
	s.offerAddon(addonArrived("New:", "WHILE-UNKNOWN"))
	if s.addon.Unregistered != unregistered+1 {
		t.Fatalf("unregistered %d after one message in the unknown state, want %d", s.addon.Unregistered, unregistered+1)
	}
	if m, ok := s.addon.Next(); ok {
		t.Fatalf("a message was delivered while the registration state was unknown: %+v", m)
	}

	// The report says unknown and names no prefix.
	s.summariseAddon(addonDrained{})
	if !s.report.Addon.RegistrationUnknown {
		t.Fatal("the report does not mark the unknown registration state")
	}
	if len(s.report.Addon.Registered) != 0 {
		t.Fatalf("the report still claims %v as registered", s.report.Addon.Registered)
	}

	// A later full unregister+register restores the list, the send and the delivery.
	tr.fail = nil
	if err := s.RegisterAddonPrefixes("New:"); err != nil {
		t.Fatalf("second registration: %v", err)
	}
	if got := s.addon.Registered(); !slices.Equal(got, []string{"New:"}) {
		t.Fatalf("registered list after the retry %v", got)
	}
	if s.addonState != addonRegistrationKnown {
		t.Fatalf("registration state after the retry %v, want known", s.addonState)
	}
	if err := s.SendAddon(addonmsg.Party, "", "New:", "AFTER", false); err != nil {
		t.Fatalf("a send after the retry: %v", err)
	}
	s.offerAddon(addonArrived("New:", "DELIVERED"))
	if m, ok := s.addon.Next(); !ok || m.Text != "DELIVERED" {
		t.Fatalf("delivery after the retry: %+v ok=%v", m, ok)
	}
	s.summariseAddon(addonDrained{})
	if s.report.Addon.RegistrationUnknown || !slices.Equal(s.report.Addon.Registered, []string{"New:"}) {
		t.Fatalf("the report after the retry: unknown=%v registered=%v",
			s.report.Addon.RegistrationUnknown, s.report.Addon.Registered)
	}
}

// TestAddonRegistrationFirstSendFails: the unregister-all packet itself is refused, so nothing on either side
// changed and the previous registration keeps working.
func TestAddonRegistrationFirstSendFails(t *testing.T) {
	s, tr := addonTestSession()
	if err := s.RegisterAddonPrefixes("Keep:"); err != nil {
		t.Fatalf("first registration: %v", err)
	}

	unregisters := tr.count(wire.CMSGChatUnregisterAllAddonPrefixes)
	register := tr.count(wire.CMSGChatRegisterAddonPrefixes)
	tr.fail = map[int]error{len(tr.sent): errAddonSend}
	if err := s.RegisterAddonPrefixes("Other:"); !errors.Is(err, errAddonSend) {
		t.Fatalf("failed unregister-all returned %v, want the send error", err)
	}
	if n := tr.count(wire.CMSGChatUnregisterAllAddonPrefixes); n != unregisters+1 {
		t.Fatalf("unregister-all packets %d, want %d", n, unregisters+1)
	}
	if n := tr.count(wire.CMSGChatRegisterAddonPrefixes); n != register {
		t.Fatalf("a registration packet followed a failed unregister-all (%d, want %d)", n, register)
	}

	if s.addonState != addonRegistrationKnown {
		t.Fatalf("registration state %v after a failed first send, want known", s.addonState)
	}
	if got := s.addon.Registered(); !slices.Equal(got, []string{"Keep:"}) {
		t.Fatalf("registered list %v after a failed first send", got)
	}
	if err := s.SendAddon(addonmsg.Party, "", "Keep:", "SEND", false); err != nil {
		t.Fatalf("a send after a failed first send: %v", err)
	}
	s.offerAddon(addonArrived("Keep:", "STILL"))
	if m, ok := s.addon.Next(); !ok || m.Text != "STILL" {
		t.Fatalf("delivery after a failed first send: %+v ok=%v", m, ok)
	}
}

// TestAddonRegistrationUnknownErrorNamesTransport: the error is the named sentinel, so a caller can tell it from a
// bad prefix or text, which are still checked after the state is known.
func TestAddonRegistrationUnknownErrorNamesState(t *testing.T) {
	s, tr := addonTestSession()
	tr.fail = map[int]error{1: errAddonSend}
	if err := s.RegisterAddonPrefixes("P:"); err == nil {
		t.Fatal("the registration did not fail")
	}
	err := s.SendAddon(addonmsg.Party, "", "P:", "X", false)
	if !errors.Is(err, addonmsg.ErrRegistrationUnknown) || err.Error() != addonmsg.ErrRegistrationUnknown.Error() {
		t.Fatalf("send error %v", err)
	}
	if err := fmt.Errorf("world: %w", err); !errors.Is(err, addonmsg.ErrRegistrationUnknown) {
		t.Fatalf("the sentinel does not survive wrapping: %v", err)
	}
}
