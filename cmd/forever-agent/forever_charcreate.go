package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// forever char-create and forever char-delete: the character screen, without entering the world. Both log in like
// world-check (the same connection flags and the same FOREVER_PASSWORD), authenticate the world socket, list the
// account's characters and then create or delete one; every result code prints the core's name.

// exitCharUncertain: the create or delete was sent and its answer did not come before the run ended, so the
// character may or may not exist (or be gone). Distinct from a refusal or a lost link: the caller lists again.
const exitCharUncertain = 32

// charScreenFlags registers the connection half both character-screen commands share: the foreverCommon connection
// flags (--build 0 means the newest build this client knows that the realm accepts) plus the instance redirect the
// world socket takes.
func charScreenFlags(fs *flag.FlagSet, c *foreverCommon) {
	c.connectionFlags(fs)
	fs.BoolVar(&c.allowRedirect, "allow-instance-redirect", false, "accept a SMSG_CONNECT_TO that names a host other than the realm socket's (clustered servers)")
}

// connect logs in at the Battle.net login server and returns the character-screen connection options, or the exit
// code of the failure.
func (c *foreverCommon) connect(ctx context.Context, stdout, stderr io.Writer) (world.Options, int) {
	result, err := c.login(ctx, "forever char screen", stdout, c.worldAccept(stdout))
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return world.Options{}, code
		}
		return world.Options{}, exitAuth
	}
	return c.withLogin(result, stdout, world.Options{}), exitOK
}

func printCharacterList(w io.Writer, characters []world.Character) {
	for _, c := range characters {
		fmt.Fprintf(w, "  character %s: level %d, class %d, race %d, sex %d, map %d, guid %s\n",
			safeName(c.DisplayName()), c.Level, c.Class, c.Race, c.Sex, c.MapID, c.GUID)
	}
}

// charChoiceList collects --choice OPTION:CHOICE: the appearance customization pairs of char-create, sent as given
// (the server validates each pair against its DB2 tables). Only the two numbers are checked here.
type charChoiceList []world.CustomizationChoice

func (c *charChoiceList) String() string { return "" }

func (c *charChoiceList) Set(v string) error {
	option, choice, ok := strings.Cut(v, ":")
	if !ok {
		return fmt.Errorf("want OPTION:CHOICE, got %q", v)
	}
	optionID, err := strconv.ParseUint(strings.TrimSpace(option), 10, 32)
	if err != nil {
		return fmt.Errorf("the option is not a number: %q", option)
	}
	choiceID, err := strconv.ParseUint(strings.TrimSpace(choice), 10, 32)
	if err != nil {
		return fmt.Errorf("the choice is not a number: %q", choice)
	}
	*c = append(*c, world.CustomizationChoice{OptionID: uint32(optionID), ChoiceID: uint32(choiceID)})
	return nil
}

// foreverCharCreateCmd is `forever char-create --name N --race R --class C [--sex S] (--choice OPTION:CHOICE ... |
// --no-appearance)`.
func foreverCharCreateCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever char-create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	charScreenFlags(fs, &common)
	name := fs.String("name", "", "character name to create")
	surname := fs.String("surname", "", "character surname (Classic 1.60)")
	race := fs.Uint("race", 0, "race id (ChrRaces.dbc)")
	class := fs.Uint("class", 0, "class id (ChrClasses.dbc)")
	sex := fs.Uint("sex", 0, "0 male, 1 female")
	noAppearance := fs.Bool("no-appearance", false, "for test characters: created with no appearance choices")
	var choices charChoiceList
	fs.Var(&choices, "choice", "appearance choice OPTION:CHOICE (repeatable; sent as given)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := common.checkConnection("forever char-create"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if fs.NArg() != 0 || *name == "" || *race == 0 || *race > 0xFF || *class == 0 || *class > 0xFF || *sex > 1 {
		fmt.Fprintln(stderr, "forever char-create: --name, --race and --class are required; --race and --class are 1-255, --sex 0 or 1")
		return exitUsage
	}
	switch {
	case len(choices) > 0 && *noAppearance:
		fmt.Fprintln(stderr, "forever char-create: --choice and --no-appearance are exclusive")
		return exitUsage
	case len(choices) == 0 && !*noAppearance:
		fmt.Fprintln(stderr, "forever char-create: give --choice OPTION:CHOICE (repeatable) or --no-appearance; a create with no appearance choices is a test character's, not a real client's")
		return exitUsage
	}
	if err := common.passwordCheck("forever char-create"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	opts, code := common.connect(ctx, stdout, stderr)
	if code != exitOK {
		return code
	}
	spec := world.CreateSpec{
		Options:      opts,
		Name:         *name,
		Surname:      *surname,
		Race:         uint8(*race),
		Class:        uint8(*class),
		Sex:          uint8(*sex),
		Choices:      choices,
		NoAppearance: *noAppearance,
	}
	report, err := world.CreateCharacter(ctx, spec)
	if report != nil {
		if report.CodeName != "" {
			fmt.Fprintf(stdout, "char-create: %s -> %s\n", safeName(*name), report.CodeName)
		}
		if len(report.Before) > 0 {
			fmt.Fprintf(stdout, "char screen before: %d character(s)\n", len(report.Before))
			printCharacterList(stdout, report.Before)
		}
		if len(report.After) > 0 {
			fmt.Fprintf(stdout, "char screen after: %d character(s)\n", len(report.After))
			printCharacterList(stdout, report.After)
		}
	}
	if err != nil {
		var uncertain world.UncertainSendError
		if errors.As(err, &uncertain) {
			// Exactly the outcome, nothing else: the character may or may not be there, and only a list settles it.
			fmt.Fprintln(stderr, uncertain.Outcome())
			return exitCharUncertain
		}
		fmt.Fprintln(stderr, "forever char-create:", err)
		var refused *world.CreateRefusedError
		if errors.As(err, &refused) {
			return exitWorld
		}
		if errors.Is(err, world.ErrLinkLost) || errors.Is(err, context.Canceled) {
			return exitDisconnected
		}
		return exitWorld
	}
	if report.RefreshErr != nil {
		fmt.Fprintf(stdout, "char-create: %s created (race %d, class %d, sex %d, %d appearance choice(s)), guid %s; done; the list could not be refreshed\n",
			safeName(report.Name), *race, *class, *sex, len(choices), report.GUID)
		return exitOK
	}
	fmt.Fprintf(stdout, "char-create: %s created (race %d, class %d, sex %d, %d appearance choice(s)), guid %s, %d character(s) after\n",
		safeName(report.Name), *race, *class, *sex, len(choices), report.GUID, len(report.After))
	return exitOK
}

// parseCharacterGUID reads the form the character list prints: 0x, then sixteen hex digits of High and sixteen of
// Low (wire.GUID.String). A bare 32-digit hex string is the same GUID without the prefix.
func parseCharacterGUID(s string) (wire.GUID, error) {
	digits := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(digits) != 32 {
		return wire.GUID{}, fmt.Errorf("--guid %q: want 0x and 32 hex digits", s)
	}
	high, err := strconv.ParseUint(digits[:16], 16, 64)
	if err != nil {
		return wire.GUID{}, fmt.Errorf("--guid %q: %v", s, err)
	}
	low, err := strconv.ParseUint(digits[16:], 16, 64)
	if err != nil {
		return wire.GUID{}, fmt.Errorf("--guid %q: %v", s, err)
	}
	return wire.GUID{Low: low, High: high}, nil
}

// foreverCharDeleteCmd is `forever char-delete --name N --yes N` (or `--guid G`): the name is resolved from the
// account's character list in the same session, the delete goes by GUID, and nothing is sent until --yes repeats the
// resolved character's full name.
func foreverCharDeleteCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever char-delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	charScreenFlags(fs, &common)
	name := fs.String("name", "", "character name to delete; a full name carries the surname after the first blank")
	surname := fs.String("surname", "", "character surname, when it is not part of --name")
	guidText := fs.String("guid", "", "exact character GUID (0xHIGH LOW), instead of --name")
	yes := fs.String("yes", "", "the resolved character's full name; without it the command deletes nothing")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := common.checkConnection("forever char-delete"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if fs.NArg() != 0 || (*name == "") == (*guidText == "") {
		fmt.Fprintln(stderr, "forever char-delete: give exactly one of --name and --guid")
		return exitUsage
	}
	if *name != "" && *surname != "" && strings.ContainsAny(strings.TrimSpace(*name), " \t") {
		fmt.Fprintln(stderr, "forever char-delete: give the surname in --name or in --surname, not both")
		return exitUsage
	}
	var guid wire.GUID
	if *guidText != "" {
		var err error
		if guid, err = parseCharacterGUID(*guidText); err != nil {
			fmt.Fprintln(stderr, "forever char-delete:", err)
			return exitUsage
		}
	}
	if err := common.passwordCheck("forever char-delete"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	opts, code := common.connect(ctx, stdout, stderr)
	if code != exitOK {
		return code
	}
	report, err := world.DeleteCharacter(ctx, world.DeleteSpec{
		Options: opts,
		GUID:    guid,
		Name:    *name,
		Surname: *surname,
		Confirm: *yes,
	})
	var unconfirmed *world.DeleteUnconfirmedError
	if errors.As(err, &unconfirmed) {
		if *yes == "" {
			fmt.Fprintf(stdout, "would delete %s (level %d, %s); pass --yes %s\n",
				safeName(unconfirmed.Character.DisplayName()), unconfirmed.Character.Level, unconfirmed.Character.GUID,
				safeName(unconfirmed.Character.DisplayName()))
		} else {
			fmt.Fprintf(stderr, "forever char-delete: --yes %q does not match %q; nothing was deleted\n",
				safeName(*yes), safeName(unconfirmed.Character.DisplayName()))
		}
		return exitUsage
	}
	if report != nil {
		if report.CodeName != "" {
			fmt.Fprintf(stdout, "char-delete: %s (%s) -> %s\n", safeName(report.Name), report.GUID, report.CodeName)
		}
		if len(report.After) > 0 {
			fmt.Fprintf(stdout, "char screen after: %d character(s)\n", len(report.After))
			printCharacterList(stdout, report.After)
		}
	}
	if err != nil {
		var uncertain world.UncertainSendError
		if errors.As(err, &uncertain) {
			// Exactly the outcome, nothing else: the character may or may not be gone, and only a list settles it.
			fmt.Fprintln(stderr, uncertain.Outcome())
			return exitCharUncertain
		}
		fmt.Fprintln(stderr, "forever char-delete:", err)
		var refused *world.DeleteRefusedError
		if errors.As(err, &refused) {
			return exitWorld
		}
		if errors.Is(err, world.ErrLinkLost) || errors.Is(err, context.Canceled) {
			return exitDisconnected
		}
		return exitWorld
	}
	if report.RefreshErr != nil {
		fmt.Fprintf(stdout, "char-delete: %s deleted; done; the list could not be refreshed\n", safeName(report.Name))
		return exitOK
	}
	fmt.Fprintf(stdout, "char-delete: %s deleted, %d character(s) after\n", safeName(report.Name), len(report.After))
	return exitOK
}
