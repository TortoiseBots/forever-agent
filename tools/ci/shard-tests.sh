#!/usr/bin/env bash
# shard-tests splits the top-level tests of one Go package into shards for parallel CI jobs, and proves that no test
# was dropped on the way.
#
# A test name is any Test* or Fuzz* function that `go test -list` prints (fuzz targets run their seeds under plain
# `go test`). Benchmarks are left out, since `go test` does not run them without -bench. `go test -list` does not print
# Example functions, so `list` fails on a package that has one: a shard would never run it.
# Sub-tests belong to their parent and run in the shard that owns it.
#
# Usage:
#   shard-tests.sh list PKG                  every top-level test name of PKG, sorted, one per line; listed with -race,
#                                            like the race run, so a `//go:build race` file counts and a `!race` one does not
#   shard-tests.sh shard I N LISTFILE        the names shard I of N (0-based) runs: sorted, round robin
#   shard-tests.sh regex I N LISTFILE        the same as a `go test -run` pattern: ^(A|B|...)$. An empty shard prints
#                                            nothing and says so on stderr: the caller must not run go test then
#   shard-tests.sh ran LOGFILE               the top-level tests a `go test -v` log started, sorted; a test that started
#                                            twice is listed twice
#   shard-tests.sh check LISTFILE RANFILE... fail unless the RANFILEs together hold exactly the names of LISTFILE, each once
#   shard-tests.sh selftest                  checks the above on a made-up list and on a small module built with -race
#
# `go` is taken from $GO (default: go on PATH).
set -euo pipefail

GO=${GO:-go}
export GO
# The flags of the race run (.github/workflows/test.yml). The list is built with the same ones.
BUILD_FLAGS=(-race)

die() {
	echo "shard-tests: $*" >&2
	exit 1
}

# Keep the lines that name a test, a fuzz target or an example: the prefix, then letters, digits and underscores, Unicode
# included (\p{L} and \p{Nd} need the UTF-8 locale). No match is an empty list, not an error.
filter_names() {
	LC_ALL=C.UTF-8 grep -P '^(Test|Fuzz|Example)[\p{L}\p{Nd}_]*$' || true
}

cmd_list() {
	(($# == 1)) || die "usage: list PKG"
	local out names dir
	out=$("$GO" test "${BUILD_FLAGS[@]}" -list '.*' "$1") || die "go test -list failed: $out"
	names=$(filter_names <<<"$out" | LC_ALL=C sort -u)
	[[ -n $names ]] || die "$1 has no Test or Fuzz function"
	dir=$("$GO" list -f '{{.Dir}}' "$1")
	if grep -q -E '^func Example' "$dir"/*_test.go; then
		die "$1 has an Example function, which go test -list does not print: no shard could run it"
	fi
	printf '%s\n' "$names"
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
	if [[ -z $names ]]; then
		echo "shard-tests: shard $1 of $2 has no tests: nothing to run" >&2
		return 0
	fi
	printf '^(%s)$\n' "$(paste -sd'|' <<<"$names")"
}

cmd_ran() {
	(($# == 1)) || die "usage: ran LOGFILE"
	[[ -f $1 ]] || die "no such log file: $1"
	# "=== RUN   TestX" for a top-level test, "=== RUN   TestX/sub" for a sub-test. Not made unique: a test that started
	# twice is listed twice, and check fails on it.
	{ sed -n 's/^=== RUN   //p' "$1" | grep -v '/' || true; } | filter_names | LC_ALL=C sort
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
	[[ -n $want ]] || die "the full list $list is empty: there is nothing to check"
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
		echo "shard-tests: tests that ran more than once (in one shard or in two):" >&2
		sed 's/^/  /' <<<"$dup" >&2
		bad=1
	fi
	((bad == 0)) || exit 1
	echo "shard-tests: ok, $(wc -l <<<"$want") tests, each run exactly once across $# shard(s)"
}

cmd_selftest() {
	local self tmp n i f c min max victim got listed fail=0
	self=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN
	expect_fail() {
		if "$@" >/dev/null 2>&1; then
			echo "selftest: expected a failure: $*" >&2
			fail=1
		fi
	}

	# Unsorted, with a duplicate and the lines that are not names: summary lines, a benchmark, the testing package's
	# warning, and two names with Unicode letters (valid in Go: an upper-case letter or an underscore after "Test").
	printf '%s\n' TestZeta TestAlpha ExampleThing FuzzParse TestBeta TestGamma TestAlpha TestDelta TestEpsilon \
		'TestÜmlaut' 'Test_α' BenchmarkNope PASS 'testing: warning: no tests to run' >"$tmp/raw.txt"
	printf 'ok  \texample.com/pkg\t0.01s\n?   \texample.com/pkg\t[no test files]\n' >>"$tmp/raw.txt"
	filter_names <"$tmp/raw.txt" | LC_ALL=C sort -u >"$tmp/all.txt"
	got=$(paste -sd' ' "$tmp/all.txt")
	[[ $got == "ExampleThing FuzzParse TestAlpha TestBeta TestDelta TestEpsilon TestGamma TestZeta Test_α TestÜmlaut" ]] ||
		{ echo "selftest: filter kept the wrong names: $got" >&2; fail=1; }

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
	# An empty full list is an error, not an empty success.
	: >"$tmp/empty.txt"
	expect_fail "$self" check "$tmp/empty.txt" "$tmp/s2-0.txt"

	# The regex is anchored and holds exactly the shard's names, Unicode ones included.
	got=$("$self" regex 0 2 "$tmp/all.txt")
	[[ $got == '^(ExampleThing|TestAlpha|TestDelta|TestGamma|Test_α)$' ]] || { echo "selftest: unexpected regex $got" >&2; fail=1; }
	# An empty shard is a no-op: no regex on stdout (so no caller runs go test with an empty -run), a note on stderr, exit 0.
	got=$("$self" regex 10 11 "$tmp/all.txt" 2>"$tmp/empty.err") || { echo "selftest: an empty shard failed" >&2; fail=1; }
	[[ -z $got ]] && grep -q 'nothing to run' "$tmp/empty.err" ||
		{ echo "selftest: an empty shard printed a regex or no note" >&2; fail=1; }
	expect_fail "$self" regex 11 11 "$tmp/all.txt"
	expect_fail "$self" shard 2 2 "$tmp/all.txt"

	# `ran` keeps top-level tests only, and a test that started twice is listed twice (so check can fail on it).
	cat >"$tmp/log.txt" <<'LOG'
=== RUN   TestAlpha
=== RUN   TestAlpha/sub_one
=== PAUSE TestAlpha/sub_one
=== RUN   FuzzParse
    --- PASS: TestAlpha (0.00s)
=== RUN   TestAlpha
LOG
	got=$("$self" ran "$tmp/log.txt" | paste -sd' ')
	[[ $got == "FuzzParse TestAlpha TestAlpha" ]] || { echo "selftest: ran parsed wrongly: $got" >&2; fail=1; }
	printf '=== RUN   TestAlpha\n=== RUN   TestBeta\n=== RUN   TestAlpha\n' >"$tmp/dup.log"
	printf 'TestAlpha\nTestBeta\n' >"$tmp/two.txt"
	"$self" ran "$tmp/dup.log" >"$tmp/dup-ran.txt"
	expect_fail "$self" check "$tmp/two.txt" "$tmp/dup-ran.txt"
	# A log with only sub-tests, or with no test at all, gives no names and no failure.
	printf '=== RUN   TestX/sub\nPASS\n' >"$tmp/none.log"
	"$self" ran "$tmp/none.log" >"$tmp/none-ran.txt" || { echo "selftest: ran failed on a log without top-level tests" >&2; fail=1; }
	[[ ! -s $tmp/none-ran.txt ]] || { echo "selftest: ran listed a name for a log without top-level tests" >&2; fail=1; }

	# The list is built with the race run's flags, in a small module: a `//go:build race` file is listed, a `!race`
	# file is not, a package without tests is refused, and an Example function is refused (go test -list omits it).
	mkdir -p "$tmp/mod/pkg" "$tmp/mod/notests"
	printf 'module example.com/race\n\ngo 1.22\n' >"$tmp/mod/go.mod"
	printf 'package pkg\n\nimport "testing"\n\nfunc TestPlain(t *testing.T) {}\n' >"$tmp/mod/pkg/plain_test.go"
	printf '//go:build race\n\npackage pkg\n\nimport "testing"\n\nfunc TestOnlyRace(t *testing.T) {}\n' >"$tmp/mod/pkg/race_test.go"
	printf '//go:build !race\n\npackage pkg\n\nimport "testing"\n\nfunc TestNoRace(t *testing.T) {}\n' >"$tmp/mod/pkg/norace_test.go"
	printf 'package notests\n' >"$tmp/mod/notests/doc.go"
	listed=$(cd "$tmp/mod" && "$self" list ./pkg) || { echo "selftest: list failed in the race module" >&2; fail=1; }
	[[ $(paste -sd' ' <<<"$listed") == "TestOnlyRace TestPlain" ]] || { echo "selftest: race module listed: $listed" >&2; fail=1; }
	expect_fail bash -c "cd '$tmp/mod' && '$self' list ./notests"
	printf 'package pkg\n\nimport "fmt"\n\nfunc X() int { return 1 }\n\n// Output: x\nfunc ExampleX() { fmt.Println("x") }\n' >"$tmp/mod/pkg/example_test.go"
	(cd "$tmp/mod" && "$self" list ./pkg) >/dev/null 2>"$tmp/ex.err" && { echo "selftest: list accepted an Example function" >&2; fail=1; }
	grep -q 'Example function' "$tmp/ex.err" || { echo "selftest: list refused without naming the Example function" >&2; fail=1; }

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
