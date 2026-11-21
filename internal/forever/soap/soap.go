// Package soap runs one console command on a ForeverCore worldserver over its SOAP endpoint, for setup without a
// client: a GM account's console commands (`worldserver.conf.dist`: SOAP.Enabled/SOAP.IP/SOAP.Port, default off)
// answer without a login, a character or the GM-guard round trip. The service is the core's own gSOAP one
// (TCSoap.cpp: ns1__executeCommand), reachable only from loopback unless the caller explicitly allows more; the
// credentials travel only in the HTTP Basic header and are never printed, logged or put in an error.
package soap

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/privfile"
)

// Defaults of the core's worldserver.conf.dist (SOAP.IP 127.0.0.1, SOAP.Port 7878).
const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 7878
	// DefaultPasswordEnv is the environment variable read when no password file is named.
	DefaultPasswordEnv = "FOREVER_SOAP_PASSWORD"
	// maxAnswer caps one SOAP answer: a command's printed text, never a page of world data.
	maxAnswer = 1 << 20
	// defaultTimeout bounds one request.
	defaultTimeout = 15 * time.Second
)

// Config is one server's SOAP endpoint. Host, Port, User and the password source are the whole configuration; the
// password itself is read at New, never stored in this struct. The JSON tags are what a slots file's `soap` block
// uses.
type Config struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	User string `json:"user,omitempty"`
	// PasswordFile is a file holding the password (read like a token file: 0600, no symlink). PasswordEnv names an
	// environment variable instead; when both are empty DefaultPasswordEnv is read.
	PasswordFile string `json:"password_file,omitempty"`
	PasswordEnv  string `json:"password_env,omitempty"`
	// TimeoutMS bounds one request; 0 is defaultTimeout, more than 2 minutes is refused.
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// Address is host:port in URL form (brackets for an IPv6 literal).
func (c Config) Address() string {
	host := c.Host
	if host == "" {
		host = DefaultHost
	}
	port := c.Port
	if port == 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Timeout is the request deadline.
func (c Config) Timeout() time.Duration {
	switch {
	case c.TimeoutMS <= 0:
		return defaultTimeout
	case c.TimeoutMS > 120000:
		return 120 * time.Second
	}
	return time.Duration(c.TimeoutMS) * time.Millisecond
}

// Validate refuses an endpoint this helper must not talk to: a non-loopback host without the explicit flag, a
// scheme or userinfo in the host, a bad port, no user, or a timeout above the cap.
func (c Config) Validate() error {
	host := c.Host
	if host == "" {
		host = DefaultHost
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/@") || strings.ContainsAny(host, " \t") {
		return fmt.Errorf("soap: host %q: give a bare host or address (no scheme, path or userinfo)", host)
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("soap: port %d is not a TCP port", c.Port)
	}
	port := c.Port
	if port == 0 {
		port = DefaultPort
	}
	if port < 1 {
		return fmt.Errorf("soap: port %d is not a TCP port", port)
	}
	// The endpoint is loopback only, always: the core's SOAP transport is plain HTTP, so the administrator's
	// credential would cross the network in cleartext. A name that resolves elsewhere is refused after the dial too.
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return fmt.Errorf("soap: %s is not loopback; the SOAP account is an administrator's and the endpoint is plain HTTP", host)
		}
	} else if !strings.EqualFold(host, "localhost") {
		return fmt.Errorf("soap: host %q is not loopback (127.0.0.1, ::1 or localhost)", host)
	}
	if strings.TrimSpace(c.User) == "" {
		return errors.New("soap: no user (the SOAP account's name)")
	}
	if c.TimeoutMS > 120000 {
		return fmt.Errorf("soap: timeout %d ms is more than 2 minutes", c.TimeoutMS)
	}
	return nil
}

// Password reads the secret this config names: the file (private to its owner, at most 4 KiB) or the environment
// variable. It never returns a value in an error message, only a path or a variable name.
func (c Config) Password() (string, error) {
	if c.PasswordFile != "" {
		raw, err := privfile.Read(c.PasswordFile, 4096)
		if err != nil {
			return "", fmt.Errorf("soap: password file %s: %v", c.PasswordFile, err)
		}
		pw := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		if pw == "" {
			return "", fmt.Errorf("soap: password file %s is empty", c.PasswordFile)
		}
		return pw, nil
	}
	name := c.PasswordEnv
	if name == "" {
		name = DefaultPasswordEnv
	}
	pw, ok := os.LookupEnv(name)
	if !ok || pw == "" {
		return "", fmt.Errorf("soap: password env %s is not set (name a file with --password-file or the variable with --password-env)", name)
	}
	return pw, nil
}

// Client is one endpoint with its password read. Every request carries the credential in its Basic header only.
type Client struct {
	cfg Config
	hc  *http.Client
	pw  string
}

// New validates cfg and reads its password. The error names only a path or a variable, never the secret.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	pw, err := cfg.Password()
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: cfg.Timeout()}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			// The host check above is by name; a name can resolve elsewhere, so the connected peer is checked too.
			if ta, ok := conn.RemoteAddr().(*net.TCPAddr); !ok || !ta.IP.IsLoopback() {
				_ = conn.Close()
				return nil, errors.New("soap: the endpoint resolved to a non-loopback address; the endpoint is plain HTTP")
			}
			return conn, nil
		},
	}
	hc := &http.Client{
		Timeout:   cfg.Timeout(),
		Transport: tr,
		// A redirect would carry the Basic header somewhere else: refuse it instead of following.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("soap: the endpoint redirected; not following with the credential")
		},
	}
	return &Client{cfg: cfg, hc: hc, pw: pw}, nil
}

// Address is the endpoint's host:port.
func (c *Client) Address() string { return c.cfg.Address() }

// Timeout is the request deadline.
func (c *Client) Timeout() time.Duration { return c.cfg.Timeout() }

// Run executes one console command and returns the text the command printed. The command is the core's console
// line without a leading dot (the same text `.account`, `.saveall` and the rest take); commands that need a
// selected unit stay on a GM client. A rule from AGENTS.md is enforced here too: a command that changes the world
// for everyone is refused, because this helper exists for per-character and per-instance setup.
func (c *Client) Run(ctx context.Context, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("soap: empty command")
	}
	if Denied(command) {
		return "", fmt.Errorf("soap: command %q changes the world for everyone (AGENTS.md): refused", cut(command))
	}
	body := executeCommandBody(command)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.cfg.Address()+"/", strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("soap: build request: %v", err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"urn:TC#executeCommand"`)
	req.SetBasicAuth(c.cfg.User, c.pw)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("soap: %s: %v", c.cfg.Address(), withoutSecret(err, c.pw))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return "", fmt.Errorf("soap: read the answer: %v", err)
	}
	if len(raw) > maxAnswer {
		return "", fmt.Errorf("soap: the answer is more than %d bytes", maxAnswer)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return "", errors.New("soap: the server refused the credentials (HTTP 401)")
	case http.StatusForbidden:
		return "", errors.New("soap: the account is not an administrator (HTTP 403)")
	default:
		if fault := faultString(raw); fault != "" {
			return "", fmt.Errorf("soap: HTTP %d: %s", resp.StatusCode, cut(c.redact(fault)))
		}
		return "", fmt.Errorf("soap: HTTP %d from %s", resp.StatusCode, c.cfg.Address())
	}
	text, fault := parseAnswer(raw)
	if fault != "" {
		return "", fmt.Errorf("soap: the command failed: %s", cut(c.redact(fault)))
	}
	if text == nil {
		return "", errors.New("soap: the answer has no executeCommandResponse result")
	}
	return c.redact(*text), nil
}

// executeCommandBody is the gSOAP document TCSoap serves: an ns1:executeCommand element with the command as a C
// string (TCSoap.cpp's generated soap_serve_ns1__executeCommand; the namespace is urn:TC).
func executeCommandBody(command string) string {
	var esc strings.Builder
	_ = xml.EscapeText(&esc, []byte(command))
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/" xmlns:ns1="urn:TC">` +
		`<SOAP-ENV:Body><ns1:executeCommand><command>` + esc.String() +
		`</command></ns1:executeCommand></SOAP-ENV:Body></SOAP-ENV:Envelope>`
}

// parseAnswer finds the executeCommandResponse's result text and any faultstring. A nil text with no fault means the
// answer carried no result element at all (a wrong service, not an empty command).
func parseAnswer(raw []byte) (text *string, fault string) {
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	var inResult, inFault bool
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "result":
				inResult = true
				b.Reset()
			case "faultstring":
				inFault = true
			}
		case xml.CharData:
			if inResult {
				b.Write(t)
			} else if inFault {
				fault += string(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "result":
				s := b.String()
				text = &s
				inResult = false
			case "faultstring":
				inFault = false
			}
		}
	}
	return text, strings.TrimSpace(fault)
}

// faultString is the fault text in a non-200 answer, for the error line; empty when the body holds none.
func faultString(raw []byte) string {
	_, fault := parseAnswer(raw)
	return fault
}

// redact removes the password from any text a server supplied (a fault, a result) or a transport error carried.
// The credential only ever travels in the Basic header; a server that echoes it must not get it into a log line.
func (c *Client) redact(s string) string {
	if c.pw != "" {
		s = strings.ReplaceAll(s, c.pw, "[redacted]")
	}
	return s
}

// withoutSecret keeps a transport error's text but removes the password if a server or a proxy echoed it. A
// password is not supposed to appear in a network error; this is the belt to the Basic header's braces.
func withoutSecret(err error, secret string) string {
	s := err.Error()
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}

// cut shortens a server text for one error line.
func cut(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// Denied reports a console command that changes the world for everyone, which AGENTS.md keeps off every GM path:
// the same list the scenario runner's gm step uses. The core resolves each token as a case-insensitive prefix
// (StringStartsWithI, ChatCommand.cpp:240-246), so `rel` runs reload and `acc create` runs account create: a denied
// word is matched the same way. Root words need two characters (a one-letter token is ambiguous enough that the
// core rarely resolves it); subcommands match at any length.
func Denied(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.TrimPrefix(t, ".")
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return false
	}
	for _, want := range []string{"reload", "server", "account", "ban", "unban"} {
		if tokenPrefix(fields[0], want, 2) {
			return true
		}
	}
	if len(fields) >= 2 {
		if tokenPrefix(fields[0], "npc", 2) {
			for _, want := range []string{"add", "delete", "move", "set"} {
				if tokenPrefix(fields[1], want, 1) {
					return true
				}
			}
		}
		if tokenPrefix(fields[0], "gobject", 3) {
			for _, want := range []string{"add", "delete"} {
				if tokenPrefix(fields[1], want, 1) {
					return true
				}
			}
		}
	}
	return false
}

// tokenPrefix reports whether tok is a prefix of want of at least min bytes, the core's command resolution.
func tokenPrefix(tok, want string, min int) bool {
	return len(tok) >= min && len(tok) <= len(want) && strings.HasPrefix(want, tok)
}
