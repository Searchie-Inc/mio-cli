package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
