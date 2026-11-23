package bnet

// Round #114: the client language the agent claims. The logon request carries it, the realm list ticket's client
// info carries its fourCC for both TextLocale and AudioLocale, and an empty option sends the default (enUS) rather
// than an empty field.

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestValidLocale: the core's names are accepted, "none" and anything else are not.
func TestValidLocale(t *testing.T) {
	for _, name := range []string{"enUS", "koKR", "frFR", "deDE", "zhCN", "zhTW", "esES", "esMX", "ruRU", "ptBR", "itIT"} {
		if !ValidLocale(name) {
			t.Errorf("ValidLocale(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "none", "xxXX", "enus", "enUS ", "de"} {
		if ValidLocale(name) {
			t.Errorf("ValidLocale(%q) = true, want false", name)
		}
	}
}

// TestLogonSendsLocale: the logon request carries Options.Locale, and an empty option sends the default rather than
// an empty field.
func TestLogonSendsLocale(t *testing.T) {
	for _, tc := range []struct {
		name   string
		locale string
		want   string
	}{
		{"explicit", "deDE", "deDE"},
		{"default", "", DefaultLocale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeLogin(t)
			defer f.close()
			opts := f.options()
			opts.ClientBuild = wire.Build70938
			opts.Locale = tc.locale
			if _, err := Login(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-f.errs:
				t.Fatal("fake server:", err)
			default:
			}
			select {
			case got := <-f.logonLocales:
				if got != tc.want {
					t.Fatalf("logon locale %q, want %q", got, tc.want)
				}
			default:
				t.Fatal("the fake server saw no logon")
			}
		})
	}
}

// TestRealmTicketLocaleFourCC: the realm list ticket's client info carries the locale's fourCC in TextLocale and
// AudioLocale (deDE = 0x64654445, big-endian ASCII as the core's FourCC packs it). A client that fixes AudioLocale at
// enUS fails here.
func TestRealmTicketLocaleFourCC(t *testing.T) {
	if got := localeFourCC("deDE"); got != 0x64654445 {
		t.Fatalf("localeFourCC(deDE) = %#08X, want 0x64654445", got)
	}
	if got := localeFourCC(DefaultLocale); got != fourCCEnUS {
		t.Fatalf("localeFourCC(%s) = %#08X, want the enUS fourCC %#08X", DefaultLocale, got, fourCCEnUS)
	}

	f := newFakeLogin(t)
	defer f.close()
	opts := f.options()
	opts.ClientBuild = wire.Build70938
	opts.Locale = "deDE"
	if _, err := Login(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.errs:
		t.Fatal("fake server:", err)
	default:
	}
	select {
	case got := <-f.clientLocales:
		if got != [2]uint32{0x64654445, 0x64654445} {
			t.Fatalf("client info locale fourCCs %#08X/%#08X, want 0x64654445 twice", got[0], got[1])
		}
	default:
		t.Fatal("the fake server saw no realm list ticket request")
	}
}
