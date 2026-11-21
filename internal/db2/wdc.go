// Package db2 reads the client's DB2 database files read-only: the WDC4 and WDC5 headers, the
// section tables, the column storage (field structures, column meta, pallet and common data), the
// records with their string block, the ID list and the copy table. An encrypted section cannot be
// read without its key; it is skipped and noted.
//
// The 1.60 client wrote WDC5 files (schema WOWSTATIC_1_60_1_*). The string offsets of WDC2 and later are
// relative to the string field that holds them, and the client reads them against one blob of every
// section's records followed by every section's strings (DB2, wowdev; DB2FileLoader.cpp:798-803 at the
// server). This reader builds the same blob, so multiple sections resolve exactly as the client does.
package db2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Header flags (DB2 "Known Flag Meanings for WDB4+", wowdev).
const (
	flagOffsetMap = 0x1 // records are variable length and located by an offset map
	flagNonInline = 0x4 // the record IDs are in the section ID list, not in the records
	dummyTactID   = 0x5452494e49545900
	columnMetaLen = 24

	sectionHeaderLen = 40
)

// The caps on what a file may declare. They are far above any real DB2 (Map.db2 is 11 KiB, a record 44
// bytes) and far below what would hurt: every size is also checked against the file's own length.
var maxFileSize int64 = 256 << 20

const (
	maxRecordSize = 1 << 16
	maxFields     = 1 << 12
	maxSections   = 1 << 8
	maxArraySize  = 1 << 10
	maxBitSize    = 256
)

// Column compression types (DB2FileLoader.cpp:32-40).
const (
	compNone uint32 = iota
	compImmediate
	compCommonData
	compPallet
	compPalletArray
	compSignedImmediate
)

// Map is one row of Map.db2: the map's id, the folder of its world data and minimap
// (World\Maps\<Directory>, world/minimaps/<Directory>), its name and its instance type.
type Map struct {
	ID           uint32
	Directory    string
	MapName      string
	InstanceType uint32
}

// File is an open DB2 file. Open reads and parses the whole file; the data is kept in memory, so the
// file itself is never written to.
type File struct {
	Version         uint32 // 4 or 5
	Schema          string // WDC5's build string, "" for WDC4
	Flags           uint16
	RecordCount     int // every section's records, encrypted ones included
	FieldCount      int // the fields stored in the records
	RecordSize      int // bytes of one record
	TotalFieldCount int // every column, the ones only in common data included
	LayoutHash      uint32
	TableHash       uint32
	// Notes says what the reader could not use: an encrypted section is skipped here.
	Notes []string

	idIndex          int
	packedDataOffset uint32 // the byte in a record where the bit-packed values start
	fields           []fieldStorage
	sections         []section
	records          []byte   // every section's records in order; an encrypted section's bytes stay zero
	strings          []byte   // every section's string table in order; an encrypted section's stays zero
	ids              []uint32 // the ID list, one per record
	copies           []copyRow
	encrypted        []bool          // one per section
	encryptedIDs     map[uint32]bool // the ids the skipped sections list
}

// section is one WDC4/WDC5 section header: a block of records, its string table, ID list and copy table.
type section struct {
	tactID           uint64
	fileOffset       uint32
	recordCount      uint32
	stringTableSize  uint32
	offsetRecordsEnd uint32
	idListSize       uint32
	relationshipData uint32
	offsetMapIDCount uint32
	copyTableCount   uint32
	firstRecord      int // the section's first record in the file's record order
	firstString      int // the section's first byte in the string blob
}

// copyRow is one copy table entry: the new row repeats the source row's values under another id.
type copyRow struct {
	newID    uint32
	sourceID uint32
}

// fieldStorage is one column's storage (field_storage_info): where its bits are in the record and how
// they are compressed.
type fieldStorage struct {
	bitOffset   uint32 // the field's place in the record, in bits
	bitSize     uint32
	additional  uint32
	compression uint32
	packedOff   uint32 // Immediate, SignedImmediate and Pallet: the packed value's bit offset in the record
	packedWidth uint32 // Immediate and Pallet: the number of bits of the packed value
	arraySize   uint32 // PalletArray: the values of one index
	defaultVal  uint32 // CommonData: the value when the record's id is absent
	signed      bool

	pallets []uint32 // Pallet and PalletArray: this field's part of the pallet data
	common  map[uint32]uint32
}

// Open reads a WDC4 or WDC5 file. Every number in the file is hostile input: each size is checked against
// the file's length and the caps above before anything is allocated or read.
func Open(path string) (*File, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxFileSize {
		return nil, fmt.Errorf("%s: %d bytes is more than the %d byte cap on a DB2 file", filepath.Base(path), st.Size(), maxFileSize)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return f, nil
}

// parse reads one file's bytes. It never panics and never allocates more than the file's own length
// (times a small constant): see the checks before each make.
func parse(b []byte) (*File, error) {
	if len(b) < 8 {
		return nil, errors.New("not a DB2 file: shorter than a header")
	}
	size := uint64(len(b))
	f := &File{}
	c := cursor{b: b}
	rem := func() uint64 { return size - uint64(c.off) } // c.off never passes len(b)
	var magic string
	c.str(4, &magic)
	switch magic {
	case "WDC5":
		f.Version = 5
		var version uint32
		c.u32(&version)
		if version != 5 {
			return nil, fmt.Errorf("WDC5 file with version %d", version)
		}
		var schema [128]byte
		c.bytes(schema[:])
		f.Schema = string(schema[:])
		if i := strings.IndexByte(f.Schema, 0); i >= 0 {
			f.Schema = f.Schema[:i]
		}
	case "WDC4":
		f.Version = 4
	default:
		return nil, fmt.Errorf("not a WDC4 or WDC5 file (magic %q)", magic)
	}
	var recordCount, fieldCount, recordSize, totalFields uint32
	c.u32(&recordCount)
	c.u32(&fieldCount)
	c.u32(&recordSize)
	c.skip(4) // the header's string table size is the sections' sum; the sections are read below
	c.u32(&f.TableHash)
	c.u32(&f.LayoutHash)
	c.skip(12) // min id, max id, locale
	c.u16(&f.Flags)
	c.i16(&f.idIndex)
	c.u32(&totalFields)
	c.u32(&f.packedDataOffset)
	var parentLookups, columnMetaSize, commonDataSize, palletDataSize, sectionCount uint32
	c.u32(&parentLookups)
	c.u32(&columnMetaSize)
	c.u32(&commonDataSize)
	c.u32(&palletDataSize)
	c.u32(&sectionCount)
	if err := c.err(); err != nil {
		return nil, err
	}
	if recordSize < 4 || recordSize > maxRecordSize {
		return nil, fmt.Errorf("a record size of %d bytes (4 to %d allowed)", recordSize, maxRecordSize)
	}
	if totalFields < 1 || totalFields > maxFields || fieldCount < 1 || fieldCount > totalFields {
		return nil, fmt.Errorf("%d fields stored of %d (1 to %d allowed)", fieldCount, totalFields, maxFields)
	}
	if uint64(recordCount)*uint64(recordSize) > size {
		return nil, fmt.Errorf("%d records of %d bytes do not fit a %d byte file", recordCount, recordSize, size)
	}
	if sectionCount > maxSections || uint64(sectionCount)*sectionHeaderLen > rem() {
		return nil, fmt.Errorf("%d sections in a %d byte file", sectionCount, size)
	}
	if uint64(palletDataSize)+uint64(commonDataSize) > size {
		return nil, fmt.Errorf("%d bytes of pallet and %d of common data in a %d byte file", palletDataSize, commonDataSize, size)
	}
	f.RecordCount, f.FieldCount, f.RecordSize, f.TotalFieldCount = int(recordCount), int(fieldCount), int(recordSize), int(totalFields)
	if f.Flags&flagOffsetMap != 0 {
		return nil, errors.New("the records are an offset map (flag 0x1); this reader takes fixed-size records")
	}
	if parentLookups > 1 {
		return nil, fmt.Errorf("%d parent lookups; at most one is allowed", parentLookups)
	}
	if f.packedDataOffset != 0 && f.packedDataOffset >= recordSize {
		return nil, fmt.Errorf("the packed data starts at byte %d of a %d byte record", f.packedDataOffset, f.RecordSize)
	}
	recordBits := uint64(recordSize) * 8

	// The section headers, then the field structures, then the column storage.
	f.sections = make([]section, sectionCount)
	var first uint64
	for i := range f.sections {
		s := &f.sections[i]
		c.u64(&s.tactID)
		c.u32(&s.fileOffset)
		c.u32(&s.recordCount)
		c.u32(&s.stringTableSize)
		c.u32(&s.offsetRecordsEnd)
		c.u32(&s.idListSize)
		c.u32(&s.relationshipData)
		c.u32(&s.offsetMapIDCount)
		c.u32(&s.copyTableCount)
		s.firstRecord = int(min(first, size)) // a sum past the file is refused below
		first += uint64(s.recordCount)
	}
	if err := c.err(); err != nil {
		return nil, err
	}
	if first != uint64(recordCount) {
		return nil, fmt.Errorf("the sections hold %d records, the header says %d", first, recordCount)
	}
	if uint64(totalFields)*4 > rem() {
		return nil, fmt.Errorf("%d field structures do not fit the file", totalFields)
	}
	positions := make([]uint32, totalFields)
	sizes := make([]int32, totalFields)
	for i := range positions {
		var bits, pos uint16
		c.u16(&bits)
		c.u16(&pos)
		sizes[i] = 32 - int32(int16(bits))
		positions[i] = uint32(pos) * 8 // field positions are bytes; the column meta counts bits
	}
	if err := c.err(); err != nil {
		return nil, err
	}

	f.fields = make([]fieldStorage, totalFields)
	if columnMetaSize > 0 {
		if uint64(columnMetaSize) != uint64(totalFields)*columnMetaLen {
			return nil, fmt.Errorf("%d bytes of column meta for %d fields; %d expected",
				columnMetaSize, totalFields, uint64(totalFields)*columnMetaLen)
		}
		if uint64(columnMetaSize) > rem() {
			return nil, fmt.Errorf("%d bytes of column meta do not fit the file", columnMetaSize)
		}
		for i := range f.fields {
			fs := &f.fields[i]
			var bitOffset, bitSize uint16
			c.u16(&bitOffset)
			c.u16(&bitSize)
			c.u32(&fs.additional)
			c.u32(&fs.compression)
			fs.bitOffset, fs.bitSize = uint32(bitOffset), uint32(bitSize)
			switch fs.compression {
			case compNone:
				c.skip(12)
			case compImmediate, compSignedImmediate:
				c.u32(&fs.packedOff)
				c.u32(&fs.packedWidth)
				var signed uint32
				c.u32(&signed)
				fs.signed = signed != 0 && fs.compression == compSignedImmediate
			case compCommonData:
				c.u32(&fs.defaultVal)
				c.skip(8)
			case compPallet:
				c.u32(&fs.packedOff)
				c.u32(&fs.packedWidth)
				c.skip(4)
			case compPalletArray:
				c.u32(&fs.packedOff)
				c.u32(&fs.packedWidth)
				c.u32(&fs.arraySize)
			default:
				return nil, fmt.Errorf("field %d: compression type %d", i, fs.compression)
			}
			if err := c.err(); err != nil {
				return nil, err
			}
			if fs.bitSize == 0 || fs.bitSize > maxBitSize {
				return nil, fmt.Errorf("field %d: %d bits", i, fs.bitSize)
			}
			switch fs.compression {
			case compNone:
				if uint64(fs.bitOffset)+uint64(fs.bitSize) > recordBits {
					return nil, fmt.Errorf("field %d: bits %d to %d are past the %d byte record", i, fs.bitOffset, uint64(fs.bitOffset)+uint64(fs.bitSize), f.RecordSize)
				}
			case compCommonData:
			default:
				// The value is bit-packed in the record's packed area, not at the column's bit offset.
				packedOff := uint64(fs.packedOff) + uint64(f.packedDataOffset)*8
				if fs.packedWidth == 0 || fs.packedWidth > 64 || packedOff+uint64(fs.packedWidth) > recordBits {
					return nil, fmt.Errorf("field %d: %d bits at bit %d of the packed area do not fit a %d byte record", i, fs.packedWidth, packedOff, f.RecordSize)
				}
				fs.packedOff = uint32(packedOff)
				if fs.compression == compPalletArray && (fs.arraySize == 0 || fs.arraySize > maxArraySize || fs.additional/4%fs.arraySize != 0) {
					return nil, fmt.Errorf("field %d: pallet array of %d values with %d bytes of pallet data", i, fs.arraySize, fs.additional)
				}
			}
		}
	} else {
		// A file without column meta stores every field plainly.
		for i := range f.fields {
			if sizes[i] < 1 || sizes[i] > maxBitSize || uint64(positions[i])+uint64(sizes[i]) > recordBits {
				return nil, fmt.Errorf("field %d: %d bits at bit %d do not fit a %d byte record", i, sizes[i], positions[i], f.RecordSize)
			}
			f.fields[i] = fieldStorage{bitOffset: positions[i], bitSize: uint32(sizes[i])}
		}
	}
	if err := c.err(); err != nil {
		return nil, err
	}

	// Pallet data: one block per pallet field, in field order. Then the same for pallet arrays, then the
	// common data. The server reads them in this order (DB2FileLoader.cpp:1808-1851). Every block is checked
	// against the header's totals and the bytes left before it is allocated, and the blocks must add up to
	// the header's sizes exactly: a column meta that disagrees with the header is a misread file.
	var palletUsed, commonUsed uint64
	readPallet := func(comp uint32) error {
		for i := range f.fields {
			fs := &f.fields[i]
			if fs.compression != comp {
				continue
			}
			if fs.additional%4 != 0 {
				return fmt.Errorf("field %d: %d bytes of pallet data", i, fs.additional)
			}
			palletUsed += uint64(fs.additional)
			if palletUsed > uint64(palletDataSize) || uint64(fs.additional) > rem() {
				return fmt.Errorf("field %d: %d bytes of pallet data; the header has %d in all", i, fs.additional, palletDataSize)
			}
			fs.pallets = make([]uint32, fs.additional/4)
			for k := range fs.pallets {
				c.u32(&fs.pallets[k])
			}
		}
		return c.err()
	}
	if err := readPallet(compPallet); err != nil {
		return nil, err
	}
	if err := readPallet(compPalletArray); err != nil {
		return nil, err
	}
	if palletUsed != uint64(palletDataSize) {
		return nil, fmt.Errorf("the columns use %d bytes of pallet data; the header says %d", palletUsed, palletDataSize)
	}
	for i := range f.fields {
		fs := &f.fields[i]
		if fs.compression != compCommonData || fs.additional == 0 {
			continue
		}
		if fs.additional%8 != 0 {
			return nil, fmt.Errorf("field %d: %d bytes of common data", i, fs.additional)
		}
		commonUsed += uint64(fs.additional)
		if commonUsed > uint64(commonDataSize) || uint64(fs.additional) > rem() {
			return nil, fmt.Errorf("field %d: %d bytes of common data; the header has %d in all", i, fs.additional, commonDataSize)
		}
		fs.common = make(map[uint32]uint32, fs.additional/8)
		for range fs.additional / 8 {
			var id, value uint32
			c.u32(&id)
			c.u32(&value)
			fs.common[id] = value
		}
	}
	if err := c.err(); err != nil {
		return nil, err
	}
	if commonUsed != uint64(commonDataSize) {
		return nil, fmt.Errorf("the columns use %d bytes of common data; the header says %d", commonUsed, commonDataSize)
	}

	// Each encrypted section lists the ids it encrypts, after all header blocks (DB2FileLoader.cpp:1900-1916).
	f.encrypted = make([]bool, len(f.sections))
	for i := range f.sections {
		s := &f.sections[i]
		if s.tactID == 0 {
			continue
		}
		// The client writes the list for every section with a key, the re-saved file's known one included.
		var count uint32
		c.u32(&count)
		if count > s.recordCount || uint64(count)*4 > rem() {
			return nil, fmt.Errorf("section %d: %d encrypted ids for %d records", i, count, s.recordCount)
		}
		if s.tactID == dummyTactID {
			c.skip(int(count) * 4)
			continue
		}
		f.encrypted[i] = true
		if f.encryptedIDs == nil {
			f.encryptedIDs = map[uint32]bool{}
		}
		for range count {
			var id uint32
			c.u32(&id)
			f.encryptedIDs[id] = true
		}
		f.Notes = append(f.Notes, fmt.Sprintf("section %d is encrypted; its %d records are skipped", i, s.recordCount))
	}
	if err := c.err(); err != nil {
		return nil, err
	}

	// Every section lies inside the file, after the header blocks, and the sections together hold no more
	// bytes than the file: only then are the record blob and the string blob allocated.
	headerEnd := uint64(c.off)
	var total, stringBytes uint64
	for i := range f.sections {
		s := &f.sections[i]
		if uint64(s.fileOffset) < headerEnd {
			return nil, fmt.Errorf("section %d starts at %d, inside the %d byte header", i, s.fileOffset, headerEnd)
		}
		bytesIn := uint64(s.recordCount)*uint64(f.RecordSize) + uint64(s.stringTableSize) + uint64(s.idListSize) + uint64(s.copyTableCount)*8
		if uint64(s.fileOffset)+bytesIn > size {
			return nil, fmt.Errorf("section %d ends at %d, past the %d byte file", i, uint64(s.fileOffset)+bytesIn, size)
		}
		total += bytesIn
		s.firstString = int(min(stringBytes, size))
		stringBytes += uint64(s.stringTableSize)
	}
	if total > size-headerEnd {
		return nil, fmt.Errorf("the sections hold %d bytes; the file has %d after its header", total, size-headerEnd)
	}
	f.records = make([]byte, f.RecordCount*f.RecordSize)
	f.strings = make([]byte, stringBytes)
	f.ids = make([]uint32, f.RecordCount)
	for i := range f.sections {
		s := &f.sections[i]
		if f.encrypted[i] {
			continue // without the key the records and strings stay zero; the ids are zero too
		}
		// The ID list is the record ids when the file sets flag 0x4; then every readable section needs one.
		hasIDs := int(s.idListSize) == 4*int(s.recordCount)
		if f.Flags&flagNonInline != 0 && !hasIDs {
			return nil, fmt.Errorf("section %d: %d bytes of ids for %d records; the file keeps its ids in the ID list (flag 0x4)", i, s.idListSize, s.recordCount)
		}
		if s.idListSize != 0 && !hasIDs {
			return nil, fmt.Errorf("section %d: %d bytes of ids for %d records", i, s.idListSize, s.recordCount)
		}
		off := int(s.fileOffset)
		copy(f.records[s.firstRecord*f.RecordSize:], b[off:off+int(s.recordCount)*f.RecordSize])
		off += int(s.recordCount) * f.RecordSize
		copy(f.strings[s.firstString:], b[off:off+int(s.stringTableSize)])
		off += int(s.stringTableSize)
		if hasIDs {
			for k := range int(s.recordCount) {
				f.ids[s.firstRecord+k] = binary.LittleEndian.Uint32(b[off+k*4:])
			}
		}
		off += int(s.idListSize)
		for k := range int(s.copyTableCount) {
			f.copies = append(f.copies, copyRow{
				newID:    binary.LittleEndian.Uint32(b[off+k*8:]),
				sourceID: binary.LittleEndian.Uint32(b[off+k*8+4:]),
			})
		}
	}
	return f, nil
}

func (f *File) encryptedRecord(rec int) bool {
	for i := range f.sections {
		s := &f.sections[i]
		if f.encrypted[i] && rec >= s.firstRecord && rec < s.firstRecord+int(s.recordCount) {
			return true
		}
	}
	return false
}

// SkippedID reports whether id is one of the ids the file lists for a section it could not read (an
// encrypted section without its key): that row exists in the client's table but not in this reader's.
func (f *File) SkippedID(id uint32) bool { return f.encryptedIDs[id] }

// record is one record's bytes.
func (f *File) record(rec int) []byte {
	return f.records[rec*f.RecordSize : (rec+1)*f.RecordSize]
}

// recordID is the row's id: from the ID list, or from the record's id field when the file keeps it inline.
func (f *File) recordID(rec int) (uint32, error) {
	if f.Flags&flagNonInline != 0 {
		return f.ids[rec], nil
	}
	if f.idIndex < 0 || f.idIndex >= len(f.fields) {
		return 0, fmt.Errorf("the id column %d is not one of the %d fields", f.idIndex, len(f.fields))
	}
	if f.fields[f.idIndex].compression == compCommonData {
		return 0, errors.New("the id column is common data keyed by the id")
	}
	v, err := f.value(rec, f.idIndex)
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint32 {
		return 0, fmt.Errorf("record %d: the id %d does not fit 32 bits", rec, v)
	}
	return uint32(v), nil
}

// value is a scalar column's value, widened to 64 bits (a signed immediate is sign-extended): plain or
// bit-packed, from the pallet, or from the common data with the record's id as the key. A pallet index
// past the pallet, or an array column, is an error.
func (f *File) value(rec, field int) (uint64, error) {
	if rec < 0 || rec >= f.RecordCount || field < 0 || field >= len(f.fields) {
		return 0, fmt.Errorf("record %d, field %d: out of range", rec, field)
	}
	fs := &f.fields[field]
	r := f.record(rec)
	switch fs.compression {
	case compNone:
		return readBits(r, uint64(fs.bitOffset), min(fs.bitSize, 64)), nil
	case compImmediate, compSignedImmediate:
		v := readBits(r, uint64(fs.packedOff), fs.packedWidth)
		if fs.signed {
			v = signExtend(v, fs.packedWidth)
		}
		return v, nil
	case compPallet:
		i := readBits(r, uint64(fs.packedOff), fs.packedWidth)
		if i >= uint64(len(fs.pallets)) {
			return 0, fmt.Errorf("record %d, field %d: pallet index %d of %d", rec, field, i, len(fs.pallets))
		}
		return uint64(fs.pallets[i]), nil
	case compPalletArray:
		if fs.arraySize != 1 {
			return 0, fmt.Errorf("field %d is an array of %d values; read it with values", field, fs.arraySize)
		}
		vs, err := f.values(rec, field)
		if err != nil {
			return 0, err
		}
		return uint64(vs[0]), nil
	case compCommonData:
		id, err := f.recordID(rec)
		if err != nil {
			return 0, err
		}
		if v, ok := fs.common[id]; ok {
			return uint64(v), nil
		}
		return uint64(fs.defaultVal), nil
	}
	return 0, fmt.Errorf("field %d: compression %d", field, fs.compression)
}

// values is a pallet array column's every value for one record.
func (f *File) values(rec, field int) ([]uint32, error) {
	if rec < 0 || rec >= f.RecordCount || field < 0 || field >= len(f.fields) {
		return nil, fmt.Errorf("record %d, field %d: out of range", rec, field)
	}
	fs := &f.fields[field]
	if fs.compression != compPalletArray {
		return nil, fmt.Errorf("field %d is not a pallet array", field)
	}
	i := readBits(f.record(rec), uint64(fs.packedOff), fs.packedWidth)
	if n := uint64(len(fs.pallets)) / uint64(fs.arraySize); i >= n {
		return nil, fmt.Errorf("record %d, field %d: pallet array index %d of %d", rec, field, i, n)
	}
	start := i * uint64(fs.arraySize)
	return fs.pallets[start : start+uint64(fs.arraySize)], nil
}

// readBits reads width bits (1 to 64) at a bit offset of r, little-endian, shifted down. The caller keeps
// bit+width inside r; a value that crosses nine bytes (a wide value at a bit shift) reads the ninth.
func readBits(r []byte, bit uint64, width uint32) uint64 {
	at := int(bit / 8)
	shift := uint(bit & 7)
	var buf [8]byte
	copy(buf[:], r[at:min(at+8, len(r))])
	v := binary.LittleEndian.Uint64(buf[:]) >> shift
	if shift > 0 && at+8 < len(r) {
		v |= uint64(r[at+8]) << (64 - shift)
	}
	return maskBits(v, width)
}

// stringField is the string at a string column: the stored 32-bit offset is the distance from the field to
// the string in the record-plus-strings blob (WDC2 and later). A distance of 0 is the empty string; one that
// leaves the blob, or a string without its end, is an error.
func (f *File) stringField(rec, field int) (string, error) {
	if rec < 0 || rec >= f.RecordCount || field < 0 || field >= len(f.fields) {
		return "", fmt.Errorf("record %d, field %d: out of range", rec, field)
	}
	fs := &f.fields[field]
	if fs.compression != compNone || fs.bitSize < 32 || fs.bitOffset%8 != 0 {
		return "", fmt.Errorf("field %d is not a plain 32-bit string column", field)
	}
	off := rec*f.RecordSize + int(fs.bitOffset/8)
	delta := binary.LittleEndian.Uint32(f.records[off:])
	if delta == 0 {
		return "", nil
	}
	p := off + int(delta)
	if p < len(f.records) || p >= len(f.records)+len(f.strings) {
		return "", fmt.Errorf("record %d, field %d: the string offset %d leaves the %d byte string block", rec, field, delta, len(f.strings))
	}
	s := f.strings[p-len(f.records):]
	i := bytes.IndexByte(s, 0)
	if i < 0 {
		return "", fmt.Errorf("record %d, field %d: the string has no end", rec, field)
	}
	return string(s[:i]), nil
}

// mapFieldCount and mapLayoutHash are the Map.db2 layout of client build 70959 (MapMeta,
// DB2Metadata.h:14538): 26 fields, layout hash 0xD43AFAC3.
const (
	mapFieldCount = 26
	mapLayoutHash = 0xD43AFAC3
)

// Map field indices of the 70959 layout (MapMeta).
const (
	mapFieldDirectory    = 0
	mapFieldMapName      = 1
	mapFieldInstanceType = 8
)

// Maps reads Map.db2's rows, and says what it left out. The layout hash and the field count must be the
// 70959 layout's; anything else is a different client layout and is refused rather than misread. A row of an
// encrypted section is not read (File.Notes and SkippedID say so); a copy row whose source is not a row read
// here is skipped and counted in the returned notes. A duplicate id, a bad string offset or a negative
// instance type is an error.
func (f *File) Maps() ([]Map, []string, error) {
	if f.TotalFieldCount != mapFieldCount || f.LayoutHash != mapLayoutHash {
		return nil, nil, fmt.Errorf("Map.db2 has %d fields and layout hash %#08x; the 1.60 build 70959 layout has %d and %#08x",
			f.TotalFieldCount, f.LayoutHash, mapFieldCount, uint32(mapLayoutHash))
	}
	read := func(rec int) (Map, error) {
		var m Map
		var err error
		if m.ID, err = f.recordID(rec); err != nil {
			return m, err
		}
		if m.Directory, err = f.stringField(rec, mapFieldDirectory); err != nil {
			return m, err
		}
		if m.MapName, err = f.stringField(rec, mapFieldMapName); err != nil {
			return m, err
		}
		v, err := f.value(rec, mapFieldInstanceType)
		if err != nil {
			return m, err
		}
		if int64(v) < 0 || v > math.MaxUint32 {
			return m, fmt.Errorf("record %d: the instance type %d is not a 32-bit count", rec, int64(v))
		}
		m.InstanceType = uint32(v)
		return m, nil
	}
	rows := make([]Map, 0, f.RecordCount)
	byID := make(map[uint32]int, f.RecordCount)
	for rec := range f.RecordCount {
		if f.encryptedRecord(rec) {
			continue
		}
		m, err := read(rec)
		if err != nil {
			return nil, nil, err
		}
		if _, dup := byID[m.ID]; dup {
			return nil, nil, fmt.Errorf("map id %d is in the file twice", m.ID)
		}
		byID[m.ID] = len(rows)
		rows = append(rows, m)
	}
	// A copy table row repeats a source row's values under another id (the client's deduplication). The
	// source is the source row's id. A source that is not a row read here (its section is encrypted, or it is
	// no id of the file) is skipped and counted: the record index is no substitute for an id.
	var notes []string
	skipped := 0
	for _, c := range f.copies {
		i, ok := byID[c.sourceID]
		if !ok {
			skipped++
			continue
		}
		if _, dup := byID[c.newID]; dup {
			return nil, nil, fmt.Errorf("map id %d is in the file twice (copy of %d)", c.newID, c.sourceID)
		}
		m := rows[i]
		m.ID = c.newID
		byID[m.ID] = len(rows)
		rows = append(rows, m)
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d copy rows skipped: their source row is in a skipped section or not in the file", skipped))
	}
	return rows, notes, nil
}

// maskBits keeps the low n bits of v (n up to 64).
func maskBits(v uint64, n uint32) uint64 {
	if n >= 64 {
		return v
	}
	return v & (1<<n - 1)
}

// signExtend sign-extends the low n bits of v.
func signExtend(v uint64, n uint32) uint64 {
	if n == 0 || n >= 64 {
		return v
	}
	if v&(1<<(n-1)) != 0 {
		return v | ^(1<<n - 1)
	}
	return v
}

// cursor is a bounds-checked little-endian reader; the first error sticks.
type cursor struct {
	b   []byte
	off int
	e   error
}

func (c *cursor) fail(what string, want int) {
	if c.e == nil {
		c.e = fmt.Errorf("truncated DB2: %s at byte %d needs %d bytes, the file has %d", what, c.off, want, len(c.b))
	}
	c.off = len(c.b)
}

func (c *cursor) bytes(p []byte) {
	if c.e != nil {
		return
	}
	if c.off+len(p) > len(c.b) {
		c.fail("data", len(p))
		return
	}
	copy(p, c.b[c.off:])
	c.off += len(p)
}

func (c *cursor) str(n int, out *string) {
	if c.e != nil {
		return
	}
	if c.off+n > len(c.b) {
		c.fail("string", n)
		return
	}
	*out = string(c.b[c.off : c.off+n])
	c.off += n
}

func (c *cursor) u16(out *uint16) {
	if c.e != nil {
		return
	}
	if c.off+2 > len(c.b) {
		c.fail("u16", 2)
		return
	}
	*out = binary.LittleEndian.Uint16(c.b[c.off:])
	c.off += 2
}

func (c *cursor) i16(out *int) {
	var v uint16
	c.u16(&v)
	*out = int(int16(v))
}

func (c *cursor) u32(out *uint32) {
	if c.e != nil {
		return
	}
	if c.off+4 > len(c.b) {
		c.fail("u32", 4)
		return
	}
	*out = binary.LittleEndian.Uint32(c.b[c.off:])
	c.off += 4
}

func (c *cursor) u64(out *uint64) {
	if c.e != nil {
		return
	}
	if c.off+8 > len(c.b) {
		c.fail("u64", 8)
		return
	}
	*out = binary.LittleEndian.Uint64(c.b[c.off:])
	c.off += 8
}

func (c *cursor) skip(n int) {
	if c.e != nil {
		return
	}
	if c.off+n > len(c.b) {
		c.fail("padding", n)
		return
	}
	c.off += n
}

func (c *cursor) err() error { return c.e }
