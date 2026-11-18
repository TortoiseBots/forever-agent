package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
)

// foreverCmd is the entry point of the Forever (Classic 1.60.1) commands. Round 1 only logs in through the login
// server; the world socket comes later.
func foreverCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: forever-agent forever login-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] --account EMAIL")
		return exitUsage
	}
	switch args[0] {
	case "login-check":
		return foreverLoginCheckCmd(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "forever-agent forever: unknown command %q\n", args[0])
		return exitUsage
	}
}

// foreverLoginCheckCmd logs in to the Forever login server and stops at the realm join answer. The password comes
// from FOREVER_PASSWORD and is never a flag, never printed and never written anywhere; the output names each
// completed step and, at the end, the realm the join answer is for.
func foreverLoginCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever login-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	restAddr := fs.String("rest", "", "login REST host:port")
	rpcAddr := fs.String("rpc", "", "Battle.net RPC host:port")
	caFile := fs.String("ca", "", "PEM file of the root CA that signed the login server's certificate")
	serverName := fs.String("server-name", bnet.DefaultServerName, "name to verify in the login server's certificate")
	clientBuild := fs.Uint("build", 70938, "client build to claim; a realm only lets in the builds it accepts")
	account := fs.String("account", "", "account e-mail or name")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: forever-agent forever login-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] --account EMAIL")
		return exitUsage
	}
	if *restAddr == "" || *rpcAddr == "" || *caFile == "" || *account == "" {
		fmt.Fprintln(stderr, "forever login-check: --rest, --rpc, --ca and --account are required")
		return exitUsage
	}
	if *clientBuild == 0 || *clientBuild > 0xFFFFFFFF {
		fmt.Fprintln(stderr, "forever login-check: --build must be a client build number")
		return exitUsage
	}
	password := os.Getenv("FOREVER_PASSWORD")
	if password == "" {
		fmt.Fprintln(stderr, "forever login-check: FOREVER_PASSWORD is not set")
		return exitUsage
	}

	result, err := bnet.Login(ctx, bnet.Options{
		RESTAddr:    *restAddr,
		RPCAddr:     *rpcAddr,
		CAFile:      *caFile,
		ServerName:  *serverName,
		Account:     *account,
		Password:    password,
		ClientBuild: uint32(*clientBuild),
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stdout, format+"\n", args...)
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "forever login-check:", err)
		var realmErr *bnet.RealmError
		if errors.As(err, &realmErr) {
			return exitRealm
		}
		return exitAuth
	}
	fmt.Fprintln(stdout, result.Summary())
	return exitOK
}
