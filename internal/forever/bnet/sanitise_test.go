package bnet

// The sanitiser tests: every field the server controls is poisoned with control bytes, an ANSI escape, a one-megabyte
// string and a token-like run, and every piece of text that reaches a sink (the error, the log callback the command
// copies to stdout, and Result.Summary) is checked for printable ASCII, a length bound and the token itself.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// maxOutputLine is the longest line any of these sinks may produce: the fixed prose of a step line plus one
// sanitised field (maxServerText bytes) fits well inside it.
const maxOutputLine = 400

func hostilePayloads() []struct{ name, text string } {
	return []struct{ name, text string }{
		{"control", "\x00\x01\x1b[31mred\x07 tail"},
		{"ansi", "\x1b[2J\x1b[1;31mhostile"},
		{"one-megabyte", strings.Repeat("A", 1<<20)},
		{"token", strings.Repeat("0123456789ABCDEF", 4)},
	}
}

// checkCleanOutput asserts the three properties a sink must keep for server text: printable ASCII plus newlines, no
// line over maxOutputLine, and no part of the poisoned payload that a sanitiser is meant to remove.
func checkCleanOutput(t *testing.T, label, text string, payloads ...string) {
	t.Helper()
	if text == "" {
		t.Fatalf("%s: no text at all", label)
	}
	for i := range len(text) {
		c := text[i]
		if c == '\n' {
			continue
		}
		if c < 0x20 || c > 0x7E {
			lo := max(0, i-40)
			t.Fatalf("%s: output carries byte %#x at %d: %q", label, c, i, text[lo:min(len(text), i+40)])
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if len(line) > maxOutputLine {
			t.Fatalf("%s: a line is %d bytes: %q", label, len(line), line[:maxOutputLine])
		}
	}
	for _, payload := range payloads {
		if strings.Contains(text, payload) {
			t.Fatalf("%s: the poisoned payload survived: %q", label, payload)
		}
	}
}

// TestServerTextIsSanitisedInEverySink drives Login against the fake with one hostile field at a time. The log
// callback is formatted exactly as cmd/forever-agent/forever_login.go prints it, so the checked text is the
// command's stdout; the error and Result.Summary are the other two sinks.
func TestServerTextIsSanitisedInEverySink(t *testing.T) {
	fields := []string{
		"form_type", "srp_url", "hash_function", "error_code", "error_message", "next_url",
		"authentication_state", "login_result_json", "realm_name", "sub_region", "ciid", "world_ip",
	}
	// these fields are logged (not refused), so the login has to complete and the token has to be redacted on the way
	completes := map[string]bool{"realm_name": true, "sub_region": true, "ciid": true, "world_ip": true}

	for _, field := range fields {
		for _, payload := range hostilePayloads() {
			t.Run(field+"/"+payload.name, func(t *testing.T) {
				f := newFakeLogin(t)
				defer f.close()
				f.hostile = map[string]string{field: payload.text}
				if field == "srp_url" {
					// a path only reaches the log if it still ends in the form's path; the payload sits in front
					f.hostile[field] = "https://" + fakeServerName + "/" + payload.text + srpPath
				}

				var out strings.Builder
				opts := f.options()
				opts.Logf = func(format string, args ...any) {
					out.WriteString(fmt.Sprintf(format, args...))
					out.WriteByte('\n')
				}
				result, err := Login(context.Background(), opts)

				switch {
				case completes[field] && payload.name == "token":
					if err != nil {
						t.Fatalf("the login stopped on a field it only logs: %v", err)
					}
					if result == nil {
						t.Fatal("no result")
					}
					out.WriteString(result.Summary())
					out.WriteByte('\n')
				case err != nil:
					out.WriteString(err.Error())
					out.WriteByte('\n')
				}
				if result != nil {
					// Result redacts and sanitises its own fields; formatting it must not undo a clean sink
					out.WriteString(fmt.Sprintf("%v", result))
					out.WriteByte('\n')
				}
				checkCleanOutput(t, field+"/"+payload.name, out.String(), payload.text)
			})
		}
	}
}

// TestConnectStepPrintsLengthsOnly: the connect answer's server id, server time and ciid name one live run, and the
// step log is the text a command copies to stdout (and a report pastes). The line prints their lengths instead.
func TestConnectStepPrintsLengthsOnly(t *testing.T) {
	f := newFakeLogin(t)
	defer f.close()

	var out strings.Builder
	opts := f.options()
	opts.Logf = func(format string, args ...any) {
		out.WriteString(fmt.Sprintf(format, args...))
		out.WriteByte('\n')
	}
	if _, err := Login(context.Background(), opts); err != nil {
		t.Fatalf("login: %v", err)
	}

	var line string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(l, "rpc connect: ") {
			line = l
		}
	}
	// the fake answers server id 1/2, server time 1234567890 and ciid "fake-ciid"
	want := "rpc connect: server id label 1 digits, epoch 1 digits, server time 10 digits, ciid 9 chars, bindless true"
	if line != want {
		t.Fatalf("connect step\n got: %q\nwant: %q", line, want)
	}
	for _, value := range []string{"fake-ciid", "1234567890", "1/2"} {
		if strings.Contains(out.String(), value) {
			t.Fatalf("the connect step prints the value %q", value)
		}
	}
}

// TestRESTStatusPhraseIsSanitised covers the one sink the fake cannot produce: net/http writes a standard reason
// phrase, so a hostile one is fed to describeRESTFailure directly.
func TestRESTStatusPhraseIsSanitised(t *testing.T) {
	for _, payload := range hostilePayloads() {
		t.Run(payload.name, func(t *testing.T) {
			for _, body := range [][]byte{nil, []byte(`{"error_code":"` + payload.text + `"}`)} {
				got := (&restClient{}).describeRESTFailure("500 "+payload.text, body)
				checkCleanOutput(t, "status/"+payload.name, got+"\n", payload.text)
			}
		})
	}
}

// TestSRPChallengeTextIsSanitised covers the challenge fields whose only sink is an error: the hash function name and
// the generator are quoted there, so both go through the sanitiser and the length bound.
func TestSRPChallengeTextIsSanitised(t *testing.T) {
	good := srpChallenge{
		Version: 2, Iterations: 15000, Modulus: modulusV2, Generator: "02",
		Username: "U", Salt: make([]byte, 32), PublicB: []byte{1}, HashFunction: "SHA-256",
	}
	for _, payload := range hostilePayloads() {
		for _, field := range []string{"hash_function", "generator"} {
			t.Run(field+"/"+payload.name, func(t *testing.T) {
				challenge := good
				if field == "hash_function" {
					challenge.HashFunction = payload.text
				} else {
					challenge.Generator = payload.text
				}
				_, err := newSRPClient(challenge, "p", nil)
				if err == nil {
					t.Fatal("the poisoned challenge was accepted")
				}
				checkCleanOutput(t, field, err.Error()+"\n", payload.text)
			})
		}
	}
}

// The account is removed before the text is truncated: a name that straddles the 120-byte limit must not survive
// as a fragment, which is what the old order (truncate, then replace the three spellings) allowed.
func TestAccountIsScrubbedBeforeTruncation(t *testing.T) {
	const account = "Notarealaccount"
	c := &restClient{account: account}
	text := strings.Repeat("x ", (maxServerText-8)/2) + account + " after"
	got := c.scrubServerText(text)
	// the marker itself can be cut by the bound (it is part of the text now), but no piece of the name may be
	if !strings.Contains(got, "<acco") {
		t.Fatalf("the account was not replaced: %q", got)
	}
	if strings.Contains(got, account[:6]) || strings.Contains(got, strings.ToLower(account[:6])) {
		t.Fatalf("a fragment of the account survived: %q", got)
	}
	if len(got) > maxServerText {
		t.Fatalf("%d bytes after the scrub, the bound is %d", len(got), maxServerText)
	}
}

// Every spelling and identifier form the login service can echo: any case, the e-mail local part and the
// Battle.net "Name#1234" discriminator.
func TestAccountFormsAreScrubbed(t *testing.T) {
	for _, tc := range []struct {
		account, in, want string
	}{
		{"Testaccount", "TESTACCOUNT refused", "<account> refused"},
		{"Testaccount", "tEsTaCcOuNt#1234 refused", "<account> refused"},
		{"Testaccount", "Testaccount@example.com refused", "<account>@example.com refused"},
		{"user@example.com", "USER@EXAMPLE.COM and user are one", "<account> and <account> are one"},
		{"user#4711", "user#4711 and user", "<account> and <account>"},
	} {
		got := (&restClient{account: tc.account}).scrubServerText(tc.in)
		if got != tc.want {
			t.Errorf("account %q: %q became %q, want %q", tc.account, tc.in, got, tc.want)
		}
	}
}

// The request path is part of an error, and the server chooses it (srp_url); it carries the account scrub too.
func TestRESTErrorPathIsScrubbed(t *testing.T) {
	c := &restClient{
		client:  &http.Client{Transport: &http.Transport{}},
		base:    "https://127.0.0.1:1",
		account: "Testaccount",
	}
	_, err := c.get(context.Background(), "/Testaccount/srp/Testaccount")
	if err == nil {
		t.Fatal("a request to a closed port succeeded")
	}
	if strings.Contains(err.Error(), "Testaccount") {
		t.Fatalf("the account survived in the path of the error: %v", err)
	}
	if !strings.Contains(err.Error(), "<account>/srp/<account>") {
		t.Fatalf("the error does not show the scrubbed path: %v", err)
	}
}
