package casc

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

// Encoding is the parsed encoding file: the table that maps a content key (CKey, the MD5 of a file's decoded bytes) to
// the encoding keys (EKey) its encoded forms are stored under. Only the CKey -> EKey table is read; the encoding
// specification strings and their table are bounds checked and skipped.
type Encoding struct {
	ckeySize int
	ekeySize int
	first    [][]byte // the first CKey of each page
	pages    [][]byte // the pages themselves
}

const (
	// encodingHeaderLen is the fixed part of the "EN" header: magic, version, key sizes, page sizes and counts, flags
	// and the ESpec block size.
	encodingHeaderLen = 0x16
	// maxEncodingPageKB caps a page size: the format's pages are 4 or 16 KiB, never over 256.
	maxEncodingPageKB = 256
)

// ParseEncoding parses the encoding file: the "EN" header, the page index with each page's first CKey, and the CKey
// pages of variable length entries. Every count and size is checked against the input before use, so a truncated or
// hostile file is an error, never a panic.
func ParseEncoding(data []byte) (*Encoding, error) {
	if len(data) < encodingHeaderLen {
		return nil, fmt.Errorf("casc: encoding file is %d bytes, shorter than its %#x byte header", len(data), encodingHeaderLen)
	}
	if string(data[:2]) != "EN" {
		return nil, errors.New("casc: not an encoding file")
	}
	if v := data[2]; v != 1 {
		return nil, fmt.Errorf("casc: encoding file has version %d, not the version 1 layout", v)
	}
	ckeySize, ekeySize := int(data[3]), int(data[4])
	if ckeySize != 16 || ekeySize != 16 {
		return nil, fmt.Errorf("casc: encoding file has %d byte content and %d byte encoding keys, not 16 and 16", ckeySize, ekeySize)
	}
	pageKB := int64(be16(data[5:]))
	especPageKB := int64(be16(data[7:]))
	pageCount := int64(be32(data[9:]))
	especPageCount := int64(be32(data[13:]))
	if data[17] != 0 {
		return nil, fmt.Errorf("casc: encoding file has flags %#x", data[17])
	}
	especSize := int64(be32(data[18:]))
	if pageKB < 1 || pageKB > maxEncodingPageKB {
		return nil, fmt.Errorf("casc: encoding file page size is %d KiB", pageKB)
	}
	if especPageCount > 0 && (especPageKB < 1 || especPageKB > maxEncodingPageKB) {
		return nil, fmt.Errorf("casc: encoding file ESpec page size is %d KiB", especPageKB)
	}

	// header, the ESpec strings, the CKey page index, the CKey pages, the EKey spec page index and its pages.
	off := int64(encodingHeaderLen)
	if especSize > int64(len(data))-off {
		return nil, fmt.Errorf("casc: encoding file ESpec block of %d bytes leaves its %d byte file", especSize, len(data))
	}
	off += especSize
	indexLen := pageCount * int64(ckeySize+16)
	if indexLen > int64(len(data))-off {
		return nil, fmt.Errorf("casc: encoding file page index of %d pages leaves its %d byte file", pageCount, len(data))
	}
	index := data[off : off+indexLen]
	off += indexLen
	pageLen := pageCount * pageKB * 1024
	if pageLen > int64(len(data))-off {
		return nil, fmt.Errorf("casc: encoding file pages of %d bytes leave its %d byte file", pageLen, len(data))
	}
	pages := data[off : off+pageLen]
	off += pageLen
	specIndexLen := especPageCount * int64(ekeySize+16)
	if specIndexLen > int64(len(data))-off {
		return nil, fmt.Errorf("casc: encoding file ESpec page index leaves its %d byte file", len(data))
	}
	off += specIndexLen
	specPagesLen := especPageCount * especPageKB * 1024
	if specPagesLen > int64(len(data))-off {
		return nil, fmt.Errorf("casc: encoding file ESpec pages leave its %d byte file", len(data))
	}

	enc := &Encoding{ckeySize: ckeySize, ekeySize: ekeySize, first: make([][]byte, pageCount), pages: make([][]byte, pageCount)}
	entryLen := int64(ckeySize + 16)
	for i := int64(0); i < pageCount; i++ {
		enc.first[i] = index[i*entryLen:][:ckeySize]
		enc.pages[i] = pages[i*pageKB*1024:][:pageKB*1024]
	}
	// Every page's entry chain is walked once here, so a malformed page is an error at parse time and a lookup can
	// only answer with an encoding key or ErrNotFound.
	for i := range enc.pages {
		if err := enc.eachEntry(i, func(_, _ []byte) bool { return false }); err != nil {
			return nil, err
		}
	}
	return enc, nil
}

// eachEntry walks the entries of page i in order and stops when fn returns true. An entry is a key count, the decoded
// size, the content key and that many encoding keys; a page is padded to its size with a zero key count.
func (e *Encoding) eachEntry(i int, fn func(ckey, ekey []byte) bool) error {
	page := e.pages[i]
	for pos := 0; pos < len(page); {
		if len(page)-pos < 6+e.ckeySize {
			break
		}
		count := int(page[pos])
		if count == 0 {
			break
		}
		size := 6 + e.ckeySize + count*e.ekeySize
		if size > len(page)-pos {
			return fmt.Errorf("casc: encoding page %d entry at %d of %d encoding keys runs past its %d bytes", i, pos, count, len(page))
		}
		if fn(page[pos+6:pos+6+e.ckeySize], page[pos+6+e.ckeySize:pos+size]) {
			break
		}
		pos += size
	}
	return nil
}

// EKeyFor returns the first encoding key of the content key. A content key the file does not name is ErrNotFound.
func (e *Encoding) EKeyFor(ckey []byte) ([]byte, error) {
	if len(ckey) != e.ckeySize {
		return nil, fmt.Errorf("casc: content key is %d bytes, the encoding file's keys are %d", len(ckey), e.ckeySize)
	}
	// The pages hold the keys in sorted order and the page index names each page's first key: the last page whose first
	// key is not above ckey is the only page that can hold it.
	i := sort.Search(len(e.first), func(i int) bool { return bytes.Compare(e.first[i], ckey) > 0 }) - 1
	if i < 0 {
		return nil, fmt.Errorf("casc: content key %x has no encoding key: %w", ckey, ErrNotFound)
	}
	var ekey []byte
	err := e.eachEntry(i, func(pageCKey, pageEKey []byte) bool {
		if bytes.Equal(pageCKey, ckey) {
			ekey = append([]byte(nil), pageEKey...)
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if ekey == nil {
		return nil, fmt.Errorf("casc: content key %x has no encoding key: %w", ckey, ErrNotFound)
	}
	return ekey, nil
}
