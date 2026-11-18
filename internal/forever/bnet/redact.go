package bnet

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Everything this package hands back from a successful login except the realm description is a secret or personal
// data. The types below therefore format themselves with lengths only, so a stray %v, %+v or %#v in a caller's
// debug output cannot print a ticket, a session key or a password.

// maxServerText is the longest piece of server-supplied text an error may carry.
const maxServerText = 120

// longSecretRun matches a run that looks like a hex or base64 value; the server text this client quotes (an error
// code, a message) has no such run, while an echoed request, ticket or key does.
var longSecretRun = regexp.MustCompile(`[A-Za-z0-9+/=]{32,}`)

// sanitizeServerText bounds and cleans text that came from the server before it goes into an error: printable ASCII
// only, at most 120 bytes, and anything that looks like a long hex or base64 run replaced by "<redacted>".
func sanitizeServerText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '\n' || c == '\r' || c == '\t':
			b.WriteByte(' ')
		case c >= 0x20 && c <= 0x7E:
			b.WriteByte(c)
		default:
			b.WriteByte('?')
		}
	}
	out := longSecretRun.ReplaceAllString(b.String(), "<redacted>")
	if len(out) > maxServerText {
		out = out[:maxServerText-3] + "..."
	}
	return out
}

// Result redacts its secrets in every format verb: only the realm description is printed.
func (r Result) String() string   { return r.redacted() }
func (r Result) GoString() string { return r.redacted() }

func (r Result) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, r.redacted()) }

func (r Result) redacted() string {
	return fmt.Sprintf(
		"bnet.Result{realm=%q id=%d address=0x%08X sub-region=%q world=%s:%d login-ticket=<redacted %d bytes> join-ticket=<redacted %d bytes> session-key=<redacted %d bytes> client-secret=<redacted %d bytes> join-secret=<redacted %d bytes>}",
		sanitizeServerText(r.RealmName), r.RealmID, r.RealmAddress, sanitizeServerText(r.SubRegion),
		sanitizeServerText(r.ServerIP), r.ServerPort,
		len(r.LoginTicket), len(r.JoinTicket), len(r.SessionKey), len(r.ClientSecret), len(r.JoinSecret))
}

// Options holds the password, so it redacts the account and the password in every format verb.
func (o Options) String() string   { return o.redacted() }
func (o Options) GoString() string { return o.redacted() }

func (o Options) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, o.redacted()) }

func (o Options) redacted() string {
	return fmt.Sprintf(
		"bnet.Options{rest=%q rpc=%q ca=%q server-name=%q account=<redacted %d chars> password=<redacted> build=%d timeout=%s deadline=%s}",
		sanitizeServerText(o.RESTAddr), sanitizeServerText(o.RPCAddr), sanitizeServerText(o.CAFile),
		sanitizeServerText(o.ServerName), len(o.Account), o.ClientBuild, o.Timeout, o.Deadline)
}

// LogonResult carries the 64-byte session key and the account's battle tag.
func (m LogonResult) String() string   { return m.redacted() }
func (m LogonResult) GoString() string { return m.redacted() }

func (m LogonResult) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.redacted()) }

func (m LogonResult) redacted() string {
	return fmt.Sprintf("bnet.LogonResult{error=%d account=%d game-accounts=%d battle-tag=<redacted> session-key=<redacted %d bytes>}",
		m.ErrorCode, m.AccountID.Low, len(m.GameAccountIDs), len(m.SessionKey))
}

// srpClient holds the client private, the shared secret and both proofs.
func (s srpClient) String() string   { return s.redacted() }
func (s srpClient) GoString() string { return s.redacted() }

func (s srpClient) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.redacted()) }

func (s srpClient) redacted() string {
	return fmt.Sprintf("bnet.srpClient{group=%d bits public-A=%d bits salt=%d bytes private-and-session-values=<redacted>}",
		s.n.BitLen(), s.A.BitLen(), len(s.salt))
}
