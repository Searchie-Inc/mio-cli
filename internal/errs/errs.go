// Package errs defines the mio CLI's typed error model and the stable
// process exit codes that AI agents and CI branch on.
//
// Every command path that can fail should return (or wrap) a *CLIError so that
// main.go can translate it into the correct exit code. The exit-code contract
// is part of the public CLI surface — see the design doc §4.5 — and must not
// drift.
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

// Exit codes. These are a stable public contract: agents and CI scripts branch
// on them, so values must never be reused for a different meaning.
const (
	ExitOK          = 0 // success
	ExitGeneric     = 1 // generic / unexpected error
	ExitUsage       = 2 // bad flags / usage error
	ExitAuth        = 3 // missing or invalid credentials
	ExitNotFound    = 4 // resource not found (404)
	ExitNeedsConfir = 5 // destructive op needs --yes in a non-TTY
	ExitRateLimited = 6 // rate limited (429)
	ExitServer      = 7 // upstream server error (5xx)
)

// CLIError carries both a human-facing error and the process exit code that
// should be returned when it propagates to main. It implements error and
// unwraps to the underlying cause so errors.Is/As keep working.
type CLIError struct {
	// Code is the process exit code (one of the Exit* constants).
	Code int
	// Err is the underlying cause; its Error() string is shown to the user.
	Err error
}

// Error implements the error interface.
func (e *CLIError) Error() string {
	if e == nil || e.Err == nil {
		return "unknown error"
	}
	return e.Err.Error()
}

// Unwrap allows errors.Is / errors.As to reach the wrapped cause.
func (e *CLIError) Unwrap() error { return e.Err }

// New builds a *CLIError from a code and a formatted message.
func New(code int, format string, args ...any) *CLIError {
	return &CLIError{Code: code, Err: fmt.Errorf(format, args...)}
}

// Wrap attaches an exit code to an existing error. If err is nil it returns nil
// so callers can wrap unconditionally.
func Wrap(code int, err error) *CLIError {
	if err == nil {
		return nil
	}
	return &CLIError{Code: code, Err: err}
}

// CodeOf extracts the exit code carried by err. A nil error is ExitOK; any error
// that is not (and does not wrap) a *CLIError is treated as ExitGeneric.
func CodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ExitGeneric
}

// ExitCodeForStatus maps an HTTP status code to a CLI exit code. Used by the
// client when an API call returns a non-2xx response so the exit code reflects
// the failure class even when the body has no structured error.
func ExitCodeForStatus(status int) int {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ExitAuth
	case status == http.StatusNotFound:
		return ExitNotFound
	case status == http.StatusTooManyRequests:
		return ExitRateLimited
	case status >= 500:
		return ExitServer
	case status == http.StatusBadRequest,
		status == http.StatusConflict,
		status == http.StatusUnprocessableEntity:
		// 400/409/422 — bad input / usage errors the caller can fix by
		// changing flags or arguments. Mapped to ExitUsage so agents and CI
		// can distinguish "you sent something wrong" from a generic failure.
		return ExitUsage
	case status >= 400:
		// Other 4xx (e.g. 405, 415) — a client-side problem we surface
		// generically.
		return ExitGeneric
	default:
		return ExitGeneric
	}
}
