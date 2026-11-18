package world

import (
	"fmt"
	"io"
)

// Options holds the join ticket, both key halves, the build auth key and the character name. It formats itself with
// lengths only in every verb, so a stray %v, %+v or %#v cannot print a secret or a player's name.

// String, GoString and Format keep the secrets out of every format verb.
func (o Options) String() string   { return o.redacted() }
func (o Options) GoString() string { return o.redacted() }

func (o Options) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, o.redacted()) }

func (o Options) redacted() string {
	return fmt.Sprintf(
		"world.Options{address=%q realm=%d realm-address=0x%08X client-build=%d realm-build=%d join-ticket=<redacted %d bytes> client-secret=<redacted %d bytes> join-secret=<redacted %d bytes> build-auth-key=<redacted %d bytes> character=<redacted %d chars> stay=%s deadline=%s allow-instance-redirect=%t}",
		boundedText(o.Address), o.RealmID, o.RealmAddress, o.ClientBuild, o.RealmBuild,
		len(o.RealmJoinTicket), len(o.ClientSecret), len(o.JoinSecret), len(o.BuildAuthKey),
		len(o.Character), o.Stay, o.Deadline, o.AllowInstanceRedirect)
}

// boundedText keeps a caller-supplied host:port printable and short, like worldconn's.
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
