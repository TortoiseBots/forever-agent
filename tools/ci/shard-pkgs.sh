#!/usr/bin/env bash
# shard-pkgs splits the packages of the module into shards for parallel CI jobs, by their recorded test time.
#
# The packages that shard-tests.sh splits by test name (SHARDED_BY_NAME below) are left out: they are too long to be
# one unit. Every other package from `go list ./...` goes to exactly one shard: greedy longest-first bin packing on the
# seconds in tools/ci/pkg-times.txt. A package missing from that file (a new one) weighs DEFAULT_WEIGHT seconds and goes
# to the lightest shard, so it is never dropped; a name in the file that no longer exists is ignored. A package that has
# tests but no line in that file is warned about on stderr by `shard` and `load`.
#
# Usage:
#   shard-pkgs.sh list                        the packages the shards share, sorted, full import paths; listed with -race,
#                                             like the race run
#   shard-pkgs.sh shard I N LISTFILE TIMES    the packages shard I of N (0-based) runs, one per line. An empty shard prints
#                                             nothing and says so on stderr: the caller must not run go test then
#   shard-pkgs.sh load N LISTFILE TIMES       the estimated seconds per shard (to see whether they drifted)
#   shard-pkgs.sh ran LOGFILE                 the packages a `go test` log shows a result line for, sorted. Not counted: a
#                                             package whose test binary ran no test ("[no tests to run]") or did not build
#   shard-pkgs.sh selftest                    checks the above on a made-up list and on a small module built with -race
# The union check is `shard-tests.sh check LISTFILE RANFILE...`, the same as for the test-name shards.
#
# `go` is taken from $GO (default: go on PATH). SHARDED_PKGS (space-separated) replaces SHARDED_BY_NAME in the self-test.
set -euo pipefail

GO=${GO:-go}
export GO
read -ra SHARDED_BY_NAME <<<"${SHARDED_PKGS:-cmd/forever-agent internal/forever/world}"
DEFAULT_WEIGHT=1
# The flags of the race run (.github/workflows/test.yml). The list is built with the same ones.
BUILD_FLAGS=(-race)

die() {
	echo "shard-pkgs: $*" >&2
	exit 1
}

module() { "$GO" list -m; }

cmd_list() {
	local mod out p names skip=()
	mod=$(module)
	for p in "${SHARDED_BY_NAME[@]}"; do skip+=("$mod/$p"); done
	out=$("$GO" list "${BUILD_FLAGS[@]}" ./...) || die "go list failed"
	# A name in SHARDED_BY_NAME that is gone would silently shard nothing: say so.
	for p in "${skip[@]}"; do
		grep -qxF "$p" <<<"$out" || die "$p is in SHARDED_BY_NAME but not in go list ./..."
	done
	names=$({ grep -vxF "$(printf '%s\n' "${skip[@]}")" <<<"$out" || true; } | LC_ALL=C sort -u)
	[[ -n $names ]] || die "go list ./... has no package besides the sharded ones"
	printf '%s\n' "$names"
}

# assign N LISTFILE TIMES: print "shard<TAB>package<TAB>weight" for every package, greedy longest-first.
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

# untimed MOD LISTFILE TIMES TESTEDFILE: the packages of LISTFILE that TESTEDFILE (import paths) lists as having tests, and
# that TIMES (paths relative to MOD) has no line for.
untimed() {
	LC_ALL=C comm -12 <(LC_ALL=C sort -u "$2") <(LC_ALL=C sort -u "$4") |
		LC_ALL=C comm -23 - <(awk -v mod="$1" '$1 !~ /^#/ && NF >= 2 {print mod "/" $1}' "$3" | LC_ALL=C sort -u)
}

# warn_untimed MOD LISTFILE TIMES: a package with tests that TIMES does not list counts as DEFAULT_WEIGHT seconds, which
# skews the shards, so say so on stderr.
warn_untimed() {
	local tested p
	tested=$("$GO" list "${BUILD_FLAGS[@]}" -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...) ||
		die "go list failed"
	while IFS= read -r p; do
		if [[ -n $p ]]; then
			echo "shard-pkgs: warning: $p has tests but no line in $3, so it counts as $DEFAULT_WEIGHT s" >&2
		fi
	done < <(untimed "$1" "$2" "$3" <(printf '%s\n' "$tested"))
}

cmd_shard() {
	(($# == 4)) || die "usage: shard I N LISTFILE TIMES"
	[[ $1 =~ ^[0-9]+$ ]] && (($1 < $2)) || die "shard index $1 is not below the count $2"
	local mod names
	mod=$(module)
	warn_untimed "$mod" "$3" "$4"
	names=$(MODULE_PREFIX=$mod assign "$2" "$3" "$4" | awk -F'\t' -v i="$1" '$1 == i {print $2}' | LC_ALL=C sort)
	if [[ -n $names ]]; then
		printf '%s\n' "$names"
	else
		echo "shard-pkgs: shard $1 of $2 has no packages: nothing to run" >&2
	fi
}

cmd_load() {
	(($# == 3)) || die "usage: load N LISTFILE TIMES"
	local mod
	mod=$(module)
	warn_untimed "$mod" "$2" "$3"
	MODULE_PREFIX=$mod assign "$1" "$2" "$3" | awk -F'\t' -v n="$1" '{c[$1]++; s[$1] += $3} END {for (i = 0; i < n; i++) printf "shard %d: %d packages, about %.0f s\n", i, c[i] + 0, s[i] + 0}'
}

cmd_ran() {
	(($# == 1)) || die "usage: ran LOGFILE"
	[[ -f $1 ]] || die "no such log file: $1"
	local mod=${MODULE_PREFIX:-}
	[[ -n $mod ]] || mod=$(module)
	# The package result lines of `go test` are tab-separated: "ok  <TAB>PKG<TAB>TIME", "FAIL<TAB>PKG<TAB>TIME" and
	# "?   <TAB>PKG<TAB>[no test files]". Only this module's packages count, and not one whose test binary ran no test
	# ("[no tests to run]") or never built ("FAIL<TAB>PKG [build failed]"): those did not run their tests.
	awk -F'\t' -v mod="$mod" '
		($1 == "ok  " || $1 == "FAIL" || $1 == "?   ") && $0 !~ /\[(no tests to run|build failed|setup failed)\]/ {
			pkg = $2
			sub(/ .*/, "", pkg)
			if (pkg == mod || index(pkg, mod "/") == 1) print pkg
		}' "$1" | LC_ALL=C sort
}

cmd_selftest() {
	local self tools tmp n i got listed fail=0
	self=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")
	tools=$(dirname "$self")
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	# The functions need a module path; the self-test feeds assign directly with MODULE_PREFIX.
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
	# A tested package with no line in the times file is reported; a listed package that has a line is not.
	printf 'ex.com/m/a\nex.com/m/new1\nex.com/m/zz\n' >"$tmp/tested.txt"
	got=$(untimed ex.com/m "$tmp/list.txt" "$tmp/times.txt" "$tmp/tested.txt" | paste -sd' ')
	[[ $got == "ex.com/m/new1" ]] || { echo "selftest: untimed gave: $got" >&2; fail=1; }

	# Result lines of a go test log, for the module ex.com/m. Counted: "ok" and "FAIL" lines with a time, and "?" lines.
	# Not counted: a [no tests to run] line, a build failure, another module's package and a near-name's package.
	printf 'ok  \tex.com/m/a\t1.2s\n?   \tex.com/m/b\t[no test files]\nFAIL\tex.com/m/c\t0.3s\nFAIL\nok  \tex.com/m/d\t(cached)\n--- FAIL: TestX (0.00s)\n' >"$tmp/log.txt"
	printf 'ok  \tex.com/m/e\t0.01s [no tests to run]\nFAIL\tex.com/m/f [build failed]\nok  \texample.org/other\t0.1s\nok  \tex.com/mx/g\t0.1s\nok  \tex.com/m/h\t0.1s\n' >>"$tmp/log.txt"
	got=$(MODULE_PREFIX=ex.com/m "$self" ran "$tmp/log.txt" | paste -sd' ')
	[[ $got == "ex.com/m/a ex.com/m/b ex.com/m/c ex.com/m/d ex.com/m/h" ]] || { echo "selftest: ran parsed wrongly: $got" >&2; fail=1; }
	# A package with two result lines is listed twice, so check can fail on it.
	printf 'ok  \tex.com/m/a\t0.1s\nok  \tex.com/m/a\t0.1s\n' >"$tmp/dup.txt"
	got=$(MODULE_PREFIX=ex.com/m "$self" ran "$tmp/dup.txt" | paste -sd' ')
	[[ $got == "ex.com/m/a ex.com/m/a" ]] || { echo "selftest: a repeated package was not listed twice: $got" >&2; fail=1; }
	# A log with no result line gives no names and no failure.
	printf 'PASS\n' >"$tmp/none.txt"
	MODULE_PREFIX=ex.com/m "$self" ran "$tmp/none.txt" >"$tmp/none-ran.txt" || { echo "selftest: ran failed on a log without results" >&2; fail=1; }
	[[ ! -s $tmp/none-ran.txt ]] || { echo "selftest: ran listed something for a log without results" >&2; fail=1; }

	# In a small module built with -race: a package whose only file is `//go:build race` is listed, one whose only file
	# is `!race` is not, and the sharded package is left out. A package with tests that the times file lacks is warned
	# about, a package without tests is not, and an empty shard prints nothing, says so and exits 0.
	mkdir -p "$tmp/mod/pkg" "$tmp/mod/plain" "$tmp/mod/racepkg" "$tmp/mod/normalpkg"
	printf 'module example.com/race\n\ngo 1.22\n' >"$tmp/mod/go.mod"
	printf 'package pkg\n\nimport "testing"\n\nfunc TestPkg(t *testing.T) {}\n' >"$tmp/mod/pkg/pkg_test.go"
	printf 'package plain\n\nfunc Plain() {}\n' >"$tmp/mod/plain/plain.go"
	printf 'package plain\n\nimport "testing"\n\nfunc TestPlain(t *testing.T) {}\n' >"$tmp/mod/plain/plain_test.go"
	printf '//go:build race\n\npackage racepkg\n' >"$tmp/mod/racepkg/race.go"
	printf '//go:build !race\n\npackage normalpkg\n' >"$tmp/mod/normalpkg/normal.go"
	got=$(cd "$tmp/mod" && SHARDED_PKGS=pkg "$self" list | paste -sd' ') || { echo "selftest: list failed in the race module" >&2; fail=1; }
	[[ $got == "example.com/race/plain example.com/race/racepkg" ]] || { echo "selftest: race module listed: $got" >&2; fail=1; }
	printf 'example.com/race/plain\nexample.com/race/racepkg\n' >"$tmp/rlist.txt"
	printf 'racepkg 1\n' >"$tmp/rtimes.txt"
	(cd "$tmp/mod" && "$self" shard 0 1 "$tmp/rlist.txt" "$tmp/rtimes.txt") >"$tmp/sh0.txt" 2>"$tmp/sh0.err" || { echo "selftest: shard failed in the race module" >&2; fail=1; }
	[[ $(paste -sd' ' "$tmp/sh0.txt") == "example.com/race/plain example.com/race/racepkg" ]] || { echo "selftest: shard 0 of 1 was: $(cat "$tmp/sh0.txt")" >&2; fail=1; }
	grep -q 'warning: example.com/race/plain has tests but no line' "$tmp/sh0.err" || { echo "selftest: no warning for a tested package without a time" >&2; fail=1; }
	if grep -q 'racepkg has tests' "$tmp/sh0.err"; then echo "selftest: a package without tests was warned about" >&2; fail=1; fi
	(cd "$tmp/mod" && "$self" shard 2 3 "$tmp/rlist.txt" "$tmp/rtimes.txt") >"$tmp/sh2.txt" 2>"$tmp/sh2.err" || { echo "selftest: an empty shard failed" >&2; fail=1; }
	[[ ! -s $tmp/sh2.txt ]] && grep -q 'nothing to run' "$tmp/sh2.err" || { echo "selftest: an empty shard printed packages or no note" >&2; fail=1; }
	got=$(cd "$tmp/mod" && "$self" load 3 "$tmp/rlist.txt" "$tmp/rtimes.txt" 2>/dev/null | wc -l)
	[[ $got == 3 ]] || { echo "selftest: load printed $got lines for 3 shards" >&2; fail=1; }

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
