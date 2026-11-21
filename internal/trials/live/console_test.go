package live

// The bench setup's optional console backend: commands run before provisioning, a failing command makes the setup
// row fail, and a configured command with no backend fails instead of being skipped.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeConsole struct {
	sent []string
	err  error
}

func (c *fakeConsole) Run(_ context.Context, command string) (string, error) {
	c.sent = append(c.sent, command)
	if c.err != nil {
		return "", c.err
	}
	return "Boss id 0 state is now set to 3 (DONE).\n", nil
}

func TestConsoleRow(t *testing.T) {
	if row := consoleRow(context.Background(), nil, nil); row != nil {
		t.Fatalf("no commands: %v", row)
	}
	console := &fakeConsole{}
	row := consoleRow(context.Background(), console, []string{".instance setbossstate 0 3", ".saveall"})
	if row == nil || row["id"] != "server_console" || row["ok"] != true {
		t.Fatalf("row %v", row)
	}
	if len(console.sent) != 2 || console.sent[0] != ".instance setbossstate 0 3" {
		t.Fatalf("console saw %v", console.sent)
	}
	list, _ := row["value"].([]any)
	if len(list) != 2 {
		t.Fatalf("value %v", row["value"])
	}
	first, _ := list[0].(map[string]any)
	if first["text"] != "Boss id 0 state is now set to 3 (DONE)." {
		t.Fatalf("first command %v", first)
	}

	// A failing command makes the row fail with the command and the error; no later command runs.
	failing := &fakeConsole{err: errors.New("the server refused the credentials (HTTP 401)")}
	row = consoleRow(context.Background(), failing, []string{".saveall", ".pinfo"})
	if row["ok"] != false || !strings.Contains(row["evidence"].(string), ".saveall") ||
		!strings.Contains(row["evidence"].(string), "HTTP 401") {
		t.Fatalf("failing row %v", row)
	}
	if len(failing.sent) != 1 {
		t.Fatalf("a failed setup ran %d commands", len(failing.sent))
	}

	// A configured command with no backend is invalid, never a silent skip.
	row = consoleRow(context.Background(), nil, []string{".saveall"})
	if row["ok"] != false || !strings.Contains(row["evidence"].(string), "no SOAP backend") {
		t.Fatalf("no-backend row %v", row)
	}
}

// TestServerExtSoapBlock: the slots file's `forever` block carries the SOAP endpoint as data (password file or
// env name), and the setup commands, without the password itself.
func TestServerExtSoapBlock(t *testing.T) {
	var doc struct {
		Forever map[string]ServerExt `json:"forever"`
	}
	raw := []byte(`{"forever":{"s1":{"soap":{"host":"127.0.0.1","port":7878,"user":"console",
		"password_env":"SOAP_TEST_PW","timeout_ms":4000},"soap_setup":[".saveall"]}}}`)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ext := doc.Forever["s1"]
	if ext.SOAP == nil || ext.SOAP.User != "console" || ext.SOAP.PasswordEnv != "SOAP_TEST_PW" || ext.SOAP.TimeoutMS != 4000 {
		t.Fatalf("soap block %+v", ext.SOAP)
	}
	if len(ext.SOAPSetup) != 1 || ext.SOAPSetup[0] != ".saveall" {
		t.Fatalf("setup %v", ext.SOAPSetup)
	}
}
