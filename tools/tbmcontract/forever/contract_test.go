package forever_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tbmcontract "github.com/pjerra/forever-agent/contract/tbm"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
)

func requireAddonAndLua(t *testing.T) string {
	t.Helper()
	strict := os.Getenv("TBM_CONTRACT_STRICT") == "1"
	skipOrFail := func(why string) {
		if strict {
			t.Fatal(why)
		}
		t.Skip(why)
	}
	if _, err := exec.LookPath("lua5.1"); err != nil {
		skipOrFail("tbm-forever-contract: SKIPPED, lua5.1 not found (apt install lua5.1)")
	}
	cmd := exec.Command("sh", "./fetch.sh")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if os.Getenv("TBFM_ADDON_DIR") != "" || strict {
			t.Fatalf("fetch TBFM: %s", msg)
		}
		t.Skipf("tbm-forever-contract: SKIPPED, %s", msg)
	}
	return strings.TrimSpace(stdout.String())
}

func writeCaseFiles(t *testing.T, cases []tbmproto.Case) string {
	t.Helper()
	dir := t.TempDir()
	for _, c := range cases {
		data, err := fs.ReadFile(tbmcontract.V1, "v1/cases/"+c.Name+".case")
		if err != nil {
			t.Fatalf("read embedded case %s: %v", c.Name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, c.Name+".case"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestForeverContract(t *testing.T) {
	addonDir := requireAddonAndLua(t)
	cases, err := tbmproto.LoadCases(tbmcontract.V1, "v1/cases")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 28 {
		t.Fatalf("want 28 v1 cases, got %d", len(cases))
	}
	caseDir := writeCaseFiles(t, cases)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			out, err := exec.Command("lua5.1", "contract.lua", addonDir, filepath.Join(caseDir, c.Name+".case")).CombinedOutput()
			if err != nil {
				t.Fatalf("contract.lua %s failed (%v):\n%s", c.Name, err, strings.TrimSpace(string(out)))
			}
		})
	}
}

func TestDriverRejectsBrokenCase(t *testing.T) {
	addonDir := requireAddonAndLua(t)
	cases := map[string]string{
		"broken-version": "case broken-version\nrecv system TBM:VERSION|v0.1.0-forever\nexpect hello.version wrong-version\n",
		"broken-roster":  "case broken-roster\nrecv system TBM:ROSTER_BEGIN|2\nrecv system TBM:ROSTER|101|Alpha|1|online|1|Elwynn|-|Warrior\nrecv system TBM:ROSTER_END\nexpect roster.committed true\n",
	}
	dir := t.TempDir()
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".case")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("lua5.1", "contract.lua", addonDir, p).CombinedOutput()
			if err == nil {
				t.Fatalf("expected broken case %s to fail, got pass:\n%s", name, string(out))
			}
			if !strings.Contains(string(out), "MISMATCH") {
				t.Fatalf("expected MISMATCH output on %s, got:\n%s", name, string(out))
			}
		})
	}
}
