package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/pjerra/forever-agent/internal/casc"
)

const cascCatUsage = "usage: forever-agent casc-cat --install <install dir> [--product P] --fdid N --out FILE\n" +
	"  writes the one file the install's root names with FileDataID N; --out must not exist yet\n" +
	"  --install is the CASC install folder holding .build.info; --product picks its row when several are active"

// cascCatCmd writes one CASC file by FileDataID: the live check for the native reader, whose encoding and root tables
// nothing else reads yet.
func cascCatCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("casc-cat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	install := fs.String("install", "", "the CASC install folder, holding .build.info")
	product := fs.String("product", "", "the .build.info product row to read; the only active row by default")
	fdid := fs.Uint64("fdid", 0, "the FileDataID of the file to write")
	out := fs.String("out", "", "the file to write; an existing file is refused")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "fdid" {
			given = true
		}
	})
	if fs.NArg() != 0 || *install == "" || *out == "" || !given || *fdid > math.MaxUint32 {
		fmt.Fprintln(stderr, cascCatUsage)
		return exitUsage
	}
	// The output file is created exclusively first: a path that already holds a file is refused before any install is
	// opened, and a failed read removes the empty file again.
	f, err := os.OpenFile(expandHome(*out), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fmt.Fprintln(stderr, "casc-cat:", err)
		return 1
	}
	written := false
	defer func() {
		if !written {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	s, err := casc.Open(expandHome(*install), *product)
	if err != nil {
		fmt.Fprintln(stderr, "casc-cat:", err)
		return 1
	}
	defer s.Close()
	data, err := s.ReadFileDataID(uint32(*fdid))
	if err != nil {
		fmt.Fprintln(stderr, "casc-cat:", err)
		return 1
	}
	if _, err := f.Write(data); err != nil {
		fmt.Fprintln(stderr, "casc-cat:", err)
		return 1
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(stderr, "casc-cat:", err)
		return 1
	}
	written = true
	fmt.Fprintf(stdout, "casc-cat: FileDataID %d, %d bytes\n", *fdid, len(data))
	return exitOK
}
