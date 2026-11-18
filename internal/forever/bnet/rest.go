package bnet

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// The REST login paths of LoginRESTService::StartNetwork (src/server/bnetserver/REST/LoginRESTService.cpp:100-145).
const (
	loginFormPath = "/bnetserver/login/"
	srpPath       = "/bnetserver/login/srp/"
)

// formInputs is the FormInputs message of GET /bnetserver/login/ (Login.proto:14-20).
type formInputs struct {
	Type   string `json:"type"`
	Inputs []struct {
		InputID   string `json:"input_id"`
		Type      string `json:"type"`
		Label     string `json:"label"`
		MaxLength uint32 `json:"max_length"`
	} `json:"inputs"`
	SRPURL string `json:"srp_url"`
}

// loginFormValue is one FormInputValue (Login.proto:22-25).
type loginFormValue struct {
	InputID string `json:"input_id"`
	Value   string `json:"value"`
}

// loginForm is the LoginForm message (Login.proto:27-34). platform_id, program_id and version are required by the
// .proto but unread by the server; the real client leaves them empty in the SRP requests.
type loginForm struct {
	PlatformID string           `json:"platform_id"`
	ProgramID  string           `json:"program_id"`
	Version    string           `json:"version"`
	Inputs     []loginFormValue `json:"inputs"`
}

// srpChallengeJSON is the SrpLoginChallenge answer (Login.proto:36-46).
type srpChallengeJSON struct {
	Version      uint32 `json:"version"`
	Iterations   uint32 `json:"iterations"`
	Modulus      string `json:"modulus"`
	Generator    string `json:"generator"`
	HashFunction string `json:"hash_function"`
	Username     string `json:"username"`
	Salt         string `json:"salt"`
	PublicB      string `json:"public_B"`
}

// loginResultJSON is the LoginResult answer (Login.proto:61-71).
type loginResultJSON struct {
	AuthenticationState string `json:"authentication_state"`
	ErrorCode           string `json:"error_code"`
	ErrorMessage        string `json:"error_message"`
	NextURL             string `json:"next_url"`
	LoginTicket         string `json:"login_ticket"`
	ServerEvidenceM2    string `json:"server_evidence_M2"`
}

// restClient talks to the login REST port over TLS. It dials addr but verifies the certificate and sends the Host
// header as serverName, so a tunnel to the server is possible without weakening the check.
type restClient struct {
	client  *http.Client
	base    string
	account string // the name the server was asked about; errors must not echo it back
}

func newRESTClient(addr, serverName, account string, roots *x509.CertPool, timeout time.Duration) (*restClient, error) {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			ServerName: serverName,
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2: false,
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("bnet: cookie jar: %w", err)
	}
	return &restClient{
		client:  &http.Client{Transport: transport, Jar: jar, Timeout: timeout},
		base:    "https://" + serverName,
		account: account,
	}, nil
}

func (c *restClient) close() {
	if transport, ok := c.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (c *restClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		// the path can be the server's srp_url, so the request error (which quotes the URL) is sanitised
		return nil, &serverTextError{prefix: "bnet: REST request: ", err: err, scrub: c.scrubServerText}
	}
	return c.do(req)
}

func (c *restClient) postJSON(ctx context.Context, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, &serverTextError{prefix: "bnet: REST request: ", err: err, scrub: c.scrubServerText}
	}
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	return c.do(req)
}

func (c *restClient) do(req *http.Request) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		// the transport's error can quote a name from the server's certificate; the chain is kept, the text bounded
		return nil, &serverTextError{prefix: fmt.Sprintf("bnet: REST %s %s: ", req.Method, c.scrubServerText(req.URL.Path)), err: err, scrub: c.scrubServerText}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("bnet: REST %s %s: %w", req.Method, c.scrubServerText(req.URL.Path), err)
	}
	if resp.StatusCode != http.StatusOK {
		// the login service answers a refusal with a JSON error message; the body is quoted only through
		// describeRESTFailure, which bounds it, keeps printable ASCII and redacts long hex/base64 runs. The path
		// can be the server's srp_url, so it goes through the sanitiser too.
		return nil, fmt.Errorf("bnet: REST %s %s: %s", req.Method, c.scrubServerText(req.URL.Path), c.describeRESTFailure(resp.Status, body))
	}
	return body, nil
}

// describeRESTFailure names a non-200 answer without passing its body through: the status line (whose reason phrase
// is the server's), the server's error code when the body is a LoginResult, and a bounded, sanitised message. The
// account name is taken out of the result: the login service can echo the name it was asked about.
func (c *restClient) describeRESTFailure(status string, body []byte) string {
	status = c.scrubServerText(status)
	var result loginResultJSON
	if json.Unmarshal(body, &result) == nil && result.ErrorCode != "" {
		message := c.scrubServerText(result.ErrorMessage)
		if message == "" {
			return status + ": " + c.scrubServerText(result.ErrorCode)
		}
		return status + ": " + c.scrubServerText(result.ErrorCode) + ": " + message
	}
	return status + ": " + c.scrubServerText(string(body))
}

// errLoginRefused marks the server's refusal of the credentials (wrong password, banned account, an authenticator
// step). The message carries only the server's bounded, sanitised reason; callers test errors.Is, not a sentence.
var errLoginRefused = errors.New("bnet: login refused")

// serverRefusal describes a LoginResult the server refused with, quoting only bounded, sanitised server text, with
// the account name the server was asked about taken out.
func (c *restClient) serverRefusal(result *loginResultJSON) string {
	switch {
	case result.ErrorCode != "" && result.ErrorMessage != "":
		return c.scrubServerText(result.ErrorCode) + ": " + c.scrubServerText(result.ErrorMessage)
	case result.ErrorCode != "":
		return c.scrubServerText(result.ErrorCode)
	case result.ErrorMessage != "":
		return c.scrubServerText(result.ErrorMessage)
	}
	return "the server gave neither a ticket nor an error code"
}

// form requests the login form and returns the SRP path it advertises.
func (c *restClient) form(ctx context.Context) (string, error) {
	body, err := c.get(ctx, loginFormPath)
	if err != nil {
		return "", err
	}
	var form formInputs
	if err := json.Unmarshal(body, &form); err != nil {
		return "", decodeError("login form", err)
	}
	if form.Type != "LOGIN_FORM" {
		return "", fmt.Errorf("bnet: login form type %q", sanitizeServerText(form.Type))
	}
	// the path is server text: it goes into the log line and the next request, so only the fixed path of the form
	// or a short printable path that still ends in srpPath is used
	path := srpPath
	if u, err := url.Parse(form.SRPURL); err == nil && printableASCII(u.Path) && len(u.Path) <= maxServerPath && strings.HasSuffix(u.Path, srpPath) {
		path = u.Path
	}
	return path, nil
}

// challenge asks for the SRP challenge of an account.
func (c *restClient) challenge(ctx context.Context, path, account string) (*srpChallenge, error) {
	body, err := c.postJSON(ctx, path, loginForm{Inputs: []loginFormValue{{InputID: "account_name", Value: account}}})
	if err != nil {
		return nil, err
	}
	var raw srpChallengeJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, decodeError("srp challenge", err)
	}
	salt, err := hex.DecodeString(raw.Salt)
	if err != nil {
		return nil, fmt.Errorf("bnet: srp challenge salt: %w", err)
	}
	publicB, err := hex.DecodeString(raw.PublicB)
	if err != nil {
		return nil, fmt.Errorf("bnet: srp challenge B: %w", err)
	}
	return &srpChallenge{
		Version:      raw.Version,
		Iterations:   raw.Iterations,
		Modulus:      raw.Modulus,
		Generator:    raw.Generator,
		Username:     raw.Username,
		Salt:         salt,
		PublicB:      publicB,
		HashFunction: raw.HashFunction,
	}, nil
}

// proof posts the SRP evidence and returns the login result.
func (c *restClient) proof(ctx context.Context, account string, s *srpClient) (*loginResultJSON, error) {
	body, err := c.postJSON(ctx, loginFormPath, loginForm{Inputs: []loginFormValue{
		{InputID: "account_name", Value: account},
		{InputID: "public_A", Value: s.publicAHex()},
		{InputID: "client_evidence_M1", Value: s.evidenceM1Hex()},
	}})
	if err != nil {
		return nil, err
	}
	var result loginResultJSON
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, decodeError("login result", err)
	}
	if result.AuthenticationState != "DONE" {
		return nil, fmt.Errorf("%w in state %q: %s", errLoginRefused, c.scrubServerText(result.AuthenticationState), c.serverRefusal(&result))
	}
	if result.ErrorCode != "" {
		return nil, fmt.Errorf("%w: %s", errLoginRefused, c.serverRefusal(&result))
	}
	if result.LoginTicket == "" {
		if result.NextURL != "" {
			return nil, fmt.Errorf("%w: the server needs a second step at %s (an authenticator code?), which this client does not do", errLoginRefused, c.scrubServerText(result.NextURL))
		}
		return nil, fmt.Errorf("%w: no ticket, no error code (wrong password)", errLoginRefused)
	}
	return &result, nil
}
