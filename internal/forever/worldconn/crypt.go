package worldconn

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

// The IV layout of WorldPacketCrypt: uint64 counter (little-endian) || uint32 magic, where "CLNT" (0x544E4C43) is
// the client -> server direction and "SRVR" (0x52565253) the server -> client one (WorldPacketCrypt.cpp:38-84).
// The counter counts EVERY frame in that direction, encrypted or not, and both sides start at zero, so the first
// encrypted frame of each direction uses counter 2 (AUTH_SESSION and the encrypted-mode ACK came before it).
const (
	clientMagic uint32 = 0x544E4C43
	serverMagic uint32 = 0x52565253
)

// packetCrypt is the AES-256-GCM context of one connection: one key for both directions, the counter is the
// caller's (each direction has its own).
type packetCrypt struct {
	aead cipher.AEAD
}

func newPacketCrypt(key [32]byte) (*packetCrypt, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("worldconn: AES key: %w", err)
	}
	aead, err := cipher.NewGCMWithTagSize(block, 12) // AES::TAG_SIZE_BYTES
	if err != nil {
		return nil, fmt.Errorf("worldconn: AES-GCM: %w", err)
	}
	return &packetCrypt{aead: aead}, nil
}

func makeIV(counter uint64, magic uint32) [12]byte {
	var out [12]byte
	for i := range 8 {
		out[i] = byte(counter >> (8 * i))
	}
	out[8] = byte(magic)
	out[9] = byte(magic >> 8)
	out[10] = byte(magic >> 16)
	out[11] = byte(magic >> 24)
	return out
}

// seal encrypts plaintext (the wire opcode and payload) for the client -> server direction at counter and returns
// the ciphertext and its 12-byte tag.
func (c *packetCrypt) seal(counter uint64, plaintext []byte) (ciphertext []byte, tag [12]byte) {
	iv := makeIV(counter, clientMagic)
	sealed := c.aead.Seal(nil, iv[:], plaintext, nil)
	n := len(sealed) - c.aead.Overhead()
	copy(tag[:], sealed[n:])
	return sealed[:n], tag
}

// open decrypts and verifies one server -> client frame at counter.
func (c *packetCrypt) open(counter uint64, ciphertext []byte, tag [12]byte) ([]byte, error) {
	iv := makeIV(counter, serverMagic)
	buf := make([]byte, 0, len(ciphertext)+len(tag))
	buf = append(buf, ciphertext...)
	buf = append(buf, tag[:]...)
	plain, err := c.aead.Open(nil, iv[:], buf, nil)
	if err != nil {
		return nil, fmt.Errorf("worldconn: packet failed its AES-GCM integrity check (counter %d)", counter)
	}
	return plain, nil
}
