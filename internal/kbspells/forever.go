package kbspells

import (
	"embed"
	"fmt"
	"sync"

	"github.com/pjerra/forever-agent/internal/foreverspells"
)

//go:embed forever/*.json
var foreverFiles embed.FS

var (
	foreverOnce sync.Once
	foreverDef  *Oracle
	foreverErr  error
)

// ForeverDefault is the oracle the agent ships for Classic 1.60 fights: the hand-written per-class lists of
// internal/kbspells/forever (the classes and specs of the Forever bot engine, cross-checked with Classic rotations),
// validated against the Forever spell table the build carries. It returns an error, never panics, when the build has
// no spell data or the data lacks an oracle name, so a build without data still runs every other command.
func ForeverDefault() (*Oracle, error) {
	foreverOnce.Do(func() { foreverDef, foreverErr = loadForever() })
	return foreverDef, foreverErr
}

func loadForever() (*Oracle, error) {
	table := foreverspells.Default()
	if !table.Loaded() {
		return nil, fmt.Errorf("the Forever oracle needs the generated spell data (internal/foreverspells/data); this build has none")
	}
	o, err := load(foreverFiles, "forever", "forever/_common.json", table)
	if err != nil {
		return nil, fmt.Errorf("forever oracle: %w", err)
	}
	return o, nil
}
