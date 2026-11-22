package foreverspells

import (
	"embed"
	"errors"
	"io/fs"
)

//go:embed all:data
var embeddedData embed.FS

// Default returns the spell table baked into the binary. Until the lead commits
// the generated data files (round R1 keeps only data/.keep) the files are
// absent and it returns an empty table; a file that is present and malformed
// panics.
func Default() *Table {
	// The embedded FS keeps the data/ prefix (//go:embed all:data); Load reads at the root of
	// its FS, so serve it the data directory. A missing file before L1 is an empty table; a
	// present-but-malformed file panics.
	sub, err := fs.Sub(embeddedData, "data")
	if err != nil {
		return &Table{}
	}
	tab, err := Load(sub)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Table{}
		}
		panic(err)
	}
	return tab
}
