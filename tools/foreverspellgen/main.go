// Command foreverspellgen writes internal/foreverspells/data from the client's spell DB2 tables:
// spells.tsv (class abilities and crowd-control auras with their cooldown, category, GCD and
// mechanic numbers) and names.tsv.gz (every SpellName id and name). The DB2 folder is a flag
// only; no path from it reaches the files.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/db2"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

func main() {
	dbc := flag.String("dbc", "", "folder holding SpellName.db2, SkillLineAbility.db2, SpellLevels.db2, SpellCooldowns.db2, SpellCategories.db2 and SpellMisc.db2")
	out := flag.String("out", "", "folder to write spells.tsv and names.tsv.gz into")
	flag.Parse()
	if *dbc == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: foreverspellgen -dbc <dir> -out <dir>")
		os.Exit(2)
	}
	if err := run(*dbc, *out); err != nil {
		fmt.Fprintln(os.Stderr, "foreverspellgen:", err)
		os.Exit(1)
	}
}

// run reads the six tables, joins them, writes the two files and prints the stats.
func run(dbc, out string) error {
	in, source, err := readInputs(dbc)
	if err != nil {
		return err
	}
	rows, names, stats, err := Build(in)
	if err != nil {
		return err
	}
	var spells bytes.Buffer
	if err := WriteSpellsTSV(&spells, source, rows); err != nil {
		return err
	}
	namesGZ, err := gzipNames(source, names)
	if err != nil {
		return err
	}
	// What was built must load: a name with a tab or newline, or a bad row, fails here and not
	// at the first Default() of the agent.
	if _, err := foreverspells.Parse(bytes.NewReader(spells.Bytes()), gzipReader(namesGZ)); err != nil {
		return fmt.Errorf("generated data does not parse: %v", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := publish(out, map[string][]byte{"spells.tsv": spells.Bytes(), "names.tsv.gz": namesGZ}); err != nil {
		return err
	}
	printStats(stats)
	return nil
}

// readInputs opens and decodes the six tables and returns their common schema string, the source
// line of both files. A table of another schema would mix builds, so it is refused.
func readInputs(dir string) (Inputs, string, error) {
	var in Inputs
	source := ""
	open := func(name string) (*db2.File, error) {
		f, err := db2.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if f.Schema == "" {
			return nil, fmt.Errorf("%s carries no schema string", name)
		}
		if source == "" {
			source = f.Schema
		} else if f.Schema != source {
			return nil, fmt.Errorf("%s is schema %q, not %q", name, f.Schema, source)
		}
		return f, nil
	}
	var notes []string
	f, err := open("SpellName.db2")
	if err != nil {
		return in, "", err
	}
	in.Names, in.Notes, err = decodeNames(f)
	if err != nil {
		return in, "", err
	}
	if f, err = open("SkillLineAbility.db2"); err != nil {
		return in, "", err
	}
	in.Skills, notes, err = decodeSkills(f)
	if err != nil {
		return in, "", err
	}
	in.Notes = append(in.Notes, notes...)
	if f, err = open("SpellLevels.db2"); err != nil {
		return in, "", err
	}
	in.Levels, notes, err = decodeLevels(f)
	if err != nil {
		return in, "", err
	}
	in.Notes = append(in.Notes, notes...)
	if f, err = open("SpellCooldowns.db2"); err != nil {
		return in, "", err
	}
	in.Cooldowns, notes, err = decodeCooldowns(f)
	if err != nil {
		return in, "", err
	}
	in.Notes = append(in.Notes, notes...)
	if f, err = open("SpellCategories.db2"); err != nil {
		return in, "", err
	}
	in.Categories, notes, err = decodeCategories(f)
	if err != nil {
		return in, "", err
	}
	in.Notes = append(in.Notes, notes...)
	if f, err = open("SpellMisc.db2"); err != nil {
		return in, "", err
	}
	in.Misc, notes, err = decodeMisc(f)
	if err != nil {
		return in, "", err
	}
	in.Notes = append(in.Notes, notes...)
	return in, source, nil
}

// printStats writes one line per class, the two-class and crowd-control counts and every note.
func printStats(stats Stats) {
	for _, class := range classes {
		fmt.Printf("%s %d rows\n", class, stats.Classes[class])
	}
	fmt.Printf("two-class spells %d\n", stats.TwoClass)
	fmt.Printf("crowd-control-only rows %d\n", stats.CConly)
	for _, note := range stats.Notes {
		fmt.Println("note:", note)
	}
}

// gzipReader opens the gzipped names for the parse check; a bad stream surfaces as a read error.
func gzipReader(b []byte) io.Reader {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return errReader{err}
	}
	return zr
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// publish writes every file to a temp name in dir first and renames them into place only when all
// were written, so a failed write never leaves a new spells file beside an old names file.
func publish(dir string, files map[string][]byte) error {
	var temps, finals []string
	cleanup := func() {
		for _, t := range temps {
			os.Remove(t)
		}
	}
	for _, name := range []string{"spells.tsv", "names.tsv.gz"} {
		tmp := filepath.Join(dir, name+".tmp")
		if err := os.WriteFile(tmp, files[name], 0o644); err != nil {
			cleanup()
			return err
		}
		temps = append(temps, tmp)
		finals = append(finals, filepath.Join(dir, name))
	}
	for i, tmp := range temps {
		if err := os.Rename(tmp, finals[i]); err != nil {
			cleanup()
			return err
		}
	}
	return nil
}
