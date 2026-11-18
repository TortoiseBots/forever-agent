// Package talents holds the premade spec names hired bots are asked for and the marker talent
// that proves a build: a talent the spec's level-35 premade link already has and no other
// premade of the class has at level 60.
package talents

import (
	"regexp"
	"strings"
)

// Marker: tab is the tree's page, index its place in that tree's sorted talent list, ranks its
// rank spells in order, and Premade35 that tree's string in the level-35 premade.
type Marker struct {
	Name      string
	Tab       int
	Index     int
	Ranks     []int64
	Premade35 string
}

// PremadeName maps a bench spec id to the TB premade spec name (PremadeSpecName.<class>.<n>).
var PremadeName = map[string]string{
	"bm":      "beastmastery",
	"mm":      "marksmanship",
	"assa":    "assassination",
	"ele":     "elemental",
	"enh":     "enhancement",
	"boomkin": "balance",
	"cat":     "feral",
	"bear":    "feral",
}

// Markers: (class, premade name) -> Marker.
var Markers = map[[2]string]Marker{
	{"warrior", "fury"}:        {"Flurry", 1, 14, []int64{12319, 12971, 12972, 12973, 12974}, "055541200001201"},
	{"warrior", "protection"}:  {"Gag Order", 2, 9, []int64{12311, 12958}, "253051023203"},
	{"warrior", "arms"}:        {"Two-Handed Weapon Specialization", 0, 9, []int64{12163, 12711, 12712}, "0522531233"},
	{"paladin", "holy"}:        {"Holy Shock", 0, 13, []int64{20473}, "05320302035201"},
	{"paladin", "protection"}:  {"Righteous Defense", 1, 11, []int64{51328, 51329, 51330}, "053123133203"},
	{"paladin", "retribution"}: {"Vengeance", 2, 12, []int64{20049, 20056, 20057, 20058, 20059}, "05203051230041"},
	{"hunter", "beastmastery"}: {"Bestial Discipline", 0, 9, []int64{19590, 19592}, "550032015221"},
	{"hunter", "marksmanship"}: {"Endless Quiver", 1, 7, []int64{51517, 51518}, "055521323"},
	{"hunter", "survival"}:     {"Stinging Nettle", 2, 11, []int64{51579, 51580}, "350323025111"},
	{"rogue", "combat"}:        {"Blade Rush", 1, 15, []int64{52507, 52508}, "5003502000501221"},
	{"rogue", "assassination"}: {"Vile Poisons", 0, 10, []int64{16513, 16514, 16515}, "005323105232"},
	{"rogue", "subtlety"}:      {"Cloaked in Shadows", 2, 10, []int64{52707, 52709}, "503303331122"},
	{"priest", "holy"}:         {"Book of Prayer", 1, 12, []int64{52943, 52944}, "3205020302351"},
	{"priest", "shadow"}:       {"Darkness", 2, 15, []int64{15259, 15307, 15308, 15309, 15310}, "0522500010250121"},
	{"priest", "discipline"}:   {"Improved Inner Fire", 0, 7, []int64{14747, 14770}, "23055022133"},
	{"shaman", "restoration"}:  {"Restorative Totems", 2, 9, []int64{16187, 16205, 16206, 16207, 16208}, "55030321052"},
	{"shaman", "enhancement"}:  {"Enhancing Totems", 1, 11, []int64{16259, 16295}, "5005303105022"},
	{"shaman", "elemental"}:    {"Reverberation", 0, 6, []int64{16040, 16113, 16114}, "55133135"},
	{"mage", "arcane"}:         {"Arcane Instability", 0, 13, []int64{15058, 15059, 15060}, "2050050310230311"},
	{"mage", "fire"}:           {"Fire Vulnerability", 1, 9, []int64{11095, 12872, 12873}, "50523201030302"},
	{"mage", "frost"}:          {"Arctic Reach", 2, 10, []int64{16757, 16758}, "0533020510232"},
	{"warlock", "affliction"}:  {"Shadow Mastery", 0, 16, []int64{18271, 18272, 18273, 18274, 18275}, "05032032022321001"},
	{"warlock", "demonology"}:  {"Power Overwhelming", 1, 13, []int64{51714}, "05003315002031"},
	{"warlock", "destruction"}: {"Improved Immolate", 2, 12, []int64{17815, 17833, 17834, 17835, 17836}, "55050015020021"},
	{"druid", "balance"}:       {"Owlkin Frenzy", 0, 13, []int64{45734, 51426, 51427}, "500002303125311"},
	{"druid", "feral"}:         {"Predatory Strikes", 1, 10, []int64{16972, 16974, 16975}, "55303021322"},
	{"druid", "restoration"}:   {"Gift of Nature", 2, 8, []int64{17104, 24943, 24944, 24945, 24946}, "5050013354"},
}

// SpecShareMin: the spec's own tree holds at least this share of all spent points.
const SpecShareMin = 0.6

var (
	linkReply = regexp.MustCompile(`(?is)talent spec is:.*?Link:\s*(\S*)`)
	// a rank is 0-5; up to three trees joined by `-`
	linkShape = regexp.MustCompile(`^[0-5]*(?:-[0-5]*){0,2}$`)
)

// PremadeSpec is the premade spec name the bench asks for (an unknown spec is itself, lower case).
func PremadeSpec(spec string) string {
	key := strings.ToLower(strings.TrimSpace(spec))
	if name, ok := PremadeName[key]; ok {
		return name
	}
	return key
}

// MarkerFor returns the Marker for a class and bench spec, or None (no marker).
func MarkerFor(class, spec string) (Marker, bool) {
	m, ok := Markers[[2]string{strings.ToLower(strings.TrimSpace(class)), PremadeSpec(spec)}]
	return m, ok
}

// LinkText returns the raw string after `Link:` in a talents reply, or ok=false when there is none.
func LinkText(text string) (string, bool) {
	m := linkReply.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// ParseLink returns the three tree strings of a talents reply; ok=false when it carries no
// well-formed link (no `Link:`, an empty one, a rank above 5, more than three trees).
func ParseLink(text string) ([]string, bool) {
	raw, ok := LinkText(text)
	if !ok || raw == "" || !linkShape.MatchString(raw) {
		return nil, false
	}
	parts := strings.Split(raw, "-")
	out := make([]string, 3)
	for i := range out {
		if i < len(parts) {
			out[i] = parts[i]
		}
	}
	return out, true
}

// MarkerRank is the marker talent's rank in the parsed link (0 when its place is past the end).
func MarkerRank(trees []string, marker Marker) int {
	tree := trees[marker.Tab]
	if marker.Index >= len(tree) {
		return 0
	}
	return int(tree[marker.Index] - '0')
}

// TreePoints is the points spent per tree of a parsed link.
func TreePoints(trees []string) []int {
	out := make([]int, 0, len(trees))
	for _, tree := range trees {
		total := 0
		for _, ch := range tree {
			total += int(ch - '0')
		}
		out = append(out, total)
	}
	return out
}

// SpecShare returns (points in the spec's own tree, all points, share 0-1).
func SpecShare(trees []string, marker Marker) (int, int, float64) {
	points := TreePoints(trees)
	total := 0
	for _, p := range points {
		total += p
	}
	share := 0.0
	if total != 0 {
		share = float64(points[marker.Tab]) / float64(total)
	}
	return points[marker.Tab], total, share
}
