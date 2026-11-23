package casc

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
)

// ErrEncrypted is returned for a BLTE chunk in encrypted ("E") mode: the storage's key ring is not read.
var ErrEncrypted = errors.New("casc: BLTE chunk is encrypted")

// ErrTooLarge is returned when a BLTE stream declares a decoded size over the cap, or decodes past it.
var ErrTooLarge = errors.New("casc: BLTE stream is over its size cap")

const (
	// maxBLTEChunks caps the chunk table size an input may declare.
	maxBLTEChunks = 65536
	// maxBLTESize caps the total decoded bytes of one stream (256 MiB).
	maxBLTESize = 256 << 20
)

// DecodeBLTE decodes a BLTE stream into its content. Both container forms are read: HeaderSize 0 is one chunk that
// runs to the end of the stream, anything else is a chunk table of 24-byte entries. The chunk modes are N (stored
// raw) and Z (zlib); E returns ErrEncrypted and every other mode is an error. Sizes are checked against the input
// before any chunk data is allocated, so a corrupt or hostile stream is an error, never a panic or a huge buffer.
func DecodeBLTE(data []byte) ([]byte, error) {
	if len(data) < 8 || string(data[:4]) != "BLTE" {
		return nil, errors.New("casc: not a BLTE stream")
	}
	headerSize := int(be32(data[4:]))
	if headerSize == 0 {
		if len(data) == 8 {
			return nil, errors.New("casc: BLTE stream has no chunk")
		}
		out, err := decodeBLTEChunk(nil, data[8:], -1)
		if err != nil {
			return nil, err
		}
		if len(out) > maxBLTESize {
			return nil, fmt.Errorf("casc: BLTE stream decodes to %d bytes, over the %d byte cap: %w", len(out), maxBLTESize, ErrTooLarge)
		}
		return out, nil
	}
	if headerSize < 12 {
		return nil, fmt.Errorf("casc: BLTE header is %d bytes, shorter than its 12 byte minimum", headerSize)
	}
	if headerSize > len(data) {
		return nil, fmt.Errorf("casc: BLTE header of %d bytes leaves its %d byte stream", headerSize, len(data))
	}
	count := int(be24(data[9:12]))
	if count == 0 {
		return nil, errors.New("casc: BLTE chunk table is empty")
	}
	if count > maxBLTEChunks {
		return nil, fmt.Errorf("casc: BLTE chunk table declares %d chunks, over the %d cap", count, maxBLTEChunks)
	}
	if 12+24*count != headerSize {
		return nil, fmt.Errorf("casc: BLTE header of %d bytes does not hold its %d chunk table entries", headerSize, count)
	}
	type chunk struct{ encoded, content int }
	chunks := make([]chunk, count)
	total := 0
	for i := range chunks {
		p := 12 + 24*i
		encoded := int(be32(data[p:]))
		content := int(be32(data[p+4:]))
		if encoded < 1 {
			return nil, fmt.Errorf("casc: BLTE chunk %d has no mode byte", i)
		}
		total += content
		if total > maxBLTESize {
			return nil, fmt.Errorf("casc: BLTE chunks decode to %d bytes, over the %d byte cap: %w", total, maxBLTESize, ErrTooLarge)
		}
		chunks[i] = chunk{encoded: encoded, content: content}
	}
	pos := headerSize
	for i, c := range chunks {
		if c.encoded > len(data)-pos {
			return nil, fmt.Errorf("casc: BLTE chunk %d of %d bytes runs past the %d byte stream at %d", i, c.encoded, len(data), pos)
		}
		pos += c.encoded
	}
	out := make([]byte, 0, len(data))
	pos = headerSize
	for i, c := range chunks {
		var err error
		if out, err = decodeBLTEChunk(out, data[pos:pos+c.encoded], c.content); err != nil {
			return nil, fmt.Errorf("casc: BLTE chunk %d: %w", i, err)
		}
		pos += c.encoded
	}
	return out, nil
}

// decodeBLTEChunk appends one mode-prefixed chunk to out. content is the chunk's declared decoded size, or -1 when
// the container does not say.
func decodeBLTEChunk(out, chunk []byte, content int) ([]byte, error) {
	switch chunk[0] {
	case 'N':
		body := chunk[1:]
		if content >= 0 && len(body) != content {
			return nil, fmt.Errorf("raw chunk holds %d bytes, its table says %d", len(body), content)
		}
		if len(out)+len(body) > maxBLTESize {
			return nil, fmt.Errorf("decoded size passes the %d byte cap: %w", maxBLTESize, ErrTooLarge)
		}
		return append(out, body...), nil
	case 'Z':
		zr, err := zlib.NewReader(bytes.NewReader(chunk[1:]))
		if err != nil {
			return nil, err
		}
		if content < 0 {
			decoded, err := io.ReadAll(io.LimitReader(zr, int64(maxBLTESize-len(out))+1))
			zr.Close()
			if err != nil {
				return nil, err
			}
			if len(out)+len(decoded) > maxBLTESize {
				return nil, fmt.Errorf("decoded size passes the %d byte cap: %w", maxBLTESize, ErrTooLarge)
			}
			return append(out, decoded...), nil
		}
		if content > maxBLTESize-len(out) {
			return nil, fmt.Errorf("decoded size passes the %d byte cap: %w", maxBLTESize, ErrTooLarge)
		}
		decoded, err := io.ReadAll(io.LimitReader(zr, int64(content)+1))
		zr.Close()
		if err != nil {
			return nil, err
		}
		if len(decoded) != content {
			return nil, fmt.Errorf("zlib chunk decodes to %d bytes, its table says %d", len(decoded), content)
		}
		return append(out, decoded...), nil
	case 'E':
		return nil, ErrEncrypted
	default:
		return nil, fmt.Errorf("chunk mode %q is not supported", chunk[0])
	}
}

// be32 reads a big-endian 32-bit value.
func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// be24 reads a big-endian 24-bit value.
func be24(b []byte) uint32 { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }

// be16 reads a big-endian 16-bit value.
func be16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
