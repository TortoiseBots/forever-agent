// Command refactor-world-types-4 rewrites the step-4 files of the
// FOREVER-AGENT-WORLD-TYPES brief: the moved session result names and the
// moved game value names become world.X in every package outside the
// protocol layer (session, auth, worldconn, packet, opcode, objects, game).
// Adds the world import, drops the session/game import only when no other
// session.*/game.* use remains. Tests included, consistent per package.
//
// Usage: go run ./tools/refactor/wt4 <session-list> <game-list>
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	sessRe = regexp.MustCompile(`\bsession\.(StatusInfo|Status|StatusInWorld|PartyInfo|PartyMember|LootWindow|LootSlot|LootTake|LootTaken|RollRef|RollInfo|TradeRequest|TradeResult|TradeFailed|TradeItemReq|TradeGave|TradeStepState|TradeSetResult|PetInfo|PetOrder|PetSpell|DeathInfo|MountRef|MountInfo|EquipRef|BuyRef|EffectRow|ReagentRow|SpellRow|SpellsFilter|MarkResult|LootedObject)\b`)
	gameRe = regexp.MustCompile(`\bgame\.(ResurrectRequest|Corpse|ItemInfo|Facing|InitialSpells|InitialCooldown|InfiniteCooldown|TradeItem)\b`)
	anySes = regexp.MustCompile(`\bsession\.[A-Z][A-Za-z0-9_]*\b`)
	anyGam = regexp.MustCompile(`\bgame\.[A-Z][A-Za-z0-9_]*\b`)
	sessIm = regexp.MustCompile(`(?m)^\t"github\.com/pjerra/forever-agent/internal/session"\n`)
	gameIm = regexp.MustCompile(`(?m)^\t"github\.com/pjerra/forever-agent/internal/game"\n`)
	worldI = "github.com/pjerra/forever-agent/internal/world"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: wt4 <session-list> <game-list>")
		os.Exit(2)
	}
	sraw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	graw, err := os.ReadFile(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fails := 0
	for _, f := range strings.Fields(string(sraw)) {
		if err := rewrite(f, sessRe, anySes, sessIm); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			fails++
		}
	}
	for _, f := range strings.Fields(string(graw)) {
		if err := rewrite(f, gameRe, anyGam, gameIm); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			fails++
		}
	}
	if fails > 0 {
		os.Exit(1)
	}
}

func rewrite(path string, re, any, imp *regexp.Regexp) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := string(b)
	if !re.MatchString(src) {
		return fmt.Errorf("no step-4 name found")
	}
	out := re.ReplaceAllString(src, "world.$1")

	if !strings.Contains(out, "\t\""+worldI+"\"\n") {
		m := imp.FindStringIndex(out)
		if m == nil {
			return fmt.Errorf("no session/game import to anchor the world import")
		}
		out = out[:m[1]] + "\t\"" + worldI + "\"\n" + out[m[1]:]
	}

	if !any.MatchString(out) {
		n := imp.FindStringIndex(out)
		if n == nil {
			return fmt.Errorf("import already gone but prefix still used?")
		}
		out = out[:n[0]] + out[n[1]:]
	}
	return os.WriteFile(path, []byte(out), 0o644)
}
