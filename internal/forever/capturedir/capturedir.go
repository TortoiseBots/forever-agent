// Package capturedir reads the debug capture that internal/forever/world writes for Options.CaptureDir: a parent
// directory holding one run directory per run (the link's start clock and four random bytes), each with a
// manifest.tsv (one `<file name>\t0x<core opcode>\t<length>` line per packet, in write order) and one file per
// packet holding the payload bytes alone. The fixture generators of the rounds use it to cut fixtures from a live
// run; nothing in production reads a capture.
package capturedir

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Record is one captured packet: the file it was written to, its core opcode and its payload.
type Record struct {
	Name    string
	Opcode  uint32
	Payload []byte
}

// ErrNoRuns is what ReadDir returns when the directory holds neither a manifest nor a run directory.
var ErrNoRuns = errors.New("capturedir: the directory holds no capture run")

// Stat is one opcode's totals in a capture: its core opcode, the name this client gives it and how many packets and
// payload bytes the manifests record for it. A core opcode without a name in this build is named by its number.
type Stat struct {
	Opcode  uint32 `json:"opcode"`
	Name    string `json:"name"`
	Packets int    `json:"packets"`
	Bytes   int64  `json:"bytes"`
}

// captureRuns resolves dir to its run directories: the directory itself when it holds a manifest, else every
// immediate subdirectory that does, in name (clock) order.
func captureRuns(dir string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, "manifest.tsv")); err == nil {
		return []string{dir}, nil
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
	return runs, nil
}

// ReadDir reads the capture under dir. dir may be one run directory (it holds manifest.tsv) or a parent of run
// directories; every immediate subdirectory with a manifest.tsv is read, in name (clock) order.
func ReadDir(dir string) ([]Record, error) {
	runs, err := captureRuns(dir)
	if err != nil {
		return nil, err
	}
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

// Stats sums a capture's manifests per opcode, one row per core opcode, by name. dir is what ReadDir takes: one run
// directory or a parent of them. Only manifest.tsv is read, so the answer is the same when a payload file is
// missing or was deleted (a run that was cut short, a fixture whose payloads went to a generator).
func Stats(dir string) ([]Stat, error) {
	runs, err := captureRuns(dir)
	if err != nil {
		return nil, err
	}
	byCore := make(map[uint32]*Stat)
	for _, run := range runs {
		entries, err := readManifest(run)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			stat := byCore[e.core]
			if stat == nil {
				name := wire.ServerName(e.core)
				if name == "" {
					name = fmt.Sprintf("0x%06X", e.core)
				}
				stat = &Stat{Opcode: e.core, Name: name}
				byCore[e.core] = stat
			}
			stat.Packets++
			if stat.Bytes > math.MaxInt64-int64(e.size) {
				return nil, fmt.Errorf("capturedir: the byte total overflows")
			}
			stat.Bytes += int64(e.size)
		}
	}
	out := make([]Stat, 0, len(byCore))
	for _, stat := range byCore {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Opcode < out[j].Opcode
	})
	return out, nil
}

// manifestEntry is one parsed manifest line: the packet file's name, its core opcode and its payload length.
type manifestEntry struct {
	name string
	core uint32
	size int
	line int
}

// The bounds a manifest reader accepts. They are the writer's (internal/forever/world/capture_run.go): one run writes
// at most 8192 packets and 32 MiB of payload, each payload at most a frame's 65535 bytes, and a manifest line is a
// short name plus two numbers. A hostile file is refused before its content reaches an error message or an int64
// total; the writer's per-run line count also keeps the sum far inside int64.
const (
	maxManifestBytes   = 4 << 20
	maxManifestEntries = 8192
	maxManifestLine    = 4096
	maxPacketBytes     = 65535
)

// readManifest parses a run's manifest. The file must be a regular file (a symlink, FIFO or device is refused) and
// within the writer's bounds. A line that is not three fields, an opcode that is not hex, a length outside a
// frame, a name that is not a plain file name inside the run, too many entries and an over-long line are errors.
// Errors name the line number and the reason, never the line's content. No payload is opened.
func readManifest(dir string) ([]manifestEntry, error) {
	path := filepath.Join(dir, "manifest.tsv")
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("capturedir: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("capturedir: manifest.tsv is not a regular file")
	}
	if fi.Size() > maxManifestBytes {
		return nil, fmt.Errorf("capturedir: manifest.tsv is larger than %d bytes", maxManifestBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("capturedir: %w", err)
	}
	defer f.Close()
	// The path may have been swapped for a symlink between Lstat and Open: what is read must be the regular file
	// Lstat described.
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("capturedir: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() > maxManifestBytes {
		return nil, fmt.Errorf("capturedir: manifest.tsv is not a bounded regular file")
	}
	sc := bufio.NewScanner(io.LimitReader(f, maxManifestBytes+1))
	sc.Buffer(make([]byte, 0, 256), maxManifestLine)
	var out []manifestEntry
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if line == "" {
			continue
		}
		if len(out) >= maxManifestEntries {
			return nil, fmt.Errorf("capturedir: manifest.tsv has more than %d entries", maxManifestEntries)
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("capturedir: manifest line %d is not name, opcode, length", n)
		}
		name := fields[0]
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("capturedir: manifest line %d: the first field is not a file name inside the run", n)
		}
		core, err := strconv.ParseUint(strings.TrimPrefix(fields[1], "0x"), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("capturedir: manifest line %d: the opcode is not hex", n)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size > maxPacketBytes {
			return nil, fmt.Errorf("capturedir: manifest line %d: the length is not 0..%d", n, maxPacketBytes)
		}
		out = append(out, manifestEntry{name: name, core: uint32(core), size: size, line: n})
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("capturedir: manifest line %d is longer than %d bytes", n+1, maxManifestLine)
		}
		return nil, fmt.Errorf("capturedir: %w", err)
	}
	return out, nil
}

// ReadRun reads one run directory: its manifest, then each packet file. A manifest that readManifest refuses, a
// length that does not match the file and a packet file that is missing are all errors; the errors name the manifest
// line, never the file's payload.
func ReadRun(dir string) ([]Record, error) {
	entries, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(entries))
	for _, e := range entries {
		payload, err := os.ReadFile(filepath.Join(dir, e.name))
		if err != nil {
			return nil, fmt.Errorf("capturedir: manifest line %d: the packet file: %w", e.line, err)
		}
		if len(payload) != e.size {
			return nil, fmt.Errorf("capturedir: manifest line %d: the packet is %d bytes, the manifest says %d", e.line, len(payload), e.size)
		}
		out = append(out, Record{Name: e.name, Opcode: e.core, Payload: payload})
	}
	return out, nil
}

// IsRunDir reports whether dir is one capture run directory.
func IsRunDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "manifest.tsv"))
	return err == nil
}
