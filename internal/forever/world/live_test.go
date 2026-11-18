package world

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
)

// TestLiveWorld runs the whole round against a real login and world server. It is skipped unless
// FOREVER_LIVE_REST is set, so it never runs in CI:
//
//	FOREVER_LIVE_REST=<host>:8081 FOREVER_LIVE_RPC=<host>:1119 FOREVER_LIVE_CA=<root ca file> \
//	FOREVER_LIVE_ACCOUNT=<account> FOREVER_PASSWORD=<password> go test ./internal/forever/world -run TestLiveWorld -v
//
// FOREVER_LIVE_BUILD (default 70707) is the client build to claim. A realm admits only the builds it accepts
// (RealmAcceptsBuild, src/server/shared/Realm/RealmList.cpp:255-279); the audit realm is on 70707 and lists no
// also-accepted builds, so claiming anything else is refused at the realm join. FOREVER_LIVE_CHARACTER is required
// (there is no default: a character name identifies a player), FOREVER_LIVE_STAY (default 20s) how long to stay,
// FOREVER_LIVE_SERVER_NAME the certificate name (default trinity.actual.battle.net).
func TestLiveWorld(t *testing.T) {
	rest := os.Getenv("FOREVER_LIVE_REST")
	if rest == "" {
		t.Skip("set FOREVER_LIVE_REST (and FOREVER_LIVE_RPC, FOREVER_LIVE_CA, FOREVER_LIVE_ACCOUNT, FOREVER_PASSWORD) to run against the live servers")
	}
	account := os.Getenv("FOREVER_LIVE_ACCOUNT")
	password := os.Getenv("FOREVER_PASSWORD")
	if account == "" || password == "" {
		t.Fatal("FOREVER_LIVE_ACCOUNT and FOREVER_PASSWORD must be set with FOREVER_LIVE_REST")
	}
	build := uint64(70707)
	if raw := os.Getenv("FOREVER_LIVE_BUILD"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			t.Fatalf("FOREVER_LIVE_BUILD: %v", err)
		}
		build = v
	}
	character := os.Getenv("FOREVER_LIVE_CHARACTER")
	if character == "" {
		t.Skip("set FOREVER_LIVE_CHARACTER to the character to enter the world with")
	}
	stay := 20 * time.Second
	if raw := os.Getenv("FOREVER_LIVE_STAY"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("FOREVER_LIVE_STAY: %v", err)
		}
		stay = v
	}
	serverName := os.Getenv("FOREVER_LIVE_SERVER_NAME")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute+stay)
	defer cancel()
	login, err := bnet.Login(ctx, bnet.Options{
		RESTAddr:    rest,
		RPCAddr:     os.Getenv("FOREVER_LIVE_RPC"),
		CAFile:      os.Getenv("FOREVER_LIVE_CA"),
		ServerName:  serverName,
		Account:     account,
		Password:    password,
		ClientBuild: uint32(build),
		Logf:        t.Logf,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	report, err := Enter(ctx, Options{
		Address:         login.ServerIP + ":" + strconv.FormatUint(uint64(login.ServerPort), 10),
		RealmID:         login.RealmID,
		RealmAddress:    login.RealmAddress,
		RealmJoinTicket: login.JoinTicket,
		ClientSecret:    login.ClientSecret,
		JoinSecret:      login.JoinSecret,
		ClientBuild:     uint32(build),
		RealmBuild:      uint32(build),
		Character:       character,
		Stay:            stay,
		Logf:            t.Logf,
	})
	if err != nil {
		if report != nil {
			t.Logf("packet counts: %v", report.Counts)
		}
		t.Fatalf("enter: %v", err)
	}
	if !report.InWorld {
		t.Fatal("the server never sent SMSG_MOVE_SET_ACTIVE_MOVER")
	}
	if !report.LogoutComplete {
		t.Fatal("no SMSG_LOGOUT_COMPLETE")
	}
	if report.TimeSyncs == 0 {
		t.Error("the server sent no SMSG_TIME_SYNC_REQUEST")
	}
	unknown := 0
	for name, n := range report.Counts {
		if strings.HasPrefix(name, "wire 0x") {
			unknown += n
		}
	}
	t.Logf("in the world: map %d, %s, %d time syncs, %d pings, %d pongs, %d unknown packets, logout complete",
		report.MapID, report.Character.DisplayName(), report.TimeSyncs, report.Pings, report.Pongs, unknown)
}
