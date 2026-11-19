package main

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestCharScreenUsage: both commands refuse a missing connection flag or a missing character flag before they touch
// the network. FOREVER_PASSWORD is unset in the test process.
func TestCharScreenUsage(t *testing.T) {
	common := []string{"--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a"}
	with := func(extra ...string) []string { return append(append([]string{}, common...), extra...) }
	cases := [][]string{
		{"forever", "char-create"},
		{"forever", "char-create", "--name", "Agt", "--race", "1", "--class", "1"},
		with("--race", "1", "--class", "1"),
		with("--name", "Agt", "--class", "1"),
		with("--name", "Agt", "--race", "1"),
		with("--name", "Agt", "--race", "1", "--class", "1", "--sex", "2"),
		with("--name", "Agt", "--race", "1", "--class", "1"),                                       // no appearance form
		with("--name", "Agt", "--race", "1", "--class", "1", "--no-appearance", "--choice", "1:2"), // both forms
		with("--name", "Agt", "--race", "1", "--class", "1", "--choice", "14"),                     // no choice number
		with("--name", "Agt", "--race", "1", "--class", "1", "--choice", "x:2"),                    // not a number
		{"forever", "char-delete"},
		with("--name", "Agt"),
		with(),
		with("--name", "Agt", "--guid", "0x0A443300000000000000000000002211"),
		with("--guid", "not-a-guid"),
		{"forever", "char-delete", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a"},
	}
	t.Setenv("FOREVER_PASSWORD", "")
	for _, args := range cases {
		code, _, errOut := runCLI(args...)
		if code != exitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, exitUsage, errOut)
		}
	}
}

// TestParseCharacterGUID: the --guid flag takes the form the list prints, and refuses anything else before the
// network is touched.
func TestParseCharacterGUID(t *testing.T) {
	want := wire.GUID{Low: 0x2211, High: 0x0A44330000000000}
	for _, in := range []string{want.String(), "0x0A443300000000000000000000002211", "0A443300000000000000000000002211"} {
		got, err := parseCharacterGUID(in)
		if err != nil || got != want {
			t.Fatalf("%q: %s, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0x", "0x1234", "0x" + strings.Repeat("Z", 32)} {
		if _, err := parseCharacterGUID(in); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
}

// TestCharScreenUsageNamesTheMissingFlag: the flag checks say which half is missing rather than exiting silently.
func TestCharScreenUsageNamesTheMissingFlag(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	code, _, errOut := runCLI("forever", "char-create", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a")
	if code != exitUsage || !strings.Contains(errOut, "--name, --race and --class are required") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	code, _, errOut = runCLI("forever", "char-create", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a", "--name", "Agt", "--race", "1", "--class", "1", "--no-appearance")
	if code != exitUsage || !strings.Contains(errOut, "FOREVER_PASSWORD is not set") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

// TestCharCreateNeedsAnAppearanceFlag: with the flags otherwise valid, the appearance form is still required and the
// two forms are exclusive; both checks happen before the login, so nothing is dialled and the exit code is the usage
// one.
func TestCharCreateNeedsAnAppearanceFlag(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "x")
	common := []string{"forever", "char-create", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a",
		"--name", "Agt", "--race", "1", "--class", "1"}
	code, _, errOut := runCLI(common...)
	if code != exitUsage || !strings.Contains(errOut, "--choice OPTION:CHOICE") {
		t.Fatalf("no appearance form: exit %d, stderr %q", code, errOut)
	}
	both := append(append([]string{}, common...), "--no-appearance", "--choice", "14:87")
	code, _, errOut = runCLI(both...)
	if code != exitUsage || !strings.Contains(errOut, "exclusive") {
		t.Fatalf("both appearance forms: exit %d, stderr %q", code, errOut)
	}
}

// TestCharChoiceList: --choice takes OPTION:CHOICE, repeated, and refuses anything that is not two numbers.
func TestCharChoiceList(t *testing.T) {
	var choices charChoiceList
	if err := choices.Set("14:87"); err != nil {
		t.Fatal(err)
	}
	if err := choices.Set(" 15 : 114 "); err != nil {
		t.Fatal(err)
	}
	want := charChoiceList{{OptionID: 14, ChoiceID: 87}, {OptionID: 15, ChoiceID: 114}}
	if len(choices) != len(want) {
		t.Fatalf("choices %+v", choices)
	}
	for i := range want {
		if choices[i] != want[i] {
			t.Fatalf("choice %d is %+v, want %+v", i, choices[i], want[i])
		}
	}
	for _, bad := range []string{"", "14", "x:1", "1:y", "-1:2", "1:2:3"} {
		var c charChoiceList
		if err := c.Set(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
