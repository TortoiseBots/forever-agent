package world

// The module mode of `forever-agent decisions pull`: one run's decision rows, read through the session's command
// channel and written beside nothing else into the caller's folder. It is the same read a watch makes while its run
// lives (the typed layer pages it, bounded), with the caller naming the run and where the file goes. The module keeps
// a run's capture only while the run is there, so this command reads a live run: a stopped one answers no-capture, and
// its rows are in the module's own folder. The file's stem is the module's run id, which is a number.
//
// The read needs the command permission, like every `.tb` line: the session confirms the GM guard before it sends
// anything (an account without the permission never sends the line), and a module build without the command answers
// "not supported by this module build". Both are errors here: this is the command's whole job, not a run that goes on.

import (
	"context"
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/driver"
)

// DecisionsPullOptions is one decisions pull: Target is the leader or the run id the module takes, Since/HasSince
// start above an earlier cursor, Limit is the page the module is asked for (0 takes the module's default), Out is the
// folder of the file and RunID its stem (empty takes the module's own run id).
type DecisionsPullOptions struct {
	Target   string
	Since    int
	HasSince bool
	Limit    int
	Out      string
	RunID    string
}

// pullDecisions reads the rows and writes them. Everything it returns is the command's own answer: a refusal code, the
// unsupported-module message, a lost link or a write error.
func (s *session) pullDecisions(ctx context.Context, o *DecisionsPullOptions) error {
	if o == nil {
		return nil
	}
	if o.Out == "" {
		return errors.New("world: decisions pull: no output folder")
	}
	s.initChat()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = dungeonSettle
	}
	if err := s.collect(ctx, settle); err != nil {
		return err
	}
	if err := s.confirmGM(ctx); err != nil {
		return err
	}
	c := newDungeonCommander(s, o.Target)
	pull, err := c.client.PullDecisions(ctx, o.Target, o.Since, o.HasSince, o.Limit)
	if err != nil {
		return decisionsPullErr(err)
	}
	stem := o.RunID
	if stem == "" {
		// The file's name is the module's own run id, which is a number: anything else is refused, so a strange
		// answer never names a file beside the record.
		if !numericRunID(pull.Run) {
			return fmt.Errorf("world: decisions pull: the module's run id %q is not a number, so no file is named", pull.Run)
		}
		stem = pull.Run
	}
	written, err := driver.WriteRunDecisions(o.Out, stem, pull)
	if err != nil {
		return err
	}
	summary := driver.RunDecisions{Rows: len(pull.Rows), Pages: pull.Pages, Bytes: written, Skipped: pull.Skipped,
		Truncated: pull.Truncated, Note: pull.Note}
	s.report.Decisions = &summary
	if written > 0 {
		s.report.DecisionsFile = driver.DecisionsPath(o.Out, stem)
	}
	s.opts.logf("decisions: %d row(s) in %d page(s), %d bytes, run %s", summary.Rows, summary.Pages, written, pull.Run)
	if summary.Skipped > 0 {
		s.opts.logf("decisions: %d row(s) were left out: a note or say text carries a tab or a newline", summary.Skipped)
	}
	if summary.Note != "" {
		s.opts.logf("decisions: %s", summary.Note)
	}
	return nil
}

// numericRunID reports whether the module's run id is all digits: the module's ids are numbers, so a file named from
// one never carries a letter or a separator of its own.
func numericRunID(id string) bool {
	if id == "" {
		return false
	}
	for i := range len(id) {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

// decisionsPullErr words the typed layer's failures for the command: the module's refusal keeps its code, a build
// without the command says so, and the command channel's own refusals keep their text.
func decisionsPullErr(err error) error {
	var refused *tbcmd.DecisionsRefusal
	if errors.As(err, &refused) {
		if refused.Msg != "" {
			return fmt.Errorf("the module refused the read: %s: %s", refused.Code, refused.Msg)
		}
		return fmt.Errorf("the module refused the read: %s", refused.Code)
	}
	if errors.Is(err, command.ErrNoPermission) || errors.Is(err, errCommandNotConfirmed) {
		return fmt.Errorf("the command permission is not confirmed, so nothing was sent: %w", err)
	}
	return err
}
