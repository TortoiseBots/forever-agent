package bughunt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ticketHits is one hit per ticket, on a run whose clock is fixed, so first_seen is a known text.
func ticketHits(t *testing.T) []Hit {
	t.Helper()
	r := testRun("watch", map[string]string{gHeal: "healer", gDps: "dps"})
	withClass(r, gHeal, "priest")
	withClass(r, gDps, "mage")
	return []Hit{
		r.Hit(15, 0, 14, gHeal, "Hex (magic) on priest healer for 14.0 s; 1 able dispeller(s) alive, nearest 6 yd and not casting",
			[]string{"15", "magic", "Hex"}),
		r.Hit(4, 10, 4, gDps, "Rain of Fire from 0x0000000000000B01 ticked 5 times over 4.0 s on 0x0000000000000A03",
			[]string{"4", "Rain of Fire"}),
	}
}

// TestTicketIDStable pins the id the lead's board cards carry: BH-<rule>-<sha1(rule|key)[:8]>.
func TestTicketIDStable(t *testing.T) {
	cases := []struct {
		rule int
		key  []string
		want string
	}{
		{15, []string{"15", "magic", "Hex"}, "BH-15-57dabda0"},
		{1, []string{"1", "heal"}, "BH-1-52be5436"},
		{8, []string{"8", "Power Word: Fortitude"}, "BH-8-a0b8a65e"},
		{4, []string{"4", "Rain of Fire"}, "BH-4-a43e14bb"},
	}
	for _, c := range cases {
		if got := TicketID(c.rule, c.key); got != c.want {
			t.Fatalf("TicketID(%d, %v) = %q, want %q", c.rule, c.key, got, c.want)
		}
	}
}

// TestBaselineNewKnownGone: a baseline's ticket is known (and keeps its first night), a new key is new, and a
// baseline ticket this run no longer sees is gone.
func TestBaselineNewKnownGone(t *testing.T) {
	hits := ticketHits(t)
	knownID := TicketID(15, []string{"15", "magic", "Hex"})
	goneID := TicketID(9, []string{"9", "z-drop"})
	baseline := []Ticket{
		{ID: knownID, Title: "missed decurse: magic Hex", Rule: 15, Key: []string{"15", "magic", "Hex"},
			Count: 3, Runs: 1, FirstSeen: "2023-01-01T00:00:00Z", DeathsNear: 2,
			Labels: []string{"bughunt", "rule-15", "healer"}, Status: "new"},
		{ID: goneID, Title: "fall/drown death: z-drop", Rule: 9, Key: []string{"9", "z-drop"},
			Count: 1, Runs: 1, FirstSeen: "2023-01-02T00:00:00Z", DeathsNear: 0,
			Labels: []string{"bughunt", "rule-9", "?"}, Status: "new"},
	}
	tickets := BuildTickets(hits, baseline, func(int) bool { return true })
	byID := map[string]Ticket{}
	for _, tk := range tickets {
		byID[tk.ID] = tk
	}
	known, ok := byID[knownID]
	if !ok || known.Status != "known" {
		t.Fatalf("known %+v", known)
	}
	if known.FirstSeen != "2023-01-01T00:00:00Z" || known.DeathsNear != 2 {
		t.Fatalf("known lost its history: %+v", known)
	}
	if known.Count != 1 || known.Runs != 1 || known.Cost != 14 {
		t.Fatalf("known counts %+v", known)
	}
	newTicket := byID[TicketID(4, []string{"4", "Rain of Fire"})]
	if newTicket.Status != "new" || newTicket.FirstSeen != "2023-11-14T22:13:30Z" {
		t.Fatalf("new %+v", newTicket)
	}
	gone, ok := byID[goneID]
	if !ok || gone.Status != "gone" || gone.Count != 0 || gone.Runs != 0 || len(gone.Evidence) != 0 {
		t.Fatalf("gone %+v", gone)
	}
	if gone.FirstSeen != "2023-01-02T00:00:00Z" || gone.Title == "" {
		t.Fatalf("gone lost its row: %+v", gone)
	}
	if known.Labels[2] != "healer" || newTicket.Labels[2] != "dps" {
		t.Fatalf("labels %v %v", known.Labels, newTicket.Labels)
	}
}

// TestTicketsMarkdownGolden pins tickets.md: the summary line, the columns and worst summed cost first.
func TestTicketsMarkdownGolden(t *testing.T) {
	tickets := BuildTickets(ticketHits(t), nil, nil)
	got := TicketsMarkdown(tickets)
	want, err := os.ReadFile(filepath.Join("testdata", "tickets.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("tickets.md:\n%s\nwant:\n%s", got, want)
	}
	if strings.Index(got, "BH-15-") > strings.Index(got, "BH-4-") {
		t.Fatal("the higher-cost ticket must come first")
	}
}

// TestWriteTickets names the two files beside each other.
func TestWriteTickets(t *testing.T) {
	dir := t.TempDir()
	tickets := BuildTickets(ticketHits(t), nil, nil)
	if err := WriteTickets(dir, tickets); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tickets.json", "tickets.md"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.Size() == 0 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := LoadBaseline(dir); err != nil {
		t.Fatalf("baseline: %v", err)
	}
}

// TestWriteHitsAndCounts keeps the hit's keys and the counts' skip reasons.
func TestWriteHitsAndCounts(t *testing.T) {
	dir := t.TempDir()
	hits, counts := Hunt([]*Run{testRun("watch", map[string]string{gHeal: "healer"})}, EventRules())
	if err := WriteHits(filepath.Join(dir, "hits.jsonl"), hits); err != nil {
		t.Fatal(err)
	}
	if err := WriteCounts(filepath.Join(dir, "counts.json"), counts); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "counts.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"rule": 1`, `"skipped": 1`, `"rule 1: no combat log"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("counts.json lacks %s:\n%s", want, b)
		}
	}
}

// TestBaselineBadJSON: an unreadable baseline is an error naming the file and the line.
func TestBaselineBadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tickets.json"), []byte("[\n {\"id\": \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(dir); err == nil || !strings.Contains(err.Error(), "tickets.json:3") {
		t.Fatalf("baseline: %v", err)
	}
	if _, err := LoadBaseline(filepath.Join(dir, "nope")); err == nil || !strings.Contains(err.Error(), "tickets.json") {
		t.Fatalf("missing: %v", err)
	}
}
