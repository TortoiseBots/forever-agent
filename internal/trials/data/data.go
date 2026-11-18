// Package data holds the reference tables the scoring half reads: the spell table and the
// gear kits. Both are copied unchanged from tortoise-trials (data/*.json), where build_data.py
// made them from the public core's SQL; the bench only reads them.
package data

import (
	"embed"
	"fmt"
)

//go:embed *.json
var files embed.FS

// Read returns a data file's content (spells.json, gear_kits.json, ...).
func Read(name string) []byte {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("trials data: %v", err))
	}
	return b
}
