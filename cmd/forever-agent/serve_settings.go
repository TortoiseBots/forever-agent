package main

// How a Forever slot's settings file fills `forever serve`'s flags: a flag the command line set
// always wins, then the file, then the flag's default.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/servesettings"
	"github.com/pjerra/forever-agent/internal/privfile"
)

// serveFlagOverrides are the flags a settings file may fill. The pointers are the flag package's
// own values, so filling one fills the parsed flag; set (flag.Visit) says which names the command
// line gave, and those are left alone. --create, --create-choice, --create-no-appearance and
// --login-budget stay command-line only: they are a one-off character or a fleet child's wiring.
type serveFlagOverrides struct {
	listen, dir, tokenFile *string
	navmesh, plansDir      *string
	captureDir, addon      *string
	noAuth, allowGMUnits   *bool
	parents                mapPairs
	grace                  *time.Duration
}

// serveFromSettings fills every serve flag the command line did not set.
func serveFromSettings(st servesettings.Settings, set map[string]bool, c *foreverCommon, v serveFlagOverrides) {
	if !set["rest"] && st.Rest != "" {
		c.restAddr = st.Rest
	}
	if !set["rpc"] && st.RPC != "" {
		c.rpcAddr = st.RPC
	}
	if !set["ca"] && st.CA != "" {
		c.caFile = st.CA
	}
	if !set["server-name"] && st.ServerName != "" {
		c.serverName = st.ServerName
	}
	if !set["build"] && st.Build != 0 {
		c.clientBuild = uint(st.Build)
	}
	if !set["account"] && st.Account != "" {
		c.account = st.Account
	}
	if !set["game-account"] && st.GameAccount != 0 {
		c.gameAccount = st.GameAccount
	}
	if !set["locale"] && st.Locale != "" {
		c.locale = st.Locale
	}
	if !set["character"] && st.Character != "" {
		c.character = st.Character
	}
	if !set["allow-instance-redirect"] && st.AllowInstanceRedirect != nil {
		c.allowRedirect = *st.AllowInstanceRedirect
	}
	if !set["world-checks-build-auth-key"] && st.WorldChecksBuildAuthKey != nil {
		c.worldChecksBuildAuthKey = *st.WorldChecksBuildAuthKey
	}
	if !set["listen"] && st.Listen != "" {
		*v.listen = st.Listen
	}
	if !set["dir"] && st.Dir != "" {
		*v.dir = st.Dir
	}
	if !set["token-file"] && st.TokenFile != "" {
		*v.tokenFile = st.TokenFile
	}
	if !set["no-auth"] && st.NoAuth != nil {
		*v.noAuth = *st.NoAuth
	}
	if !set["navmesh"] && st.Navmesh != "" {
		*v.navmesh = st.Navmesh
	}
	if !set["plans-dir"] && st.PlansDir != "" {
		*v.plansDir = st.PlansDir
	}
	if !set["parent"] {
		for from, parent := range st.Parents {
			v.parents[from] = parent
		}
	}
	if !set["disconnect-grace"] && st.DisconnectGrace != nil {
		*v.grace = time.Duration(*st.DisconnectGrace)
	}
	if !set["allow-gm-units"] && st.AllowGMUnits != nil {
		*v.allowGMUnits = *st.AllowGMUnits
	}
	if !set["addon"] && st.Addon != "" {
		*v.addon = strings.ToLower(st.Addon)
	}
	if !set["capture-dir"] && st.CaptureDir != "" {
		*v.captureDir = st.CaptureDir
	}
}

// servePasswordFor resolves the password: an explicit --password-file wins over the settings file
// (an empty one means the environment), the file's password_file comes before an inline password,
// and FOREVER_PASSWORD is the last resort. The flag's file keeps the old loose read; a settings
// file's password_file is strict. The value is never printed on any path.
func servePasswordFor(passwordFileSet bool, passwordFile string, st *servesettings.Settings) (string, error) {
	if passwordFileSet {
		if passwordFile == "" {
			return os.Getenv("FOREVER_PASSWORD"), nil
		}
		return servePassword(passwordFile)
	}
	if st != nil {
		if st.PasswordFile != "" {
			return serveSettingsPassword(st.PasswordFile)
		}
		if st.Password != "" {
			return st.Password, nil
		}
	}
	return os.Getenv("FOREVER_PASSWORD"), nil
}

// serveSettingsPassword reads the file a settings file's password_file names. Unlike --password-file,
// which has always read any file the user can read, this file is named by a settings file that is
// itself private, so it must be private too: privfile.Read refuses a symlink, a loose mode and a
// file swapped between its check and its open. The error names the path and the problem, never a
// value.
func serveSettingsPassword(path string) (string, error) {
	raw, err := privfile.Read(path, 4096+1)
	if err != nil {
		return "", fmt.Errorf("settings password_file %s: %v", path, err)
	}
	if len(raw) > 4096 {
		return "", fmt.Errorf("settings password_file %s: more than 4096 bytes", path)
	}
	s := strings.TrimSuffix(string(raw), "\n")
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		return "", fmt.Errorf("settings password_file %s is empty", path)
	}
	return s, nil
}
