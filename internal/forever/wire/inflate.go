package wire

import (
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// The server compresses a packet whose payload (without its 4-byte opcode) is larger than 0x400 bytes into
// SMSG_COMPRESSED_PACKET (WorldSocket.cpp:50-59, 573-616). Its body is three uint32s and then the raw-deflate
// bytes: the inner opcode followed by the payload. ONE deflate stream is created per connection (windowBits -15,
// memLevel 8, Z_SYNC_FLUSH per packet) and is never reset, so packet n's bytes continue packet n-1's stream.
const (
	// CompressionMinSize is the server's WorldSocket::MinSizeForCompression.
	CompressionMinSize = 0x400
	// compressionSeed is the adler32 seed the server uses for both checksums.
	compressionSeed = 0x9827D8F1
	// MaxUncompressed caps the size a compressed packet may claim. The server's largest packet in this round
	// is about 200 KB (the hotfix list); the bound keeps a hostile header from allocating.
	MaxUncompressed = 16 << 20
)

// Inflater is one inflate stream per connection. It must be reused for every compressed packet of that
// connection and never reset: the server's deflate stream carries state across packets.
//
// The decoder pulls bytes synchronously from the chunk of the packet being decompressed (chunkSource). It never
// waits for another goroutine: when the chunk runs out the source returns io.ErrUnexpectedEOF, so a packet that
// claims more bytes than its deflate data produces fails at once instead of blocking a read that only the same
// goroutine could satisfy. The deflate state (the 32 KiB window) stays in the flate reader across packets; after a
// packet that cannot produce its declared size the stream position is unknown, so the inflater refuses every
// later packet (the connection is poisoned, as the notes say).
type Inflater struct {
	src *chunkSource
	r   io.Reader

	// pending holds the bytes of the previous chunk the decoder did not consume (at most the tail of the
	// server's Z_SYNC_FLUSH marker); they are prepended to the next chunk because the stream is continuous.
	pending []byte

	closed sync.Once
	dead   atomic.Bool
}

// chunkSource is the inflater's input: the current packet's compressed bytes, and nothing else. Read and ReadByte
// are called by the flate reader on the goroutine that called DecompressPacket, so no lock is needed; Close only
// drops the reference (flate's own Close would race with that reader), which is safe because the source never
// blocks.
type chunkSource struct {
	cur []byte
}

func (s *chunkSource) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(s.cur) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

// ReadByte makes chunkSource a flate.Reader, so compress/flate reads the chunk directly instead of wrapping it in
// a bufio.Reader; that keeps the exact number of unconsumed bytes visible to DecompressPacket.
func (s *chunkSource) ReadByte() (byte, error) {
	if len(s.cur) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	c := s.cur[0]
	s.cur = s.cur[1:]
	return c, nil
}

// NewInflater starts a raw-deflate reader for one connection.
func NewInflater() *Inflater {
	src := &chunkSource{}
	return &Inflater{src: src, r: flate.NewReader(src)}
}

// Close releases the inflater. The deflate stream has no end marker in this protocol, so Close does not wait for
// one; it only stops the reader. It is idempotent and safe to call while another goroutine is in
// DecompressPacket: the source never blocks and the flate reader needs no wake-up.
func (in *Inflater) Close() error {
	// Nothing to free: the flate reader has no goroutine and no file descriptor, and its Close would read the
	// error field a concurrent DecompressPacket writes. Dropping the references lets the GC do the rest.
	in.closed.Do(func() {})
	return nil
}

// DecompressPacket parses one SMSG_COMPRESSED_PACKET body and returns the inner wire opcode and its payload. The
// checksums are verified before the result is used: compressedAdler over the compressed bytes, and
// uncompressedAdler over the inner opcode bytes followed by the payload. A body whose deflate data cannot produce
// exactly the declared uncompressed size is an error, and it poisons the inflater: the deflate stream is
// continuous, so there is no position to resynchronise from.
func (in *Inflater) DecompressPacket(body []byte) (opcode uint32, payload []byte, err error) {
	if in.dead.Load() {
		return 0, nil, fmt.Errorf("wire: inflater is unusable after an earlier packet failed")
	}
	if len(body) < 12 {
		return 0, nil, fmt.Errorf("wire: compressed packet of %d bytes has no header", len(body))
	}
	uncompressedSize := binary.LittleEndian.Uint32(body[0:4])
	uncompressedAdler := binary.LittleEndian.Uint32(body[4:8])
	compressedAdler := binary.LittleEndian.Uint32(body[8:12])
	compressed := body[12:]
	if uncompressedSize < 4 || uncompressedSize > MaxUncompressed {
		return 0, nil, fmt.Errorf("wire: compressed packet claims %d uncompressed bytes", uncompressedSize)
	}
	if len(compressed) == 0 {
		return 0, nil, fmt.Errorf("wire: compressed packet has no data")
	}
	if got := adler32Seed(compressionSeed, compressed); got != compressedAdler {
		in.dead.Store(true)
		return 0, nil, fmt.Errorf("wire: compressed data checksum mismatch")
	}
	// The chunk is handed to the decoder before the read, on this goroutine: there is no channel and no second
	// goroutine to wait for. Copy because the caller's frame buffer may be reused; prepend what the previous
	// chunk left in the stream.
	need := len(in.src.cur) + len(compressed)
	if cap(in.pending) < need {
		in.pending = make([]byte, 0, need)
	}
	in.pending = append(in.pending[:0], in.src.cur...)
	in.pending = append(in.pending, compressed...)
	in.src.cur = in.pending
	out := make([]byte, uncompressedSize)
	if _, err := io.ReadFull(in.r, out); err != nil {
		in.dead.Store(true)
		in.src.cur = nil
		return 0, nil, fmt.Errorf("wire: compressed packet claims %d bytes but its deflate data ends: %w", uncompressedSize, err)
	}
	in.pending = in.src.cur
	if got := adler32Seed(compressionSeed, out); got != uncompressedAdler {
		in.dead.Store(true)
		return 0, nil, fmt.Errorf("wire: uncompressed data checksum mismatch")
	}
	return binary.LittleEndian.Uint32(out[0:4]), out[4:], nil
}

// adler32Seed is zlib's adler32 with an explicit seed (the server calls adler32(0x9827D8F1, data, len)); the
// standard library's adler32 always starts from the zlib initial value 1.
func adler32Seed(seed uint32, p []byte) uint32 {
	s1, s2 := seed&0xFFFF, (seed>>16)&0xFFFF
	for _, c := range p {
		s1 += uint32(c)
		if s1 >= 65521 {
			s1 -= 65521
		}
		s2 += s1
		if s2 >= 65521 {
			s2 -= 65521
		}
	}
	return s2<<16 | s1
}
