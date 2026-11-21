#!/usr/bin/env bash
# shard-pkgs splits the packages of the module into shards for parallel CI jobs, by their recorded test time.
#
# The packages that shard-tests.sh splits by test name (SHARDED_BY_NAME below) are left out: they are too long to be
# one unit. Every other package from `go list ./...` goes to exactly one shard: greedy longest-first bin packing on the
# seconds in tools/ci/pkg-times.txt. A package missing from that file (a new one) weighs DEFAULT_WEIGHT seconds and goes to
# the lightest shard, so it is never dropped; a name in the file that no longer exists is ignored.
#
# Usage:
#   shard-pkgs.sh list                        the packages the shards share, sorted, full import paths
#   shard-pkgs.sh shard I N LISTFILE TIMES    the packages shard I of N (0-based) runs, one per line
#   shard-pkgs.sh load N LISTFILE TIMES       the estimated seconds per shard (to see whether they drifted)
#   shard-pkgs.sh ran LOGFILE                 the packages a `go test` log shows a result line for, sorted
#   shard-pkgs.sh selftest                    checks the above on a small made-up list
# The union check is `shard-tests.sh check LISTFILE RANFILE...`, the same as for the test-name shards.
#
# `go` is taken from $GO (default: go on PATH).
set -euo pipefail

GO=${GO:-go}
SHARDED_BY_NAME=(cmd/forever-agent internal/forever/world)
DEFAULT_WEIGHT=1

die() {
	echo "shard-pkgs: $*" >&2
	exit 1
}

module() { "$GO" list -m; }

cmd_list() {
	local mod out p skip=()
	mod=$(module)
	for p in "${SHARDED_BY_NAME[@]}"; do skip+=("$mod/$p"); done
	out=$("$GO" list ./...) || die "go list failed"
	# A name in SHARDED_BY_NAME that is gone would silently shard nothing: say so.
	for p in "${skip[@]}"; do
		grep -qxF "$p" <<<"$out" || die "$p is in SHARDED_BY_NAME but not in go list ./..."
	done
	grep -vxF "$(printf '%s\n' "${skip[@]}")" <<<"$out" | LC_ALL=C sort -u
}

# assign N LISTFILE TIMES: print "shard package weight" for every package, greedy longest-first.
assign() {
	local n=$1 list=$2 times=$3 mod=${MODULE_PREFIX:-}
	[[ $n =~ ^[1-9][0-9]*$ ]] || die "shard count must be a number >= 1"
	[[ -f $list && -f $times ]] || die "list or times file missing"
	# weight<TAB>package, heaviest first, ties by name.
	awk -v mod="$mod" -v def="$DEFAULT_WEIGHT" '
		FNR == NR { if ($0 !~ /^#/ && NF >= 2) t[$1] = $2; next }
		{ rel = $0; if (mod != "" && index(rel, mod "/") == 1) rel = substr(rel, length(mod) + 2)
		  w = (rel in t) ? t[rel] : def; printf "%.3f\t%s\n", w, $0 }' "$times" <(LC_ALL=C sort -u "$list") |
		LC_ALL=C sort -t$'\t' -k1,1nr -k2,2 |
		awk -F'\t' -v n="$n" '{
			best = 0
			for (i = 1; i < n; i++) if (load[i] < load[best] - 1e-9) best = i
			load[best] += $1
			printf "%d\t%s\t%s\n", best, $2, $1
		}'
}

cmd_shard() {
	(($# == 4)) || die "usage: shard I N LISTFILE TIMES"
	[[ $1 =~ ^[0-9]+$ ]] && (($1 < $2)) || die "shard index $1 is not below the count $2"
	MODULE_PREFIX=$(module) assign "$2" "$3" "$4" | awk -F'\t' -v i="$1" '$1 == i {print $2}' | LC_ALL=C sort
}

cmd_load() {
	(($# == 3)) || die "usage: load N LISTFILE TIMES"
	MODULE_PREFIX=$(module) assign "$1" "$2" "$3" | awk -F'\t' '{c[$1]++; s[$1] += $3} END {for (i = 0; i in s; i++) printf "shard %d: %d packages, about %.0f s\n", i, c[i], s[i]}'
}

cmd_ran() {
	(($# == 1)) || die "usage: ran LOGFILE"
	[[ -f $1 ]] || die "no such log file: $1"
	# "ok  <TAB>pkg<TAB>1.2s", "FAIL<TAB>pkg<TAB>1.2s" and "?   <TAB>pkg<TAB>[no test files]".
	awk '$1 == "ok" || $1 == "FAIL" || $1 == "?" {print $2}' "$1" | grep -E '^[A-Za-z0-9._~/-]+$' | LC_ALL=C sort -u
}

cmd_selftest() {
	local self=${BASH_SOURCE[0]} tools tmp n i fail=0
	tools=$(dirname "$self")
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	# The functions need a module path; the self-test feeds assign directly.
	cat >"$tmp/list.txt" <<'LIST'
ex.com/m/a
ex.com/m/b
ex.com/m/c
ex.com/m/d
ex.com/m/e
ex.com/m/new1
ex.com/m/new2
LIST
	cat >"$tmp/times.txt" <<'TIMES'
# comment
a 100
b 60
c 50
d 40
e 10
gone 999
TIMES
	for n in 1 2 3 5 9; do
		local ranfiles=()
		for ((i = 0; i < n; i++)); do
			MODULE_PREFIX=ex.com/m assign "$n" "$tmp/list.txt" "$tmp/times.txt" | awk -F'\t' -v i="$i" '$1 == i {print $2}' | LC_ALL=C sort >"$tmp/s$n-$i.txt"
			ranfiles+=("$tmp/s$n-$i.txt")
		done
		"$tools/shard-tests.sh" check "$tmp/list.txt" "${ranfiles[@]}" >/dev/null || { echo "selftest: $n shards do not cover the list" >&2; fail=1; }
	done
	# Two shards: a (100) is alone first, b+c+d+e fill up to 140 / 120, and the two new packages (1 s each) go to the
	# lighter shard, 1.
	local got
	got=$(MODULE_PREFIX=ex.com/m assign 2 "$tmp/list.txt" "$tmp/times.txt" | awk -F'\t' '{printf "%s=%s ", $2, $1}')
	[[ $got == *"ex.com/m/a=0 "* && $got == *"ex.com/m/new1=1 "* && $got == *"ex.com/m/new2=1 "* ]] || { echo "selftest: unexpected assignment: $got" >&2; fail=1; }
	# Dropping one package from one shard fails the union check.
	MODULE_PREFIX=ex.com/m assign 3 "$tmp/list.txt" "$tmp/times.txt" | awk -F'\t' '$1 == 1 {print $2}' >"$tmp/x1.txt"
	MODULE_PREFIX=ex.com/m assign 3 "$tmp/list.txt" "$tmp/times.txt" | awk -F'\t' '$1 == 0 {print $2}' >"$tmp/x0.txt"
	MODULE_PREFIX=ex.com/m assign 3 "$tmp/list.txt" "$tmp/times.txt" | awk -F'\t' '$1 == 2 {print $2}' >"$tmp/x2.txt"
	head -n -1 "$tmp/x2.txt" >"$tmp/x2cut.txt"
	if "$tools/shard-tests.sh" check "$tmp/list.txt" "$tmp/x0.txt" "$tmp/x1.txt" "$tmp/x2cut.txt" >/dev/null 2>&1; then
		echo "selftest: a dropped package was not noticed" >&2
		fail=1
	fi
	# Result lines of a go test log.
	printf 'ok  \tex.com/m/a\t1.2s\n?   \tex.com/m/b\t[no test files]\nFAIL\tex.com/m/c\t0.3s\nFAIL\nok  \tex.com/m/d\t(cached)\n--- FAIL: TestX (0.00s)\n' >"$tmp/log.txt"
	[[ $(cmd_ran "$tmp/log.txt" | paste -sd' ') == "ex.com/m/a ex.com/m/b ex.com/m/c ex.com/m/d" ]] || { echo "selftest: ran parsed wrongly" >&2; fail=1; }
	((fail == 0)) || exit 1
	echo "shard-pkgs selftest: ok"
}

cmd=${1:-}
shift || true
case $cmd in
list) cmd_list ;;
shard) cmd_shard "$@" ;;
load) cmd_load "$@" ;;
ran) cmd_ran "$@" ;;
selftest) cmd_selftest ;;
*) die "usage: $0 list|shard|load|ran|selftest (see the header)" ;;
esac
