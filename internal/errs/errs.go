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
	// HTTPStatus is the status the API actually answered with, or 0 when this
	// error never came from an HTTP response (a genuinely local failure: bad
	// flags, unreadable file, no API key).
	//
	// Why this field exists (MIO-2656): the JSON:API error envelope main.go
	// writes to stderr used to reconstruct its `status` member from Code via a
	// reverse lookup. That reverse is lossy BY CONSTRUCTION, because
	// ExitCodeForStatus is many-to-one — 401 and 403 both collapse to ExitAuth,
	// and 400, 409 and 422 all collapse to ExitUsage. The envelope therefore
	// reported 403 as "401", and 409/422 as "400". Agents branch on
	// errors[].status: 403-vs-401 decides whether re-authenticating can help,
	// and 409-vs-422 decides whether retrying can help, so a rewritten status
	// is a broken machine contract. Carrying the real status alongside the
	// (deliberately coarse) exit code lets both be right at once.
	HTTPStatus int
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

// NewHTTP builds a *CLIError for a non-2xx HTTP response: the message is
// formatted like New, the exit code is derived from status via
// ExitCodeForStatus, and the status itself is retained on the error so the
// error envelope can report what the API really answered (MIO-2656).
//
// Deriving the code here rather than taking it from the caller keeps Code and
// HTTPStatus in lockstep: they can never describe two different failures.
func NewHTTP(status int, format string, args ...any) *CLIError {
	ce := New(ExitCodeForStatus(status), format, args...)
	ce.HTTPStatus = normalizeHTTPStatus(status)
	return ce
}

// WrapHTTP is NewHTTP for an existing cause: it attaches the status-derived
// exit code AND the status itself. If err is nil it returns nil so callers can
// wrap unconditionally.
func WrapHTTP(status int, err error) *CLIError {
	if err == nil {
		return nil
	}
	ce := Wrap(ExitCodeForStatus(status), err)
	ce.HTTPStatus = normalizeHTTPStatus(status)
	return ce
}

// normalizeHTTPStatus keeps only plausible HTTP status codes, so a stray 0 (or
// anything else that is not a real status) can never reach the envelope and be
// rendered as a bogus `status` member. Anything out of range degrades to 0,
// which the envelope reads as "no HTTP status known" and falls back to the
// exit-code-derived value.
func normalizeHTTPStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

// HTTPStatusOf returns the HTTP status the API answered with for err, or 0 when
// err did not come from an HTTP response.
//
// It walks the WHOLE error chain rather than reading only the outermost
// *CLIError, returning the first RECORDED status it finds from the outside in
// (wrappers that carry none are transparent). Commands routinely re-wrap a
// client error to add context — e.g. scaffold's
// errs.Wrap(errs.CodeOf(err), fmt.Errorf("page %q: %w", slug, err)) — and that
// outer wrapper has no status of its own. Stopping at the outermost error would
// silently drop the API's answer for exactly the paths that need it most, and
// adding context to an error does not make the server's verdict less true.
//
// Note this deliberately differs from CodeOf, which reads only the outermost
// *CLIError: a command may intentionally RECLASSIFY the exit code (that is a
// local judgement about how the process should terminate) while the HTTP status
// remains a statement of fact about what the API returned.
func HTTPStatusOf(err error) int {
	for err != nil {
		var ce *CLIError
		if !errors.As(err, &ce) {
			return 0
		}
		if ce.HTTPStatus > 0 {
			return ce.HTTPStatus
		}
		// Descend past this *CLIError and look for a deeper one.
		err = ce.Unwrap()
	}
	return 0
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
