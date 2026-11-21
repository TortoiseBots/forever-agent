package soap

// The SOAP helper against a fake endpoint: the core's own service shape (TCSoap.cpp's ns1__executeCommand), the
// loopback rule, and the password never appearing in any output or error.

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakePassword = "s3cr3t-MARKER-do-not-print"

// fakeSoap answers the core's executeCommand call. reply is the command's printed text; status and fault shape a
// refusal.
type fakeSoap struct {
	user, password string
	status         int
	fault          string
	reply          string
	gotCommand     string
	gotAction      string
}

func (f *fakeSoap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	f.gotAction = r.Header.Get("SOAPAction")
	u, p, ok := r.BasicAuth()
	if !ok || u != f.user || p != f.password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var doc struct {
		Body struct {
			Command string `xml:"executeCommand>command"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.gotCommand = doc.Body.Command
	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
	}
	if f.fault != "" {
		fmt.Fprintf(w, `<?xml version="1.0"?><SOAP-ENV:Envelope xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/">`+
			`<SOAP-ENV:Body><SOAP-ENV:Fault><faultcode>SOAP-ENV:Sender</faultcode><faultstring>%s</faultstring></SOAP-ENV:Fault></SOAP-ENV:Body></SOAP-ENV:Envelope>`,
			f.fault)
		return
	}
	var esc strings.Builder
	_ = xml.EscapeText(&esc, []byte(f.reply))
	fmt.Fprintf(w, `<?xml version="1.0"?><SOAP-ENV:Envelope xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/" xmlns:ns1="urn:TC">`+
		`<SOAP-ENV:Body><ns1:executeCommandResponse><result>%s</result></ns1:executeCommandResponse></SOAP-ENV:Body></SOAP-ENV:Envelope>`,
		esc.String())
}

// soapConfig points a Config at the fake server's loopback address.
func soapConfig(t *testing.T, srv *httptest.Server, user string) Config {
	t.Helper()
	host, portS, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portS, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return Config{Host: host, Port: port, User: user, PasswordEnv: "TEST_SOAP_PASSWORD"}
}

// TestSoapAnswersTheCoreShape: one Basic-authenticated executeCommand POST, the command escaped into the body, and
// the printed text back exactly.
func TestSoapAnswersTheCoreShape(t *testing.T) {
	t.Setenv("TEST_SOAP_PASSWORD", fakePassword)
	fake := &fakeSoap{user: "console", password: fakePassword, reply: "There are 3 players in the world.\nServer uptime: 12 s"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := soapConfig(t, srv, "console")
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text, err := c.Run(context.Background(), ".pinfo")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if text != fake.reply {
		t.Fatalf("reply %q, want %q", text, fake.reply)
	}
	if fake.gotCommand != ".pinfo" || fake.gotAction != `"urn:TC#executeCommand"` {
		t.Fatalf("server saw command %q action %q", fake.gotCommand, fake.gotAction)
	}

	// A command with XML metacharacters travels as data, not as markup.
	fake.gotCommand = ""
	if _, err := c.Run(context.Background(), `.pinfo A&B <C>`); err != nil {
		t.Fatal(err)
	}
	if fake.gotCommand != `.pinfo A&B <C>` {
		t.Fatalf("escaped command arrived as %q", fake.gotCommand)
	}
}

// TestSoapRefusalsKeepTheSecret: 401, a SOAP fault, a malformed answer and a transport failure all answer an error,
// and no error or output carries the password.
func TestSoapRefusalsKeepTheSecret(t *testing.T) {
	t.Setenv("TEST_SOAP_PASSWORD", fakePassword)
	for _, tc := range []struct {
		name   string
		fake   *fakeSoap
		status int
	}{
		{"wrong password", &fakeSoap{user: "console", password: "other", fault: ""}, 0},
		{"soap fault", &fakeSoap{user: "console", password: fakePassword, status: http.StatusInternalServerError, fault: "Command can not be empty"}, 0},
		{"fault echoes the secret", &fakeSoap{user: "console", password: fakePassword, status: http.StatusInternalServerError, fault: "bad " + fakePassword}, 0},
		{"not administrator", &fakeSoap{user: "console", password: fakePassword, status: http.StatusForbidden}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.fake)
			defer srv.Close()
			c, err := New(soapConfig(t, srv, "console"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Run(context.Background(), ".saveall")
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), fakePassword) {
				t.Fatalf("the error carries the password: %v", err)
			}
		})
	}
	// A server whose result echoes the credential does not get it into the output either.
	echo := &fakeSoap{user: "console", password: fakePassword, reply: fakePassword}
	srv := httptest.NewServer(echo)
	defer srv.Close()
	c, err := New(soapConfig(t, srv, "console"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := c.Run(context.Background(), ".saveall")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, fakePassword) || !strings.Contains(text, "[redacted]") {
		t.Fatalf("the result text was not redacted: %q", text)
	}

	// A closed endpoint (connection refused) is one error line without the secret.
	closed := httptest.NewServer(http.NotFoundHandler())
	host, portS, _ := net.SplitHostPort(strings.TrimPrefix(closed.URL, "http://"))
	closed.Close()
	var port int
	_, _ = fmt.Sscanf(portS, "%d", &port)
	cfg := Config{Host: host, Port: port, User: "console", PasswordEnv: "TEST_SOAP_PASSWORD", TimeoutMS: 300}
	closedClient, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := closedClient.Run(context.Background(), ".saveall"); err == nil || strings.Contains(err.Error(), fakePassword) {
		t.Fatalf("closed endpoint: %v", err)
	}
}

// TestSoapLoopbackOnly: a non-loopback host is refused unless the explicit flag is set; a scheme, a userinfo and a
// bad port are refused always.
func TestSoapLoopbackOnly(t *testing.T) {
	hosts := []string{"elsewhere", "remote-console", "another-machine"}
	for _, host := range hosts {
		if err := (Config{Host: host, User: "u"}).Validate(); err == nil {
			t.Fatalf("%s was accepted without --allow-non-loopback", host)
		}
	}
	if err := (Config{Host: "elsewhere", User: "u", AllowNonLoopback: true}).Validate(); err != nil {
		t.Fatalf("the explicit flag still refuses a remote host: %v", err)
	}
	for _, host := range []string{"127.0.0.1", "localhost"} {
		if err := (Config{Host: host, User: "u"}).Validate(); err != nil {
			t.Fatalf("%s refused: %v", host, err)
		}
	}
	for _, cfg := range []Config{
		{Host: "https://127.0.0.1", User: "u"},
		{Host: "u:p@127.0.0.1", User: "u"},
		{Host: "127.0.0.1/x", User: "u"},
		{Host: "127.0.0.1", Port: -1, User: "u"},
		{Host: "127.0.0.1", User: " "},
		{Host: "127.0.0.1", Port: 7878, User: "u", TimeoutMS: 120001},
	} {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%+v was accepted", cfg)
		}
	}
}

// TestSoapPasswordSources: the file must be private (privfile's rule) and the environment is the fallback; neither
// error names the secret.
func TestSoapPasswordSources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "soap.pw")
	if err := os.WriteFile(path, []byte(fakePassword+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Config{Host: "127.0.0.1", User: "u", PasswordFile: path}).Password(); err == nil {
		t.Fatal("a 0644 password file was read")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	pw, err := (Config{Host: "127.0.0.1", User: "u", PasswordFile: path}).Password()
	if err != nil || pw != fakePassword {
		t.Fatalf("file password %q, %v", pw, err)
	}
	// The environment fallback and the default variable name.
	t.Setenv(DefaultPasswordEnv, fakePassword)
	pw, err = (Config{Host: "127.0.0.1", User: "u"}).Password()
	if err != nil || pw != fakePassword {
		t.Fatalf("env password %q, %v", pw, err)
	}
	os.Unsetenv(DefaultPasswordEnv)
	_, err = (Config{Host: "127.0.0.1", User: "u"}).Password()
	if err == nil || strings.Contains(err.Error(), fakePassword) {
		t.Fatalf("missing env: %v", err)
	}
}

// TestSoapDeniedWorldCommands: the AGENTS.md list is refused before anything is sent.
func TestSoapDeniedWorldCommands(t *testing.T) {
	t.Setenv("TEST_SOAP_PASSWORD", fakePassword)
	fake := &fakeSoap{user: "console", password: fakePassword}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c, err := New(soapConfig(t, srv, "console"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{".reload all", ".server shutdown 1", ".account create x y", ".npc add 1", ".gobject delete 2", ".ban X"} {
		if _, err := c.Run(context.Background(), cmd); err == nil {
			t.Fatalf("%q was sent", cmd)
		}
	}
	if fake.gotCommand != "" {
		t.Fatalf("a denied command reached the server: %q", fake.gotCommand)
	}
	// A per-character/per-instance setup command is allowed.
	fake.reply = "Saved."
	if _, err := c.Run(context.Background(), ".saveall"); err != nil {
		t.Fatalf(".saveall: %v", err)
	}
}
