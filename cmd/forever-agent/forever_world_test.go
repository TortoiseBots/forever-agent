package main

import (
	"strings"
	"testing"
)

// TestWorldCheckUsage: world-check needs every address, the CA, the account and a character before it touches the
// network, and its usage line names the command.
func TestWorldCheckUsage(t *testing.T) {
	cases := [][]string{
		{"forever"},
		{"forever", "world-check"},
		{"forever", "world-check", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a"},   // no character
		{"forever", "world-check", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--character", "C"}, // no account
		{"forever", "world-check", "--rest", "h:1", "--rpc", "h:2", "--account", "a", "--character", "C"}, // no CA
		{"forever", "world-check", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a", "--character", "C", "--build", "0"},
		{"forever", "world-check", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a", "--character", "C", "--stay", "11m"},
		{"forever", "world-check", "extra"},
	}
	for _, args := range cases {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%v: code %d, want %d", args, code, exitUsage)
		}
	}
	if _, _, errOut := runCLI("forever"); !strings.Contains(errOut, "world-check") {
		t.Errorf("the forever usage does not name world-check: %q", errOut)
	}
}

// TestWorldCheckNeedsPassword: without FOREVER_PASSWORD the command stops at usage and never dials.
func TestWorldCheckNeedsPassword(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	code, _, errOut := runCLI("forever", "world-check", "--rest", "h:1", "--rpc", "h:2", "--ca", "ca.pem", "--account", "a", "--character", "C")
	if code != exitUsage || !strings.Contains(errOut, "FOREVER_PASSWORD") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}
