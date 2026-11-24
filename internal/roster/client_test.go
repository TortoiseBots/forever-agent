package roster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/tbdash"
)

// The tests' values are made up: no account, host or bot name from a live dashboard.
const (
	testUser     = "DashUser"
	testPassword = "not-a-real-roster-password"
	testEnv      = "ROSTER_TEST_PASSWORD"
)

func testSource(t *testing.T) tbdash.PasswordSource {
	t.Helper()
	t.Setenv(testEnv, testPassword)
	return tbdash.PasswordEnv(testEnv)
}

func newTestClient(t *testing.T, base string, timeout time.Duration) *Client {
	t.Helper()
	c, err := New(base, testUser, testSource(t), Options{Timeout: timeout})
	if err != nil {
		t.Fatalf("New(%q): %v", base, err)
	}
	return c
}

func TestRosterLoginsWithCookieAndDecodesUnknownFields(t *testing.T) {
	const token = "session-token-from-the-dashboard"
	var logins, reads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			if r.Method != http.MethodPost {
				t.Errorf("login method %s, want POST", r.Method)
			}
			var req struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := decodeJSON(r, &req); err != nil {
				t.Errorf("login body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if req.Username != testUser || req.Password != testPassword {
				t.Errorf("login user %q with the wrong password", req.Username)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			atomic.AddInt32(&logins, 1)
			http.SetCookie(w, &http.Cookie{Name: "roster_session", Value: token, Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
		case "/api/v1/bots":
			atomic.AddInt32(&reads, 1)
			c, err := r.Cookie("roster_session")
			if err != nil || c.Value != token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{
				"bots": [
					{"name":"Ember","guid":123,"class":"Druid","level":60,"hp":900,"max_hp":1000,
					 "power":450,"max_power":500,"power_type":"mana","map":0,"zone":"Elwynn Forest",
					 "instance":"","x":1.5,"y":2.5,"z":3.5,"o":1.2,"target":"","group":"g1",
					 "kind":"pool","state":"moving","role":"tank","strategy":"assist",
					 "xp":{"level":60,"percent":12},"a_new_field":[1,2,3]},
					{"name":"Cinder","guid":"456","class":11,"level":60,"hp":0,"max_hp":1000,
					 "power":0,"max_power":100,"power_type":1,"map":1,"zone":"Durotar","instance":2,
					 "x":0,"y":0,"z":0,"o":0,"target":"Ember","group":"g1","kind":"hired","state":"idle"}
				],
				"heartbeat": {"bots":2,"humans":1,"states":{"idle":0.5,"moving":0.5},"extra":true},
				"a_new_top_level": "ignored"
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 2*time.Second)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	r, err := c.Roster(context.Background())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if n := atomic.LoadInt32(&logins); n != 1 {
		t.Fatalf("%d logins, want 1", n)
	}
	if n := atomic.LoadInt32(&reads); n != 1 {
		t.Fatalf("%d reads, want 1", n)
	}
	if len(r.Bots) != 2 {
		t.Fatalf("%d bots, want 2", len(r.Bots))
	}
	ember, cinder := r.Bots[0], r.Bots[1]
	if ember.Name != "Ember" || ember.GUID != "123" || ember.Class != "Druid" || ember.Level != 60 {
		t.Errorf("first bot = %+v", ember)
	}
	if ember.HP != 900 || ember.MaxHP != 1000 || ember.Power != 450 || ember.MaxPower != 500 || ember.PowerType != "mana" {
		t.Errorf("first bot's health or power = %+v", ember)
	}
	if ember.Map != 0 || ember.Zone != "Elwynn Forest" || ember.Instance != "" {
		t.Errorf("first bot's place = %+v", ember)
	}
	if ember.X != 1.5 || ember.Y != 2.5 || ember.Z != 3.5 || ember.O != 1.2 {
		t.Errorf("first bot's coordinates = %+v", ember)
	}
	if ember.Target != "" || ember.Group != "g1" || ember.Kind != KindPool || ember.State != StateMoving {
		t.Errorf("first bot's target, group or state = %+v", ember)
	}
	if ember.Role != "tank" || ember.Strategy != "assist" {
		t.Errorf("first bot's role or strategy = %q, %q", ember.Role, ember.Strategy)
	}
	if cinder.GUID != "456" || cinder.Class != "11" || cinder.PowerType != "1" || cinder.Instance != "2" {
		t.Errorf("a numeric identifier field should keep its decimal text: %+v", cinder)
	}
	if cinder.Role != "" || cinder.Strategy != "" {
		t.Errorf("an absent optional field should stay empty: %q, %q", cinder.Role, cinder.Strategy)
	}
	if r.Heartbeat == nil || r.Heartbeat.Bots != 2 || r.Heartbeat.Humans != 1 {
		t.Fatalf("heartbeat = %+v", r.Heartbeat)
	}
	if got := r.Heartbeat.States["idle"]; got != 0.5 {
		t.Errorf("heartbeat idle ratio = %v, want 0.5", got)
	}
	if got := r.Heartbeat.States["moving"]; got != 0.5 {
		t.Errorf("heartbeat moving ratio = %v, want 0.5", got)
	}
}

func TestRosterAcceptsBareArrayAndAbsentHeartbeat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		fmt.Fprint(w, `[{"name":"Ash","guid":1,"kind":"pool","state":"dead","hp":0,"max_hp":0,"map":0,"instance":"","x":0,"y":0,"z":0}]`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 2*time.Second)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	r, err := c.Roster(context.Background())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(r.Bots) != 1 || r.Bots[0].State != StateDead || r.Bots[0].Level != 0 {
		t.Fatalf("roster = %+v", r)
	}
	if r.Heartbeat != nil {
		t.Fatalf("heartbeat = %+v, want nil when the dashboard sends none", r.Heartbeat)
	}
}

func TestRosterReloginsOnceOn401(t *testing.T) {
	var logins, reads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			atomic.AddInt32(&logins, 1)
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		if r.URL.Path == "/api/v1/status" {
			// The heartbeat fallback's own read, not a roster read.
			fmt.Fprint(w, `{"bots":0,"humans":0,"states":{}}`)
			return
		}
		atomic.AddInt32(&reads, 1)
		c, err := r.Cookie("s")
		if err != nil || c.Value != "v" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"bots":[]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 2*time.Second)
	r, err := c.Roster(context.Background())
	if err != nil {
		t.Fatalf("Roster after the 401: %v", err)
	}
	if len(r.Bots) != 0 {
		t.Fatalf("bots = %+v, want none", r.Bots)
	}
	if n := atomic.LoadInt32(&logins); n != 1 {
		t.Errorf("%d logins, want 1", n)
	}
	if n := atomic.LoadInt32(&reads); n != 2 {
		t.Errorf("%d reads, want 2 (the 401 and its retry)", n)
	}
}

func TestDecodeRosterRefusesContractBreaks(t *testing.T) {
	// A record-level break leaves the answer usable: the record is rejected, not the roster.
	records := []struct {
		name string
		body string
		want string
	}{
		{"level is a string", `{"bots":[{"name":"A","guid":1,"kind":"pool","state":"idle","level":"60"}]}`, "level"},
		{"name is a number", `{"bots":[{"name":7,"guid":1,"kind":"pool","state":"idle"}]}`, "name"},
		{"guid is a bool", `{"bots":[{"name":"A","guid":true,"kind":"pool","state":"idle"}]}`, "string or a number"},
		{"no guid", `{"bots":[{"name":"A","kind":"pool","state":"idle"}]}`, "guid"},
		{"no name", `{"bots":[{"guid":1,"kind":"pool","state":"idle"}]}`, "name"},
		{"no kind", `{"bots":[{"name":"A","guid":1,"state":"idle"}]}`, "kind"},
		{"unknown kind", `{"bots":[{"name":"A","guid":1,"kind":"pet","state":"idle"}]}`, "kind"},
		{"unknown state", `{"bots":[{"name":"A","guid":1,"kind":"pool","state":"sleeping"}]}`, "state"},
		{"not an object", `{"bots":[5]}`, "record"},
		{"null record", `{"bots":[null]}`, "name"},
	}
	for _, tc := range records {
		t.Run(tc.name, func(t *testing.T) {
			r, err := DecodeRoster([]byte(tc.body))
			if err != nil {
				t.Fatalf("a bad record failed the whole answer: %v", err)
			}
			if len(r.Bots) != 0 || len(r.Rejected) != 1 {
				t.Fatalf("bots %+v, rejections %+v", r.Bots, r.Rejected)
			}
			if !strings.Contains(r.Rejected[0].Reason, tc.want) {
				t.Fatalf("reason %q does not name %q", r.Rejected[0].Reason, tc.want)
			}
		})
	}

	// An answer that cannot be a roster at all is still an error.
	structural := []struct {
		name string
		body string
		want string
	}{
		{"no bots list", `{"heartbeat":{"bots":0,"humans":0}}`, "bots list"},
		{"null bots list", `{"bots":null}`, "bots list"},
		{"empty answer", ``, "empty"},
		{"not JSON", `hello`, "neither"},
		{"top-level scalar", `7`, "neither"},
	}
	for _, tc := range structural {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeRoster([]byte(tc.body))
			if err == nil {
				t.Fatal("DecodeRoster accepted the answer")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestRosterRequestIsBoundedByItsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	c := newTestClient(t, srv.URL, 60*time.Millisecond)
	start := time.Now()
	err := c.Login(context.Background())
	if err == nil {
		t.Fatal("Login succeeded against a server that never answers")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the login took %s, want it bounded by the client timeout", elapsed)
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "timeout") && !strings.Contains(msg, "deadline") {
		t.Fatalf("error %q does not name the timeout", err)
	}
}

func TestPasswordSourceFromEnvironment(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			t.Errorf("login body: %v", err)
		}
		got = req.Password
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 2*time.Second)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got != testPassword {
		t.Fatalf("the login sent %q, want the environment value", got)
	}

	t.Setenv(testEnv, "")
	src := tbdash.PasswordEnv(testEnv)
	if _, err := src.Password(); err == nil || !strings.Contains(err.Error(), testEnv) {
		t.Fatalf("empty environment: %v, want an error naming %s", err, testEnv)
	}
	if _, err := src.Password(); err != nil && strings.Contains(err.Error(), testPassword) {
		t.Fatalf("the error leaks the password: %v", err)
	}
}

func TestPasswordFileWithLooseModeIsRefused(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "dash.pass")
	if err := os.WriteFile(path, []byte(testPassword+"\n"), 0o644); err != nil {
		t.Fatalf("writing the password file: %v", err)
	}
	c, err := New(srv.URL, testUser, tbdash.PasswordFile(path), Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = c.Login(context.Background())
	if err == nil {
		t.Fatal("Login accepted a 0644 password file")
	}
	if !strings.Contains(err.Error(), "0644") {
		t.Fatalf("error %q does not name the mode", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Fatalf("error %q leaks the password", err)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("%d requests reached the server, want 0", n)
	}
}

func TestRosterNon200IsAnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, time.Second)
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, err := c.Roster(context.Background())
	var httpErr *tbdash.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusInternalServerError {
		t.Fatalf("Roster = %v, want *HTTPError 500", err)
	}
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// TestRosterTakesTheHeartbeatFromStatus is #236 point 2: this port answers GET /api/v1/bots with a bare list and
// keeps the heartbeat on GET /api/v1/status, so a bots answer without one asks the status route. A dashboard with
// neither leaves the roster without a heartbeat and is not an error.
func TestRosterTakesTheHeartbeatFromStatus(t *testing.T) {
	var statusReads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
		case "/api/v1/bots":
			fmt.Fprint(w, `[{"name":"Ash","guid":1,"kind":"pool","state":"busy","hp":10,"max_hp":10,"map":0,"instance":"","x":0,"y":0,"z":0}]`)
		case "/api/v1/status":
			atomic.AddInt32(&statusReads, 1)
			fmt.Fprint(w, `{"online":true,"seq":7,"diff":12.5,"humans":2,"bots":3,"states":{"busy":0.5,"idle":0.5}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 2*time.Second)
	r, err := c.Roster(context.Background())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(r.Bots) != 1 || r.Bots[0].State != StateBusy {
		t.Fatalf("bots = %+v, want the busy bot kept", r.Bots)
	}
	if r.Heartbeat == nil {
		t.Fatal("no heartbeat: the status route's counts were not read")
	}
	if r.Heartbeat.Bots != 3 || r.Heartbeat.Humans != 2 || r.Heartbeat.States["busy"] != 0.5 {
		t.Fatalf("heartbeat = %+v", r.Heartbeat)
	}
	if n := atomic.LoadInt32(&statusReads); n != 1 {
		t.Fatalf("%d status reads, want 1", n)
	}
}

// TestRosterWithoutAStatusRouteIsStillARoster: a dashboard that has no status route is a roster without a heartbeat,
// never a failed read.
func TestRosterWithoutAStatusRouteIsStillARoster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 2*time.Second)
	r, err := c.Roster(context.Background())
	if err == nil {
		t.Fatalf("Roster = %+v, want the refused bots read", r)
	}

	// The same dashboard with a bots answer but no status route: the roster stands, without a heartbeat.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
		case "/api/v1/bots":
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv2.Close()
	c2 := newTestClient(t, srv2.URL, 2*time.Second)
	r2, err := c2.Roster(context.Background())
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if r2.Heartbeat != nil {
		t.Fatalf("heartbeat = %+v, want nil", r2.Heartbeat)
	}
}
