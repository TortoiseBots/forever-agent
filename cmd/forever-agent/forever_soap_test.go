package main

// `forever soap`: one console command over a fake endpoint, the loopback refusal and the secret kept out of every
// line the command writes.

import (
	"bytes"
	"context"
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

const cliSoapPassword = "cli-marker-never-print"

type cliSoapFake struct {
	user, password, reply string
	gotCommand            string
}

func (f *cliSoapFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, p, ok := r.BasicAuth()
	if !ok || u != f.user || p != f.password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	start := strings.Index(body, "<command>")
	end := strings.Index(body, "</command>")
	if start < 0 || end < start {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.gotCommand = body[start+len("<command>") : end]
	fmt.Fprintf(w, `<?xml version="1.0"?><SOAP-ENV:Envelope xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/" xmlns:ns1="urn:TC">`+
		`<SOAP-ENV:Body><ns1:executeCommandResponse><result>%s</result></ns1:executeCommandResponse></SOAP-ENV:Body></SOAP-ENV:Envelope>`,
		f.reply)
}

func cliSoapAddr(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func TestForeverSoapRunsACommand(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "soap.pw")
	if err := os.WriteFile(pw, []byte(cliSoapPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &cliSoapFake{user: "console", password: cliSoapPassword, reply: "Saved.\n"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	host, port := cliSoapAddr(t, srv)
	var out, errOut bytes.Buffer
	code := foreverCmd(context.Background(), []string{"soap", "--host", host, "--port", port, "--user", "console",
		"--password-file", pw, ".saveall"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if out.String() != "Saved.\n" || fake.gotCommand != ".saveall" {
		t.Fatalf("out %q, command %q", out.String(), fake.gotCommand)
	}
	for _, text := range []string{out.String(), errOut.String()} {
		if strings.Contains(text, cliSoapPassword) {
			t.Fatalf("the password is in the output: %q", text)
		}
	}
}

// TestForeverSoapRefusesOffTheAllowListBeforeAnyRequest: a console command outside the setup allow-list is refused
// before any request, and a prefix of an allowed command is not one.
func TestForeverSoapRefusesOffTheAllowListBeforeAnyRequest(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "soap.pw")
	if err := os.WriteFile(pw, []byte(cliSoapPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &cliSoapFake{user: "console", password: cliSoapPassword, reply: "x"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	host, port := cliSoapAddr(t, srv)
	for _, cmd := range []string{".repop", ".sa", ".announce hi"} {
		var out, errOut bytes.Buffer
		code := foreverCmd(context.Background(), []string{"soap", "--host", host, "--port", port, "--user", "console",
			"--password-file", pw, cmd}, &out, &errOut)
		if code != exitUsage || fake.gotCommand != "" {
			t.Fatalf("%q: exit %d, command %q", cmd, code, fake.gotCommand)
		}
	}
}

func TestForeverSoapRefusesNonLoopbackAndDeniedCommands(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "soap.pw")
	if err := os.WriteFile(pw, []byte(cliSoapPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := foreverCmd(context.Background(), []string{"soap", "--host", "elsewhere", "--user", "console",
		"--password-file", pw, ".saveall"}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "not loopback") {
		t.Fatalf("non-loopback: exit %d, stderr %q", code, errOut.String())
	}
	// There is no opt-out: a remote host would carry the administrator's password over plain HTTP, so the old flag
	// is not defined at all.
	out.Reset()
	errOut.Reset()
	code = foreverCmd(context.Background(), []string{"soap", "--host", "elsewhere", "--port", "9", "--user", "console",
		"--password-file", pw, "--allow-non-loopback", "--timeout", "1s", ".saveall"}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "allow-non-loopback") {
		t.Fatalf("the non-loopback opt-out was accepted: exit %d, stderr %q", code, errOut.String())
	}
	// A world-changing command is refused before any request, flag or not.
	fake := &cliSoapFake{user: "console", password: cliSoapPassword}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	host, port := cliSoapAddr(t, srv)
	out.Reset()
	errOut.Reset()
	code = foreverCmd(context.Background(), []string{"soap", "--host", host, "--port", port, "--user", "console",
		"--password-file", pw, ".account create x y"}, &out, &errOut)
	if code != exitUsage || fake.gotCommand != "" {
		t.Fatalf("denied: exit %d, command %q", code, fake.gotCommand)
	}
	for _, text := range []string{out.String(), errOut.String()} {
		if strings.Contains(text, cliSoapPassword) {
			t.Fatalf("the password is in the output: %q", text)
		}
	}
}
