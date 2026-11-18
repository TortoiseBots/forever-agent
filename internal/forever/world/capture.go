package world

// The debug capture of fight-check. With --capture-dir the session writes the payload of every received combat packet
// into a scratch directory, one JSON object per line. That directory belongs OUTSIDE the repository (it holds the
// run's GUIDs and clock values); the fixture generator rewrites the identifiers and the clock values before any
// payload becomes a committed fixture (docs/forever/COMBAT-R1-NOTES.md, "Fixtures and their source").

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// captureFile is the name of the capture inside the directory. It is rewritten (truncated) by each run.
const captureFile = "packets.jsonl"

// captureLine is one captured packet: the core opcode number and the payload. No direction is needed: only server
// combat packets are captured.
type captureLine struct {
	Opcode  uint32 `json:"opcode"`
	Payload string `json:"payload"`
}

// captureWriter appends one line per received combat packet. It is opened only when the operator asked for it, so a
// nil writer is the normal case.
type captureWriter struct {
	dir string
	f   *os.File
	enc *json.Encoder
	err error
}

// openCapture creates the directory and truncates the capture file.
func openCapture(dir string) (*captureWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("world: capture dir: %w", err)
	}
	f, err := os.Create(filepath.Join(dir, captureFile))
	if err != nil {
		return nil, fmt.Errorf("world: capture file: %w", err)
	}
	c := &captureWriter{dir: dir, f: f, enc: json.NewEncoder(f)}
	return c, nil
}

// write appends one packet. A write error is kept (the first one) and reported once by Close; the fight never stops
// because a debug capture could not be written.
func (c *captureWriter) write(core uint32, payload []byte) {
	if c == nil || c.err != nil {
		return
	}
	c.err = c.enc.Encode(captureLine{Opcode: core, Payload: hex.EncodeToString(payload)})
}

// Close returns the first write error and closes the file.
func (c *captureWriter) Close() error {
	if c == nil {
		return nil
	}
	err := c.err
	if cerr := c.f.Close(); err == nil {
		err = cerr
	}
	return err
}
