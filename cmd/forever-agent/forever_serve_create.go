package main

// forever serve --create RACE,CLASS[,SEX] (--create-choice OPTION:CHOICE ... | --create-no-appearance): create the
// character on login when the account's list lacks the name (#99; turtle-agent's `--create`). The create is the
// character screen's own (world.CreateCharacter with OnlyIfMissing): the list is read first, a name already there
// sends nothing, and a refusal ends serve with the core's own code name before the real login is attempted. The
// character screen and the serving session are two logins (a realm join ticket is spent by the first), so a serve
// that creates takes two turns at the login gate.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

// serveCreate is the parsed --create flags: which character serve creates when it is missing.
type serveCreate struct {
	Race, Class, Sex uint8
	Choices          charChoiceList
	NoAppearance     bool
}

// parseServeCreate reads --create "race,class[,sex]" plus the appearance flags. Race and class are the ids as the
// character screen takes them (1-255; the server validates the pair against its tables), sex is 0 or 1, and, as for
// `char-create`, a create needs --create-choice pairs or --create-no-appearance by name.
func parseServeCreate(spec string, choices charChoiceList, noAppearance bool) (*serveCreate, error) {
	if spec == "" {
		if len(choices) > 0 || noAppearance {
			return nil, errors.New("--create-choice and --create-no-appearance need --create RACE,CLASS[,SEX]")
		}
		return nil, nil
	}
	parts := strings.Split(spec, ",")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, fmt.Errorf("--create %q: want race,class[,sex]", spec)
	}
	vals := []uint64{0, 0, 0}
	for i, part := range parts {
		v, err := strconv.ParseUint(strings.TrimSpace(part), 10, 8)
		if err != nil {
			return nil, fmt.Errorf("--create %q: %q is not a number 0-255", spec, strings.TrimSpace(part))
		}
		vals[i] = v
	}
	switch {
	case vals[0] == 0:
		return nil, fmt.Errorf("--create %q: the race is 1-255", spec)
	case vals[1] == 0:
		return nil, fmt.Errorf("--create %q: the class is 1-255", spec)
	case vals[2] > 1:
		return nil, fmt.Errorf("--create %q: the sex is 0 (male) or 1 (female)", spec)
	case len(choices) > 0 && noAppearance:
		return nil, errors.New("--create-choice and --create-no-appearance are exclusive")
	case len(choices) == 0 && !noAppearance:
		return nil, errors.New("--create needs --create-choice OPTION:CHOICE (repeatable) or --create-no-appearance; a create with no appearance choices is a test character's, not a real client's")
	}
	return &serveCreate{Race: uint8(vals[0]), Class: uint8(vals[1]), Sex: uint8(vals[2]), Choices: choices, NoAppearance: noAppearance}, nil
}

// serveEnsureCharacter is the create-on-login step: one login to the character screen, the create when the name is
// missing, and the exit code of a failure (exitOK when the character exists or was created).
func serveEnsureCharacter(ctx context.Context, common *foreverCommon, password string, c *serveCreate, stdout, stderr io.Writer) int {
	result, err := common.loginWithPassword(ctx, "forever serve", stdout, password, common.worldAccept(stdout))
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever serve: %v\n", err)
		return exitAuth
	}
	opts := common.withLogin(result, stdout, world.Options{})
	report, err := world.CreateCharacter(ctx, world.CreateSpec{
		Options: opts, Name: common.character, Race: c.Race, Class: c.Class, Sex: c.Sex,
		Choices: c.Choices, NoAppearance: c.NoAppearance, OnlyIfMissing: true,
	})
	if err != nil {
		var uncertain world.UncertainSendError
		if errors.As(err, &uncertain) {
			fmt.Fprintln(stderr, uncertain.Outcome())
			return exitCharUncertain
		}
		fmt.Fprintln(stderr, "forever serve: create:", err)
		if errors.Is(err, world.ErrLinkLost) || errors.Is(err, context.Canceled) {
			return exitDisconnected
		}
		return exitWorld
	}
	if report.Existed {
		fmt.Fprintf(stdout, "serve: character %s is on the account: nothing created\n", safeName(common.character))
		return exitOK
	}
	fmt.Fprintf(stdout, "serve: character %s created (race %d, class %d, sex %d, %d appearance choice(s)), guid %s\n",
		safeName(common.character), c.Race, c.Class, c.Sex, len(c.Choices), report.GUID)
	return exitOK
}
