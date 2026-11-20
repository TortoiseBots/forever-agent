package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// foreverCmd is the entry point of the Forever (Classic 1.60.1) commands: login-check stops at the realm join
// answer, world-check enters the world with one character, walk-check enters it, reads the objects around the
// character and walks.
func foreverCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: forever-agent forever login-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] --account EMAIL")
		fmt.Fprintln(stderr, "       forever-agent forever world-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever walk-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--dx YARDS] [--dy YARDS]")
		fmt.Fprintln(stderr, "       forever-agent forever chat-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--say TEXT] [--whisper NAME:TEXT] [--emote TEXT] [--listen DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever party-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--invite NAME | --accept-from NAME] [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever cmd-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--line TEXT]... [--tb-state TARGET]")
		fmt.Fprintln(stderr, "       forever-agent forever addon-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME --prefix PREFIX [--send TEXT] [--channel party|whisper:NAME] [--invite NAME | --accept-from NAME] [--listen DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever fb-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--add CLASS[:ROLE]] [--order WORD|party:WORD] [--dismiss NAME|all] [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever fb-dungeon [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--dungeon NAME] [--role tank|healer|dps] [--watch SECONDS] [--leave=false] [--settle DURATION] [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever fight-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--entry N] [--fight-for DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever map-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--settle DURATION] [--stay DURATION] [--area-trigger ID]")
		fmt.Fprintln(stderr, "       forever-agent forever char-create [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --name NAME [--surname S] --race R --class C [--sex S] (--choice OPTION:CHOICE ... | --no-appearance)")
		fmt.Fprintln(stderr, "       forever-agent forever char-delete [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL (--name NAME [--surname S] | --guid GUID) --yes NAME")
		fmt.Fprintln(stderr, "       forever-agent forever path-check --navmesh DIR --map N --from X,Y,Z --to X,Y,Z [--parent N] [--server-filter]")
		fmt.Fprintln(stderr, "       forever-agent forever dungeon-run --plan FILE --party TANK,HEALER,DPS,DPS,DPS --character NAME [--rest host:port] [--rpc host:port] [--ca FILE] [--account EMAIL] [--enter tele|portal|none] [--from-leg N] [--legs a,b] [--reset] [--out DIR] [--navmesh DIR] [--dry]")
		fmt.Fprintln(stderr, "       forever-agent forever dash-bots --url URL --user NAME [--pass-file FILE] [--dash-insecure-http] [--watch [--interval 2s]] [--stuck-after 20s] [--lone-combat-after 5s] [--group-distance 60] [--timeout 15s]")
		return exitUsage
	}
	switch args[0] {
	case "login-check":
		return foreverLoginCheckCmd(ctx, args[1:], stdout, stderr)
	case "world-check":
		return foreverWorldCheckCmd(ctx, args[1:], stdout, stderr)
	case "walk-check":
		return foreverWalkCheckCmd(ctx, args[1:], stdout, stderr)
	case "chat-check":
		return foreverChatCheckCmd(ctx, args[1:], stdout, stderr)
	case "party-check":
		return foreverPartyCheckCmd(ctx, args[1:], stdout, stderr)
	case "cmd-check":
		return foreverCommandCheckCmd(ctx, args[1:], stdout, stderr)
	case "addon-check":
		return foreverAddonCheckCmd(ctx, args[1:], stdout, stderr)
	case "fb-check":
		return foreverFBCheckCmd(ctx, args[1:], stdout, stderr)
	case "fb-dungeon":
		return foreverFBDungeonCmd(ctx, args[1:], stdout, stderr)
	case "fight-check":
		return foreverFightCheckCmd(ctx, args[1:], stdout, stderr)
	case "death-check":
		return foreverDeathCheckCmd(ctx, args[1:], stdout, stderr)
	case "map-check":
		return foreverMapCheckCmd(ctx, args[1:], stdout, stderr)
	case "char-create":
		return foreverCharCreateCmd(ctx, args[1:], stdout, stderr)
	case "char-delete":
		return foreverCharDeleteCmd(ctx, args[1:], stdout, stderr)
	case "path-check":
		return foreverPathCheckCmd(ctx, args[1:], stdout, stderr)
	case "dungeon-run":
		return foreverDungeonRunCmd(ctx, args[1:], stdout, stderr)
	case "dash-bots":
		return foreverDashBotsCmd(ctx, args[1:], stdout, stderr)
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
	var common foreverCommon
	common.connectionFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: forever-agent forever login-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] --account EMAIL")
		fmt.Fprintln(stderr, "       forever-agent forever world-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION]")
		return exitUsage
	}
	if err := common.checkConnection("forever login-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := common.passwordCheck("forever login-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	result, err := common.login(ctx, "forever login-check", stdout, nil)
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		return exitAuth
	}
	fmt.Fprintln(stdout, result.Summary())
	return exitOK
}
