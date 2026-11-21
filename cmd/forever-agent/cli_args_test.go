package main

import (
	"io"
	"testing"
	"time"
)

// Every documented flag order for teardown, lease and slot setup parses to
// the same options: flags work before, between or after the positionals,
// values come from `--flag value` or `--flag=value`, and boolean flags take
// no value. Restoring the old leading-only splitPositionals breaks every
// flags-first row below.
func checkParse[T comparable](t *testing.T, name string, got, want T, code, wantCode int) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("%s: exit %d, want %d", name, code, wantCode)
	}
	if code == exitOK && got != want {
		t.Fatalf("%s: options %+v, want %+v", name, got, want)
	}
}

func TestTeardownFlagOrderSameOptions(t *testing.T) {
	full := teardownOptions{dir: "somedir", keepLease: true, releaseAnyway: true, gm: "keep", dryRun: true, asJSON: true, slotsPath: "somefile"}
	dry := teardownOptions{dir: "somedir", gm: "off", dryRun: true}
	for _, r := range []struct {
		name string
		args []string
		want teardownOptions
		code int
	}{
		{"flags after dir", []string{"somedir", "--keep-lease", "--release-anyway", "--gm", "keep", "--dry-run", "--json", "--slots", "somefile"}, full, exitOK},
		{"flags before dir", []string{"--keep-lease", "--release-anyway", "--gm", "keep", "--dry-run", "--json", "--slots", "somefile", "somedir"}, full, exitOK},
		{"flags around dir", []string{"--keep-lease", "somedir", "--release-anyway", "--gm", "keep", "--dry-run", "--json", "--slots", "somefile"}, full, exitOK},
		{"equals form", []string{"--keep-lease", "--release-anyway", "--gm=keep", "--dry-run", "--json", "--slots=somefile", "somedir"}, full, exitOK},
		{"bool equals form", []string{"--dry-run=true", "--json=true", "--keep-lease=true", "--release-anyway=true", "--gm", "keep", "--slots", "somefile", "somedir"}, full, exitOK},
		// The exact reported failures: each must parse, not print usage.
		{"dry-run before dir", []string{"--dry-run", "somedir"}, dry, exitOK},
		{"dry-run json before dir", []string{"--dry-run", "--json", "somedir"}, teardownOptions{dir: "somedir", gm: "off", dryRun: true, asJSON: true}, exitOK},
		{"dry-run slots before dir", []string{"--dry-run", "--slots", "somefile", "somedir"}, teardownOptions{dir: "somedir", gm: "off", dryRun: true, slotsPath: "somefile"}, exitOK},
		{"dry-run equals before dir", []string{"--dry-run=true", "somedir"}, dry, exitOK},
		{"dry-run after dir", []string{"somedir", "--dry-run"}, dry, exitOK},
		{"no dir", []string{"--dry-run"}, teardownOptions{}, exitUsage},
		{"two dirs", []string{"a", "b", "--dry-run"}, teardownOptions{}, exitUsage},
		{"bad gm", []string{"--gm", "sometimes", "somedir"}, teardownOptions{}, exitUsage},
	} {
		got, code := parseTeardownArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}
func TestRPCFlagOrderSameOptions(t *testing.T) {
	full := rpcOptions{dir: "somedir", method: "status", params: `{"a":1}`, timeout: 30 * time.Second}
	fullRaw := rpcOptions{dir: "somedir", method: "status", params: `{"a":1}`, timeout: 5 * time.Second, raw: true}
	for _, r := range []struct {
		name string
		args []string
		want rpcOptions
		code int
	}{
		{"positionals first", []string{"somedir", "status", `{"a":1}`}, full, exitOK},
		{"flags first", []string{"--timeout", "5s", "somedir", "status", `{"a":1}`}, rpcOptions{dir: "somedir", method: "status", params: `{"a":1}`, timeout: 5 * time.Second}, exitOK},
		{"flags last", []string{"somedir", "status", `{"a":1}`, "--timeout", "5s"}, rpcOptions{dir: "somedir", method: "status", params: `{"a":1}`, timeout: 5 * time.Second}, exitOK},
		{"flags around", []string{"somedir", "--timeout", "5s", "--raw", "status", `{"a":1}`}, fullRaw, exitOK},
		{"equals form", []string{"--timeout=5s", "--raw", "somedir", "status", `{"a":1}`}, fullRaw, exitOK},
		{"stdin params", []string{"somedir", "status", "-"}, rpcOptions{dir: "somedir", method: "status", paramsFromStdin: true, timeout: 30 * time.Second}, exitOK},
		{"no method", []string{"somedir"}, rpcOptions{}, exitUsage},
		{"no dir", []string{}, rpcOptions{}, exitUsage},
		{"bad timeout", []string{"--timeout", "bogus", "somedir", "status"}, rpcOptions{}, exitUsage},
		{"zero timeout", []string{"--timeout", "0s", "somedir", "status"}, rpcOptions{}, exitUsage},
	} {
		got, code := parseRPCArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseAcquireFlagOrderSameOptions(t *testing.T) {
	full := leaseAcquireOptions{server: "test1", slotID: "4", holder: "w-a", ttlMin: 5, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseAcquireOptions
		code int
	}{
		{"server first", []string{"test1", "--slot", "4", "--holder", "w-a", "--ttl", "5", "--slots", "somefile"}, full, exitOK},
		{"server last", []string{"--slot", "4", "--holder", "w-a", "--ttl", "5", "--slots", "somefile", "test1"}, full, exitOK},
		{"server between", []string{"--slot", "4", "test1", "--holder", "w-a", "--ttl", "5", "--slots", "somefile"}, full, exitOK},
		{"equals form", []string{"--slot=4", "--holder=w-a", "--ttl=5", "--slots=somefile", "test1"}, full, exitOK},
		{"any first", []string{"--any", "--holder", "w-a", "test1"}, leaseAcquireOptions{server: "test1", any: true, holder: "w-a", ttlMin: 180}, exitOK},
		{"no server", []string{"--slot", "4", "--holder", "w-a"}, leaseAcquireOptions{}, exitUsage},
	} {
		got, code := parseLeaseAcquireArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseRenewFlagOrderSameOptions(t *testing.T) {
	full := leaseRenewOptions{server: "test1", slotID: "1", holder: "w-a", leaseID: "lid-9", ttlMin: 7, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseRenewOptions
		code int
	}{
		{"positionals first", []string{"test1", "1", "--holder", "w-a", "--lease-id", "lid-9", "--ttl", "7", "--slots", "somefile"}, full, exitOK},
		{"positionals last", []string{"--holder", "w-a", "--lease-id", "lid-9", "--ttl", "7", "--slots", "somefile", "test1", "1"}, full, exitOK},
		{"positionals split", []string{"test1", "--holder", "w-a", "--lease-id", "lid-9", "1", "--ttl", "7", "--slots", "somefile"}, full, exitOK},
		{"equals form", []string{"--holder=w-a", "--lease-id=lid-9", "--ttl=7", "--slots=somefile", "test1", "1"}, full, exitOK},
		{"one positional", []string{"test1", "--holder", "w-a", "--lease-id", "lid-9"}, leaseRenewOptions{}, exitUsage},
	} {
		got, code := parseLeaseRenewArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseReleaseFlagOrderSameOptions(t *testing.T) {
	plain := leaseReleaseOptions{server: "test1", slotID: "1", holder: "w-a", leaseID: "lid-9", slotsPath: "somefile"}
	forced := leaseReleaseOptions{server: "test1", slotID: "1", holder: "w-a", force: true, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseReleaseOptions
		code int
	}{
		{"positionals first", []string{"test1", "1", "--holder", "w-a", "--lease-id", "lid-9", "--slots", "somefile"}, plain, exitOK},
		{"positionals last", []string{"--holder", "w-a", "--lease-id", "lid-9", "--slots", "somefile", "test1", "1"}, plain, exitOK},
		{"positionals split", []string{"test1", "--holder", "w-a", "1", "--lease-id", "lid-9", "--slots", "somefile"}, plain, exitOK},
		{"equals form", []string{"--holder=w-a", "--lease-id=lid-9", "--slots=somefile", "test1", "1"}, plain, exitOK},
		{"force before positionals", []string{"--force", "--holder", "w-a", "--slots", "somefile", "test1", "1"}, forced, exitOK},
		{"force equals before positionals", []string{"--force=true", "--holder=w-a", "--slots=somefile", "test1", "1"}, forced, exitOK},
	} {
		got, code := parseLeaseReleaseArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseListFlagOrderSameOptions(t *testing.T) {
	full := leaseListOptions{server: "test1", asJSON: true, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseListOptions
		code int
	}{
		{"server first", []string{"test1", "--json", "--slots", "somefile"}, full, exitOK},
		{"server last", []string{"--json", "--slots", "somefile", "test1"}, full, exitOK},
		{"equals form", []string{"--json=true", "--slots=somefile", "test1"}, full, exitOK},
		{"no server", []string{"--json", "--slots", "somefile"}, leaseListOptions{asJSON: true, slotsPath: "somefile"}, exitOK},
		{"bare", []string{}, leaseListOptions{}, exitOK},
		{"two servers", []string{"a", "b"}, leaseListOptions{}, exitUsage},
	} {
		got, code := parseLeaseListArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestSlotSetupFlagOrderSameOptions(t *testing.T) {
	full := slotSetupOptions{server: "test1", slot: "1", holder: "w-a", dir: "D", binary: "B", dungeonsDir: "G", mmapsDir: "M", dungeonsFrom: "P", character: "C", slotsPath: "F"}
	for _, r := range []struct {
		name string
		args []string
		want slotSetupOptions
		code int
	}{
		{"positionals first", []string{"test1", "1", "--holder", "w-a", "--dir", "D", "--binary", "B", "--dungeons-dir", "G", "--mmaps-dir", "M", "--dungeons-from", "P", "--character", "C", "--slots", "F"}, full, exitOK},
		{"positionals last", []string{"--holder", "w-a", "--dir", "D", "--binary", "B", "--dungeons-dir", "G", "--mmaps-dir", "M", "--dungeons-from", "P", "--character", "C", "--slots", "F", "test1", "1"}, full, exitOK},
		{"positionals split", []string{"test1", "--holder", "w-a", "1", "--dir", "D", "--binary", "B", "--dungeons-dir", "G", "--mmaps-dir", "M", "--dungeons-from", "P", "--character", "C", "--slots", "F"}, full, exitOK},
		{"equals form", []string{"--holder=w-a", "--dir=D", "--binary=B", "--dungeons-dir=G", "--mmaps-dir=M", "--dungeons-from=P", "--character=C", "--slots=F", "test1", "1"}, full, exitOK},
		{"forever", []string{"--forever", "test1", "1", "--holder", "w-a", "--slots", "F"}, slotSetupOptions{server: "test1", slot: "1", holder: "w-a", slotsPath: "F", forever: true}, exitOK},
		{"no holder", []string{"test1", "1", "--dir", "D"}, slotSetupOptions{}, exitUsage},
	} {
		got, code := parseSlotSetupArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}
