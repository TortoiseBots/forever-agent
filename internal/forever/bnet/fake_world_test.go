package bnet

// A fake world socket for the build probe. The realm list and the world server keep different build knowledge: the
// realm list can accept several builds while the world server has a build_info row for one and answers
// ERROR_BAD_VERSION for the rest (WorldSocket.cpp:739-763). This fake speaks just enough of the handshake for
// worldconn.Authenticate: the banners, SMSG_AUTH_CHALLENGE, CMSG_AUTH_SESSION, and either the refusal or
// SMSG_ENTER_ENCRYPTED_MODE, the ack and an encrypted SMSG_AUTH_RESPONSE. The wire numbers are the ones the client
// builds translate to (wire.OpcodeTable), not hardcoded ones, so a wrong table shows up as a protocol error.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// fakeWorldChallenge is the 32-byte SMSG_AUTH_CHALLENGE the fake sends.
var fakeWorldChallenge = [32]byte{
	0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF,
	0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF, 0x10, 0x32, 0x54, 0x76, 0x98, 0xBA, 0xDC, 0xFE,
}

// fakeWorldAttempt is what the probe's callback handed the fake for the next connection.
type fakeWorldAttempt struct {
	build        uint32
	clientSecret []byte
	joinSecret   []byte
}

// fakeWorld refuses the builds refuse names and authenticates every other one. It records the build of every
// connection in order, so a test can see the probe tried the builds it says it tried.
type fakeWorld struct {
	ln     net.Listener
	refuse func(build uint32) bool

	mu       sync.Mutex
	next     *fakeWorldAttempt
	attempts []uint32
	errs     chan error
	closed   bool
}

func newFakeWorld(t *testing.T, refuse func(build uint32) bool) *fakeWorld {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w := &fakeWorld{ln: ln, refuse: refuse, errs: make(chan error, 8)}
	go w.serve()
	t.Cleanup(w.close)
	return w
}

func (w *fakeWorld) close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	_ = w.ln.Close()
}

func (w *fakeWorld) addr() string { return w.ln.Addr().String() }

// prepare hands the fake the next attempt's build and both key halves, then reads what the connection does.
func (w *fakeWorld) prepare(build uint32, clientSecret, joinSecret []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next = &fakeWorldAttempt{build: build, clientSecret: append([]byte(nil), clientSecret...), joinSecret: append([]byte(nil), joinSecret...)}
}

func (w *fakeWorld) take() (*fakeWorldAttempt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := w.next
	w.next = nil
	if a == nil {
		return nil, errors.New("fake world: a connection arrived without a prepared attempt")
	}
	w.attempts = append(w.attempts, a.build)
	return a, nil
}

func (w *fakeWorld) builds() []uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]uint32(nil), w.attempts...)
}

func (w *fakeWorld) serve() {
	for {
		nc, err := w.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
			if err := w.handle(nc); err != nil {
				select {
				case w.errs <- err:
				default:
				}
			}
		}()
	}
}

// wait checks the fake saw no protocol error and returns the builds it saw.
func (w *fakeWorld) wait(t *testing.T) []uint32 {
	t.Helper()
	// the last connection's handler may still be finishing when the probe returns; give it a moment, then drain
	time.Sleep(50 * time.Millisecond)
	for {
		select {
		case err := <-w.errs:
			t.Fatalf("fake world: %v", err)
		default:
			return w.builds()
		}
	}
}

func (w *fakeWorld) handle(nc net.Conn) error {
	defer nc.Close()
	att, err := w.take()
	if err != nil {
		return err
	}
	table := wire.NewOpcodeTable(att.build, att.build)

	banner := make([]byte, len(worldconn.ServerHello))
	copy(banner, worldconn.ServerHello)
	if err := writeAll(nc, banner); err != nil {
		return err
	}
	clientBanner := make([]byte, len(worldconn.ClientHello))
	if _, err := io.ReadFull(nc, clientBanner); err != nil {
		return fmt.Errorf("reading the client banner: %w", err)
	}
	if string(clientBanner) != worldconn.ClientHello {
		return fmt.Errorf("client banner %q", clientBanner)
	}

	challengePayload := make([]byte, 65)
	copy(challengePayload[32:], fakeWorldChallenge[:])
	if err := sendPlain(nc, table.TranslateServerCore(wire.SMSGAuthChallenge), challengePayload); err != nil {
		return err
	}

	auth, err := readPlain(nc)
	if err != nil {
		return fmt.Errorf("reading CMSG_AUTH_SESSION: %w", err)
	}
	if opcode, _ := table.WireForClient(wire.CMSGAuthSession); auth.opcode != opcode {
		return fmt.Errorf("CMSG_AUTH_SESSION: wire 0x%06X, want 0x%06X", auth.opcode, opcode)
	}
	if len(auth.body) < 52 {
		return fmt.Errorf("CMSG_AUTH_SESSION body is %d bytes", len(auth.body))
	}
	localChallenge := auth.body[20:52]

	if w.refuse(att.build) {
		refusal := make([]byte, 4)
		binary.LittleEndian.PutUint32(refusal, 0x1C) // ERROR_BAD_VERSION
		return sendPlain(nc, table.TranslateServerCore(wire.SMSGAuthResponse), refusal)
	}

	// The encrypted-mode packet is 69 bytes: region group, the 64-byte signature (not verifiable here either) and
	// the enabled bit.
	enter := make([]byte, 69)
	enter[68] = 0x80
	if err := sendPlain(nc, table.TranslateServerCore(wire.SMSGEnterEncryptedMode), enter); err != nil {
		return err
	}
	ack, err := readPlain(nc)
	if err != nil {
		return fmt.Errorf("reading the encrypted-mode ack: %w", err)
	}
	if opcode, _ := table.WireForClient(wire.CMSGEnterEncryptedModeAck); ack.opcode != opcode {
		return fmt.Errorf("CMSG_ENTER_ENCRYPTED_MODE_ACK: wire 0x%06X, want 0x%06X", ack.opcode, opcode)
	}

	keyData := append(append([]byte(nil), att.clientSecret...), att.joinSecret...)
	sessionKey := fakeWorldSessionKey(keyData, fakeWorldChallenge[:], localChallenge)
	encryptKey := fakeWorldEncryptKey(sessionKey[:], localChallenge, fakeWorldChallenge[:])

	body := make([]byte, 4+4)
	binary.LittleEndian.PutUint32(body, table.TranslateServerCore(wire.SMSGAuthResponse))
	// result stays AUTH_OK
	if err := sendEncrypted(nc, encryptKey, 2, body); err != nil {
		return err
	}
	// hold the socket open until the client closes it: the probe hands it on to the entry
	_, _ = io.Copy(io.Discard, nc)
	return nil
}

// fakeWorldSessionKey mirrors worldconn's sessionKeyFrom (WorldSocket.cpp:800-806, SessionKeyGenerator.h:28-52).
func fakeWorldSessionKey(keyData, serverChallenge, localChallenge []byte) [40]byte {
	seed := fakeHMAC(fakeSHA(keyData), serverChallenge, localChallenge, worldconnSessionKeySeed())
	half := len(seed) / 2
	o1 := fakeSHA(seed[:half])
	o2 := fakeSHA(seed[half:])
	o0 := fakeSHA(o1, make([]byte, sha512.Size), o2)
	var out [40]byte
	copy(out[:], o0[:40])
	return out
}

// fakeWorldEncryptKey mirrors worldconn's encryptKeyFrom (WorldSocket.cpp:808-816).
func fakeWorldEncryptKey(sessionKey, localChallenge, serverChallenge []byte) [32]byte {
	digest := fakeHMAC(sessionKey, localChallenge, serverChallenge, worldconnEncryptionKeySeed())
	var out [32]byte
	copy(out[:], digest[:32])
	return out
}

func worldconnSessionKeySeed() []byte {
	return []byte{
		0xE8, 0x1E, 0x8B, 0x59, 0x27, 0x62, 0x1E, 0xAA, 0x86, 0x15, 0x18, 0xEA, 0xC0, 0xBF, 0x66, 0x8C,
		0x6D, 0xBF, 0x83, 0x93, 0xBC, 0xAA, 0x80, 0x52, 0x5B, 0x1E, 0xDC, 0x23, 0xA0, 0x12, 0xB7, 0x50,
	}
}

func worldconnEncryptionKeySeed() []byte {
	return []byte{
		0x71, 0xC9, 0xED, 0x5A, 0xA7, 0x0E, 0x4D, 0xFF, 0x4C, 0x36, 0xA6, 0x5A, 0x3E, 0x46, 0x8A, 0x4A,
		0x5D, 0xA1, 0x48, 0xC8, 0x30, 0x47, 0x4A, 0xDE, 0xF6, 0x0D, 0x6C, 0xBE, 0x6F, 0xE4, 0x55, 0x73,
	}
}

func fakeSHA(parts ...[]byte) []byte {
	h := sha512.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func fakeHMAC(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha512.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

type fakeWorldFrame struct {
	opcode uint32
	body   []byte // without the opcode
}

func writeAll(nc net.Conn, p []byte) error {
	_, err := nc.Write(p)
	return err
}

func sendPlain(nc net.Conn, opcode uint32, payload []byte) error {
	body := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(body, opcode)
	copy(body[4:], payload)
	frame := append(wire.EncodeHeader(wire.Header{Size: uint32(len(body))}), body...)
	return writeAll(nc, frame)
}

func readPlain(nc net.Conn) (fakeWorldFrame, error) {
	var headerBytes [wire.HeaderSize]byte
	if _, err := io.ReadFull(nc, headerBytes[:]); err != nil {
		return fakeWorldFrame{}, err
	}
	header, err := wire.DecodeHeader(headerBytes[:])
	if err != nil {
		return fakeWorldFrame{}, err
	}
	if header.Size < 4 || header.Size > wire.MaxFrame {
		return fakeWorldFrame{}, fmt.Errorf("frame size %d", header.Size)
	}
	body := make([]byte, header.Size)
	if _, err := io.ReadFull(nc, body); err != nil {
		return fakeWorldFrame{}, err
	}
	return fakeWorldFrame{opcode: binary.LittleEndian.Uint32(body), body: body[4:]}, nil
}

func sendEncrypted(nc net.Conn, key [32]byte, counter uint64, plaintext []byte) error {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCMWithTagSize(block, 12)
	if err != nil {
		return err
	}
	var iv [12]byte
	for i := range 8 {
		iv[i] = byte(counter >> (8 * i))
	}
	const serverMagic uint32 = 0x52565253 // "SRVR"
	binary.LittleEndian.PutUint32(iv[8:], serverMagic)
	sealed := aead.Seal(nil, iv[:], plaintext, nil)
	n := len(sealed) - aead.Overhead()
	header := wire.Header{Size: uint32(n)}
	copy(header.Tag[:], sealed[n:])
	frame := append(wire.EncodeHeader(header), sealed[:n]...)
	return writeAll(nc, frame)
}
