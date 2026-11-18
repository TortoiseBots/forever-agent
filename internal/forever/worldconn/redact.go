package worldconn

import (
	"fmt"
	"io"
)

// The world socket types hold the join ticket, the two halves of the key data and the derived session key. Like the
// round-1 login types, they format themselves with lengths only, so a stray %v, %+v or %#v in a caller's debug
// output cannot print a secret. This is belt and braces: today no call site formats them.

// Config redacts the join ticket, both key halves and the build auth key in every format verb.
func (c Config) String() string   { return c.redacted() }
func (c Config) GoString() string { return c.redacted() }

func (c Config) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.redacted()) }

func (c Config) redacted() string {
	return fmt.Sprintf(
		"worldconn.Config{address=%q realm=%d realm-address=0x%08X client-build=%d realm-build=%d join-ticket=<redacted %d bytes> client-secret=<redacted %d bytes> join-secret=<redacted %d bytes> build-auth-key=<redacted %d bytes> allow-instance-redirect=%t deadline=%s}",
		boundedText(c.Address), c.RealmID, c.RealmAddress, c.ClientBuild, c.RealmBuild,
		len(c.RealmJoinTicket), len(c.ClientSecret), len(c.JoinSecret), len(c.BuildAuthKey),
		c.AllowInstanceRedirect, c.Deadline)
}

// Realm redacts the 40-byte session key and the connection (whose crypt holds the AES key).
func (r Realm) String() string   { return r.redacted() }
func (r Realm) GoString() string { return r.redacted() }

func (r Realm) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, r.redacted()) }

func (r Realm) redacted() string {
	return fmt.Sprintf("worldconn.Realm{session-key=<redacted %d bytes> connection=<redacted>}", len(r.sessionKey))
}

// Instance redacts the connection (whose crypt holds the AES key).
func (i Instance) String() string   { return i.redacted() }
func (i Instance) GoString() string { return i.redacted() }

func (i Instance) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, i.redacted()) }

func (i Instance) redacted() string {
	return "worldconn.Instance{connection=<redacted>}"
}

// boundedText keeps a caller-supplied address printable and short; it may be a host name a caller took from a
// configuration file, not server text, so the login package's account scrubber does not apply.
func boundedText(s string) string {
	const max = 120
	out := make([]byte, 0, min(len(s), max)+3)
	for i := 0; i < len(s) && len(out) < max; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7E {
			c = '?'
		}
		out = append(out, c)
	}
	if len(s) > len(out) {
		return string(out) + "..."
	}
	return string(out)
}
