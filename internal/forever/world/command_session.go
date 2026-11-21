package world

// Round A6, the command channel: the session side of internal/forever/command. The collector does not own a
// socket; this file pumps the session's own packet loop, and every chat line the loop handles goes to the running
// command through session.cmdFeed before the chat log's display-sized copy is made, so the command sees the whole
// line while unrelated system lines still reach the log. A line reaches at most one run: the feed is set only
// while a run's pump is active, and command.Session serialises the runs.
//
// `forever cmd-check` runs here: a settle, the GM guard, the caller's lines, the typed state table, and the guard's
// undo. Nothing is sent before the guard confirms the account holds the command permission: an account without it
// gets its line said as ordinary say chat by the core (Chat.cpp:166-179).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// gmProbe is the one command sent before anything the caller asked for. It is the core's non-mutating `gm`
	// form: ForeverCore's gm command table (src/server/scripts/Commands/cs_gm.cpp:46-60) has no bare invoker, so
	// `.gm` prints the subcommand help; the entry `|- gm on` is listed only for an account that holds
	// RBAC_PERM_COMMAND_GM (371), the permission the module's `.tb` commands need too. It changes nothing, so
	// there is no mode to restore and no way to switch off a character that had GM mode on.
	gmProbe = ".gm"
	// gmProbeQuiet and gmProbeDeadline bound the probe: the help block is sent at once, and the quiet window
	// leaves room for a notification or a late line before the decision.
	gmProbeQuiet    = 1500 * time.Millisecond
	gmProbeDeadline = 4 * time.Second
	// cmdPoll is how long one pump iteration waits for a server packet before it re-checks the collector's clock.
	cmdPoll = 100 * time.Millisecond
)

// errCommandNotConfirmed is what every send path returns for a command line while the GM guard has not confirmed
// the permission. cmdLink.SendCommand is the one place that enforces it.
var errCommandNotConfirmed = errors.New("world: the GM guard has not confirmed this account's command permission, so the line was not sent")

// gmAcknowledgement reports whether a probe reply is the probe's specific acknowledgement: the `gm on` entry of
// the gm help block, which the core lists only to an account holding RBAC_PERM_COMMAND_GM (371). A notification,
// a MOTD, an announcement or "Command 'gm' does not exist" is not it.
func gmAcknowledgement(rep *command.Reply) bool {
	if rep == nil || rep.Echoed {
		return false
	}
	for _, ln := range rep.Lines {
		switch strings.TrimSpace(ln.Raw) {
		case "|- gm on", "|- gm off":
			return true
		}
	}
	return false
}

// cmdLink is one run's transport: the session's own SendCommand, and a pump over the session's packet loop that
// hands every chat line to the collector through session.cmdFeed. The line the collector sees is the raw parsed
// line bounded at command.MaxLineBytes, not the chat log's display-sized copy: the module's state lines are longer
// than a display line (a typical one is about 530 bytes, the longest about 900), and the log would cut them.
type cmdLink struct {
	s *session
	// probe marks the one link that may send the probe while the guard is unconfirmed. No other link can send a
	// command line: SendCommand refuses it, so runOne, tbcmd.Client and any future caller are guarded alike.
	probe bool
}

func (l *cmdLink) SendCommand(ctx context.Context, text string) error {
	if l.s.gmStop != nil {
		return l.s.gmStop
	}
	if !l.probe && command.IsCommandText(text) && !l.s.gmConfirmed {
		return errCommandNotConfirmed
	}
	return l.s.SendCommandCtx(ctx, text)
}

func (l *cmdLink) Pump(ctx context.Context, feed func(chat.Line), done func() bool) error {
	l.s.cmdFeed = feed
	defer func() { l.s.cmdFeed = nil }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if done() {
			return nil
		}
		// The pump reads on the run's own context, not the session's: a stop or cleanup run handed a detached
		// context still has to see its reply after the caller's interrupt ended the session's context.
		ev, err := l.s.nextCtx(ctx, "the command reply", cmdPoll)
		if err != nil {
			if errors.Is(err, errWaitTimeout) {
				continue
			}
			return err
		}
		if err := l.s.handle(ctx, ev); err != nil {
			return err
		}
	}
}

// feedCommand hands one chat line to a running command, bounded by the command collector's own line limit. The
// text is NOT sanitised here: the collector matches the server's exact words (the module's markers, refusals,
// tokens and order acknowledgements) and makes its own display copy; the chat log gets a separately sanitised
// copy afterwards. It also watches every self say line: a command text said aloud (now, between runs, or long
// after the probe) means the account's commands fall through as chat, so the guard is reset and every later send
// is refused.
func (s *session) feedCommand(line chat.Line) {
	if line.Type == chat.TypeWhisper {
		s.fbRememberWhisper(line)
	}
	if line.Type == chat.TypeSay && s.isSelf(line) && command.IsCommandText(line.Text) {
		s.gmConfirmed = false
		if s.report != nil {
			s.report.GMConfirmed = false
		}
		if s.gmStop == nil {
			s.gmStop = fmt.Errorf("%w (the server said %q as ordinary say chat)", command.ErrNoPermission, chat.Sanitise(line.Text, command.MaxLineBytes))
		}
	}
	if s.cmdFeed == nil {
		return
	}
	line.Text = rawBound(line.Text, command.MaxLineBytes)
	s.cmdFeed(line)
}

// isSelf reports whether a chat line was sent by this character: the create block's name or GUID when they have
// arrived, else the active mover's GUID (the server fills SenderName only for a whisper).
func (s *session) isSelf(line chat.Line) bool {
	if s.report != nil && s.report.Character.Name != "" &&
		strings.EqualFold(strings.TrimSpace(line.SenderName), s.report.Character.Name) {
		return true
	}
	self := s.selfOrMover()
	return !self.IsZero() && line.SenderGUID == self
}

// selfOrMover is this character's GUID: the create block's when it has arrived, else the active mover's.
func (s *session) selfOrMover() wire.GUID {
	if s.selfSeen {
		return s.self
	}
	return s.report.ActiveMoverGUID
}

// commandOptions fills the session's defaults into one run's options. A caller that passes none gets the
// session's own name, GUID, quiet window and deadline.
func (s *session) commandOptions(o command.Options) command.Options {
	if o.SelfName == "" {
		o.SelfName = s.report.Character.Name
	}
	if o.SelfGUID.IsZero() {
		o.SelfGUID = s.self
		if o.SelfGUID.IsZero() {
			// The create block may not have arrived yet (the active-mover exchange names this character first).
			o.SelfGUID = s.report.ActiveMoverGUID
		}
	}
	if o.Quiet <= 0 {
		o.Quiet = s.opts.CommandQuiet
	}
	if o.Deadline <= 0 {
		o.Deadline = s.opts.CommandTimeout
	}
	return o
}

// runCommandLine fills the session's defaults into one run's options and runs it through the queue.
func (s *session) runCommandLine(ctx context.Context, q *command.Session, line string, o command.Options) (*command.Reply, error) {
	return q.Run(ctx, line, s.commandOptions(o))
}

// commandCheck is `forever cmd-check`: settle, confirm the GM permission, run the caller's lines, print the typed
// state table. A refusal of one of the caller's lines is a result, not an end; a lost link or the run's deadline
// ends it. The probe changes nothing, so there is no flag to put back.
func (s *session) commandCheck(ctx context.Context) error {
	s.initChat()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 4 * time.Second
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	q := command.NewSession(&cmdLink{s: s})
	if err := s.confirmGM(ctx); err != nil {
		return err
	}
	for _, line := range s.opts.CommandLines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := s.runOne(ctx, q, line); err != nil {
			return err
		}
	}
	if s.opts.CommandTBState != "" {
		if err := s.printTBState(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// confirmGM sends the probe and decides. The account holds the permission only when the probe's specific
// acknowledgement (the `gm on` entry of the gm help block) arrives and a say echo appears nowhere in the run: a
// notification, a late MOTD or an announcement that arrives first is not an answer, and the echo check stays
// active after the first line. No acknowledgement, or any echo, is not confirmed, and nothing else is sent.
func (s *session) confirmGM(ctx context.Context) error {
	link := &cmdLink{s: s, probe: true}
	rep, err := command.Run(ctx, link, gmProbe, s.commandOptions(command.Options{Quiet: gmProbeQuiet, Deadline: gmProbeDeadline}))
	s.report.Commands = append(s.report.Commands, commandRun(gmProbe, rep, err))
	s.opts.logf("cmd: %s  (the GM guard's probe)", gmProbe)
	if rep != nil {
		for _, l := range rep.Raw {
			s.opts.logf("cmd-r: %s", l)
		}
	}
	switch {
	case rep != nil && rep.Echoed:
		s.gmConfirmed = false
		return fmt.Errorf("world: the account is not a GM: the server said %q as ordinary say chat, so it did not run and this run sends nothing else (%w)",
			gmProbe, command.ErrNoPermission)
	case gmAcknowledgement(rep):
		// The block also contains "Possible subcommands:", which the collector names a syntax refusal; that is
		// what a help block looks like, and the specific `gm on` entry is what proves the permission.
	case err != nil:
		return fmt.Errorf("world: GM not confirmed (%q: %v); this run sends nothing else", gmProbe, err)
	default:
		return fmt.Errorf("world: GM not confirmed (%q answered %d line(s) but none was the gm permission's acknowledgement); this run sends nothing else",
			gmProbe, len(rep.Raw))
	}
	s.gmConfirmed = true
	s.report.GMConfirmed = true
	s.opts.logf("cmd-check: GM confirmed (the probe listed the gm command); the caller's commands will be sent")
	return nil
}

// commandRun copies one run's outcome into the report.
func commandRun(line string, rep *command.Reply, err error) CommandRun {
	row := CommandRun{Line: line}
	if rep != nil {
		row.Lines = append([]string(nil), rep.Raw...)
		row.End = rep.End
		row.Refusal = rep.RefusalText
		if rep.Refusal != nil {
			row.RefusalErr = rep.Refusal.Error()
		}
	}
	if err != nil {
		row.Err = err.Error()
	}
	return row
}

// noteGuard reads one run's outcome into the guard. A say echo (or the named no-permission refusal) means the
// account's commands are falling through as chat: the guard is reset and every later send is refused.
func (s *session) noteGuard(rep *command.Reply, err error) {
	if (rep != nil && rep.Echoed) || errors.Is(err, command.ErrNoPermission) {
		s.gmConfirmed = false
		if s.report != nil {
			s.report.GMConfirmed = false
		}
		if s.gmStop == nil {
			s.gmStop = fmt.Errorf("%w", command.ErrNoPermission)
		}
	}
}

// runOne runs one of the caller's lines and prints what it answered. A refusal or an incomplete report is logged
// and the loop continues; a say echo, the guard's refusal to send, the link's death or the run's own deadline ends
// the run.
func (s *session) runOne(ctx context.Context, q *command.Session, line string) error {
	_, err := s.runOneResult(ctx, q, line)
	return err
}

// oneResult is one command's reply and the command run's own error (nil when it ended well).
type oneResult struct {
	Rep    *command.Reply
	RunErr error
}

// runOneResult is runOne that also hands back the reply and the run's own error, for a caller, such as the act
// runner's gm step, that must tell a clear success from a refusal the command session only logs. The error is what
// runOne returns.
func (s *session) runOneResult(ctx context.Context, q *command.Session, line string) (oneResult, error) {
	rep, runErr := s.runCommandLine(ctx, q, line, command.Options{})
	return oneResult{Rep: rep, RunErr: runErr}, s.finishOne(rep, runErr, line)
}

// finishOne records and classifies one command's outcome (see runOne).
func (s *session) finishOne(rep *command.Reply, err error, line string) error {
	s.noteGuard(rep, err)
	s.report.Commands = append(s.report.Commands, commandRun(line, rep, err))
	s.opts.logf("cmd: %s", line)
	if rep != nil {
		for _, l := range rep.Raw {
			s.opts.logf("cmd-r: %s", l)
		}
	}
	switch {
	case err == nil:
		s.opts.logf("cmd: %d line(s), ended by %s", len(rep.Raw), endName(rep.End))
		return nil
	case errors.Is(err, errCommandNotConfirmed), errors.Is(err, s.gmStop),
		errors.Is(err, command.ErrNoPermission):
		return fmt.Errorf("world: the GM guard stopped the run after %q: %w", line, err)
	case errors.Is(err, command.ErrIncomplete):
		s.opts.logf("cmd: not acted on: %v (%d line(s))", err, len(rep.Raw))
		return nil
	case errors.Is(err, command.ErrUnknownCommand), errors.Is(err, command.ErrSyntax),
		errors.Is(err, command.ErrNoReply),
		errors.Is(err, command.ErrReplyTooLong):
		s.opts.logf("cmd: refused: %v", err)
		return nil
	default:
		// A lost link, the run's deadline, or a packet the session could not parse while the reply arrived: the
		// session's own failure, which ends the run as it would outside a command.
		return fmt.Errorf("world: the command run ended: %w", err)
	}
}

func endName(end string) string {
	switch end {
	case "":
		return "no marker"
	case "quiet":
		return "the quiet window"
	case "maxlines":
		return "the line cap"
	case "deadline":
		return "the deadline"
	case "echo":
		return "the say echo"
	}
	return end
}

// CommandRun is one command's outcome, for the report and the tests.
type CommandRun struct {
	Line       string
	Lines      []string
	End        string
	Refusal    string // the line that made it a refusal
	RefusalErr string // the named refusal error's message
	Err        string
}

// TBStateTable is a plain copy of the typed state report for the run's report and its tests: no GUIDs.
type TBStateTable struct {
	Count   int
	Resting int
	Target  string
	Bots    []TBStateRow
}

// TBStateRow is one bot's row of the state table.
type TBStateRow struct {
	Name           string
	Role           string
	Class          string
	Level          int
	HP, HPMax      int
	Power          int
	PowerMax       int
	PowerType      string
	State          string
	Move           string
	TargetEntry    int
	HasTargetEntry bool
	TargetHPPct    int
	HasTargetHPPct bool
	Order          string
	Resting        bool
	RestReasons    []string
}

// printTBState reads the typed state report and prints a table. A module that refuses or does not answer fails
// this step (the caller asked for a table it did not get).
func (s *session) printTBState(ctx context.Context, q *command.Session) error {
	client := tbcmd.NewClient(q)
	client.Base = command.Options{SelfName: s.report.Character.Name, SelfGUID: s.selfOrMover(), Quiet: s.opts.CommandQuiet, Deadline: s.opts.CommandTimeout}
	rep, err := client.StateReport(ctx, s.opts.CommandTBState)
	if err != nil {
		s.noteGuard(nil, err)
		if errors.Is(err, command.ErrNoPermission) || errors.Is(err, errCommandNotConfirmed) || errors.Is(err, s.gmStop) {
			return fmt.Errorf("world: the GM guard stopped the run: %w", err)
		}
		return fmt.Errorf("world: --tb-state %s: %w", chat.SanitiseName(s.opts.CommandTBState), err)
	}
	table := &TBStateTable{Count: len(rep.Bots), Resting: rep.Resting, Target: rep.Target}
	s.report.TBState = table
	s.opts.logf("tb-state: %d bot(s), %d resting, target %s", len(rep.Bots), rep.Resting, rep.Target)
	s.opts.logf("tb-state: %-12s %-6s %-11s %-9s %-16s %-10s %-8s %-14s %s",
		"bot", "role", "class/level", "hp", "power", "st", "mv", "tgt", "ord/resting")
	for _, b := range rep.Bots {
		row := TBStateRow{
			Name: b.Name, Role: b.Role, Class: b.Class, Level: b.Level,
			HP: b.HP, HPMax: b.HPMax, Power: b.Power, PowerMax: b.PowerMax, PowerType: b.PowerType,
			State: b.State, Move: b.Move,
			TargetEntry: b.TargetEntry, HasTargetEntry: b.HasTargetEntry,
			TargetHPPct: b.TargetHPPct, HasTargetHPPct: b.HasTargetHPPct,
			Resting: b.Resting, RestReasons: append([]string(nil), b.RestReasons...),
		}
		power := fmt.Sprintf("%d/%d", b.Power, b.PowerMax)
		if b.PowerType != "" {
			power = fmt.Sprintf("%d/%d %s", b.Power, b.PowerMax, b.PowerType)
		}
		tgt := "-"
		if b.HasTargetEntry {
			tgt = fmt.Sprintf("entry %d", b.TargetEntry)
			if b.HasTargetHPPct {
				tgt += fmt.Sprintf(" %d%%", b.TargetHPPct)
			}
		}
		ord := "-"
		if b.Order.Status == "fail" {
			ord = "fail:" + b.Order.Code
		} else if b.Order.Status == "queued" || b.Order.Status == "ok" {
			ord = fmt.Sprintf("%d:%s", b.Order.Seq, b.Order.Status)
		}
		row.Order = ord
		rest := "ready"
		if b.Resting {
			rest = "resting"
			if len(b.RestReasons) > 0 {
				rest += "(" + strings.Join(b.RestReasons, "+") + ")"
			}
		}
		table.Bots = append(table.Bots, row)
		s.opts.logf("tb-state: %-12s %-6s %-11s %-9s %-16s %-10s %-8s %-14s %s %s",
			b.Name, b.Role, fmt.Sprintf("%s/%d", b.Class, b.Level), fmt.Sprintf("%d/%d", b.HP, b.HPMax),
			power, b.State, b.Move, tgt, ord, rest)
	}
	return nil
}
