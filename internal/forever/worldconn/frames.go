package worldconn

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Packet is one server packet after decryption and decompression.
type Packet struct {
	Wire    uint32 // the number on the wire
	Core    uint32 // the core OpcodeServer it translates to (0 when the core has no such number)
	Name    string // the core name, or "" for an unknown wire number
	Payload []byte // without the 4-byte opcode
}

// conn is one world socket connection.
type conn struct {
	nc       net.Conn
	table    *wire.OpcodeTable
	inflater *wire.Inflater

	crypt     *packetCrypt
	encrypted bool
	sendSeq   uint64
	recvSeq   uint64

	// logf writes the step lines; sendMu makes the frame counter and the write (with its deadline) of one
	// connection a single operation, because the session answers packets from its event loop while a reader
	// goroutine may send a ping.
	logf      func(format string, args ...any)
	sendMu    sync.Mutex
	closeOnce sync.Once
	closeErr  error
	// handshakeNames records the server packets the handshake read (challenge, encrypted mode, auth response),
	// so the caller's packet count includes them; handshakeDone stops the recording when the handshake ends.
	handshakeNames []string
	handshakeDone  bool
	// buildAuthKeyUsed records that CMSG_AUTH_SESSION carried a digest made from a 16-byte build auth key; a
	// refusal of this connection carries it (Refused.BuildAuthKeyUsed).
	buildAuthKeyUsed bool
}

func (c *conn) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// maxHandshakeNames bounds the list of handshake packet names a connection keeps: the handshake reads a handful,
// and the list is reported as part of the packet count, so a flooding server cannot grow it.
const maxHandshakeNames = 64

// defaultIOTimeout is the deadline of one read or write. It bounds a single operation, not the connection's life: a
// keep-alive loop arms the socket again for every frame, so a run that keeps receiving traffic for hours runs for
// hours. The operation's context is what ends the connection (watchContext interrupts the blocked call when it is
// done); the socket deadline itself is never the context's absolute deadline, or each frame would inherit the
// connection's end instead of extending the wait for the next one.
const defaultIOTimeout = 60 * time.Second

// operationDeadline is the deadline one read or write of this connection gets: the stated default from now, never
// the context's absolute deadline, so every frame that arrives extends the wait for the next one. There is
// deliberately no connection-wide deadline.
func operationDeadline(context.Context) time.Time {
	return time.Now().Add(defaultIOTimeout)
}

// armRead arms the socket for one read, bounded by the operation's deadline, and unblocks a read already in
// progress when the context ends. The returned function stops the watcher and clears the deadline; the next
// operation arms the socket again. Nothing here is absolute from the connection's start.
func (c *conn) armRead(ctx context.Context) (stop func()) {
	if err := ctx.Err(); err != nil {
		_ = c.nc.SetReadDeadline(time.Unix(1, 0))
		return func() {}
	}
	_ = c.nc.SetReadDeadline(operationDeadline(ctx))
	return watchContext(ctx, func() { _ = c.nc.SetReadDeadline(time.Unix(1, 0)) }, func() { _ = c.nc.SetReadDeadline(time.Time{}) })
}

func (c *conn) armWrite(ctx context.Context) (stop func()) {
	if err := ctx.Err(); err != nil {
		_ = c.nc.SetWriteDeadline(time.Unix(1, 0))
		return func() {}
	}
	_ = c.nc.SetWriteDeadline(operationDeadline(ctx))
	return watchContext(ctx, func() { _ = c.nc.SetWriteDeadline(time.Unix(1, 0)) }, func() { _ = c.nc.SetWriteDeadline(time.Time{}) })
}

// watchContext runs interrupt when ctx is done, until the returned function stops it. stop waits for the watcher,
// so after stop returns the interrupt cannot run any more.
func watchContext(ctx context.Context, interrupt, restore func()) (stop func()) {
	if ctx.Done() == nil {
		return func() {}
	}
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			interrupt()
		case <-stopCh:
		}
	}()
	return func() {
		close(stopCh)
		<-done
		restore()
	}
}

// contextError turns a read/write error into the context's error when the context ended, so a deadline shows up as
// context.DeadlineExceeded rather than as a bare i/o timeout.
func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// readFrame reads one frame, decrypts it when encryption is on, decompresses SMSG_COMPRESSED_PACKET, and
// translates the wire opcode. Every packet is also recorded by name for the caller's count.
func (c *conn) readFrame(ctx context.Context) (Packet, error) {
	stop := c.armRead(ctx)
	defer stop()

	var headerBytes [wire.HeaderSize]byte
	if _, err := io.ReadFull(c.nc, headerBytes[:]); err != nil {
		return Packet{}, fmt.Errorf("worldconn: reading a frame header: %w", contextError(ctx, err))
	}
	header, err := wire.DecodeHeader(headerBytes[:])
	if err != nil {
		return Packet{}, err
	}
	if header.Size < 4 || header.Size > wire.MaxFrame {
		return Packet{}, fmt.Errorf("worldconn: frame size %d out of range", header.Size)
	}
	body := make([]byte, header.Size)
	if _, err := io.ReadFull(c.nc, body); err != nil {
		return Packet{}, fmt.Errorf("worldconn: reading a frame body: %w", contextError(ctx, err))
	}

	counter := c.recvSeq
	c.recvSeq++
	if c.encrypted {
		plain, err := c.crypt.open(counter, body, header.Tag)
		if err != nil {
			return Packet{}, err
		}
		body = plain
	}
	if len(body) < 4 {
		return Packet{}, fmt.Errorf("worldconn: frame body of %d bytes has no opcode", len(body))
	}
	wireOpcode := binary.LittleEndian.Uint32(body)
	payload := body[4:]

	if wireOpcode == c.table.TranslateServerCore(wire.SMSGCompressedPacket) {
		innerOpcode, innerPayload, err := c.inflater.DecompressPacket(payload)
		if err != nil {
			return Packet{}, err
		}
		wireOpcode, payload = innerOpcode, innerPayload
	}
	if len(payload) > wire.MaxUncompressed {
		return Packet{}, fmt.Errorf("worldconn: packet of %d bytes exceeds the cap", len(payload))
	}
	core := c.table.TranslateServerWire(wireOpcode)
	name := wire.ServerName(core)
	if !c.handshakeDone && len(c.handshakeNames) < maxHandshakeNames {
		c.handshakeNames = append(c.handshakeNames, packetLabel(wireOpcode, core))
	}
	return Packet{Wire: wireOpcode, Core: core, Name: name, Payload: payload}, nil
}

// sendFrame translates a core client opcode, encrypts when encryption is on, and writes one frame.
func (c *conn) sendFrame(ctx context.Context, core uint32, payload []byte) error {
	wireOpcode, ok := c.table.WireForClient(core)
	if !ok {
		return fmt.Errorf("worldconn: no wire number for %s for client build %d", wire.ClientName(core), c.table.ClientBuild())
	}
	// PacketHeader::IsValidSize requires Size < 0x10000 (WorldSocket.h:75), and Size counts the opcode: the server
	// rejects a client frame of 64 KiB or more outright.
	if len(payload)+4 >= wire.MaxClientFrame {
		return fmt.Errorf("worldconn: refusing to send %s with %d payload bytes: the server rejects client frames of 64 KiB or more (WorldSocket.h:75)", wire.ClientName(core), len(payload))
	}
	body := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(body, wireOpcode)
	copy(body[4:], payload)

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	header := wire.Header{Size: uint32(len(body))}
	if c.encrypted {
		var tag [wire.TagSize]byte
		body, tag = c.crypt.seal(c.sendSeq, body)
		header.Tag = tag
	}
	c.sendSeq++

	stop := c.armWrite(ctx)
	defer stop()
	frame := append(wire.EncodeHeader(header), body...)
	if _, err := c.nc.Write(frame); err != nil {
		return fmt.Errorf("worldconn: sending %s: %w", wire.ClientName(core), contextError(ctx, err))
	}
	return nil
}

// readExact reads exactly n bytes for the handshake (banners), with the connection deadline.
func (c *conn) readExact(ctx context.Context, n int) ([]byte, error) {
	stop := c.armRead(ctx)
	defer stop()
	p := make([]byte, n)
	if _, err := io.ReadFull(c.nc, p); err != nil {
		return nil, fmt.Errorf("worldconn: reading %d bytes: %w", n, contextError(ctx, err))
	}
	return p, nil
}

func (c *conn) writeAll(ctx context.Context, p []byte) error {
	stop := c.armWrite(ctx)
	defer stop()
	if _, err := c.nc.Write(p); err != nil {
		return fmt.Errorf("worldconn: writing the banner: %w", contextError(ctx, err))
	}
	return nil
}

// close closes the socket and the inflater, once: a second call (from both Close methods, or from a defer and an
// error path) is a no-op. Closing is safe while a pump is reading: the socket Close interrupts it and the
// inflater has no channel to close.
func (c *conn) close() error {
	c.closeOnce.Do(func() {
		if c.inflater != nil {
			_ = c.inflater.Close()
		}
		if c.nc != nil {
			c.closeErr = c.nc.Close()
		}
	})
	return c.closeErr
}
