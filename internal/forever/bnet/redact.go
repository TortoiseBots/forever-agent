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

// maxServerPath is the longest server-supplied path this client requests: the fixed form path is a handful of bytes,
// so a megabyte-long srp_url is not a path.
const maxServerPath = 512

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

// printableASCII reports whether s is non-empty and every byte is printable ASCII: no control byte, no ESC and no
// multibyte sequence, which is what a path from the server must be before it is logged or requested.
func printableASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// serverTextError keeps an error chain (errors.Is/As still work) but prints a bounded, sanitised version of the
// message: a TLS or HTTP transport error can quote names from the server's certificate, which is server data.
type serverTextError struct {
	prefix string
	err    error
}

func (e *serverTextError) Error() string {
	return sanitizeServerText(e.prefix) + sanitizeServerText(e.err.Error())
}
func (e *serverTextError) Unwrap() error { return e.err }

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

// logonResult carries the 64-byte session key and the account's battle tag.
func (m logonResult) String() string   { return m.redacted() }
func (m logonResult) GoString() string { return m.redacted() }

func (m logonResult) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.redacted()) }

func (m logonResult) redacted() string {
	return fmt.Sprintf("bnet.logonResult{error=%d account=%d game-accounts=%d battle-tag=<redacted> session-key=<redacted %d bytes>}",
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

// logonRequest carries the login ticket as cached_web_credentials; only its length is printed.
func (m logonRequest) String() string   { return m.redacted() }
func (m logonRequest) GoString() string { return m.redacted() }

func (m logonRequest) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.redacted()) }

func (m logonRequest) redacted() string {
	return fmt.Sprintf(
		"bnet.logonRequest{program=%q platform=%q locale=%q build=%d device-id=<redacted %d chars> login-ticket=<redacted %d bytes>}",
		m.Program, m.Platform, m.Locale, m.ApplicationVersion, len(m.DeviceID), len(m.CachedWebCredentials))
}

// variant is an attribute value; a blob or a string can be a join secret or a ticket, so both print as lengths.
func (v variant) String() string   { return v.redacted() }
func (v variant) GoString() string { return v.redacted() }

func (v variant) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, v.redacted()) }

func (v variant) redacted() string {
	switch {
	case v.BoolValue != nil:
		return fmt.Sprintf("bnet.variant{bool=%t}", *v.BoolValue)
	case v.IntValue != nil:
		return fmt.Sprintf("bnet.variant{int=%d}", *v.IntValue)
	case v.FloatValue != nil:
		return fmt.Sprintf("bnet.variant{float=%g}", *v.FloatValue)
	case v.StringValue != nil:
		return fmt.Sprintf("bnet.variant{string=<redacted %d chars>}", len(*v.StringValue))
	case v.BlobValue != nil:
		return fmt.Sprintf("bnet.variant{blob=<redacted %d bytes>}", len(v.BlobValue))
	case v.UintValue != nil:
		return fmt.Sprintf("bnet.variant{uint=%d}", *v.UintValue)
	}
	return "bnet.variant{}"
}

// attribute wraps a variant, so it prints the name and the redacted variant.
func (a attribute) String() string   { return a.redacted() }
func (a attribute) GoString() string { return a.redacted() }

func (a attribute) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, a.redacted()) }

func (a attribute) redacted() string {
	return fmt.Sprintf("bnet.attribute{name=%q value=%s}", sanitizeServerText(a.Name), a.Value.redacted())
}

// clientRequest holds the attributes whose blobs carry the join secret and the tickets.
func (m clientRequest) String() string   { return m.redacted() }
func (m clientRequest) GoString() string { return m.redacted() }

func (m clientRequest) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.redacted()) }

func (m clientRequest) redacted() string {
	return fmt.Sprintf("bnet.clientRequest{attributes=%d values=<redacted>}", len(m.Attributes))
}

// clientResponse holds the attributes the server answered with (tickets, the join secret).
func (m clientResponse) String() string   { return m.redacted() }
func (m clientResponse) GoString() string { return m.redacted() }

func (m clientResponse) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.redacted()) }

func (m clientResponse) redacted() string {
	return fmt.Sprintf("bnet.clientResponse{attributes=%d values=<redacted>}", len(m.Attributes))
}

// loginFormValue carries A and M1 of the SRP proof in Value.
func (v loginFormValue) String() string   { return v.redacted() }
func (v loginFormValue) GoString() string { return v.redacted() }

func (v loginFormValue) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, v.redacted()) }

func (v loginFormValue) redacted() string {
	return fmt.Sprintf("bnet.loginFormValue{input-id=%q value=<redacted %d chars>}", sanitizeServerText(v.InputID), len(v.Value))
}

// loginForm contains the proof values, so each input prints through loginFormValue's redaction.
func (m loginForm) String() string   { return m.redacted() }
func (m loginForm) GoString() string { return m.redacted() }

func (m loginForm) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, m.redacted()) }

func (m loginForm) redacted() string {
	var b strings.Builder
	fmt.Fprintf(&b, "bnet.loginForm{platform=%q program=%q version=%q inputs=[",
		m.PlatformID, m.ProgramID, m.Version)
	for i, in := range m.Inputs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(in.redacted())
	}
	b.WriteString("]}")
	return b.String()
}

// loginResultJSON carries the login ticket and the server evidence.
func (r loginResultJSON) String() string   { return r.redacted() }
func (r loginResultJSON) GoString() string { return r.redacted() }

func (r loginResultJSON) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, r.redacted()) }

func (r loginResultJSON) redacted() string {
	return fmt.Sprintf(
		"bnet.loginResultJSON{state=%q error-code=%q error-message=%q next-url=%q login-ticket=<redacted %d chars> server-evidence-M2=<redacted %d chars>}",
		sanitizeServerText(r.AuthenticationState), sanitizeServerText(r.ErrorCode),
		sanitizeServerText(r.ErrorMessage), sanitizeServerText(r.NextURL), len(r.LoginTicket), len(r.ServerEvidenceM2))
}

// joinAnswer carries the join ticket and the 32-byte join secret.
func (j joinAnswer) String() string   { return j.redacted() }
func (j joinAnswer) GoString() string { return j.redacted() }

func (j joinAnswer) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, j.redacted()) }

func (j joinAnswer) redacted() string {
	return fmt.Sprintf("bnet.joinAnswer{join-ticket=<redacted %d bytes> addresses=<redacted %d bytes> join-secret=<redacted %d bytes>}",
		len(j.ticket), len(j.addresses), len(j.secret))
}

// clientInformation carries the 32-byte client secret the world key is made from.
func (c clientInformation) String() string   { return c.redacted() }
func (c clientInformation) GoString() string { return c.redacted() }

func (c clientInformation) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.redacted()) }

func (c clientInformation) redacted() string {
	return fmt.Sprintf(
		"bnet.clientInformation{platform=0x%08X build-variant=%q type=0x%08X build=%d locales=0x%08X/0x%08X secret=<redacted %d bytes>}",
		c.Platform, sanitizeServerText(c.BuildVariant), c.Type, c.VersionDataBuild, c.TextLocale, c.AudioLocale, len(c.Secret))
}

// realmListTicketClientInformation wraps clientInformation, which fmt would not format as an unexported field value.
func (c realmListTicketClientInformation) String() string   { return c.redacted() }
func (c realmListTicketClientInformation) GoString() string { return c.redacted() }

func (c realmListTicketClientInformation) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, c.redacted())
}

func (c realmListTicketClientInformation) redacted() string {
	return "bnet.realmListTicketClientInformation{info=" + c.Info.redacted() + "}"
}

// rpcSession keeps the logon result (session key) in an unexported field, which fmt does not format through that
// type's methods; the session therefore redacts it.
func (s rpcSession) String() string   { return s.redacted() }
func (s rpcSession) GoString() string { return s.redacted() }

func (s rpcSession) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.redacted()) }

func (s rpcSession) redacted() string {
	logon := "<none>"
	if s.logon != nil {
		logon = "<redacted>"
	}
	return fmt.Sprintf("bnet.rpcSession{ciid=%q token=%d logon=%s}", sanitizeServerText(s.ciid), s.token, logon)
}
