#!/bin/sh
# Replay every contract/tbm/v1/cases/*.case file through TortoiseBotsForeverManager (Lua 5.1).
#
#   run.sh [case.case...]
#
# Skips (exit 0, clear message) when lua5.1 or the addon is unavailable,
# unless TBM_CONTRACT_STRICT=1 is set.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
LUA=${LUA:-lua5.1}

skip() {
	echo "tbm-forever-contract: SKIPPED, $1" >&2
	[ "${TBM_CONTRACT_STRICT:-0}" = 1 ] && exit 1
	exit 0
}

command -v "$LUA" >/dev/null 2>&1 || skip "$LUA not found (apt install lua5.1)"
addon=$("$here/fetch.sh") || { [ -z "${TBFM_ADDON_DIR:-}" ] || exit 1; skip "addon not cached and not downloadable"; }
# The fetched commit must still be the release the cases were verified against:
# a bump to fetch.sh without re-checking the cases fails here (an explicit
# TBFM_ADDON_DIR is the caller's own checkout and is left alone).
PINNED_VERSION=2.4.1-forever.3
if [ -z "${TBFM_ADDON_DIR:-}" ]; then
	got=$(sed -n 's/^## Version:[[:space:]]*//p' "$addon/TortoiseBotsForeverManager.toc" | tr -d '\r')
	[ "$got" = "$PINNED_VERSION" ] || {
		echo "tbm-forever-contract: pinned addon is version $got, the cases were verified against $PINNED_VERSION" >&2
		exit 1
	}
fi
echo "tbm-forever-contract: addon $(basename "$addon") ($(ls "$addon"/*.lua | wc -l) lua files), $("$LUA" -v 2>&1)"

if [ "$#" -gt 0 ]; then
	for f in "$@"; do
		"$LUA" "$here/contract.lua" "$addon" "$f"
	done
	exit 0
fi

fail=0
count=0
for f in "$root"/contract/tbm/v1/cases/*.case; do
	count=$((count + 1))
	"$LUA" "$here/contract.lua" "$addon" "$f" || fail=1
done
[ "$count" -gt 0 ] || { echo "tbm-forever-contract: no .case files found" >&2; exit 1; }
[ "$fail" = 0 ] || { echo "tbm-forever-contract: FAILED" >&2; exit 1; }
echo "tbm-forever-contract: ok ($count cases)"
