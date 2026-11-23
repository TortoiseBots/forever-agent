package casc

import (
	"bytes"
	"crypto/md5"
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
	enc   *Encoding // read on the first ReadFileDataID
	root  *Root     // read on the first ReadFileDataID
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

// ReadFileDataID returns the decoded content of the file the root names with id. The encoding file and the root are
// read on the first call; a FileDataID the root does not name is ErrNotFound, and a file whose decoded bytes do not
// hash to the root's content key is an error rather than a silently wrong picture.
func (s *Storage) ReadFileDataID(id uint32) ([]byte, error) {
	enc, err := s.encodingFile()
	if err != nil {
		return nil, err
	}
	root, err := s.rootFile(enc)
	if err != nil {
		return nil, err
	}
	e, ok := root.files[id]
	if !ok {
		return nil, fmt.Errorf("casc: FileDataID %d is not in the root: %w", id, ErrNotFound)
	}
	ekey, err := enc.EKeyFor(e.ckey)
	if err != nil {
		return nil, fmt.Errorf("casc: FileDataID %d: %w", id, err)
	}
	data, err := s.ReadEKey(ekey)
	if err != nil {
		return nil, fmt.Errorf("casc: FileDataID %d: %w", id, err)
	}
	if got := md5.Sum(data); !bytes.Equal(got[:], e.ckey) {
		return nil, fmt.Errorf("casc: FileDataID %d decodes to content %x, the root says %x", id, got, e.ckey)
	}
	return data, nil
}

// encodingFile reads and parses the encoding file the build config names, on first use.
func (s *Storage) encodingFile() (*Encoding, error) {
	if s.enc != nil {
		return s.enc, nil
	}
	data, err := s.ReadEKey(s.cfg.Encoding.EKey)
	if err != nil {
		return nil, fmt.Errorf("casc: read the encoding file: %w", err)
	}
	if err := checkContentKey(data, s.cfg.Encoding.CKey, "encoding file"); err != nil {
		return nil, err
	}
	enc, err := ParseEncoding(data)
	if err != nil {
		return nil, err
	}
	s.enc = enc
	return enc, nil
}

// rootFile reads the root the build config names through enc, on first use.
func (s *Storage) rootFile(enc *Encoding) (*Root, error) {
	if s.root != nil {
		return s.root, nil
	}
	ekey, err := enc.EKeyFor(s.cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("casc: root file: %w", err)
	}
	data, err := s.ReadEKey(ekey)
	if err != nil {
		return nil, fmt.Errorf("casc: read the root file: %w", err)
	}
	if err := checkContentKey(data, s.cfg.Root, "root file"); err != nil {
		return nil, err
	}
	root, err := ParseRoot(data)
	if err != nil {
		return nil, err
	}
	s.root = root
	return root, nil
}

// checkContentKey verifies that data hashes to the content key the build config gives for it.
func checkContentKey(data, ckey []byte, what string) error {
	if len(ckey) != md5.Size {
		return fmt.Errorf("casc: %s content key is %d bytes", what, len(ckey))
	}
	if got := md5.Sum(data); !bytes.Equal(got[:], ckey) {
		return fmt.Errorf("casc: the %s hashes to %x, the build config says %x", what, got, ckey)
	}
	return nil
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
