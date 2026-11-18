package party

// The fixture generator: it cuts the committed synthetic fixtures out of the private capture. It is not part of CI
// (it needs FOREVER_CAPTURE_PKT or FOREVER_CAPTURE_DIR and FOREVER_WRITE_FIXTURES=1). Every packed GUID is replaced
// with a synthetic one of the same mask and type, every name (member, inviter, realm, decline) with a run of the same
// length, the invite's two realm identifiers with 4242, and the member state's run values (health, power, level,
// zone, position) with fixed synthetic numbers. The result must still parse.
//
// FOREVER_CAPTURE_DIR reads the raw payloads `forever ... --capture-dir` writes: <NNNN>_<name>.bin plus a
// manifest.tsv of name, core opcode and length.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type partyFixture struct {
	Name    string   `json:"name"`
	Opcode  string   `json:"opcode"`
	Payload string   `json:"payload"`
	GUIDs   []string `json:"synthetic_guids"`
	Notes   []string `json:"rewritten"`
}

type partyFixtureFile struct {
	Note    string         `json:"note"`
	Packets []partyFixture `json:"packets"`
}

type pktRecord struct {
	ToServer bool
	Core     uint32
	Payload  []byte
}

const pktHeaderSize = 44

func readPKT(t *testing.T, path string) []pktRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if len(data) < 66 || string(data[:3]) != "PKT" {
		t.Fatalf("%s is not a PKT file", path)
	}
	var out []pktRecord
	for off := 66; off+pktHeaderSize <= len(data); {
		direction := binary.LittleEndian.Uint32(data[off:])
		size := binary.LittleEndian.Uint32(data[off+16:])
		core := binary.LittleEndian.Uint32(data[off+40:])
		if size < 4 || off+pktHeaderSize+int(size)-4 > len(data) {
			t.Fatalf("%s: record at %d has length %d", path, off, size)
		}
		payload := append([]byte(nil), data[off+pktHeaderSize:off+pktHeaderSize+int(size)-4]...)
		out = append(out, pktRecord{ToServer: direction == 0x47534D43, Core: core, Payload: payload})
		off += pktHeaderSize + int(size) - 4
	}
	return out
}

// readCaptureDir reads the payloads a --capture-dir run wrote through the one reader both fixture generators use
// (internal/forever/capturedir): a run directory or a parent of run directories.
func readCaptureDir(t *testing.T, dir string) []pktRecord {
	t.Helper()
	recs, err := capturedir.ReadDir(dir)
	if err != nil {
		t.Fatalf("the capture directory: %v", err)
	}
	out := make([]pktRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, pktRecord{Core: r.Opcode, Payload: r.Payload})
	}
	return out
}

func guidFromRaw(raw [16]byte) wire.GUID {
	var g wire.GUID
	for i := range 8 {
		g.Low |= uint64(raw[i]) << (8 * i)
		g.High |= uint64(raw[i+8]) << (8 * i)
	}
	return g
}

// syntheticGUID builds a GUID of the original's mask and type. n is the GUID's position in the packet's audit
// list and the first eight data bytes carry it in base 64, so two distinct GUIDs never map to the same synthetic one
// (the earlier n%5 did: party_update_full's five members held duplicates).
func syntheticGUID(orig []byte, n int) []byte {
	mask := binary.LittleEndian.Uint16(orig)
	var raw [16]byte
	digits := n + 1
	for i := range 16 {
		if mask&(1<<i) == 0 {
			continue
		}
		if i < 8 {
			raw[i] = byte(0x40 + digits&0x3f)
			digits >>= 6
			continue
		}
		raw[i] = byte(0x40 + (i%8)*3)
	}
	if mask&0x8000 != 0 {
		raw[15] = (raw[15] & 0x03) | (orig[len(orig)-1] & 0xFC)
	}
	out := wire.NewWriter()
	wire.WritePackedGUID(out, guidFromRaw(raw))
	return append([]byte(nil), out.Bytes()...)
}

// rewriteGUIDs replaces every distinct non-empty packed GUID and records the synthetic forms.
func rewriteGUIDs(t *testing.T, name string, out []byte, guids []wire.GUID) ([]byte, []string, []string) {
	t.Helper()
	seen := map[string]bool{}
	var notes, synthetic []string
	for i, g := range guids {
		raw := wire.NewWriter()
		wire.WritePackedGUID(raw, g)
		orig := append([]byte(nil), raw.Bytes()...)
		if len(orig) == 2 && orig[0] == 0 && orig[1] == 0 {
			continue
		}
		key := hex.EncodeToString(orig)
		if seen[key] {
			continue
		}
		seen[key] = true
		syn := syntheticGUID(orig, i)
		if !bytes.Contains(out, orig) {
			t.Fatalf("%s: a packed GUID is not in the payload", name)
		}
		out = bytes.ReplaceAll(out, orig, syn)
		synthetic = append(synthetic, hex.EncodeToString(syn))
	}
	if len(synthetic) > 0 {
		notes = append(notes, fmt.Sprintf("%d packed GUID(s) replaced", len(synthetic)))
	}
	return out, notes, synthetic
}

// rewriteAt replaces one audited string with a run of c at its recorded offset: the same number of bytes, so every
// later offset in the audit stays right.
func rewriteAt(t *testing.T, name string, out []byte, s StringIdent, c byte) []byte {
	t.Helper()
	if s.Offset+s.Width > len(out) {
		t.Fatalf("%s: %s does not fit", name, s.Name)
	}
	copy(out[s.Offset:s.Offset+s.Width], strings.Repeat(string(c), s.Width))
	return out
}

// rewriteUpdate rewrites SMSG_PARTY_UPDATE: GUIDs and member names (each at the offset the audit recorded).
func rewriteUpdate(t *testing.T, name string, payload []byte) ([]byte, []string, []string) {
	t.Helper()
	u, audit, err := ParseUpdateWithAudit(payload, true)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, notes, guids := rewriteGUIDs(t, name, append([]byte(nil), payload...), audit.GUIDs)
	names := 0
	for _, s := range audit.Strings {
		if s.Name != "member name" {
			continue
		}
		out = rewriteAt(t, name, out, s, 'P')
		names++
	}
	if names > 0 {
		notes = append(notes, fmt.Sprintf("%d member name(s) replaced by a run of the same length", names))
	}
	after, _, err := ParseUpdateWithAudit(out, true)
	if err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	if len(after.Members) != len(u.Members) || after.Flags != u.Flags {
		t.Fatalf("%s: the rewrite changed the structure", name)
	}
	for i := range after.Members {
		if !isSyntheticRun(after.Members[i].Name, 'P') {
			t.Fatalf("%s: the member name is not synthetic", name)
		}
	}
	return out, notes, guids
}

// rewriteState rewrites SMSG_PARTY_MEMBER_FULL_STATE: GUIDs and the run values the audit recorded.
func rewriteState(t *testing.T, name string, payload []byte) ([]byte, []string, []string) {
	t.Helper()
	_, audit, err := ParseMemberStateWithAudit(payload, true)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, notes, guids := rewriteGUIDs(t, name, append([]byte(nil), payload...), audit.GUIDs)
	for _, s := range audit.Strings {
		if s.Name != "pet name" || s.Width == 0 {
			continue
		}
		out = rewriteAt(t, name, out, s, 'P')
		notes = append(notes, "the pet name replaced by a run of the same length")
	}
	synthetic := map[string]uint32{
		"health": 100, "max health": 200, "power": 50, "max power": 100,
		"level": 1, "zone": 12, "x": 1000, "y": 2000, "z": 50,
	}
	patched := 0
	for _, id := range audit.Idents {
		want, ok := synthetic[id.Name]
		if !ok {
			continue
		}
		if id.Offset+id.Width > len(out) {
			t.Fatalf("%s: %s does not fit", name, id.Name)
		}
		switch id.Width {
		case 2:
			binary.LittleEndian.PutUint16(out[id.Offset:], uint16(want))
		case 4:
			binary.LittleEndian.PutUint32(out[id.Offset:], want)
		default:
			t.Fatalf("%s: %s has width %d", name, id.Name, id.Width)
		}
		patched++
	}
	if patched > 0 {
		notes = append(notes, fmt.Sprintf("%d run value(s) set to synthetic numbers", patched))
	}
	after, _, err := ParseMemberStateWithAudit(out, true)
	if err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	if after.Health != 100 || after.MaxHealth != 200 || after.CurrentPower != 50 || after.Level != 1 || after.X != 1000 {
		t.Fatalf("%s: the run values did not change: %+v", name, after)
	}
	return out, notes, guids
}

// rewriteInvite rewrites SMSG_PARTY_INVITE: the GUIDs, the inviter name and surname, the realm name and its
// normalised form (each to a run of the same length), and the two realm identifiers (the cfg realm id and the realm
// address) to 4242.
func rewriteInvite(t *testing.T, name string, payload []byte) ([]byte, []string, []string) {
	t.Helper()
	inv, audit, err := ParseInviteWithAudit(payload, true)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, notes, guids := rewriteGUIDs(t, name, append([]byte(nil), payload...), audit.GUIDs)
	patched := 0
	for _, id := range audit.Idents {
		if id.Width != 2 && id.Width != 4 {
			continue
		}
		if id.Offset+id.Width > len(out) {
			t.Fatalf("%s: %s does not fit", name, id.Name)
		}
		if id.Width == 2 {
			binary.LittleEndian.PutUint16(out[id.Offset:], 4242)
		} else {
			binary.LittleEndian.PutUint32(out[id.Offset:], 4242)
		}
		patched++
	}
	if patched > 0 {
		notes = append(notes, fmt.Sprintf("%d realm identifier(s) set to 4242", patched))
	}
	// Every name at its own offset (the realm name and its normalised form can be the same string, which a
	// by-value replacement would rewrite twice).
	want := map[string]byte{
		"inviter name":             'N',
		"inviter surname":          'S',
		"inviter realm name":       'R',
		"inviter realm normalised": 'R',
	}
	names := 0
	for _, s := range audit.Strings {
		c, ok := want[s.Name]
		if !ok {
			continue
		}
		out = rewriteAt(t, name, out, s, c)
		if s.Width > 0 {
			names++
		}
	}
	if names > 0 {
		notes = append(notes, fmt.Sprintf("%d name(s) replaced by a run of the same length", names))
	}
	after, _, err := ParseInviteWithAudit(out, true)
	if err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	if after.InviterName != strings.Repeat("N", len(inv.InviterName)) {
		t.Fatalf("%s: the inviter name is not synthetic: %q", name, after.InviterName)
	}
	return out, notes, guids
}

// rewriteName replaces a single name (SMSG_GROUP_DECLINE) with a run of the same length.
func rewriteName(t *testing.T, name string, payload []byte, parsed string) ([]byte, []string, []string) {
	t.Helper()
	out := bytes.ReplaceAll(payload, []byte(parsed), []byte(strings.Repeat("P", len(parsed))))
	return out, []string{"the name replaced by a run of the same length"}, nil
}

// rewriteCommandResult replaces the name and surname with runs; the result GUID in the captured packet is empty.
func rewriteCommandResult(t *testing.T, name string, payload []byte) ([]byte, []string, []string) {
	t.Helper()
	_, audit, err := ParseCommandResultWithAudit(payload, true)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out, notes, guids := rewriteGUIDs(t, name, append([]byte(nil), payload...), audit.GUIDs)
	for _, s := range audit.Strings {
		if s.Name != "command result name" && s.Name != "command result surname" {
			continue
		}
		out = rewriteAt(t, name, out, s, 'P')
	}
	notes = append(notes, "name and surname replaced by runs of the same length")
	if _, err := ParseCommandResult(out); err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	return out, notes, guids
}

// rewriteLeaderChanged replaces SMSG_GROUP_NEW_LEADER's name.
func rewriteLeaderChanged(t *testing.T, name string, payload []byte) ([]byte, []string, []string) {
	t.Helper()
	l, err := ParseLeaderChanged(payload)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out := bytes.ReplaceAll(payload, []byte(l.Name), []byte(strings.Repeat("P", len(l.Name))))
	if _, err := ParseLeaderChanged(out); err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	return out, []string{"name replaced by a run of the same length"}, nil
}

// duplicateRosterPayload is the one hand-built fixture: an SMSG_PARTY_UPDATE written the way PartyUpdate::Write
// lays it out (PartyPackets.cpp:584-627), with two member slots that carry the SAME GUID. No core path sends it
// (Group::SendUpdateToPlayer walks m_memberSlots, one slot per member), which is why it is the model's negative case
// for item 2 rather than a capture. It parses; only validateRoster rejects it.
func duplicateRosterPayload() ([]byte, []string) {
	us := wire.GUID{Low: 0x51, High: 0x0800000000000000}
	partyGUID := wire.GUID{Low: 0xF00D, High: 0x1B00000000000000}
	b := wire.NewWriter()
	b.WriteU16(0) // flags
	b.WriteU8(0)  // party index
	b.WriteU8(GroupTypeNormal)
	b.WriteI32(0) // MyIndex: the first slot is this character
	wire.WritePackedGUID(b, partyGUID)
	b.WriteU32(1) // sequence
	b.WriteU8(0)  // 70291 byte
	wire.WritePackedGUID(b, us)
	b.WriteU8(1)  // leader faction group
	b.WriteI32(0) // ping restriction
	b.WriteU64(0) // 70291 member-count prefix
	b.WriteU32(2) // member count: two slots, one GUID
	for i, name := range []string{"P", "PP"} {
		b.WriteBits(uint64(len(name)), 6)
		b.WriteBits(1, 9) // empty voice id
		b.WriteBit(i == 0)
		b.WriteBit(false)
		b.WriteBit(false)
		wire.WritePackedGUID(b, us)
		b.WriteBytes([]byte{0, 0, 0, 0, 1, 1}) // subgroup, flags, roles, rolesUnk, class, faction
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteU32(0) // leave score
		b.WriteU32(0) // season
		b.WriteU32(0) // leaves
		b.WriteU32(0) // successes
		b.WriteI32(0) // consecutive
		b.WriteU64(0) // last penalty
		b.WriteU64(0) // expiration
		b.WriteI32(0) // flags
		b.WriteBit(false)
		b.FlushBits()
		b.WriteString(name)
	}
	b.WriteBytes([]byte{0}) // no challenge, no lfg, no loot, no difficulty: four clear bits, flushed
	var guids []string
	for _, g := range []wire.GUID{partyGUID, us} {
		raw := wire.NewWriter()
		wire.WritePackedGUID(raw, g)
		guids = append(guids, hex.EncodeToString(raw.Bytes()))
	}
	return b.Bytes(), guids
}

// TestGenerateFixtures writes internal/forever/party/testdata/party.json from the party capture.
func TestGenerateFixtures(t *testing.T) {
	if os.Getenv("FOREVER_WRITE_FIXTURES") != "1" {
		t.Skip("set FOREVER_WRITE_FIXTURES=1 (and FOREVER_CAPTURE_PKT) to regenerate the fixtures")
	}
	paths := strings.FieldsFunc(os.Getenv("FOREVER_CAPTURE_PKT"), func(r rune) bool { return r == ',' || r == ':' })
	var pktRecords []pktRecord
	for _, path := range paths {
		pktRecords = append(pktRecords, readPKT(t, path)...)
	}
	var captured []pktRecord
	for _, dir := range strings.FieldsFunc(os.Getenv("FOREVER_CAPTURE_DIR"), func(r rune) bool { return r == ',' || r == ':' }) {
		captured = append(captured, readCaptureDir(t, dir)...)
	}
	if len(pktRecords) == 0 && len(captured) == 0 {
		t.Skip("FOREVER_CAPTURE_PKT and FOREVER_CAPTURE_DIR name no capture")
	}
	type pick struct {
		name  string
		core  uint32
		match func(pktRecord) bool
		// preferCaptured searches the live --capture-dir payloads first: the packets the two-client run produced.
		preferCaptured bool
	}
	picks := []pick{
		{"party_invite", wire.SMSGPartyInvite, func(r pktRecord) bool {
			_, err := ParseInvite(r.Payload)
			return err == nil
		}, true},
		{"group_decline", wire.SMSGGroupDecline, func(r pktRecord) bool {
			_, err := ParseDeclined(r.Payload)
			return err == nil
		}, true},
		{"group_uninvite", wire.SMSGGroupUninvite, func(r pktRecord) bool {
			_, err := ParseKicked(r.Payload)
			return err == nil
		}, true},
		{"group_destroyed", wire.SMSGGroupDestroyed, func(r pktRecord) bool {
			return ParseGroupDestroyed(r.Payload) == nil
		}, true},
		{"party_update_joined", wire.SMSGPartyUpdate, func(r pktRecord) bool {
			u, err := ParseUpdate(r.Payload)
			return err == nil && len(u.Members) == 2 && u.Flags&FlagDestroyed == 0
		}, true},
		{"party_update_small", wire.SMSGPartyUpdate, func(r pktRecord) bool {
			u, err := ParseUpdate(r.Payload)
			return err == nil && len(u.Members) >= 2 && len(u.Members) <= MaxPartyMembers && u.Flags&FlagDestroyed == 0
		}, false},
		{"party_update_full", wire.SMSGPartyUpdate, func(r pktRecord) bool {
			u, err := ParseUpdate(r.Payload)
			return err == nil && len(u.Members) == MaxPartyMembers && u.Flags&FlagDestroyed == 0
		}, false},
		{"party_update_destroyed", wire.SMSGPartyUpdate, func(r pktRecord) bool {
			u, err := ParseUpdate(r.Payload)
			return err == nil && u.Flags&FlagDestroyed != 0
		}, false},
		{"member_state_1", wire.SMSGPartyMemberFullState, func(r pktRecord) bool {
			_, err := ParseMemberState(r.Payload)
			return err == nil
		}, true},
		{"member_state_2", wire.SMSGPartyMemberFullState, func(r pktRecord) bool {
			_, err := ParseMemberState(r.Payload)
			return err == nil
		}, false},
		{"party_command_result", wire.SMSGPartyCommandResult, func(r pktRecord) bool {
			_, err := ParseCommandResult(r.Payload)
			return err == nil
		}, true},
		{"group_new_leader", wire.SMSGGroupNewLeader, func(r pktRecord) bool {
			_, err := ParseLeaderChanged(r.Payload)
			return err == nil
		}, false},
	}
	chosen := make([]partyFixture, 0, len(picks))
	usedStates := map[string]bool{}
	for _, pk := range picks {
		found := false
		sources := [][]pktRecord{pktRecords}
		if pk.preferCaptured {
			sources = [][]pktRecord{captured, pktRecords}
		}
		for _, source := range sources {
			for _, rec := range source {
				if rec.ToServer || rec.Core != pk.core || !pk.match(rec) {
					continue
				}
				if pk.name == "member_state_2" {
					// Pick a different member than the first state.
					ms, _ := ParseMemberState(rec.Payload)
					if usedStates[ms.GUID.String()] {
						continue
					}
					usedStates[ms.GUID.String()] = true
				}
				if pk.name == "member_state_1" {
					ms, _ := ParseMemberState(rec.Payload)
					usedStates[ms.GUID.String()] = true
				}
				var payload []byte
				var notes, guids []string
				switch pk.name {
				case "party_update_small", "party_update_full", "party_update_destroyed", "party_update_joined":
					payload, notes, guids = rewriteUpdate(t, pk.name, rec.Payload)
				case "member_state_1", "member_state_2":
					payload, notes, guids = rewriteState(t, pk.name, rec.Payload)
				case "party_command_result":
					payload, notes, guids = rewriteCommandResult(t, pk.name, rec.Payload)
				case "group_new_leader":
					payload, notes, guids = rewriteLeaderChanged(t, pk.name, rec.Payload)
				case "party_invite":
					payload, notes, guids = rewriteInvite(t, pk.name, rec.Payload)
				case "group_decline":
					name, err := ParseDeclined(rec.Payload)
					if err != nil {
						t.Fatalf("group_decline: %v", err)
					}
					payload, notes, guids = rewriteName(t, pk.name, rec.Payload, name)
				case "group_uninvite":
					payload, notes, guids = append([]byte(nil), rec.Payload...), []string{"one reason byte, no identifier"}, []string(nil)
				case "group_destroyed":
					payload, notes, guids = append([]byte(nil), rec.Payload...), []string{"empty payload, no identifier"}, []string(nil)
				}
				chosen = append(chosen, partyFixture{
					Name:    pk.name,
					Opcode:  fmt.Sprintf("0x%06X", rec.Core),
					Payload: hex.EncodeToString(payload),
					GUIDs:   guids,
					Notes:   notes,
				})
				found = true
				break
			}
			if found {
				break
			}
		}
		if !found {
			t.Logf("no capture packet for %s", pk.name)
		}
	}
	if len(chosen) == 0 {
		t.Fatal("no fixtures were cut")
	}
	dup, dupGUIDs := duplicateRosterPayload()
	chosen = append(chosen, partyFixture{
		Name:    "party_update_duplicate_members",
		Opcode:  fmt.Sprintf("0x%06X", wire.SMSGPartyUpdate),
		Payload: hex.EncodeToString(dup),
		GUIDs:   dupGUIDs,
		Notes:   []string{"hand-built, no capture: two member slots share one GUID (the model's negative case)"},
	})
	doc := partyFixtureFile{
		Note: "Payloads only (the 4-byte opcode stripped), cut from the private PKT 3.1 capture and from the " +
			"two-client --capture-dir run, then rewritten: every packed GUID is synthetic with the same mask and " +
			"type, member and inviter and realm names are runs of the same length, the invite's realm identifiers are " +
			"4242, and the member state carries fixed synthetic health, power, level, zone and position values. One " +
			"fixture, party_update_duplicate_members, is hand-built (no capture holds it): the model's duplicate-GUID " +
			"negative case. No original identifier is committed.",
		Packets: chosen,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join("testdata", "party.json"), data, 0o644); err != nil {
		t.Fatalf("writing: %v", err)
	}
	t.Logf("%d fixture(s) written", len(chosen))
}
