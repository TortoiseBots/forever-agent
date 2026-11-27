package live

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// TBRunner adapts tbcmd's Runner (one line, one collected reply) to the
// trials serve RPC: `bot` sends the `.tb` line and `chat_log` reads the
// server's visible answer lines (each with its seq). A tokened call sends
// with the run's token and collects from the log cursor until the reply's
// end marker (or the module's error line) carries the token; a tokenless
// call collects until the quiet window passes with no new line.
//
// The adapter exists because the runner never owns the session's packet
// loop: only the serve agent does. Every `.tb` send in this package goes
// through this runner, so the `.tb` vocabulary stays the test kit's typed
// calls plus the guard's one `gm` call site (guard.go). A bot RPC answer's
// refusal shape (a -32002 disconnect, a collector timeout) ends the repeat
// like any other transport failure.
type TBRunner struct {
	client *Client
	// ChatLog reads one page: the fake in fakeserve_test.go queues them.
	chatLog func(ctx context.Context, since uint64, waitMS int) (ChatPage, error)
	// Bot sends one module line, returning the server's visible lines.
	bot func(ctx context.Context, line string, waitMS, maxLines int) (BotResult, error)
	// now is the clock; nil means time.Now. Tests pass a fake clock.
	now func() time.Time
	// cursor is the shared chat-log cursor: a tokenless reply starts at the
	// log's current end, not at seq 0 (the session's whole chat history).
	mu        sync.Mutex
	cursor    uint64
	cursorSet bool
}

// ChatPage is one `chat_log` page: the log rows and the next cursor.
type ChatPage struct {
	Lines []ChatLine
	Next  uint64
}

// ChatLine is one visible server line with its log cursor.
type ChatLine struct {
	Seq  uint64
	Text string
}

// BotResult is one `bot` RPC answer: the line sent and the visible answer.
type BotResult struct {
	Sent     string
	Lines    []string
	Complete bool
	End      string
}

// CommandPage reads one chat_log page through the client.
func (c *Client) commandPage(ctx context.Context, since uint64, waitMS int) (ChatPage, error) {
	var raw struct {
		Events []struct {
			Seq  uint64 `json:"seq"`
			Text string `json:"text"`
		} `json:"events"`
		Next uint64 `json:"next"`
	}
	if err := c.call(ctx, "chat_log", map[string]any{"since": since, "limit": 200, "wait_ms": waitMS}, &raw, 30*time.Second); err != nil {
		return ChatPage{}, err
	}
	out := ChatPage{Next: raw.Next}
	for _, e := range raw.Events {
		out.Lines = append(out.Lines, ChatLine{Seq: e.Seq, Text: e.Text})
	}
	return out, nil
}

// CommandBot sends one module line through the client. maxLines lifts the
// server-side line cap of a tokenless reply (0 keeps the command default);
// a verbose dump such as `.bot inv` needs it to reach its end marker.
func (c *Client) commandBot(ctx context.Context, line string, waitMS, maxLines int) (BotResult, error) {
	var raw struct {
		Sent     string   `json:"sent"`
		Lines    []string `json:"lines"`
		Complete bool     `json:"complete"`
		End      string   `json:"end"`
	}
	params := map[string]any{"line": line, "wait_ms": waitMS}
	if maxLines > 0 {
		params["max_lines"] = maxLines
	}
	if err := c.call(ctx, "bot", params, &raw, 40*time.Second); err != nil {
		return BotResult{}, err
	}
	return BotResult{Sent: raw.Sent, Lines: raw.Lines, Complete: raw.Complete, End: raw.End}, nil
}

// NewTBRunner returns the `.tb` runner over client.
func NewTBRunner(client *Client) *TBRunner {
	r := &TBRunner{client: client}
	r.chatLog = client.commandPage
	r.bot = client.commandBot
	return r
}

// Run implements tbcmd.Runner. The `bot` RPC both sends and answers: when it
// returns complete lines they are the reply; otherwise the runner drains
// `chat_log` from the cursor until the token's end marker (or the module's
// error line) arrives, the quiet window passes with no new line, or the
// deadline ends the call.
func (r *TBRunner) Run(ctx context.Context, line string, opts command.Options) (*command.Reply, error) {
	token := opts.Token
	waitMS := 15000
	if opts.Deadline > 0 {
		if ms := int(opts.Deadline / time.Millisecond); ms < waitMS {
			waitMS = ms
		}
		if waitMS < 1000 {
			waitMS = 1000
		}
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}
	if token == "" {
		return r.runTokenless(ctx, line, opts, waitMS, now)
	}
	return r.runTokened(ctx, line, opts, waitMS, now)
}

// runTokened sends the line with the token and collects until its end.
func (r *TBRunner) runTokened(ctx context.Context, line string, opts command.Options, waitMS int, now func() time.Time) (*command.Reply, error) {
	sent, err := r.bot(ctx, line+" #"+opts.Token, waitMS, opts.MaxLines)
	if err != nil {
		return nil, err
	}
	raw := append([]string(nil), sent.Lines...)
	lastSeq, _ := r.head(ctx)
	complete := sent.Complete
	if rep := collectTokened(raw, line, opts.Token); rep != nil && rep.Complete {
		rep.Raw = raw
		r.drainToEnd(ctx)
		return rep, nil
	}
	if complete && len(raw) > 0 {
		// The RPC answered complete lines without this run's end: never
		// act on them; drain the log for the run's own end instead.
		raw = nil
	}
	deadline := now().Add(r.deadlineOf(opts, 15*time.Second))
	cursor := lastSeq
	_ = cursor
	for {
		if ctx.Err() != nil {
			rep := collectTokened(raw, line, opts.Token)
			if rep == nil {
				rep = &command.Reply{Line: line, Token: opts.Token, Raw: raw}
			}
			return rep, ctx.Err()
		}
		remain := int(deadline.Sub(now()) / time.Millisecond)
		if remain <= 0 {
			rep := collectTokened(raw, line, opts.Token)
			if rep == nil {
				rep = &command.Reply{Line: line, Token: opts.Token, Raw: raw}
			}
			return rep, command.ErrIncomplete
		}
		if remain > 2000 {
			remain = 2000
		}
		page, err := r.chatLog(ctx, lastSeq, remain)
		if err != nil {
			rep := collectTokened(raw, line, opts.Token)
			if rep == nil {
				rep = &command.Reply{Line: line, Token: opts.Token, Raw: raw}
			}
			return rep, err
		}
		for _, ln := range page.Lines {
			raw = append(raw, ln.Text)
			if ln.Seq+1 > lastSeq {
				lastSeq = ln.Seq + 1
			}
		}
		if rep := collectTokened(raw, line, opts.Token); rep != nil && rep.Complete {
			rep.Raw = raw
			rep.Token = opts.Token
			rep.Line = line
			r.advance(lastSeq)
			r.drainToEnd(ctx)
			return rep, classifyTokened(rep)
		}
		if len(page.Lines) == 0 && len(raw) > 0 {
			// No new line and still no end: the quiet window decides
			// (the collector's tokenless rule); one more short page
			// below would only repeat the same wait.
			rep := collectTokened(raw, line, opts.Token)
			if rep == nil {
				rep = &command.Reply{Line: line, Token: opts.Token, Raw: raw}
			}
			return rep, command.ErrIncomplete
		}
		lastSeq = page.Next
	}
}

// drainToEnd drops everything already in the chat log from the shared
// cursor: a reply answered by the RPC's own lines has no seqs here, and its
// lines must not be read as the next command's answer.
func (r *TBRunner) drainToEnd(ctx context.Context) {
	for i := 0; i < 64; i++ {
		r.mu.Lock()
		since := r.cursor
		r.mu.Unlock()
		page, err := r.chatLog(ctx, since, 0)
		if err != nil {
			return
		}
		if len(page.Lines) == 0 || page.Next <= since {
			return
		}
		r.advance(page.Next)
	}
}

// advance moves the shared cursor forward (never back): the next tokenless
// reply starts after this one.
func (r *TBRunner) advance(seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq > r.cursor || !r.cursorSet {
		r.cursor, r.cursorSet = seq, true
	}
}

// head advances the shared chat-log cursor to the log's current end (the
// first call drains everything already there and discards it): a tokenless
// reply is the lines after the send, never the session's chat history.
func (r *TBRunner) head(ctx context.Context) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cursorSet {
		return r.cursor, nil
	}
	since := uint64(0)
	for {
		page, err := r.chatLog(ctx, since, 0)
		if err != nil {
			return 0, err
		}
		if len(page.Lines) == 0 || page.Next <= since {
			next := page.Next
			if next < since {
				next = since
			}
			r.cursor, r.cursorSet = next, true
			return r.cursor, nil
		}
		since = page.Next
	}
}

// runTokenless sends the line and collects until the quiet window passes.
func (r *TBRunner) runTokenless(ctx context.Context, line string, opts command.Options, waitMS int, now func() time.Time) (*command.Reply, error) {
	lastSeq, err := r.head(ctx)
	if err != nil {
		return nil, err
	}
	sent, err := r.bot(ctx, line, waitMS, opts.MaxLines)
	if err != nil {
		return nil, err
	}
	raw := append([]string(nil), sent.Lines...)
	deadline := now().Add(r.deadlineOf(opts, 15*time.Second))
	quiet := opts.Quiet
	if quiet <= 0 {
		quiet = 1500 * time.Millisecond
	}
	lastAt := now()
	for {
		if ctx.Err() != nil {
			return tokenlessReply(line, raw, lastAt, now), ctx.Err()
		}
		remain := int(deadline.Sub(now()) / time.Millisecond)
		if remain <= 0 {
			rep := tokenlessReply(line, raw, lastAt, now)
			rep.End = "deadline"
			return rep, command.ErrIncomplete
		}
		if remain > 2000 {
			remain = 2000
		}
		page, err := r.chatLog(ctx, lastSeq, remain)
		if err != nil {
			return tokenlessReply(line, raw, lastAt, now), err
		}
		if len(page.Lines) == 0 {
			if now().Sub(lastAt) >= quiet {
				rep := tokenlessReply(line, raw, lastAt, now)
				rep.End = "quiet"
				rep.Complete = len(raw) > 0
				return rep, classifyTokenless(rep)
			}
			continue
		}
		for _, ln := range page.Lines {
			raw = append(raw, ln.Text)
			if ln.Seq+1 > lastSeq {
				lastSeq = ln.Seq + 1
			}
		}
		lastAt = now()
		lastSeq = page.Next
		r.advance(lastSeq)
	}
}

func (r *TBRunner) deadlineOf(opts command.Options, def time.Duration) time.Duration {
	if opts.Deadline > 0 {
		return opts.Deadline
	}
	return def
}

// collectTokened parses raw lines for a tokened reply: only a line carrying
// the token counts; the reply is complete with its end marker or the
// single error line (command.IsEndKind). Untagged traffic (another run's
// lines) never ends this run.
func collectTokened(raw []string, line, token string) *command.Reply {
	rep := &command.Reply{Line: line, Token: token, Raw: append([]string(nil), raw...)}
	var kept []command.ReplyLine
	for _, text := range raw {
		ln := command.ParseReplyLine(text)
		if ln.Token != token {
			continue
		}
		kept = append(kept, ln)
		if command.IsEndKind(ln.Kind) {
			rep.End = ln.Kind
			if ln.Kind == "error" || strings.HasSuffix(ln.Kind, "-end") {
				rep.RawEnd = text
				rep.Complete = true
			}
		}
	}
	rep.Lines = kept
	if len(kept) == 0 {
		return nil
	}
	return rep
}

// classifyTokened maps a complete tokened reply to the collector's errors:
// an error line is the module's refusal (nil here; tbcmd parses it), an
// end marker completes, anything else is incomplete.
func classifyTokened(rep *command.Reply) error {
	if rep == nil || !rep.Complete {
		return command.ErrIncomplete
	}
	return nil
}

// tokenlessReply builds the quiet-window reply.
func tokenlessReply(line string, raw []string, lastAt time.Time, now func() time.Time) *command.Reply {
	rep := &command.Reply{Line: line, Raw: append([]string(nil), raw...)}
	for _, text := range raw {
		ln := command.ParseReplyLine(text)
		rep.Lines = append(rep.Lines, ln)
	}
	rep.LastAt = lastAt
	rep.Elapsed = now().Sub(lastAt)
	return rep
}

// classifyTokenless maps the quiet reply: no line at all is ErrNoReply; a
// core refusal word in the tail is the matching named error (the same words
// command.Run classifies); anything else is the answer.
func classifyTokenless(rep *command.Reply) error {
	if rep == nil || len(rep.Lines) == 0 {
		return command.ErrNoReply
	}
	tail := ""
	if len(rep.Raw) > 0 {
		tail = rep.Raw[len(rep.Raw)-1]
	}
	lower := strings.ToLower(tail)
	switch {
	case strings.Contains(lower, "does not exist"):
		return &command.RefusalError{Err: command.ErrUnknownCommand, Text: tail}
	case strings.Contains(lower, "incorrect syntax") || strings.Contains(lower, "usage:"):
		return &command.RefusalError{Err: command.ErrSyntax, Text: tail}
	}
	if len(rep.Raw) > 0 && isEchoLine(rep.Raw[0]) {
		rep.Echoed = true
		return &command.RefusalError{Err: command.ErrNoPermission, Text: rep.Raw[0]}
	}
	return nil
}

// isEchoLine reports whether the reply repeats the sent command: the core's
// fall-through (a say echo) when the account may not run it.
func isEchoLine(text string) bool {
	t := strings.TrimSpace(text)
	return len(t) >= 2 && (t[0] == '.' || t[0] == '!') && !strings.Contains(t, "TortoiseBots:")
}

// checkTBRunner is the compile-time seam: *TBRunner implements tbcmd.Runner.
var _ tbcmd.Runner = (*TBRunner)(nil)
