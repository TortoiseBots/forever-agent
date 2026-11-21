#!/usr/bin/env bash
# shard-tests splits the top-level tests of one Go package into shards for parallel CI jobs, and proves that no test
# was dropped on the way.
#
# A test name is any Test*, Fuzz* or Example* function that `go test -list` prints: Fuzz targets run their seeds
# under plain `go test`, and Examples run too. Benchmarks are left out, since `go test` does not run them without -bench.
# Sub-tests belong to their parent and run in the shard that owns it.
#
# Usage:
#   shard-tests.sh list PKG                  every top-level test name of PKG, sorted, one per line
#   shard-tests.sh shard I N LISTFILE        the names shard I of N (0-based) runs: sorted, round robin
#   shard-tests.sh regex I N LISTFILE        the same as a `go test -run` pattern: ^(A|B|...)$
#   shard-tests.sh ran LOGFILE               the top-level tests a `go test -v` log started, sorted
#   shard-tests.sh check LISTFILE RANFILE... fail unless the RANFILEs together hold exactly the names of LISTFILE,
#                                            each once
#   shard-tests.sh selftest                  checks the above on a small made-up list
#
# `go` is taken from $GO (default: go on PATH).
set -euo pipefail

GO=${GO:-go}

die() {
	echo "shard-tests: $*" >&2
	exit 1
}

# Keep only the lines that name a test function.
filter_names() {
	grep -E '^(Test|Fuzz|Example)[A-Za-z0-9_]*$' | LC_ALL=C sort -u
}

cmd_list() {
	(($# == 1)) || die "usage: list PKG"
	local out
	out=$("$GO" test -list '.*' "$1") || die "go test -list failed: $out"
	filter_names <<<"$out"
}

check_shard_args() {
	[[ $1 =~ ^[0-9]+$ && $2 =~ ^[1-9][0-9]*$ ]] || die "shard index and count must be numbers (count >= 1)"
	(($1 < $2)) || die "shard index $1 is not below the count $2"
	[[ -f $3 ]] || die "no such list file: $3"
}

cmd_shard() {
	(($# == 3)) || die "usage: shard I N LISTFILE"
	check_shard_args "$@"
	LC_ALL=C sort -u "$3" | awk -v i="$1" -v n="$2" '(NR - 1) % n == i'
}

cmd_regex() {
	(($# == 3)) || die "usage: regex I N LISTFILE"
	local names
	names=$(cmd_shard "$@")
	[[ -n $names ]] || die "shard $1 of $2 is empty"
	printf '^(%s)$\n' "$(paste -sd'|' <<<"$names")"
}

cmd_ran() {
	(($# == 1)) || die "usage: ran LOGFILE"
	[[ -f $1 ]] || die "no such log file: $1"
	# "=== RUN   TestX" for a top-level test, "=== RUN   TestX/sub" for a sub-test.
	sed -n 's/^=== RUN   //p' "$1" | grep -v '/' | filter_names
}

cmd_check() {
	(($# >= 2)) || die "usage: check LISTFILE RANFILE..."
	local list=$1
	shift
	[[ -f $list ]] || die "no such list file: $list"
	local f
	for f in "$@"; do [[ -f $f ]] || die "no such ran file: $f"; done
	local want got dup missing extra bad=0
	want=$(LC_ALL=C sort -u "$list")
	got=$(cat "$@" | LC_ALL=C sort)
	dup=$(uniq -d <<<"$got")
	missing=$(LC_ALL=C comm -23 <(cat <<<"$want") <(uniq <<<"$got"))
	extra=$(LC_ALL=C comm -13 <(cat <<<"$want") <(uniq <<<"$got"))
	if [[ -n $missing ]]; then
		echo "shard-tests: tests in no shard run ($(wc -l <<<"$missing")):" >&2
		sed 's/^/  /' <<<"$missing" >&2
		bad=1
	fi
	if [[ -n $extra ]]; then
		echo "shard-tests: tests that ran but are not in the full list ($(wc -l <<<"$extra")):" >&2
		sed 's/^/  /' <<<"$extra" >&2
		bad=1
	fi
	if [[ -n $dup ]]; then
		echo "shard-tests: tests that ran in more than one shard:" >&2
		sed 's/^/  /' <<<"$dup" >&2
		bad=1
	fi
	((bad == 0)) || exit 1
	echo "shard-tests: ok, $(wc -l <<<"$want") tests, each run exactly once across $# shard(s)"
}

cmd_selftest() {
	local self=${BASH_SOURCE[0]} tmp n i fail=0
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	expect_fail() {
		if "$@" >/dev/null 2>&1; then
			echo "selftest: expected a failure: $*" >&2
			fail=1
		fi
	}

	# Unsorted, with a duplicate and a few non-test lines the way `go test -list` prints them.
	cat >"$tmp/raw.txt" <<'LIST'
TestZeta
TestAlpha
ExampleThing
FuzzParse
TestBeta
TestGamma
TestAlpha
TestDelta
TestEpsilon
BenchmarkNope
ok  	example.com/pkg	0.01s
LIST
	filter_names <"$tmp/raw.txt" >"$tmp/all.txt"
	[[ $(wc -l <"$tmp/all.txt") == 8 ]] || { echo "selftest: filter kept the wrong lines" >&2; fail=1; }
	grep -q Benchmark "$tmp/all.txt" && { echo "selftest: a benchmark was kept" >&2; fail=1; }

	# For every shard count from 1 to 10 (more shards than tests included), the shards partition the list exactly.
	for n in 1 2 3 4 7 8 10; do
		local ranfiles=()
		for ((i = 0; i < n; i++)); do
			"$self" shard "$i" "$n" "$tmp/all.txt" >"$tmp/s$n-$i.txt"
			ranfiles+=("$tmp/s$n-$i.txt")
		done
		"$self" check "$tmp/all.txt" "${ranfiles[@]}" >/dev/null || { echo "selftest: $n shards do not cover the list" >&2; fail=1; }
		# Round robin keeps shard sizes within one of each other.
		local min=999 max=0 c
		for f in "${ranfiles[@]}"; do
			c=$(wc -l <"$f")
			((c < min)) && min=$c
			((c > max)) && max=$c
		done
		((max - min <= 1)) || { echo "selftest: $n shards are unbalanced ($min..$max)" >&2; fail=1; }
		# Dropping one name from one shard must fail the check; so must running one twice.
		local victim=${ranfiles[0]}
		if [[ -s $victim ]]; then
			grep -v "^$(head -n1 "$victim")\$" "$victim" >"$victim.cut" || true
			expect_fail "$self" check "$tmp/all.txt" "$victim.cut" "${ranfiles[@]:1}"
			expect_fail "$self" check "$tmp/all.txt" "${ranfiles[@]}" "$victim"
		fi
	done
	# A name that ran but is not in the full list fails too.
	echo TestSurprise >"$tmp/extra.txt"
	expect_fail "$self" check "$tmp/all.txt" "$tmp/s2-0.txt" "$tmp/s2-1.txt" "$tmp/extra.txt"

	# The regex is anchored and holds exactly the shard's names.
	[[ $("$self" regex 0 2 "$tmp/all.txt") == "^(ExampleThing|TestAlpha|TestDelta|TestGamma)\$" ]] ||
		{ echo "selftest: unexpected regex $("$self" regex 0 2 "$tmp/all.txt")" >&2; fail=1; }
	expect_fail "$self" regex 9 10 "$tmp/all.txt"
	expect_fail "$self" shard 2 2 "$tmp/all.txt"

	# `ran` keeps top-level tests only.
	cat >"$tmp/log.txt" <<'LOG'
=== RUN   TestAlpha
=== RUN   TestAlpha/sub_one
=== PAUSE TestAlpha/sub_one
=== RUN   FuzzParse
    --- PASS: TestAlpha (0.00s)
=== RUN   TestAlpha
LOG
	[[ $("$self" ran "$tmp/log.txt" | paste -sd' ') == "FuzzParse TestAlpha" ]] || { echo "selftest: ran parsed wrongly" >&2; fail=1; }

	((fail == 0)) || exit 1
	echo "shard-tests selftest: ok"
}

cmd=${1:-}
shift || true
case $cmd in
list) cmd_list "$@" ;;
shard) cmd_shard "$@" ;;
regex) cmd_regex "$@" ;;
ran) cmd_ran "$@" ;;
check) cmd_check "$@" ;;
selftest) cmd_selftest ;;
*) die "usage: $0 list|shard|regex|ran|check|selftest (see the header)" ;;
esac
