package live

import (
	"context"
	"path/filepath"
)

// AbsPath resolves out to an absolute path.
func AbsPath(out string) string {
	if abs, err := filepath.Abs(out); err == nil {
		return abs
	}
	return out
}

// RunOne runs one trial arg repeats times with r and returns its id.
func RunOne(ctx context.Context, r *Runner, trialArg string, repeats int) (string, error) {
	trial, err := findTrial(trialArg)
	if err != nil {
		return "", err
	}
	id := strOf(trial["id"])
	if _, err := r.RunTrial(ctx, trialArg, repeats); err != nil {
		return id, err
	}
	_ = trial
	return id, nil
}
