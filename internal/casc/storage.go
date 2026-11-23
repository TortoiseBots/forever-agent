package casc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound means the local index has no entry for the encoding key: the content is not installed here.
var ErrNotFound = errors.New("casc: not found in the local index")

// maxSpanSize bounds a span read from a data archive: the decoded cap plus one MiB for BLTE framing.
const maxSpanSize = maxBLTESize + 1<<20

// Storage is an open local CASC install: the selected build row, its build config, the merged local index and the
// data archives, which are opened on first use.
type Storage struct {
	dir   string // the Data/data directory, holding the .idx files and the data.NNN archives
	build BuildRow
	cfg   *Config
	index *Index
	files map[uint32]*os.File
}

// Open opens the CASC install at installDir. The build row is the one of product, or the only active row when
// product is empty; the build config and every bucket's newest index file are read.
func Open(installDir, product string) (*Storage, error) {
	info, err := LoadBuildInfo(filepath.Join(installDir, ".build.info"))
	if err != nil {
		return nil, err
	}
	row, err := info.Select(product)
	if err != nil {
		return nil, err
	}
	cfg, err := readConfig(installDir, row.BuildKey())
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(installDir, "Data", "data")
	index, err := LoadIndex(dir)
	if err != nil {
		return nil, err
	}
	return &Storage{dir: dir, build: row, cfg: cfg, index: index, files: map[uint32]*os.File{}}, nil
}

// Config returns the install's build config.
func (s *Storage) Config() *Config { return s.cfg }

// Build returns the build row the storage was opened with.
func (s *Storage) Build() BuildRow { return s.build }

// Close closes the data archives the storage opened.
func (s *Storage) Close() error {
	var first error
	for _, f := range s.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.files = nil
	return first
}

// ReadEKey returns the decoded content of the span that the first 9 bytes of ekey address. The span's 30-byte local
// header is skipped and its BLTE stream decoded; a key the local index does not hold is ErrNotFound.
func (s *Storage) ReadEKey(ekey []byte) ([]byte, error) {
	e, ok := s.index.lookup(ekey)
	if !ok {
		return nil, ErrNotFound
	}
	if e.size < 30 || uint64(e.size) > maxSpanSize {
		return nil, fmt.Errorf("casc: span size %d is outside 30..%d", e.size, maxSpanSize)
	}
	f, err := s.archiveFile(e.archive)
	if err != nil {
		return nil, err
	}
	span := make([]byte, e.size)
	if n, err := f.ReadAt(span, int64(e.offset)); n != len(span) {
		return nil, fmt.Errorf("casc: data.%03d holds %d of the %d span bytes at %#x: %v", e.archive, n, len(span), e.offset, err)
	}
	if got := binary.LittleEndian.Uint32(span[16:]); got != e.size {
		return nil, fmt.Errorf("casc: the span header at data.%03d %#x says %d bytes, the index says %d", e.archive, e.offset, got, e.size)
	}
	return DecodeBLTE(span[30:])
}

// archiveFile returns the open data.NNN file, opening it on first use.
func (s *Storage) archiveFile(n uint32) (*os.File, error) {
	if f, ok := s.files[n]; ok {
		return f, nil
	}
	path := filepath.Join(s.dir, fmt.Sprintf("data.%03d", n))
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if s.files == nil {
		s.files = map[uint32]*os.File{}
	}
	s.files[n] = f
	return f, nil
}
