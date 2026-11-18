package chat

// The fixture generator: it cuts the committed synthetic fixtures out of the private capture. It is not part of CI
// (it needs FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1). For each chosen packet it takes the captured
// payload, rewrites every packed GUID the parse decoded — the ones this round keeps and the ones it drops —
// through the wire buffer's GUID hook, rewrites the two virtual-realm addresses by the offset the audit parse
// recorded, and rewrites every name, channel and text to a run of the same length. The result must still parse to
// the same structure, and the committed fixture records what was rewritten but no offset or original value.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// chatFixture is one committed packet.
type chatFixture struct {
	Name    string   `json:"name"`
	Opcode  string   `json:"opcode"`
	Payload string   `json:"payload"`
	GUIDs   []string `json:"synthetic_guids"`
	Notes   []string `json:"rewritten"`
}

type chatFixtureFile struct {
	Note    string        `json:"note"`
	Packets []chatFixture `json:"packets"`
}

// pktRecord is one packet of a PKT 3.1 log (PacketLog.cpp:29-52); this is the same reader the objects round uses.
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

// syntheticGUID builds a GUID with the same packed mask (and so the same length) and the same type nibble as orig,
// with deterministic payload bytes.
func syntheticGUID(orig []byte, n int) []byte {
	mask := binary.LittleEndian.Uint16(orig)
	var raw [16]byte
	for i := range 16 {
		if mask&(1<<i) == 0 {
			continue
		}
		raw[i] = byte(0x40 + (i%8)*3 + n%5)
	}
	if mask&0x8000 != 0 {
		// Keep the type bits (the top six of byte 15) so a parser still sees a player, party or account GUID;
		// byte 15 is the last data byte of the packed form.
		raw[15] = (raw[15] & 0x03) | (orig[len(orig)-1] & 0xFC)
	}
	out := wire.NewWriter()
	wire.WritePackedGUID(out, guidFromRaw(raw))
	return append([]byte(nil), out.Bytes()...)
}

func guidFromRaw(raw [16]byte) wire.GUID {
	var g wire.GUID
	for i := range 8 {
		g.Low |= uint64(raw[i]) << (8 * i)
		g.High |= uint64(raw[i+8]) << (8 * i)
	}
	return g
}

// rewriteChat rewrites the identifiers of one parsed chat packet and returns the synthetic payload and a note per
// rewrite. It fails unless the rewritten payload still parses and every string is synthetic.
func rewriteChat(t *testing.T, name string, payload []byte) ([]byte, []string, []string) {
	t.Helper()
	line, audit, err := ParseWithAudit(payload, true)
	if err != nil {
		t.Fatalf("%s: the captured payload does not parse: %v", name, err)
	}
	out := append([]byte(nil), payload...)
	var notes []string
	var guids []string

	// GUIDs: each distinct packed form becomes a synthetic one of the same mask and type.
	seen := map[string]bool{}
	type sub struct{ old, new []byte }
	var subs []sub
	for i, g := range audit.GUIDs {
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
		subs = append(subs, sub{orig, syn})
		guids = append(guids, hex.EncodeToString(syn))
	}
	for _, s := range subs {
		if !bytes.Contains(out, s.old) {
			t.Fatalf("%s: a packed GUID is not in the payload", name)
		}
		out = bytes.ReplaceAll(out, s.old, s.new)
	}
	if len(subs) > 0 {
		notes = append(notes, fmt.Sprintf("%d packed GUID(s) replaced", len(subs)))
	}

	// The two virtual-realm addresses are not GUIDs; patch them at the recorded offsets.
	for _, id := range audit.Idents {
		if id.Width != 4 {
			continue
		}
		if id.Offset+4 > len(out) {
			t.Fatalf("%s: identifier %s does not fit", name, id.Name)
		}
		binary.LittleEndian.PutUint32(out[id.Offset:], 4242)
	}
	if len(audit.Idents) > 0 {
		notes = append(notes, fmt.Sprintf("%d virtual-realm address(es) set to 4242", len(audit.Idents)))
	}

	// Names, channel and text: same length, one repeated letter each. Two fields can hold the same string
	// (Player::Whisper sets SenderName and TargetName to the same name), so each distinct value is replaced once.
	replacements := map[string]string{}
	add := func(value, letter string) string {
		if value == "" {
			return ""
		}
		if r, ok := replacements[value]; ok {
			return r
		}
		r := strings.Repeat(letter, len(value))
		replacements[value] = r
		return r
	}
	wantSender := add(line.SenderName, "N")
	wantTarget := add(line.TargetName, "S")
	wantPrefix := add(line.Prefix, "P")
	wantChannel := add(line.Channel, "C")
	wantText := add(line.Text, "T")
	values := make([]string, 0, len(replacements))
	for v := range replacements {
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		if !bytes.Contains(out, []byte(v)) {
			t.Fatalf("%s: a string %q is not in the payload", name, v)
		}
		out = bytes.ReplaceAll(out, []byte(v), []byte(replacements[v]))
		notes = append(notes, "a string replaced by a run of the same length")
	}

	// The rewrite must still be the same packet.
	after, _, err := ParseWithAudit(out, true)
	if err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	if after.Type != line.Type || after.Language != line.Language || len(after.Text) != len(line.Text) {
		t.Fatalf("%s: the rewrite changed the structure", name)
	}
	for _, id := range []struct {
		got  string
		want string
		what string
	}{
		{after.SenderName, wantSender, "sender name"},
		{after.TargetName, wantTarget, "target name"},
		{after.Prefix, wantPrefix, "addon prefix"},
		{after.Channel, wantChannel, "channel"},
		{after.Text, wantText, "text"},
	} {
		if id.got != id.want {
			t.Fatalf("%s: the %s is not synthetic: %q (want %q)", name, id.what, id.got, id.want)
		}
	}
	return out, notes, guids
}

// TestGenerateFixtures writes internal/forever/chat/testdata/chat.json from the capture.
func TestGenerateFixtures(t *testing.T) {
	if os.Getenv("FOREVER_WRITE_FIXTURES") != "1" {
		t.Skip("set FOREVER_WRITE_FIXTURES=1 (and FOREVER_CAPTURE_PKT) to regenerate the fixtures")
	}
	paths := strings.FieldsFunc(os.Getenv("FOREVER_CAPTURE_PKT"), func(r rune) bool { return r == ',' || r == ':' })
	if len(paths) == 0 {
		t.Skip("FOREVER_CAPTURE_PKT names no capture")
	}
	type pick struct {
		name  string
		core  uint32
		match func(Line) bool
	}
	any := func(Line) bool { return true }
	picks := []pick{
		{"chat_system", wire.SMSGChat, func(l Line) bool { return l.Type == TypeSystem }},
		{"chat_party", wire.SMSGChat, func(l Line) bool { return l.Type == TypeParty }},
		{"chat_party_leader", wire.SMSGChat, func(l Line) bool { return l.Type == TypePartyLeader }},
		{"chat_whisper_addon", wire.SMSGChat, func(l Line) bool {
			return l.Type == TypeWhisper && l.Language == LanguageAddon
		}},
		{"chat_whisper", wire.SMSGChat, func(l Line) bool {
			return l.Type == TypeWhisper && l.Language == LanguageUniversal && l.Text != ""
		}},
		{"chat_channel", wire.SMSGChat, func(l Line) bool { return l.Type == TypeChannel }},
		{"chat_achievement", wire.SMSGChat, func(l Line) bool { return l.Type == TypeAchievement }},
		{"print_notification", wire.SMSGPrintNotification, any},
		{"emote", wire.SMSGEmote, any},
	}
	chosen := make([]chatFixture, 0, len(picks))
	for _, pk := range picks {
		found := false
		for _, path := range paths {
			for _, rec := range readPKT(t, path) {
				if rec.ToServer || rec.Core != pk.core {
					continue
				}
				var line Line
				ok := false
				switch rec.Core {
				case wire.SMSGChat:
					line, ok = parseForPick(rec.Payload)
				case wire.SMSGPrintNotification:
					if pk.name == "print_notification" {
						_, err := ParseNotification(rec.Payload)
						ok = err == nil
					}
				case wire.SMSGEmote:
					if pk.name == "emote" {
						_, err := ParseEmote(rec.Payload)
						ok = err == nil
					}
				}
				if !ok || !pk.match(line) {
					continue
				}
				var payload []byte
				var notes, guids []string
				switch rec.Core {
				case wire.SMSGChat:
					payload, notes, guids = rewriteChat(t, pk.name, rec.Payload)
				case wire.SMSGPrintNotification:
					payload, notes = rewriteNotification(t, pk.name, rec.Payload)
				case wire.SMSGEmote:
					payload, notes, guids = rewriteEmote(t, pk.name, rec.Payload)
				}
				chosen = append(chosen, chatFixture{
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
			t.Logf("no capture packet for %s (it stays hand-built)", pk.name)
		}
	}
	if len(chosen) == 0 {
		t.Fatal("no fixtures were cut")
	}
	doc := chatFixtureFile{
		Note: "Payloads only (the 4-byte opcode stripped), cut from the private PKT 3.1 capture and rewritten: " +
			"every packed GUID is synthetic with the same mask and type, the two virtual-realm addresses are 4242, " +
			"and sender name, target name, addon prefix, channel and text are runs of the same length. No original " +
			"identifier is committed.",
		Packets: chosen,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshalling the fixtures: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join("testdata", "chat.json"), data, 0o644); err != nil {
		t.Fatalf("writing the fixtures: %v", err)
	}
	t.Logf("%d fixture(s) written", len(chosen))
}

// rewriteNotification replaces SMSG_PRINT_NOTIFICATION's text with a run of the same length.
func rewriteNotification(t *testing.T, name string, payload []byte) ([]byte, []string) {
	t.Helper()
	text, err := ParseNotification(payload)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if text == "" {
		return payload, nil
	}
	out := bytes.ReplaceAll(payload, []byte(text), []byte(strings.Repeat("T", len(text))))
	if _, err := ParseNotification(out); err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	return out, []string{"text replaced by a run of the same length"}
}

// rewriteEmote rewrites SMSG_EMOTE's packed GUID; it carries no string.
func rewriteEmote(t *testing.T, name string, payload []byte) ([]byte, []string, []string) {
	t.Helper()
	_, audit, err := ParseEmoteWithAudit(payload, true)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out := append([]byte(nil), payload...)
	var notes, guids []string
	seen := map[string]bool{}
	for i, g := range audit.GUIDs {
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
			t.Fatalf("%s: the packed GUID is not in the payload", name)
		}
		out = bytes.ReplaceAll(out, orig, syn)
		guids = append(guids, hex.EncodeToString(syn))
	}
	if len(guids) > 0 {
		notes = append(notes, fmt.Sprintf("%d packed GUID(s) replaced", len(guids)))
	}
	if _, err := ParseEmote(out); err != nil {
		t.Fatalf("%s: the rewritten payload does not parse: %v", name, err)
	}
	return out, notes, guids
}

func parseForPick(p []byte) (Line, bool) {
	l, err := Parse(p)
	return l, err == nil
}
