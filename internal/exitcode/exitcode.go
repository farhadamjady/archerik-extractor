// Package exitcode is the process-exit-code taxonomy (PLAN §B.3). Subsystems
// return a *Error tagged with the code the CLI should exit with; the CLI calls
// Of(err) to get it. Keeping the mapping in one leaf package means auth, detect,
// and submit all agree on what "not entitled" or "detection failed" exits as.
package exitcode

import (
	"errors"
	"fmt"
)

// Code is a process exit code. Values are stable — CI and docs depend on them.
type Code int

const (
	OK      Code = 0
	Runtime Code = 1 // generic runtime error (parse / IO / marshal / usage)
	Detect  Code = 2 // detection failed: no provider matched, or ambiguous

	AuthMissingKey  Code = 10 // no key via flag, env, or config
	AuthInvalid     Code = 11 // 401 invalid / expired
	AuthNotEntitled Code = 12 // 403 not entitled
	AuthQuota       Code = 13 // 429 quota exceeded
	AuthUnreachable Code = 14 // validation server unreachable (fail-closed)

	Submit Code = 20 // submission failed (re-validation / network)
)

// Error carries an exit Code alongside a message and optional cause.
type Error struct {
	Code Code
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds a coded error with a formatted message and no cause.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Wrap builds a coded error around a cause.
func Wrap(code Code, msg string, err error) *Error {
	return &Error{Code: code, Msg: msg, Err: err}
}

// Of returns the exit code for err: OK for nil, the tagged Code for a *Error
// (anywhere in the chain), and Runtime for any other error.
func Of(err error) int {
	if err == nil {
		return int(OK)
	}
	var e *Error
	if errors.As(err, &e) {
		return int(e.Code)
	}
	return int(Runtime)
}
