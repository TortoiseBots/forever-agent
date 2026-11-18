// Package bnet is a client for the Forever (Classic 1.60.1, build 70938) login service: the HTTPS login/REST port
// (SRP form login, login ticket) and the Battle.net RPC port (TLS, protobuf frames) up to the realm join answer.
//
// Wire behaviour and every message layout are taken from the ForeverCore sources (commit 6517af05); the file:line
// references live in docs/forever/PROTOCOL-LOGIN.md.
package bnet

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
)

// The SRP6 group parameters of the two Battle.net variants, copied from the server
// (src/common/Cryptography/Authentication/SRP6.cpp:29-33).
const (
	modulusV1 = "86A7F6DEEB306CE519770FE37D556F29944132554DED0BD68205E27F3231FEF5A10108238A3150C59CAF7B0B6478691C13A6ACF5E1B5ADAFD4A943D4A21A142B800E8A55F8BFBAC700EB77A7235EE5A609E350EA9FC19F10D921C2FA832E4461B7125D38D254A0BE873DFC27858ACB3F8B9F258461E4373BC3A6C2A9634324AB"

	modulusV2 = "AC6BDB41324A9A9BF166DE5E1389582FAF72B6651987EE07FC3192943DB56050A37329CBB4A099ED8193E0757767A13DD52312AB4B03310DCD7F48A9DA04FD50E8083969EDB767B0CF6095179A163AB3661A05FBD5FAAAE82918A9962F0B93B855F97993EC975EEAA80D740ADBF4FF747359D041D5C33EA71D281E446B14773BCA97B43A23FB801676BD207A436C6481F1D2B9078717461A5B9D32E688F87748544523B524B0D57D5EA77A2775D2ECFA032CFBDBF52FB3786160279004E57AE6AF874E7303CE53299CCC041C7BC308D82A5698F3A8D0C38271AE35F8E9DBFBB694B5C803D89F7AE435DE236D525F54759B65E372FCD68EF20FA7111F9E4AFF73"
)

var groupGenerator = big.NewInt(2)

// maxChallengeHex bounds a hex integer from the challenge before it is parsed. Both moduli are 128 or 256 bytes
// (256 or 512 hex digits), so anything far larger is not this server's group and does not get to allocate a big.Int.
const maxChallengeHex = 4096

// srpChallenge is the subset of SrpLoginChallenge this client needs.
type srpChallenge struct {
	Version      uint32
	Iterations   uint32
	Modulus      string
	Generator    string
	Username     string
	Salt         []byte
	PublicB      []byte
	HashFunction string
}

// srpClient computes the client half of Battle.net SRP6 exactly as BnetSRP6v1Base/BnetSRP6v2Base verify it
// (src/common/Cryptography/Authentication/SRP6.cpp:151-220).
type srpClient struct {
	n    *big.Int
	g    *big.Int
	size int // byte length of N, i.e. the padding length of every hashed value

	salt []byte
	b    *big.Int // server public
	a    *big.Int // client private
	x    *big.Int
	u    *big.Int
	A    *big.Int
	S    *big.Int
	M1   []byte
	M2   []byte
}

func newSRPClient(c srpChallenge, password string, private *big.Int) (*srpClient, error) {
	if c.HashFunction != "SHA-256" {
		return nil, fmt.Errorf("srp: hash function %q is not SHA-256", c.HashFunction)
	}
	var modulusHex string
	switch c.Version {
	case 1:
		if c.Iterations != 1 {
			return nil, fmt.Errorf("srp: version 1 with %d iterations", c.Iterations)
		}
		modulusHex = modulusV1
	case 2:
		if c.Iterations != 15000 {
			return nil, fmt.Errorf("srp: version 2 with %d iterations", c.Iterations)
		}
		modulusHex = modulusV2
	default:
		return nil, fmt.Errorf("srp: unknown version %d", c.Version)
	}

	if len(c.Modulus) > maxChallengeHex || len(c.Generator) > maxChallengeHex {
		return nil, errors.New("srp: the challenge carries an implausible integer")
	}
	n, ok := new(big.Int).SetString(c.Modulus, 16)
	if !ok || n.Sign() <= 0 {
		return nil, errors.New("srp: the challenge has no usable modulus")
	}
	// both sides must agree on the group, otherwise the proofs cannot match
	if known, _ := new(big.Int).SetString(modulusHex, 16); known.Cmp(n) != 0 {
		return nil, errors.New("srp: the challenge modulus is not the one of that SRP version")
	}
	// the generator is the server's, but only as a check: both Battle.net groups fix g = 2
	// (src/common/Cryptography/Authentication/SRP6.cpp:30,33). Any other value changes k and every proof input,
	// and a value wider than N would make the padding below panic, so it is refused instead of used.
	g := new(big.Int).Set(groupGenerator)
	if c.Generator != "" {
		generated, ok := new(big.Int).SetString(c.Generator, 16)
		if !ok || generated.Cmp(groupGenerator) != 0 {
			return nil, fmt.Errorf("srp: the challenge generator %q is not the group's 2", sanitizeServerText(c.Generator))
		}
	}
	if len(c.Salt) != 32 {
		return nil, fmt.Errorf("srp: salt is %d bytes, want 32", len(c.Salt))
	}
	if len(c.PublicB) == 0 {
		return nil, errors.New("srp: the challenge has no public B")
	}
	size := len(n.Bytes())
	// reject an over-long encoding before SetBytes: the hashed form of B is padded to the modulus size, and
	// FillBytes panics on a value that does not fit.
	if len(c.PublicB) > size {
		return nil, fmt.Errorf("srp: the server's public B is %d bytes, the group is %d", len(c.PublicB), size)
	}
	b := new(big.Int).SetBytes(c.PublicB)
	// the server's own verifier works mod N and refuses A % N == 0 (SRP6.cpp:163-180); B outside [1, N) is not a
	// group element, and using it would also break the padding assumption above.
	if b.Sign() <= 0 || b.Cmp(n) >= 0 {
		return nil, errors.New("srp: the server's public B is not in [1, N)")
	}

	a := private
	if a == nil {
		var err error
		a, err = randomScalar(n)
		if err != nil {
			return nil, err
		}
	}

	s := &srpClient{
		n:    n,
		g:    g,
		size: size,
		salt: c.Salt,
		b:    b,
		a:    a,
	}

	x, err := srpPasswordKey(c.Version, c.Iterations, c.Username, password, c.Salt, n)
	if err != nil {
		return nil, err
	}
	s.x = x
	s.A = new(big.Int).Exp(s.g, s.a, n)

	// k = H(N || g), both padded to the size of N, read big-endian (SRP6.h:196,219)
	k := new(big.Int).SetBytes(digest(s.pad(n), s.pad(g)))
	// u = H(A || B), padded the same way (SRP6.h:203-206)
	s.u = new(big.Int).SetBytes(digest(s.pad(s.A), s.pad(s.b)))

	// S = (B - k * g^x) ^ (a + u*x) mod N
	gx := new(big.Int).Exp(s.g, s.x, n)
	base := new(big.Int).Sub(s.b, new(big.Int).Mul(k, gx))
	base.Mod(base, n)
	exp := new(big.Int).Add(s.a, new(big.Int).Mul(s.u, s.x))
	s.S = new(big.Int).Exp(base, exp, n)

	// M1 = H(A || B || S), M2 = H(A || M1 || S) with the server's "broken" evidence encoding (SRP6.cpp:182-186)
	s.M1 = digest(s.broken(s.A), s.broken(s.b), s.broken(s.S))
	// the server hashes M1 as a BigNumber too, so it gets the same leading-zero padding as A and S
	s.M2 = digest(s.broken(s.A), s.broken(new(big.Int).SetBytes(s.M1)), s.broken(s.S))
	return s, nil
}

// srpPasswordKey is CalculateX of the server: a case-sensitive PBKDF2-HMAC-SHA512 for version 2, a doubled
// SHA-256 read little-endian for version 1 (SRP6.cpp:193-220).
func srpPasswordKey(version, iterations uint32, srpUsername, password string, salt []byte, n *big.Int) (*big.Int, error) {
	switch version {
	case 1:
		// the server uppercases the password for version 1 logins (LoginRESTService.cpp:346-349), so the client must
		password = upperOnlyLatin(password)
		user := srpUsername + ":" + password
		inner := digest([]byte(user))
		outer := digest(salt, inner)
		reverse(outer)
		return new(big.Int).SetBytes(outer), nil
	case 2:
		raw, err := pbkdf2.Key[hash.Hash](sha512.New, srpUsername+":"+password, salt, int(iterations), 64)
		if err != nil {
			return nil, fmt.Errorf("srp: pbkdf2: %w", err)
		}
		x := new(big.Int).SetBytes(raw)
		if raw[0]&0x80 != 0 {
			// the server subtracts a 65-byte big-endian array with the 1 in the last byte, and constructs the
			// BigNumber from it with the default little-endian flag: that is 2^512, not 1 (SRP6.cpp:210-215)
			x.Sub(x, new(big.Int).Lsh(big.NewInt(1), 512))
		}
		x.Mod(x, new(big.Int).Sub(n, big.NewInt(1)))
		return x, nil
	}
	return nil, fmt.Errorf("srp: unknown version %d", version)
}

// verify checks the server evidence M2 of the login answer. The server sends it with BN_bn2hex
// (src/server/bnetserver/REST/LoginRESTService.cpp:364, BigNumber::AsHexStr in
// src/common/Cryptography/BigNumber.cpp:210-216), which prints no leading zero bytes; a digest whose first byte is
// zero therefore arrives as 31 bytes of hex. The decoded value is left-padded back to the digest length before the
// comparison, and a value wider than the digest is refused.
func (s *srpClient) verify(serverM2Hex string) error {
	got, err := decodeServerEvidence(serverM2Hex, len(s.M2))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(got, s.M2) != 1 {
		return errors.New("srp: the server evidence M2 does not match")
	}
	return nil
}

// decodeServerEvidence turns a number-as-hex into its size-byte big-endian form. BN_bn2hex drops leading zeros and
// prints a zero-valued number as "0", so a short or odd-length string is a value, not a malformed digest; anything
// needing more than size bytes is not this digest.
func decodeServerEvidence(text string, size int) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("srp: the server evidence M2 is empty")
	}
	if size <= 0 {
		return nil, errors.New("srp: no digest length to decode the server evidence into")
	}
	out := make([]byte, size)
	pos := size
	for end := len(text); end > 0; {
		start := end - 2
		if start < 0 {
			start = end - 1
		}
		hi, ok := hexNibble(text[start])
		if !ok {
			return nil, fmt.Errorf("srp: the server evidence M2 is not hex (byte %q)", text[start])
		}
		value := hi
		if end-start == 2 {
			lo, ok := hexNibble(text[start+1])
			if !ok {
				return nil, fmt.Errorf("srp: the server evidence M2 is not hex (byte %q)", text[start+1])
			}
			value = hi<<4 | lo
		}
		if pos == 0 {
			return nil, fmt.Errorf("srp: the server evidence M2 is longer than %d bytes", size)
		}
		pos--
		out[pos] = value
		end = start
	}
	return out, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func (s *srpClient) publicAHex() string    { return strings.ToUpper(s.A.Text(16)) }
func (s *srpClient) evidenceM1Hex() string { return strings.ToUpper(hex.EncodeToString(s.M1)) }

// pad left-pads a value to the modulus length, the form every hashed pair in SRP uses.
func (s *srpClient) pad(v *big.Int) []byte {
	out := make([]byte, s.size)
	v.FillBytes(out)
	return out
}

// broken is GetBrokenEvidenceVector: big-endian, (bits+8)>>3 bytes, so a value whose bit length is a multiple of 8
// gets one leading zero byte (SRP6.cpp:182-186).
func (s *srpClient) broken(v *big.Int) []byte {
	out := make([]byte, (v.BitLen()+8)>>3)
	v.FillBytes(out)
	return out
}

func digest(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// upperOnlyLatin mirrors Utf8ToUpperOnlyLatin: it folds only the ASCII letters, so accented and non-Latin
// characters are left untouched (src/common/Utilities/Util.h:121-128,307).
func upperOnlyLatin(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

func reverse(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

// randomScalar picks the client private a in [1, N-1), 256 bits of randomness like a real client.
func randomScalar(n *big.Int) (*big.Int, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// a broken RNG must not produce a weak key, so the caller gets an error instead of a panic
		return nil, fmt.Errorf("bnet: no randomness: %w", err)
	}
	a := new(big.Int).SetBytes(buf)
	a.Mod(a, n)
	if a.Sign() == 0 {
		a.SetInt64(1)
	}
	return a, nil
}
