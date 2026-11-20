package world

// Enter must adopt the socket the login's build probe already authenticated instead of authenticating a second
// time: that second handshake would be another session for a build the world server just accepted. The test sets a
// dead Address, so a run that ignored the probe socket could not connect at all.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

func TestEnterAdoptsTheProbedWorldSocket(t *testing.T) {
	f := newFakeServer(t)
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	cfg := worldconn.Config{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		BuildAuthKey:    f.buildKey[:],
		Deadline:        10 * time.Second,
	}
	probe, err := worldconn.Authenticate(context.Background(), cfg)
	if err != nil {
		t.Fatalf("the probe's authentication: %v", err)
	}

	var steps []string
	opts := Options{
		// Dead on purpose: Enter may only use the probed socket. Anything that dials this address fails at once.
		Address:         "127.0.0.1:1",
		RealmID:         cfg.RealmID,
		RealmAddress:    cfg.RealmAddress,
		RealmJoinTicket: cfg.RealmJoinTicket,
		ClientSecret:    cfg.ClientSecret,
		JoinSecret:      cfg.JoinSecret,
		ClientBuild:     cfg.ClientBuild,
		RealmBuild:      cfg.RealmBuild,
		BuildAuthKey:    cfg.BuildAuthKey,
		Character:       "XXXXXX", // the fixture's character name
		Stay:            time.Second,
		Deadline:        30 * time.Second,
		WorldConn:       probe,
		Logf: func(format string, args ...any) {
			steps = append(steps, fmt.Sprintf(format, args...))
		},
	}
	report, err := Enter(context.Background(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	f.wait(t)

	if !report.InWorld || !report.LogoutComplete {
		t.Fatalf("in world %v, logout complete %v", report.InWorld, report.LogoutComplete)
	}
	if report.Character.Name != "XXXXXX" {
		t.Fatalf("character %q", report.Character.Name)
	}
	if !strings.Contains(strings.Join(steps, "\n"), "adopting the connection the login authenticated") {
		t.Errorf("Enter did not report the adopted socket:\n%s", strings.Join(steps, "\n"))
	}
}
