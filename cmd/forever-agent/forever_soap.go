package main

// forever soap: one console-capable command on a ForeverCore worldserver over its SOAP endpoint, for setup without
// a client. The endpoint is loopback only unless --allow-non-loopback says otherwise; the credential is never a
// flag value in the process list, only a private file or an environment variable, and nothing prints it.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/soap"
)

const soapUsage = "usage: forever-agent forever soap [--host H] [--port N] [--user NAME] [--password-file FILE | --password-env VAR] [--allow-non-loopback] [--timeout DURATION] COMMAND"

func foreverSoapCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever soap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", envOr("FOREVER_SOAP_HOST", soap.DefaultHost), "SOAP endpoint host (loopback only unless --allow-non-loopback)")
	port := fs.Int("port", envPort("FOREVER_SOAP_PORT", soap.DefaultPort), "SOAP endpoint port (worldserver.conf.dist SOAP.Port)")
	user := fs.String("user", os.Getenv("FOREVER_SOAP_USER"), "admin account name the SOAP endpoint authenticates")
	passwordFile := fs.String("password-file", os.Getenv("FOREVER_SOAP_PASSWORD_FILE"), "private file (0600) holding the password")
	passwordEnv := fs.String("password-env", "", "environment variable holding the password (default "+soap.DefaultPasswordEnv+")")
	allowNonLoopback := fs.Bool("allow-non-loopback", false, "allow a host that is not loopback")
	timeout := fs.Duration("timeout", 0, "one request's deadline (default 15s, at most 2m)")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 || strings.TrimSpace(pos[0]) == "" {
		fmt.Fprintln(stderr, soapUsage)
		return exitUsage
	}
	if *timeout < 0 || *timeout > 2*time.Minute {
		fmt.Fprintln(stderr, "forever soap: --timeout is at most 2m")
		return exitUsage
	}
	command := strings.TrimSpace(pos[0])
	if soap.Denied(command) {
		fmt.Fprintf(stderr, "forever soap: command %q changes the world for everyone (AGENTS.md): refused\n", command)
		return exitUsage
	}
	cfg := soap.Config{Host: *host, Port: *port, User: *user,
		PasswordFile: *passwordFile, PasswordEnv: *passwordEnv, AllowNonLoopback: *allowNonLoopback}
	if *timeout > 0 {
		cfg.TimeoutMS = int(timeout.Milliseconds())
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "forever soap: %v\n", err)
		return exitUsage
	}
	client, err := soap.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "forever soap: %v\n", err)
		return exitUsage
	}
	text, err := client.Run(ctx, command)
	if err != nil {
		fmt.Fprintf(stderr, "forever soap: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(stdout)
	}
	return exitOK
}

// envPort reads a decimal port from the environment, else def.
func envPort(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envOr is an environment variable's value, else def.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
