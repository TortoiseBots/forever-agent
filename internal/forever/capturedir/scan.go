package capturedir

// Scan is scrub-check's reader: it walks every file a run directory holds (the payloads the manifest lists,
// manifest.tsv itself, and any other file a person dropped beside them) and reports the files that hold one of the
// needles in any form capture.Variants lists, or an IPv4 address. It never returns a needle or a file's bytes: a
// Finding names the run, the file, what matched and (for a needle) which one.
//
// Redact is Scan's other half: it writes a copy of every run whose needles and addresses are masked in place. The
// mask keeps each match's length, so a run's manifest lines stay valid and the copy still reads.
//
// The reader keeps the writer's bounds and refuses what readManifest refuses: a symlink, FIFO or device is not read,
// a file over the per-packet bound and a run over the writer's payload cap plus a full manifest are errors. So a
// capture that was edited to point at a big or a linked file is refused before its content reaches memory.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/pjerra/forever-agent/internal/capture"
)

// Kind is what a finding holds: the class the command prints, so a caller never learns a secret to name it.
type Kind string

const (
	KindAccount    Kind = "an account name"
	KindPassword   Kind = "a password"
	KindSessionKey Kind = "a session key"
	KindAuthToken  Kind = "an auth token"
	// KindIPAddress is a finding with no needle behind it: a dotted quad anywhere in a file.
	KindIPAddress Kind = "an IP address"
)

// Needle is one secret the scan looks for: its bytes and the class its finding prints.
type Needle struct {
	Bytes []byte
	Kind  Kind
}

// Finding is one file of one run that holds a needle or an address: the run directory's name, the file's name inside
// it, the index of the needle in the needles argument (minus one for an address, which has no needle) and the class.
type Finding struct {
	Run    string `json:"run"`
	File   string `json:"file"`
	Needle int    `json:"needle"`
	Kind   Kind   `json:"kind"`
}

// The bounds of one scanned run: the writer's payload cap (maxCaptureBytes in internal/forever/world) plus a full
// manifest (maxManifestBytes), and the shortest needle capture.Scan accepts (3 bytes: shorter proves nothing).
const (
	maxRunScanBytes = 33 << 20
	minNeedle       = 3
)

// maskByte is what a redacted match becomes: one printable byte per original byte, so a masked file keeps its size.
const maskByte = '*'

// ipv4Re finds dotted quads. The octets are validated afterwards: 256.1.2.3 is no address, and its last three
// groups must not be reported as one either.
var ipv4Re = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

var (
	errNotRegular = errors.New("not a regular file (a symlink or device is refused)")
	errScanTooBig = fmt.Errorf("larger than %d bytes", maxPacketBytes)
)

// Scan reports every file under dir that holds one of the needles in any form capture.Variants lists, or an IPv4
// address. dir is what ReadDir takes: one run directory, or a parent of them. A needle under 3 bytes is
// capture.ErrSecretTooShort. A file matching several needles is one Finding: the first needle that matches wins over
// an address, and an address is one Finding with Needle -1.
func Scan(dir string, needles []Needle) ([]Finding, error) {
	found, _, err := Check(dir, needles)
	return found, err
}

// Check is Scan that also answers how many files it read, for the command's clean line.
func Check(dir string, needles []Needle) ([]Finding, int, error) {
	vars, err := needleVariants(needles)
	if err != nil {
		return nil, 0, err
	}
	runs, err := captureRuns(dir)
	if err != nil {
		return nil, 0, err
	}
	var found []Finding
	files := 0
	for _, run := range runs {
		runName := filepath.Base(run)
		entries, infos, err := scanEntries(run, runName)
		if err != nil {
			return nil, 0, err
		}
		for i, e := range entries {
			data, err := readScanned(filepath.Join(run, e.Name()), infos[i])
			if err != nil {
				return nil, 0, fmt.Errorf("capturedir: %s/%s: %w", runName, e.Name(), err)
			}
			files++
			hit, needle := -1, -1
			for j := range vars {
				for _, v := range vars[j] {
					if bytes.Contains(data, v) {
						hit, needle = int(j), j
						break
					}
				}
				if hit >= 0 {
					break
				}
			}
			if hit >= 0 {
				found = append(found, Finding{Run: runName, File: e.Name(), Needle: needle, Kind: needlesKind(needles[hit])})
				continue
			}
			if hasIPv4(data) {
				found = append(found, Finding{Run: runName, File: e.Name(), Needle: -1, Kind: KindIPAddress})
			}
		}
	}
	return found, files, nil
}

// RedactReport is what one redacted copy took: the runs and files written and the number of masked matches.
type RedactReport struct {
	Runs   int `json:"runs"`
	Files  int `json:"files"`
	Masked int `json:"masked"`
}

// Redact writes a cleaned copy of the capture under dir into out: every run directory, every regular file, with each
// needle in every form and every IPv4 address masked by one '*' per byte. dir is what Check takes; out must not
// exist and must not be inside dir. A symlink, a non-regular entry or a file or run past the writer's bounds is
// refused before anything is written, so a refused copy leaves no directory behind. Every file keeps its length, so
// the manifests stay valid.
func Redact(dir, out string, needles []Needle) (RedactReport, error) {
	var rep RedactReport
	vars, err := needleVariants(needles)
	if err != nil {
		return rep, err
	}
	runs, err := captureRuns(dir)
	if err != nil {
		return rep, err
	}
	// Every run is validated before the target is made: a refused copy leaves no directory behind.
	type listedRun struct {
		dir     string
		name    string
		entries []os.DirEntry
		infos   []os.FileInfo
	}
	lists := make([]listedRun, 0, len(runs))
	for _, run := range runs {
		name := filepath.Base(run)
		entries, infos, err := scanEntries(run, name)
		if err != nil {
			return rep, err
		}
		lists = append(lists, listedRun{dir: run, name: name, entries: entries, infos: infos})
	}
	if err := redactTarget(dir, out); err != nil {
		return RedactReport{}, err
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		return RedactReport{}, fmt.Errorf("capturedir: %w", err)
	}
	for _, run := range lists {
		runDir := filepath.Join(out, run.name)
		if err := os.Mkdir(runDir, 0o700); err != nil {
			return RedactReport{}, fmt.Errorf("capturedir: %w", err)
		}
		rep.Runs++
		// The manifest is re-emitted from its parsed entries: every file name is masked (and the files are copied
		// under their masked names), while the opcode and the length stay what they were, so the copy reads. A
		// manifest a cut-short run left unparseable is masked byte for byte instead; its names then mask the same
		// way, so the files still line up.
		names := make(map[string]string)
		manifestOK := false
		var manifest bytes.Buffer
		if entries, err := readManifest(run.dir); err == nil {
			manifestOK = true
			used := make(map[string]string, len(entries))
			for _, en := range entries {
				masked, _ := maskAll([]byte(en.name), vars)
				name := string(masked)
				if orig, ok := used[name]; ok && orig != en.name {
					return RedactReport{}, fmt.Errorf("capturedir: %s: two file names mask alike", run.name)
				}
				used[name] = en.name
				names[en.name] = name
				fmt.Fprintf(&manifest, "%s\t0x%06X\t%d\n", name, en.core, en.size)
			}
		}
		for i, e := range run.entries {
			data, err := readScanned(filepath.Join(run.dir, e.Name()), run.infos[i])
			if err != nil {
				return RedactReport{}, fmt.Errorf("capturedir: %s/%s: %w", run.name, e.Name(), err)
			}
			masked, n := maskAll(data, vars)
			rep.Masked += n
			name := e.Name()
			if e.Name() == "manifest.tsv" && manifestOK {
				masked = manifest.Bytes()
			} else if renamed, ok := names[name]; ok {
				name = renamed
			} else {
				b, _ := maskAll([]byte(name), vars)
				name = string(b)
			}
			f, err := os.OpenFile(filepath.Join(runDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return RedactReport{}, fmt.Errorf("capturedir: %s/%s: %w", run.name, name, err)
			}
			if _, err := f.Write(masked); err != nil {
				f.Close()
				return RedactReport{}, fmt.Errorf("capturedir: %s/%s: %w", run.name, name, err)
			}
			if err := f.Close(); err != nil {
				return RedactReport{}, fmt.Errorf("capturedir: %s/%s: %w", run.name, name, err)
			}
			rep.Files++
		}
	}
	return rep, nil
}

// redactTarget refuses an out that exists or that lives inside the capture, so a copy never lands on a file it is
// reading or over an earlier one.
func redactTarget(dir, out string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("capturedir: %w", err)
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("capturedir: %w", err)
	}
	if absOut == absDir || len(absOut) > len(absDir) && absOut[:len(absDir)] == absDir && absOut[len(absDir)] == filepath.Separator {
		return fmt.Errorf("capturedir: %s is inside the capture %s", out, dir)
	}
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("capturedir: %s exists (a redacted copy is written to a new directory)", out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("capturedir: %w", err)
	}
	return nil
}

// scanEntries lists a run's entries and their file infos after settling the run's bounds, so an oversized run is
// refused cheaply and before a byte is read.
func scanEntries(run, runName string) ([]os.DirEntry, []os.FileInfo, error) {
	entries, err := os.ReadDir(run)
	if err != nil {
		return nil, nil, fmt.Errorf("capturedir: %w", err)
	}
	infos := make([]os.FileInfo, len(entries))
	var total int64
	for i, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, nil, fmt.Errorf("capturedir: %s/%s: %w", runName, e.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("capturedir: %s/%s is %w", runName, e.Name(), errNotRegular)
		}
		if info.Size() > maxPacketBytes {
			return nil, nil, fmt.Errorf("capturedir: %s/%s is %w", runName, e.Name(), errScanTooBig)
		}
		if total > maxRunScanBytes-info.Size() {
			return nil, nil, fmt.Errorf("capturedir: %s is larger than %d bytes in total", runName, maxRunScanBytes)
		}
		total += info.Size()
		infos[i] = info
	}
	return entries, infos, nil
}

// needleVariants expands every needle to the forms capture.Variants lists, in needle order, and refuses a needle
// shorter than 3 bytes.
func needleVariants(needles []Needle) ([][][]byte, error) {
	vars := make([][][]byte, len(needles))
	for j, n := range needles {
		if len(n.Bytes) < minNeedle {
			return nil, fmt.Errorf("capturedir: needle %d: %w", j, capture.ErrSecretTooShort)
		}
		vars[j] = capture.Variants(n.Bytes)
	}
	return vars, nil
}

// needlesKind is a needle's class, the account name when the caller named none.
func needlesKind(n Needle) Kind {
	if n.Kind == "" {
		return KindAccount
	}
	return n.Kind
}

// maskAll replaces every needle form and every address in data with mask bytes of the same length, and reports how
// many matches it masked. The needle forms are sorted longest first, so a form that contains another is masked whole.
func maskAll(data []byte, vars [][][]byte) ([]byte, int) {
	var forms [][]byte
	for _, v := range vars {
		forms = append(forms, v...)
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	out, masked := data, 0
	for _, v := range forms {
		if len(v) == 0 {
			continue
		}
		n := bytes.Count(out, v)
		if n == 0 {
			continue
		}
		out = bytes.ReplaceAll(out, v, maskOf(len(v)))
		masked += n
	}
	out, n := maskIPv4(out)
	return out, masked + n
}

// maskIPv4 masks every dotted quad in data, keeping its length.
func maskIPv4(data []byte) ([]byte, int) {
	var b bytes.Buffer
	last, n := 0, 0
	for _, m := range ipv4Re.FindAllIndex(data, -1) {
		if !validIPv4(data[m[0]:m[1]]) {
			continue
		}
		b.Write(data[last:m[0]])
		b.Write(maskOf(m[1] - m[0]))
		last, n = m[1], n+1
	}
	if n == 0 {
		return data, 0
	}
	b.Write(data[last:])
	return b.Bytes(), n
}

// hasIPv4 reports whether data holds a well-formed dotted quad.
func hasIPv4(data []byte) bool {
	for _, m := range ipv4Re.FindAllIndex(data, -1) {
		if validIPv4(data[m[0]:m[1]]) {
			return true
		}
	}
	return false
}

// validIPv4 reports whether b is four decimal octets, each 0..255.
func validIPv4(b []byte) bool {
	ip := net.ParseIP(string(b))
	return ip != nil && ip.To4() != nil
}

// maskOf is n mask bytes.
func maskOf(n int) []byte {
	return bytes.Repeat([]byte{maskByte}, n)
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
