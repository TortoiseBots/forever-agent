package capturedir

// Scan is scrub-check's reader: it walks every file a run directory holds (the payloads the manifest lists,
// manifest.tsv itself, and any other file a person dropped beside them) and reports the files that hold one of the
// needles in any form capture.Variants lists. It never returns a needle or a file's bytes: a Finding names the run,
// the file and which needle matched.
//
// The reader keeps the writer's bounds and refuses what readManifest refuses: a symlink, FIFO or device is not read,
// a file over the per-packet bound and a run over the writer's payload cap plus a full manifest are errors. So a
// capture that was edited to point at a big or a linked file is refused before its content reaches memory.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/capture"
)

// Finding is one file of one run that holds a needle: the run directory's name, the file's name inside it and the
// index of the needle in the needles argument Scan was given.
type Finding struct {
	Run    string `json:"run"`
	File   string `json:"file"`
	Needle int    `json:"needle"`
}

// The bounds of one scanned run: the writer's payload cap (maxCaptureBytes in internal/forever/world) plus a full
// manifest (maxManifestBytes), and the shortest needle capture.Scan accepts (3 bytes: shorter proves nothing).
const (
	maxRunScanBytes = 33 << 20
	minNeedle       = 3
)

var (
	errNotRegular = errors.New("not a regular file (a symlink or device is refused)")
	errScanTooBig = fmt.Errorf("larger than %d bytes", maxPacketBytes)
)

// Scan reports every file under dir that holds one of the needles in any form capture.Variants lists. dir is what
// ReadDir takes: one run directory, or a parent of them. A needle under 3 bytes is capture.ErrSecretTooShort. A file
// matching several needles is one Finding: the first needle that matches.
func Scan(dir string, needles [][]byte) ([]Finding, error) {
	found, _, err := Check(dir, needles)
	return found, err
}

// Check is Scan that also answers how many files it read, for the command's clean line.
func Check(dir string, needles [][]byte) ([]Finding, int, error) {
	vars := make([][][]byte, len(needles))
	for j, n := range needles {
		if len(n) < minNeedle {
			return nil, 0, fmt.Errorf("capturedir: needle %d: %w", j, capture.ErrSecretTooShort)
		}
		vars[j] = capture.Variants(n)
	}
	runs, err := captureRuns(dir)
	if err != nil {
		return nil, 0, err
	}
	var found []Finding
	files := 0
	for _, run := range runs {
		runName := filepath.Base(run)
		entries, err := os.ReadDir(run)
		if err != nil {
			return nil, 0, fmt.Errorf("capturedir: %w", err)
		}
		// The run's total is settled before a byte is read, so an oversized run is refused cheaply.
		var total int64
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				return nil, 0, fmt.Errorf("capturedir: %s/%s: %w", runName, e.Name(), err)
			}
			if !info.Mode().IsRegular() {
				return nil, 0, fmt.Errorf("capturedir: %s/%s is %w", runName, e.Name(), errNotRegular)
			}
			if info.Size() > maxPacketBytes {
				return nil, 0, fmt.Errorf("capturedir: %s/%s is %w", runName, e.Name(), errScanTooBig)
			}
			if total > maxRunScanBytes-info.Size() {
				return nil, 0, fmt.Errorf("capturedir: %s is larger than %d bytes in total", runName, maxRunScanBytes)
			}
			total += info.Size()
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				return nil, 0, fmt.Errorf("capturedir: %s/%s: %w", runName, e.Name(), err)
			}
			data, err := readScanned(filepath.Join(run, e.Name()), info)
			if err != nil {
				return nil, 0, fmt.Errorf("capturedir: %s/%s: %w", runName, e.Name(), err)
			}
			files++
			for j := range vars {
				hit := false
				for _, v := range vars[j] {
					if bytes.Contains(data, v) {
						hit = true
						break
					}
				}
				if hit {
					found = append(found, Finding{Run: runName, File: e.Name(), Needle: j})
					break
				}
			}
		}
	}
	return found, files, nil
}

// readScanned reads one file Scan accepted from its directory entry, bounded by maxPacketBytes. The open file must
// be the very regular file the entry named: a swap to a symlink between ReadDir and Open is refused.
func readScanned(path string, want os.FileInfo) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || !os.SameFile(want, st) {
		return nil, errNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPacketBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPacketBytes {
		return nil, errScanTooBig
	}
	return data, nil
}

// IsCaptureDir reports whether dir resolves to capture runs: one run directory, or a parent holding at least one.
func IsCaptureDir(dir string) bool {
	_, err := captureRuns(dir)
	return err == nil
}
