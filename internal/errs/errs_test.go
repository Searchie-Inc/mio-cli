package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestHTTPConstructors_KeepStatusAndDeriveCode(t *testing.T) {
	cases := []struct {
		status   int
		wantCode int
	}{
		{401, ExitAuth},
		{403, ExitAuth},
		{400, ExitUsage},
		{409, ExitUsage},
		{422, ExitUsage},
		{404, ExitNotFound},
		{429, ExitRateLimited},
		{503, ExitServer},
		{405, ExitGeneric},
	}

	for _, tc := range cases {
		wrapped := WrapHTTP(tc.status, errors.New("boom"))
		if wrapped.HTTPStatus != tc.status {
			t.Errorf("WrapHTTP(%d).HTTPStatus = %d, want %d", tc.status, wrapped.HTTPStatus, tc.status)
		}
		if wrapped.Code != tc.wantCode {
			t.Errorf("WrapHTTP(%d).Code = %d, want %d", tc.status, wrapped.Code, tc.wantCode)
		}
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

func TestWrapHTTP_NilStaysNil(t *testing.T) {
	if got := WrapHTTP(500, nil); got != nil {
		t.Errorf("WrapHTTP(500, nil) = %v, want nil", got)
	}
}

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

func TestHTTPStatusOf_LocalErrors(t *testing.T) {
	if got := HTTPStatusOf(nil); got != 0 {
		t.Errorf("HTTPStatusOf(nil) = %d, want 0", got)
	}
	if got := HTTPStatusOf(errors.New("plain")); got != 0 {
		t.Errorf("HTTPStatusOf(plain error) = %d, want 0", got)
	}
	if got := HTTPStatusOf(New(ExitAuth, "no API key found")); got != 0 {
		t.Errorf("HTTPStatusOf(local ExitAuth) = %d, want 0", got)
	}
	if got := HTTPStatusOf(Wrap(ExitUsage, errors.New("bad flag"))); got != 0 {
		t.Errorf("HTTPStatusOf(local ExitUsage) = %d, want 0", got)
	}
}

func TestHTTPStatusOf_SurvivesReWrapping(t *testing.T) {
	inner := WrapHTTP(409, errors.New("slug already taken"))

	if got := HTTPStatusOf(fmt.Errorf("page %q: %w", "home", inner)); got != 409 {
		t.Errorf("HTTPStatusOf(fmt-wrapped) = %d, want 409", got)
	}

	reWrapped := Wrap(CodeOf(inner), fmt.Errorf("page %q: %w", "home", inner))
	if got := HTTPStatusOf(reWrapped); got != 409 {
		t.Errorf("HTTPStatusOf(re-wrapped CLIError) = %d, want 409", got)
	}
	if got := CodeOf(reWrapped); got != ExitUsage {
		t.Errorf("CodeOf(re-wrapped) = %d, want %d — the exit code must be untouched", got, ExitUsage)
	}

	reclassified := Wrap(ExitGeneric, inner)
	if got := HTTPStatusOf(reclassified); got != 409 {
		t.Errorf("HTTPStatusOf(reclassified) = %d, want 409", got)
	}
	if got := CodeOf(reclassified); got != ExitGeneric {
		t.Errorf("CodeOf(reclassified) = %d, want %d", got, ExitGeneric)
	}
}

func TestHTTPStatusOf_OutermostRecordedStatusWins(t *testing.T) {
	inner := WrapHTTP(404, errors.New("not found"))
	outer := WrapHTTP(500, fmt.Errorf("retry failed: %w", inner))
	if got := HTTPStatusOf(outer); got != 500 {
		t.Errorf("HTTPStatusOf(nested) = %d, want 500 (the outermost recorded status)", got)
	}
}

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
