// Package casc is a native, read-only reader of a local CASC install: the build info and build config, the local
// index files, BLTE streams and encoded-key reads of the data archives. It reads the format from its public
// description only; no client data is needed to use it.
package casc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

// BuildInfo is the parsed .build.info table of an install: a typed, pipe-separated header line and one row per
// build. A header column reads Name!TYPE:size, where TYPE is STRING, DEC or HEX; the type only documents the text.
type BuildInfo struct {
	columns []string
	rows    []BuildRow
}

// BuildRow is one .build.info row, keyed by its header column name.
type BuildRow struct {
	values map[string]string
}

// ParseBuildInfo parses the contents of a .build.info file.
func ParseBuildInfo(data []byte) (*BuildInfo, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // a UTF-8 BOM in front of the header
	lines := strings.Split(string(data), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i == len(lines) {
		return nil, errors.New("casc: .build.info is empty")
	}
	info := &BuildInfo{}
	seen := map[string]bool{}
	for _, field := range strings.Split(lines[i], "|") {
		name := strings.TrimSpace(field)
		if j := strings.IndexByte(name, '!'); j >= 0 {
			name = strings.TrimSpace(name[:j])
		}
		if name == "" {
			return nil, fmt.Errorf("casc: .build.info column %d has no name", len(info.columns))
		}
		if seen[name] {
			return nil, fmt.Errorf("casc: .build.info names the column %q twice", name)
		}
		seen[name] = true
		info.columns = append(info.columns, name)
	}
	for _, line := range lines[i+1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		row := BuildRow{values: make(map[string]string, len(info.columns))}
		for c, name := range info.columns {
			if c < len(fields) {
				row.values[name] = strings.TrimSpace(fields[c])
			}
		}
		info.rows = append(info.rows, row)
	}
	if len(info.rows) == 0 {
		return nil, errors.New("casc: .build.info has a header but no rows")
	}
	return info, nil
}

// LoadBuildInfo parses the .build.info file at path.
func LoadBuildInfo(path string) (*BuildInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseBuildInfo(data)
}

// Get returns the row's cell for column, or "" when the row has no such column.
func (r BuildRow) Get(column string) string { return r.values[column] }

// BuildKey returns the Build Key cell, the hex name of the build config file.
func (r BuildRow) BuildKey() string { return r.Get("Build Key") }

// Product returns the Product cell.
func (r BuildRow) Product() string { return r.Get("Product") }

// Version returns the Version cell.
func (r BuildRow) Version() string { return r.Get("Version") }

// Active reports whether the Active cell is 1.
func (r BuildRow) Active() bool { return strings.TrimSpace(r.Get("Active")) == "1" }

// Rows returns the table's rows in file order.
func (b *BuildInfo) Rows() []BuildRow { return append([]BuildRow(nil), b.rows...) }

// Select picks the build row: the row of product when product is not empty (the active row if several rows share
// the product), else the only active row. Several active rows without a product is an error naming their products.
func (b *BuildInfo) Select(product string) (BuildRow, error) {
	if product != "" {
		var match []BuildRow
		for _, r := range b.rows {
			if strings.EqualFold(r.Product(), product) {
				match = append(match, r)
			}
		}
		if len(match) == 0 {
			return BuildRow{}, fmt.Errorf("casc: .build.info has no row for product %q (products: %s)", product, b.products())
		}
		if len(match) > 1 {
			var active []BuildRow
			for _, r := range match {
				if r.Active() {
					active = append(active, r)
				}
			}
			if len(active) == 1 {
				return active[0], nil
			}
			return BuildRow{}, fmt.Errorf("casc: .build.info has %d rows for product %q and %d of them active; the product does not name one build",
				len(match), product, len(active))
		}
		return match[0], nil
	}
	var active []BuildRow
	for _, r := range b.rows {
		if r.Active() {
			active = append(active, r)
		}
	}
	switch len(active) {
	case 1:
		return active[0], nil
	case 0:
		return BuildRow{}, fmt.Errorf("casc: .build.info has no active build (products: %s)", b.products())
	default:
		var names []string
		for _, r := range active {
			names = append(names, fmt.Sprintf("%q", r.Product()))
		}
		return BuildRow{}, fmt.Errorf("casc: .build.info has %d active builds (%s); pass a product", len(active), strings.Join(names, ", "))
	}
}

// products lists the table's distinct Product cells, for error messages.
func (b *BuildInfo) products() string {
	var names []string
	for _, r := range b.rows {
		p := r.Product()
		if p == "" {
			continue
		}
		dup := false
		for _, n := range names {
			if strings.EqualFold(n, p) {
				dup = true
				break
			}
		}
		if !dup {
			names = append(names, p)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
