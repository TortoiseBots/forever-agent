package bnet

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestLiveLogin runs the whole round against a real login server. It is skipped unless FOREVER_LIVE_REST is set, so
// CI never needs the server:
//
//	FOREVER_LIVE_REST=127.0.0.1:8081 FOREVER_LIVE_RPC=127.0.0.1:1119 FOREVER_LIVE_CA=/path/root_ca.pem \
//	FOREVER_LIVE_ACCOUNT=... FOREVER_PASSWORD=... go test ./internal/forever/bnet -run TestLiveLogin -v
//
// FOREVER_LIVE_BUILD pins the client build to claim; without it the login claims the newest build it knows that the realm accepts
// (Options.ClientBuild 0, the default the commands use). A realm only lets in the builds it accepts.
//
// The server must present a certificate for DefaultServerName (or FOREVER_LIVE_SERVER_NAME) signed by the CA, and
// the account must be a Battle.net account without an authenticator.
func TestLiveLogin(t *testing.T) {
	rest := os.Getenv("FOREVER_LIVE_REST")
	if rest == "" {
		t.Skip("set FOREVER_LIVE_REST (and FOREVER_LIVE_RPC, FOREVER_LIVE_CA, FOREVER_LIVE_ACCOUNT, FOREVER_PASSWORD) to run against a live login server")
	}
	account := os.Getenv("FOREVER_LIVE_ACCOUNT")
	password := os.Getenv("FOREVER_PASSWORD")
	if account == "" || password == "" {
		t.Fatal("FOREVER_LIVE_ACCOUNT and FOREVER_PASSWORD must be set with FOREVER_LIVE_REST")
	}
	serverName := os.Getenv("FOREVER_LIVE_SERVER_NAME")
	if serverName == "" {
		serverName = DefaultServerName
	}
	build := uint32(0) // the newest build this client knows that the realm accepts
	if raw := os.Getenv("FOREVER_LIVE_BUILD"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			t.Fatalf("FOREVER_LIVE_BUILD: %v", err)
		}
		build = uint32(value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := Login(ctx, Options{
		RESTAddr:    rest,
		RPCAddr:     os.Getenv("FOREVER_LIVE_RPC"),
		CAFile:      os.Getenv("FOREVER_LIVE_CA"),
		ServerName:  serverName,
		Account:     account,
		Password:    password,
		ClientBuild: build,
		Logf:        func(format string, args ...any) { t.Logf(format, args...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RealmName == "" || result.RealmID == 0 || result.RealmAddress == 0 {
		t.Fatalf("join answer without a realm: name=%d chars id=%d address=0x%08X, tickets=%d/%d bytes, secrets=%d/%d/%d bytes",
			len(result.RealmName), result.RealmID, result.RealmAddress,
			len(result.LoginTicket), len(result.JoinTicket),
			len(result.SessionKey), len(result.ClientSecret), len(result.JoinSecret))
	}
	if len(result.JoinTicket) == 0 || len(result.JoinSecret) != 32 || len(result.SessionKey) != 64 {
		t.Fatalf("join answer incomplete: ticket %d, secret %d, session key %d",
			len(result.JoinTicket), len(result.JoinSecret), len(result.SessionKey))
	}
	// the realm name and the world address are the server's text; print them through the sanitiser like the command does
	t.Logf("realm %q id %d address 0x%08X, world %s:%d", sanitizeServerText(result.RealmName), result.RealmID,
		result.RealmAddress, sanitizeServerText(result.ServerIP), result.ServerPort)
}
