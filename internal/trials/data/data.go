// Package data holds the reference tables the scoring half reads: the spell table and the
// gear kits. Both are copied unchanged from tortoise-trials (data/*.json), where build_data.py
// made them from the public core's SQL; the bench only reads them.
//
// The `forever` subdirectory is the ForeverCore overlay: rows the live server proved (arenas,
// mobs, the bot gear kits and the spec markers), merged over the copied tables by key. An
// overlay row wins over the base row with the same key; an overlay file left empty adds
// nothing. Never guess a row here: an entry is added only after a read-back on the server.
package data

import (
	"embed"
	"fmt"
)

//go:embed *.json forever/*.json
var files embed.FS

// Read returns a data file's content (spells.json, gear_kits.json, ...).
func Read(name string) []byte {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("trials data: %v", err))
	}
	return b
}

// ReadOverlay returns a forever overlay file's content ("null" when the
// overlay has no such file, so a merge treats it as empty).
func ReadOverlay(name string) []byte {
	b, err := files.ReadFile("forever/" + name)
	if err != nil {
		return nil
	}
	return b
}
