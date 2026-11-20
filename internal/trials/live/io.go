package live

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

func scoreRunE(outDir string) ([]map[string]any, error) { return score.ScoreRunE(outDir) }

func joinPath(a, b string) string { return filepath.Join(a, b) }

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// dumpDoc writes doc atomically: a same-directory temp file, fsync, rename.
// A mid-write crash leaves the old file, never a truncation.
func dumpDoc(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := pyjson.Marshal(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
