package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/servesettings"
	"github.com/pjerra/forever-agent/internal/privfile"
)

// settableServeFlags registers serve's flags in a set the test can fill from a settings file.
type settableServeFlags struct {
	fs                     *flag.FlagSet
	common                 foreverCommon
	listen, dir, tokenFile *string
	navmesh, plansDir      *string
	captureDir, addon      *string
	noAuth, allowGMUnits   *bool
	parents                mapPairs
	grace                  *time.Duration
}

func newSettableServeFlags() *settableServeFlags {
	v := &settableServeFlags{fs: flag.NewFlagSet("serve", flag.ContinueOnError)}
	v.common.flags(v.fs)
	v.listen = v.fs.String("listen", "127.0.0.1:8765", "")
	v.dir = v.fs.String("dir", "", "")
	v.tokenFile = v.fs.String("token-file", "", "")
	v.noAuth = v.fs.Bool("no-auth", false, "")
	v.navmesh = v.fs.String("navmesh", "", "")
	v.plansDir = v.fs.String("plans-dir", "dungeons", "")
	v.parents = mapPairs{}
	v.fs.Var(v.parents, "parent", "")
	v.grace = v.fs.Duration("disconnect-grace", 30*time.Second, "")
	v.allowGMUnits = v.fs.Bool("allow-gm-units", false, "")
	v.addon = v.fs.String("addon", "on", "")
	return v
}

func (v *settableServeFlags) parse(t *testing.T, args ...string) map[string]bool {
	t.Helper()
	if err := v.fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	v.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func (v *settableServeFlags) overrides() serveFlagOverrides {
	return serveFlagOverrides{
		listen: v.listen, dir: v.dir, tokenFile: v.tokenFile, navmesh: v.navmesh, plansDir: v.plansDir,
		captureDir: v.captureDir, addon: v.addon, noAuth: v.noAuth, allowGMUnits: v.allowGMUnits,
		parents: v.parents, grace: v.grace,
	}
}

// TestServeSettingsFillOnlyTheFlagsTheCommandLineLeftUnset is the precedence rule: a flag wins,
// then the file, then the flag's default.
func TestServeSettingsFillOnlyTheFlagsTheCommandLineLeftUnset(t *testing.T) {
	v := newSettableServeFlags()
	dir := t.TempDir()
	v.captureDir = v.fs.String("capture-dir", "", "")
	set := v.parse(t, "--account", "cli@example.invalid", "--build", "123",
		"--parent", "36=43")
	st := servesettings.Settings{
		Account: "file@example.invalid", Character: "Filechar", Rest: "127.0.0.1:1119",
		RPC: "127.0.0.1:1118", CA: "/ca.pem", ServerName: "file.invalid", Build: 999, GameAccount: 2,
		Locale: "enUS", Listen: "127.0.0.1:9999", Dir: dir, TokenFile: dir + "/tok", Navmesh: dir + "/mm",
		PlansDir: dir + "/plans", Parents: servesettings.MapPairs{36: 44, 429: 45}, Addon: "off",
		CaptureDir: dir + "/cap", DisconnectGrace: new(servesettings.Duration(45 * time.Second)),
		AllowGMUnits: new(true),
	}
	serveFromSettings(st, set, &v.common, v.overrides())
	if v.common.account != "cli@example.invalid" || v.common.clientBuild != 123 {
		t.Fatalf("a command-line flag lost to the file: account %q build %d", v.common.account, v.common.clientBuild)
	}
	if v.common.character != "Filechar" || v.common.restAddr != "127.0.0.1:1119" ||
		v.common.rpcAddr != "127.0.0.1:1118" || v.common.caFile != "/ca.pem" ||
		v.common.gameAccount != 2 || v.common.locale != "enUS" {
		t.Fatalf("file values did not fill the unset flags: %+v", v.common)
	}
	if *v.listen != "127.0.0.1:9999" || *v.dir != dir || *v.tokenFile != dir+"/tok" ||
		*v.navmesh != dir+"/mm" || *v.plansDir != dir+"/plans" || *v.captureDir != dir+"/cap" {
		t.Fatalf("paths: listen %q dir %q token %q navmesh %q plans %q capture %q",
			*v.listen, *v.dir, *v.tokenFile, *v.navmesh, *v.plansDir, *v.captureDir)
	}
	if v.parents[36] != 43 || len(v.parents) != 1 {
		t.Fatalf("a repeated --parent lost to the file: %+v", v.parents)
	}
	if *v.grace != 45*time.Second || *v.addon != "off" || !*v.allowGMUnits {
		t.Fatalf("grace %v addon %q gm %v", *v.grace, *v.addon, *v.allowGMUnits)
	}
}

// TestServePasswordPrecedence: --password-file, then the file's password_file, then its inline
// password, then the environment.
func TestServePasswordPrecedence(t *testing.T) {
	dir := t.TempDir()
	flagFile := filepath.Join(dir, "flag-pw")
	settingsFile := filepath.Join(dir, "settings-pw")
	for path, secret := range map[string]string{flagFile: "flag-secret", settingsFile: "settings-file-secret"} {
		if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FOREVER_PASSWORD", "env-secret")

	st := &servesettings.Settings{PasswordFile: settingsFile}
	if got, err := servePasswordFor(true, flagFile, st); err != nil || got != "flag-secret" {
		t.Fatalf("flag file: %q %v", got, err)
	}
	if got, err := servePasswordFor(false, "", st); err != nil || got != "settings-file-secret" {
		t.Fatalf("settings password_file: %q %v", got, err)
	}
	if got, err := servePasswordFor(false, "", &servesettings.Settings{Password: "settings-inline"}); err != nil || got != "settings-inline" {
		t.Fatalf("settings password: %q %v", got, err)
	}
	// An explicit empty --password-file means the environment, even when the file has a secret.
	if got, err := servePasswordFor(true, "", st); err != nil || got != "env-secret" {
		t.Fatalf("explicit empty flag: %q %v", got, err)
	}
	if got, err := servePasswordFor(false, "", nil); err != nil || got != "env-secret" {
		t.Fatalf("environment: %q %v", got, err)
	}
}

// TestServeSettingsPasswordFileMustBePrivate: a settings file's password_file is a bearer secret,
// so it must be a regular, non-symlinked file only its owner can access, at most 4096 bytes. A
// loose mode, a symlink or an over-long file is refused, and the error names the path and the
// problem, never the content.
func TestServeSettingsPasswordFileMustBePrivate(t *testing.T) {
	dir := t.TempDir()
	const secret = "Zq7-settings-pw-5512"
	write := func(name, body string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	private := write("pw", secret+"\n", 0o600)
	loose := write("loose-pw", secret+"\n", 0o644)
	link := filepath.Join(dir, "link-pw")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	big := write("big-pw", strings.Repeat("x", 4097), 0o600)
	for name, path := range map[string]string{"loose": loose, "symlink": link, "over 4096 bytes": big} {
		got, err := servePasswordFor(false, "", &servesettings.Settings{PasswordFile: path})
		if err == nil {
			t.Errorf("%s: accepted (%q)", name, got)
			continue
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("%s: the error does not name the path: %v", name, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error leaks the password: %v", name, err)
		}
	}
	if got, err := servePasswordFor(false, "", &servesettings.Settings{PasswordFile: private}); err != nil || got != secret {
		t.Fatalf("private password file: %q %v", got, err)
	}
}

// TestServeSettingsPasswordFileSwappedAfterTheCheckIsRefused: the settings path is read through
// privfile.Read, so a password file replaced between its check and its open is refused instead of
// read.
func TestServeSettingsPasswordFileSwappedAfterTheCheckIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw")
	if err := os.WriteFile(path, []byte("checked-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other-pw")
	if err := os.WriteFile(other, []byte("swapped-secret\n"), 0o600); err != nil {
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
	got, err := servePasswordFor(false, "", &servesettings.Settings{PasswordFile: path})
	if err == nil || !strings.Contains(err.Error(), "changed while being read") {
		t.Fatalf("a password file swapped after the check was read: %q, %v", got, err)
	}
}

// TestServePasswordFlagKeepsItsLooseFileBehaviour: --password-file has always read any file the
// user can read (servePassword on main). Only a settings file's password_file is strict, so an
// existing flag user is not broken.
func TestServePasswordFlagKeepsItsLooseFileBehaviour(t *testing.T) {
	dir := t.TempDir()
	loose := filepath.Join(dir, "flag-pw")
	if err := os.WriteFile(loose, []byte("flag-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := servePasswordFor(true, loose, nil); err != nil || got != "flag-secret" {
		t.Fatalf("--password-file with mode 0644: %q %v", got, err)
	}
}

// TestServeSettingsPasswordFileIsRefusedBeforeTheLog: a password_file with a loose mode stops
// serve with a usage error that names the path and the fix, and the secret reaches no output.
func TestServeSettingsPasswordFileIsRefusedBeforeTheLog(t *testing.T) {
	dir := t.TempDir()
	const secret = "Zq7-serve-file-pw-7731"
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	body := `{"account":"TESTACC","character":"Gbmage","rest":"127.0.0.1:1","rpc":"127.0.0.1:2",
	  "ca":"` + filepath.Join(dir, "none.pem") + `","password_file":"` + pw + `",
	  "listen":"127.0.0.1:0"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	if code := foreverServeCmd(context.Background(), []string{"--settings", path}, &out, &errOut); code != exitUsage {
		t.Fatalf("loose password_file: exit %d, stdout %q stderr %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), pw) || !strings.Contains(errOut.String(), "chmod 600") {
		t.Errorf("the error does not name the path and the fix: %q", errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), secret) {
		t.Fatalf("the logs leak the password:\nstdout %q\nstderr %q", out.String(), errOut.String())
	}
}

// TestServeSettingsBadFileIsAUsageError: a missing or malformed settings file stops the command
// with a clear usage error before any login.
func TestServeSettingsBadFileIsAUsageError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	for name, path := range map[string]string{"missing": missing} {
		var out, errOut strings.Builder
		code := foreverServeCmd(context.Background(), []string{"--settings", path}, &out, &errOut)
		if code != exitUsage || !strings.Contains(errOut.String(), "no such file") {
			t.Errorf("%s: exit %d stderr %q", name, code, errOut.String())
		}
	}
	bad := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(bad, []byte(`{"account":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	if code := foreverServeCmd(context.Background(), []string{"--settings", bad}, &out, &errOut); code != exitUsage ||
		!strings.Contains(errOut.String(), "not valid settings JSON") {
		t.Fatalf("malformed: exit %d stderr %q", code, errOut.String())
	}
}

// TestServeSettingsKeepsTheSecretOutOfTheLog: a settings file with an inline password runs the
// command (the unreachable login address fails), and neither the captured output nor the error
// ever holds the secret.
func TestServeSettingsKeepsTheSecretOutOfTheLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	body := `{"account":"TESTACC","character":"Gbmage","rest":"127.0.0.1:1","rpc":"127.0.0.1:2",
	  "ca":"` + filepath.Join(dir, "none.pem") + `","password":"` + serveTestPassword + `",
	  "listen":"127.0.0.1:0"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := foreverServeCmd(context.Background(), []string{"--settings", path}, &out, &errOut)
	if code == exitOK {
		t.Fatalf("the unreachable login succeeded? stdout %q", out.String())
	}
	if strings.Contains(out.String()+errOut.String(), serveTestPassword) {
		t.Fatalf("the logs leak the password:\nstdout %q\nstderr %q", out.String(), errOut.String())
	}
}
