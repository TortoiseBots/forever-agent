package casc

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// indexEntry is where an encoding key's span lives: the data.NNN archive, the byte offset of its 30-byte local
// header, and the size of the whole span (header included).
type indexEntry struct {
	archive uint32
	offset  uint64
	size    uint32
}

// Index maps the first 9 bytes of an encoding key to its span.
type Index struct {
	entries map[[9]byte]indexEntry
}

const (
	// idxHeaderEnd is the offset of the first entry: a 16-byte file header in a guarded block at 0, 8 bytes of
	// padding, then the entry array's guarded block header at 0x20.
	idxHeaderEnd = 0x28
	// idxEntryLen is 9 key bytes, a 5-byte big-endian storage offset and a 4-byte little-endian encoded size.
	idxEntryLen = 18
	// idxSegmentBits splits the storage offset into the archive number (the high bits) and the byte offset within
	// that archive (the low 30 bits).
	idxSegmentBits = 30
)

// lookup returns the span of the encoding key whose first 9 bytes are ekey's.
func (x *Index) lookup(ekey []byte) (indexEntry, bool) {
	if len(ekey) < 9 {
		return indexEntry{}, false
	}
	var key [9]byte
	copy(key[:], ekey)
	e, ok := x.entries[key]
	return e, ok
}

// LoadIndex reads the index files of a Data/data directory. Each bucket 0x00-0x0F can have several files; only the
// file with the highest version in its name counts, and all buckets are merged into one map.
func LoadIndex(dir string) (*Index, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		version uint32
		path    string
	}
	newest := make(map[byte]candidate, 16)
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if !strings.HasSuffix(name, ".idx") {
			continue
		}
		base := strings.TrimSuffix(name, ".idx")
		if len(base) != 10 {
			continue
		}
		h, err := hex.DecodeString(base)
		if err != nil {
			continue
		}
		bucket := h[0]
		version := binary.BigEndian.Uint32(h[1:])
		if cur, ok := newest[bucket]; !ok || version > cur.version {
			newest[bucket] = candidate{version: version, path: filepath.Join(dir, name)}
		}
	}
	if len(newest) == 0 {
		return nil, fmt.Errorf("casc: no .idx files in %s", dir)
	}
	x := &Index{entries: map[[9]byte]indexEntry{}}
	for b := range 16 {
		c, ok := newest[byte(b)]
		if !ok {
			continue
		}
		if err := x.loadFile(c.path, byte(b)); err != nil {
			return nil, err
		}
	}
	return x, nil
}

// loadFile reads one version 7 index file of the given bucket into the map.
func (x *Index) loadFile(path string, bucket byte) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	le := binary.LittleEndian
	if len(data) < idxHeaderEnd {
		return fmt.Errorf("casc: index %s is %d bytes, shorter than its %d byte header", name, len(data), idxHeaderEnd)
	}
	// The file header sits in a guarded block: 4 bytes of block size and 4 bytes of hash before its 16 bytes. The
	// hash is not verified here; the header fields below are what the reader relies on.
	if size := le.Uint32(data[0:]); size != 16 {
		return fmt.Errorf("casc: index %s header block is %d bytes, not 16", name, size)
	}
	if rev := le.Uint16(data[8:]); rev != 7 {
		return fmt.Errorf("casc: index %s has revision %d, not the version 7 layout", name, rev)
	}
	if data[10] != bucket {
		return fmt.Errorf("casc: index %s is in bucket %d but its header says %d", name, bucket, data[10])
	}
	if flags := data[11]; flags != 0 {
		return fmt.Errorf("casc: index %s has flags %#x", name, flags)
	}
	if sizeBytes, offBytes, keyBytes, segmentBits := data[12], data[13], data[14], data[15]; sizeBytes != 4 || offBytes != 5 || keyBytes != 9 || segmentBits != idxSegmentBits {
		return fmt.Errorf("casc: index %s has size %d, offset %d, key %d and segment %d bytes; the version 7 layout has 4, 5, 9 and 30",
			name, sizeBytes, offBytes, keyBytes, segmentBits)
	}
	blockSize := int(le.Uint32(data[0x20:]))
	if blockSize%idxEntryLen != 0 {
		return fmt.Errorf("casc: index %s has a %d byte entry block, not a multiple of %d", name, blockSize, idxEntryLen)
	}
	if blockSize > len(data)-idxHeaderEnd {
		return fmt.Errorf("casc: index %s entry block of %d bytes leaves its %d byte file", name, blockSize, len(data))
	}
	for p := idxHeaderEnd; p < idxHeaderEnd+blockSize; p += idxEntryLen {
		var key [9]byte
		copy(key[:], data[p:])
		if key == ([9]byte{}) {
			continue // padding and the free-space stubs carry a zero key
		}
		off := uint64(data[p+9])<<32 | uint64(data[p+10])<<24 | uint64(data[p+11])<<16 | uint64(data[p+12])<<8 | uint64(data[p+13])
		x.entries[key] = indexEntry{
			archive: uint32(off >> idxSegmentBits),
			offset:  off & (1<<idxSegmentBits - 1),
			size:    le.Uint32(data[p+14:]),
		}
	}
	return nil
}
