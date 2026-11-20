package live

import (
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

func scoreRunE(outDir string) ([]map[string]any, error) { return score.ScoreRunE(outDir) }

func joinPath(a, b string) string { return filepath.Join(a, b) }

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func dumpDoc(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return pyjson.Dump(path, doc)
}
