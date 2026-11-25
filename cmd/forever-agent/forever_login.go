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
		fmt.Fprintln(stderr, "       forever-agent forever serve [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--password-file FILE] [--listen HOST:PORT] [--dir DIR] [--no-auth] [--navmesh DIR] [--parent MAP=PARENT] [--capture-dir DIR]")
		fmt.Fprintln(stderr, "       forever-agent forever spell-score [--json] [--min-active 30] [--fail-unknown] --oracle DIR [--spells DIR] <watch dir|trace>")
		fmt.Fprintln(stderr, "       forever-agent forever spell-diff [--json] [--min-active 30] [--fail-unknown] [--oracle DIR] [--spells DIR] <before> <after>")
		fmt.Fprintln(stderr, "       forever-agent forever bot-snapshot --dir DIR [--bots NAME,...] [--wait MS] [--out FILE]")
		fmt.Fprintln(stderr, "       forever-agent forever capture-stats <dir>")
		fmt.Fprintln(stderr, "       forever-agent forever scrub-check [--slots FILE] [--account EMAIL]... [--password-file FILE] [--session-key-file FILE]... [--auth-token-file FILE]... [--redact DIR] <dir>...")
		fmt.Fprintln(stderr, "       forever-agent forever capture-events [--kinds a,b] [--out FILE [--force]] <dir>")
		fmt.Fprintln(stderr, "       forever-agent forever rpc [--timeout DUR] [--raw] <dir> <method|list> [<params-json>|-]")
		fmt.Fprintln(stderr, "       forever-agent forever walk-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--dx YARDS] [--dy YARDS]")
		fmt.Fprintln(stderr, "       forever-agent forever chat-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--say TEXT] [--whisper NAME:TEXT] [--emote TEXT] [--listen DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever party-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--invite NAME | --accept-from NAME] [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever cmd-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--line TEXT]... [--tb-state TARGET]")
		fmt.Fprintln(stderr, "       forever-agent forever addon-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME --prefix PREFIX [--send TEXT] [--channel party|whisper:NAME] [--invite NAME | --accept-from NAME] [--listen DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever tbm-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--bots A,B] [--keep-bots] [--stay DURATION] [--cmd COMMAND]... [--transport auto|addon|addon-self|chat] [--timeout DURATION] [--tbm-raw FILE] [--capture-dir DIR]")
		fmt.Fprintln(stderr, "       forever-agent forever fb-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--add CLASS[:ROLE]] [--order WORD|party:WORD] [--dismiss NAME|all] [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever fb-dungeon [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--dungeon NAME] [--role tank|healer|dps] [--watch SECONDS] [--leave=false] [--settle DURATION] [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever fight-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--entry N] [--fight-for DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever loot-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--entry N] [--roll need|greed|pass] [--take N] [--money=false] [--at x,y,z] [--at-range YARDS] [--range YARDS] [--stay DURATION] [--fight-for DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever object-use [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME --entry N [--range YARDS] [--stay DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever death-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--death-wait DURATION] [--accept-resurrect [--accept-resurrect-from NAME] [--resurrect-wait DURATION]] [--spirit-healer] [--navmesh DIR [--parent MAP=PARENT[,..]]] [--capture-dir DIR]")
		fmt.Fprintln(stderr, "       forever-agent forever act [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--steps FILE | --step 'verb k=v'...]")
		fmt.Fprintln(stderr, "       forever-agent forever char-create [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --name NAME [--surname S] --race R --class C [--sex S] (--choice OPTION:CHOICE ... | --no-appearance)")
		fmt.Fprintln(stderr, "       forever-agent forever char-delete [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL (--name NAME [--surname S] | --guid GUID) --yes NAME")
		fmt.Fprintln(stderr, "       forever-agent forever path-check --navmesh DIR --map N --from X,Y,Z --to X,Y,Z [--parent N] [--server-filter]")
		fmt.Fprintln(stderr, "       forever-agent forever route-walk --plan FILE [--legs a-b|NAME,...] [--gm] --navmesh DIR [--parent MAP=PARENT[,..]] [--enter tele|none] [--leave tele|none] [--confirm gps|client] [--out DIR] [--json] --rest host:port --rpc host:port --ca FILE --account EMAIL --character NAME [--max-deviation YD] [--stuck-after DURATION] [--fall-drop YD] [--sample DURATION] [--timeout DURATION] [--settle DURATION]")
		fmt.Fprintln(stderr, "       forever-agent forever dungeon-run --plan FILE --party TANK,HEALER,DPS,DPS,DPS --character NAME [--rest host:port] [--rpc host:port] [--ca FILE] [--account EMAIL] [--enter tele|portal|none] [--from-leg N] [--legs a,b] [--reset] [--out DIR] [--navmesh DIR] [--dry]")
		fmt.Fprintln(stderr, "       forever-agent forever dungeon-watch --leader NAME --plan FILE --character NAME [--rest host:port] [--rpc host:port] [--ca FILE] [--account EMAIL] [--enter tele|portal|none] [--max-minutes N] [--max-wipes N] [--practice=true] [--watchdog-minutes N] [--pause-minutes N] [--poll SEC] [--repulls N] [--member-deaths N] [--slow-factor X] [--combatlog-watch group|unit|off] [--out DIR] [--map] [--navmesh DIR] [--dry]")
		fmt.Fprintln(stderr, "       forever-agent forever raid-watch --leader NAME --plan FILE --character NAME [--layout FILE] [--rest host:port] [--rpc host:port] [--ca FILE] [--account EMAIL] [--enter tele|portal|none] [--max-minutes N] [--max-wipes N] [--practice=true] [--watchdog-minutes N] [--pause-minutes N] [--poll SEC] [--out DIR] [--dry]")
		fmt.Fprintln(stderr, "       forever-agent forever dungeon-play --plan FILE --bots TANK,HEALER,DPS,DPS --character NAME [--rest host:port] [--rpc host:port] [--ca FILE] [--account EMAIL] [--max-minutes N] [--max-wipes N] [--disband] [--out DIR] [--map] [--min-active SEC] [--navmesh DIR] [--dry]")
		fmt.Fprintln(stderr, "       "+foreverPreflightUsage[len("usage: "):])
		fmt.Fprintln(stderr, "       forever-agent forever bg-watch --map wsg|ab [--role observer|fc] [--character NAME] [--rest host:port] [--rpc host:port] [--ca FILE] [--account EMAIL] [--max-minutes N] [--poll SEC] [--quiet-min N] [--idle-s N] [--escort-share X] [--touch-s N] [--out DIR] [--dry]")
		fmt.Fprintln(stderr, "       forever-agent forever run-map DIR [--plan FILE] [--navmesh DIR [--parent MAP=PARENT[,..]]] [--out NAME.svg] [--html] [--names] [--floors auto|off|z1,z2] [--width PX] [--from-s N] [--to-s N] [--force]")
		fmt.Fprintln(stderr, "       forever-agent forever gm-walk --follow NAME [--marks-from gm|any] [--sample DURATION] [--tolerance YD] [--follow-yd YD] [--out FILE] [--module-out FILE] [--dry FILE] [--settle DURATION] [--timeout DURATION] --rest host:port --rpc host:port --ca FILE --account EMAIL --character NAME")
		fmt.Fprintln(stderr, "       forever-agent forever run-report RECORD [--json] [--out DIR] [--repulls N] [--member-deaths N] [--slow-factor X]")
		fmt.Fprintln(stderr, "       forever-agent forever run-report --compare A B")
		fmt.Fprintln(stderr, "       forever-agent forever deathrecap [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME --name NAME")
		fmt.Fprintln(stderr, "       forever-agent forever soap [--host H] [--port N] [--user NAME] [--password-file FILE | --password-env VAR] [--timeout DURATION] COMMAND")
		return exitUsage
	}
	switch args[0] {
	case "login-check":
		return foreverLoginCheckCmd(ctx, args[1:], stdout, stderr)
	case "world-check":
		return foreverWorldCheckCmd(ctx, args[1:], stdout, stderr)
	case "serve":
		return foreverServeCmd(ctx, args[1:], stdout, stderr)
	case "rpc":
		return foreverRPCCmd(ctx, args[1:], stdout, stderr)
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
	case "tbm-check":
		return foreverTBMCheckCmd(ctx, args[1:], stdout, stderr)
	case "fb-check":
		return foreverFBCheckCmd(ctx, args[1:], stdout, stderr)
	case "fb-dungeon":
		return foreverFBDungeonCmd(ctx, args[1:], stdout, stderr)
	case "fight-check":
		return foreverFightCheckCmd(ctx, args[1:], stdout, stderr)
	case "loot-check":
		return foreverLootCheckCmd(ctx, args[1:], stdout, stderr)
	case "object-use":
		return foreverObjectUseCmd(ctx, args[1:], stdout, stderr)
	case "death-check":
		return foreverDeathCheckCmd(ctx, args[1:], stdout, stderr)
	case "act":
		return foreverActCmd(ctx, args[1:], stdout, stderr)
	case "map-check":
		return foreverMapCheckCmd(ctx, args[1:], stdout, stderr)
	case "char-create":
		return foreverCharCreateCmd(ctx, args[1:], stdout, stderr)
	case "char-delete":
		return foreverCharDeleteCmd(ctx, args[1:], stdout, stderr)
	case "path-check":
		return foreverPathCheckCmd(ctx, args[1:], stdout, stderr)
	case "route-walk":
		return foreverRouteWalkCmd(ctx, args[1:], stdout, stderr)
	case "dungeon-run":
		return foreverDungeonRunCmd(ctx, args[1:], stdout, stderr)
	case "dungeon-watch":
		return foreverDungeonWatchCmd(ctx, args[1:], stdout, stderr)
	case "raid-watch":
		return foreverRaidWatchCmd(ctx, args[1:], stdout, stderr)
	case "preflight":
		return foreverPreflightCmd(ctx, args[1:], stdout, stderr)
	case "dungeon-play":
		return foreverDungeonPlayCmd(ctx, args[1:], stdout, stderr)
	case "bg-watch":
		return foreverBGWatchCmd(ctx, args[1:], stdout, stderr)
	case "run-map":
		return foreverRunMapCmd(ctx, args[1:], stdout, stderr)
	case "run-report":
		return foreverRunReportCmd(ctx, args[1:], stdout, stderr)
	case "gm-walk":
		return foreverGMWalkCmd(ctx, args[1:], stdout, stderr)
	case "gm-convert":
		return foreverGMConvertCmd(ctx, args[1:], stdout, stderr)
	case "dash-bots":
		return foreverDashBotsCmd(ctx, args[1:], stdout, stderr)
	case "deathrecap":
		return foreverDeathRecapCmd(ctx, args[1:], stdout, stderr)
	case "soap":
		return foreverSoapCmd(ctx, args[1:], stdout, stderr)
	case "spell-score":
		return foreverSpellScoreCmd(args[1:], stdout, stderr)
	case "spell-diff":
		return spellDiffCmd(args[1:], stdout, stderr)
	case "bot-snapshot":
		return botsnapshotCmd(ctx, args[1:], stdout, stderr)
	case "capture-stats":
		return foreverCaptureStatsCmd(args[1:], stdout, stderr)
	case "capture-events":
		return foreverCaptureEventsCmd(args[1:], stdout, stderr)
	case "scrub-check":
		return foreverScrubCmd(args[1:], stdout, stderr)
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
		fmt.Fprintln(stderr, "       forever-agent forever serve [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--password-file FILE] [--listen HOST:PORT] [--dir DIR] [--no-auth] [--navmesh DIR] [--parent MAP=PARENT]")
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
