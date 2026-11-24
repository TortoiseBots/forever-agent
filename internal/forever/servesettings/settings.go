// Package servesettings loads the settings file a Forever slot is started from (`forever serve
// --settings FILE`): the account, the character, the login endpoints, the navmesh and plans
// folders, and the rest of serve's flags. A flag on the command line always wins over the file.
//
// The file may hold the account password, so no value read here may reach a log line, an error,
// an event or a capture. Load refuses a file that anyone but its owner can read, and an error
// names a field, never a value.
package servesettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/slots"
)

// maxFileBytes bounds the file a slot's settings.json may be; a real one is a few hundred bytes.
const maxFileBytes = 256 << 10

// MapPairs is a parent map in the file: {"36": 43}. The instance map keys are JSON object keys,
// so they arrive as strings and are parsed here.
type MapPairs map[uint32]uint32

func (m *MapPairs) UnmarshalJSON(b []byte) error {
	var raw map[string]uint32
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf(`want {"<map>": <parent map>}`)
	}
	out := make(MapPairs, len(raw))
	for k, parent := range raw {
		from, err := strconv.ParseUint(k, 10, 32)
		if err != nil || from == 0 {
			return fmt.Errorf("%q is not a map id", k)
		}
		if parent == 0 {
			return fmt.Errorf("map %s: parent map 0", k)
		}
		out[uint32(from)] = parent
	}
	*m = out
	return nil
}

// Duration is a duration written as a Go duration string ("30s", "2m"): the same text serve's
// flags take, so a file and a command line read alike.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New(`want a duration string such as "30s"`)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("want a duration such as %q: %v", "30s", err)
	}
	*d = Duration(v)
	return nil
}

// Settings is the Forever serve settings file. Unknown keys are refused, like the slots file and
// the Turtle settings file: a misspelt key must not silently leave a default in place.
type Settings struct {
	Account   string `json:"account,omitempty"`
	Character string `json:"character,omitempty"`

	Rest        string `json:"rest,omitempty"`
	RPC         string `json:"rpc,omitempty"`
	CA          string `json:"ca,omitempty"`
	ServerName  string `json:"server_name,omitempty"`
	Build       uint64 `json:"build,omitempty"`
	GameAccount int    `json:"game_account,omitempty"`
	Locale      string `json:"locale,omitempty"`

	AllowInstanceRedirect   *bool `json:"allow_instance_redirect,omitempty"`
	WorldChecksBuildAuthKey *bool `json:"world_checks_build_auth_key,omitempty"`

	Listen    string `json:"listen,omitempty"`
	Dir       string `json:"dir,omitempty"`
	TokenFile string `json:"token_file,omitempty"`
	NoAuth    *bool  `json:"no_auth,omitempty"`

	Navmesh  string   `json:"navmesh,omitempty"`
	PlansDir string   `json:"plans_dir,omitempty"`
	Parents  MapPairs `json:"parents,omitempty"`

	Addon           string    `json:"addon,omitempty"`
	CaptureDir      string    `json:"capture_dir,omitempty"`
	DisconnectGrace *Duration `json:"disconnect_grace,omitempty"`
	AllowGMUnits    *bool     `json:"allow_gm_units,omitempty"`

	// PasswordFile names a file holding the password; Password holds it inline. Both is refused,
	// and the file pointing at a password file is the form to prefer: it keeps the secret out of
	// a file that is also a slot's record. Neither is ever printed.
	Password     string `json:"password,omitempty"`
	PasswordFile string `json:"password_file,omitempty"`
}

// Load reads the settings file at path. A missing file, a file another user can read, malformed
// JSON, an unknown key and a value out of range are each a clear error that names the file and the
// field, never a value.
func Load(path string) (Settings, error) {
	p := slots.ExpandTilde(path)
	info, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Settings{}, fmt.Errorf("settings %s: no such file", p)
		}
		return Settings{}, fmt.Errorf("settings %s: %v", p, err)
	}
	if !info.Mode().IsRegular() {
		return Settings{}, fmt.Errorf("settings %s: not a regular file", p)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return Settings{}, fmt.Errorf("settings %s has mode %04o; it may name a slot's account and password, run: chmod 600 %s", p, perm, p)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Settings{}, fmt.Errorf("settings %s: %v", p, err)
	}
	if len(raw) > maxFileBytes {
		return Settings{}, fmt.Errorf("settings %s: larger than %d bytes", p, maxFileBytes)
	}
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Settings{}, fmt.Errorf("settings %s is not valid settings JSON: %s", p, jsonFieldError(err))
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Settings{}, fmt.Errorf("settings %s is not valid settings JSON: trailing data after the object", p)
	}
	if err := s.validate(); err != nil {
		return Settings{}, fmt.Errorf("settings %s: %v", p, err)
	}
	dir := filepath.Dir(p)
	resolve := func(v string) string { return resolvePath(dir, v) }
	s.CA = resolve(s.CA)
	s.Dir = resolve(s.Dir)
	s.TokenFile = resolve(s.TokenFile)
	s.Navmesh = resolve(s.Navmesh)
	s.PlansDir = resolve(s.PlansDir)
	s.CaptureDir = resolve(s.CaptureDir)
	s.PasswordFile = resolve(s.PasswordFile)
	return s, nil
}

// jsonFieldError keeps the field name of the decoder's unknown-field error (not a value) and drops
// the "json: " prefix.
func jsonFieldError(err error) string {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, "json: "); ok {
		return rest
	}
	return msg
}

// resolvePath expands a leading ~/ and makes a relative path relative to the settings file's
// folder: a settings.json names the slot's own files, not the folder serve happens to start in.
func resolvePath(dir, p string) string {
	if p == "" {
		return ""
	}
	p = slots.ExpandTilde(p)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

func (s *Settings) validate() error {
	if s.Password != "" && s.PasswordFile != "" {
		return errors.New(`"password" and "password_file" are both set: give one, preferably password_file`)
	}
	if s.Build > 0xFFFFFFFF {
		return errors.New(`"build" must be a client build number`)
	}
	if s.GameAccount < 0 {
		return errors.New(`"game_account" is 1 or more`)
	}
	if s.Locale != "" && !bnet.ValidLocale(s.Locale) {
		return fmt.Errorf(`"locale" must be one of %s`, strings.Join(bnet.Locales, ", "))
	}
	if s.Addon != "" {
		switch strings.ToLower(s.Addon) {
		case "on", "off":
		default:
			return errors.New(`"addon" must be "on" or "off"`)
		}
	}
	if s.DisconnectGrace != nil {
		if d := time.Duration(*s.DisconnectGrace); d < 0 || d > 10*time.Minute {
			return errors.New(`"disconnect_grace" must be 0 to 10m`)
		}
	}
	return nil
}
