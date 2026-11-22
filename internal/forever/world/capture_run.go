package world

// The debug capture (Options.CaptureDir): one facility for every packet the combat, chat and party handlers see.
// --capture-dir names a parent, and each run writes into a NEW subdirectory of it (the link's start clock plus a
// random suffix). Nothing an earlier run wrote is touched: every file is opened with O_CREATE|O_EXCL, and a name
// collision is an error, not an overwrite. A path component that is a symlink or not a directory is refused, so a
// capture cannot be redirected through a link.
//
// Bounds: one run writes at most maxCapturePackets files and maxCaptureBytes of payload in total, each file one
// packet's payload (the frame reader's wire.MaxUncompressed caps one payload; a frame header's Size is a uint32). A
// serving daemon has ONE cap for the whole slot, across every reconnect (captureBudget): the same file and byte
// bounds in total, and at most
// maxCaptureRuns run directories, so a flapping link cannot write a full run per reconnect. Past a bound the run
// says so once and stops capturing; the run itself continues, because the capture is a debug aid.
//
// The manifest is one TSV line per captured packet, in write order: `<file name>\t0x<core opcode, 6 hex>\t<length>`.
// The file holds the payload bytes alone. `internal/forever/capturedir` reads a parent of run directories or one run
// directory.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// captureRunLayout is the clock part of a run directory's name.
const captureRunLayout = "20060102T150405Z"

// The bounds of one capture run: at most 8192 packet files and 32 MiB of payload in total. A longer or busier run
// keeps the first packets it wrote and reports that the capture stopped.
const (
	maxCapturePackets = 8192
	maxCaptureBytes   = 32 << 20
)

// maxCaptureRuns bounds how many run directories one slot may create across its reconnects.
const maxCaptureRuns = 64

// captureBudget is the one cap of a serving slot: every link of a serving daemon (the first login and every
// reconnect) shares it, so a flapping link cannot write maxCaptureBytes per reconnect. It carries the same bounds as
// one run (maxCapturePackets files, maxCaptureBytes of payload) for the whole slot, plus maxCaptureRuns run
// directories. It is created by Enter for a serving session and handed to each re-entry through the options. A
// standalone session (a check command) has no budget and is bounded per run, as before.
type captureBudget struct {
	mu      sync.Mutex
	packets int
	bytes   int
	runs    int
}

// startRun reserves one run directory, or errCaptureFull when the slot's runs or payload are spent.
func (b *captureBudget) startRun() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runs >= maxCaptureRuns || b.packets >= maxCapturePackets || b.bytes >= maxCaptureBytes {
		return errCaptureFull
	}
	b.runs++
	return nil
}

// take reserves one packet of n payload bytes, or errCaptureFull.
func (b *captureBudget) take(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.packets >= maxCapturePackets || b.bytes+n > maxCaptureBytes {
		return errCaptureFull
	}
	b.packets++
	b.bytes += n
	return nil
}

// errCaptureFull ends the capture of one run: its bounds are reached so the rest is skipped.
var errCaptureFull = errors.New("capture: the run's bound is reached")

// captureRepoRefusal refuses a capture parent inside a git work tree, naming the work tree's directory. A capture
// holds a run's identifiers (names, guids, payloads), so it never lands in a repository: it is refused before
// anything is created, whether or not the parent itself exists yet (a new directory inside a checkout is inside
// it too). Lstat is used so a repository whose .git is a worktree file counts like one with a directory.
func captureRepoRefusal(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("capture: %s: %w", path, err)
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return fmt.Errorf("capture: %s is inside the git work tree %s; a capture is written outside a repository", path, dir)
		}
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return nil
}

// CheckCaptureDir is captureRepoRefusal for a command that validates its --capture-dir before it logs in.
func CheckCaptureDir(path string) error { return captureRepoRefusal(path) }

// ensureCaptureDir creates path when it is missing and refuses any existing component that is not a real directory.
// MkdirAll would follow a symlinked component; this walks the path with Lstat instead.
func ensureCaptureDir(path string) error {
	clean := filepath.Clean(path)
	prefix := ""
	if filepath.IsAbs(clean) {
		prefix = string(filepath.Separator)
		clean = strings.TrimPrefix(clean, prefix)
	}
	cur := prefix
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(cur, 0o700); err != nil {
				return fmt.Errorf("capture: %s: %w", cur, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("capture: %s: %w", cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("capture: %s is a symlink", cur)
		}
		if !fi.IsDir() {
			return fmt.Errorf("capture: %s is not a directory", cur)
		}
	}
	return nil
}

// openCaptureRun creates a new run directory under base and opens its manifest exclusively. The directory is named
// from start's UTC clock and four random bytes; an existing directory of that name is a collision and an error.
func openCaptureRun(base string, start time.Time, random io.Reader) (string, *os.File, error) {
	if err := captureRepoRefusal(base); err != nil {
		return "", nil, err
	}
	if err := ensureCaptureDir(base); err != nil {
		return "", nil, err
	}
	var suffix [4]byte
	if _, err := io.ReadFull(random, suffix[:]); err != nil {
		return "", nil, fmt.Errorf("capture: the run's suffix: %w", err)
	}
	dir := filepath.Join(base, start.UTC().Format(captureRunLayout)+"-"+hex.EncodeToString(suffix[:]))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("capture: the run directory: %w", err)
	}
	f, err := openCaptureManifest(dir)
	if err != nil {
		return "", nil, err
	}
	return dir, f, nil
}

// openCaptureManifest creates the run's manifest exclusively: an existing file, including a symlink, is an error.
func openCaptureManifest(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "manifest.tsv"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("capture: the manifest: %w", err)
	}
	return f, nil
}

// captureRun is one run's directory and manifest.
type captureRun struct {
	dir      string
	manifest *os.File
	n        int
	bytes    int
	// budget is the slot's shared cap (nil for a standalone session): a packet that would pass it is refused.
	budget *captureBudget
}

// start creates the run directory on the first packet and returns the run; the caller logs the error once and
// skips captures for the rest of the run.
func startCaptureRun(base string, start time.Time) (*captureRun, error) {
	dir, f, err := openCaptureRun(base, start, rand.Reader)
	if err != nil {
		return nil, err
	}
	return &captureRun{dir: dir, manifest: f}, nil
}

// write stores one packet payload and its manifest line. Both files are created exclusively: a name this run
// already wrote, or one a concurrent run somehow made, is an error. Past maxCapturePackets files or maxCaptureBytes
// of payload it returns errCaptureFull and writes nothing.
func (r *captureRun) write(name string, core uint32, payload []byte) error {
	if r.n >= maxCapturePackets || r.bytes+len(payload) > maxCaptureBytes {
		return errCaptureFull
	}
	if r.budget != nil {
		if err := r.budget.take(len(payload)); err != nil {
			return err
		}
	}
	path := filepath.Join(r.dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("capture: %s: %w", name, err)
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return fmt.Errorf("capture: %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("capture: %s: %w", name, err)
	}
	r.n++
	r.bytes += len(payload)
	if _, err := fmt.Fprintf(r.manifest, "%s\t0x%06X\t%d\n", name, core, len(payload)); err != nil {
		return fmt.Errorf("capture: the manifest: %w", err)
	}
	return nil
}

// close closes the manifest (the run directory stays).
func (r *captureRun) close() error {
	if r == nil || r.manifest == nil {
		return nil
	}
	err := r.manifest.Close()
	r.manifest = nil
	return err
}

// capturePacket writes one received packet's payload to this run's capture, plus its manifest line (name, core
// opcode, length). The run directory is created on the first captured packet. A capture error is logged once and
// never ends the run: the option is a debugging aid. Only the session goroutine calls this.
func (s *session) capturePacket(p worldconn.Packet) {
	if s.opts.CaptureDir == "" || s.captureErr != nil {
		return
	}
	if s.captureRun == nil {
		if b := s.opts.captureBudget; b != nil {
			if err := b.startRun(); err != nil {
				s.captureErr = err
				s.opts.logf("capture: the slot's bound (%d packets, %d MiB or %d runs across every link) is reached (captures are skipped)",
					maxCapturePackets, maxCaptureBytes>>20, maxCaptureRuns)
				return
			}
		}
		run, err := startCaptureRun(s.opts.CaptureDir, s.start)
		if err == nil {
			run.budget = s.opts.captureBudget
		}
		if err != nil {
			s.captureErr = err
			s.opts.logf("capture: %v (captures are skipped)", err)
			return
		}
		s.captureRun = run
		s.opts.logf("capture: writing to %s", run.dir)
	}
	name := fmt.Sprintf("%04d_%s.bin", s.captureRun.n, strings.ReplaceAll(wire.ServerName(p.Core), " ", "_"))
	if err := s.captureRun.write(name, p.Core, p.Payload); err != nil {
		s.captureErr = err
		if errors.Is(err, errCaptureFull) {
			s.opts.logf("capture: the run's bound (%d packets or %d MiB) is reached (further captures are skipped)",
				maxCapturePackets, maxCaptureBytes>>20)
			return
		}
		s.opts.logf("capture: %v (further captures are skipped)", err)
		return
	}
	s.report.Captured++
}
