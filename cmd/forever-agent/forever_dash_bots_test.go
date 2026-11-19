package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The tests use made-up accounts, names and passwords only.
const dashTestPassword = "not-a-real-dash-password"

func TestDashBotsUsageRequiresInputs(t *testing.T) {
	t.Setenv(dashBotsPasswordEnv, "")
	var stdout, stderr bytes.Buffer
	if code := foreverDashBotsCmd(context.Background(), nil, &stdout, &stderr); code != exitUsage {
		t.Fatalf("no flags: exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "--url") {
		t.Fatalf("stderr %q does not name the usage", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	args := []string{"--url", "http://127.0.0.1:1", "--user", "DashUser"}
	if code := foreverDashBotsCmd(context.Background(), args, &stdout, &stderr); code != exitUsage {
		t.Fatalf("no password: exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), dashBotsPasswordEnv) {
		t.Fatalf("stderr %q does not name %s", stderr.String(), dashBotsPasswordEnv)
	}
}

func TestDashBotsPrintsASanitisedRoster(t *testing.T) {
	t.Setenv(dashBotsPasswordEnv, dashTestPassword)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			var req struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := decodeJSONBody(r, &req); err != nil || req.Password != dashTestPassword {
				t.Errorf("login body %+v, %v", req, err)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		fmt.Fprint(w, `{"bots":[{
			"name":"Ember\u001b[31m","guid":7,"class":"Druid","level":60,"hp":900,"max_hp":1000,
			"power":450,"max_power":500,"power_type":"mana","map":0,"zone":"Elwynn Forest",
			"instance":"","x":1.5,"y":2.5,"z":3.5,"o":1.2,"target":"","group":"g1",
			"kind":"pool","state":"moving"}],
			"heartbeat":{"bots":1,"humans":2,"states":{"idle":1}}}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	args := []string{"--url", srv.URL, "--user", "DashUser"}
	if code := foreverDashBotsCmd(context.Background(), args, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "roster: 1 bot(s), 2 human(s); states idle 1") {
		t.Fatalf("stdout %q does not carry the heartbeat", out)
	}
	if !strings.Contains(out, "Ember") {
		t.Fatalf("stdout %q does not name the bot", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("stdout carries a terminal escape: %q", out)
	}
	if strings.Contains(out, dashTestPassword) || strings.Contains(stderr.String(), dashTestPassword) {
		t.Fatal("the password reached the output")
	}
}

func TestDashBotsWatchReportsADeath(t *testing.T) {
	t.Setenv(dashBotsPasswordEnv, dashTestPassword)
	var reads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		state := "idle"
		if atomic.AddInt32(&reads, 1) > 1 {
			state = "dead"
		}
		fmt.Fprintf(w, `{"bots":[{"name":"Ember","guid":1,"class":"Druid","level":60,"hp":900,"max_hp":1000,"map":0,"instance":"","x":0,"y":0,"z":0,"kind":"pool","state":%q}]}`, state)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	args := []string{"--url", srv.URL, "--user", "DashUser", "--watch", "--interval", "100ms", "--timeout", "2s"}
	if code := foreverDashBotsCmd(ctx, args, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "death  Ember died") {
		t.Fatalf("stdout %q does not report the death", out)
	}
	if n := atomic.LoadInt32(&reads); n < 2 {
		t.Fatalf("%d reads, want at least 2", n)
	}
}

func TestDashBotsRefusesALoosePasswordFile(t *testing.T) {
	t.Setenv(dashBotsPasswordEnv, "")
	path := filepath.Join(t.TempDir(), "dash.pass")
	if err := os.WriteFile(path, []byte(dashTestPassword+"\n"), 0o644); err != nil {
		t.Fatalf("writing the password file: %v", err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"--url", "http://127.0.0.1:1", "--user", "DashUser", "--pass-file", path}
	if code := foreverDashBotsCmd(context.Background(), args, &stdout, &stderr); code != exitDashBotsFailed {
		t.Fatalf("exit %d, want %d", code, exitDashBotsFailed)
	}
	if !strings.Contains(stderr.String(), "0644") {
		t.Fatalf("stderr %q does not name the mode", stderr.String())
	}
	if strings.Contains(stderr.String(), dashTestPassword) || strings.Contains(stdout.String(), dashTestPassword) {
		t.Fatal("the password reached the output")
	}
}

func decodeJSONBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func TestDashBotsReportsSkippedRecords(t *testing.T) {
	t.Setenv(dashBotsPasswordEnv, dashTestPassword)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "v", Path: "/"})
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		fmt.Fprint(w, `{"bots":[
			{"name":"Ember","guid":1,"class":"Druid","level":60,"hp":900,"max_hp":1000,"map":0,"instance":"","x":1,"y":2,"z":3,"group":"g1","kind":"pool","state":"idle"},
			{"name":"Cinder","guid":2,"kind":"pool","state":"idle"}]}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	args := []string{"--url", srv.URL, "--user", "DashUser"}
	if code := foreverDashBotsCmd(context.Background(), args, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Ember") {
		t.Fatalf("stdout %q does not show the good record", stdout.String())
	}
	if strings.Contains(stdout.String(), "Cinder") {
		t.Fatalf("stdout %q shows a rejected record as a bot", stdout.String())
	}
	for _, want := range []string{"bot record 1", "Cinder", "skipped", "hp is missing"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr %q does not name %q", stderr.String(), want)
		}
	}
}

func TestDashBotsRefusesPlainHTTPWithoutTheFlag(t *testing.T) {
	t.Setenv(dashBotsPasswordEnv, dashTestPassword)
	var stdout, stderr bytes.Buffer
	args := []string{"--url", "http://192.0.2.10:8790", "--user", "DashUser"}
	if code := foreverDashBotsCmd(context.Background(), args, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "clear text") {
		t.Fatalf("stderr %q does not explain the plain-http boundary", stderr.String())
	}
}

func TestDashBotsInsecureHTTPFlagWarnsAndAllows(t *testing.T) {
	t.Setenv(dashBotsPasswordEnv, dashTestPassword)
	var stdout, stderr bytes.Buffer
	args := []string{"--url", "http://192.0.2.10:1", "--user", "DashUser", "--dash-insecure-http", "--timeout", "1s"}
	code := foreverDashBotsCmd(context.Background(), args, &stdout, &stderr)
	if code != exitDashBotsFailed {
		t.Fatalf("exit %d, want %d (the login cannot reach the address)", code, exitDashBotsFailed)
	}
	if !strings.Contains(stderr.String(), "warning: plain HTTP dashboard") {
		t.Fatalf("stderr %q does not carry the warning", stderr.String())
	}
	if strings.Contains(stderr.String(), "clear text; use https") {
		t.Fatalf("the flag was still refused: %q", stderr.String())
	}
}
