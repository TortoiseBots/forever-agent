package worldconn

import (
	"fmt"
	"strings"
	"testing"
)

// Config, Realm and Instance must not print a ticket, a key half, the build key or the session key under any verb,
// including nested in another value. This is the round-1 login-type check applied to the world socket.
func TestWorldSocketTypesRedactEveryVerb(t *testing.T) {
	secretA := "JOIN-TICKET-SECRET-AAAAAAAAAAAAAAA"
	secretB := "CLIENT-SECRET-BBBBBBBBBBBBBBBBBBBBB"
	secretC := "JOIN-SECRET-CCCCCCCCCCCCCCCCCCCCCC"
	secretD := "BUILD-KEY-DDDDDDDDD"
	session := strings.Repeat("S", 40)

	cfg := Config{
		Address:         "127.0.0.1:8085",
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(secretA),
		ClientSecret:    []byte(secretB),
		JoinSecret:      []byte(secretC),
		BuildAuthKey:    []byte(secretD),
		ClientBuild:     70707,
		RealmBuild:      70707,
	}
	var key [40]byte
	copy(key[:], session)
	realm := &Realm{sessionKey: key}
	instance := &Instance{}

	nestedConfig := struct{ C Config }{cfg}
	nestedRealm := struct{ R *Realm }{realm}

	for _, value := range []any{cfg, &cfg, realm, instance, nestedConfig, nestedRealm} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			out := fmt.Sprintf(verb, value)
			for _, secret := range []string{secretA, secretB, secretC, secretD, session} {
				if strings.Contains(out, secret) {
					t.Fatalf("%T with %s printed a secret: %q", value, verb, out)
				}
			}
		}
	}
	out := fmt.Sprintf("%v", cfg)
	for _, want := range []string{
		fmt.Sprintf("<redacted %d bytes>", len(secretA)),
		fmt.Sprintf("<redacted %d bytes>", len(secretB)),
		fmt.Sprintf("<redacted %d bytes>", len(secretD)),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the redacted form does not report lengths (%s): %q", want, out)
		}
	}
	if out := fmt.Sprintf("%v", realm); !strings.Contains(out, "<redacted 40 bytes>") {
		t.Fatalf("the realm's redacted form does not report the session key length: %q", out)
	}
}
