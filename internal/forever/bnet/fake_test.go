package bnet

// A fake login service for the whole sequence: an HTTPS REST endpoint that speaks the SRP exchange of the server
// (verifier, B, M1 check, M2) and a TLS listener that speaks the RPC frame format. No live server, and no skipped
// certificate check: the test issues its own CA and hands the client that CA and the certificate name.

import (
	"bytes"
	"compress/zlib"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

const fakeServerName = "login.test"

// fakeCert makes a CA and a leaf for fakeServerName and returns the CA in PEM form plus the leaf's key pair.
func fakeCert(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: fakeServerName},
		DNSNames:     []string{fakeServerName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return caPEM, tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}

// compressJSON mirrors GameUtilities::CompressJson: uint32 LE length (with the trailing NUL), zlib of prefix+json+NUL.
func compressJSON(t *testing.T, prefix string, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(prefix), body...)
	raw = append(raw, 0)
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 4, 4+buf.Len())
	binary.LittleEndian.PutUint32(out, uint32(len(raw)))
	return append(out, buf.Bytes()...)
}

// The fake server's arithmetic is its own: it must not call the client's digest/pad/broken helpers, or a bug the
// two share would stay invisible to the end-to-end test. These four functions are the whole server-side primitive
// set; TestFakeEvidenceMatchesVectors pins them against the C++-produced vectors.

func fakeSHA256(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// fakeBroken is the server's evidence input: big-endian, (bits+8)>>3 bytes (SRP6.cpp:182-186).
func fakeBroken(v *big.Int) []byte {
	out := make([]byte, (v.BitLen()+8)>>3)
	v.FillBytes(out)
	return out
}

func fakePad(v *big.Int, size int) []byte {
	out := make([]byte, size)
	v.FillBytes(out)
	return out
}

func fakeEvidenceM1(a, b, s *big.Int) []byte {
	return fakeSHA256(fakeBroken(a), fakeBroken(b), fakeBroken(s))
}

func fakeEvidenceM2(a *big.Int, m1 *big.Int, s *big.Int) []byte {
	return fakeSHA256(fakeBroken(a), fakeBroken(m1), fakeBroken(s))
}

func fakeReverse(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

// fakeSRP is the server half of SRP6: the verifier of a vector account and a fixed b. When vector is set, verify
// compares the client's evidence with that C++-produced vector instead of recomputing it.
type fakeSRP struct {
	n, g, k, v, b, B *big.Int
	salt             []byte
	username         string
	password         string
	version          uint32
	iterations       uint32
	vector           *srpVector
}

func newFakeSRP(t *testing.T) *fakeSRP {
	t.Helper()
	var vectors []srpVector
	data, err := os.ReadFile("testdata/srp-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	var chosen *srpVector
	for i := range vectors {
		if vectors[i].Version == 2 {
			chosen = &vectors[i]
			break
		}
	}
	if chosen == nil {
		t.Fatal("no v2 vector")
	}
	f := &fakeSRP{
		n:          new(big.Int).SetBytes(mustHex(t, chosen.Modulus)),
		g:          new(big.Int).SetInt64(2),
		salt:       mustHex(t, chosen.Salt),
		username:   chosen.SRPUsername,
		password:   chosen.Password,
		version:    2,
		iterations: 15000,
	}
	size := len(f.n.Bytes())
	f.k = new(big.Int).SetBytes(fakeSHA256(fakePad(f.n, size), fakePad(f.g, size)))
	verifier := mustHex(t, chosen.Verifier) // little-endian on disk
	fakeReverse(verifier)
	f.v = new(big.Int).SetBytes(verifier)
	f.b = new(big.Int).SetBytes([]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88})
	f.B = new(big.Int).Add(new(big.Int).Exp(f.g, f.b, f.n), new(big.Int).Mul(f.k, f.v))
	f.B.Mod(f.B, f.n)
	return f
}

// challengeJSON is the SrpLoginChallenge answer written out by the fake itself (not through the client's struct).
func (f *fakeSRP) challengeJSON() map[string]any {
	return map[string]any{
		"version":       f.version,
		"iterations":    f.iterations,
		"modulus":       strings.ToUpper(f.n.Text(16)),
		"generator":     "02",
		"hash_function": "SHA-256",
		"username":      f.username,
		"salt":          hex.EncodeToString(f.salt),
		"public_B":      strings.ToUpper(f.B.Text(16)),
	}
}

// verify checks the client evidence and returns the server evidence M2 as the text BN_bn2hex produces (no leading
// zero bytes), which is what the real server sends.
func (f *fakeSRP) verify(publicAHex, m1Hex string) (string, error) {
	a, ok := new(big.Int).SetString(publicAHex, 16)
	if !ok || a.Sign() == 0 || a.Cmp(f.n) >= 0 {
		return "", fmt.Errorf("bad A")
	}
	clientM1, ok := new(big.Int).SetString(m1Hex, 16)
	if !ok {
		return "", fmt.Errorf("bad M1")
	}
	if f.vector != nil {
		// the pinned case: the client must reproduce the C++ vector bit for bit, and the answer is the vector's
		// own M2 text (BN_bn2hex form), not a recomputation
		wantA, _ := new(big.Int).SetString(f.vector.ClientPublicA, 16)
		wantM1, _ := new(big.Int).SetString(f.vector.ClientEvidenceM1, 16)
		if a.Cmp(wantA) != 0 {
			return "", fmt.Errorf("A is not the vector's")
		}
		if clientM1.Cmp(wantM1) != 0 {
			return "", fmt.Errorf("client evidence is not the vector's")
		}
		return f.vector.ServerEvidenceM2, nil
	}
	size := len(f.n.Bytes())
	u := new(big.Int).SetBytes(fakeSHA256(fakePad(a, size), fakePad(f.B, size)))
	s := new(big.Int).Exp(f.v, u, f.n)
	s.Mul(s, a)
	s.Mod(s, f.n)
	s.Exp(s, f.b, f.n)

	ourM1 := new(big.Int).SetBytes(fakeEvidenceM1(a, f.B, s))
	if ourM1.Cmp(clientM1) != 0 {
		return "", fmt.Errorf("client evidence does not match")
	}
	m2 := new(big.Int).SetBytes(fakeEvidenceM2(a, ourM1, s))
	return strings.ToUpper(m2.Text(16)), nil
}

// fakeLogonRequest is the subset of authentication.v1.LogonRequest the fake checks.
type fakeLogonRequest struct {
	program, platform, locale string
	build                     uint64
	ticket                    []byte
}

// parseFakeLogonRequest walks the request field by field (program 1, platform 2, locale 3, application_version 6,
// cached_web_credentials 12). It uses the wire format directly, so it does not inherit a bug in the client's
// encoder or struct tags.
func parseFakeLogonRequest(b []byte) (fakeLogonRequest, error) {
	var out fakeLogonRequest
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return out, protowire.ParseError(n)
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			b = b[m:]
			if num == 6 {
				out.build = v
			}
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return out, protowire.ParseError(m)
			}
			b = b[m:]
			switch num {
			case 1:
				out.program = string(v)
			case 2:
				out.platform = string(v)
			case 3:
				out.locale = string(v)
			case 12:
				out.ticket = v
			}
		default:
			return out, fmt.Errorf("unexpected wire type %d for field %d", typ, num)
		}
	}
	return out, nil
}

// TestFakeEvidenceMatchesVectors checks the fake's own evidence code against the values the server's C++ produced:
// the same A, B and S must give the vector's M1, and the same M1 must give the vector's M2 in the server's stripped
// text form. This is what makes the end-to-end test trustworthy even though the fake and the client share no code.
func TestFakeEvidenceMatchesVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		a, ok := new(big.Int).SetString(v.ClientPublicA, 16)
		b, okB := new(big.Int).SetString(v.ServerPublicB, 16)
		s, okS := new(big.Int).SetString(v.SessionS, 16)
		if !ok || !okB || !okS {
			t.Fatalf("%s: bad vector hex", v.Name)
		}
		m1 := new(big.Int).SetBytes(fakeEvidenceM1(a, b, s))
		if got := strings.ToUpper(m1.Text(16)); got != strings.ToUpper(v.ClientEvidenceM1) {
			t.Errorf("%s: fake M1 = %s, want %s", v.Name, got, v.ClientEvidenceM1)
		}
		m2 := new(big.Int).SetBytes(fakeEvidenceM2(a, m1, s))
		if got := strings.ToUpper(m2.Text(16)); got != strings.ToUpper(v.ServerEvidenceM2) {
			t.Errorf("%s: fake M2 = %s, want %s", v.Name, got, v.ServerEvidenceM2)
		}
	}
}

// fakeLogin runs the two listeners and answers Login for the vector account.
type fakeLogin struct {
	t      *testing.T
	srp    *fakeSRP
	rest   *httptest.Server
	rpc    net.Listener
	caFile string
	ticket string
	errs   chan error

	realmPopulation uint32
	realmFlags      uint32
	realmBuild      uint32
	restFailBody    string // when set, the SRP request answers 500 with this body
	rpcDrip         bool   // when set, the RPC port never answers the connect request
}

// fakeLoginForm is the subset of the client's LoginForm the fake reads, parsed by the fake's own type so a wrong
// field name in the client's struct does not pass unnoticed.
type fakeLoginForm struct {
	Inputs []struct {
		InputID string `json:"input_id"`
		Value   string `json:"value"`
	} `json:"inputs"`
}

func (f *fakeLoginForm) values() map[string]string {
	out := map[string]string{}
	for _, in := range f.Inputs {
		out[in.InputID] = in.Value
	}
	return out
}

const fakeGameAccountID = 7

func newFakeLogin(t *testing.T) *fakeLogin {
	t.Helper()
	return newFakeLoginWith(t, newFakeSRP(t))
}

// newFakeLoginForVector is the fake server pinned to one C++-produced vector: the challenge carries its salt, B and
// version, and the proof is accepted only if the client reproduces the vector's A and M1.
func newFakeLoginForVector(t *testing.T, v srpVector) *fakeLogin {
	t.Helper()
	return newFakeLoginWith(t, &fakeSRP{
		n:          new(big.Int).SetBytes(mustHex(t, v.Modulus)),
		g:          new(big.Int).SetInt64(2),
		salt:       mustHex(t, v.Salt),
		username:   v.SRPUsername,
		password:   v.Password,
		version:    v.Version,
		iterations: v.Iterations,
		B:          new(big.Int).SetBytes(mustHex(t, v.ServerPublicB)),
		vector:     &v,
	})
}

func newFakeLoginWith(t *testing.T, srp *fakeSRP) *fakeLogin {
	t.Helper()
	caPEM, cert := fakeCert(t)
	caFile := filepath.Join(t.TempDir(), "root_ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLogin{
		t: t, srp: srp, caFile: caFile,
		ticket:          "TC-" + strings.Repeat("AB", 20),
		errs:            make(chan error, 16),
		realmPopulation: 1, realmBuild: build70938,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(loginFormPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			f.handleProof(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "LOGIN_FORM",
			"inputs": []map[string]any{
				{"input_id": "account_name", "type": "text", "label": "E-mail", "max_length": 320},
				{"input_id": "password", "type": "password", "label": "Password", "max_length": 128},
				{"input_id": "log_in_submit", "type": "submit", "label": "Log In"},
			},
			"srp_url": "https://" + fakeServerName + "/bnetserver/login/srp/",
		})
	})
	mux.HandleFunc(srpPath, func(w http.ResponseWriter, r *http.Request) {
		if f.restFailBody != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(f.restFailBody))
			return
		}
		var form fakeLoginForm
		if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		found := strings.EqualFold(form.values()["account_name"], "VECTOR@LOCAL")
		if !found {
			_ = json.NewEncoder(w).Encode(map[string]any{"authentication_state": "DONE"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "session-1", Path: "/bnetserver", Secure: true, HttpOnly: true})
		_ = json.NewEncoder(w).Encode(f.srp.challengeJSON())
	})
	f.rest = httptest.NewUnstartedServer(mux)
	f.rest.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	f.rest.StartTLS()

	f.rpc = f.startRPC(cert)
	return f
}

func (f *fakeLogin) handleProof(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("JSESSIONID"); err != nil || c.Value != "session-1" {
		http.Error(w, "no session cookie", http.StatusUnauthorized)
		return
	}
	var form fakeLoginForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	values := form.values()
	m2, err := f.srp.verify(values["public_A"], values["client_evidence_M1"])
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"authentication_state": "DONE"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authentication_state": "DONE",
		"login_ticket":         f.ticket,
		"server_evidence_M2":   m2,
	})
}

func (f *fakeLogin) close() {
	if f.rpc != nil {
		_ = f.rpc.Close()
	}
	f.rest.Close()
}

func (f *fakeLogin) options() Options {
	return Options{
		RESTAddr:   f.rest.Listener.Addr().String(),
		RPCAddr:    f.rpc.Addr().String(),
		CAFile:     f.caFile,
		ServerName: fakeServerName,
		Account:    "VECTOR@LOCAL",
		Password:   f.srp.password,
		Timeout:    10 * time.Second,
	}
}

// fail records a handler-level problem; the test checks the channel at the end.
func (f *fakeLogin) fail(format string, args ...any) {
	select {
	case f.errs <- fmt.Errorf(format, args...):
	default:
	}
}

// ---------------------------------------------------------------- the RPC side

func (f *fakeLogin) startRPC(cert tls.Certificate) net.Listener {
	f.t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		f.t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serveRPC(conn)
		}
	}()
	return ln
}

type fakeConn struct {
	net.Conn
}

func (c fakeConn) writeFrame(h Header, body []byte) error {
	header := h.marshal()
	frame := make([]byte, 2+len(header)+len(body))
	binary.BigEndian.PutUint16(frame, uint16(len(header)))
	copy(frame[2:], header)
	copy(frame[2+len(header):], body)
	for len(frame) > 0 {
		n, err := c.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}

func (c fakeConn) readFrame() (Header, []byte, error) {
	var lenBuf [2]byte
	if _, err := readFull(c.Conn, lenBuf[:]); err != nil {
		return Header{}, nil, err
	}
	headerBuf := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
	if _, err := readFull(c.Conn, headerBuf); err != nil {
		return Header{}, nil, err
	}
	h, err := unmarshalHeader(headerBuf)
	if err != nil {
		return Header{}, nil, err
	}
	body := make([]byte, h.Size)
	if _, err := readFull(c.Conn, body); err != nil {
		return Header{}, nil, err
	}
	return h, body, nil
}

func readFull(c net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := c.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (f *fakeLogin) respond(c fakeConn, request Header, body []byte) error {
	return c.writeFrame(Header{ServiceID: serviceIDResponse, Token: request.Token, Size: uint32(len(body))}, body)
}

func (f *fakeLogin) serveRPC(raw net.Conn) {
	defer raw.Close()
	c := fakeConn{Conn: raw}
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
	if f.rpcDrip {
		if _, _, err := c.readFrame(); err != nil {
			return
		}
		for i := 0; ; i++ {
			if err := c.writeFrame(Header{
				ServiceID: serviceIDRequest, ServiceHash: authenticationListenerHash,
				MethodID: 99, Token: uint32(i),
			}, nil); err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wantToken := uint32(0)
	for {
		h, body, err := c.readFrame()
		if err != nil {
			return
		}
		if h.ServiceID != serviceIDRequest {
			f.fail("rpc request with service id 0x%X", h.ServiceID)
			return
		}
		if h.Token != wantToken {
			f.fail("rpc request token %d, want %d", h.Token, wantToken)
		}
		wantToken++
		switch {
		case h.ServiceHash == connectionServiceHash && h.MethodID == connectionConnectMethod:
			resp := putBytes(nil, 1, ProcessID{Label: 1, Epoch: 2}.marshal())
			resp = append(resp, putVarint(nil, 6, 1234567890)...)
			resp = append(resp, putVarint(nil, 7, 1)...)
			resp = append(resp, putString(nil, 9, "fake-ciid")...)
			if err := f.respond(c, h, resp); err != nil {
				return
			}

		case h.ServiceHash == authenticationServiceHash && h.MethodID == authenticationLogonMethod:
			logon, err := parseFakeLogonRequest(body)
			if err != nil {
				f.fail("logon request: %v", err)
				return
			}
			if logon.program != "WoW" || logon.platform != "Wn64" || logon.locale != "enUS" {
				f.fail("logon program/platform/locale = %q/%q/%q", logon.program, logon.platform, logon.locale)
			}
			if logon.build != build70938 {
				f.fail("logon application_version = %d, want %d", logon.build, build70938)
			}
			if !bytes.Equal(logon.ticket, []byte(f.ticket)) {
				f.fail("logon cached_web_credentials = %q, want the login ticket", logon.ticket)
			}
			if err := f.respond(c, h, nil); err != nil { // NoData
				return
			}
			key := bytes.Repeat([]byte{0x5A}, 64)
			result := putVarint(nil, 1, 0)
			result = append(result, putBytes(nil, 2, EntityID{High: 0x0100000000000000, Low: 4242}.marshal())...)
			result = append(result, putBytes(nil, 3, EntityID{High: 0x0200000200576F57, Low: fakeGameAccountID}.marshal())...)
			result = append(result, putBytes(nil, 9, key)...)
			if err := c.writeFrame(Header{
				ServiceID: serviceIDRequest, ServiceHash: authenticationListenerHash,
				MethodID: authenticationLogonComplete, Size: uint32(len(result)),
			}, result); err != nil {
				return
			}

		case h.ServiceHash == gameUtilitiesServiceHash && h.MethodID == gameUtilitiesGetAllValuesMethod:
			if !bytes.Contains(body, []byte("Command_RealmListRequest_v1_b9")) {
				f.fail("GetAllValues asks for % X", body)
			}
			sub := "70-1-70"
			resp := putBytes(nil, 1, Variant{StringValue: &sub}.marshal())
			if err := f.respond(c, h, resp); err != nil {
				return
			}

		case h.ServiceHash == gameUtilitiesServiceHash && h.MethodID == gameUtilitiesProcessRequest:
			if err := f.handleClientRequest(c, h, body); err != nil {
				return
			}

		default:
			f.fail("unexpected request service 0x%08X method %d", h.ServiceHash, h.MethodID)
			_ = f.respond(c, h, nil)
		}
	}
}

func (f *fakeLogin) handleClientRequest(c fakeConn, h Header, body []byte) error {
	resp, err := unmarshalClientResponse(body) // a request has the same repeated Attribute field 1
	if err != nil {
		f.fail("client request: %v", err)
		return f.respond(c, h, nil)
	}
	command := ""
	for _, a := range resp.Attributes {
		if strings.HasPrefix(a.Name, "Command_") {
			command = a.Name
		}
	}
	switch {
	case strings.HasPrefix(command, "Command_RealmListTicketRequest_v1"):
		var identity realmListTicketIdentity
		identityBlob, _ := resp.blob("Param_Identity")
		if err := json.Unmarshal(stripPrefix(identityBlob), &identity); err != nil {
			f.fail("identity: %v", err)
		}
		if identity.GameAccountID != fakeGameAccountID || identity.GameAccountRegion != 2 {
			f.fail("identity = %+v", identity)
		}
		var info realmListTicketClientInformation
		infoBlob, _ := resp.blob("Param_ClientInfo")
		if err := json.Unmarshal(stripPrefix(infoBlob), &info); err != nil {
			f.fail("client info: %v", err)
		}
		if len(info.Info.Secret) != 32 {
			f.fail("client secret is %d bytes", len(info.Info.Secret))
		}
		if info.Info.PlatformType != fourCCWin || info.Info.ClientArch != fourCCTextX64 || info.Info.Type != fourCCWoW {
			f.fail("client variant %08X/%08X/%08X", info.Info.PlatformType, info.Info.ClientArch, info.Info.Type)
		}
		ticket := append([]byte("AuthRealmListTicket"), 0)
		return f.respond(c, h, putBytes(nil, 1, attrBlob("Param_RealmListTicket", ticket).marshal()))

	case strings.HasPrefix(command, "Command_RealmListRequest_v1"):
		address := uint32(0x46010046)
		list := map[string]any{"updates": []any{map[string]any{
			"wowRealmAddress": address,
			"deleting":        false,
			"update": map[string]any{
				"wowRealmAddress": address,
				"cfgRealmsID":     70,
				"name":            "TestRealm",
				"populationState": f.realmPopulation,
				"cfgContentSetID": 137,
				"superDistrictID": 2,
				"flags":           f.realmFlags,
				"version":         map[string]any{"versionBuild": f.realmBuild},
			},
		}}}
		counts := map[string]any{"counts": []any{map[string]any{"wowRealmAddress": address, "count": 0}}}
		bodyOut := putBytes(nil, 1, attrBlob("Param_RealmList", compressJSON(f.t, "JSONRealmListUpdates:", list)).marshal())
		bodyOut = append(bodyOut, putBytes(nil, 1, attrBlob("Param_CharacterCountList", compressJSON(f.t, "JSONRealmCharacterCountList:", counts)).marshal())...)
		return f.respond(c, h, bodyOut)

	case strings.HasPrefix(command, "Command_RealmJoinRequest_v1"):
		var address uint64
		for _, a := range resp.Attributes {
			if a.Name == "Param_RealmAddress" && a.Value.UintValue != nil {
				address = *a.Value.UintValue
			}
		}
		if address != 0x46010046 {
			f.fail("realm address %#x", address)
		}
		ticket := map[string]any{"gameAccount": fmt.Sprintf("%d#1", fakeGameAccountID), "platform": fourCCWn64, "type": fourCCWoW, "clientArch": fourCCTextX64}
		ticketJSON, err := json.Marshal(ticket)
		if err != nil {
			f.fail("join ticket: %v", err)
			return f.respond(c, h, nil)
		}
		addresses := map[string]any{"families": []any{map[string]any{
			"family":    1,
			"addresses": []any{map[string]any{"ip": "127.0.0.1", "port": 8085}},
		}}}
		secret := bytes.Repeat([]byte{0x33}, 32)
		bodyOut := putBytes(nil, 1, attrBlob("Param_RealmJoinTicket", ticketJSON).marshal())
		bodyOut = append(bodyOut, putBytes(nil, 1, attrBlob("Param_ServerAddresses", compressJSON(f.t, "JSONRealmListServerIPAddresses:", addresses)).marshal())...)
		bodyOut = append(bodyOut, putBytes(nil, 1, attrBlob("Param_JoinSecret", secret).marshal())...)
		return f.respond(c, h, bodyOut)
	}
	f.fail("unexpected command %q", command)
	return f.respond(c, h, nil)
}

// stripPrefix drops "<Something>:" in front of the JSON the server, and this fake, put into the blob.
func stripPrefix(blob []byte) []byte {
	if i := bytes.IndexByte(blob, ':'); i >= 0 {
		return blob[i+1:]
	}
	return blob
}
