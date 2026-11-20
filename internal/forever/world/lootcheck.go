package world

// forever loot-check and forever object-use: this round's two commands and their reports.
//
// loot-check settles, finds the nearest lootable thing (a dead creature of an entry, or a chest), kills it first when
// it is still alive, opens its window, votes on a group roll when --roll asked for one, takes the money and the
// items it may take, and releases the window. object-use settles, finds the nearest game object of an entry, uses
// it, waits for the object's own state update and reports the state before and after.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/objects"
)

// LootReport is what loot-check observed: the target, the window's contents, what was taken and what the rolls did.
type LootReport struct {
	Kind     string
	Type     string
	Entry    int32
	Name     string
	Distance float32

	Opened     bool
	Failure    uint8
	Method     uint8
	Threshold  uint8
	Coins      uint32
	Items      []LootItemRow
	Currencies int

	MoneyTaken uint64
	MoneyMod   uint64
	Removed    []uint8
	Released   bool
	// ReleaseAttempted and ReleaseErr are the cleanup release a run that ends before its own release still owes
	// the server: it is attempted on its own short context, and its failure is recorded here instead of replacing
	// the run's own error.
	ReleaseAttempted bool
	ReleaseErr       string

	Rolls []LootRollRow
	Votes int

	StateBefore  int8
	StateAfter   int8
	FlagsBefore  uint32
	FlagsAfter   uint32
	StateKnown   bool
	StateChanged bool

	// Ambiguous is set when more than one game object of the target's entry stood in range and --at named none:
	// Candidates lists them, nothing was opened, and the run ends with ErrAmbiguousObject.
	Ambiguous  bool
	Candidates []ObjectUseCandidate
}

// LootItemRow is one item of the open window.
type LootItemRow struct {
	LootListID uint8
	ItemID     int32
	Count      uint32
	Slot       uint8
	Roll       bool
	Trade      bool
}

// LootRollRow is one started roll and how it ended. OurVote is 0xFF when this session sent none.
type LootRollRow struct {
	LootListID uint8
	ItemID     int32
	Count      uint32
	ValidRolls uint8
	TimeLimit  uint32
	OurVote    uint8
	Vote       uint8
	VoteSeen   int
	Winner     string
	Number     int32
	AllPassed  bool
	Autopassed int
	OffSpec    int
	Ended      bool
}

// ObjectUseCandidate is one in-range game object of the wanted entry: where it stands and how far it is. The list
// names no identifier, only the position and the distance.
type ObjectUseCandidate struct {
	X, Y, Z  float32
	Distance float32
}

// ObjectUseReport is what object-use observed: the object, its state and flags before and after the use, and
// whether the use opened a loot window.
type ObjectUseReport struct {
	Found        bool
	Entry        int32
	Name         string
	Distance     float32
	StateBefore  int8
	StateAfter   int8
	FlagsBefore  uint32
	FlagsAfter   uint32
	StateKnown   bool
	StateChanged bool
	LootOpened   bool
	LootItems    int
	Released     bool
	// Ambiguous is set when more than one object of the entry was in range and none was chosen: Candidates lists
	// them, so the caller can pick one with --at.
	Ambiguous  bool
	Candidates []ObjectUseCandidate
}

// lootCandidates lists what a loot run may target, nearest first: dead units, chests, and (only with an entry) the
// living creature of that entry, which the run kills first. Distance is measured from (x,y,z).
type lootCandidate struct {
	obj  *objects.Object
	dist float32
	kind string // "loot", "open" or "kill"
}

func (s *session) lootCandidates(x, y, z, reach float32, entry int32) []lootCandidate {
	var out []lootCandidate
	add := func(o *objects.Object, kind string) {
		tx, ty, tz := o.X, o.Y, o.Z
		d := objects.Distance(x, y, z, tx, ty, tz)
		if d > reach {
			return
		}
		out = append(out, lootCandidate{obj: o, dist: d, kind: kind})
	}
	for _, o := range s.world.LootableUnits(x, y, z) {
		if entry != 0 && o.Info.Entry != entry {
			continue
		}
		add(o, "loot")
	}
	for _, o := range s.world.GameObjects(x, y, z) {
		if entry != 0 {
			if o.Info.Entry != entry {
				continue
			}
		} else if o.Info.GOType != goTypeChest {
			// Without an entry the run looks for what gives loot: a chest (GAMEOBJECT_TYPE_CHEST, SharedDefines.h).
			continue
		}
		add(o, "open")
	}
	if entry != 0 {
		for _, o := range s.world.Units(x, y, z) {
			if o.Info.Entry != entry || o.Dead || (o.Info.Health <= 0 && o.Info.MaxHealth > 0) {
				continue
			}
			if o.GUID == s.self {
				continue
			}
			add(o, "kill")
		}
	}
	sortCandidates(out)
	return out
}

// goTypeChest is GAMEOBJECT_TYPE_CHEST (SharedDefines.h GameObjectTypes).
const goTypeChest int8 = 3

// objectAtRange is how close a game object must stand to --at to be the one, in yards: the flag's default.
const objectAtRange float32 = 2

// ErrAmbiguousObject is a run that refuses to guess between several game objects of one entry: no use is sent, the
// candidates are reported by position and distance only, and the caller picks one with --at.
var ErrAmbiguousObject = errors.New("world: several game objects of the entry stand in range")

// objectCandidates names the objects of an ambiguity by where they stand and how far they are, with no identifier.
func objectCandidates(matches []*objects.Object, dists []float32) []ObjectUseCandidate {
	out := make([]ObjectUseCandidate, 0, len(matches))
	for i, o := range matches {
		out = append(out, ObjectUseCandidate{X: o.X, Y: o.Y, Z: o.Z, Distance: dists[i]})
	}
	return out
}

// chooseObject picks the one object among matches: --at names the nearest to that point within the tolerance, one
// match is the one, and several matches without --at are ErrAmbiguousObject, because a use sent to the wrong object
// opens someone else's door or loots someone else's chest.
func (s *session) chooseObject(matches []*objects.Object, dists []float32, entry int32) (int, error) {
	if len(matches) == 1 {
		return 0, nil
	}
	if len(matches) == 0 {
		return -1, fmt.Errorf("world: the game object of entry %d is gone", entry)
	}
	if !s.opts.AtSet {
		return -1, ErrAmbiguousObject
	}
	tol := s.opts.AtRange
	if tol <= 0 {
		tol = objectAtRange
	}
	idx := -1
	var near float32
	for i, o := range matches {
		d := objects.Distance(s.opts.AtX, s.opts.AtY, s.opts.AtZ, o.X, o.Y, o.Z)
		if d > tol {
			continue
		}
		if idx < 0 || d < near {
			idx, near = i, d
		}
	}
	if idx < 0 {
		return -1, fmt.Errorf("world: no game object with entry %d within %.1f yd of --at (%.2f, %.2f, %.2f)",
			entry, tol, s.opts.AtX, s.opts.AtY, s.opts.AtZ)
	}
	return idx, nil
}

// objectsOfEntry lists the game objects of one entry within reach of (x, y, z), nearest first, with their
// distances. The world's list is already nearest first, so the first object past reach ends the walk.
func (s *session) objectsOfEntry(x, y, z, reach float32, entry int32) ([]*objects.Object, []float32) {
	var matches []*objects.Object
	var dists []float32
	for _, o := range s.world.GameObjects(x, y, z) {
		if o.Info.Entry != entry {
			continue
		}
		d := objects.Distance(x, y, z, o.X, o.Y, o.Z)
		if d > reach {
			break
		}
		matches = append(matches, o)
		dists = append(dists, d)
	}
	return matches, dists
}

func sortCandidates(c []lootCandidate) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].dist < c[j-1].dist; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

// lootCheck is forever loot-check.
func (s *session) lootCheck(ctx context.Context) error {
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 8 * time.Second
	}
	if err := s.settle(ctx, settle); err != nil {
		return err
	}
	if s.mover == nil {
		return ErrNotReady
	}
	if s.selfDead() {
		return ErrSelfDead
	}
	s.sendPing()
	s.queryNames(ctx)
	if err := s.linkDead(); err != nil {
		return err
	}
	s.summarise(s.MoverX(), s.MoverY(), s.MoverZ())

	vote, hasVote, err := loot.VoteFromWord(s.opts.Roll)
	if err != nil {
		return err
	}
	reach := s.opts.LootRange
	if reach <= 0 {
		reach = 30
	}
	x, y, z := s.MoverX(), s.MoverY(), s.MoverZ()
	candidates := s.lootCandidates(x, y, z, reach, s.opts.Entry)
	if len(candidates) == 0 {
		s.opts.logf("loot-check: nothing to loot within %.0f yd%s", reach, entryNote(s.opts.Entry))
		return nil
	}
	target := candidates[0]
	s.opts.logf("loot-check: nearest target is a %s, entry %d, at %.1f yd (%s)", objects.TypeName(target.obj.Type),
		target.obj.Info.Entry, target.dist, target.kind)
	if name := s.world.Name(target.obj); name != "" {
		s.opts.logf("loot-check: it is %s", name)
	}

	// A game object is only opened when the choice is unambiguous: several objects of its entry in range are a
	// guess at whose chest this is, the same refusal object-use makes, unless --at names one.
	if target.obj.Info.HasGameObject || target.obj.Type == objects.TypeGameObject {
		matches, dists := s.objectsOfEntry(x, y, z, reach, target.obj.Info.Entry)
		idx, cerr := s.chooseObject(matches, dists, target.obj.Info.Entry)
		if cerr != nil {
			if !errors.Is(cerr, ErrAmbiguousObject) {
				return cerr
			}
			r := &s.report.Loot
			r.Ambiguous = true
			r.Candidates = objectCandidates(matches, dists)
			s.opts.logf("loot-check: %d game objects with entry %d within %.0f yd; refusing to guess which one",
				len(matches), target.obj.Info.Entry, reach)
			for _, c := range r.Candidates {
				s.opts.logf("loot-check:   at %.2f, %.2f, %.2f (%.1f yd)", c.X, c.Y, c.Z, c.Distance)
			}
			s.opts.logf("loot-check: give --at x,y,z to choose one")
			return ErrAmbiguousObject
		}
		if len(matches) > 1 {
			s.opts.logf("loot-check: %d game objects with entry %d within %.0f yd; --at (%.2f, %.2f, %.2f) picks the one at %.2f, %.2f, %.2f",
				len(matches), target.obj.Info.Entry, reach, s.opts.AtX, s.opts.AtY, s.opts.AtZ,
				matches[idx].X, matches[idx].Y, matches[idx].Z)
		}
		target = lootCandidate{obj: matches[idx], dist: dists[idx], kind: target.kind}
	}

	if target.kind == "kill" {
		s.opts.logf("loot-check: it is alive; killing it first")
		if err := s.Attack(ctx, target.obj.GUID); err != nil {
			if errors.Is(err, ErrSelfDead) {
				return err
			}
			s.opts.logf("loot-check: the kill did not finish: %v", err)
			// Only a death makes it lootable; without one the run ends with the fight's reason.
			return err
		}
	}

	release, err := s.lootTarget(ctx, target.obj)
	defer release()
	if err != nil {
		if errors.Is(err, ErrLootRefused) || errors.Is(err, ErrNoLootWindow) {
			s.opts.logf("loot-check: %v", err)
			return nil
		}
		return err
	}
	r := &s.report.Loot
	if !r.Opened {
		s.opts.logf("loot-check: no window opened; the object's state %s", stateTransitionText(r))
		return nil
	}
	s.opts.logf("loot-check: window: %s, threshold %d, %d copper, %d item(s), %d currency row(s)",
		loot.MethodName(r.Method), r.Threshold, r.Coins, len(r.Items), r.Currencies)
	for _, it := range r.Items {
		s.opts.logf("loot-check:   slot %d: item %d x%d (%s)%s", it.LootListID, it.ItemID, it.Count,
			loot.SlotName(it.Slot), rollNote(it))
	}
	if err := s.voteRolls(ctx, s.loot.window, hasVote, vote); err != nil {
		return err
	}
	if err := s.takeLoot(ctx, s.loot.window, s.opts.TakeItems, s.opts.LootMoney); err != nil {
		return err
	}
	if r.MoneyTaken != 0 || r.MoneyMod != 0 {
		s.opts.logf("loot-check: took %d copper (modifier %d)", r.MoneyTaken, r.MoneyMod)
	}
	if n := len(r.Removed); n > 0 {
		s.opts.logf("loot-check: the server removed %d item slot(s): %v", n, r.Removed)
	}
	s.opts.logf("loot-check: the window is released")
	return nil
}

func entryNote(entry int32) string {
	if entry == 0 {
		return ""
	}
	return fmt.Sprintf(" with entry %d", entry)
}

func rollNote(it LootItemRow) string {
	if it.Roll {
		return ", needs a roll"
	}
	return ""
}

func stateTransitionText(r *LootReport) string {
	if !r.StateKnown {
		return "is not known (no values update carried it)"
	}
	if r.StateChanged {
		return fmt.Sprintf("changed from %s to %s", goStateName(r.StateBefore), goStateName(r.StateAfter))
	}
	return fmt.Sprintf("stayed %s", goStateName(r.StateBefore))
}

// goStateName names a GOState (SharedDefines.h:3278-3285).
func goStateName(state int8) string {
	switch state {
	case 0:
		return "active (open)"
	case 1:
		return "ready (closed)"
	case 2:
		return "destroyed"
	case 24:
		return "transport active"
	case 25:
		return "transport stopped"
	}
	return fmt.Sprintf("state %d", state)
}

// objectUse is forever object-use.
func (s *session) objectUse(ctx context.Context) error {
	settle := s.opts.Settle
	if settle <= 0 {
		settle = 8 * time.Second
	}
	if err := s.settle(ctx, settle); err != nil {
		return err
	}
	if s.mover == nil {
		return ErrNotReady
	}
	if s.selfDead() {
		return ErrSelfDead
	}
	s.sendPing()
	s.queryNames(ctx)
	if err := s.linkDead(); err != nil {
		return err
	}
	s.summarise(s.MoverX(), s.MoverY(), s.MoverZ())

	reach := s.opts.LootRange
	if reach <= 0 {
		reach = 40
	}
	x, y, z := s.MoverX(), s.MoverY(), s.MoverZ()
	matches, dists := s.objectsOfEntry(x, y, z, reach, s.opts.Entry)
	rep := &s.report.ObjectUse
	rep.Entry = s.opts.Entry
	if len(matches) == 0 {
		s.opts.logf("object-use: no game object with entry %d within %.0f yd", s.opts.Entry, reach)
		return nil
	}
	idx, err := s.chooseObject(matches, dists, s.opts.Entry)
	if err != nil {
		if !errors.Is(err, ErrAmbiguousObject) {
			return err
		}
		// Several objects of the entry stand in range: the nearest is a guess, and a use sent to the wrong one
		// opens someone else's door (or loots someone else's chest). Refuse, name each by where it stands, and end
		// non-zero, so a script can tell a refusal from a use.
		rep.Ambiguous = true
		rep.Candidates = objectCandidates(matches, dists)
		s.opts.logf("object-use: %d game objects with entry %d within %.0f yd; refusing to guess which one",
			len(matches), s.opts.Entry, reach)
		for _, c := range rep.Candidates {
			s.opts.logf("object-use:   at %.2f, %.2f, %.2f (%.1f yd)", c.X, c.Y, c.Z, c.Distance)
		}
		s.opts.logf("object-use: give --at x,y,z to choose one")
		return ErrAmbiguousObject
	}
	if len(matches) > 1 {
		s.opts.logf("object-use: %d game objects with entry %d within %.0f yd; --at (%.2f, %.2f, %.2f) picks the one at %.2f, %.2f, %.2f",
			len(matches), s.opts.Entry, reach, s.opts.AtX, s.opts.AtY, s.opts.AtZ,
			matches[idx].X, matches[idx].Y, matches[idx].Z)
	}
	target, dist := matches[idx], dists[idx]
	rep.Found = true
	rep.Name = s.world.Name(target)
	rep.Distance = dist
	s.opts.logf("object-use: entry %d (%s) at %.1f yd, state %s", target.Info.Entry, rep.Name, dist,
		goStateName(target.Info.GOState))

	// lootTarget sends the use, waits for the window or the state change and records both in the loot report.
	release, err := s.lootTarget(ctx, target)
	defer release()
	lr := &s.report.Loot
	if err != nil {
		if errors.Is(err, ErrLootRefused) || errors.Is(err, ErrNoLootWindow) {
			s.opts.logf("object-use: %v", err)
		} else {
			return err
		}
	}
	rep.StateBefore, rep.StateAfter = lr.StateBefore, lr.StateAfter
	rep.FlagsBefore, rep.FlagsAfter = lr.FlagsBefore, lr.FlagsAfter
	rep.StateKnown, rep.StateChanged = lr.StateKnown, lr.StateChanged
	rep.LootOpened = lr.Opened
	rep.LootItems = len(lr.Items)
	if lr.Opened {
		// The window is not the point of this command: read it and close it, taking nothing.
		if err := s.releaseLoot(ctx, s.loot.window); err != nil {
			return err
		}
		rep.Released = s.report.Loot.Released
	}
	s.opts.logf("object-use: state after: %s; %s", goStateName(lr.StateAfter), stateTransitionText(lr))
	if lr.Opened {
		s.opts.logf("object-use: the use opened a loot window (%d item(s)); it was released", len(lr.Items))
	}
	if !lr.StateKnown && !lr.Opened {
		s.opts.logf("object-use: the server sent neither a values update nor a loot window")
	}
	return nil
}
