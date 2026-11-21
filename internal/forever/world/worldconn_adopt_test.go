package world

// Enter must adopt the socket the login's build probe already authenticated instead of authenticating a second
// time: that second handshake would be another session for a build the world server just accepted. The test sets a
// dead Address, so a run that ignored the probe socket could not connect at all.

import (
	"bytes"
	"context"
	"fmt"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// adoptStepLimit bounds each blocking step of the test (the probe's handshake, Enter). It is generous for a loaded
// machine but finite: a step that outlives it fails the test with a message instead of hanging the package.
const adoptStepLimit = 60 * time.Second

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
	// Every blocking step below has its own deadline, so a run starved by a loaded machine (or a wedged step) fails
	// with a message naming the step and the goroutines, instead of waiting for go test's package timeout.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), adoptStepLimit)
	defer cancelProbe()
	probe, err := worldconn.Authenticate(probeCtx, cfg)
	if err != nil {
		t.Fatalf("the probe's authentication: %v", err)
	}

	var stepsMu sync.Mutex
	var stepList []string
	stepsText := func() string {
		stepsMu.Lock()
		defer stepsMu.Unlock()
		return strings.Join(stepList, "\n")
	}
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
		Stay:            100 * time.Millisecond,
		Deadline:        adoptStepLimit,
		WorldConn:       probe,
		Logf: func(format string, args ...any) {
			stepsMu.Lock()
			stepList = append(stepList, fmt.Sprintf(format, args...))
			stepsMu.Unlock()
		},
	}
	enterCtx, cancelEnter := context.WithTimeout(context.Background(), adoptStepLimit)
	defer cancelEnter()
	type result struct {
		report *Report
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		r, err := Enter(enterCtx, opts)
		resCh <- result{r, err}
	}()
	var res result
	select {
	case res = <-resCh:
	case <-time.After(adoptStepLimit + 15*time.Second):
		// Enter did not return even after its own context ended: dump the goroutines and fail now.
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 1)
		t.Fatalf("Enter did not return within %s of its deadline\ngoroutines:\n%s", 15*time.Second, buf.String())
	}
	report, err := res.report, res.err
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, stepsText())
	}
	f.wait(t)

	if !report.InWorld || !report.LogoutComplete {
		t.Fatalf("in world %v, logout complete %v", report.InWorld, report.LogoutComplete)
	}
	if report.Character.Name != "XXXXXX" {
		t.Fatalf("character %q", report.Character.Name)
	}
	if !strings.Contains(stepsText(), "adopting the connection the login authenticated") {
		t.Errorf("Enter did not report the adopted socket:\n%s", stepsText())
	}
}
