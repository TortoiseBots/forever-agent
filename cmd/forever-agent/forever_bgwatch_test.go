package main

// forever bg-watch in its dry mode: the argument checks before any network,
// the WSG and AB dry runs, and the record/card files.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
)

func runBGCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := foreverCmd(context.Background(), append([]string{"bg-watch"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestForeverBGWatchDryWSG(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runBGCmd(t, "--dry", "--map", "wsg", "--out", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"queued for wsg", "match-end", "card: CLEARED"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output has no %q:\n%s", want, stdout)
		}
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.forever-bg.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("record files: %v %v", matches, err)
	}
	rec, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"schema": "forever-bg"`, `"map": "wsg"`, `"winner": "alliance"`} {
		if !strings.Contains(string(rec), want) {
			t.Fatalf("record has no %s:\n%s", want, rec)
		}
	}
	cards, err := filepath.Glob(filepath.Join(dir, "*.bg-card.txt"))
	if err != nil || len(cards) != 1 {
		t.Fatalf("card files: %v %v", cards, err)
	}
}

func TestForeverBGWatchDryAB(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runBGCmd(t, "--dry", "--map", "ab", "--out", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "queued for ab") {
		t.Fatalf("output:\n%s", stdout)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.forever-bg.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("record files: %v %v", matches, err)
	}
}

func TestBGWatchDryInviteUsesTheMap(t *testing.T) {
	// --dry --map ab must invite into Arathi Basin, not Warsong Gulch.
	f := &bgDryFake{mapID: bg.Map(bg.MapAB)}
	inv, ok := f.Invite(time.Time{})
	if !ok {
		t.Fatal("the dry fake served no invite")
	}
	if inv.Map != bg.MapAB {
		t.Fatalf("invite map = %d, want %d", inv.Map, bg.MapAB)
	}
}

func TestForeverBGWatchArguments(t *testing.T) {
	cases := [][]string{
		{"--dry", "--map", "xx"},
		{"--dry", "--role", "tank"},
		{"--dry", "--poll", "0"},
		{"--dry", "--poll", "31"},
		{"--dry", "--quiet-min", "0"},
		{"--dry", "--idle-s", "5"},
		{"--dry", "--escort-share", "2"},
		{"--dry", "--touch-s", "0"},
		{"--dry", "--team-of", "1"},
		{"--dry", "--role", "fc"},
		{"--dry", "--out", t.TempDir(), "--run-id", "../x"},
		{"--dry", "--out", t.TempDir(), "--run-id", "a/b"},
	}
	for _, args := range cases {
		code, _, _ := runBGCmd(t, args...)
		if code != exitUsage {
			t.Fatalf("args %v: exit %d, want %d", args, code, exitUsage)
		}
	}
}
