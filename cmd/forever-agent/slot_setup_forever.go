// Command slot setup --forever builds a serve slot dir: lease, dir, plans
// as today (one shared body with the old path, see slot_setup_common.go) but
// no settings file, and the printed start line is the `forever serve --dir`
// line.
package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/trials/live"
)

// slotSetupForeverCmd sets up a forever serve slot: the lease, the claimed
// dir, plans and dungeons as today, the slot.json manifest (the real server
// name, so teardown releases the real lease; the settings path is recorded
// as the path teardown and rpc derive, but no file is written there), and no
// settings file. It prints the `forever serve --dir` line the fleet's serve
// children run.
func slotSetupForeverCmd(o slotSetupOptions, stdout, stderr io.Writer) int {
	if o.mmapsDir != "" {
		fmt.Fprintln(stderr, "slot setup: --mmaps-dir is not used with --forever: a serve takes its navmesh folder from its own --navmesh flag, not from the slot")
		return exitUsage
	}
	b, code := slotSetupPrepare(o, stderr)
	if code != exitOK {
		return code
	}
	server, slot := b.server, b.slot
	character, code := b.character(o.character, stderr)
	if code != exitOK {
		return code
	}
	// The serve's connection flags come from the slots file's `forever`
	// block (rest, rpc, ca, build), read only. No settings file is written.
	sf, err := live.LoadSlotsFile(b.slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return exitUsage
	}
	ext, ok := sf.Forever[server.Name]
	if !ok || ext.Rest == "" || ext.RPC == "" || ext.CA == "" {
		fmt.Fprintf(stderr, "slot setup: server %q has no complete `forever` block (rest, rpc, ca) in %s\n", server.Name, b.slotsPath)
		return exitUsage
	}
	c, code := slotSetupClaim(o.dir, o.holder, stderr)
	if code != exitOK {
		return code
	}
	dir := c.dir
	binDst, err := c.copyBinary(o.binary)
	if err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	// Plans as today (dungeons/ under the claimed dir, seeded copies); a
	// serve never reads them, but the fleet's trials and plan tools share
	// the dir layout.
	_, plansCopied, plansSource, err := c.seedDungeons(o.dungeonsDir, o.dungeonsFrom)
	if err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	if slotSetupPreWriteHook != nil {
		slotSetupPreWriteHook()
	}
	if code := b.recheck(c, "slot.json", stderr); code != exitOK {
		return code
	}
	manifest := slotManifest{
		Server:    server.Name,
		Slot:      slot.ID,
		Holder:    o.holder,
		LeaseID:   b.leaseID,
		Account:   slot.Account,
		Character: character,
		Port:      slot.Port,
		Dir:       dir,
		Binary:    binDst,
		// The path teardown and rpc derive; no file is written there.
		Settings:  filepath.Join(dir, "settings.json"),
		PIDFile:   filepath.Join(dir, "agent.pid"),
		TokenFile: filepath.Join(dir, fmt.Sprintf("rpc-%d.token", slot.Port)),
		SlotsPath: b.slotsPath,
	}
	if code := b.writeManifest(c, manifest, stderr); code != exitOK {
		return code
	}
	serveLine := fmt.Sprintf("%s forever serve --rest %s --rpc %s --ca %s --account %s --character %s --password-file %s --listen 127.0.0.1:%d --dir %s",
		binDst, ext.Rest, ext.RPC, ext.CA, slot.Account, character, "<password-file>", slot.Port, dir)
	if ext.Build != "" && ext.Build != "0" {
		serveLine += " --build " + ext.Build
	}
	fmt.Fprintf(stdout, "slot %s/%s ready in %s\n", server.Name, slot.ID, dir)
	fmt.Fprintf(stdout, "account %s, character %s, port %d\n", slot.Account, character, slot.Port)
	fmt.Fprintf(stdout, "serve: %s\n", serveLine)
	if o.dungeonsFrom != "" {
		fmt.Fprintf(stdout, "dungeons: %d plans copied from %s\n", plansCopied, plansSource)
	}
	return exitOK
}
