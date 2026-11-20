package tbdash

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestPlainHTTPToNonLoopbackIsRefused is review point 1: the login carries a GM password, so a
// plain-http URL is only allowed to a loopback IP literal or localhost. The addresses are
// TEST-NET literals: no DNS and no real host is involved.
func TestPlainHTTPToNonLoopbackIsRefused(t *testing.T) {
	for _, base := range []string{"http://192.0.2.10:8790", "http://198.51.100.7:8790"} {
		_, err := New(base, "DashUser", "/nonexistent")
		if err == nil {
			t.Errorf("New(%q) accepted plain HTTP to a non-loopback host", base)
			continue
		}
		if !strings.Contains(err.Error(), "clear text") && !strings.Contains(err.Error(), "https") {
			t.Errorf("New(%q) error %q does not explain the boundary", base, err)
		}
	}
}

func TestLoopbackPlainHTTPIsAllowed(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:8790", "http://127.9.8.7:8790", "http://[::1]:8790", "http://localhost:8790"} {
		if _, err := New(base, "DashUser", "/nonexistent"); err != nil {
			t.Errorf("New(%q) refused a loopback URL: %v", base, err)
		}
	}
}

func TestHTTPSNonLoopbackIsAllowed(t *testing.T) {
	if _, err := New("https://192.0.2.10:8790", "DashUser", "/nonexistent"); err != nil {
		t.Errorf("New refuted an https URL: %v", err)
	}
}

// TestPlainHTTPNameIsRefusedWhateverDNSsays is review point 1: the loopback check and the dial
// must not be two different answers. A plain-http host name is refused even when the resolver
// maps it to loopback, and New must not consult the resolver at all.
func TestPlainHTTPNameIsRefusedWhateverDNSsays(t *testing.T) {
	old := lookupIP
	defer func() { lookupIP = old }()

	var asked atomic.Bool
	lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		asked.Store(true)
		return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
	}
	_, err := New("http://dash.internal:8790", "DashUser", "/nonexistent")
	if err == nil {
		t.Fatal("a plain-http host name was accepted because DNS said loopback")
	}
	if !strings.Contains(err.Error(), "clear text") && !strings.Contains(err.Error(), "https") {
		t.Fatalf("error %q does not explain the boundary", err)
	}
	if asked.Load() {
		t.Fatal("New resolved the host; no lookup may decide where the password goes")
	}
}

// TestLocalhostIsDialledAsALiteral pins the other half: the only accepted name is dialled as
// 127.0.0.1 by the client, with the port kept.
func TestLocalhostIsDialledAsALiteral(t *testing.T) {
	for base, want := range map[string]string{
		"http://localhost:8790": "127.0.0.1:8790",
		"http://LOCALHOST:80":   "127.0.0.1:80",
		"http://localhost":      "127.0.0.1",
	} {
		c, err := New(base, "DashUser", "/nonexistent")
		if err != nil {
			t.Fatalf("New(%q): %v", base, err)
		}
		if c.Base.Host != want {
			t.Errorf("New(%q) dials %q, want %q", base, c.Base.Host, want)
		}
	}
}

func TestInsecureHTTPOptionAllowsANonLoopbackHost(t *testing.T) {
	if _, err := NewWithOptions("http://192.0.2.10:8790", "DashUser", PasswordFile("/nonexistent"), Options{AllowInsecureHTTP: true}); err != nil {
		t.Fatalf("the explicit insecure option was refused: %v", err)
	}
	// The option must not weaken anything else: a bad scheme is still bad.
	if _, err := NewWithOptions("ftp://192.0.2.10:8790", "DashUser", PasswordFile("/nonexistent"), Options{AllowInsecureHTTP: true}); err == nil {
		t.Fatal("the insecure option accepted an ftp URL")
	}
}

// TestConcurrent401sLogInOnce is review point 3: every caller that met the same stale session
// must not log in again after one of them has. The server rotates its cookie on every login and
// holds the first wave of reads until all of them have arrived, so every read starts stale.
func TestConcurrent401sLogInOnce(t *testing.T) {
	const readers = 8
	var mu sync.Mutex
	logins, arrived := 0, 0
	token := ""
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			mu.Lock()
			logins++
			token = fmt.Sprintf("session-%d", logins)
			tok := token
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "s", Value: tok, Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
		case "/api/v1/commanders":
			mu.Lock()
			arrived++
			firstWave := arrived <= readers
			if arrived == readers {
				close(gate)
			}
			mu.Unlock()
			if firstWave {
				<-gate
			}
			mu.Lock()
			current := token
			mu.Unlock()
			c, err := r.Cookie("s")
			if err != nil || c.Value != current {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"commanders":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, 0o600)
	start := make(chan struct{})
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := c.GetJSON(context.Background(), "/api/v1/commanders", nil)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("a concurrent read failed: %v", err)
		}
	}
	mu.Lock()
	got := logins
	mu.Unlock()
	if got != 1 {
		t.Fatalf("%d logins for %d concurrent stale reads, want 1", got, readers)
	}
}

// TestAnswerOverTheLimitIsRefused is review point 4: a dashboard answer has a documented cap.
func TestAnswerOverTheLimitIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(maxResponseBytes+1))
		_, _ = w.Write(make([]byte, maxResponseBytes+1))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, 0o600)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, err := c.Commanders(context.Background())
	if err == nil {
		t.Fatal("an over-limit answer was accepted")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error %q does not name the limit", err)
	}
}

// The retry after a 401 reads through the same cap, without a Content-Length.
func TestAnswerOverTheLimitOnThe401Retry(t *testing.T) {
	var reads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		if atomic.AddInt32(&reads, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		for range 9 {
			_, _ = w.Write(make([]byte, 1<<20))
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, 0o600)
	_, err := c.Commanders(context.Background())
	if err == nil {
		t.Fatal("an over-limit answer on the retry was accepted")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error %q does not name the limit", err)
	}
	if n := atomic.LoadInt32(&reads); n != 2 {
		t.Fatalf("%d reads, want 2 (the 401 and its retry)", n)
	}
}
