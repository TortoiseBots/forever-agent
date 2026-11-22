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
