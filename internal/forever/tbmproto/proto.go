// Package tbmproto is the reference reader and encoder for the TBM add-on
// protocol contract v1 (contract/tbm/v1): the agent side of the exchange the
// Forever module will speak. It never touches the Turtle-only internal/tbm.
package tbmproto

const (
	Prefix         = "TBM"
	LinePrefix     = "TBM:"
	ChatCommand    = ".bot"
	ProtoLevel     = 1
	MinModuleProto = 1
	MaxText        = 255 // one message of text; matches addonmsg.MaxText
)

// Error is a protocol or encode failure; Line is the offending line.
type Error struct {
	Code, Line string
}

func (e *Error) Error() string { return "tbm: " + e.Code + ": " + e.Line }

// Is matches errors with the same Code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

var (
	ErrStreamCount        = &Error{Code: "stream-count"}
	ErrRowOutsideStream   = &Error{Code: "row-outside-stream"}
	ErrEndWithoutBegin    = &Error{Code: "end-without-begin"}
	ErrNestedStream       = &Error{Code: "nested-stream"}
	ErrUnterminatedStream = &Error{Code: "unterminated-stream"}
	ErrFieldCount         = &Error{Code: "field-count"}
	ErrBadNumber          = &Error{Code: "bad-number"}
	ErrBadScope           = &Error{Code: "bad-scope"}
	ErrEmptyField         = &Error{Code: "empty-field"}
	ErrBadRow             = &Error{Code: "bad-row"}
	ErrBadPair            = &Error{Code: "bad-pair"}
	ErrTextTooLong        = &Error{Code: "text-too-long"}
	ErrHelloIncomplete    = &Error{Code: "hello-incomplete"}
	ErrProtoIncompatible  = &Error{Code: "proto-incompatible"}
	ErrEmptyCommand       = &Error{Code: "empty-command"}
	ErrUnsafeArg          = &Error{Code: "unsafe-arg"}
)
