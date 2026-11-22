// Package tbmclient drives the TBM add-on protocol over a Forever session:
// the hello on chat, the switch to the add-on transport on party, and one
// command at a time with the 350 ms send gap the module expects.
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
// add-on send, a whisper to self for the chat transport, and the next
// delivered message.
type Conn interface {
	RegisterAddonPrefixes(prefixes ...string) error
	SendAddon(ch addonmsg.Channel, target, prefix, text string, logged bool) error
	WhisperSelf(text string) error
	Next(ctx context.Context) (tbmproto.Incoming, error)
}

// ErrTimeout is the client's own timeout: the hello or a command did not
// finish in time.
var ErrTimeout = errors.New("tbmclient: timed out")

// sendGap is the gap between sends, the module's SEND_DELAY (spec D3).
const sendGap = 350 * time.Millisecond

// Client speaks TBM over one session. The transport starts as chat; the
// hello switches it to add-on when the module answers party.
type Client struct {
	conn       Conn
	transport  tbmproto.Transport
	registered bool
	lastSend   time.Time
}

// New returns a client on the chat transport.
func New(conn Conn) *Client {
	return &Client{conn: conn, transport: tbmproto.TransportChat}
}

// Transport reports the transport commands go out on.
func (c *Client) Transport() tbmproto.Transport { return c.transport }

// SetTransport forces the transport commands go out on. The hello normally
// switches it to add-on when the module answers party; a caller can also set
// it before the first send.
func (c *Client) SetTransport(t tbmproto.Transport) { c.transport = t }

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
			if h.Transport == "party" {
				c.transport = tbmproto.TransportAddon
			}
			return h, nil
		}
	}
}

// Do sends one command on the current transport and reads until the ACK or
// ERR whose intent is the command's action intent, a stream END, or the
// timeout.
func (c *Client) Do(ctx context.Context, command string, timeout time.Duration) (tbmproto.Result, error) {
	if err := c.send(ctx, c.transport, command); err != nil {
		return tbmproto.Result{}, err
	}
	r := tbmproto.NewReader()
	want, wantAck := ackIntent(command)
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
		res := r.Result()
		if wantAck {
			for _, a := range res.Acks {
				if a.Intent == want {
					r.Finish()
					return r.Result(), nil
				}
			}
			for _, e := range res.Errs {
				if e.Intent == want {
					r.Finish()
					return r.Result(), nil
				}
			}
		}
		if streamEnded(in) {
			r.Finish()
			return r.Result(), nil
		}
	}
}

// ackIntent names the intent a command's ACK or ERR carries: "action <intent>"
// answers with that intent; every other command has no matching intent.
func ackIntent(command string) (string, bool) {
	fields := strings.Fields(command)
	if len(fields) >= 2 && fields[0] == "action" {
		return fields[1], true
	}
	return "", false
}

// streamEnded reports whether a delivered message closes a stream.
func streamEnded(in tbmproto.Incoming) bool {
	if in.Kind == "addon" && in.Prefix != tbmproto.Prefix {
		return false
	}
	for _, line := range strings.Split(in.Text, "\n") {
		if !strings.HasPrefix(line, tbmproto.LinePrefix) {
			continue
		}
		typ := strings.TrimPrefix(line, tbmproto.LinePrefix)
		if i := strings.Index(typ, "|"); i >= 0 {
			typ = typ[:i]
		}
		if strings.HasSuffix(typ, "_END") {
			return true
		}
	}
	return false
}
