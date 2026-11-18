package world

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// A fake world server for the end-to-end test. It is written against the packet layouts and the hand-computed wire
// numbers (70707 client and realm build, opcode group shift 1) rather than against this package's translation or
// codecs: a wrong wire number or field order in the client shows up here as a protocol error. It does what the real
// server does for this round: banners, SMSG_AUTH_CHALLENGE, a CMSG_AUTH_SESSION whose digest it really verifies (it
// has a build auth key, which the live server does not), key derivation with its own HMAC/SHA512 code, the
// encrypted mode, a compressed auth response and character list, SMSG_CONNECT_TO, the instance socket with
// CMSG_AUTH_CONTINUED_SESSION and its mandatory digest, SMSG_RESUME_COMMS, the login burst, time sync, the
// active-mover exchange, ping/pong and the logout.

const (
	fakeServerHello = "WORLD OF WARCRAFT CONNECTION - SERVER TO CLIENT - V2\n"
	fakeClientHello = "WORLD OF WARCRAFT CONNECTION - CLIENT TO SERVER - V2\n"

	// wire numbers for client build 70707 on realm build 70707, hand-computed from ClassicOpcodes.cpp
	fakeCMSGAuthSession          = 0x460001
	fakeCMSGAuthContinuedSession = 0x460003
	fakeCMSGEnterEncryptedMode   = 0x460005
	fakeCMSGPing                 = 0x460006
	fakeCMSGQueuedMessagesEnd    = 0x46000A
	fakeCMSGEnumCharacters       = 0x440014
	fakeCMSGPlayerLogin          = 0x440016
	fakeCMSGLoadingScreenNotify  = 0x440024
	fakeCMSGMoveInitActiveMover  = 0x420064
	fakeCMSGTimeSyncResponse     = 0x42005C
	fakeCMSGLogoutRequest        = 0x3F0074
	fakeCMSGLogDisconnect        = 0x460007

	fakeSMSGAuthChallenge        = 0x4E0000
	fakeSMSGEnterEncrypted       = 0x4E0004
	fakeSMSGResumeComms          = 0x4E0006
	fakeSMSGConnectTo            = 0x4E0008
	fakeSMSGPong                 = 0x4E0009
	fakeSMSGCompressedPacket     = 0x4E000E
	fakeSMSGAuthResponse         = 0x470001
	fakeSMSGEnumCharacters       = 0x470018
	fakeSMSGLoginVerifyWorld     = 0x47002F
	fakeSMSGTimeSyncRequest      = 0x600000
	fakeSMSGMoveSetActive        = 0x600003
	fakeSMSGLogoutResponse       = 0x470130
	fakeSMSGLogoutComplete       = 0x470131
	fakeSMSGCharacterLoginFailed = 0x4701B1

	// the unasked companions of the character list
	fakeSMSGBattlePayDistribution = 0x47021F
	fakeSMSGRecentAlly            = 0x470363
	fakeSMSGAccountItemCollection = 0x470361
)

var (
	fakeSessionKeySeed = []byte{
		0xE8, 0x1E, 0x8B, 0x59, 0x27, 0x62, 0x1E, 0xAA, 0x86, 0x15, 0x18, 0xEA, 0xC0, 0xBF, 0x66, 0x8C,
		0x6D, 0xBF, 0x83, 0x93, 0xBC, 0xAA, 0x80, 0x52, 0x5B, 0x1E, 0xDC, 0x23, 0xA0, 0x12, 0xB7, 0x50,
	}
	fakeEncryptionKeySeed = []byte{
		0x71, 0xC9, 0xED, 0x5A, 0xA7, 0x0E, 0x4D, 0xFF, 0x4C, 0x36, 0xA6, 0x5A, 0x3E, 0x46, 0x8A, 0x4A,
		0x5D, 0xA1, 0x48, 0xC8, 0x30, 0x47, 0x4A, 0xDE, 0xF6, 0x0D, 0x6C, 0xBE, 0x6F, 0xE4, 0x55, 0x73,
	}
	fakeAuthCheckSeed = []byte{
		0xDE, 0x3A, 0x2A, 0x8E, 0x6B, 0x89, 0x52, 0x66, 0x88, 0x9D, 0x7E, 0x7A, 0x77, 0x1D, 0x5D, 0x1F,
		0x4E, 0xD9, 0x0C, 0x23, 0x9B, 0xCD, 0x0E, 0xDC, 0xD2, 0xE8, 0x04, 0x3A, 0x68, 0x64, 0xC7, 0xB0,
	}
	fakeContinuedSeed = []byte{
		0x56, 0x5C, 0x61, 0x9C, 0x48, 0x3A, 0x52, 0x1F, 0x61, 0x5D, 0x05, 0x49, 0xB2, 0x9A, 0x39, 0xBF,
		0x4B, 0x97, 0xB0, 0x1B, 0xF9, 0x6C, 0xDE, 0xD6, 0x80, 0x1D, 0xAB, 0x26, 0x02, 0xA9, 0x9B, 0x9D,
	}
)

type fakeServer struct {
	t                *testing.T
	realmListener    net.Listener
	instanceListener net.Listener
	errs             chan error
	wg               sync.WaitGroup

	keyData    [64]byte
	buildKey   [16]byte
	sessionKey [40]byte
	encryptKey [32]byte

	realmChallenge    [32]byte
	instanceChallenge [32]byte
	localChallenge    [32]byte
	ticket            []byte
	clientGUID        []byte // the packed guid bytes of the character list fixture
	enumResult        []byte

	logoutOnce sync.Once
	logoutDone chan struct{}
	// failAfterLogin makes the fake refuse the character just after the active-mover exchange, so the client's stay
	// loop fails and Enter has to send the abort logout; abortDone is closed when the fake has seen it.
	failAfterLogin bool
	abortDone      chan struct{}
	mu             sync.Mutex
	realmWire      []uint32
	instWire       []uint32
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	rl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	il, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		rl.Close()
		t.Fatal(err)
	}
	f := &fakeServer{
		t:                t,
		realmListener:    rl,
		instanceListener: il,
		errs:             make(chan error, 32),
		logoutDone:       make(chan struct{}),
		abortDone:        make(chan struct{}),
	}
	for i := range 32 {
		f.keyData[i] = byte(i)
		f.keyData[32+i] = byte(0x20 + i)
		f.realmChallenge[i] = byte(0x40 + i)
		f.instanceChallenge[i] = byte(0x80 + i)
	}
	for i := range 16 {
		f.buildKey[i] = byte(0xA0 + i)
	}
	f.enumResult = fixture(t, "enum_characters_result")
	// the packed guid at the start of the first regionwide entry: uint16 mask, then popcount bytes
	mask := binary.LittleEndian.Uint16(f.enumResult[34:36])
	count := bits.OnesCount16(mask)
	f.clientGUID = append([]byte{}, f.enumResult[36:36+count]...)
	return f
}

func (f *fakeServer) close() {
	f.realmListener.Close()
	f.instanceListener.Close()
}

func (f *fakeServer) failf(format string, args ...any) {
	err := fmt.Errorf(format, args...)
	f.t.Logf("fake server: %v", err)
	select {
	case f.errs <- err:
	default:
	}
}

// wait waits for both handlers and returns the first protocol error they reported.
func (f *fakeServer) wait(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the fake server did not finish")
	}
	select {
	case err := <-f.errs:
		t.Fatalf("fake server: %v", err)
	default:
	}
}

// fakeConn is one side of the fake's socket. Writes may come from two goroutines (the logout completion is sent on
// the realm socket while its reader is blocked), so the send counter is under a mutex.
type fakeConn struct {
	f       *fakeServer
	nc      net.Conn
	writeMu sync.Mutex
	readMu  sync.Mutex

	sendSeq   uint64
	recvSeq   uint64
	gcm       cipher.AEAD
	encrypted bool
	compress  *flate.Writer
	cmpBuf    *bytes.Buffer
}

func (f *fakeServer) newConn(nc net.Conn) (*fakeConn, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	return &fakeConn{f: f, nc: nc, compress: w, cmpBuf: &buf}, nil
}

func (c *fakeConn) setKey(key [32]byte) error {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	c.gcm, err = cipher.NewGCMWithTagSize(block, 12)
	return err
}

func fakeIV(counter uint64, magic uint32) [12]byte {
	var iv [12]byte
	binary.LittleEndian.PutUint64(iv[:8], counter)
	binary.LittleEndian.PutUint32(iv[8:], magic)
	return iv
}

func (c *fakeConn) writeFrame(wire uint32, payload []byte) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	body := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(body, wire)
	copy(body[4:], payload)
	var tag [12]byte
	if c.encrypted {
		iv := fakeIV(c.sendSeq, 0x52565253)
		sealed := c.gcm.Seal(nil, iv[:], body, nil)
		body = sealed[:len(sealed)-12]
		copy(tag[:], sealed[len(sealed)-12:])
	}
	c.sendSeq++
	frame := make([]byte, 16+len(body))
	binary.LittleEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:16], tag[:])
	copy(frame[16:], body)
	if _, err := c.nc.Write(frame); err != nil {
		c.f.failf("fake server write: %v", err)
	}
}

func (c *fakeConn) readFrame() (uint32, []byte, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	var header [16]byte
	if _, err := io.ReadFull(c.nc, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.LittleEndian.Uint32(header[:4])
	if size < 4 || size > 4<<20 {
		return 0, nil, fmt.Errorf("fake: bad frame size %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(c.nc, body); err != nil {
		return 0, nil, err
	}
	if c.encrypted {
		var tag [12]byte
		copy(tag[:], header[4:16])
		iv := fakeIV(c.recvSeq, 0x544E4C43)
		buf := append(append([]byte{}, body...), tag[:]...)
		plain, err := c.gcm.Open(nil, iv[:], buf, nil)
		if err != nil {
			return 0, nil, fmt.Errorf("fake: client packet failed its GCM check at counter %d", c.recvSeq)
		}
		body = plain
	}
	c.recvSeq++
	return binary.LittleEndian.Uint32(body), body[4:], nil
}

// writeCompressed writes one packet the way the server compresses a large encrypted packet: one deflate stream per
// connection, opcode first, Z_SYNC_FLUSH per packet, and the two seeded adler32 values.
func (c *fakeConn) writeCompressed(innerWire uint32, innerPayload []byte) {
	before := c.cmpBuf.Len()
	var opcode [4]byte
	binary.LittleEndian.PutUint32(opcode[:], innerWire)
	if _, err := c.compress.Write(opcode[:]); err != nil {
		c.f.failf("fake compression: %v", err)
	}
	if _, err := c.compress.Write(innerPayload); err != nil {
		c.f.failf("fake compression: %v", err)
	}
	if err := c.compress.Flush(); err != nil {
		c.f.failf("fake compression flush: %v", err)
	}
	compressed := c.cmpBuf.Bytes()[before:]
	body := make([]byte, 12, 12+len(compressed))
	binary.LittleEndian.PutUint32(body[0:4], uint32(4+len(innerPayload)))
	binary.LittleEndian.PutUint32(body[4:8], fakeAdler(fakeAdler(0x9827D8F1, opcode[:]), innerPayload))
	binary.LittleEndian.PutUint32(body[8:12], fakeAdler(0x9827D8F1, compressed))
	body = append(body, compressed...)
	c.writeFrame(fakeSMSGCompressedPacket, body)
}

func fakeAdler(seed uint32, p []byte) uint32 {
	s1, s2 := seed&0xFFFF, (seed>>16)&0xFFFF
	for _, c := range p {
		s1 = (s1 + uint32(c)) % 65521
		s2 = (s2 + s1) % 65521
	}
	return s2<<16 | s1
}

func fakeHMAC(key []byte, parts ...[]byte) []byte {
	h := hmac.New(sha512.New, key)
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func fakeSHA512(parts ...[]byte) []byte {
	h := sha512.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func (f *fakeServer) deriveKeys(keyData, serverChallenge, localChallenge []byte) ([40]byte, [32]byte) {
	seed := fakeHMAC(fakeSHA512(keyData), serverChallenge, localChallenge, fakeSessionKeySeed)
	o1 := fakeSHA512(seed[:32])
	o2 := fakeSHA512(seed[32:])
	o0 := fakeSHA512(o1, make([]byte, 64), o2)
	var sessionKey [40]byte
	copy(sessionKey[:], o0[:40])
	digest := fakeHMAC(sessionKey[:], localChallenge, serverChallenge, fakeEncryptionKeySeed)
	var encryptKey [32]byte
	copy(encryptKey[:], digest[:32])
	return sessionKey, encryptKey
}

func (f *fakeServer) serveRealm() {
	defer f.wg.Done()
	conn, err := f.realmListener.Accept()
	if err != nil {
		f.failf("realm accept: %v", err)
		return
	}
	c, err := f.newConn(conn)
	if err != nil {
		f.failf("realm: %v", err)
		return
	}
	if !f.banners(conn, "realm") {
		return
	}
	challenge := make([]byte, 65)
	for i := range 32 {
		challenge[i] = byte(i) // the DoS challenge, which the server never checks
		challenge[32+i] = f.realmChallenge[i]
	}
	challenge[64] = 1
	c.writeFrame(fakeSMSGAuthChallenge, challenge)

	wire, payload, err := c.readFrame()
	if err != nil || wire != fakeCMSGAuthSession {
		f.failf("realm: expected CMSG_AUTH_SESSION 0x%06X, got 0x%06X (%v)", fakeCMSGAuthSession, wire, err)
		return
	}
	if len(payload) < 81 {
		f.failf("realm: CMSG_AUTH_SESSION is %d bytes", len(payload))
		return
	}
	if realmID := binary.LittleEndian.Uint32(payload[16:20]); realmID != 70 {
		f.failf("realm: CMSG_AUTH_SESSION carries realm %d, want 70", realmID)
		return
	}
	copy(f.localChallenge[:], payload[20:52])
	clientDigest := payload[52:76]
	if payload[76] != 0 {
		f.failf("realm: the UseIPv6 bit byte is 0x%02X, want one zero byte", payload[76])
		return
	}
	ticketSize := binary.LittleEndian.Uint32(payload[77:81])
	if int(ticketSize) != len(payload)-81 {
		f.failf("realm: the join ticket size says %d, the packet carries %d", ticketSize, len(payload)-81)
		return
	}
	f.ticket = append([]byte{}, payload[81:]...)
	f.mu.Lock()
	f.realmWire = append(f.realmWire, wire)
	f.mu.Unlock()
	wantDigest := fakeHMAC(fakeSHA512(f.keyData[:], f.buildKey[:]), f.localChallenge[:], f.realmChallenge[:], fakeAuthCheckSeed)
	if !hmac.Equal(clientDigest, wantDigest[:24]) {
		f.failf("realm: the CMSG_AUTH_SESSION digest is not the server's HMAC")
		return
	}
	f.sessionKey, f.encryptKey = f.deriveKeys(f.keyData[:], f.realmChallenge[:], f.localChallenge[:])
	if err := c.setKey(f.encryptKey); err != nil {
		f.failf("realm: %v", err)
		return
	}

	mode := make([]byte, 69)
	binary.LittleEndian.PutUint32(mode, 8) // RegionGroup
	for i := range 64 {
		mode[4+i] = 0x5A // the Ed25519 signature; the client has no public key to verify it with
	}
	mode[68] = 0x80
	c.writeFrame(fakeSMSGEnterEncrypted, mode)
	wire, _, err = c.readFrame()
	if err != nil || wire != fakeCMSGEnterEncryptedMode {
		f.failf("realm: expected CMSG_ENTER_ENCRYPTED_MODE_ACK, got 0x%06X (%v)", wire, err)
		return
	}
	c.encrypted = true

	// SMSG_AUTH_RESPONSE and the character list are larger than 0x400 bytes and arrive compressed
	c.writeCompressed(fakeSMSGAuthResponse, fixture(f.t, "auth_response"))

	for {
		wire, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			case <-f.abortDone:
				return
			case <-time.After(time.Second):
			}
			f.failf("realm: %v", err)
			return
		}
		f.mu.Lock()
		f.realmWire = append(f.realmWire, wire)
		f.mu.Unlock()
		switch wire {
		case fakeCMSGEnumCharacters:
			if len(payload) != 0 {
				f.failf("realm: CMSG_ENUM_CHARACTERS carries %d bytes", len(payload))
			}
			c.writeCompressed(fakeSMSGEnumCharacters, f.enumResult)
			c.writeFrame(fakeSMSGBattlePayDistribution, []byte{0, 0, 0, 0, 0, 0})
			c.writeFrame(fakeSMSGRecentAlly, []byte{0x10, 0xBD, 0xEC, 0x08, 7, 0, 0, 0, 0})
			c.writeFrame(fakeSMSGAccountItemCollection, []byte{0, 0, 0, 0})
			// a wire number the core has no opcode for: the client must count it and go on, never fail
			c.writeFrame(0x990001, []byte{0xDE, 0xAD})
		case fakeCMSGPlayerLogin:
			mask := binary.LittleEndian.Uint16(payload)
			count := bits.OnesCount16(mask)
			if len(payload) != 2+count+4+1 {
				f.failf("realm: CMSG_PLAYER_LOGIN is %d bytes for a %d-byte guid", len(payload), count)
				continue
			}
			if !bytes.Equal(payload[2:2+count], f.clientGUID) {
				f.failf("realm: CMSG_PLAYER_LOGIN guid % X, the list sent % X", payload[2:2+count], f.clientGUID)
				continue
			}
			if binary.LittleEndian.Uint32(payload[2+count:2+count+4]) != 0x44424000 { // 777.0
				f.failf("realm: CMSG_PLAYER_LOGIN far clip is not 777.0")
				continue
			}
			c.writeFrame(fakeSMSGConnectTo, f.connectToPayload())
			f.wg.Add(1)
			go f.serveInstance(c)
		case fakeCMSGLoadingScreenNotify:
			if len(payload) != 5 || binary.LittleEndian.Uint32(payload) != 0 {
				f.failf("realm: CMSG_LOADING_SCREEN_NOTIFY = % X, want map 0 and one zero bit byte", payload)
				continue
			}
		case fakeCMSGPing:
			if len(payload) != 8 {
				f.failf("realm: CMSG_PING is %d bytes", len(payload))
				continue
			}
			c.writeFrame(fakeSMSGPong, payload[:4])
		default:
			f.failf("realm: unexpected client wire 0x%06X", wire)
		}
	}
}

func (f *fakeServer) banners(conn net.Conn, which string) bool {
	// the server sends its banner on connect and then waits for the client's
	if _, err := conn.Write([]byte(fakeServerHello)); err != nil {
		f.failf("%s: writing the banner: %v", which, err)
		return false
	}
	banner := make([]byte, len(fakeClientHello))
	if _, err := io.ReadFull(conn, banner); err != nil {
		f.failf("%s: reading the client banner: %v", which, err)
		return false
	}
	if string(banner) != fakeClientHello {
		f.failf("%s: the client did not send its V2 banner", which)
		return false
	}
	return true
}

// serveInstance runs the instance socket; the realm conn is used for SMSG_LOGOUT_COMPLETE, which the real server
// sends on the realm socket (SMSG_LOGOUT_COMPLETE's connection index) while it closes the instance socket.
func (f *fakeServer) serveInstance(realm *fakeConn) {
	defer f.wg.Done()
	conn, err := f.instanceListener.Accept()
	if err != nil {
		f.failf("instance accept: %v", err)
		return
	}
	c, err := f.newConn(conn)
	if err != nil {
		f.failf("instance: %v", err)
		return
	}
	if !f.banners(conn, "instance") {
		return
	}
	challenge := make([]byte, 65)
	copy(challenge[32:64], f.instanceChallenge[:])
	challenge[64] = 1
	c.writeFrame(fakeSMSGAuthChallenge, challenge)

	wire, payload, err := c.readFrame()
	f.mu.Lock()
	f.instWire = append(f.instWire, wire)
	f.mu.Unlock()
	if err != nil || wire != fakeCMSGAuthContinuedSession {
		f.failf("instance: expected CMSG_AUTH_CONTINUED_SESSION 0x%06X, got 0x%06X (%v)", fakeCMSGAuthContinuedSession, wire, err)
		return
	}
	if len(payload) != 80 {
		f.failf("instance: CMSG_AUTH_CONTINUED_SESSION is %d bytes, want 80", len(payload))
		return
	}
	local := payload[8:40]
	digest := payload[40:64]
	key := binary.LittleEndian.Uint64(payload[64:72])
	var keyBytes [8]byte
	binary.LittleEndian.PutUint64(keyBytes[:], key)
	want := fakeHMAC(f.sessionKey[:], keyBytes[:], local, f.instanceChallenge[:], fakeContinuedSeed)
	if !hmac.Equal(digest, want[:24]) {
		f.failf("instance: the continued-session digest is not the server's HMAC")
		return
	}
	if binary.LittleEndian.Uint32(payload[72:76]) != 0x46010046 {
		f.failf("instance: native realm address 0x%08X, want 0x46010046", binary.LittleEndian.Uint32(payload[72:76]))
		return
	}
	// the instance's encryption key is derived from the instance challenge, not the realm one
	instanceKeyDigest := fakeHMAC(f.sessionKey[:], local, f.instanceChallenge[:], fakeEncryptionKeySeed)
	var instanceKey [32]byte
	copy(instanceKey[:], instanceKeyDigest[:32])
	if err := c.setKey(instanceKey); err != nil {
		f.failf("instance: %v", err)
		return
	}
	mode := make([]byte, 69)
	mode[68] = 0x80
	c.writeFrame(fakeSMSGEnterEncrypted, mode)
	wire, _, err = c.readFrame()
	f.mu.Lock()
	f.instWire = append(f.instWire, wire)
	f.mu.Unlock()
	if err != nil || wire != fakeCMSGEnterEncryptedMode {
		f.failf("instance: expected the encrypted-mode ack, got 0x%06X (%v)", wire, err)
		return
	}
	c.encrypted = true

	c.writeFrame(fakeSMSGResumeComms, nil)
	wire, payload, err = c.readFrame()
	f.mu.Lock()
	f.instWire = append(f.instWire, wire)
	f.mu.Unlock()
	if err != nil || wire != fakeCMSGQueuedMessagesEnd {
		f.failf("instance: expected CMSG_QUEUED_MESSAGES_END, got 0x%06X (%v)", wire, err)
		return
	}
	if len(payload) != 4 || binary.LittleEndian.Uint32(payload) == 0 {
		f.failf("instance: CMSG_QUEUED_MESSAGES_END carries %d bytes", len(payload))
		return
	}
	c.writeFrame(fakeSMSGLoginVerifyWorld, fixture(f.t, "login_verify_world"))
	c.writeFrame(fakeSMSGTimeSyncRequest, []byte{7, 0, 0, 0})
	mover := make([]byte, 2+len(f.clientGUID))
	binary.LittleEndian.PutUint16(mover, binary.LittleEndian.Uint16(f.enumResult[34:36]))
	copy(mover[2:], f.clientGUID)
	c.writeFrame(fakeSMSGMoveSetActive, mover)
	if f.failAfterLogin {
		// the player is in the world (the mover went out), then the server refuses the character: the client's stay
		// loop fails and Enter must tell the server the player is leaving before the sockets drop
		c.writeFrame(fakeSMSGCharacterLoginFailed, []byte{4})
		for {
			wire, _, err := c.readFrame()
			if err != nil {
				close(f.abortDone)
				time.Sleep(20 * time.Millisecond)
				conn.Close()
				return
			}
			f.mu.Lock()
			f.instWire = append(f.instWire, wire)
			f.mu.Unlock()
		}
	}

	sawTimeSync, sawMover := false, false
	for {
		wire, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			f.failf("instance: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wire)
		f.mu.Unlock()
		switch wire {
		case fakeCMSGTimeSyncResponse:
			if len(payload) != 8 || binary.LittleEndian.Uint32(payload) != 7 || binary.LittleEndian.Uint32(payload[4:]) == 0 {
				f.failf("instance: CMSG_TIME_SYNC_RESPONSE = % X", payload)
				continue
			}
			sawTimeSync = true
		case fakeCMSGMoveInitActiveMover:
			if len(payload) != 4 || binary.LittleEndian.Uint32(payload) == 0 {
				f.failf("instance: CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE = % X", payload)
				continue
			}
			sawMover = true
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGLogoutRequest:
			if !sawTimeSync || !sawMover {
				f.failf("instance: logout before the time sync (%v) and active mover (%v) answers", sawTimeSync, sawMover)
			}
			c.writeFrame(fakeSMSGLogoutResponse, []byte{0, 0, 0, 0, 0})
			time.Sleep(20 * time.Millisecond) // the completion travels on the other socket
			realm.writeFrame(fakeSMSGLogoutComplete, []byte{0})
			f.logoutOnce.Do(func() { close(f.logoutDone) })
			time.Sleep(20 * time.Millisecond)
			conn.Close()
			return
		default:
			f.failf("instance: unexpected client wire 0x%06X", wire)
		}
	}
}

func (f *fakeServer) connectToPayload() []byte {
	p := make([]byte, 0, 45)
	p = appendU32(p, 1)                  // one address
	p = appendU32(p, 17)                 // serial
	p = append(p, 1)                     // connection type INSTANCE
	p = appendU64(p, 0x1122334455667788) // the synthetic key the instance must echo
	p = appendU32(p, 0x46010046)         // native realm address
	p = appendU32(p, 0)                  // Key3
	p = append(p, 1, 127, 0, 0, 1)       // IPv4 loopback
	p = appendU16(p, uint16(f.instanceListener.Addr().(*net.TCPAddr).Port))
	p = append(p, 0, 0, 0, 0, 0) // the empty bleep token: 35 zero bits, flushed, and a zero duration
	return p
}

func appendU32(p []byte, v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return append(p, b[:]...)
}

func appendU16(p []byte, v uint16) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	return append(p, b[:]...)
}

func appendU64(p []byte, v uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return append(p, b[:]...)
}

// TestEnterAgainstFakeServer is the round's end-to-end test: the real client code against a server that speaks the
// captured and hand-computed protocol, from the banners to SMSG_LOGOUT_COMPLETE.
func TestEnterAgainstFakeServer(t *testing.T) {
	f := newFakeServer(t)
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX", // the fixture's character name
		Stay:            time.Second,
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)

	if len(report.Characters) != 1 {
		t.Fatalf("%d characters", len(report.Characters))
	}
	if report.Character.Name != "XXXXXX" || report.Character.Surname != "XXXX" {
		t.Fatalf("character %q %q", report.Character.Name, report.Character.Surname)
	}
	if !report.InWorld || report.ActiveMoverGUID != report.Character.GUID {
		t.Fatalf("in world %v, active mover %s, character %s", report.InWorld, report.ActiveMoverGUID, report.Character.GUID)
	}
	if report.MapID != 0 || report.Position[0] != 1.25 || report.Position[1] != 2.5 || report.Position[2] != 3.75 {
		t.Fatalf("map %d position %v", report.MapID, report.Position)
	}
	if report.TimeSyncs < 1 || report.Pings < 1 || report.Pongs < 1 {
		t.Fatalf("time syncs %d, pings %d, pongs %d", report.TimeSyncs, report.Pings, report.Pongs)
	}
	if !report.LogoutComplete || report.Logout.Result != 0 {
		t.Fatalf("logout %+v complete %v", report.Logout, report.LogoutComplete)
	}
	for _, name := range []string{
		"SMSG_AUTH_RESPONSE", "SMSG_ENUM_CHARACTERS_RESULT", "SMSG_CONNECT_TO", "SMSG_RESUME_COMMS",
		"SMSG_LOGIN_VERIFY_WORLD", "SMSG_TIME_SYNC_REQUEST", "SMSG_MOVE_SET_ACTIVE_MOVER", "SMSG_PONG",
		"SMSG_LOGOUT_RESPONSE", "SMSG_LOGOUT_COMPLETE",
		// the unasked companions of the list, named through the 70707 translation
		"SMSG_BATTLE_PAY_GET_DISTRIBUTION_LIST_RESPONSE", "SMSG_RECENT_ALLY_DATA_RESPONSE",
		"SMSG_ACCOUNT_ITEM_COLLECTION_DATA",
	} {
		if report.Counts[name] == 0 {
			t.Errorf("the report does not count %s; counts: %v", name, report.Counts)
		}
	}
	if report.Counts["wire 0x990001"] != 1 {
		t.Errorf("an unknown wire number was not counted and skipped; counts: %v", report.Counts)
	}
	if len(f.ticket) == 0 || !bytes.Contains(f.ticket, []byte(`"gameAccount":"000#0"`)) {
		t.Fatalf("the fake did not receive the join ticket: %q", f.ticket)
	}
	for _, wire := range []uint32{fakeCMSGEnumCharacters, fakeCMSGPlayerLogin, fakeCMSGQueuedMessagesEnd, fakeCMSGTimeSyncResponse, fakeCMSGMoveInitActiveMover, fakeCMSGLoadingScreenNotify, fakeCMSGPing, fakeCMSGLogoutRequest} {
		if !containsWire(f.realmWire, wire) && !containsWire(f.instWire, wire) {
			t.Errorf("the client never sent wire 0x%06X", wire)
		}
	}
}

func containsWire(list []uint32, want uint32) bool {
	for _, w := range list {
		if w == want {
			return true
		}
	}
	return false
}

func joinLines(lines []string) string {
	var b bytes.Buffer
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// A run that fails after the character was logged in must not leave it online until the server's socket timeout:
// Enter sends CMSG_LOGOUT_REQUEST and CMSG_LOG_DISCONNECT on the instance socket before it closes.
func TestEnterSendsTheAbortLogoutWhenItFailsInWorld(t *testing.T) {
	f := newFakeServer(t)
	f.failAfterLogin = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	opts := Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Stay:            time.Second,
		Deadline:        10 * time.Second,
		BuildAuthKey:    f.buildKey[:],
	}
	report, err := Enter(t.Context(), opts)
	if err == nil {
		t.Fatal("the run succeeded although the server refused the character")
	}
	f.wait(t)
	if report == nil || !report.InWorld {
		t.Fatalf("the report does not say the character was in the world: %+v", report)
	}
	for _, wire := range []uint32{fakeCMSGLogoutRequest, fakeCMSGLogDisconnect} {
		if !containsWire(f.instWire, wire) {
			t.Fatalf("the failed run did not send wire 0x%06X before closing; sent: % X", wire, f.instWire)
		}
	}
}

// shutdown returns only after both readers have stopped, so Enter never leaves a goroutine behind that could touch
// a closing socket or inflater. A pump that only ends when its context is cancelled proves the wait.
func TestShutdownWaitsForThePumps(t *testing.T) {
	s := &session{events: make(chan event, 1)}
	s.pumpCtx, s.pumpCancel = context.WithCancel(context.Background())
	defer s.pumpCancel()
	finished := make(chan struct{})
	blocking := func(ctx context.Context) (worldconn.Packet, error) {
		<-ctx.Done()
		close(finished)
		return worldconn.Packet{}, ctx.Err()
	}
	s.startPump(sideRealm, blocking)
	time.Sleep(10 * time.Millisecond) // let the pump block in its reader
	s.shutdown(context.Background())
	select {
	case <-finished:
	default:
		t.Fatal("shutdown returned before its pump finished")
	}
}
