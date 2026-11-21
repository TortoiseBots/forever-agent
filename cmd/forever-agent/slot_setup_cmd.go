// Command slot setup builds a private client dir for a leased slot.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/forever-agent/internal/config"
	"github.com/pjerra/forever-agent/internal/slots"
)

const slotSetupUsage = "usage: forever-agent slot setup <server> <slot> --holder NAME [--dir DIR] [--binary PATH] [--dungeons-dir DIR] [--mmaps-dir DIR] [--dungeons-from DIR] [--character NAME] [--slots FILE]\n       forever-agent slot setup --forever <server> <slot> --holder NAME [--dir DIR] [--binary PATH] [--dungeons-dir DIR] [--dungeons-from DIR] [--character NAME] [--slots FILE]"

// slotManifest is the no-secret <dir>/slot.json written by slot setup.
type slotManifest struct {
	Server    string `json:"server"`
	Slot      string `json:"slot"`
	Holder    string `json:"holder"`
	LeaseID   string `json:"lease_id"`
	Account   string `json:"account"`
	Character string `json:"character"`
	Port      int    `json:"port"`
	Dir       string `json:"dir"`
	Binary    string `json:"binary"`
	Settings  string `json:"settings"`
	PIDFile   string `json:"pid_file"`
	TokenFile string `json:"token_file"`
	SlotsPath string `json:"slots_path"`
	Created   string `json:"created"`
}

// slotSetupPreWriteHook runs right before setup re-validates the lease and
// writes settings.json. Tests set it to simulate a takeover between the
// initial lease check and the write; it is nil in production.
var slotSetupPreWriteHook func()

func slotSetupCmd(args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("slot", stderr)
	}
	o, code := parseSlotSetupArgs(args, stderr)
	if code != exitOK {
		return code
	}
	if o.forever {
		return slotSetupForeverCmd(o, stdout, stderr)
	}
	b, code := slotSetupPrepare(o, stderr)
	if code != exitOK {
		return code
	}
	server, slot := b.server, b.slot
	// --mmaps-dir is validated before anything is created: a missing dir
	// refuses like other slot refusals and leaves nothing behind.
	var mmapsOverride string
	if o.mmapsDir != "" {
		mmapsOverride = slots.ExpandTilde(o.mmapsDir)
		if info, err := os.Stat(mmapsOverride); err != nil || !info.IsDir() {
			fmt.Fprintf(stderr, "slot setup: --mmaps-dir %s is not a folder (copy the server's data/mmaps files there)\n", mmapsOverride)
			return exitSlotRefused
		}
	}
	character, code := b.character(o.character, stderr)
	if code != exitOK {
		return code
	}
	// Claim the dir first, before any credential exists. Everything setup
	// creates below is tracked for cleanup on failure; cleanup only removes
	// setup-created paths inside the claimed dir.
	c, code := slotSetupClaim(o.dir, o.holder, stderr)
	if code != exitOK {
		return code
	}
	dir := c.dir
	binDst, err := c.copyBinary(o.binary)
	if err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	settingsFrom := slots.ExpandTilde(slot.SettingsFrom)
	src, err := config.Load(settingsFrom)
	if err != nil {
		return c.fail(stderr, "%v", err)
	}
	pw, err := src.Password(slot.Account)
	if err != nil {
		return c.fail(stderr, "slot setup: %s does not hold the slot's account", slot.SettingsFrom)
	}
	// Seeded plans (backlog 56: the empty dungeons/ folder made
	// follow_start/dungeon_run fail with 'no such file'). Copied files sit
	// inside the claimed dir, so the normal cleanup removes them; a
	// pre-existing --dungeons-dir keeps its old files (rename never
	// overwrites: it refuses instead).
	dungeonsDir, plansCopied, plansSource, err := c.seedDungeons(o.dungeonsDir, o.dungeonsFrom)
	if err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	tracesDir := filepath.Join(dir, "traces")
	tracesMade, err := slotSetupEnsureDir(tracesDir, 0o755)
	if err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	if tracesMade {
		c.created = append(c.created, tracesDir)
	}
	stateDir := slots.ExpandTilde(server.StateDir)
	if stateDir == "" {
		stateDir = filepath.Dir(settingsFrom)
	} else if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	mmapsDir := slots.ExpandTilde(server.MmapsDir)
	if mmapsDir == "" {
		mmapsDir = src.MmapsDir
	}
	if mmapsOverride != "" {
		mmapsDir = mmapsOverride
	}
	dst := config.Settings{
		Realmd:            server.Realmd,
		Realm:             server.Realm,
		ServerName:        server.Name,
		WorldAddr:         server.WorldAddr,
		RPC:               fmt.Sprintf("127.0.0.1:%d", slot.Port),
		Accounts:          map[string]string{slot.Account: pw},
		MmapsDir:          mmapsDir,
		DungeonsDir:       dungeonsDir,
		TracesDir:         tracesDir,
		TestSpell:         src.TestSpell,
		Spot:              src.Spot,
		Addons:            src.Addons,
		SharedFingerprint: src.SharedFingerprint,
		RotationsDir:      src.RotationsDir,
		TBMAddonDir:       src.TBMAddonDir,
		RunMeta:           src.RunMeta,
		StateDir:          stateDir,
	}
	if slotSetupPreWriteHook != nil {
		slotSetupPreWriteHook()
	}
	// Re-validate the lease identity and the dir right before settings land:
	// a takeover between the check and the write must leave no credentials.
	if code := b.recheck(c, "settings", stderr); code != exitOK {
		return code
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := slotSetupWriteSettings(settingsPath, dst, stateDir); err != nil {
		return c.fail(stderr, "slot setup: %v", err)
	}
	c.created = append(c.created, settingsPath)
	tokenFile := filepath.Join(dir, fmt.Sprintf("rpc-%d.token", slot.Port))
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
		Settings:  settingsPath,
		PIDFile:   filepath.Join(dir, "agent.pid"),
		TokenFile: tokenFile,
		SlotsPath: b.slotsPath,
	}
	if code := b.writeManifest(c, manifest, stderr); code != exitOK {
		return code
	}
	fmt.Fprintf(stdout, "slot %s/%s ready in %s\n", server.Name, slot.ID, dir)
	fmt.Fprintf(stdout, "account %s, character %s, port %d\n", slot.Account, character, slot.Port)
	fmt.Fprintf(stdout, "start: nohup %s login --config %s --pid-file %s %s > %s 2>&1 &\n",
		binDst, settingsPath, manifest.PIDFile, character, filepath.Join(dir, "agent.out"))
	fmt.Fprintf(stdout, "rpc: 127.0.0.1:%d, token file %s (written at login)\n", slot.Port, tokenFile)
	fmt.Fprintf(stdout, "teardown: %s teardown %s\n", binDst, dir)
	if mmapsOverride != "" {
		fmt.Fprintf(stdout, "mmaps: %s\n", mmapsOverride)
	}
	if o.dungeonsFrom != "" {
		fmt.Fprintf(stdout, "dungeons: %d plans copied from %s\n", plansCopied, plansSource)
	}
	return exitOK
}

// slotSetupCheckNoSymlink refuses a --dir that is a symlink or sits under
// one: writes through a symlink land outside the claimed dir.
func slotSetupCheckNoSymlink(dir string) error {
	clean := filepath.Clean(dir)
	for p := clean; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("slot setup: %s resolves through a symlink: pick a new --dir (setup never writes through a symlink)", clean)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("slot setup: %v", err)
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	// Belt and braces: the deepest existing ancestor must resolve to
	// itself, or a symlink component hid from the walk above.
	existing := clean
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	if real, err := filepath.EvalSymlinks(existing); err == nil && real != filepath.Clean(existing) {
		return fmt.Errorf("slot setup: %s resolves through a symlink: pick a new --dir (setup never writes through a symlink)", clean)
	}
	return nil
}

// slotSetupEnsureDir MkdirAlls path, reporting whether setup created it.
// Only setup-created paths are tracked for cleanup: a pre-existing
// caller-supplied dir (e.g. an external --dungeons-dir) is never removed.
func slotSetupEnsureDir(path string, mode os.FileMode) (bool, error) {
	if st, err := os.Stat(path); err == nil {
		if !st.IsDir() {
			return false, fmt.Errorf("slot setup: %s exists and is not a dir", path)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return false, err
	}
	return true, nil
}

// slotSetupInsideDir reports whether p is dir itself or below it.
func slotSetupInsideDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// slotSetupPlanNameOK reports whether name is a plain file name the copy may
// place in the dungeons dir: no path separators, no dot-dirs. ReadDir never
// returns separators, but the copy still proves every destination stays
// inside the dungeons dir instead of assuming it.
func slotSetupPlanNameOK(name string) bool {
	return name != "" && name != "." && name != ".." &&
		name == filepath.Base(name) && !strings.ContainsAny(name, `/\`)
}

// slotSetupCopyPlans copies every *.json plan file from srcDir into dstDir
// and returns the copied destination paths plus the source shown in the
// printed line. Only plain files are copied: a symlinked *.json refuses the
// setup (setup never follows symlinks), any other non-regular file does too,
// and a bad name refuses it. Other files are skipped. A git:<ref> source is
// refused: only the DIR form is implemented (export dungeons/*.json of that
// ref into a folder instead).
func slotSetupCopyPlans(srcDir, dstDir string) ([]string, string, error) {
	if ref, ok := strings.CutPrefix(srcDir, "git:"); ok {
		return nil, "", fmt.Errorf("--dungeons-from git:%s is not supported: export dungeons/*.json of that ref into a folder and pass its DIR", ref)
	}
	info, err := os.Stat(srcDir)
	if err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("--dungeons-from %s is not a folder of plan files", srcDir)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil, "", fmt.Errorf("--dungeons-from %s: %v", srcDir, err)
	}
	var copied []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if !slotSetupPlanNameOK(name) {
			return nil, "", fmt.Errorf("--dungeons-from %s: bad plan name %q (plain file names only)", srcDir, name)
		}
		src := filepath.Join(srcDir, name)
		fi, err := os.Lstat(src)
		if err != nil {
			return nil, "", fmt.Errorf("--dungeons-from %s: %v", srcDir, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("--dungeons-from %s: %q is a symlink (setup never follows symlinks)", srcDir, name)
		}
		if !fi.Mode().IsRegular() {
			return nil, "", fmt.Errorf("--dungeons-from %s: %q is not a plain file", srcDir, name)
		}
		dst := filepath.Join(dstDir, name)
		if err := copyFileExcl(src, dst, 0o644); err != nil {
			return nil, "", fmt.Errorf("--dungeons-from %s: %v", srcDir, err)
		}
		copied = append(copied, dst)
	}
	return copied, srcDir, nil
}

func slotSetupCheckLease(store *slots.Store, server, slot, holder string) (string, error) {
	states, err := store.List(server)
	if err != nil {
		return "", err
	}
	for _, s := range states {
		if s.Slot.ID != slot {
			continue
		}
		if s.State == "held" && s.Lease != nil && s.Lease.Holder == holder {
			return s.Lease.LeaseID, nil
		}
		return "", fmt.Errorf("not leased by %s", holder)
	}
	return "", fmt.Errorf("not leased by %s", holder)
}

func slotSetupAllows(slot *slots.Slot, character string) bool {
	if character == slot.Character {
		return true
	}
	for _, other := range slot.OtherCharacters {
		if character == other {
			return true
		}
	}
	return false
}

func slotSetupAllowed(slot *slots.Slot) string {
	if len(slot.OtherCharacters) == 0 {
		return slot.Character
	}
	return slot.Character + " (and " + strings.Join(slot.OtherCharacters, ", ") + ")"
}

// slotSetupWriteSettings writes the client settings via tmp+rename (0600),
// then loads them back; a load failure removes the file. The dir was claimed
// empty, so the rename never overwrites.
func slotSetupWriteSettings(path string, dst config.Settings, stateDir string) error {
	raw, err := json.MarshalIndent(dst, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	if _, err := config.Load(tmpName); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func copyFileExcl(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(dst)
		return closeErr
	}
	if err := os.Chmod(dst, mode); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

func writeFileExcl(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		os.Remove(path)
		return werr
	}
	if cerr != nil {
		os.Remove(path)
		return cerr
	}
	return nil
}
