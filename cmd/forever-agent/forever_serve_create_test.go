package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseServeCreate(t *testing.T) {
	two := charChoiceList{{OptionID: 1, ChoiceID: 2}}
	cases := []struct {
		name   string
		spec   string
		choice charChoiceList
		noApp  bool
		want   *serveCreate
		errHas string
	}{
		{name: "off", spec: "", want: nil},
		{name: "choices", spec: "1,1", choice: two, want: &serveCreate{Race: 1, Class: 1, Choices: two}},
		{name: "sex", spec: "2, 4, 1", noApp: true, want: &serveCreate{Race: 2, Class: 4, Sex: 1, NoAppearance: true}},
		{name: "flags without create", spec: "", noApp: true, errHas: "need --create"},
		{name: "one field", spec: "1", noApp: true, errHas: "race,class"},
		{name: "four fields", spec: "1,2,0,4", noApp: true, errHas: "race,class"},
		{name: "not a number", spec: "elf,1", noApp: true, errHas: "not a number"},
		{name: "race too big", spec: "256,1", noApp: true, errHas: "not a number"},
		{name: "race zero", spec: "0,1", noApp: true, errHas: "race is 1-255"},
		{name: "class zero", spec: "1,0", noApp: true, errHas: "class is 1-255"},
		{name: "sex two", spec: "1,1,2", noApp: true, errHas: "sex is 0"},
		{name: "no appearance given", spec: "1,1", errHas: "needs --create-choice"},
		{name: "both appearances", spec: "1,1", choice: two, noApp: true, errHas: "exclusive"},
	}
	for _, c := range cases {
		got, err := parseServeCreate(c.spec, c.choice, c.noApp)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("%s: error %v, want one naming %q", c.name, err, c.errHas)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if (got == nil) != (c.want == nil) || (got != nil && (got.Race != c.want.Race || got.Class != c.want.Class ||
			got.Sex != c.want.Sex || got.NoAppearance != c.want.NoAppearance || len(got.Choices) != len(c.want.Choices))) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestServeCreateFlagsRefusedBeforeTheLogin: a bad create spec is a usage error before any password is read or any
// server is contacted (the addresses here would refuse a connection).
func TestServeCreateFlagsRefusedBeforeTheLogin(t *testing.T) {
	dir := t.TempDir()
	var out, errOut strings.Builder
	code := foreverServeCmd(context.Background(), []string{
		"--rest", "127.0.0.1:1", "--rpc", "127.0.0.1:2", "--ca", filepath.Join(dir, "ca.pem"),
		"--account", "a@example.invalid", "--character", "Chara", "--create", "1,1",
	}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "needs --create-choice") {
		t.Fatalf("exit %d, stderr %q: want a usage error naming the missing appearance", code, errOut.String())
	}
}
