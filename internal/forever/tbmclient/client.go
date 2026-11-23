// Package tbmclient drives the TBM add-on protocol over a Forever session:
// the hello as an add-on whisper to self while the transport is unknown, the
// switch to PARTY once the module answers party, and one command at a time
// with the 350 ms send gap the module expects.
package tbmclient

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
)

// Conn is the session the client speaks through: prefix registration, the
// add-on send (PARTY), the add-on whisper to self, a whisper to self for the
// chat transport, and the next delivered message.
type Conn interface {
	RegisterAddonPrefixes(prefixes ...string) error
	SendAddon(ch addonmsg.Channel, target, prefix, text string, logged bool) error
	SendAddonSelf(prefix, text string) error
	WhisperSelf(text string) error
	Next(ctx context.Context) (tbmproto.Incoming, error)
}

// ErrTimeout is the client's own timeout: the hello or a command did not
// finish in time.
var ErrTimeout = errors.New("tbmclient: timed out")

// sendGap is the gap between sends, the module's SEND_DELAY (spec D3).
const sendGap = 350 * time.Millisecond

// Client speaks TBM over one session. The transport starts as addon-self;
// the hello switches it to add-on when the module answers party.
type Client struct {
	conn       Conn
	transport  tbmproto.Transport
	forced     bool
	registered bool
	lastSend   time.Time
}

// New returns a client on the addon-self transport: an add-on whisper to self
// is the way to reach the module while the transport is unknown.
func New(conn Conn) *Client {
	return &Client{conn: conn, transport: tbmproto.TransportAddonSelf}
}

// Transport reports the transport commands go out on.
func (c *Client) Transport() tbmproto.Transport { return c.transport }

// SetTransport forces the transport commands go out on, for a caller that
// pinned one; the hello then leaves it alone instead of following
// TRANSPORT. Without it the hello decides: party sends on add-on, none keeps
// addon-self.
func (c *Client) SetTransport(t tbmproto.Transport) {
	c.transport = t
	c.forced = true
}

// send registers the prefix once and encodes the command on the transport.
// It waits the send gap after the previous send.
func (c *Client) send(ctx context.Context, t tbmproto.Transport, command string) error {
	if !c.registered {
		if err := c.conn.RegisterAddonPrefixes(tbmproto.Prefix); err != nil {
			return err
		}
		c.registered = true
	}
	if !c.lastSend.IsZero() {
		if wait := sendGap - time.Since(c.lastSend); wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	f, err := tbmproto.Encode(t, command)
	if err != nil {
		return err
	}
	c.lastSend = time.Now()
	if f.Kind == "addon" {
		if f.Channel == "WHISPER_SELF" {
			return c.conn.SendAddonSelf(f.Prefix, f.Text)
		}
		return c.conn.SendAddon(addonmsg.Party, "", f.Prefix, f.Text, false)
	}
	return c.conn.WhisperSelf(f.Text)
}

// Hello sends roster and reads the trailer until TRANSPORT. It checks the
// trailer and switches to add-on when the module answers party.
func (c *Client) Hello(ctx context.Context, timeout time.Duration) (tbmproto.Hello, error) {
	if err := c.send(ctx, c.transport, tbmproto.Roster()); err != nil {
		return tbmproto.Hello{}, err
	}
	r := tbmproto.NewReader()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return r.Result().Hello, ErrTimeout
		}
		nctx, cancel := context.WithDeadline(ctx, deadline)
		in, err := c.conn.Next(nctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return r.Result().Hello, ErrTimeout
			}
			return r.Result().Hello, err
		}
		r.Feed(in)
		h := r.Result().Hello
		if h.Complete {
			if err := h.Check(); err != nil {
				return h, err
			}
			if !c.forced {
				switch h.Transport {
				case "party":
					c.transport = tbmproto.TransportAddon
				case "none":
					c.transport = tbmproto.TransportAddonSelf
				}
			}
			return h, nil
		}
	}
}

// Do sends one command on the current transport and reads until the command
// is answered: roster and list end at the TRANSPORT line, alts at ALTSUM_END,
// every other command at the ACK or ERR whose intent is its own or "-".
func (c *Client) Do(ctx context.Context, command string, timeout time.Duration) (tbmproto.Result, error) {
	if err := c.send(ctx, c.transport, command); err != nil {
		return tbmproto.Result{}, err
	}
	r := tbmproto.NewReader()
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			r.Finish()
			return r.Result(), ErrTimeout
		}
		nctx, cancel := context.WithDeadline(ctx, deadline)
		in, err := c.conn.Next(nctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				r.Finish()
				return r.Result(), ErrTimeout
			}
			r.Finish()
			return r.Result(), err
		}
		r.Feed(in)
		if commandDone(in, command, r.Result()) {
			r.Finish()
			return r.Result(), nil
		}
	}
}

// commandIntent names the intent a command's ACK or ERR carries: "action
// <intent>" answers with that intent; every other command with its first word.
func commandIntent(command string) string {
	fields := strings.Fields(command)
	if len(fields) >= 2 && fields[0] == "action" {
		return fields[1]
	}
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// commandDone reports whether the last message answers the command.
func commandDone(in tbmproto.Incoming, command string, res tbmproto.Result) bool {
	first := ""
	if fields := strings.Fields(command); len(fields) > 0 {
		first = fields[0]
	}
	switch first {
	case "roster", "list":
		return res.Hello.Complete
	case "alts":
		return hasLine(in, "ALTSUM_END")
	}
	want := commandIntent(command)
	for _, a := range res.Acks {
		if a.Intent == want || a.Intent == "-" {
			return true
		}
	}
	for _, e := range res.Errs {
		if e.Intent == want || e.Intent == "-" {
			return true
		}
	}
	return false
}

// hasLine reports whether a delivered message carries a TBM line of type typ.
func hasLine(in tbmproto.Incoming, typ string) bool {
	if in.Kind == "addon" && in.Prefix != tbmproto.Prefix {
		return false
	}
	for _, line := range strings.Split(in.Text, "\n") {
		rest, ok := strings.CutPrefix(line, tbmproto.LinePrefix)
		if !ok {
			continue
		}
		if i := strings.Index(rest, "|"); i >= 0 {
			rest = rest[:i]
		}
		if rest == typ {
			return true
		}
	}
	return false
}
