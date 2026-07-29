package errs

import (
	"errors"
	"fmt"
	"testing"
)

// MIO-2656: the error envelope must be able to report the status the API really
// returned. These tests pin the mechanics that make that possible — the exit
// code stays derived from the status (coarse, stable), the status is retained
// verbatim (precise), and neither can be lost by wrapping.

// WrapHTTP/NewHTTP must record the real status AND derive the same exit code
// the CLI has always used, for every status in the two many-to-one collapses
// that made the old reverse mapping lossy.
func TestHTTPConstructors_KeepStatusAndDeriveCode(t *testing.T) {
	cases := []struct {
		status   int
		wantCode int
	}{
		{401, ExitAuth},        // collapses with 403
		{403, ExitAuth},        // was reported as "401"
		{400, ExitUsage},       // collapses with 409 and 422
		{409, ExitUsage},       // was reported as "400"
		{422, ExitUsage},       // was reported as "400"
		{404, ExitNotFound},    // already 1:1
		{429, ExitRateLimited}, // already 1:1
		{503, ExitServer},      // 5xx collapse — status still exact
		{405, ExitGeneric},     // "other 4xx" bucket
	}

	for _, tc := range cases {
		wrapped := WrapHTTP(tc.status, errors.New("boom"))
		if wrapped.HTTPStatus != tc.status {
			t.Errorf("WrapHTTP(%d).HTTPStatus = %d, want %d", tc.status, wrapped.HTTPStatus, tc.status)
		}
		if wrapped.Code != tc.wantCode {
			t.Errorf("WrapHTTP(%d).Code = %d, want %d", tc.status, wrapped.Code, tc.wantCode)
		}
		// The code must match what the untouched forward mapping says, so the
		// exit-code contract cannot drift as a side effect of this feature.
		if wrapped.Code != ExitCodeForStatus(tc.status) {
			t.Errorf("WrapHTTP(%d).Code = %d, diverges from ExitCodeForStatus = %d",
				tc.status, wrapped.Code, ExitCodeForStatus(tc.status))
		}

		created := NewHTTP(tc.status, "boom %d", tc.status)
		if created.HTTPStatus != tc.status || created.Code != tc.wantCode {
			t.Errorf("NewHTTP(%d) = {status:%d code:%d}, want {status:%d code:%d}",
				tc.status, created.HTTPStatus, created.Code, tc.status, tc.wantCode)
		}
	}
}

// WrapHTTP(nil) must stay nil so callers can wrap unconditionally, exactly like
// Wrap.
func TestWrapHTTP_NilStaysNil(t *testing.T) {
	if got := WrapHTTP(500, nil); got != nil {
		t.Errorf("WrapHTTP(500, nil) = %v, want nil", got)
	}
}

// A value that is not a plausible HTTP status must never reach the envelope: it
// degrades to 0 so the envelope falls back to the exit-code-derived status
// rather than printing something like "status": "0".
func TestHTTPConstructors_RejectImplausibleStatuses(t *testing.T) {
	for _, status := range []int{0, -1, 99, 600, 1000} {
		if got := WrapHTTP(status, errors.New("boom")).HTTPStatus; got != 0 {
			t.Errorf("WrapHTTP(%d).HTTPStatus = %d, want 0 (implausible status dropped)", status, got)
		}
		if got := NewHTTP(status, "boom").HTTPStatus; got != 0 {
			t.Errorf("NewHTTP(%d).HTTPStatus = %d, want 0 (implausible status dropped)", status, got)
		}
	}
}

// HTTPStatusOf must answer 0 for anything that never came from an HTTP
// response, so main.go can tell "the API said X" apart from "this never left
// the machine".
func TestHTTPStatusOf_LocalErrors(t *testing.T) {
	if got := HTTPStatusOf(nil); got != 0 {
		t.Errorf("HTTPStatusOf(nil) = %d, want 0", got)
	}
	if got := HTTPStatusOf(errors.New("plain")); got != 0 {
		t.Errorf("HTTPStatusOf(plain error) = %d, want 0", got)
	}
	// A CLIError built by the ordinary constructors carries no status: these are
	// the local failures (bad flag, unreadable file, no API key) whose envelope
	// status stays exit-code-derived.
	if got := HTTPStatusOf(New(ExitAuth, "no API key found")); got != 0 {
		t.Errorf("HTTPStatusOf(local ExitAuth) = %d, want 0", got)
	}
	if got := HTTPStatusOf(Wrap(ExitUsage, errors.New("bad flag"))); got != 0 {
		t.Errorf("HTTPStatusOf(local ExitUsage) = %d, want 0", got)
	}
}

// The status must survive being wrapped for context. Commands routinely add a
// prefix ("page %q: …") and re-attach an exit code; that must not erase the
// API's answer, which is why HTTPStatusOf walks the whole chain instead of
// reading only the outermost *CLIError the way CodeOf does.
func TestHTTPStatusOf_SurvivesReWrapping(t *testing.T) {
	inner := WrapHTTP(409, errors.New("slug already taken"))

	// 1. plain fmt.Errorf %w wrapping.
	if got := HTTPStatusOf(fmt.Errorf("page %q: %w", "home", inner)); got != 409 {
		t.Errorf("HTTPStatusOf(fmt-wrapped) = %d, want 409", got)
	}

	// 2. the scaffold pattern: re-wrapped in a CLIError that preserves the code.
	reWrapped := Wrap(CodeOf(inner), fmt.Errorf("page %q: %w", "home", inner))
	if got := HTTPStatusOf(reWrapped); got != 409 {
		t.Errorf("HTTPStatusOf(re-wrapped CLIError) = %d, want 409", got)
	}
	if got := CodeOf(reWrapped); got != ExitUsage {
		t.Errorf("CodeOf(re-wrapped) = %d, want %d — the exit code must be untouched", got, ExitUsage)
	}

	// 3. a DELIBERATE reclassification of the exit code (a local judgement about
	//    how the process should terminate) leaves the status alone, because the
	//    status is a statement of fact about what the server answered.
	reclassified := Wrap(ExitGeneric, inner)
	if got := HTTPStatusOf(reclassified); got != 409 {
		t.Errorf("HTTPStatusOf(reclassified) = %d, want 409", got)
	}
	if got := CodeOf(reclassified); got != ExitGeneric {
		t.Errorf("CodeOf(reclassified) = %d, want %d", got, ExitGeneric)
	}
}

// The OUTERMOST recorded status wins when two HTTP errors end up in one chain —
// the walk stops at the first *CLIError that actually carries one.
func TestHTTPStatusOf_OutermostRecordedStatusWins(t *testing.T) {
	inner := WrapHTTP(404, errors.New("not found"))
	outer := WrapHTTP(500, fmt.Errorf("retry failed: %w", inner))
	if got := HTTPStatusOf(outer); got != 500 {
		t.Errorf("HTTPStatusOf(nested) = %d, want 500 (the outermost recorded status)", got)
	}
}

// errors.Is/As must keep reaching the cause through an HTTP-tagged CLIError —
// the new field must not change the unwrap behaviour any existing code relies on.
func TestWrapHTTP_UnwrapStillWorks(t *testing.T) {
	sentinel := errors.New("sentinel")
	err := error(WrapHTTP(403, fmt.Errorf("forbidden: %w", sentinel)))
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is must still reach the wrapped cause through WrapHTTP")
	}
	var ce *CLIError
	if !errors.As(err, &ce) || ce.HTTPStatus != 403 {
		t.Error("errors.As must still find the *CLIError and its status")
	}
}

// ExitCodeForStatus is the untouched half of the contract. MIO-2656 changes only
// what the envelope PRINTS, never how the process exits, so this pins that the
// many-to-one collapses are still exactly as documented in the README/AGENTS
// exit-code table.
func TestExitCodeForStatus_ContractUnchanged(t *testing.T) {
	want := map[int]int{
		400: ExitUsage, 409: ExitUsage, 422: ExitUsage,
		401: ExitAuth, 403: ExitAuth,
		404: ExitNotFound,
		429: ExitRateLimited,
		500: ExitServer, 502: ExitServer, 503: ExitServer,
		405: ExitGeneric, 415: ExitGeneric,
	}
	for status, code := range want {
		if got := ExitCodeForStatus(status); got != code {
			t.Errorf("ExitCodeForStatus(%d) = %d, want %d — the exit-code contract must not drift",
				status, got, code)
		}
	}
}
