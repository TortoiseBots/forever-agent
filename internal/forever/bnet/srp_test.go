package bnet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
)

// srpVector is one entry of testdata/srp-vectors.json, produced by the server's own SRP6 code (see testdata/README.md).
type srpVector struct {
	Name             string `json:"name"`
	Version          uint32 `json:"version"`
	Iterations       uint32 `json:"iterations"`
	Login            string `json:"login"`
	Password         string `json:"password"`
	SRPUsername      string `json:"srpUsername"`
	Modulus          string `json:"modulus"`
	Generator        string `json:"generator"`
	Salt             string `json:"salt"`
	Verifier         string `json:"verifier"`
	ClientPrivate    string `json:"clientPrivate"`
	ClientPublicA    string `json:"clientPublicA"`
	ServerPublicB    string `json:"serverPublicB"`
	SessionS         string `json:"sessionS"`
	ClientEvidenceM1 string `json:"clientEvidenceM1"`
	ServerEvidenceM2 string `json:"serverEvidenceM2"`
}

func loadVectors(t testing.TB) []srpVector {
	t.Helper()
	data, err := os.ReadFile("testdata/srp-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []srpVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) == 0 {
		t.Fatal("no SRP vectors")
	}
	return vectors
}

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestSRPVectors reproduces the server's vectors: the password derivation (and therefore the verifier), A, S, M1 and
// M2 for the same salt, B and client private must be bit-identical.
func TestSRPVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			challenge := srpChallenge{
				Version:      v.Version,
				Iterations:   v.Iterations,
				Modulus:      v.Modulus,
				Generator:    v.Generator,
				Username:     v.SRPUsername,
				Salt:         mustHex(t, v.Salt),
				PublicB:      mustHex(t, v.ServerPublicB),
				HashFunction: "SHA-256",
			}
			private, ok := new(big.Int).SetString(v.ClientPrivate, 16)
			if !ok {
				t.Fatal("bad client private")
			}
			s, err := newSRPClient(challenge, v.Password, private)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.publicAHex(); got != strings.ToUpper(v.ClientPublicA) {
				t.Errorf("A = %s, want %s", got, v.ClientPublicA)
			}
			if got := strings.ToUpper(hex.EncodeToString(s.S.Bytes())); got != strings.ToUpper(v.SessionS) {
				t.Errorf("S = %s, want %s", got, v.SessionS)
			}
			if got := s.evidenceM1Hex(); got != strings.ToUpper(v.ClientEvidenceM1) {
				t.Errorf("M1 = %s, want %s", got, v.ClientEvidenceM1)
			}
			// the server sends M2 as the number's hex (BigNumber::AsHexStr), which drops leading zero bytes, so
			// the vector keeps exactly that text and verify() must accept it
			if err := s.verify(v.ServerEvidenceM2); err != nil {
				t.Errorf("verify stripped M2: %v", err)
			}

			// the verifier the server stored for this salt and password: v = g^x mod N, little-endian
			x, err := srpPasswordKey(v.Version, v.Iterations, v.SRPUsername, v.Password, challenge.Salt, s.n)
			if err != nil {
				t.Fatal(err)
			}
			verifier := new(big.Int).Exp(s.g, x, s.n).Bytes()
			want := mustHex(t, v.Verifier)
			for len(verifier) < len(want) {
				verifier = append([]byte{0}, verifier...)
			}
			reverse(verifier)
			if !bytes.Equal(verifier, want) {
				t.Errorf("verifier = %X, want %X", verifier, want)
			}
		})
	}
}

// TestSRPWrongPassword makes sure a wrong password produces different evidence (and therefore no accepted login).
func TestSRPWrongPassword(t *testing.T) {
	for _, v := range loadVectors(t) {
		if v.Version != 2 {
			continue
		}
		challenge := srpChallenge{
			Version: v.Version, Iterations: v.Iterations, Modulus: v.Modulus, Generator: v.Generator,
			Username: v.SRPUsername, Salt: mustHex(t, v.Salt), PublicB: mustHex(t, v.ServerPublicB), HashFunction: "SHA-256",
		}
		private, _ := new(big.Int).SetString(v.ClientPrivate, 16)
		s, err := newSRPClient(challenge, v.Password+"x", private)
		if err != nil {
			t.Fatal(err)
		}
		if s.evidenceM1Hex() == strings.ToUpper(v.ClientEvidenceM1) {
			t.Error("a wrong password produced the vector's M1")
		}
	}
}

// TestSRPV1PasswordIsUpperLatin pins the v1 password rule: the server uppercases the password for version 1 logins
// (LoginRESTService.cpp:346-349), so "vector-password" and "VECTOR-PASSWORD" must give the same x.
func TestSRPV1PasswordIsUpperLatin(t *testing.T) {
	n, _ := new(big.Int).SetString(modulusV1, 16)
	salt := bytes.Repeat([]byte{7}, 32)
	lower, err := srpPasswordKey(1, 1, "USER", upperOnlyLatin("vector-password"), salt, n)
	if err != nil {
		t.Fatal(err)
	}
	upper, err := srpPasswordKey(1, 1, "USER", "VECTOR-PASSWORD", salt, n)
	if err != nil {
		t.Fatal(err)
	}
	if lower.Cmp(upper) != 0 {
		t.Error("v1 password case is not folded")
	}
}

// TestSRPRejectsWrongGroup refuses a challenge whose modulus is not the group of its version.
func TestSRPRejectsWrongGroup(t *testing.T) {
	v := loadVectors(t)[0]
	other := modulusV1
	if v.Version == 1 {
		other = modulusV2
	}
	_, err := newSRPClient(srpChallenge{
		Version: v.Version, Iterations: v.Iterations, Modulus: other, Generator: v.Generator,
		Username: v.SRPUsername, Salt: mustHex(t, v.Salt), PublicB: mustHex(t, v.ServerPublicB), HashFunction: "SHA-256",
	}, v.Password, nil)
	if err == nil {
		t.Fatal("a modulus of the other version was accepted")
	}
}

// clientFromVector builds the client half of one vector.
func clientFromVector(t testing.TB, v srpVector) *srpClient {
	t.Helper()
	private, ok := new(big.Int).SetString(v.ClientPrivate, 16)
	if !ok {
		t.Fatal("bad client private")
	}
	s, err := newSRPClient(srpChallenge{
		Version: v.Version, Iterations: v.Iterations, Modulus: v.Modulus, Generator: v.Generator,
		Username: v.SRPUsername, Salt: mustHex(t, v.Salt), PublicB: mustHex(t, v.ServerPublicB),
		HashFunction: "SHA-256",
	}, v.Password, private)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSRPVerifyStrippedM2 pins bug 1: the server prints M2 with BN_bn2hex, so a digest whose first byte is zero
// arrives with fewer than 64 hex characters and must still verify.
func TestSRPVerifyStrippedM2(t *testing.T) {
	var chosen *srpClient
	var text string
	for _, v := range loadVectors(t) {
		if len(v.ServerEvidenceM2) < 64 {
			chosen = clientFromVector(t, v)
			text = v.ServerEvidenceM2
			break
		}
	}
	if chosen == nil {
		t.Fatal("no vector whose M2 lost a leading zero byte")
	}
	if err := chosen.verify(text); err != nil {
		t.Fatalf("the server's stripped M2 %q was rejected: %v", text, err)
	}
	if err := chosen.verify(strings.ToLower(text)); err != nil {
		t.Fatalf("lowercase M2 was rejected: %v", err)
	}
	if err := chosen.verify(text + "00"); err == nil {
		t.Fatal("an M2 longer than the digest was accepted")
	}
	if err := chosen.verify(strings.Repeat("0", 65)); err == nil {
		t.Fatal("a 33-byte M2 was accepted")
	}
	if err := chosen.verify(""); err == nil {
		t.Fatal("an empty M2 was accepted")
	}
	if err := chosen.verify("zz"); err == nil {
		t.Fatal("non-hex M2 was accepted")
	}
	// BN_bn2hex prints a zero value as "0", an odd-length value, not a malformed 32-byte encoding
	if got, err := decodeServerEvidence("0", 32); err != nil || len(got) != 32 || got[31] != 0 {
		t.Fatalf("decode \"0\": %d bytes, %v", len(got), err)
	}
}

// TestSRPVectorEdgeCases checks that the vector file really covers the evidence edges the search was asked for.
func TestSRPVectorEdgeCases(t *testing.T) {
	edges := []string{"a-leading-zero", "b-leading-zero", "s-leading-zero", "bit-length-multiple-of-8", "m2-leading-zero"}
	found := map[uint32]map[string]bool{1: {}, 2: {}}
	for _, v := range loadVectors(t) {
		for _, edge := range edges {
			if !strings.HasSuffix(v.Name, edge) {
				continue
			}
			found[v.Version][edge] = true
			s := clientFromVector(t, v)
			size := s.size
			a, _ := new(big.Int).SetString(v.ClientPublicA, 16)
			b, _ := new(big.Int).SetString(v.ServerPublicB, 16)
			sv, _ := new(big.Int).SetString(v.SessionS, 16)
			switch edge {
			case "a-leading-zero":
				if a.BitLen() > 8*(size-1) {
					t.Errorf("%s: A has no leading zero byte", v.Name)
				}
			case "b-leading-zero":
				if b.BitLen() > 8*(size-1) {
					t.Errorf("%s: B has no leading zero byte", v.Name)
				}
			case "s-leading-zero":
				if sv.BitLen() > 8*(size-1) {
					t.Errorf("%s: S has no leading zero byte", v.Name)
				}
			case "bit-length-multiple-of-8":
				if a.BitLen()%8 != 0 && b.BitLen()%8 != 0 && sv.BitLen()%8 != 0 {
					t.Errorf("%s: no evidence value has a bit length that is a multiple of 8", v.Name)
				}
			case "m2-leading-zero":
				if len(v.ServerEvidenceM2) >= 2*len(s.M2) {
					t.Errorf("%s: M2 has no leading zero byte (%d hex chars)", v.Name, len(v.ServerEvidenceM2))
				}
			}
		}
	}
	for _, version := range []uint32{1, 2} {
		for _, edge := range edges {
			if !found[version][edge] {
				t.Errorf("no v%d vector for %s", version, edge)
			}
		}
	}
}

// TestSRPRejectsHostileChallenges feeds the challenge a broken or hostile server could send. Every one either gets
// an error or a well-formed client; none may panic in the big-integer code.
func TestSRPRejectsHostileChallenges(t *testing.T) {
	v := loadVectors(t)[0]
	n, ok := new(big.Int).SetString(modulusV2, 16)
	if !ok {
		t.Fatal("bad modulus")
	}
	size := len(n.Bytes())
	good := srpChallenge{
		Version: 2, Iterations: 15000, Modulus: modulusV2, Generator: "02",
		Username: v.SRPUsername, Salt: mustHex(t, v.Salt), PublicB: mustHex(t, v.ServerPublicB),
		HashFunction: "SHA-256",
	}
	cases := []struct {
		name   string
		mutate func(*srpChallenge)
	}{
		{"generator 3", func(c *srpChallenge) { c.Generator = "03" }},
		{"generator wider than N", func(c *srpChallenge) { c.Generator = "0" + strings.Repeat("FF", size) }},
		{"generator over-long text", func(c *srpChallenge) { c.Generator = strings.Repeat("0", maxChallengeHex+1) }},
		{"generator not hex", func(c *srpChallenge) { c.Generator = "two" }},
		{"empty B", func(c *srpChallenge) { c.PublicB = nil }},
		{"B zero", func(c *srpChallenge) { c.PublicB = make([]byte, size) }},
		{"B zero without padding", func(c *srpChallenge) { c.PublicB = []byte{0} }},
		{"B equals N", func(c *srpChallenge) { c.PublicB = n.Bytes() }},
		{"B above N", func(c *srpChallenge) { c.PublicB = new(big.Int).Add(n, big.NewInt(1)).Bytes() }},
		{"B all ones", func(c *srpChallenge) { c.PublicB = bytes.Repeat([]byte{0xFF}, size) }},
		{"B one byte too long", func(c *srpChallenge) { c.PublicB = append([]byte{0}, n.Bytes()...) }},
		{"B over-long text", func(c *srpChallenge) { c.PublicB = bytes.Repeat([]byte{0xFF}, 4096) }},
		{"salt 31 bytes", func(c *srpChallenge) { c.Salt = make([]byte, 31) }},
		{"salt 33 bytes", func(c *srpChallenge) { c.Salt = make([]byte, 33) }},
		{"modulus over-long text", func(c *srpChallenge) { c.Modulus = strings.Repeat("0", maxChallengeHex+1) }},
		{"modulus not hex", func(c *srpChallenge) { c.Modulus = "nope" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			challenge := good
			c.mutate(&challenge)
			if _, err := newSRPClient(challenge, v.Password, nil); err == nil {
				t.Errorf("%s was accepted", c.name)
			}
		})
	}

	// and the values that are legal but easy to get wrong: B = 1, zero-padded within the group, lowercase hex
	one := make([]byte, size)
	one[size-1] = 1
	challenge := good
	challenge.Generator = "2"
	challenge.PublicB = one
	if _, err := newSRPClient(challenge, v.Password, nil); err != nil {
		t.Errorf("B = 1 in the group was rejected: %v", err)
	}
}

// FuzzNewSRPClient keeps the challenge parser honest: every byte string a server could send either becomes an error
// or a usable client, never a panic.
func FuzzNewSRPClient(f *testing.F) {
	for _, v := range loadVectors(f) {
		f.Add(v.Version, []byte(v.Generator), mustHex(f, v.Salt), mustHex(f, v.ServerPublicB))
	}
	f.Fuzz(func(t *testing.T, version uint32, generator, salt, publicB []byte) {
		c := srpChallenge{
			Version: version, Iterations: 1, Modulus: modulusV1, Generator: string(generator),
			Username: "U", Salt: salt, PublicB: publicB, HashFunction: "SHA-256",
		}
		if version == 2 {
			c.Iterations = 15000
			c.Modulus = modulusV2
		}
		_, _ = newSRPClient(c, "p", nil)
	})
}
