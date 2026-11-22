package tbmproto

import "strings"

// Transport is the channel a command goes out on.
type Transport string

const (
	TransportAddon Transport = "addon"
	TransportChat  Transport = "chat"
)

// Frame is an encoded command awaiting the session.
type Frame struct{ Kind, Channel, Prefix, Text string }

// Encode wraps a command for its transport: addon sends the bare command
// with prefix TBM on PARTY, chat whispers ".bot <command>" to self. The
// command is rejected, never rewritten: blank, a separator or line break,
// or a frame over 255 bytes all fail.
func Encode(t Transport, command string) (Frame, error) {
	if strings.TrimSpace(command) == "" {
		return Frame{}, &Error{Code: "empty-command", Line: command}
	}
	if strings.ContainsAny(command, "|\r\n") {
		return Frame{}, &Error{Code: "unsafe-arg", Line: command}
	}
	f := Frame{Kind: "chat", Channel: "WHISPER_SELF", Text: ChatCommand + " " + command}
	if t == TransportAddon {
		f = Frame{Kind: "addon", Channel: "PARTY", Prefix: Prefix, Text: command}
	}
	if len(f.Text) > MaxText {
		return Frame{}, &Error{Code: "text-too-long", Line: command}
	}
	return f, nil
}

// Roster is the hello command. Version is the version command.
func Roster() string  { return "roster" }
func Version() string { return "version" }

// Behavior builds "behavior <bot>". Action builds "action <intent>" or
// "action <intent> <bot>".
func Behavior(bot string) string { return "behavior " + bot }
func Action(intent, bot string) string {
	if bot == "" {
		return "action " + intent
	}
	return "action " + intent + " " + bot
}

var safeReplacer = strings.NewReplacer("|", " ", "\r", " ", "\n", " ")

// Safe maps the separator and line breaks out of a field (ProtocolSafe).
func Safe(field string) string { return safeReplacer.Replace(field) }

// EncodeLine wraps a type and its fields in the TBM: envelope, the inverse
// of the reader's split: module output round-trips through it byte for byte.
func EncodeLine(typ string, fields ...string) (string, error) {
	var b strings.Builder
	b.WriteString(LinePrefix + typ)
	for _, f := range fields {
		b.WriteByte('|')
		b.WriteString(Safe(f))
	}
	if line := b.String(); len(line) <= MaxText {
		return line, nil
	} else {
		return "", &Error{Code: "text-too-long", Line: line}
	}
}
