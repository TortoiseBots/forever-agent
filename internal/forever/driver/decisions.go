package driver

// The decisions pull: at the end of a run whose command turned it on (--decisions-pull, off by default) the agent
// reads the module's decision rows of that run and writes them beside the record as <run_id>.decisions.jsonl, one
// JSON object per line, the form the combat log uses. The read is one bounded pull (the typed layer pages it); this
// file is the file, the summary block and the words a failed pull leaves behind. A failed pull is a warning in the
// summary and one log line; it never fails the run and never changes its exit code.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trace"
)

// DecisionsSuffix names the run's decision rows file beside the trace and the summary.
const DecisionsSuffix = ".decisions.jsonl"

// decisionsPullTimeout bounds the end-of-run read: the module answers pages of at most 200 rows, so a pull that has
// not finished by then (a lost link, a stuck command) is a warning and the run ends as it did.
const decisionsPullTimeout = 30 * time.Second

// ErrDecisionsNoPermission says the command channel did not confirm this account's command permission, so the read
// never went out. A player account sees it: every `.tb` line needs that permission, and a player run never asks for
// it.
var ErrDecisionsNoPermission = errors.New("driver: the command permission is not confirmed, so the decisions read was not sent")

// Decisions is a run's decisions read: one whole pull, pages and all, from the module of the run it addresses.
// Commander has the same method, so a watch reads through the commander it already holds.
type Decisions interface {
	RunDecisions(ctx context.Context) (tbcmd.DecisionsPull, error)
}

// RunDecisions is the summary's decisions block: how much the pull wrote and, when it wrote nothing, why. It carries
// no character name: the note is one of this file's own words, never the module's error text (which may name a
// leader).
type RunDecisions struct {
	Rows      int    `json:"rows"`
	Pages     int    `json:"pages,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Note      string `json:"note,omitempty"`
}

// DecisionsPath is the decisions file of a run.
func DecisionsPath(dir, runID string) string { return filepath.Join(dir, runID+DecisionsSuffix) }

// WriteRunDecisions writes a pull's rows as one JSON object per line into dir/<runID>.decisions.jsonl and returns the
// bytes written. A row count of zero writes no file, a run id that cannot name a file is refused, and a write error
// removes what it wrote: a caller never reads a partial page. The caller decides what to say; this function only
// writes.
func WriteRunDecisions(dir, runID string, pull tbcmd.DecisionsPull) (int64, error) {
	if !trace.ValidRunID(runID) {
		return 0, fmt.Errorf("decisions: %q is not a run id (letters, digits, . _ - ; at most 64)", runID)
	}
	if len(pull.Rows) == 0 {
		return 0, nil
	}
	buf := make([]byte, 0, 256*len(pull.Rows))
	for i, row := range pull.Rows {
		b, err := row.JSON()
		if err != nil {
			return 0, fmt.Errorf("decisions: row %d: %w", i+1, err)
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	at := DecisionsPath(dir, runID)
	if err := os.WriteFile(at, buf, 0o644); err != nil {
		_ = os.Remove(at)
		return 0, err
	}
	return int64(len(buf)), nil
}

// Decisions writes a pull into the record and returns the block the summary carries. A refused file (a run id that
// cannot name one, a write error) is the block's note; the run goes on.
func (r *Recorder) Decisions(pull tbcmd.DecisionsPull) RunDecisions {
	block := RunDecisions{Rows: 0, Pages: pull.Pages, Truncated: pull.Truncated, Note: pull.Note}
	if r == nil {
		block.Note = "no record folder was opened"
		return block
	}
	n, err := WriteRunDecisions(r.dir, r.runID, pull)
	if err != nil {
		block.Note = "the decisions file was not written: " + err.Error()
		return block
	}
	block.Rows, block.Bytes = len(pull.Rows), n
	r.sum.Decisions = &block
	return block
}

// DecisionsNote records a pull that read no row in the summary, with this file's own words for why.
func (r *Recorder) DecisionsNote(note string) RunDecisions {
	block := RunDecisions{Note: note}
	if r != nil {
		r.sum.Decisions = &block
	}
	return block
}

// decisionsWarning words a failed pull for the summary and the log. The summary's note is one of these fixed words;
// the full error goes to the log line only, because the module's error text can name the leader.
func decisionsWarning(err error) (note, log string) {
	var refused *tbcmd.DecisionsRefusal
	switch {
	case errors.Is(err, tbcmd.ErrNotSupported):
		return "not supported by this module build", "the module has no run decisions command"
	case errors.As(err, &refused):
		switch refused.Code {
		case "capture-off":
			return "the module's decision capture is off", "the module's decision capture is off (code=capture-off)"
		case "no-capture":
			return "the module keeps no capture for this run (a player-led or stopped run: its rows are in the module's folder)",
				"the module keeps no capture for this run (code=no-capture)"
		case "no-run":
			return "the module knows no run by that leader or id", "the module knows no run by that leader or id (code=no-run)"
		default:
			return "the module refused the read: " + refused.Code, "the module refused the read (code=" + refused.Code + ")"
		}
	case errors.Is(err, ErrDecisionsNoPermission):
		return "the account's command permission is not confirmed", "the command permission is not confirmed, so no read went out"
	case errors.Is(err, ErrLinkLost):
		return "the link is lost", "the link is lost, so the rows were not read"
	}
	return "the pull failed", err.Error()
}

// pullDecisions reads the run's decision rows once it has ended, when the config asks for it. A read that fails is
// one warning in the summary and one log line; the run's own outcome is unchanged.
func (r *Run) pullDecisions(ctx context.Context) {
	if !r.cfg.PullDecisions || r.cmd == nil {
		return
	}
	if r.rec == nil {
		r.logf("decisions: --decisions-pull needs --out, so nothing was written")
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), decisionsPullTimeout)
	defer cancel()
	pull, err := r.cmd.RunDecisions(pctx)
	if err != nil {
		note, log := decisionsWarning(err)
		r.rec.DecisionsNote(note)
		r.logf("decisions: %s", log)
		return
	}
	block := r.rec.Decisions(pull)
	r.logf("decisions: %d row(s) in %d page(s), %d bytes", block.Rows, block.Pages, block.Bytes)
	if block.Note != "" {
		r.logf("decisions: %s", block.Note)
	}
}
