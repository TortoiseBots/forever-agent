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

func loadVectors(t *testing.T) []srpVector {
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

func mustHex(t *testing.T, s string) []byte {
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
			if got := s.serverEvidenceM2Hex(); got != strings.ToUpper(v.ServerEvidenceM2) {
				t.Errorf("M2 = %s, want %s", got, v.ServerEvidenceM2)
			}
			if err := s.verify(v.ServerEvidenceM2); err != nil {
				t.Errorf("verify: %v", err)
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
