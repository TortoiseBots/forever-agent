package driver

// The decisions pull: a watch whose command turned it on (--decisions-pull, off by default) reads the module's
// decision rows of the live run while it is still there and appends them beside the record as
// <run_id>.decisions.jsonl, one JSON object per line, the form the combat log uses. The module erases a run's capture
// when the run ends, so a read after the end answers no-capture: the watch reads incrementally during the run, reads
// once more just before it stops the run, and never reads after. A failed read is a warning in the summary and one
// log line; it never fails the run and never changes its exit code.

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

// decisionsPullTimeout bounds one decisions read: the typed layer pages it, and a read that has not finished by then
// (a lost link, a stuck command) is a warning and the watch goes on.
const decisionsPullTimeout = 30 * time.Second

// decisionsReadEvery is how often the watch reads the rows a live run added above its cursor. The module's capture
// keeps the newest 512 rows, and a run adds them at a fraction of this rate, so a read this often never outruns it;
// the interval is also far above the command's own rate limit, so an incremental read never floods the channel.
const decisionsReadEvery = 15 * time.Second

// ErrDecisionsNoPermission says the command channel did not confirm this account's command permission, so the read
// never went out. A player account sees it: every `.tb` line needs that permission, and a player run never asks for
// it.
var ErrDecisionsNoPermission = errors.New("driver: the command permission is not confirmed, so the decisions read was not sent")

// RunDecisions is the summary's decisions block: how much the watch wrote and, when it wrote nothing or left
// something out, why. It carries no character name: the note is one of this file's own words, never the module's
// error text (which may name a leader).
type RunDecisions struct {
	Rows      int    `json:"rows"`
	Pages     int    `json:"pages,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Skipped   int    `json:"skipped,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Note      string `json:"note,omitempty"`
}

// DecisionsPath is the decisions file of a run.
func DecisionsPath(dir, runID string) string { return filepath.Join(dir, runID+DecisionsSuffix) }

// decisionsFileRunID checks the stem of a decisions file: a run id of the trace's shape.
func decisionsFileRunID(runID string) error {
	if !trace.ValidRunID(runID) {
		return fmt.Errorf("decisions: %q is not a run id (letters, digits, . _ - ; at most 64)", runID)
	}
	return nil
}

// encodeDecisionsRows renders rows one JSON object per line.
func encodeDecisionsRows(rows []tbcmd.DecisionRow) ([]byte, error) {
	buf := make([]byte, 0, 256*len(rows))
	for i, row := range rows {
		b, err := row.JSON()
		if err != nil {
			return nil, fmt.Errorf("decisions: row %d: %w", i+1, err)
		}
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	return buf, nil
}

// WriteRunDecisions writes a pull's rows as one JSON object per line into dir/<runID>.decisions.jsonl and returns the
// bytes written. A row count of zero writes no file, a run id that cannot name a file is refused, and a write error
// removes what it wrote: a caller never reads a partial page. The caller decides what to say; this function only
// writes.
func WriteRunDecisions(dir, runID string, pull tbcmd.DecisionsPull) (int64, error) {
	if err := decisionsFileRunID(runID); err != nil {
		return 0, err
	}
	if len(pull.Rows) == 0 {
		return 0, nil
	}
	buf, err := encodeDecisionsRows(pull.Rows)
	if err != nil {
		return 0, err
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

// AppendRunDecisions appends a page's rows to dir/<runID>.decisions.jsonl, creating the file on the first row, and
// returns the bytes written. No row writes nothing. A write error cuts the file back to where it was, so a caller
// never reads a partial page.
func AppendRunDecisions(dir, runID string, rows []tbcmd.DecisionRow) (int64, error) {
	if err := decisionsFileRunID(runID); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	buf, err := encodeDecisionsRows(rows)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	at := DecisionsPath(dir, runID)
	f, err := os.OpenFile(at, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, err
	}
	before := info.Size()
	if _, err := f.Write(buf); err != nil {
		_ = f.Truncate(before)
		_ = f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		_ = os.Truncate(at, before)
		return 0, err
	}
	return int64(len(buf)), nil
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

// watchPullDecisions reads the decision rows above the watch's cursor and appends them to the run's file. It is
// called on the watch's interval and once more just before the stop: the module erases a run's capture when it ends,
// so a run that has already reached done or stopped has nothing newer to read, and a read that fails is one warning
// that never ends the watch.
func (r *Run) watchPullDecisions(ctx context.Context, w *watchRun) {
	if !r.cfg.PullDecisions || r.cmd == nil || w.decisionsOff || !w.have {
		return
	}
	if w.state == "done" || w.state == "stopped" {
		// The run ended by itself (or was stopped elsewhere): its capture went with the end, and nothing newer can
		// be read. The reads made while it lived are the record of what it did.
		return
	}
	if r.rec == nil {
		if !w.decisionsBlind {
			w.decisionsBlind = true
			r.logf("decisions: --decisions-pull needs --out, so nothing was written")
		}
		return
	}
	// The read runs after the caller's context ended too: an interrupt must not lose the rows that are still there.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), decisionsPullTimeout)
	defer cancel()
	pull, err := r.cmd.RunDecisions(pctx, w.decisionsSeq, w.decisionsHas)
	if err != nil {
		note, log := decisionsWarning(err)
		if !w.decisionsBlind {
			w.decisionsBlind = true
			r.logf("decisions: %s", log)
		}
		if errors.Is(err, ErrLinkLost) {
			// Nothing is sent after a lost link, so no later read goes out either.
			w.decisionsOff = true
		}
		if w.decisionsRows == 0 {
			w.decisionsNote = note
		}
		return
	}
	if w.decisionsBlind {
		w.decisionsBlind = false
		r.logf("decisions: the reads answer again")
	}
	if pull.Run != "" {
		if w.runID == "" {
			w.runID = pull.Run
		} else if pull.Run != w.runID {
			// A newer run of the same leader answers in its place: its rows are not this run's, and writing them
			// under this run's file would mix two runs.
			w.decisionsOff = true
			w.decisionsNote = "another run of the same leader replaced this one, so the pull stopped"
			r.logf("decisions: the module's run is not the one watched; nothing newer is read")
			return
		}
	}
	if len(pull.Rows) > 0 {
		n, err := AppendRunDecisions(r.rec.dir, r.rec.runID, pull.Rows)
		if err != nil {
			w.decisionsNote = "the decisions file was not written: " + err.Error()
			r.logf("decisions: %s", w.decisionsNote)
			return
		}
		w.decisionsRows += len(pull.Rows)
		w.decisionsBytes += n
	}
	w.decisionsReads++
	w.decisionsPages += pull.Pages
	w.decisionsSkipped += pull.Skipped
	if pull.Truncated {
		w.decisionsTrunc = true
	}
	if pull.Note != "" {
		w.decisionsPull = pull.Note
	}
	if pull.Next > w.decisionsSeq || (!w.decisionsHas && len(pull.Rows) > 0) {
		w.decisionsSeq, w.decisionsHas = pull.Next, true
	}
}

// watchDecisionsSummary writes what the reads added up to into the record's summary, with the skipped rows and the
// warning a failed read left. A watch that read nothing and had nothing to say leaves no block.
func (r *Run) watchDecisionsSummary(w *watchRun) {
	if !r.cfg.PullDecisions || r.rec == nil {
		return
	}
	if w.decisionsReads == 0 && w.decisionsNote == "" && w.decisionsSkipped == 0 && !w.decisionsTrunc {
		return
	}
	block := RunDecisions{
		Rows: w.decisionsRows, Pages: w.decisionsPages, Bytes: w.decisionsBytes,
		Skipped: w.decisionsSkipped, Truncated: w.decisionsTrunc,
	}
	switch {
	case w.decisionsNote != "":
		block.Note = w.decisionsNote
	case w.decisionsPull != "":
		block.Note = w.decisionsPull
	}
	r.rec.sum.Decisions = &block
	r.logf("decisions: %d row(s) in %d page(s), %d bytes", block.Rows, block.Pages, block.Bytes)
	if block.Skipped > 0 {
		r.logf("decisions: %d row(s) were left out: a note or say text carries a tab or a newline", block.Skipped)
	}
	if block.Note != "" {
		r.logf("decisions: %s", block.Note)
	}
}
