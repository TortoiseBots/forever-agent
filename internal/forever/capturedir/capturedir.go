// Package capturedir reads the debug capture that internal/forever/world writes for Options.CaptureDir: a parent
// directory holding one run directory per run (the link's start clock and four random bytes), each with a
// manifest.tsv (one `<file name>\t0x<core opcode>\t<length>` line per packet, in write order) and one file per
// packet holding the payload bytes alone. The fixture generators of the rounds use it to cut fixtures from a live
// run; nothing in production reads a capture.
package capturedir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Record is one captured packet: the file it was written to, its core opcode and its payload.
type Record struct {
	Name    string
	Opcode  uint32
	Payload []byte
}

// ErrNoRuns is what ReadDir returns when the directory holds neither a manifest nor a run directory.
var ErrNoRuns = errors.New("capturedir: the directory holds no capture run")

// ReadDir reads the capture under dir. dir may be one run directory (it holds manifest.tsv) or a parent of run
// directories; every immediate subdirectory with a manifest.tsv is read, in name (clock) order.
func ReadDir(dir string) ([]Record, error) {
	if _, err := os.Stat(filepath.Join(dir, "manifest.tsv")); err == nil {
		return ReadRun(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("capturedir: %w", err)
	}
	var runs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(sub, "manifest.tsv")); err != nil {
			continue
		}
		runs = append(runs, sub)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoRuns, dir)
	}
	sort.Strings(runs)
	var out []Record
	for _, run := range runs {
		recs, err := ReadRun(run)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// ReadRun reads one run directory: its manifest, then each packet file. A manifest line that is not three fields,
// an opcode that is not hex, a length that does not match the file, a packet file that is missing, and a name that
// is not a plain file name inside the run are all errors.
func ReadRun(dir string) ([]Record, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.tsv"))
	if err != nil {
		return nil, fmt.Errorf("capturedir: %w", err)
	}
	var out []Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("capturedir: %q is not name, opcode, length", line)
		}
		name := fields[0]
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("capturedir: %q is not a file name inside the run", name)
		}
		core, err := strconv.ParseUint(strings.TrimPrefix(fields[1], "0x"), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("capturedir: %q: the opcode: %w", line, err)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("capturedir: %q: the length %q", line, fields[2])
		}
		payload, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("capturedir: %w", err)
		}
		if len(payload) != size {
			return nil, fmt.Errorf("capturedir: %s is %d bytes, the manifest says %d", name, len(payload), size)
		}
		out = append(out, Record{Name: name, Opcode: uint32(core), Payload: payload})
	}
	return out, nil
}

// IsRunDir reports whether dir is one capture run directory.
func IsRunDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "manifest.tsv"))
	return err == nil
}
