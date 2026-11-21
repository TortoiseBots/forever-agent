package bughunt

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/trace"
)

// Frame is one sample of the world: the party keyed by the run's own identity, and the hostiles it saw. GC is the
// group's combat flag and GCSince the frame's time when that run of group combat started (derived after loading,
// and reset by a MAX_GAP_S gap).
type Frame struct {
	T          float64
	Units      map[string]*Unit
	Order      []string // the units' order, so a rule's output does not depend on map iteration
	Mobs       []*Mob
	GC         bool
	GCSince    float64
	HasGCSince bool
}

// Unit is one party member in a frame.
type Unit struct {
	G         string
	Alive     bool
	Combat    bool
	State     string // engine state: c combat, n non-combat, d dead, r resting
	HP        float64
	MaxHP     float64
	Power     float64
	MaxPower  float64
	PType     int // 0 mana, 1 rage, 2 focus, 3 energy
	X, Y, Z   float64
	Moving    bool
	HasMoving bool // false when the source did not say; finalise derives it from the positions, as Python does
	Target    string
	Casting   bool
	Spell     uint32
	Act       string
	Res       string
	Lead      bool
	Auras     []uint32

	// The module's own report, when the frame comes from `.tb state` on a watch. HasMeal says whether this row
	// carried the meal report at all: false is unknown, which is not "did not eat or drink".
	Eat, Drink, Rest bool
	HasMeal          bool
	Life             string   // alive, dead, awaiting-res, ghost, running-back
	DL               *float64 // yards to the party leader; nil when unknown (self, another map)
	Move             string   // follow, stay; empty when the module names no order
	Stuck            string   // blocked:<reason>, away:<seconds left>, noengine
}

// TopEntry is one entry of a mob's threat list: a guid and its absolute threat, highest first.
type TopEntry struct {
	G      string `json:"g"`
	Threat int    `json:"threat"`
}

// Mob is one hostile in a frame. HPPct is health in percent (the loaders all normalise to it), Entry the creature
// entry when the run knows it, Victim the threat victim a watch's `.tb threat` names, Target the model's target.
type Mob struct {
	G       string
	Entry   uint32
	Name    string
	Boss    bool
	HPPct   float64
	X, Y, Z float64
	Combat  bool
	Target  string
	Victim  string
	CC      bool
	Flee    bool
	NP      bool // the position is not known
	Top     []TopEntry
}

// Event is one normalised trials event: the row `events.jsonl` carries.
type Event struct {
	T       float64
	Kind    string
	Src     string
	Dst     string
	Spell   string
	Amount  *float64
	SpellID int
	SrcRole string
	DstRole string
}

// Death is one death the run records: its time on the run's clock, the member,
// and the parsed `.deathrecap` timeline the watch attached once when the death
// arrived (empty when it never asked: a dry run, a record from before this
// round, a death of no party member, or a trials repeat).
type Death struct {
	T     float64
	Bot   string
	Recap []string
}

// CombatActor is one side of a combat-log row.
type CombatActor struct {
	GUID string `json:"guid"`
	Name string `json:"name"`
}

// CombatRow is one row of a watch's `<run_id>.combatlog.jsonl`, the shape the world session writes and
// events.Normalise reads.
type CombatRow struct {
	Seq     int         `json:"seq"`
	Kind    string      `json:"kind"`
	Time    string      `json:"time"`
	MonoMS  int64       `json:"mono_ms"`
	Source  CombatActor `json:"source"`
	Target  CombatActor `json:"target"`
	SpellID int         `json:"spell_id"`
	Amount  int         `json:"amount"`
	Reason  string      `json:"reason,omitempty"`
	// The core-gains round's row fields: an interrupt's stopped cast, an absorb's incoming spell, a dispel's list,
	// and the advanced log's unit block.
	InterruptedSpellID int             `json:"interrupted_spell_id,omitempty"`
	AbsorbedSpellID    int             `json:"absorbed_spell_id,omitempty"`
	Dispelled          []DispelledAura `json:"dispelled,omitempty"`
	Sample             *CombatSample   `json:"sample,omitempty"`
	// RemainingMS is an aura_applied row's remaining duration: when the aura would expire by itself. Rule 18 ends a
	// debuff episode there when no removal row came first; 0 means the row did not say.
	RemainingMS int64 `json:"remaining_ms,omitempty"`

	// The knockback round's fields: the takeoff (knockback) or landing (knockback_land) position, the flight
	// speeds and direction the mover's own packet carried, and the landing's air time and drop.
	Pos       *CombatPoint `json:"pos,omitempty"`
	Self      bool         `json:"self,omitempty"`
	HorzSpeed float64      `json:"horz_speed,omitempty"`
	VertSpeed float64      `json:"vert_speed,omitempty"`
	DirX      float64      `json:"dir_x,omitempty"`
	DirY      float64      `json:"dir_y,omitempty"`
	AirMS     int64        `json:"air_ms,omitempty"`
	Drop      float64      `json:"drop,omitempty"`

	// T is the row on the run's clock (seconds from t=0), computed from Time and the run's start, and TKnown
	// says whether it could be: a row with no readable time is unknown at every rule, never at t=0.
	T      float64 `json:"-"`
	TKnown bool    `json:"-"`
}

// CombatPoint is a knockback row's takeoff or landing position.
type CombatPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// DispelledAura is one aura a dispel row removed.
type DispelledAura struct {
	SpellID int  `json:"spell_id"`
	Harmful bool `json:"harmful,omitempty"`
}

// CombatSample is an advanced-log row's unit block: health and the unit's power bar.
type CombatSample struct {
	Health    int64 `json:"health"`
	Power     int32 `json:"power"`
	PowerType int8  `json:"power_type"`
}

// Run is one loaded input: a watch recording, a trials repeat or a roster-only run.
type Run struct {
	Kind    string // watch, trials
	Name    string
	DS      string // the dataset label the report groups runs by
	Path    string
	T0      float64 // epoch of t=0, for the clock; 0 when unknown
	Frames  []*Frame
	Events  []Event
	Deaths  []Death
	Combat  []CombatRow
	Roles   map[string]string
	Classes map[string]string
	// Names is the party record's name per guid: the character's for a dungeon run, the party slot's word for a
	// watch (whose records carry no character names).
	Names map[string]string
	// PartyOrder is the party records' guids in the order the record lists them: the party index, leader first.
	PartyOrder []string
	// Subjects are the members the rules apply to; nil means every unit in a frame.
	Subjects map[string]bool
	// CastTimes is every cast, cast start, damage and heal event per source, sorted: rule 1's "no cast/swing" window.
	CastTimes map[string][]float64
	// BossEntries are the entries a watch's instance report listed, which rule 3 does not count as adds.
	BossEntries map[uint32]string
	// ByGUID maps a canonical guid to the run's own identity for that unit (a watch keys members by guid, a trials
	// repeat by character name), and GUIDOf is the reverse. A combat row's guid becomes the run's identity through
	// them, so the rules join a row to a frame whatever the run keys its members by.
	ByGUID map[string]string
	GUIDOf map[string]string
	// SpellNames are the spell names a live-checked data file gives (id -> name), and Dispel the dispel types the same
	// source gives (id -> magic, curse, disease, poison, or never). A row's id without a name is `spell <id>` and
	// never matches a name rule; an id without a dispel type is not a known debuff. (Names is the party record's
	// name per guid, above; the spell map is SpellNames so the two never share a field.)
	SpellNames map[int]string
	Dispel     map[int]string

	// combatBySrc indexes Combat by a row's canonical source guid, built on first use.
	combatBySrc   map[string][]int
	combatIndexed bool

	// coverage is what the combat log provably covers (cover.go), built once per log; unknown counts, per rule,
	// the windows a rule left unjudged because the log did not cover them (Hunt reads and resets it).
	coverage   *logCover
	coverFor   int
	coverBuilt bool
	unknown    map[int]map[string]int

	// TraceEvents are the trace's own event records (run_start, wipe, pull_start, ...), for adapters that place a
	// record's events on the trace's clock instead of a summary's module clock.
	TraceEvents []trace.Event
	// recapSum stages the watch summary's death log until the deaths are
	// dated (recapFrom runs after deathsFromFrames): the parsed `.deathrecap`
	// timeline per death, attached by member.
	recapSum *foreverSummary
}

// Role is the member's role, "?" when the run does not know it.
func (r *Run) Role(n string) string {
	if v, ok := r.Roles[n]; ok && v != "" {
		return v
	}
	return "?"
}

// Class is the member's class, "?" when the run does not know it.
func (r *Run) Class(n string) string {
	if v, ok := r.Classes[n]; ok && v != "" {
		return v
	}
	return "?"
}

// SpellName is the spell's name from the live-checked names file, or `spell <id>` when the file does not name it.
// An unnamed id never matches a name rule (a buff, an AoE or a heal): the name is what the rule reads, and a name
// this run does not have is unknown, not a guess.
func (r *Run) SpellName(id int) string {
	if v, ok := r.SpellNames[id]; ok && v != "" {
		return v
	}
	return fmt.Sprintf("spell %d", id)
}

// DispelType is the dispel type the dispel file gives for a spell id, empty when it lists none. It is unknown, not
// undispellable: rule 15 only judges ids the file lists.
func (r *Run) DispelType(id int) string {
	return r.Dispel[id]
}

// combatIndex builds the source index over Combat, in the rows' own order. A rule looks up the rows of one member
// instead of scanning the log per frame.
func (r *Run) combatIndex() {
	if r.combatIndexed {
		return
	}
	r.combatIndexed = true
	r.combatBySrc = map[string][]int{}
	for i := range r.Combat {
		row := &r.Combat[i]
		if g := CanonGUID(row.Source.GUID); g != "" {
			r.combatBySrc[g] = append(r.combatBySrc[g], i)
		}
	}
}

// Member is a combat row's guid as the run's own identity: the party member it belongs to, else the guid text.
func (r *Run) Member(guid string) string {
	g := CanonGUID(guid)
	if g == "" {
		return ""
	}
	if n, ok := r.ByGUID[g]; ok {
		return n
	}
	return g
}

// guidOf is a member's guid text: its own when the run keys members by guid.
func (r *Run) guidOf(n string) string {
	if g := r.GUIDOf[n]; g != "" {
		return g
	}
	return n
}

// rowsFromMember are the indexes of the rows a member is the source of, whatever the run keys it by.
func (r *Run) rowsFromMember(n string) []int { return r.rowsFrom(r.guidOf(n)) }

// rowsFrom are the indexes of the rows a member is the source of.
func (r *Run) rowsFrom(guid string) []int {
	r.combatIndex()
	return r.combatBySrc[CanonGUID(guid)]
}

// IsSubject says whether the rules apply to n: every unit when the run has no subject set.
func (r *Run) IsSubject(n string) bool {
	if r.Subjects == nil {
		return true
	}
	return r.Subjects[n]
}

// ReportsMeals says whether the run's frames carry the module's eat/drink report. Rule 13 reads "no drink" as a
// fact from it, so a run that does not report meals has that check skipped, never answered with a hit.
func (r *Run) ReportsMeals() bool {
	for _, f := range r.Frames {
		for _, n := range f.Order {
			if f.Units[n].HasMeal {
				return true
			}
		}
	}
	return false
}

// Clock is the hit's clock text: the wall time in UTC when the run knows its start, else the run-relative time.
func (r *Run) Clock(t float64) string {
	e := r.T0 + t
	if e > 1e9 {
		sec := math.Floor(e)
		nsec := (e - sec) * 1e9
		return time.Unix(int64(sec), int64(math.Round(nsec))).UTC().Format("15:04:05Z")
	}
	return fmt.Sprintf("t=%.0fs", t)
}

// finalise derives what every loader's rules need: the movement flag where the source did not carry one, and each
// frame's group-combat start (a gap of MAX_GAP_S or a frame out of combat ends it).
func (r *Run) finalise() {
	if r.ByGUID == nil {
		r.ByGUID = map[string]string{}
	}
	if r.GUIDOf == nil {
		r.GUIDOf = map[string]string{}
	}
	for _, f := range r.Frames {
		for _, n := range f.Order {
			if g := CanonGUID(f.Units[n].G); g != "" {
				r.ByGUID[g] = n
				r.GUIDOf[n] = g
			}
		}
	}
	type prev struct{ t, x, y float64 }
	last := map[string]prev{}
	for _, f := range r.Frames {
		for _, n := range f.Order {
			u := f.Units[n]
			if !u.HasMoving {
				p, ok := last[n]
				u.Moving = ok && math.Hypot(u.X-p.x, u.Y-p.y) > 0.3*math.Max(f.T-p.t, 0.5)
				u.HasMoving = true
			}
			last[n] = prev{f.T, u.X, u.Y}
		}
	}
	var lastT, gcSince float64
	var haveLast, haveSince bool
	for _, f := range r.Frames {
		if f.GC {
			if !haveSince || (haveLast && f.T-lastT > MAX_GAP_S) {
				gcSince, haveSince = f.T, true
			}
		} else {
			haveSince = false
		}
		f.GCSince, f.HasGCSince = gcSince, haveSince
		lastT, haveLast = f.T, true
	}
}

// framesBetween are the frames whose time is in [t0, t1], in run order.
func (r *Run) framesBetween(t0, t1 float64) []*Frame {
	i := sort.Search(len(r.Frames), func(i int) bool { return r.Frames[i].T >= t0 })
	var out []*Frame
	for ; i < len(r.Frames) && r.Frames[i].T <= t1; i++ {
		out = append(out, r.Frames[i])
	}
	return out
}

// addUnit appends a unit to the frame, keeping its first position when the same identity repeats.
func (f *Frame) addUnit(n string, u *Unit) {
	if _, ok := f.Units[n]; !ok {
		f.Order = append(f.Order, n)
	}
	f.Units[n] = u
}

// dist2 is the horizontal distance between two points, as the Python checker's.
func dist2(ax, ay, bx, by float64) float64 { return math.Hypot(ax-bx, ay-by) }

// dist3 is the 3-D distance between two units, as the Python checker's.
func dist3(a, b *Unit) float64 {
	return math.Sqrt((a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y) + (a.Z-b.Z)*(a.Z-b.Z))
}

// pctOf is a unit's power in percent of its maximum.
func pctOf(u *Unit) float64 { return 100 * u.Power / math.Max(u.MaxPower, 1) }

// hpPctOf is a unit's health in percent of its maximum.
func hpPctOf(u *Unit) float64 { return 100 * u.HP / math.Max(u.MaxHP, 1) }

// entryOf is the Python checker's entry_of: the creature entry a hostile guid carries (the 6 hex digits after its
// f1 marker), 0 when the guid is not one.
func entryOf(guid string) uint32 {
	g := strings.ReplaceAll(strings.ToLower(guid), "0x", "")
	if len(g) >= 16 && strings.HasPrefix(g, "f1") {
		if v, err := strconv.ParseUint(g[4:10], 16, 32); err == nil {
			return uint32(v)
		}
	}
	return 0
}

// pyRound is Python's round(x, n): the nearest value at that many decimal places, ties to even, read off the
// correctly rounded decimal text.
func pyRound(x float64, n int) float64 {
	s := strconv.FormatFloat(x, 'f', n, 64)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return x
	}
	return v
}

// pyBool is Python's str(True)/str(False), which the old checker's evidence text used.
func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// truthy is Python truthiness for the JSON values a row carries.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return false
}

// rowStr is a row's text field; a number keeps its decimal text (a name field is always a string).
func rowStr(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// rowNumOK is a row's number; a missing or non-numeric field is 0.
func rowNumOK(row map[string]any, key string) float64 {
	switch v := row[key].(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
	case string:
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return 0
}

// readJSONL reads the JSON objects a JSONL file holds, skipping blank lines, lines that are not an object, and lines
// whose JSON is invalid: the Python checker's jl does the same, and a cut-short last line is normal in a recording.
// A file that cannot be read is no rows. It is for optional inputs; a required file is read with readTrialsJSONL,
// which fails on the file and the line.
func readJSONL(path string) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil || row == nil {
			continue
		}
		out = append(out, row)
	}
	return out
}

// parseISO is Python's datetime.fromisoformat(...).timestamp() for the wall clocks a repeat carries; 0 when the
// text is not a time.
func parseISO(s string) float64 {
	if s == "" {
		return 0
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999Z0700",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		var (
			dt  time.Time
			err error
		)
		if strings.Contains(layout, "Z07") {
			dt, err = time.Parse(layout, s)
		} else {
			dt, err = time.ParseInLocation(layout, s, time.Local)
		}
		if err == nil {
			return float64(dt.Unix()) + float64(dt.Nanosecond())/1e9
		}
	}
	return 0
}

// CanonGUID is a guid text in the trace's one form, `0x` and 32 upper-case hex digits: a shorter hex guid is padded.
// Text that is not hex is returned unchanged (an identity from another source keeps its text).
func CanonGUID(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	body := t
	if len(body) >= 2 && (body[0] == '0') && (body[1] == 'x' || body[1] == 'X') {
		body = body[2:]
	}
	if body == "" || len(body) > 32 {
		return s
	}
	for _, c := range body {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return s
		}
	}
	return "0x" + strings.Repeat("0", 32-len(body)) + strings.ToUpper(body)
}
