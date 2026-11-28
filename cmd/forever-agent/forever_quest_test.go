package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

func TestQuestShareUsage(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "x")
	base := []string{"quest-share", "--rest", "127.0.0.1:1", "--rpc", "127.0.0.1:2", "--ca", "ca.pem", "--account", "a@b", "--character", "XXXXXX"}
	cases := [][]string{
		base,
		append(append([]string{}, base...), "--accept", "--decline"),
		append(append([]string{}, base...), "--quest", "0"),
		append(append([]string{}, base...), "--quest", "-5"),
		append(append([]string{}, base...), "--quest", "783", "--from", "Mateone"),
		append(append([]string{}, base...), "--accept", "--wait", "3m"),
		append(append([]string{}, base...), "--accept", "--settle", "2m"),
		append(append([]string{}, base...), "--accept", "--stay", "3m"),
		append(append([]string{}, base...), "--accept", "extra"),
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := foreverCmd(context.Background(), args, &stdout, &stderr); code != exitUsage {
			t.Errorf("%v: code %d, want %d (stderr: %s)", args, code, exitUsage, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	foreverCmd(context.Background(), nil, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "quest-share") {
		t.Fatalf("forever usage does not list quest-share: %q", stderr.String())
	}
}

func TestPrintQuestShareSummary(t *testing.T) {
	var out bytes.Buffer
	printQuestShareSummary(&out, &world.Report{QuestShare: &world.QuestShareReport{
		Mode:    "share",
		QuestID: 783,
		OK:      true,
		Results: []world.ServeQuestPushMember{
			{GUID: world.GUID{Low: 0xAA01, High: 0x0800000000000000}, Name: "Mateone", Result: 0, Reason: "success"},
			{GUID: world.GUID{Low: 0xAA02, High: 0x0800000000000000}, Name: "Matetwo", Result: 10, Reason: "on_quest"},
		},
	}})
	text := out.String()
	for _, want := range []string{"quest 783", "Mateone", "success", "Matetwo", "on_quest"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary missing %q:\n%s", want, text)
		}
	}
}
