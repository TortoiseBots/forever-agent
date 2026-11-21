package main

// forever gm-convert: render a gm-walk world-data file in the server module's shape. No login, no server.

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/pjerra/forever-agent/internal/forever/gmwalk"
)

const gmConvertUsage = "usage: forever-agent forever gm-convert --in FILE --out FILE [--module-out FILE] [--force]"

func foreverGMConvertCmd(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever gm-convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "gm-walk world-data JSON file")
	out := fs.String("out", "", "validated copy of the world data (same shape, normalised; never overwritten unless --force)")
	moduleOut := fs.String("module-out", "", "module-shape JSON file (handcrafted_road rows plus zone/point proposals; never overwritten unless --force)")
	force := fs.Bool("force", false, "overwrite an existing --out or --module-out file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, gmConvertUsage)
		return exitUsage
	}
	if *in == "" || *out == "" {
		fmt.Fprintln(stderr, "forever gm-convert: --in and --out are required")
		fmt.Fprintln(stderr, gmConvertUsage)
		return exitUsage
	}
	data, err := gmwalk.ReadFile(*in)
	if err != nil {
		fmt.Fprintf(stderr, "forever gm-convert: %v\n", err)
		return exitUsage
	}
	if err := gmwalk.WriteFileForce(*out, data, *force); err != nil {
		fmt.Fprintf(stderr, "forever gm-convert: %v\n", err)
		return exitWorld
	}
	fmt.Fprintf(stdout, "gm-convert: %s: version %d, %d road(s), %d point(s), %d zone(s) -> %s\n",
		*in, data.Version, len(data.Roads), len(data.Points), len(data.Zones), *out)
	if *moduleOut != "" {
		mod, dropped := gmwalk.Convert(data)
		if dropped > 0 {
			fmt.Fprintf(stdout, "gm-convert: module converter dropped %d one-point road(s): no hop to render\n", dropped)
		}
		if err := gmwalk.WriteModuleForce(*moduleOut, mod, *force); err != nil {
			fmt.Fprintf(stderr, "forever gm-convert: %v\n", err)
			return exitWorld
		}
		fmt.Fprintf(stdout, "gm-convert: module data -> %s\n", *moduleOut)
	}
	return exitOK
}
