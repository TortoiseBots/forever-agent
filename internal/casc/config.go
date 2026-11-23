package casc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeyPair is a build config key pair: the content key (MD5 of the decoded file) and the encoding key that addresses
// its encoded span in the data archives.
type KeyPair struct {
	CKey []byte
	EKey []byte
}

// Config is a parsed build config: the encoding and root keys, and every other key = value line.
type Config struct {
	Encoding KeyPair             // the encoding file: its content key and the encoding key of its span
	Root     []byte              // the root file's content key; resolved through the encoding file
	Values   map[string][]string // every line, keyed by name, with its space-separated value tokens
}

// ParseConfig parses the text of a build config file: blank lines and lines starting with # are skipped, and each
// other line is key = value with the value split on spaces.
func ParseConfig(data []byte) (*Config, error) {
	cfg := &Config{Values: map[string][]string{}}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		cfg.Values[strings.TrimSpace(key)] = strings.Fields(value)
	}
	enc := cfg.Values["encoding"]
	if len(enc) < 2 {
		return nil, errors.New("casc: build config has no encoding key pair")
	}
	ckey, err := hexKey(enc[0], "encoding content key")
	if err != nil {
		return nil, err
	}
	ekey, err := hexKey(enc[1], "encoding key")
	if err != nil {
		return nil, err
	}
	cfg.Encoding = KeyPair{CKey: ckey, EKey: ekey}
	root := cfg.Values["root"]
	if len(root) < 1 {
		return nil, errors.New("casc: build config has no root key")
	}
	if cfg.Root, err = hexKey(root[0], "root key"); err != nil {
		return nil, err
	}
	return cfg, nil
}

// readConfig reads the build config that buildKey names: Data/config/<h[0:2]>/<h[2:4]>/<h>.
func readConfig(installDir, buildKey string) (*Config, error) {
	h := strings.ToLower(strings.TrimSpace(buildKey))
	if len(h) != 32 {
		return nil, fmt.Errorf("casc: build key %q is not 32 hex characters", buildKey)
	}
	if _, err := hex.DecodeString(h); err != nil {
		return nil, fmt.Errorf("casc: build key %q is not hex: %w", buildKey, err)
	}
	path := filepath.Join(installDir, "Data", "config", h[:2], h[2:4], h)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(data)
}

// hexKey decodes a 16-byte hex key and names it in the error.
func hexKey(s, what string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return nil, fmt.Errorf("casc: build config %s %q is not a 16-byte hex key", what, s)
	}
	return b, nil
}
