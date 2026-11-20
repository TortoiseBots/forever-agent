package worldconn

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func testConn(nc net.Conn) *conn {
	return &conn{
		nc:       nc,
		table:    wire.NewOpcodeTable(70707, 70707),
		inflater: wire.NewInflater(),
	}
}

func encodeFrame(wireOpcode uint32, body []byte) []byte {
	payload := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(payload, wireOpcode)
	copy(payload[4:], body)
	return append(wire.EncodeHeader(wire.Header{Size: uint32(len(payload))}), payload...)
}

// Close is idempotent and interrupts a read in progress; run with -race. The old conn closed the inflater's chunk
// channel, which panicked when a reader was sending on it, and a second Close panicked too.
func TestConnCloseIsIdempotentAndUnblocksARead(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := testConn(client)
	read := make(chan error, 1)
	go func() {
		_, err := c.readFrame(context.Background())
		read <- err
	}()
	time.Sleep(10 * time.Millisecond) // let the read reach the socket
	if err := c.close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock the read")
	}
	_ = c.close()
	_ = c.close()
}

// Two connections close cleanly in either order while both readers are blocked, and the readers return.
func TestCloseBothSocketsInEitherOrder(t *testing.T) {
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		pairs := make([][2]net.Conn, 2)
		conns := make([]*conn, 2)
		reads := make([]chan error, 2)
		for i := range conns {
			client, server := net.Pipe()
			pairs[i] = [2]net.Conn{client, server}
			conns[i] = testConn(client)
			reads[i] = make(chan error, 1)
			go func(i int) {
				_, err := conns[i].readFrame(context.Background())
				reads[i] <- err
			}(i)
		}
		time.Sleep(10 * time.Millisecond)
		for _, i := range order {
			_ = conns[i].close()
			_ = conns[i].close()
		}
		for i := range reads {
			select {
			case <-reads[i]:
			case <-time.After(time.Second):
				t.Fatalf("order %v: reader %d did not return", order, i)
			}
		}
		for _, p := range pairs {
			_ = p[1].Close()
		}
	}
}

// The server rejects a client frame of 64 KiB or more (PacketHeader::IsValidSize, WorldSocket.h:75), so the client
// refuses to build one and says why instead of getting the socket closed for it.
func TestSendFrameCapsAtTheServerLimit(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, server); close(drained) }()
	c := testConn(client)
	defer c.close()

	if err := c.sendFrame(context.Background(), wire.CMSGEnumCharacters, make([]byte, wire.MaxClientFrame-1-4)); err != nil {
		t.Fatalf("a frame of exactly the server's limit was refused: %v", err)
	}
	err := c.sendFrame(context.Background(), wire.CMSGEnumCharacters, make([]byte, wire.MaxClientFrame-4))
	if err == nil {
		t.Fatal("a frame of 64 KiB was sent")
	}
	if !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("the error does not name the server's limit: %v", err)
	}
	_ = client.Close()
	select {
	case <-drained:
	case <-time.After(time.Second):
	}
}

// A deadline belongs to one operation: the first read timing out must not make the connection unusable, which is
// what an absolute deadline from Authenticate did (every read after five minutes failed).
func TestDeadlinesArePerOperation(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := testConn(client)
	defer c.close()

	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.readFrame(short); err == nil {
		t.Fatal("a read with a 30 ms deadline returned a packet")
	} else if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("the read failed for another reason: %v", err)
	}
	// the next operation arms the socket for itself, so a packet that arrives now is read normally
	go func() { _, _ = server.Write(encodeFrame(0x4E0000, make([]byte, 65))) }()
	long, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	p, err := c.readFrame(long)
	if err != nil {
		t.Fatalf("the read after the timed-out operation failed: %v", err)
	}
	if p.Core != wire.SMSGAuthChallenge {
		t.Fatalf("read %s, want SMSG_AUTH_CHALLENGE", packetName(p))
	}
}

// recordingConn records every read deadline armed on the socket.
type recordingConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Time
}

func (c *recordingConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, t)
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

// TestReadDeadlineExtendsWithEveryFrame: each frame is read under a fresh, relative socket deadline, not under the
// connection's context deadline. A run that keeps receiving frames (the server's time syncs) must therefore never
// have a read fail because of an absolute time fixed when the run started. The context here stands in for the
// run's own deadline and the frames for a stay of many minutes.
func TestReadDeadlineExtendsWithEveryFrame(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	rc := &recordingConn{Conn: client}
	c := testConn(rc)
	defer c.close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	const frames = 5
	for i := range frames {
		go func() { _, _ = server.Write(encodeFrame(0x4E0000, make([]byte, 65))) }()
		if _, err := c.readFrame(ctx); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond) // one frame per moment, as a stay's time syncs arrive
	}

	rc.mu.Lock()
	recorded := append([]time.Time(nil), rc.deadlines...)
	rc.mu.Unlock()
	var armed []time.Time
	for _, d := range recorded {
		if !d.IsZero() { // the restore after each operation clears the deadline
			armed = append(armed, d)
		}
	}
	if len(armed) < frames {
		t.Fatalf("%d read deadline(s) armed for %d frames", len(armed), frames)
	}
	for i, d := range armed {
		if left := time.Until(d); left < defaultIOTimeout/2 {
			t.Fatalf("read %d was armed %s ahead, want about the per-operation %s rather than the context's 300 ms",
				i, left.Round(time.Millisecond), defaultIOTimeout)
		}
		if i > 0 && d.Before(armed[i-1]) {
			t.Fatalf("read %d's deadline %s is before read %d's %s", i, d, i-1, armed[i-1])
		}
	}
}

// realmOnListner returns a Realm whose socket is the client half of a TCP pair; the server half is accepted and
// kept open by the test until cleanup.
func realmOnListener(t *testing.T, allowRedirect bool) *Realm {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		ln.Close()
		select {
		case c := <-accepted:
			_ = c.Close()
		default:
		}
	})
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("the listener did not accept")
	}
	return &Realm{c: testConn(client), allowInstanceRedirect: allowRedirect}
}

// A hostile server can name any host in SMSG_CONNECT_TO, and the client would dial it and send the continued-session
// digest there. The default refuses a host other than the realm socket's; the error names both addresses.
func TestDialInstanceRefusesAHostOtherThanTheRealm(t *testing.T) {
	r := realmOnListener(t, false)
	_, err := r.DialInstance(context.Background(), ConnectTo{IP: net.IPv4(127, 0, 0, 2), Port: 8085})
	if err == nil {
		t.Fatal("the redirect to 127.0.0.2 was accepted")
	}
	if !strings.Contains(err.Error(), "127.0.0.2:8085") || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("the refusal does not name both addresses: %v", err)
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("the error does not say it refused: %v", err)
	}
}

// The explicit option permits a different host for clustered servers: the guard passes and the dial happens.
func TestDialInstanceAllowsARedirectOnlyWithTheOption(t *testing.T) {
	r := realmOnListener(t, true)
	_, err := r.DialInstance(context.Background(), ConnectTo{IP: net.IPv4(127, 0, 0, 2), Port: 1})
	if err == nil {
		t.Fatal("the dial to a closed port succeeded")
	}
	if strings.Contains(err.Error(), "refusing") {
		t.Fatalf("the redirect was refused although the option is set: %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.2:1") {
		t.Fatalf("the error does not name the redirect target: %v", err)
	}
}

// maxIgnoredHandshakeFrames stops a handshake loop: a server that keeps sending something other than the packet
// the step waits for fails the run at the cap instead of holding the client.
func TestHandshakeLoopsCapIgnoredFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		loop func(*conn, context.Context) error
	}{
		{"encrypted mode", func(c *conn, ctx context.Context) error { return c.waitEncryptedMode(ctx, [32]byte{}) }},
		{"auth response", func(c *conn, ctx context.Context) error { return c.expectAuthResponse(ctx) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			cn := testConn(client)
			defer cn.close()
			defer server.Close()
			opcode := cn.table.TranslateServerCore(wire.SMSGPong)
			if opcode == 0 {
				t.Fatal("the table has no wire number for SMSG_PONG")
			}
			go func() {
				for range maxIgnoredHandshakeFrames + 4 {
					if _, err := server.Write(encodeFrame(opcode, nil)); err != nil {
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := tc.loop(cn, ctx)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d packets", maxIgnoredHandshakeFrames+1)) {
				t.Fatalf("the loop did not stop at the cap of %d: %v", maxIgnoredHandshakeFrames, err)
			}
		})
	}
}

// The handshake packet names are reported in the caller's count, so the list has a bound: a server that floods
// the socket before the handshake ends cannot grow it.
func TestHandshakeNamesAreBounded(t *testing.T) {
	client, server := net.Pipe()
	cn := testConn(client)
	defer cn.close()
	defer server.Close()
	opcode := cn.table.TranslateServerCore(wire.SMSGPong)
	if opcode == 0 {
		t.Fatal("the table has no wire number for SMSG_PONG")
	}
	want := maxHandshakeNames + 3
	go func() {
		for range want {
			if _, err := server.Write(encodeFrame(opcode, nil)); err != nil {
				return
			}
		}
	}()
	for range want {
		if _, err := cn.readFrame(context.Background()); err != nil {
			t.Fatalf("frame: %v", err)
		}
	}
	if len(cn.handshakeNames) != maxHandshakeNames {
		t.Fatalf("%d handshake names kept, want the cap %d", len(cn.handshakeNames), maxHandshakeNames)
	}
	cn.handshakeDone = true
	go func() { _, _ = server.Write(encodeFrame(opcode, nil)) }()
	if _, err := cn.readFrame(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cn.handshakeNames) != maxHandshakeNames {
		t.Fatalf("a frame after the handshake was recorded: %d names", len(cn.handshakeNames))
	}
}

// The first encrypted frame of each direction uses counter 2 because the two plain frames before it advance the
// same counter (WorldPacketCrypt.cpp:69-103: EncryptSend/DecryptRecv increment even when not initialized). Pinned
// against a vector the core's own WorldPacketCrypt produced after two plain frames, not against this package.
func TestFirstEncryptedFrameUsesCounterTwo(t *testing.T) {
	v := loadWorldVectors(t)
	f := v.FirstEncryptedFrames
	if f.Counter != 2 || f.PlainFramesBefore != 2 {
		t.Fatalf("the vector is for counter %d after %d plain frames", f.Counter, f.PlainFramesBefore)
	}
	var key [32]byte
	copy(key[:], mustHex(t, v.EncryptKey))
	pc, err := newPacketCrypt(key)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := mustHex(t, f.Plaintext)
	serverCipher := mustHex(t, f.Server.Ciphertext)
	serverTag := mustHex(t, f.Server.Tag)

	// ---- send: two plain frames, then the vector's plaintext encrypted. net.Pipe has no buffer, so the reads
	// happen on their own goroutine.
	client, server := net.Pipe()
	defer server.Close()
	c := testConn(client)
	c.crypt = pc
	defer c.close()
	type rawFrame struct {
		header [16]byte
		body   []byte
		err    error
	}
	frames := make(chan rawFrame, 3)
	go func() {
		for range 3 {
			header, body, err := readRawFrame(server)
			frames <- rawFrame{header, body, err}
		}
	}()
	for range 2 {
		if err := c.sendFrame(context.Background(), wire.CMSGAuthSession, nil); err != nil {
			t.Fatal(err)
		}
	}
	c.encrypted = true
	payload := plaintext[4:]
	if err := c.sendFrame(context.Background(), wire.CMSGTimeSyncResponse, payload); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := <-frames; got.err != nil {
			t.Fatal(got.err)
		}
	}
	first := <-frames
	if first.err != nil {
		t.Fatal(first.err)
	}
	// open it with an AES-GCM instance built here, at counter 2: if sendFrame had counted the plain frames wrongly
	// (0 or 1), the tag would not verify. The core-vector equality of our seal at counter 2 is in
	// TestGCMFrameVectors; the receive half below pins the counter against the core's own frame.
	{
		block, err := aes.NewCipher(key[:])
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cipher.NewGCMWithTagSize(block, 12)
		if err != nil {
			t.Fatal(err)
		}
		sealed := append(append([]byte{}, first.body...), first.header[4:16]...)
		iv := makeIV(f.Counter, clientMagic)
		opened, err := gcm.Open(nil, iv[:], sealed, nil)
		if err != nil {
			t.Fatalf("the first encrypted frame does not open at counter 2 (its counter is not 2): %v", err)
		}
		want := encodeFrame(0x42005C, payload)[16:] // the wire opcode CMSG_TIME_SYNC_RESPONSE then the payload
		if toHex(opened) != toHex(want) {
			t.Fatalf("the first encrypted frame carries %s, want %s", toHex(opened), toHex(want))
		}
	}

	// ---- receive: two plain frames, then the server's vector frame at counter 2
	c.encrypted = false
	c.recvSeq = 0
	go func() {
		_, _ = server.Write(encodeFrame(0x4E0000, make([]byte, 65)))
		_, _ = server.Write(encodeFrame(0x4E0004, make([]byte, 69)))
		frame := make([]byte, 16+len(serverCipher))
		binary.LittleEndian.PutUint32(frame, uint32(len(serverCipher)))
		copy(frame[4:16], serverTag)
		copy(frame[16:], serverCipher)
		_, _ = server.Write(frame)
	}()
	for i := range 2 {
		if _, err := c.readFrame(context.Background()); err != nil {
			t.Fatalf("plain frame %d: %v", i+1, err)
		}
	}
	c.encrypted = true
	p, err := c.readFrame(context.Background())
	if err != nil {
		t.Fatalf("the first encrypted frame at counter 2 was refused: %v", err)
	}
	if p.Wire != 0x470001 || toHex(p.Payload) != toHex(plaintext[4:]) {
		t.Fatalf("the vector frame decrypted to wire 0x%06X and %s", p.Wire, toHex(p.Payload))
	}
}

func readRawFrame(nc net.Conn) ([16]byte, []byte, error) {
	var header [16]byte
	if _, err := io.ReadFull(nc, header[:]); err != nil {
		return header, nil, err
	}
	size := binary.LittleEndian.Uint32(header[:4])
	body := make([]byte, size)
	if _, err := io.ReadFull(nc, body); err != nil {
		return header, nil, err
	}
	return header, body, nil
}

func toHex(p []byte) string { return fmt.Sprintf("%X", p) }
