#!/bin/sh
# Fetch TortoiseBotsForeverManager at the pinned commit into .cache/ (ignored by git)
# using plain git clone --depth 1 (no GitHub API) and print the directory.
# Pin: 2.4.1-forever.3 (main, 2026-11-23); contract rev 2, PROTO level 1.
# Exit 3 when it is not cached and cannot be cloned.
set -eu

ADDON_COMMIT=7e980230ad41cc57fc415943f1e9686e2734b079
ADDON_REPO=TortoiseBots/TortoiseBotsForeverManager

root=$(cd "$(dirname "$0")/../../.." && pwd)
if [ -n "${TBFM_ADDON_DIR:-}" ]; then
	if [ ! -f "$TBFM_ADDON_DIR/TortoiseBotsForeverManager.toc" ]; then
		echo "tbm-forever-contract: TBFM_ADDON_DIR=$TBFM_ADDON_DIR has no TortoiseBotsForeverManager.toc; not touching it" >&2
		exit 3
	fi
	echo "tbm-forever-contract: using TBFM_ADDON_DIR=$TBFM_ADDON_DIR, the pinned commit $ADDON_COMMIT is bypassed" >&2
	echo "$TBFM_ADDON_DIR"
	exit 0
fi

dir=$root/.cache/tbmcontract/forever-$ADDON_COMMIT
if [ -f "$dir/TortoiseBotsForeverManager.toc" ]; then
	echo "$dir"
	exit 0
fi

command -v git >/dev/null 2>&1 || { echo "tbm-forever-contract: git missing, cannot fetch the addon" >&2; exit 3; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if ! GIT_TERMINAL_PROMPT=0 git clone --quiet --depth 1 "https://github.com/$ADDON_REPO.git" "$tmp/x" 2>/dev/null; then
	echo "tbm-forever-contract: cannot clone $ADDON_REPO@$ADDON_COMMIT (offline?)" >&2
	exit 3
fi
head=$(git -C "$tmp/x" rev-parse HEAD)
if [ "$head" != "$ADDON_COMMIT" ]; then
	if ! GIT_TERMINAL_PROMPT=0 git -C "$tmp/x" fetch --quiet --depth 1 origin "$ADDON_COMMIT" 2>/dev/null \
		|| ! git -C "$tmp/x" checkout --quiet "$ADDON_COMMIT" 2>/dev/null; then
		echo "tbm-forever-contract: cannot checkout pinned commit $ADDON_COMMIT" >&2
		exit 3
	fi
fi
[ -f "$tmp/x/TortoiseBotsForeverManager.toc" ] || { echo "tbm-forever-contract: cloned repo lacks TortoiseBotsForeverManager.toc" >&2; exit 3; }

mkdir -p "$(dirname "$dir")"
rm -rf "$dir"
mv "$tmp/x" "$dir"
echo "$dir"
