package servesettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/privfile"
)

const testPassword = "Zq7-fixture-pw-9141"

// writeSettings writes body as a private settings file and returns its path.
func writeSettings(t *testing.T, dir, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsEveryServeSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	body := `{
	  "account": "user@example.invalid",
	  "character": "Slotchar",
	  "rest": "127.0.0.1:1119",
	  "rpc": "127.0.0.1:1118",
	  "ca": "config/ca.pem",
	  "server_name": "example.invalid",
	  "build": 70959,
	  "game_account": 2,
	  "locale": "enUS",
	  "allow_instance_redirect": true,
	  "world_checks_build_auth_key": false,
	  "listen": "127.0.0.1:28771",
	  "dir": ".",
	  "token_file": "rpc.token",
	  "no_auth": true,
	  "navmesh": "~/maps/mmaps",
	  "plans_dir": "dungeons",
	  "parents": {"36": 43, "429": 44},
	  "addon": "off",
	  "capture_dir": "captures",
	  "disconnect_grace": "45s",
	  "allow_gm_units": true,
	  "password_file": "pw"
	}`
	s, err := Load(writeSettings(t, dir, body, 0o600))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Account != "user@example.invalid" || s.Character != "Slotchar" ||
		s.Rest != "127.0.0.1:1119" || s.RPC != "127.0.0.1:1118" ||
		s.ServerName != "example.invalid" || s.Build != 70959 || s.GameAccount != 2 || s.Locale != "enUS" {
		t.Fatalf("connection facts: %+v", s)
	}
	if s.AllowInstanceRedirect == nil || !*s.AllowInstanceRedirect ||
		s.WorldChecksBuildAuthKey == nil || *s.WorldChecksBuildAuthKey ||
		s.NoAuth == nil || !*s.NoAuth || s.AllowGMUnits == nil || !*s.AllowGMUnits {
		t.Fatalf("bool settings did not keep set-and-false apart: %+v", s)
	}
	if s.Listen != "127.0.0.1:28771" || s.Addon != "off" || s.DisconnectGrace == nil || time.Duration(*s.DisconnectGrace) != 45*time.Second {
		t.Fatalf("serve settings: %+v", s)
	}
	if s.Parents[36] != 43 || s.Parents[429] != 44 || len(s.Parents) != 2 {
		t.Fatalf("parents: %+v", s.Parents)
	}
	// A relative path names a file beside the settings file; ~/ is the home folder.
	if s.CA != filepath.Join(dir, "config/ca.pem") {
		t.Errorf("ca = %q, want it beside the settings file", s.CA)
	}
	if s.Dir != dir || s.TokenFile != filepath.Join(dir, "rpc.token") ||
		s.CaptureDir != filepath.Join(dir, "captures") || s.PlansDir != filepath.Join(dir, "dungeons") {
		t.Errorf("relative paths: dir=%q token=%q capture=%q plans=%q", s.Dir, s.TokenFile, s.CaptureDir, s.PlansDir)
	}
	if s.Navmesh != filepath.Join(home, "maps/mmaps") {
		t.Errorf("navmesh = %q, want the home expansion", s.Navmesh)
	}
	if s.PasswordFile != filepath.Join(dir, "pw") {
		t.Errorf("password_file = %q", s.PasswordFile)
	}
}

func TestLoadRefusesAnUnreadableOrBadFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing: %v", err)
	}
	for name, body := range map[string]string{
		"not json":      `{"account":`,
		"not an object": `[]`,
		"trailing":      `{"account":"a"} {"account":"b"}`,
	} {
		if _, err := Load(writeSettings(t, t.TempDir(), body, 0o600)); err == nil || !strings.Contains(err.Error(), "not valid settings JSON") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A misspelt key is refused and the error names the key, not a value.
	p := writeSettings(t, t.TempDir(), `{"account":"a@example.invalid","nonsense":1}`, 0o600)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), `unknown field "nonsense"`) {
		t.Errorf("unknown field: %v", err)
	}
	// Loose mode is refused: the file may name the account's password.
	if _, err := Load(writeSettings(t, t.TempDir(), `{"account":"a"}`, 0o644)); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("loose mode: %v", err)
	}
	// A symlink is not a settings file.
	real := writeSettings(t, t.TempDir(), `{"account":"a"}`, 0o600)
	link := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Error("a symlink was accepted")
	}
}

// TestLoadRefusesASettingsFileSwappedAfterTheCheck: the file is read through privfile.Read, which
// compares the opened file with the one it checked; a file moved over the path in that window is
// refused, so an attacker-substituted settings file cannot start the serve.
func TestLoadRefusesASettingsFileSwappedAfterTheCheck(t *testing.T) {
	dir := t.TempDir()
	path := writeSettings(t, dir, `{"account":"checked@example.invalid"}`, 0o600)
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(`{"account":"swapped@example.invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	privfile.AfterCheck = func() {
		if swapped {
			return
		}
		swapped = true
		if err := os.Rename(other, path); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { privfile.AfterCheck = nil }()
	s, err := Load(path)
	if err == nil {
		t.Fatalf("a settings file swapped after the check was loaded: %+v", s)
	}
	if !strings.Contains(err.Error(), "changed while being read") {
		t.Errorf("unclear swap error: %v", err)
	}
}

func TestLoadRefusesTwoSecretsAndBadValues(t *testing.T) {
	for name, body := range map[string]string{
		"password and password_file": `{"password":"` + testPassword + `","password_file":"pw"}`,
		"build":                      `{"build":4294967296}`,
		"negative game account":      `{"game_account":-1}`,
		"locale":                     `{"locale":"xxXX"}`,
		"addon":                      `{"addon":"maybe"}`,
		"grace":                      `{"disconnect_grace":"11m"}`,
		"parents map id":             `{"parents":{"zero":"12"}}`,
		"parents parent":             `{"parents":{"36":0}}`,
	} {
		p := writeSettings(t, t.TempDir(), body, 0o600)
		_, err := Load(p)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), testPassword) {
			t.Errorf("%s: the error leaks the password: %v", name, err)
		}
	}
	// An inline password is read but never in an error.
	if _, err := Load(writeSettings(t, t.TempDir(), `{"password":"`+testPassword+`","addon":"maybe"}`, 0o600)); err == nil || strings.Contains(err.Error(), testPassword) {
		t.Errorf("inline password error: %v", err)
	}
}
