package world

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Options must not print the join ticket, the key halves, the build key or the character name under any verb,
// including nested.
func TestOptionsRedactsEveryVerb(t *testing.T) {
	secretA := "JOIN-TICKET-SECRET-AAAAAAAAAAAAAAA"
	secretB := "CLIENT-SECRET-BBBBBBBBBBBBBBBBBBBBB"
	secretC := "JOIN-SECRET-CCCCCCCCCCCCCCCCCCCCCC"
	secretD := "BUILD-KEY-DDDDDDDDD"
	character := "Secretchar"

	opts := Options{
		Address:         "127.0.0.1:8085",
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(secretA),
		ClientSecret:    []byte(secretB),
		JoinSecret:      []byte(secretC),
		BuildAuthKey:    []byte(secretD),
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       character,
		Stay:            time.Second,
	}
	nested := struct{ O Options }{opts}
	for _, value := range []any{opts, &opts, nested} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			out := fmt.Sprintf(verb, value)
			for _, secret := range []string{secretA, secretB, secretC, secretD, character} {
				if strings.Contains(out, secret) {
					t.Fatalf("%T with %s printed a secret: %q", value, verb, out)
				}
			}
		}
	}
	if out := fmt.Sprintf("%v", opts); !strings.Contains(out, fmt.Sprintf("<redacted %d bytes>", len(secretA))) || !strings.Contains(out, fmt.Sprintf("<redacted %d chars>", len(character))) {
		t.Fatalf("the redacted form does not report lengths: %q", out)
	}
}
