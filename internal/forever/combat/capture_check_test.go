package combat

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (the directory with the four
// files) is set, every combat packet in them is parsed and the run fails on any packet that is not consumed to its
// last byte. It never runs in CI (the files are not in the repository); it is how the layouts were proven against
// real traffic before the synthetic fixtures were cut from it.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestCaptureCombatPackets parses every combat packet of the capture(s).
func TestCaptureCombatPackets(t *testing.T) {
	var files []string
	if path := os.Getenv("FOREVER_CAPTURE_PKT"); path != "" {
		files = append(files, path)
	}
	if dir := os.Getenv("FOREVER_CAPTURE_DIR"); dir != "" {
		entries, err := filepath.Glob(filepath.Join(dir, "*.pkt"))
		if err != nil {
			t.Fatalf("globbing %s: %v", dir, err)
		}
		files = append(files, entries...)
	}
	if len(files) == 0 {
		t.Skip("set FOREVER_CAPTURE_PKT or FOREVER_CAPTURE_DIR to run the capture cross-check")
	}
	kinds := map[string]uint32{
		wire.ServerName(wire.SMSGAttackStart):          wire.SMSGAttackStart,
		wire.ServerName(wire.SMSGAttackStop):           wire.SMSGAttackStop,
		wire.ServerName(wire.SMSGAttackSwingError):     wire.SMSGAttackSwingError,
		wire.ServerName(wire.SMSGAttackSwingLandedLog): wire.SMSGAttackSwingLandedLog,
		wire.ServerName(wire.SMSGAttackerStateUpdate):  wire.SMSGAttackerStateUpdate,
		wire.ServerName(wire.SMSGSpellStart):           wire.SMSGSpellStart,
		wire.ServerName(wire.SMSGSpellGo):              wire.SMSGSpellGo,
		wire.ServerName(wire.SMSGCastFailed):           wire.SMSGCastFailed,
		wire.ServerName(wire.SMSGSpellFailure):         wire.SMSGSpellFailure,
		wire.ServerName(wire.SMSGSpellFailedOther):     wire.SMSGSpellFailedOther,
		wire.ServerName(wire.SMSGSpellNonMeleeDamage):  wire.SMSGSpellNonMeleeDamage,
		wire.ServerName(wire.SMSGSpellHealLog):         wire.SMSGSpellHealLog,
		wire.ServerName(wire.SMSGSpellPeriodicAuraLog): wire.SMSGSpellPeriodicAuraLog,
		wire.ServerName(wire.SMSGSpellEnergizeLog):     wire.SMSGSpellEnergizeLog,
		wire.ServerName(wire.SMSGSpellDelayed):         wire.SMSGSpellDelayed,
		wire.ServerName(wire.SMSGAuraUpdate):           wire.SMSGAuraUpdate,
		wire.ServerName(wire.SMSGSpellCooldown):        wire.SMSGSpellCooldown,
		wire.ServerName(wire.SMSGCooldownEvent):        wire.SMSGCooldownEvent,
		wire.ServerName(wire.SMSGClearCooldown):        wire.SMSGClearCooldown,
		wire.ServerName(wire.SMSGClearCooldowns):       wire.SMSGClearCooldowns,
		wire.ServerName(wire.SMSGPowerUpdate):          wire.SMSGPowerUpdate,
		wire.ServerName(wire.SMSGLogXPGain):            wire.SMSGLogXPGain,
		wire.ServerName(wire.SMSGDeathReleaseLoc):      wire.SMSGDeathReleaseLoc,
		wire.ServerName(wire.SMSGSendKnownSpells):      wire.SMSGSendKnownSpells,
	}
	for _, path := range files {
		counts := map[string]int{}
		for _, p := range readPKT(t, path) {
			if p.ToServer {
				continue
			}
			core, ok := kinds[wire.ServerName(p.Core)]
			if !ok {
				continue
			}
			if _, err := parseFixture(p.Payload, core); err != nil {
				t.Fatalf("%s: %s (%d bytes): %v", filepath.Base(path), wire.ServerName(p.Core), len(p.Payload), err)
			}
			counts[wire.ServerName(p.Core)]++
		}
		t.Logf("%s: %v", filepath.Base(path), counts)
	}
}
