package tbcmd

// The decisions read: the module's bounded, paged answer of a run's decision rows, the strict parse of every record,
// the pager's cursor and its own bound, and the refusals that end it. The lines copy the command's documented shape
// (a header record, one row per line, then the end marker), never a capture.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

func decisionsHeader(count, since, next int, more string) string {
	return fmt.Sprintf("decisions run=r-20261124T190001-a1b2 lead=Tankbot plan=ragefire_chasm capture=cap-7 "+
		"count=%d since=%d next=%d more=%s #T", count, since, next, more)
}

func decisionRow(seq int) string {
	return fmt.Sprintf("decision seq=%d t=%d dt=200 state=running phase=walk step=3 record=tick "+
		"order=attack%%20the%%20skull note=all%%20good say= #T", seq, 1764000000000+seq*200)
}

func decisionsEnd(count, next int, more string) string {
	return fmt.Sprintf("run-end count=%d records=%d verb=decisions lead=Tankbot next=%d more=%s #T",
		count, count, next, more)
}

func TestRunDecisionsEveryKey(t *testing.T) {
	row := "decision seq=6 t=1764000001200 dt=200 state=paused phase=wait step=17 record=stub " +
		"order=attack%20the%20skull note=refused%3A%20revive%20party say=hold%20on trunc=yes future_key=a%23b #T"
	r := oneReply("run-begin #T", decisionsHeader(1, 5, 6, "no"), row, decisionsEnd(2, 6, "no"))
	page, err := NewClient(r).RunDecisions(context.Background(), "Tankbot", 5, true, 0)
	if err != nil {
		t.Fatalf("RunDecisions: %v", err)
	}
	if want := ".tb run decisions Tankbot since=5 limit=25"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
	h := page.Header
	if h.Run != "r-20261124T190001-a1b2" || h.Lead != "Tankbot" || h.Plan != "ragefire_chasm" ||
		h.Capture != "cap-7" || h.Count != 1 || h.Since != 5 || h.Next != 6 || h.More {
		t.Fatalf("header: %+v", h)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("%d row(s)", len(page.Rows))
	}
	d := page.Rows[0]
	if d.Seq != 6 || d.T != 1764000001200 || d.DT != 200 || d.State != "paused" || d.Phase != "wait" || d.Step != 17 {
		t.Fatalf("row: %+v", d)
	}
	if d.Record != "stub" || !d.Trunc || d.Order != "attack the skull" || d.Note != "refused: revive party" || d.Say != "hold on" {
		t.Fatalf("row text: %+v", d)
	}
	if d.Extra["future_key"] != "a#b" {
		t.Fatalf("extra key kept as %q", d.Extra["future_key"])
	}
}

func TestRunDecisionsWithoutSince(t *testing.T) {
	r := oneReply("run-begin #T", decisionsHeader(0, 0, 0, "no"), decisionsEnd(1, 0, "no"))
	if _, err := NewClient(r).RunDecisions(context.Background(), "r-20261124T190001-a1b2", 0, false, 25); err != nil {
		t.Fatalf("RunDecisions: %v", err)
	}
	if want := ".tb run decisions r-20261124T190001-a1b2 limit=25"; r.seen[0] != want {
		t.Fatalf("line %q, want %q", r.seen[0], want)
	}
}

// TestPullDecisionsPages follows the cursor: every call names the next= of the one before, the rows arrive oldest
// first, and the pull stops on more=no.
func TestPullDecisionsPages(t *testing.T) {
	runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case !strings.Contains(line, "since="):
			return reply(o, "run-begin #T", decisionsHeader(2, 0, 2, "yes"), decisionRow(1), decisionRow(2),
				decisionsEnd(3, 2, "yes")), nil
		case strings.Contains(line, "since=2"):
			return reply(o, "run-begin #T", decisionsHeader(1, 2, 3, "no"), decisionRow(3),
				decisionsEnd(2, 3, "no")), nil
		}
		return reply(o, "error code=bad-argument cmd=run #T"), nil
	}}
	pull, err := NewClient(runner).PullDecisions(context.Background(), "Tankbot", 0, false, 0)
	if err != nil {
		t.Fatalf("PullDecisions: %v", err)
	}
	if len(runner.seen) != 2 {
		t.Fatalf("%d call(s): %v", len(runner.seen), runner.seen)
	}
	if want := ".tb run decisions Tankbot limit=25"; runner.seen[0] != want {
		t.Fatalf("first line %q, want %q", runner.seen[0], want)
	}
	if want := ".tb run decisions Tankbot since=2 limit=25"; runner.seen[1] != want {
		t.Fatalf("second line %q, want %q", runner.seen[1], want)
	}
	if len(pull.Rows) != 3 || pull.Rows[0].Seq != 1 || pull.Rows[2].Seq != 3 {
		t.Fatalf("rows: %+v", pull.Rows)
	}
	if pull.Pages != 2 || pull.PageBound || pull.Run != "r-20261124T190001-a1b2" || pull.Capture != "cap-7" {
		t.Fatalf("pull: %+v", pull)
	}
}

// TestPullDecisionsBound: a module that always says more stops the pull at its own page bound, and says so.
func TestPullDecisionsBound(t *testing.T) {
	calls := 0
	runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		calls++
		seq := calls
		return reply(o, "run-begin #T", decisionsHeader(1, seq-1, seq, "yes"), decisionRow(seq),
			decisionsEnd(2, seq, "yes")), nil
	}}
	pull, err := NewClient(runner).PullDecisions(context.Background(), "Tankbot", 0, false, 0)
	if err != nil {
		t.Fatalf("PullDecisions: %v", err)
	}
	if calls != decisionsMaxPages {
		t.Fatalf("%d call(s), want the bound %d", calls, decisionsMaxPages)
	}
	if len(pull.Rows) != decisionsMaxPages || !pull.PageBound || pull.Note == "" {
		t.Fatalf("pull: %d row(s), page_bound=%v, note=%q", len(pull.Rows), pull.PageBound, pull.Note)
	}
}

// TestPullDecisionsNoProgress: a page that brings no row while more=yes does not loop forever.
func TestPullDecisionsNoProgress(t *testing.T) {
	runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, "run-begin #T", decisionsHeader(0, 4, 4, "yes"), decisionsEnd(1, 4, "yes")), nil
	}}
	pull, err := NewClient(runner).PullDecisions(context.Background(), "Tankbot", 4, true, 0)
	if err != nil {
		t.Fatalf("PullDecisions: %v", err)
	}
	if len(runner.seen) != 1 || len(pull.Rows) != 0 || pull.Note == "" {
		t.Fatalf("%d call(s), %d row(s), note %q", len(runner.seen), len(pull.Rows), pull.Note)
	}
}

// TestPullDecisionsRepeatedPage: a module that sends a row at or below the cursor again is refused, never read as a
// second page.
func TestPullDecisionsRepeatedPage(t *testing.T) {
	calls := 0
	runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		calls++
		if calls == 1 {
			return reply(o, "run-begin #T", decisionsHeader(1, 0, 1, "yes"), decisionRow(1),
				decisionsEnd(2, 1, "yes")), nil
		}
		return reply(o, "run-begin #T", decisionsHeader(1, 1, 1, "yes"), decisionRow(1),
			decisionsEnd(2, 1, "yes")), nil
	}}
	pull, err := NewClient(runner).PullDecisions(context.Background(), "Tankbot", 0, false, 0)
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("err = %v, want ErrBadReport for a repeated row", err)
	}
	if len(pull.Rows) != 1 {
		t.Fatalf("%d row(s) before the refusal, want 1", len(pull.Rows))
	}
}

// pageSpec is one scripted answer of the inconsistent-pages test: the header's fields and its rows.
type pageSpec struct {
	since, next int
	more        string
	run         string
	seqs        []int
}

// pageReply builds the module's answer for one pageSpec.
func pageReply(o command.Options, p pageSpec) *command.Reply {
	raws := []string{"run-begin #T",
		fmt.Sprintf("decisions run=%s lead=Tankbot plan=ragefire_chasm capture=cap-7 count=%d since=%d next=%d more=%s #T",
			p.run, len(p.seqs), p.since, p.next, p.more)}
	for _, q := range p.seqs {
		raws = append(raws, decisionRow(q))
	}
	raws = append(raws, decisionsEnd(len(p.seqs)+1, p.next, p.more))
	return reply(o, raws...)
}

// TestPullDecisionsInconsistentPages: every paging invariant a corrupt or confusing answer can break is refused
// instead of written.
func TestPullDecisionsInconsistentPages(t *testing.T) {
	cases := map[string][]pageSpec{
		"header since is not the cursor asked": {
			{since: 0, next: 2, more: "yes", run: "r-1", seqs: []int{1, 2}},
			{since: 0, next: 3, more: "no", run: "r-1", seqs: []int{3}},
		},
		"next is past the last row": {
			{since: 0, next: 9, more: "no", run: "r-1", seqs: []int{1, 2}},
		},
		"the second page repeats the cursor row": {
			{since: 0, next: 2, more: "yes", run: "r-1", seqs: []int{1, 2}},
			{since: 2, next: 2, more: "yes", run: "r-1", seqs: []int{2}},
		},
		"an empty page moved the cursor": {
			{since: 0, next: 2, more: "yes", run: "r-1", seqs: []int{1, 2}},
			{since: 2, next: 0, more: "no", run: "r-1"},
		},
		"another run on the second page": {
			{since: 0, next: 2, more: "yes", run: "r-1", seqs: []int{1, 2}},
			{since: 2, next: 3, more: "no", run: "r-2", seqs: []int{3}},
		},
	}
	for name, pages := range cases {
		n := 0
		runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
			if n >= len(pages) {
				return reply(o, "error code=no-run cmd=run #T"), nil
			}
			pg := pages[n]
			n++
			return pageReply(o, pg), nil
		}}
		if _, err := NewClient(runner).PullDecisions(context.Background(), "Tankbot", 0, false, 0); !errors.Is(err, ErrBadReport) {
			t.Errorf("%s: err = %v, want ErrBadReport", name, err)
		}
	}
}

// TestDecisionsTruncatedAnswer: a reply that lost its end marker is not a page, however many rows it carried.
func TestDecisionsTruncatedAnswer(t *testing.T) {
	runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		rep := reply(o, "run-begin #T", decisionsHeader(1, 0, 1, "no"), decisionRow(1))
		rep.Complete = false
		return rep, nil
	}}
	if _, err := NewClient(runner).RunDecisions(context.Background(), "Tankbot", 0, false, 0); !errors.Is(err, ErrBadReport) {
		t.Fatalf("err = %v, want ErrBadReport", err)
	}
}

// TestDecisionsRefusals: each documented refusal code ends the read with its own code, and a module build without
// the command is the sentinel, never a refusal.
func TestDecisionsRefusals(t *testing.T) {
	for _, code := range []string{"capture-off", "no-capture", "no-run", "bad-argument", "unexpected-argument"} {
		r := oneReply("error code=" + code + " cmd=run msg=no #T")
		_, err := NewClient(r).RunDecisions(context.Background(), "Tankbot", 0, false, 0)
		var refused *DecisionsRefusal
		if !errors.As(err, &refused) || refused.Code != code {
			t.Fatalf("code %s: err = %v", code, err)
		}
	}
	for _, code := range []string{"unknown-verb", "unsupported", "invalid", "engine-not-built"} {
		r := oneReply("error code=" + code + " cmd=run #T")
		_, err := NewClient(r).RunDecisions(context.Background(), "Tankbot", 0, false, 0)
		if !errors.Is(err, ErrNotSupported) {
			t.Fatalf("code %s: err = %v, want ErrNotSupported", code, err)
		}
		if !strings.Contains(err.Error(), "not supported by this module build") {
			t.Fatalf("code %s: err text %q", code, err)
		}
	}
}

// TestDecisionsMalformedRecord: every strictness rule of the shape refuses instead of filling a zero.
func TestDecisionsMalformedRecord(t *testing.T) {
	cases := map[string][]string{
		"missing capture": {"run-begin #T",
			"decisions run=r-1 lead=Tankbot plan=ragefire_chasm count=0 since=0 next=0 more=no #T",
			decisionsEnd(1, 0, "no")},
		"missing phase": {"run-begin #T", decisionsHeader(1, 0, 1, "no"),
			"decision seq=1 t=1764000000000 dt=200 state=running step=3 record=tick order= note= say= #T",
			decisionsEnd(2, 1, "no")},
		"unknown state": {"run-begin #T", decisionsHeader(1, 0, 1, "no"),
			"decision seq=1 t=1764000000000 dt=200 state=idle phase=walk step=3 record=tick order= note= say= #T",
			decisionsEnd(2, 1, "no")},
		"unknown record": {"run-begin #T", decisionsHeader(1, 0, 1, "no"),
			"decision seq=1 t=1764000000000 dt=200 state=running phase=walk step=3 record=ok order= note= say= #T",
			decisionsEnd(2, 1, "no")},
		"trunc=no": {"run-begin #T", decisionsHeader(1, 0, 1, "no"),
			"decision seq=1 t=1764000000000 dt=200 state=running phase=walk step=3 record=tick order= note= say= trunc=no #T",
			decisionsEnd(2, 1, "no")},
		"repeated seq": {"run-begin #T", decisionsHeader(2, 0, 2, "no"), decisionRow(1), decisionRow(1),
			decisionsEnd(3, 2, "no")},
		"wrong count": {"run-begin #T", decisionsHeader(1, 0, 1, "no"), decisionRow(1), decisionsEnd(5, 1, "no")},
		"a split row": {"run-begin #T", decisionsHeader(1, 0, 1, "no"),
			"decision seq=1 t=1764000000000 dt=200 state=running phase=walk step=3 record=tick order= note= say= #T",
			"decision+ seq=1 say=more #T", decisionsEnd(3, 1, "no")},
		"no header": {"run-begin #T", decisionRow(1), decisionsEnd(2, 1, "no")},
	}
	for name, lines := range cases {
		r := oneReply(lines...)
		_, err := NewClient(r).RunDecisions(context.Background(), "Tankbot", 0, false, 0)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestDecisionsTarget: the word names a leader or a run id, so a space, '#' or '=' never reaches the line.
func TestDecisionsTarget(t *testing.T) {
	for _, bad := range []string{"", "Tank bot", "Tank#1", "Tank=1", "a\tb"} {
		r := oneReply("run-begin #T", decisionsHeader(0, 0, 0, "no"), decisionsEnd(1, 0, "no"))
		if _, err := NewClient(r).RunDecisions(context.Background(), bad, 0, false, 0); err == nil {
			t.Errorf("target %q was accepted", bad)
		}
		if len(r.seen) != 0 {
			t.Errorf("target %q sent %v", bad, r.seen)
		}
	}
}

// TestDecisionsLimitClamp: the call never asks for more rows than the command allows.
func TestDecisionsLimitClamp(t *testing.T) {
	for _, c := range []struct {
		in   int
		want string
	}{{0, "limit=25"}, {-5, "limit=25"}, {1, "limit=1"}, {200, "limit=200"}, {5000, "limit=200"}} {
		r := oneReply("run-begin #T", decisionsHeader(0, 0, 0, "no"), decisionsEnd(1, 0, "no"))
		if _, err := NewClient(r).RunDecisions(context.Background(), "Tankbot", 0, false, c.in); err != nil {
			t.Fatalf("limit %d: %v", c.in, err)
		}
		if !strings.HasSuffix(r.seen[0], " "+c.want) {
			t.Fatalf("limit %d: line %q, want %s", c.in, r.seen[0], c.want)
		}
	}
}

// pageArgs reads the since= and limit= words of a decisions line as the module would.
func pageArgs(line string) (since, limit int, hasSince bool) {
	limit = decisionsPageDefault
	for _, w := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(w, "since="); ok {
			since, _ = strconv.Atoi(v)
			hasSince = true
		}
		if v, ok := strings.CutPrefix(w, "limit="); ok {
			limit, _ = strconv.Atoi(v)
		}
	}
	return since, limit, hasSince
}

// longDecisionRows is n rows whose three free texts are cut at the stored 240-byte bound and percent-encoded: the
// worst row the command writes, one line each.
func longDecisionRows(n int) []string {
	// 240 spaces is the stored bound, and every one is %20: the 720-byte text the command's worst row carries.
	text := strings.Repeat("%20", 240)
	rows := make([]string, 0, n)
	for seq := 1; seq <= n; seq++ {
		rows = append(rows, fmt.Sprintf("decision seq=%d t=%d dt=200 state=running phase=walk step=3 record=tick "+
			"order=%s note=%s say=%s", seq, 1764000000000+int64(seq)*200, text, text, text))
	}
	return rows
}

// collectorRunner answers every page through the real collector: the shared 64 KiB reply bound is what refuses a
// page, exactly as the module's answer meets it on the wire.
type collectorRunner struct {
	rows []string
	seen []string
}

func (r *collectorRunner) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	r.seen = append(r.seen, line)
	since, limit, _ := pageArgs(line)
	first := 0
	for first < len(r.rows) && rowSeq(r.rows[first]) <= since {
		first++
	}
	end := first + limit
	if end > len(r.rows) {
		end = len(r.rows)
	}
	next, more := since, "no"
	if end > first {
		next = rowSeq(r.rows[end-1])
	}
	if end < len(r.rows) {
		more = "yes"
	}
	lines := []string{"run-begin #" + o.Token,
		fmt.Sprintf("decisions run=r-20261124T190001-a1b2 lead=Tankbot plan=ragefire_chasm capture=cap-7 "+
			"count=%d since=%d next=%d more=%s #%s", end-first, since, next, more, o.Token)}
	for _, row := range r.rows[first:end] {
		lines = append(lines, row+" #"+o.Token)
	}
	lines = append(lines, fmt.Sprintf("run-end count=%d records=%d verb=decisions lead=Tankbot next=%d more=%s #%s",
		end-first+1, end-first+1, next, more, o.Token))
	return command.Run(ctx, &feedTransport{lines: lines}, line, o)
}

// rowSeq is a decision line's seq.
func rowSeq(raw string) int {
	for _, w := range strings.Fields(raw) {
		if v, ok := strings.CutPrefix(w, "seq="); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

// feedTransport feeds the collector one scripted line per packet.
type feedTransport struct{ lines []string }

func (t *feedTransport) SendCommand(context.Context, string) error { return nil }

func (t *feedTransport) Pump(_ context.Context, feed func(chat.Line), done func() bool) error {
	for _, s := range t.lines {
		feed(chat.Line{Type: chat.TypeSystem, Text: s})
		if done() {
			return nil
		}
	}
	return nil
}

// TestPullDecisionsLongRowsFitTheCollector: 200 rows at the command's stored-text bound go through the real
// collector without one reply-too-long: the page the pull asks for fits the shared 64 KiB bound.
func TestPullDecisionsLongRowsFitTheCollector(t *testing.T) {
	cr := &collectorRunner{rows: longDecisionRows(200)}
	pull, err := NewClient(cr).PullDecisions(context.Background(), "Tankbot", 0, false, 0)
	if err != nil {
		t.Fatalf("PullDecisions: %v", err)
	}
	if len(pull.Rows) != 200 || pull.Rows[199].Seq != 200 {
		t.Fatalf("%d row(s), last %d", len(pull.Rows), pull.Rows[len(pull.Rows)-1].Seq)
	}
	if len(cr.seen) < 2 {
		t.Fatalf("one page for 200 rows: %v", cr.seen)
	}
}

// TestPullDecisionsHalvesAPageOverTheReplyBound: a page the collector refuses is retried with half the rows, same
// cursor, so a run whose notes are worst-case long still completes.
func TestPullDecisionsHalvesAPageOverTheReplyBound(t *testing.T) {
	var limits []int
	runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		since, limit, hasSince := pageArgs(line)
		limits = append(limits, limit)
		if limit > 6 {
			return nil, command.ErrReplyTooLong
		}
		rows := make([]string, 0, limit)
		raws := []string{"run-begin #T"}
		for seq := since + 1; seq <= since+limit; seq++ {
			rows = append(rows, decisionRow(seq))
		}
		headerSince := since
		if !hasSince {
			headerSince = 0
		}
		raws = append(raws, decisionsHeader(len(rows), headerSince, since+limit, "no"))
		raws = append(raws, rows...)
		raws = append(raws, decisionsEnd(len(rows)+1, since+limit, "no"))
		return reply(o, raws...), nil
	}}
	pull, err := NewClient(runner).PullDecisions(context.Background(), "Tankbot", 0, false, 0)
	if err != nil {
		t.Fatalf("PullDecisions: %v", err)
	}
	if got := fmt.Sprint(limits); got != "[25 12 6]" {
		t.Fatalf("limits %s, want [25 12 6]", got)
	}
	if len(pull.Rows) != 6 || pull.Pages != 1 {
		t.Fatalf("%d row(s) in %d page(s), want 6 in 1", len(pull.Rows), pull.Pages)
	}
}

// TestPullDecisionsOneRowStillTooLong: at one row the retry stops and names the cursor the row would have come from.
func TestPullDecisionsOneRowStillTooLong(t *testing.T) {
	runner := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return nil, command.ErrReplyTooLong
	}}
	_, err := NewClient(runner).PullDecisions(context.Background(), "Tankbot", 0, false, 0)
	if !errors.Is(err, command.ErrReplyTooLong) {
		t.Fatalf("err = %v, want ErrReplyTooLong", err)
	}
	if !strings.Contains(err.Error(), "seq 0") {
		t.Fatalf("the error names no row: %v", err)
	}
	if want := "limit=1"; !strings.HasSuffix(runner.seen[len(runner.seen)-1], want) {
		t.Fatalf("last line %q, want %s", runner.seen[len(runner.seen)-1], want)
	}
}

// TestPullDecisionsSkipsARowWithAControlByteText: a tab or newline in a note is the one row-level loss the read takes;
// every other row of the page is kept and the pull counts the skip.
func TestPullDecisionsSkipsARowWithAControlByteText(t *testing.T) {
	cases := map[string]struct {
		rows    []string
		want    []int
		skipped int
	}{
		"middle row": {
			rows: []string{decisionRow(1),
				"decision seq=2 t=1764000000400 dt=200 state=running phase=walk step=3 record=tick " +
					"order=attack note=bad%09tab say= #T",
				decisionRow(3)},
			want: []int{1, 3}, skipped: 1,
		},
		"last row": {
			rows: []string{decisionRow(1),
				"decision seq=2 t=1764000000400 dt=200 state=running phase=walk step=3 record=tick " +
					"order=attack note=line%0Abreak say= #T"},
			want: []int{1}, skipped: 1,
		},
	}
	for name, c := range cases {
		next := rowSeq(c.rows[len(c.rows)-1])
		r := oneReply(append(append([]string{"run-begin #T"}, decisionsHeader(len(c.rows), 0, next, "no")),
			append(c.rows, decisionsEnd(len(c.rows)+1, next, "no"))...)...)
		pull, err := NewClient(r).PullDecisions(context.Background(), "Tankbot", 0, false, 0)
		if err != nil {
			t.Fatalf("%s: PullDecisions: %v", name, err)
		}
		if pull.Skipped != c.skipped || len(pull.Rows) != len(c.want) {
			t.Fatalf("%s: %d row(s), %d skipped, want %d/%d", name, len(pull.Rows), pull.Skipped, len(c.want), c.skipped)
		}
		for i, seq := range c.want {
			if pull.Rows[i].Seq != seq {
				t.Fatalf("%s: row %d seq=%d, want %d", name, i, pull.Rows[i].Seq, seq)
			}
		}
	}
}

// TestDecisionRowJSON keeps the documented keys, their types and an unknown key the module added.
func TestDecisionRowJSON(t *testing.T) {
	r := oneReply("run-begin #T", decisionsHeader(1, 0, 1, "no"),
		"decision seq=6 t=1764000001200 dt=200 state=running phase=walk step=3 record=tick order=skull note=n say= "+
			"trunc=yes future_key=a%23b #T", decisionsEnd(2, 1, "no"))
	page, err := NewClient(r).RunDecisions(context.Background(), "Tankbot", 0, false, 0)
	if err != nil {
		t.Fatalf("RunDecisions: %v", err)
	}
	b, err := page.Rows[0].JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, want := range []string{`"seq":6`, `"t":1764000001200`, `"dt":200`, `"state":"running"`,
		`"phase":"walk"`, `"step":3`, `"record":"tick"`, `"order":"skull"`, `"note":"n"`, `"say":""`,
		`"trunc":true`, `"future_key":"a#b"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("row JSON %s lacks %s", b, want)
		}
	}
}
