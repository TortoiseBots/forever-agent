package bughunt

// The tickets: one board card per (rule, key), the id the lead turns into a card, and its life against a baseline.
// A ticket is `BH-<rule>-<8 hex of sha1(rule|key)>`, so the same finding keeps its id across nights; a baseline
// makes it `known`, a finding the baseline lacks is `new`, and a baseline ticket this run does not see is `gone`.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Ticket is one row of tickets.json: one board card.
type Ticket struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Rule       int      `json:"rule"`
	Key        []string `json:"key"`
	Count      int      `json:"count"`
	Runs       int      `json:"runs"`
	FirstSeen  string   `json:"first_seen"`
	DeathsNear int      `json:"deaths_near"`
	Evidence   []string `json:"evidence"`
	Labels     []string `json:"labels"`
	Status     string   `json:"status"`
	// Cost is the summed cost of the ticket's hits, which ranks tickets.md (worst first). A gone ticket has none.
	Cost float64 `json:"cost"`
}

// RuleCount is one rule's row of counts.json.
type RuleCount struct {
	Rule     int            `json:"rule"`
	Name     string         `json:"name"`
	Hits     int            `json:"hits"`
	Runs     int            `json:"runs"`
	Skipped  int            `json:"skipped"`
	Datasets map[string]int `json:"datasets"`
}

// Counts is counts.json: the runs read, every selected rule's hits and skips, and why checks were skipped.
type Counts struct {
	Runs        int            `json:"runs"`
	Rules       []RuleCount    `json:"rules"`
	SkipReasons map[string]int `json:"skip_reasons,omitempty"`
}

// Hunt runs the rules over the loaded runs. A rule that cannot judge a run (its input is missing) is counted as a
// skipped check with the reason and contributes no hit: unknown stays unknown.
func Hunt(runs []*Run, rules []Rule) ([]Hit, Counts) {
	counts := Counts{Runs: len(runs), SkipReasons: map[string]int{}}
	var hits []Hit
	for _, rule := range rules {
		rc := RuleCount{Rule: rule.Number, Name: rule.Name, Datasets: map[string]int{}}
		for _, run := range runs {
			if rule.Skipped != nil {
				if reason := rule.Skipped(run); reason != "" {
					rc.Skipped++
					counts.SkipReasons[fmt.Sprintf("rule %d: %s", rule.Number, reason)]++
					continue
				}
			}
			run.unknown = nil
			got := rule.Run(run)
			if why := run.unknown[rule.Number]; len(why) > 0 {
				// Windows the input did not cover were left unjudged: the run's check was only partly made.
				rc.Skipped++
				for reason, n := range why {
					counts.SkipReasons[fmt.Sprintf("rule %d: %s", rule.Number, reason)] += n
				}
			}
			if len(got) == 0 {
				continue
			}
			rc.Hits += len(got)
			rc.Runs++
			rc.Datasets[run.DS] += len(got)
			hits = append(hits, got...)
		}
		counts.Rules = append(counts.Rules, rc)
	}
	return hits, counts
}

// Complete says whether the rule was selected and judged every run in full: not skipped on any run and with no
// window left unjudged. Only then does the absence of a finding mean anything.
func (c Counts) Complete(rule int) bool {
	for _, rc := range c.Rules {
		if rc.Rule == rule {
			return rc.Skipped == 0
		}
	}
	return false
}

// TicketID is a ticket's id: BH-<rule>-<8 hex of sha1(rule|key)>. The key is the hit's own key, joined with `|`.
func TicketID(rule int, key []string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%d|%s", rule, strings.Join(key, "|"))))
	return "BH-" + fmt.Sprintf("%d", rule) + "-" + hex.EncodeToString(sum[:4])
}

// ticketTitle is the rule's name and the key's own words: `missed decurse: magic Hex`.
func ticketTitle(rule int, key []string) string {
	name, ok := ruleNames[rule]
	if !ok {
		name = fmt.Sprintf("rule %d", rule)
	}
	tail := key
	if len(tail) > 0 && tail[0] == fmt.Sprintf("%d", rule) {
		tail = tail[1:]
	}
	if len(tail) == 0 {
		return name
	}
	return name + ": " + strings.Join(tail, " ")
}

// BuildTickets groups the hits into tickets and applies the baseline. Tickets are ordered by rule and id. A baseline
// ticket this run did not see comes back as `gone` so the board can close it, but only when complete says its rule
// ran with all its inputs on every run: a rule that was skipped, only partly judged or not selected says nothing
// about its tickets, which keep their previous state. A `gone` ticket that returns is `reopened`.
func BuildTickets(hits []Hit, baseline []Ticket, complete func(rule int) bool) []Ticket {
	type group struct {
		rule int
		key  []string
		hits []Hit
	}
	byID := map[string]*group{}
	var order []string
	for _, h := range hits {
		id := TicketID(h.Rule, h.Key)
		g, ok := byID[id]
		if !ok {
			g = &group{rule: h.Rule, key: append([]string(nil), h.Key...)}
			byID[id] = g
			order = append(order, id)
		}
		g.hits = append(g.hits, h)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	base := map[string]Ticket{}
	for _, t := range baseline {
		base[t.ID] = t
	}
	var out []Ticket
	for _, id := range order {
		g := byID[id]
		sort.SliceStable(g.hits, func(i, j int) bool {
			if g.hits[i].At != g.hits[j].At {
				return g.hits[i].At < g.hits[j].At
			}
			if g.hits[i].DS != g.hits[j].DS {
				return g.hits[i].DS < g.hits[j].DS
			}
			return g.hits[i].Run < g.hits[j].Run
		})
		t := Ticket{
			ID:    id,
			Title: ticketTitle(g.rule, g.key),
			Rule:  g.rule,
			Key:   g.key,
			Count: len(g.hits),
			Runs:  distinctRuns(g.hits),
		}
		for _, h := range g.hits {
			t.DeathsNear += h.Deaths
			t.Cost += h.Cost
			if h.At > 0 {
				if seen := formatSeen(h.At); t.FirstSeen == "" || seen < t.FirstSeen {
					t.FirstSeen = seen
				}
			}
			if len(t.Evidence) < 5 {
				t.Evidence = append(t.Evidence, fmt.Sprintf("%s %s/%s: %s", h.Clock, h.DS, h.Run, h.Ev))
			}
		}
		t.Cost = pyRound(t.Cost, 1)
		t.Labels = []string{"bughunt", fmt.Sprintf("rule-%d", g.rule), ticketRole(g.hits)}
		if old, ok := base[id]; ok {
			t.Status = "known"
			if old.Status == "gone" {
				t.Status = "reopened"
			}
			// A known ticket keeps the first night it was seen and every death it ever counted near.
			if old.FirstSeen != "" && (t.FirstSeen == "" || old.FirstSeen < t.FirstSeen) {
				t.FirstSeen = old.FirstSeen
			}
			if old.DeathsNear > t.DeathsNear {
				t.DeathsNear = old.DeathsNear
			}
		} else {
			t.Status = "new"
		}
		out = append(out, t)
	}
	for id, old := range base {
		if _, ok := byID[id]; ok {
			continue
		}
		if complete != nil && complete(old.Rule) {
			old.Status = "gone"
			old.Count, old.Runs, old.Cost = 0, 0, 0
			old.Evidence = []string{}
		}
		out = append(out, old)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// distinctRuns counts the runs the hits came from (a dataset and a run name together).
func distinctRuns(hits []Hit) int {
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.DS+"\x00"+h.Run] = true
	}
	return len(seen)
}

// ticketRole is the ticket's role label: the hits' one role, `mixed` when they disagree, `?` when none knows one.
func ticketRole(hits []Hit) string {
	role := ""
	for _, h := range hits {
		r := h.Role
		if r == "" {
			r = "?"
		}
		if role == "" {
			role = r
		} else if role != r {
			return "mixed"
		}
	}
	if role == "" {
		return "?"
	}
	return role
}

// formatSeen is a wall time as RFC3339 UTC.
func formatSeen(at float64) string {
	sec := int64(at)
	return time.Unix(sec, int64((at-float64(sec))*1e9)).UTC().Format(time.RFC3339)
}

// LoadBaseline reads a previous run's `tickets.json` from a directory.
func LoadBaseline(dir string) ([]Ticket, error) {
	path := filepath.Join(dir, "tickets.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tickets []Ticket
	if err := json.Unmarshal(b, &tickets); err != nil {
		return nil, jsonInputError(b, path, err)
	}
	return tickets, nil
}

// WriteHits writes hits.jsonl: one hit per line, the Python checker's keys.
func WriteHits(path string, hits []Hit) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, h := range hits {
		if err := enc.Encode(h); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// WriteCounts writes counts.json.
func WriteCounts(path string, counts Counts) error {
	b, err := json.MarshalIndent(counts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// WriteTickets writes tickets.json and tickets.md.
func WriteTickets(dir string, tickets []Ticket) error {
	b, err := json.MarshalIndent(tickets, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "tickets.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "tickets.md"), []byte(TicketsMarkdown(tickets)), 0o644)
}

// TicketsMarkdown is tickets.md: worst summed cost first.
func TicketsMarkdown(tickets []Ticket) string {
	var b strings.Builder
	newN, knownN, goneN, reopenedN := 0, 0, 0, 0
	for _, t := range tickets {
		switch t.Status {
		case "new":
			newN++
		case "known":
			knownN++
		case "gone":
			goneN++
		case "reopened":
			reopenedN++
		}
	}
	fmt.Fprintf(&b, "# Bug hunt tickets\n\n%d tickets: %d new, %d known, %d reopened, %d gone. Worst summed cost first.\n\n",
		len(tickets), newN, knownN, reopenedN, goneN)
	b.WriteString("| id | status | rule | ticket | count | runs | cost | deaths | first seen | evidence |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	ranked := append([]Ticket(nil), tickets...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Cost != ranked[j].Cost {
			return ranked[i].Cost > ranked[j].Cost
		}
		return ranked[i].ID < ranked[j].ID
	})
	for _, t := range ranked {
		ev := ""
		if len(t.Evidence) > 0 {
			ev = t.Evidence[0]
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %d | %d | %.0f | %d | %s | %s |\n",
			mdCell(t.ID), mdCell(t.Status), t.Rule, mdCell(t.Title), t.Count, t.Runs, t.Cost, t.DeathsNear,
			mdCell(t.FirstSeen), mdCell(ev))
	}
	return b.String()
}

// mdCell escapes one table cell: a pipe would split the row and a newline would end it.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}
