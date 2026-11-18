package bnet

// The sanitiser tests: every field the server controls is poisoned with control bytes, an ANSI escape, a one-megabyte
// string and a token-like run, and every piece of text that reaches a sink (the error, the log callback the command
// copies to stdout, and Result.Summary) is checked for printable ASCII, a length bound and the token itself.

import (
	"context"
	"fmt"
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

// TestRESTStatusPhraseIsSanitised covers the one sink the fake cannot produce: net/http writes a standard reason
// phrase, so a hostile one is fed to describeRESTFailure directly.
func TestRESTStatusPhraseIsSanitised(t *testing.T) {
	for _, payload := range hostilePayloads() {
		t.Run(payload.name, func(t *testing.T) {
			for _, body := range [][]byte{nil, []byte(`{"error_code":"` + payload.text + `"}`)} {
				got := describeRESTFailure("500 "+payload.text, body)
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
