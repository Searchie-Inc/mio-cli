package cmd

// mio2269_common_test.go — shared helpers for the MIO-2269 long-tail admin
// bundle contract tests (roles permissions, hubs redirect-origins, hubs
// policies gate/get, email suppressions, analytics engagement).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureAdminReq starts a test server that records the first request's method,
// path, raw query string, and raw body, then replies with respBody at the given
// HTTP status. It returns pointers the caller dereferences after runContract.
func captureAdminReq(t *testing.T, status int, respBody string) (srv *httptest.Server, method, path, rawQuery *string, body *[]byte) {
	t.Helper()
	var m, p, q string
	var b []byte
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m = r.Method
		p = r.URL.Path
		q = r.URL.RawQuery
		b, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &m, &p, &q, &b
}

// firedGuardServer starts a test server that flips *fired to true if it ever
// receives a request. Used by required-flag / usage-error cases that must exit
// before any HTTP request is made.
func firedGuardServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &fired
}
