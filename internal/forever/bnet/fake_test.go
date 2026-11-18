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

// brokenEvidence is the server's evidence encoding: big-endian, (bits+8)>>3 bytes (SRP6.cpp:182-186).
func brokenEvidence(v *big.Int) []byte {
	out := make([]byte, (v.BitLen()+8)>>3)
	v.FillBytes(out)
	return out
}

func padTo(v *big.Int, size int) []byte {
	out := make([]byte, size)
	v.FillBytes(out)
	return out
}

// fakeSRP is the server half of SRP6: the verifier of a vector account and a fixed b.
type fakeSRP struct {
	n, g, k, v, b, B *big.Int
	salt             []byte
	username         string
	password         string
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
		n:        new(big.Int).SetBytes(mustHex(t, chosen.Modulus)),
		g:        new(big.Int).SetInt64(2),
		salt:     mustHex(t, chosen.Salt),
		username: chosen.SRPUsername,
		password: chosen.Password,
	}
	size := len(f.n.Bytes())
	f.k = new(big.Int).SetBytes(digest(padTo(f.n, size), padTo(f.g, size)))
	verifier := mustHex(t, chosen.Verifier) // little-endian on disk
	reverse(verifier)
	f.v = new(big.Int).SetBytes(verifier)
	f.b = new(big.Int).SetBytes([]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88})
	f.B = new(big.Int).Add(new(big.Int).Exp(f.g, f.b, f.n), new(big.Int).Mul(f.k, f.v))
	f.B.Mod(f.B, f.n)
	return f
}

func (f *fakeSRP) challenge() srpChallengeJSON {
	return srpChallengeJSON{
		Version: 2, Iterations: 15000,
		Modulus:      strings.ToUpper(f.n.Text(16)),
		Generator:    "02",
		HashFunction: "SHA-256",
		Username:     f.username,
		Salt:         hex.EncodeToString(f.salt),
		PublicB:      strings.ToUpper(f.B.Text(16)),
	}
}

// verify checks the client evidence and returns the server evidence M2.
func (f *fakeSRP) verify(publicAHex, m1Hex string) (string, error) {
	a, ok := new(big.Int).SetString(publicAHex, 16)
	if !ok || a.Sign() == 0 {
		return "", fmt.Errorf("bad A")
	}
	clientM1, ok := new(big.Int).SetString(m1Hex, 16)
	if !ok {
		return "", fmt.Errorf("bad M1")
	}
	size := len(f.n.Bytes())
	u := new(big.Int).SetBytes(digest(padTo(a, size), padTo(f.B, size)))
	s := new(big.Int).Exp(f.v, u, f.n)
	s.Mul(s, a)
	s.Mod(s, f.n)
	s.Exp(s, f.b, f.n)

	m1 := new(big.Int).SetBytes(digest(brokenEvidence(a), brokenEvidence(f.B), brokenEvidence(s)))
	if m1.Cmp(clientM1) != 0 {
		return "", fmt.Errorf("client evidence does not match")
	}
	m2 := new(big.Int).SetBytes(digest(brokenEvidence(a), brokenEvidence(clientM1), brokenEvidence(s)))
	return strings.ToUpper(m2.Text(16)), nil
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
}

const fakeGameAccountID = 7

func newFakeLogin(t *testing.T) *fakeLogin {
	t.Helper()
	caPEM, cert := fakeCert(t)
	caFile := filepath.Join(t.TempDir(), "root_ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeLogin{
		t: t, srp: newFakeSRP(t), caFile: caFile,
		ticket: "TC-" + strings.Repeat("AB", 20),
		errs:   make(chan error, 16),
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
		var form loginForm
		if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		found := false
		for _, in := range form.Inputs {
			if in.InputID == "account_name" && strings.EqualFold(in.Value, "VECTOR@LOCAL") {
				found = true
			}
		}
		if !found {
			_ = json.NewEncoder(w).Encode(loginResultJSON{AuthenticationState: "DONE"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "session-1", Path: "/bnetserver", Secure: true, HttpOnly: true})
		_ = json.NewEncoder(w).Encode(f.srp.challenge())
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
	var form loginForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	values := map[string]string{}
	for _, in := range form.Inputs {
		values[in.InputID] = in.Value
	}
	m2, err := f.srp.verify(values["public_A"], values["client_evidence_M1"])
	if err != nil {
		_ = json.NewEncoder(w).Encode(loginResultJSON{AuthenticationState: "DONE"})
		return
	}
	_ = json.NewEncoder(w).Encode(loginResultJSON{
		AuthenticationState: "DONE",
		LoginTicket:         f.ticket,
		ServerEvidenceM2:    m2,
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
	for {
		h, body, err := c.readFrame()
		if err != nil {
			return
		}
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
			if !bytes.Contains(body, []byte(f.ticket)) {
				f.fail("logon request does not carry the login ticket")
			}
			if !bytes.Contains(body, []byte("WoW")) || !bytes.Contains(body, []byte("Wn64")) || !bytes.Contains(body, []byte("enUS")) {
				f.fail("logon request lacks program/platform/locale")
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
				"populationState": 1,
				"cfgContentSetID": 137,
				"superDistrictID": 2,
				"version":         map[string]any{"versionBuild": 70938},
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
