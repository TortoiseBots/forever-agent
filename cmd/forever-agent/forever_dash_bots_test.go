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
		fmt.Fprintf(w, `{"bots":[{"name":"Ember","guid":1,"class":"Druid","level":60,"kind":"pool","state":%q}]}`, state)
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
