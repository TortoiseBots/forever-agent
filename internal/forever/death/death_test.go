package death

// The death wire layer against the committed fixtures: every captured packet parses to its last byte, the writers
// reproduce the captured client packets byte for byte, every GUID in a fixture is synthetic (and the audit can
// fail), the hand-built packets are labelled, the bounds refuse an oversized count or length, and nothing panics on
// mutated input. The capture itself is never in CI; TestCaptureDeathPackets runs only when FOREVER_CAPTURE_PKT
// names the private log.

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type fixtureDoc struct {
	Note    string          `json:"note"`
	Packets []fixturePacket `json:"packets"`
}

// readFixtureDoc reads the committed fixture file; it takes no testing.T so the fuzz seed can use it.
func readFixtureDoc() (fixtureDoc, error) {
	var doc fixtureDoc
	data, err := os.ReadFile("testdata/death.json")
	if err != nil {
		return doc, err
	}
	err = json.Unmarshal(data, &doc)
	return doc, err
}

func mustFixtureDoc(t *testing.T) fixtureDoc {
	t.Helper()
	doc, err := readFixtureDoc()
	if err != nil {
		t.Fatalf("reading the fixtures: %v", err)
	}
	return doc
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	for _, p := range mustFixtureDoc(t).Packets {
		if p.Name == name {
			raw, err := hex.DecodeString(p.Payload)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return raw
		}
	}
	t.Fatalf("the fixtures have no %q", name)
	return nil
}

func TestFixtureDeathReleaseLoc(t *testing.T) {
	grave, err := ParseDeathRelease(loadFixture(t, "death_release_loc"))
	if err != nil {
		t.Fatal(err)
	}
	if grave.MapID != 0 || grave.Revived() {
		t.Fatalf("the graveyard: %+v", grave)
	}
	if grave.X != 1000 || grave.Y != 2000 || grave.Z != 50 {
		t.Fatalf("the synthetic graveyard point: %.1f %.1f %.1f", grave.X, grave.Y, grave.Z)
	}
	revive, err := ParseDeathRelease(loadFixture(t, "death_release_loc_revived"))
	if err != nil {
		t.Fatal(err)
	}
	if !revive.Revived() || revive.MapID != -1 {
		t.Fatalf("the revive marker: %+v", revive)
	}
	if revive.X != 0 || revive.Y != 0 || revive.Z != 0 {
		t.Fatalf("the revive marker carries a location: %+v", revive)
	}
}

func TestFixtureReclaimDelay(t *testing.T) {
	delay, err := ParseCorpseReclaimDelay(loadFixture(t, "corpse_reclaim_delay"))
	if err != nil {
		t.Fatal(err)
	}
	// The first step of the server's {30, 60, 120} seconds (Player.cpp:175): a game value, not a clock reading.
	if delay != 30000 {
		t.Fatalf("the reclaim delay is %d ms, want the capture's 30000", delay)
	}
}

func TestFixturePreRessurect(t *testing.T) {
	g, err := ParsePreRessurect(loadFixture(t, "pre_ressurect"))
	if err != nil {
		t.Fatal(err)
	}
	if g.IsZero() || g.Type() != 2 {
		t.Fatalf("the pre-ressurect GUID: %s (type %d)", g, g.Type())
	}
}

func TestFixtureStandState(t *testing.T) {
	s, err := ParseStandState(loadFixture(t, "stand_state_update"))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != 0 || s.AnimKit != 0 {
		t.Fatalf("the death stand state: %+v", s)
	}
}

func TestFixtureGossipMessage(t *testing.T) {
	m, err := ParseGossipMessage(loadFixture(t, "gossip_message"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Unit.IsZero() || m.Unit.Type() != 8 { // HighGuid::Creature (ObjectGuid.h:118)
		t.Fatalf("the gossip unit: %s (type %d)", m.Unit, m.Unit.Type())
	}
	if m.MenuID != syntheticGossipMenuID || len(m.Options) != 1 || m.Texts != 0 {
		t.Fatalf("the menu: id %d, %d option(s), %d text(s)", m.MenuID, len(m.Options), m.Texts)
	}
	o := m.Options[0]
	if o.ID != syntheticGossipOptionID || o.NPC != GossipNPCSpiritHealer {
		t.Fatalf("the option: id 0x%X, npc %d", o.ID, o.NPC)
	}
	if o.Text != "Return me to life." {
		t.Fatalf("the option text: %q", o.Text)
	}
	if o.HasSpell || o.Confirm != "" {
		t.Fatalf("the option carries %v / %q", o.HasSpell, o.Confirm)
	}
	// The menu's two trailing bits: no random text, one broadcast text id (the server's title for the healer).
	if m.HasRandomText || !m.HasBroadcastText || m.BroadcastTextID != 2545 {
		t.Fatalf("the menu's text blocks: random %v/%d, broadcast %v/%d",
			m.HasRandomText, m.RandomTextID, m.HasBroadcastText, m.BroadcastTextID)
	}
	if _, ok := m.SpiritHealer(); !ok {
		t.Fatal("the spirit healer option was not found")
	}
}

func TestFixtureResurrectRequests(t *testing.T) {
	player, err := ParseResurrectRequest(loadFixture(t, "resurrect_request"))
	if err != nil {
		t.Fatal(err)
	}
	if player.Offerer.IsZero() || player.Offerer.Type() != 2 {
		t.Fatalf("the player offerer: %s", player.Offerer)
	}
	if player.SpellID != 2008 || player.UseTimer || player.Sickness || player.Name != "" {
		t.Fatalf("the player's offer: %+v", player)
	}
	healer, err := ParseResurrectRequest(loadFixture(t, "resurrect_request_healer"))
	if err != nil {
		t.Fatal(err)
	}
	if healer.SpellID != SpiritHealerSpell || !healer.Sickness || healer.Name != "H" {
		t.Fatalf("the healer's offer: %+v", healer)
	}
}

func TestFixtureCorpseLocation(t *testing.T) {
	c, err := ParseCorpseLocation(loadFixture(t, "corpse_location"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Valid || c.Player.IsZero() || c.Player.Type() != 2 {
		t.Fatalf("the corpse answer: %+v", c)
	}
	if c.MapID != 0 || c.ActualMapID != 0 || c.X != 1000 || c.Y != 2000 || c.Z != 50 {
		t.Fatalf("the corpse position: map %d/%d at %.0f %.0f %.0f", c.ActualMapID, c.MapID, c.X, c.Y, c.Z)
	}
	if !c.Transport.IsZero() {
		t.Fatalf("the corpse is on transport %s", c.Transport)
	}
	inv, err := ParseCorpseLocation(loadFixture(t, "corpse_location_invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if inv.Valid {
		t.Fatal("the no-corpse answer is valid")
	}
	a, err := ParseCorpseTransportAnswer(loadFixture(t, "corpse_transport_answer"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Player.IsZero() || a.X != 0 || a.Facing != 0 {
		t.Fatalf("the empty transport answer: %+v", a)
	}
}

// TestWritersMatchTheCapturedClient rebuilds every captured client packet from its own fields and requires the same
// bytes: the real client wrote them, so this is the writers' oracle.
func TestWritersMatchTheCapturedClient(t *testing.T) {
	cases := []struct {
		fixture string
		build   func(*testing.T, []byte) []byte
	}{
		{"repop_request", func(*testing.T, []byte) []byte { return BuildRepopRequest(false) }},
		{"talk_to_gossip", func(t *testing.T, p []byte) []byte {
			return BuildTalkToGossip(readGUIDPrefix(t, p, 0))
		}},
		{"spirit_healer_activate", func(t *testing.T, p []byte) []byte {
			return BuildSpiritHealerActivate(readGUIDPrefix(t, p, 0))
		}},
		{"close_interaction", func(t *testing.T, p []byte) []byte {
			return BuildCloseInteraction(readGUIDPrefix(t, p, 0))
		}},
		{"gossip_select_option", func(t *testing.T, p []byte) []byte {
			b := wire.NewReader(p)
			g := wire.ReadPackedGUID(b)
			menu := b.ReadI32()
			option := b.ReadI32()
			n := int(b.ReadBits(8))
			b.FlushBits()
			code := b.ReadString(n)
			if b.Err() != nil || b.Remaining() != 0 {
				t.Fatalf("the captured select: %v, %d byte(s) left", b.Err(), b.Remaining())
			}
			return BuildGossipSelectOption(g, menu, option, code)
		}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			want := loadFixture(t, c.fixture)
			got := c.build(t, want)
			if string(got) != string(want) {
				t.Fatalf("the writer's bytes differ\n got % X\nwant % X", got, want)
			}
		})
	}
}

// readGUIDPrefix reads a packed GUID at off and requires the reader to end there.
func readGUIDPrefix(t *testing.T, p []byte, off int) wire.GUID {
	t.Helper()
	b := wire.NewReader(p)
	b.Skip(off)
	g := wire.ReadPackedGUID(b)
	if b.Err() != nil || b.Remaining() != 0 {
		t.Fatalf("the fixture is not one GUID at %d: %v, %d byte(s) left", off, b.Err(), b.Remaining())
	}
	return g
}

// TestFixtureGUIDsAreSynthetic walks every GUID a fixture carries through its parser's audit hook and requires it
// to be one the generator wrote. The negative subtests prove the sweep can fail.
func TestFixtureGUIDsAreSynthetic(t *testing.T) {
	synthetic := map[string]bool{}
	for _, p := range mustFixtureDoc(t).Packets {
		for _, g := range p.GuideGUIDs {
			synthetic[g] = true
		}
	}
	type auditCase struct {
		name  string
		parse func([]byte, func(wire.GUID, int)) error
		off   int
	}
	cases := []auditCase{
		{"pre_ressurect", func(p []byte, at func(wire.GUID, int)) error {
			_, err := parsePreRessurect(p, at)
			return err
		}, 0},
		{"gossip_message", func(p []byte, at func(wire.GUID, int)) error {
			_, err := parseGossipMessage(p, at)
			return err
		}, 0},
		{"resurrect_request", func(p []byte, at func(wire.GUID, int)) error {
			_, err := parseResurrectRequest(p, at)
			return err
		}, 0},
		{"resurrect_request_healer", func(p []byte, at func(wire.GUID, int)) error {
			_, err := parseResurrectRequest(p, at)
			return err
		}, 0},
		{"corpse_location", func(p []byte, at func(wire.GUID, int)) error {
			_, err := parseCorpseLocation(p, at)
			return err
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := loadFixture(t, c.name)
			seen := 0
			if err := c.parse(raw, func(g wire.GUID, off int) {
				seen++
				if g.IsZero() {
					return // an empty transport is not an identifier
				}
				if !synthetic[g.String()] {
					t.Fatalf("GUID %s at offset %d is not synthetic", g, off)
				}
			}); err != nil {
				t.Fatalf("%v", err)
			}
			if seen == 0 {
				t.Fatal("no GUID was decoded")
			}
			// The same fixture with its first GUID rewritten to a different synthetic value of the same packed
			// shape must fail the audit.
			doctored := append([]byte(nil), raw...)
			b := wire.NewReader(doctored)
			b.Skip(c.off)
			before := b.Offset()
			wire.ReadPackedGUID(b)
			length := b.Offset() - before
			syntheticPacked(doctored[c.off:c.off+length], 200)
			if got := wire.ReadPackedGUID(wire.NewReader(doctored[c.off : c.off+length])); synthetic[got.String()] {
				t.Fatalf("the doctored GUID is one of the fixture's own: %s", got)
			}
			failed := false
			_ = c.parse(doctored, func(g wire.GUID, off int) {
				if !g.IsZero() && !synthetic[g.String()] {
					failed = true
				}
			})
			if !failed {
				t.Fatal("the audit accepted a replaced GUID")
			}
		})
	}
}

// TestFixtureConsumedExactly requires a parser to refuse one byte more than its layout: a parser that leaves bytes
// behind cannot be trusted with the fields it read.
func TestFixtureConsumedExactly(t *testing.T) {
	raw := append(loadFixture(t, "death_release_loc"), 0x00)
	if _, err := ParseDeathRelease(raw); err == nil {
		t.Fatal("the parser accepted a death release with a trailing byte")
	}
}

func TestHandBuiltFixturesAreLabelled(t *testing.T) {
	hand := map[string]bool{
		"resurrect_request": true, "resurrect_request_healer": true,
		"corpse_location": true, "corpse_location_invalid": true, "corpse_transport_answer": true,
	}
	for _, p := range mustFixtureDoc(t).Packets {
		isHand := len(p.Source) >= 10 && p.Source[:10] == "hand-built"
		if hand[p.Name] && !isHand {
			t.Fatalf("%s is hand-built but says %q", p.Name, p.Source)
		}
		if !hand[p.Name] && p.Source != "captured" {
			t.Fatalf("%s is not labelled captured or hand-built: %q", p.Name, p.Source)
		}
	}
}

// TestFixtureGossipIDsAreSynthetic is the identifier sweep for the gossip menu: the menu's own id and every option's
// id are the server's data, so a committed fixture carries the generator's synthetic 4242, in the menu and in the
// select that answers it. The negative subtests prove the sweep can fail.
func TestFixtureGossipIDsAreSynthetic(t *testing.T) {
	menu := loadFixture(t, "gossip_message")
	// menuFindings is the sweep: one finding per id that is not the synthetic one.
	menuFindings := func(payload []byte) []string {
		var ids []GossipIdent
		if _, err := parseGossipMessageAudit(payload, nil, func(id GossipIdent) { ids = append(ids, id) }); err != nil {
			return []string{"the menu does not parse"}
		}
		if len(ids) < 2 {
			return []string{"the menu reports fewer than a menu id and an option id"}
		}
		var findings []string
		for _, id := range ids {
			want := syntheticGossipMenuID
			if id.Name == "gossip option id" {
				want = syntheticGossipOptionID
			}
			if id.Value != want {
				findings = append(findings, id.Name+" is not the synthetic value")
			}
		}
		return findings
	}
	if got := menuFindings(menu); len(got) != 0 {
		t.Fatalf("the gossip menu: %s", strings.Join(got, "; "))
	}
	selectIDs := func(payload []byte) (int32, int32) {
		b := wire.NewReader(payload)
		wire.ReadPackedGUID(b)
		m, o := b.ReadI32(), b.ReadI32()
		if b.Err() != nil || b.Remaining() == 0 {
			t.Fatalf("the captured select does not read as a GUID, a menu id, an option id and a bit block: %v", b.Err())
		}
		return m, o
	}
	if m, o := selectIDs(loadFixture(t, "gossip_select_option")); m != syntheticGossipMenuID || o != syntheticGossipOptionID {
		t.Fatalf("the select's ids are %d/%d, not the synthetic %d/%d", m, o, syntheticGossipMenuID, syntheticGossipOptionID)
	}
	t.Run("a menu id the generator did not write", func(t *testing.T) {
		doctored := append([]byte(nil), menu...)
		var ids []GossipIdent
		if _, err := parseGossipMessageAudit(doctored, nil, func(id GossipIdent) { ids = append(ids, id) }); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id.Name == "gossip menu id" {
				binary.LittleEndian.PutUint32(doctored[id.Offset:], 1) // a plausible built-in value
			}
		}
		if got := menuFindings(doctored); len(got) == 0 {
			t.Fatal("the sweep accepted a menu id the generator did not write")
		}
	})
	t.Run("an option id the generator did not write", func(t *testing.T) {
		doctored := append([]byte(nil), menu...)
		var ids []GossipIdent
		if _, err := parseGossipMessageAudit(doctored, nil, func(id GossipIdent) { ids = append(ids, id) }); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id.Name == "gossip option id" {
				binary.LittleEndian.PutUint32(doctored[id.Offset:], 2)
			}
		}
		if got := menuFindings(doctored); len(got) == 0 {
			t.Fatal("the sweep accepted an option id the generator did not write")
		}
	})
}

// TestBounds refuses an oversized count or length instead of allocating from it.
func TestBounds(t *testing.T) {
	t.Run("gossip options", func(t *testing.T) {
		assertRefused(t, gossipHeader(maxGossipOptions+1, 0))
	})
	t.Run("gossip texts", func(t *testing.T) {
		assertRefused(t, gossipHeader(0, maxGossipTexts+1))
	})
	t.Run("treasure items", func(t *testing.T) {
		assertRefused(t, gossipOneOptionWithTreasure(maxTreasureItems+1))
	})
	t.Run("resurrect name", func(t *testing.T) {
		b := wire.NewWriter()
		wire.WritePackedGUID(b, syntaxGUID())
		b.WriteU32(0)
		b.WriteU32(0)
		b.WriteI32(1)
		b.WriteBits(maxResurrectNameLen+1, 11)
		b.WriteBit(false)
		b.WriteBit(false)
		b.FlushBits()
		if _, err := ParseResurrectRequest(b.Bytes()); err == nil {
			t.Fatal("the parser accepted a name over the bound")
		}
	})
	t.Run("gossip option text", func(t *testing.T) {
		assertRefused(t, gossipOneOption(maxGossipTextLen+1))
	})
}

// assertRefused requires the gossip parser to refuse the payload without panicking.
func assertRefused(t *testing.T, p []byte) {
	t.Helper()
	if _, err := ParseGossipMessage(p); err == nil {
		t.Fatal("the parser accepted a count or length over the bound")
	}
}

// gossipHeader builds the header of a gossip message with the given option and text counts and no bodies.
func gossipHeader(options, texts int) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, syntaxGUID())
	b.WriteI32(83)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU32(uint32(options))
	b.WriteU32(uint32(texts))
	return b.Bytes()
}

// gossipOneOptionWithTreasure builds a gossip message with one option that declares n treasure items and nothing
// after them (the count is refused before any item is read).
func gossipOneOptionWithTreasure(n int) []byte {
	b := wire.NewWriter()
	b.WriteBytes(gossipHeader(1, 0))
	b.WriteI32(1) // option id
	b.WriteU32(4) // SpiritHealer
	b.WriteU8(0)  // option flags
	b.WriteU64(0) // cost
	b.WriteU32(0) // language
	b.WriteU32(uint32(n))
	return b.Bytes()
}

// gossipOneOption builds a gossip message with one option whose 12-bit text length is textLen, followed by nothing.
func gossipOneOption(textLen int) []byte {
	b := wire.NewWriter()
	b.WriteBytes(gossipHeader(1, 0))
	b.WriteI32(1)
	b.WriteU32(4)
	b.WriteU8(0)
	b.WriteU64(0)
	b.WriteU32(0)
	b.WriteU32(0) // no treasure items
	b.WriteI32(0) // flags
	b.WriteI32(0) // order
	b.WriteBits(uint64(textLen), 12)
	b.WriteBits(0, 12)
	b.WriteBits(0, 2)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBits(1, 8)
	b.FlushBits()
	return b.Bytes()
}

func FuzzDeathPackets(f *testing.F) {
	doc, err := readFixtureDoc()
	if err != nil {
		f.Fatalf("reading the fixtures: %v", err)
	}
	parsers := []func([]byte) error{
		func(p []byte) error { _, err := ParseDeathRelease(p); return err },
		func(p []byte) error { _, err := ParseCorpseReclaimDelay(p); return err },
		func(p []byte) error { _, err := ParsePreRessurect(p); return err },
		func(p []byte) error { _, err := ParseResurrectRequest(p); return err },
		func(p []byte) error { _, err := ParseCorpseLocation(p); return err },
		func(p []byte) error { _, err := ParseCorpseTransportAnswer(p); return err },
		func(p []byte) error { _, err := ParseStandState(p); return err },
		func(p []byte) error { _, err := ParseGossipMessage(p); return err },
	}
	// The opcode's name selects the parser, so every seed exercises the parser its payload belongs to.
	which := map[string]byte{
		"SMSG_DEATH_RELEASE_LOC":      0,
		"SMSG_CORPSE_RECLAIM_DELAY":   1,
		"SMSG_PRE_RESSURECT":          2,
		"SMSG_RESURRECT_REQUEST":      3,
		"SMSG_CORPSE_LOCATION":        4,
		"SMSG_CORPSE_TRANSPORT_QUERY": 5,
		"SMSG_STAND_STATE_UPDATE":     6,
		"SMSG_GOSSIP_MESSAGE":         7,
	}
	for _, p := range doc.Packets {
		raw, err := hex.DecodeString(p.Payload)
		if err != nil {
			f.Fatalf("%s: %v", p.Name, err)
		}
		if k, ok := which[p.Opcode]; ok {
			f.Add(k, raw)
		}
	}
	f.Fuzz(func(t *testing.T, k byte, p []byte) {
		_ = parsers[int(k)%len(parsers)](p) // no panic, no allocation driven by a declared count
	})
}

// TestCaptureDeathPackets parses every death and gossip packet of the private capture exactly. It never runs in CI.
func TestCaptureDeathPackets(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	counts := map[string]int{}
	for _, p := range readPKT(t, path) {
		if p.ToServer {
			continue
		}
		var err error
		name := wire.ServerName(p.Core)
		switch name {
		case "SMSG_DEATH_RELEASE_LOC":
			_, err = ParseDeathRelease(p.Payload)
		case "SMSG_CORPSE_RECLAIM_DELAY":
			_, err = ParseCorpseReclaimDelay(p.Payload)
		case "SMSG_PRE_RESSURECT":
			_, err = ParsePreRessurect(p.Payload)
		case "SMSG_RESURRECT_REQUEST":
			_, err = ParseResurrectRequest(p.Payload)
		case "SMSG_CORPSE_LOCATION":
			_, err = ParseCorpseLocation(p.Payload)
		case "SMSG_CORPSE_TRANSPORT_QUERY":
			_, err = ParseCorpseTransportAnswer(p.Payload)
		case "SMSG_STAND_STATE_UPDATE":
			_, err = ParseStandState(p.Payload)
		case "SMSG_GOSSIP_MESSAGE":
			_, err = ParseGossipMessage(p.Payload)
		default:
			continue
		}
		if err != nil {
			t.Fatalf("%s (%d bytes): %v", name, len(p.Payload), err)
		}
		counts[name]++
	}
	t.Logf("parsed %v", counts)
}
