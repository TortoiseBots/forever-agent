package casc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	// noNameHash is the root block content flag that says the block carries no name hash array after its content keys.
	noNameHash = 0x10000000
	// contentLowViolence is the content flag of an alternate (censored) asset: a plain entry of the same FileDataID wins.
	contentLowViolence = 0x80
	// localeEnUS is the locale flag of enUS content; the only locale this reader keeps beside the locale-neutral blocks.
	localeEnUS = 0x2

	// rootV0Header is the header of the 8.2 root: the magic, the total and named record counts, then the blocks.
	rootV0Header = 12
	// rootV1Header is the 10.1.7 header: the magic, the header size, the version, the two counts, then the blocks.
	rootV1Header = 20
)

// rootEntry is what a root record says about one FileDataID: its content key and whether the entry is an alternate
// (low violence) asset.
type rootEntry struct {
	ckey        []byte
	lowViolence bool
}

// Root is the parsed root file: FileDataID to the content key of the file it names.
type Root struct {
	files map[uint32]rootEntry
}

// rootHeader is one reading of the root file's header: where the first block starts, the counts the header claims and
// the block header layout its version implies.
type rootHeader struct {
	off      int
	total    uint32
	named    uint32
	blockVer uint32
}

// ParseRoot parses a "TSFM" root file. The header is the 10.1.7 form (a header size and a version) or the 8.2 form (the
// two counts right after the magic); each reading is tried and the first whose blocks fill the file and match the
// header's record counts wins. Blocks whose locale is neither neutral nor enUS are skipped, and of two entries with one
// FileDataID the plain one wins over a low violence one.
func ParseRoot(data []byte) (*Root, error) {
	if len(data) < 4 || string(data[:4]) != "TSFM" {
		return nil, errors.New("casc: not a TSFM root file")
	}
	var errs []error
	headers := rootHeaders(data)
	if len(headers) == 0 {
		return nil, fmt.Errorf("casc: root file is %d bytes, shorter than its %d byte header", len(data), rootV0Header)
	}
	for _, h := range headers {
		root, err := parseRootBlocks(data, h)
		if err == nil {
			return root, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("casc: root file: %w", errors.Join(errs...))
}

// rootHeaders lists the header readings the file's length allows, the 10.1.7 form first.
func rootHeaders(data []byte) []rootHeader {
	if len(data) < rootV0Header {
		return nil
	}
	var out []rootHeader
	le := binary.LittleEndian
	if len(data) >= rootV1Header {
		size, version := int(le.Uint32(data[4:])), le.Uint32(data[8:])
		if (version == 1 || version == 2) && size >= rootV1Header && size <= len(data) {
			out = append(out, rootHeader{off: size, total: le.Uint32(data[12:]), named: le.Uint32(data[16:]), blockVer: version})
		}
	}
	out = append(out, rootHeader{off: rootV0Header, total: le.Uint32(data[4:]), named: le.Uint32(data[8:])})
	return out
}

// parseRootBlocks reads every block the header's offset starts, then checks the header's counts against what it read.
func parseRootBlocks(data []byte, h rootHeader) (*Root, error) {
	le := binary.LittleEndian
	root := &Root{files: map[uint32]rootEntry{}}
	var total, named uint64
	pos := h.off
	for pos < len(data) {
		headLen := 12
		if h.blockVer == 2 {
			headLen = 17
		}
		if len(data)-pos < headLen {
			return nil, fmt.Errorf("root block header at %#x of %d bytes leaves the %d byte file", pos, headLen, len(data))
		}
		n := int64(le.Uint32(data[pos:]))
		var content, locale uint32
		if h.blockVer == 2 {
			locale = le.Uint32(data[pos+4:])
			content = le.Uint32(data[pos+8:]) | le.Uint32(data[pos+12:]) | uint32(data[pos+16])<<17
		} else {
			content = le.Uint32(data[pos+4:])
			locale = le.Uint32(data[pos+8:])
		}
		pos += headLen
		perRecord := int64(4 + 16)
		if content&noNameHash == 0 {
			perRecord += 8
		}
		if n > int64(len(data)-pos)/perRecord {
			return nil, fmt.Errorf("root block at %#x declares %d records, more than its %d remaining bytes hold", pos-headLen, n, len(data)-pos)
		}
		deltas := data[pos : pos+int(n)*4]
		ckeys := data[pos+int(n)*4:][:int(n)*16]
		pos += int(n) * int(perRecord)
		total += uint64(n)
		if content&noNameHash == 0 {
			named += uint64(n)
		}
		if locale != 0 && locale&localeEnUS == 0 {
			continue
		}
		lowViolence := content&contentLowViolence != 0
		id := int64(0)
		for i := int64(0); i < n; i++ {
			delta := int64(int32(le.Uint32(deltas[i*4:])))
			if i == 0 {
				id = delta
			} else {
				id += 1 + delta
			}
			if id < 0 || id > math.MaxInt32 {
				return nil, fmt.Errorf("root block at %#x record %d has FileDataID %d", pos-headLen, i, id)
			}
			cur, ok := root.files[uint32(id)]
			if ok && !(cur.lowViolence && !lowViolence) {
				continue
			}
			ckey := make([]byte, 16)
			copy(ckey, ckeys[i*16:])
			root.files[uint32(id)] = rootEntry{ckey: ckey, lowViolence: lowViolence}
		}
	}
	if total != uint64(h.total) || named != uint64(h.named) {
		return nil, fmt.Errorf("root header at %d says %d records with %d named, its blocks hold %d with %d named",
			h.off, h.total, h.named, total, named)
	}
	return root, nil
}
