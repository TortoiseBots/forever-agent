package tbmclient

import (
	"context"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
)

// FakeModule is a Conn that answers from the golden cases: a frame equal to a
// case's frame answers that case's following recv records. A reply for an
// unregistered prefix is dropped, as the core drops it.
type FakeModule struct {
	cases []tbmproto.Case
	// IgnoreRegistration makes RegisterAddonPrefixes a no-op, so the client's
	// sends are dropped and the hello times out (the failing case).
	IgnoreRegistration bool

	registered map[string]bool
	queue      []tbmproto.Incoming
}

// NewFakeModule returns a fake module over the parsed cases.
func NewFakeModule(cases []tbmproto.Case) *FakeModule {
	return &FakeModule{cases: cases, registered: map[string]bool{}}
}

// RegisterAddonPrefixes records the prefixes unless registration is ignored.
func (f *FakeModule) RegisterAddonPrefixes(prefixes ...string) error {
	if f.IgnoreRegistration {
		return nil
	}
	for _, p := range prefixes {
		f.registered[p] = true
	}
	return nil
}

// SendAddon answers an add-on frame. A prefix the client did not register is
// dropped, as the core drops a delivery of an unregistered prefix.
func (f *FakeModule) SendAddon(ch addonmsg.Channel, target, prefix, text string, logged bool) error {
	if !f.registered[prefix] {
		return nil
	}
	return f.answer(tbmproto.Frame{Kind: "addon", Channel: "PARTY", Prefix: prefix, Text: text})
}

// SendAddonSelf answers an add-on whisper to self. The core echoes the request
// back to its sender before the module's answer, so that echo is queued first:
// the hello's ignored count depends on it. A prefix the client did not register
// is dropped, as the core drops an add-on delivery of an unregistered prefix.
func (f *FakeModule) SendAddonSelf(prefix, text string) error {
	if !f.registered[prefix] {
		return nil
	}
	if err := f.answer(tbmproto.Frame{Kind: "addon", Channel: "WHISPER_SELF", Prefix: prefix, Text: text}); err != nil {
		return err
	}
	echo := tbmproto.Incoming{Kind: "addon", Channel: "WHISPER", Prefix: prefix, Text: text}
	f.queue = append([]tbmproto.Incoming{echo}, f.queue...)
	return nil
}

// WhisperSelf answers a chat frame. A whisper to self is not filtered by the
// prefix registration, so it always answers.
func (f *FakeModule) WhisperSelf(text string) error {
	return f.answer(tbmproto.Frame{Kind: "chat", Channel: "WHISPER_SELF", Text: text})
}

// Next returns the next queued message; with an empty queue it waits for the
// context's deadline.
func (f *FakeModule) Next(ctx context.Context) (tbmproto.Incoming, error) {
	if len(f.queue) > 0 {
		in := f.queue[0]
		f.queue = f.queue[1:]
		return in, nil
	}
	<-ctx.Done()
	return tbmproto.Incoming{}, ctx.Err()
}

// answer matches the frame to a case and queues its recv records.
func (f *FakeModule) answer(frame tbmproto.Frame) error {
	for i := range f.cases {
		c := &f.cases[i]
		for _, rec := range c.Records {
			if rec.Kind != "frame" || !frameMatches(frame, rec.Fields) {
				continue
			}
			f.queue = f.queue[:0]
			for _, r := range c.Records {
				if r.Kind == "recv" {
					f.queue = append(f.queue, incoming(r))
				}
			}
			return nil
		}
	}
	return nil
}

// frameMatches compares a frame with a case's frame fields.
func frameMatches(frame tbmproto.Frame, fields []string) bool {
	if fields[0] == "addon" {
		return frame.Kind == "addon" && frame.Channel == fields[1] &&
			frame.Prefix == fields[2] && frame.Text == fields[3]
	}
	return frame.Kind == "chat" && frame.Channel == fields[1] && frame.Text == fields[2]
}

// incoming maps a case's recv record to a delivered message.
func incoming(rec tbmproto.Record) tbmproto.Incoming {
	if rec.Fields[0] == "addon" {
		return tbmproto.Incoming{Kind: "addon", Channel: rec.Fields[1], Prefix: rec.Fields[2], Text: rec.Fields[3]}
	}
	return tbmproto.Incoming{Kind: "system", Text: rec.Fields[1]}
}
