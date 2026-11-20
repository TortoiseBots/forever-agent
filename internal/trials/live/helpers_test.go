package live

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func ctxBackground() context.Context { return context.Background() }

func marshalDoc(doc map[string]any) ([]byte, error) { return json.Marshal(doc) }

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
