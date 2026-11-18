package worldconn

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The vectors in testdata/world-vectors.json come from tools/forevervec/world_vectors.cpp, built against
// ForeverCore 6517af05: the core's HMAC/SHA512/SessionKeyGenerator, its AES and WorldPacketCrypt for both frame
// directions (each one is decrypted again with the other side's function before it is printed), zlib for the
// compression stream, and the server's own constants. Nothing here was produced by this package.
type worldVectors struct {
	KeyData               string `json:"keyData"`
	ServerChallenge       string `json:"serverChallenge"`
	LocalChallenge        string `json:"localChallenge"`
	InstanceChallenge     string `json:"instanceChallenge"`
	BuildAuthKey          string `json:"buildAuthKey"`
	ContinuedKey          string `json:"continuedKey"`
	SessionKey            string `json:"sessionKey"`
	EncryptKey            string `json:"encryptKey"`
	AuthDigest            string `json:"authDigest"`
	ContinuedDigest       string `json:"continuedDigest"`
	EnableEncryptionInput string `json:"enableEncryptionInput"`
	GCM                   []struct {
		Direction  string `json:"direction"`
		Counter    uint64 `json:"counter"`
		Plaintext  string `json:"plaintext"`
		Tag        string `json:"tag"`
		Ciphertext string `json:"ciphertext"`
	} `json:"gcm"`
	FirstEncryptedFrames struct {
		PlainFramesBefore uint32 `json:"plainFramesBefore"`
		Counter           uint64 `json:"counter"`
		Plaintext         string `json:"plaintext"`
		Server            struct {
			Tag        string `json:"tag"`
			Ciphertext string `json:"ciphertext"`
		} `json:"server"`
		Client struct {
			Tag        string `json:"tag"`
			Ciphertext string `json:"ciphertext"`
		} `json:"client"`
	} `json:"firstEncryptedFrames"`
	Compression []struct {
		UncompressedSize  uint32 `json:"uncompressedSize"`
		UncompressedAdler string `json:"uncompressedAdler"`
		CompressedAdler   string `json:"compressedAdler"`
		InnerOpcode       string `json:"innerOpcode"`
		Plaintext         string `json:"plaintext"`
		Compressed        string `json:"compressed"`
	} `json:"compression"`
}

func loadWorldVectors(t *testing.T) worldVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/world-vectors.json")
	if err != nil {
		t.Fatalf("reading the vectors: %v", err)
	}
	var v worldVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsing the vectors: %v", err)
	}
	return v
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

func hexOf(b []byte) string { return strings.ToUpper(hex.EncodeToString(b)) }

func TestKeyDerivationVectors(t *testing.T) {
	v := loadWorldVectors(t)
	keyData := mustHex(t, v.KeyData)
	serverChallenge := mustHex(t, v.ServerChallenge)
	localChallenge := mustHex(t, v.LocalChallenge)
	instanceChallenge := mustHex(t, v.InstanceChallenge)
	if len(keyData) != 64 || len(serverChallenge) != 32 || len(localChallenge) != 32 || len(instanceChallenge) != 32 {
		t.Fatalf("vector inputs: keyData %d, challenges %d/%d/%d", len(keyData), len(serverChallenge), len(localChallenge), len(instanceChallenge))
	}

	sessionKey := sessionKeyFrom(keyData, serverChallenge, localChallenge)
	if got := hexOf(sessionKey[:]); got != v.SessionKey {
		t.Fatalf("session key = %s, the server's code produced %s", got, v.SessionKey)
	}
	encryptKey := encryptKeyFrom(sessionKey[:], localChallenge, serverChallenge)
	if got := hexOf(encryptKey[:]); got != v.EncryptKey {
		t.Fatalf("encryption key = %s, the server's code produced %s", got, v.EncryptKey)
	}
	digest := authDigest(keyData, mustHex(t, v.BuildAuthKey), localChallenge, serverChallenge)
	if got := hexOf(digest[:]); got != v.AuthDigest {
		t.Fatalf("auth digest = %s, the server's code produced %s", got, v.AuthDigest)
	}
	key, err := strconv.ParseUint(strings.TrimPrefix(v.ContinuedKey, "0x"), 16, 64)
	if err != nil {
		t.Fatalf("continued key: %v", err)
	}
	continued := continuedSessionDigest(sessionKey[:], key, localChallenge, instanceChallenge)
	if got := hexOf(continued[:]); got != v.ContinuedDigest {
		t.Fatalf("continued-session digest = %s, the server's code produced %s", got, v.ContinuedDigest)
	}
	enable := enableEncryptionSignatureInput(encryptKey[:])
	if got := hexOf(enable[:]); got != v.EnableEncryptionInput {
		t.Fatalf("encrypted-mode signature input = %s, the server's code produced %s", got, v.EnableEncryptionInput)
	}
}

func TestGCMFrameVectors(t *testing.T) {
	v := loadWorldVectors(t)
	key := mustHex(t, v.EncryptKey)
	var encryptKey [32]byte
	copy(encryptKey[:], key)
	pc, err := newPacketCrypt(encryptKey)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, frame := range v.GCM {
		plaintext := mustHex(t, frame.Plaintext)
		ciphertext := mustHex(t, frame.Ciphertext)
		tagBytes := mustHex(t, frame.Tag)
		if len(tagBytes) != 12 {
			t.Fatalf("%s/%d: tag is %d bytes", frame.Direction, frame.Counter, len(tagBytes))
		}
		var tag [12]byte
		copy(tag[:], tagBytes)
		switch frame.Direction {
		case "server":
			plain, err := pc.open(frame.Counter, ciphertext, tag)
			if err != nil {
				t.Fatalf("server frame %d: %v", frame.Counter, err)
			}
			if !bytes.Equal(plain, plaintext) {
				t.Fatalf("server frame %d decrypted to other bytes", frame.Counter)
			}
		case "client":
			sealed, sealedTag := pc.seal(frame.Counter, plaintext)
			if !bytes.Equal(sealed, ciphertext) || sealedTag != tag {
				t.Fatalf("client frame %d: our AES-GCM does not match the server's AES at counter %d", frame.Counter, frame.Counter)
			}
		default:
			t.Fatalf("unknown direction %q", frame.Direction)
		}
		checked++
	}
	if checked < 8 {
		t.Fatalf("only %d GCM vectors", checked)
	}
	// a tag that is off by one bit must be refused
	for _, frame := range v.GCM {
		if frame.Direction != "server" {
			continue
		}
		ciphertext := mustHex(t, frame.Ciphertext)
		tagBytes := mustHex(t, frame.Tag)
		tagBytes[0] ^= 0x01
		var tag [12]byte
		copy(tag[:], tagBytes)
		if _, err := pc.open(frame.Counter, ciphertext, tag); err == nil {
			t.Fatalf("a corrupted tag decoded (counter %d)", frame.Counter)
		}
		break
	}
}

func TestCompressionVectors(t *testing.T) {
	v := loadWorldVectors(t)
	if len(v.Compression) != 2 {
		t.Fatalf("%d compression vectors, want 2 packets in one stream", len(v.Compression))
	}
	in := wire.NewInflater()
	defer in.Close()
	for i, c := range v.Compression {
		compressed := mustHex(t, c.Compressed)
		body := make([]byte, 12, 12+len(compressed))
		binary.LittleEndian.PutUint32(body[0:4], c.UncompressedSize)
		adlerOf, err := strconv.ParseUint(strings.TrimPrefix(c.UncompressedAdler, "0x"), 16, 32)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		binary.LittleEndian.PutUint32(body[4:8], uint32(adlerOf))
		adler, err := strconv.ParseUint(strings.TrimPrefix(c.CompressedAdler, "0x"), 16, 32)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		binary.LittleEndian.PutUint32(body[8:12], uint32(adler))
		body = append(body, compressed...)
		opcode, payload, err := in.DecompressPacket(body)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		wantOpcode, err := strconv.ParseUint(strings.TrimPrefix(c.InnerOpcode, "0x"), 16, 32)
		if err != nil {
			t.Fatalf("vector %d: %v", i, err)
		}
		if opcode != uint32(wantOpcode) || !bytes.Equal(payload, mustHex(t, c.Plaintext)) {
			t.Fatalf("vector %d: opcode 0x%06X, %d payload bytes", i, opcode, len(payload))
		}
	}
}
