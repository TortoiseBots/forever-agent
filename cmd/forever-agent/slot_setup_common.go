// Steps shared by `slot setup` and `slot setup --forever`: the lease check,
// the dir claim (symlink refusal, 0700, never overwrite, cleanup), the
// binary and plans copy, and the last re-check plus the slot.json write. One
// body means the safeguards cannot drift between the two paths.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/slots"
)

// slotSetupBase is what both setup paths resolve first: the slots table, the
// server and slot, the store, and the lease id the holder owns.
type slotSetupBase struct {
	holder, slotsPath string
	server            *slots.Server
	slot              *slots.Slot
	store             *slots.Store
	leaseID           string
}

// slotSetupPrepare validates the holder, loads the slots file, finds the
// slot (refusing a reserved one) and checks the holder owns its lease. A
// non-exitOK code means the refusal is already printed.
func slotSetupPrepare(o slotSetupOptions, stderr io.Writer) (*slotSetupBase, int) {
	if !leaseHolderRe.MatchString(o.holder) {
		fmt.Fprintln(stderr, "lease: --holder must be 1-64 letters, digits, . _ -")
		return nil, exitUsage
	}
	slotsPath := o.slotsPath
	if slotsPath == "" {
		slotsPath = slots.DefaultPath()
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return nil, exitUsage
	}
	server, slot, err := cfg.Find(o.server, o.slot)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return nil, exitUsage
	}
	if slot.Reserved != "" {
		fmt.Fprintf(stderr, "slot setup: %s/%s is reserved (%s)\n", server.Name, slot.ID, slot.Reserved)
		return nil, exitSlotRefused
	}
	store := slots.NewStore(cfg, slotsPath)
	leaseID, err := slotSetupCheckLease(store, server.Name, slot.ID, o.holder)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %s/%s is not leased by %s: run forever-agent lease acquire %s --slot %s --holder %s first\n",
			server.Name, slot.ID, o.holder, server.Name, slot.ID, o.holder)
		return nil, exitSlotRefused
	}
	return &slotSetupBase{holder: o.holder, slotsPath: slotsPath, server: server, slot: slot, store: store, leaseID: leaseID}, exitOK
}

// character resolves the character to play (the flag or the slot's) and
// refuses one the slot does not allow.
func (b *slotSetupBase) character(flag string, stderr io.Writer) (string, int) {
	character := b.slot.Character
	if flag != "" {
		character = flag
	}
	if !slotSetupAllows(b.slot, character) {
		fmt.Fprintf(stderr, "slot setup: %s/%s allows %s\n", b.server.Name, b.slot.ID, slotSetupAllowed(b.slot))
		return "", exitUsage
	}
	return character, exitOK
}

// recheck re-validates the lease identity and the claimed dir immediately
// before the secret-bearing or manifest write: a takeover between the first
// check and the write must leave nothing behind. what names the file about
// to be written, for the refusal text.
func (b *slotSetupBase) recheck(c *slotClaim, what string, stderr io.Writer) int {
	cur, err := b.store.Get(b.server.Name, b.slot.ID)
	if err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	if cur == nil || cur.Holder != b.holder || cur.LeaseID != b.leaseID || b.store.IsStale(cur) {
		c.cleanup()
		fmt.Fprintf(stderr, "slot setup: %s/%s is not leased by %s: run forever-agent lease acquire %s --slot %s --holder %s first\n",
			b.server.Name, b.slot.ID, b.holder, b.server.Name, b.slot.ID, b.holder)
		return exitSlotRefused
	}
	// The dir must still be the claimed one (not a swapped-in symlink).
	curInfo, err := os.Lstat(c.dir)
	if err != nil || curInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(c.info, curInfo) {
		return c.fail(stderr, "slot setup: %s was replaced during setup: refusing to write %s", c.dir, what)
	}
	return exitOK
}

// writeManifest writes slot.json (never over an existing file) and records
// the dir on the lease. A lease refusal cleans up and exits refused.
func (b *slotSetupBase) writeManifest(c *slotClaim, m slotManifest, stderr io.Writer) int {
	m.Created = b.store.Now().UTC().Format("2006-01-02T15:04:05Z")
	raw, err := json.Marshal(m)
	if err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	path := filepath.Join(c.dir, "slot.json")
	if err := writeFileExcl(path, raw, 0o600); err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	c.created = append(c.created, path)
	if err := b.store.SetDir(b.server.Name, b.slot.ID, b.holder, b.leaseID, c.dir); err != nil {
		if r, ok := err.(*slots.Refusal); ok {
			c.cleanup()
			fmt.Fprintf(stderr, "slot setup: %s\n", r.Msg)
			return exitSlotRefused
		}
		return c.fail(stderr, "slot setup: %v", err)
	}
	return exitOK
}

// slotClaim is the dir setup claimed and everything it created inside it.
type slotClaim struct {
	dir     string
	info    os.FileInfo
	created []string
}

// slotSetupClaim claims the dir before anything is written: refuse a symlink
// --dir (or a symlink in any existing parent component), create it 0700,
// refuse a non-empty dir, chmod an empty one to 0700, and resolve it to its
// real path once. A non-exitOK code means the refusal is printed.
func slotSetupClaim(dirFlag, holder string, stderr io.Writer) (*slotClaim, int) {
	dir := dirFlag
	if dir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			fmt.Fprintf(stderr, "slot setup: %v\n", herr)
			return nil, exitUsage
		}
		dir = filepath.Join(home, "ta-"+holder)
	}
	dir = slots.ExpandTilde(dir)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return nil, exitUsage
	}
	dir = absDir
	if err := slotSetupCheckNoSymlink(dir); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return nil, exitUsage
	}
	var claimInfo os.FileInfo
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() {
			fmt.Fprintf(stderr, "slot setup: %s exists and is not empty: pick a new --dir (setup never overwrites)\n", dir)
			return nil, exitUsage
		}
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			fmt.Fprintf(stderr, "slot setup: %v\n", rerr)
			return nil, exitUsage
		}
		if len(entries) > 0 {
			fmt.Fprintf(stderr, "slot setup: %s exists and is not empty: pick a new --dir (setup never overwrites)\n", dir)
			return nil, exitUsage
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			fmt.Fprintf(stderr, "slot setup: %v\n", err)
			return nil, exitUsage
		}
		claimInfo = info
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return nil, exitUsage
	} else if merr := os.MkdirAll(dir, 0o700); merr != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", merr)
		return nil, exitUsage
	} else if merr := os.Chmod(dir, 0o700); merr != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", merr)
		return nil, exitUsage
	} else if info, serr := os.Lstat(dir); serr != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", serr)
		return nil, exitUsage
	} else {
		claimInfo = info
	}
	// A refused-symlink check ran above, so this succeeds only on a plain
	// dir: children are built from the real path, never through a
	// later-swapped link.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		fmt.Fprintf(stderr, "slot setup: %v\n", err)
		return nil, exitUsage
	}
	return &slotClaim{dir: realDir, info: claimInfo}, exitOK
}

// cleanup removes the paths setup created inside the claimed dir. The dir
// swapped under setup is never reached through.
func (c *slotClaim) cleanup() {
	if c.info != nil {
		if cur, err := os.Lstat(c.dir); err != nil || cur.Mode()&os.ModeSymlink != 0 || !os.SameFile(c.info, cur) {
			return
		}
	}
	for _, p := range c.created {
		if !slotSetupInsideDir(c.dir, p) {
			continue
		}
		os.RemoveAll(p)
	}
}

// fail cleans up, prints the message and returns exitUsage.
func (c *slotClaim) fail(stderr io.Writer, format string, a ...any) int {
	c.cleanup()
	fmt.Fprintf(stderr, format+"\n", a...)
	return exitUsage
}

// copyBinary copies the agent binary (binary flag, or this executable) into
// the claimed dir as forever-agent and returns its path.
func (c *slotClaim) copyBinary(binaryFlag string) (string, error) {
	binSrc := binaryFlag
	if binSrc == "" {
		var err error
		if binSrc, err = os.Executable(); err != nil {
			return "", err
		}
	}
	binDst := filepath.Join(c.dir, "forever-agent")
	if err := copyFileExcl(binSrc, binDst, 0o755); err != nil {
		return "", err
	}
	c.created = append(c.created, binDst)
	return binDst, nil
}

// seedDungeons makes the plans dir (the flag, or dungeons/ in the claimed
// dir) and copies seeded plans into it. Only a dir setup created itself is
// tracked for cleanup: a pre-existing caller-supplied --dungeons-dir is never
// removed. It returns the dir, the number of plans copied and their source.
func (c *slotClaim) seedDungeons(dungeonsFlag, fromFlag string) (dungeonsDir string, copied int, source string, err error) {
	dungeonsDir = dungeonsFlag
	if dungeonsDir == "" {
		dungeonsDir = filepath.Join(c.dir, "dungeons")
	}
	made, err := slotSetupEnsureDir(dungeonsDir, 0o755)
	if err != nil {
		return "", 0, "", err
	}
	if made {
		c.created = append(c.created, dungeonsDir)
	}
	if fromFlag != "" {
		files, src, err := slotSetupCopyPlans(slots.ExpandTilde(fromFlag), dungeonsDir)
		if err != nil {
			return "", 0, "", err
		}
		source = src
		copied = len(files)
		c.created = append(c.created, files...)
	}
	return dungeonsDir, copied, source, nil
}
