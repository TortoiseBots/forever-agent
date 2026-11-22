package world

// The session's spellbook (ticket #93 round R5): the spells the character knows, kept from SMSG_SEND_KNOWN_SPELLS
// and the three change packets, one event per change, and the `spells` serve method that answers the book with the
// generated data's metadata. Only the session goroutine touches the book, like every other model here.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// spellBook is the character's spellbook: the known ids as a set and in arrival order (the serve method sorts).
type spellBook struct {
	known map[int32]bool
	order []int32
}

func newSpellBook() *spellBook { return &spellBook{known: map[int32]bool{}} }

// replace resets the book to ids, the full list SMSG_SEND_KNOWN_SPELLS carries at login.
func (b *spellBook) replace(ids []int32) {
	b.known = make(map[int32]bool, len(ids))
	b.order = b.order[:0]
	for _, id := range ids {
		if b.known[id] {
			continue
		}
		b.known[id] = true
		b.order = append(b.order, id)
	}
}

// add adds one id; it reports whether the book changed.
func (b *spellBook) add(id int32) bool {
	if b.known[id] {
		return false
	}
	b.known[id] = true
	b.order = append(b.order, id)
	return true
}

// remove drops one id; it reports whether the book changed.
func (b *spellBook) remove(id int32) bool {
	if !b.known[id] {
		return false
	}
	delete(b.known, id)
	for i, v := range b.order {
		if v == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	return true
}

// has reports whether the character knows the spell.
func (b *spellBook) has(id int32) bool { return b.known[id] }

// ids returns the known ids sorted ascending, the shape the report and the fight plan read.
func (b *spellBook) ids() []int32 {
	out := append([]int32(nil), b.order...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// spellbook returns this session's book, making an empty one on first use.
func (s *session) spellbook() *spellBook {
	if s.spells == nil {
		s.spells = newSpellBook()
	}
	return s.spells
}

// spellTable is the spell data this session reads: the table a caller injected (a test), the embedded table
// otherwise.
func (s *session) spellTable() *foreverspells.Table {
	if s.spellData == nil {
		if s.opts.spellTable != nil {
			s.spellData = s.opts.spellTable
		} else {
			s.spellData = foreverspells.Default()
		}
	}
	return s.spellData
}

// spellName is the best name this session has for a spell: the generated data first, then the server's own
// SpellName record when the combat log already cached one; "" when neither has it.
func (s *session) spellName(id int32) string {
	if id <= 0 {
		return ""
	}
	if name := s.spellTable().Name(uint32(id)); name != "" {
		return name
	}
	return s.cachedSpellName(uint32(id))
}

// cachedSpellName is the server's SpellName record for id when the session already holds it. It never asks the
// server: the spell book path is a read and must not block the session on a query.
func (s *session) cachedSpellName(id uint32) string {
	if s.db == nil {
		return ""
	}
	data, found, ok := s.db.Get(tables.TableSpellName, id)
	if !ok || !found {
		return ""
	}
	name, err := tables.ParseSpellName(data, id)
	if err != nil {
		return ""
	}
	return name.Name
}

// learnSpell adds the learned spell to the book. A higher rank carries the id it supersedes: that one leaves the
// book and the event names it, so a reader sees the replacement rather than a second rank beside the old.
func (s *session) learnSpell(v combat.LearnedSpell) {
	book := s.spellbook()
	added := book.add(v.SpellID)
	if v.Superceded != nil {
		// A repeated or out-of-order packet that changes nothing writes no event.
		removed := book.remove(*v.Superceded)
		if added || removed {
			s.spellEvent(events.KindSpellSuperseded, v.SpellID, map[string]any{"was": *v.Superceded})
		}
	} else if added {
		s.spellEvent(events.KindSpellLearned, v.SpellID, nil)
	}
	s.syncKnown()
}

// unlearnSpell drops the spell and writes spell_removed, only when the spell was in the book.
func (s *session) unlearnSpell(id int32) {
	if s.spellbook().remove(id) {
		s.spellEvent(events.KindSpellRemoved, id, nil)
	}
	s.syncKnown()
}

// spellEvent writes one spell book event: the spell id, its name as text, and the kind's own data.
func (s *session) spellEvent(kind string, id int32, data map[string]any) {
	s.event(Event{Kind: kind, SpellID: uint32(id), Text: s.spellName(id), Data: data})
}

// syncKnown re-derives the session's known list and the report's copy from the book, both sorted ascending.
func (s *session) syncKnown() {
	ids := s.spellbook().ids()
	s.known = ids
	if s.report != nil {
		s.report.KnownSpells = append([]int32(nil), ids...)
	}
}

// serveSpellsParams is the `spells` filter, the same shape as the Turtle method (spell_id, name, highest, passive).
type serveSpellsParams struct {
	SpellID uint32 `json:"spell_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Highest bool   `json:"highest,omitempty"`
	Passive bool   `json:"passive,omitempty"`
}

// ServeSpellRow is one spell of the book: the id and name, the rank and whether it is the highest known rank, the
// generated data's metadata, and its cooldown numbers. The JSON names are the Turtle row's where they exist.
type ServeSpellRow struct {
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name"`
	Rank    uint8  `json:"rank,omitempty"`
	// Highest is true for the highest rank of the name among the KNOWN ids (the only id of a rankless name).
	Highest bool   `json:"highest"`
	Meta    bool   `json:"meta"` // false for an id the generated table does not hold
	Level   uint16 `json:"level,omitempty"`
	Passive bool   `json:"passive,omitempty"`
	// CooldownMS, Category and CategoryCDMS are the class ability's numbers; GCDMS its global cooldown.
	CooldownMS   uint32 `json:"cooldown_ms,omitempty"`
	Category     uint16 `json:"category,omitempty"`
	CategoryCDMS uint32 `json:"category_cd_ms,omitempty"`
	GCDMS        uint16 `json:"gcd_ms"`
	Mechanic     uint8  `json:"mechanic,omitempty"`
	Dispel       uint8  `json:"dispel,omitempty"`
	Class        string `json:"class,omitempty"`
}

// serveSpells answers the book: every known spell, filtered, sorted by name and rank. Passive spells are left out
// unless the caller asks for them.
func (s *session) serveSpells(p serveSpellsParams) ([]ServeSpellRow, error) {
	type candidate struct {
		row ServeSpellRow
		key string
	}
	type bestRank struct {
		rank uint8
		id   int32
	}
	ids := s.spellbook().ids()
	cands := make([]candidate, 0, len(ids))
	best := make(map[string]bestRank, len(ids))
	for _, id := range ids {
		row := s.serveSpellRow(id)
		key := spellRowKey(row)
		cands = append(cands, candidate{row: row, key: key})
		if cur, ok := best[key]; !ok || row.Rank > cur.rank || (row.Rank == cur.rank && id > cur.id) {
			best[key] = bestRank{rank: row.Rank, id: id}
		}
	}
	needle := strings.ToLower(strings.TrimSpace(p.Name))
	out := make([]ServeSpellRow, 0, len(cands))
	for _, c := range cands {
		if p.SpellID != 0 && c.row.SpellID != p.SpellID {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(c.row.Name), needle) {
			continue
		}
		if c.row.Passive && !p.Passive {
			continue
		}
		c.row.Highest = best[c.key].id == int32(c.row.SpellID)
		if p.Highest && !c.row.Highest {
			continue
		}
		out = append(out, c.row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].SpellID < out[j].SpellID
	})
	return out, nil
}

// serveSpellRow builds one row for a known id.
func (s *session) serveSpellRow(id int32) ServeSpellRow {
	row := ServeSpellRow{SpellID: uint32(id), Name: s.spellName(id)}
	sp, ok := s.spellTable().Lookup(uint32(id))
	if !ok {
		return row
	}
	row.Meta = true
	row.Rank = sp.Rank
	row.Level = sp.Level
	row.Passive = sp.Passive()
	row.CooldownMS = sp.RecoveryMS
	row.Category = sp.Category
	row.CategoryCDMS = sp.CategoryRecoveryMS
	row.GCDMS = sp.GCDMS
	row.Mechanic = sp.Mechanic
	row.Dispel = sp.Dispel
	row.Class = sp.Class
	return row
}

// spellRowKey groups rows by name; a nameless id groups alone so two unknown spells never hide each other.
func spellRowKey(r ServeSpellRow) string {
	if r.Name == "" {
		return "#" + strconv.FormatUint(uint64(r.SpellID), 10)
	}
	return strings.ToLower(r.Name)
}
