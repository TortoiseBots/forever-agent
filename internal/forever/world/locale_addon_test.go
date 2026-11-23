package world

// Round #114: the add-on switch and the session's language. --addon off must stop the session from ever registering
// an add-on prefix (so it never speaks TBM), and status reports both the claimed language and whether the add-on
// transport is on.

import (
	"context"
	"errors"
	"testing"
)

// TestRegisterAddonPrefixesAddonOff: with the add-on off the registration is refused by name before anything is
// sent. The gate sits in RegisterAddonPrefixes, so the channel and TBM paths (which call it directly) cannot slip a
// prefix past it either.
func TestRegisterAddonPrefixesAddonOff(t *testing.T) {
	s, tr := addonTestSession()
	s.opts.AddonOff = true
	if err := s.RegisterAddonPrefixes("TBM:"); !errors.Is(err, ErrAddonOff) {
		t.Fatalf("registration with the add-on off returned %v, want ErrAddonOff", err)
	}
	if len(tr.sent) != 0 {
		t.Fatalf("the add-on-off session sent %d packet(s)", len(tr.sent))
	}
	if s.addon != nil {
		t.Fatalf("the add-on-off session built an add-on inbox with %v", s.addon.Registered())
	}
}

// TestServeAddonSendAddonOff: serve's add-on method refuses with ErrAddonOff and sends nothing, so a bot or TBM
// command cannot register a prefix for a session started with --addon off.
func TestServeAddonSendAddonOff(t *testing.T) {
	s, tr := addonTestSession()
	s.opts.AddonOff = true
	if err := s.serveAddon(context.Background(), "party", "TBM:", "HI", ""); !errors.Is(err, ErrAddonOff) {
		t.Fatalf("serveAddon with the add-on off returned %v, want ErrAddonOff", err)
	}
	if len(tr.sent) != 0 {
		t.Fatalf("the add-on-off session sent %d packet(s)", len(tr.sent))
	}
}

// TestStatusLocaleAndAddon: status carries the claimed language (empty means enUS) and on/off for the add-on
// transport.
func TestStatusLocaleAndAddon(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   Options
		locale string
		addon  string
	}{
		{"default", Options{}, "enUS", "on"},
		{"german", Options{Locale: "deDE"}, "deDE", "on"},
		{"addon off", Options{AddonOff: true}, "enUS", "off"},
		{"both", Options{Locale: "frFR", AddonOff: true}, "frFR", "off"},
	} {
		s := &session{opts: tc.opts, report: &Report{Counts: map[string]int{}}}
		st := s.serveStatus(&serveState{})
		if st.Locale != tc.locale || st.Addon != tc.addon {
			t.Errorf("%s: status locale=%q addon=%q, want %q/%q", tc.name, st.Locale, st.Addon, tc.locale, tc.addon)
		}
	}
}
