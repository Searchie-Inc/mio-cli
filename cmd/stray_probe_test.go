package cmd

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func isStrayProbe(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		r.URL.Path == "/" &&
		r.URL.RawQuery == "" &&
		strings.HasPrefix(r.UserAgent(), "node") &&
		r.Header.Get("Authorization") == ""
}

type errorReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

func failUnlessStrayProbe(t errorReporter, r *http.Request, format string, args ...any) {
	t.Helper()
	if isStrayProbe(r) {
		return
	}
	t.Errorf(format, args...)
}

type recordedErrors struct{ msgs []string }

func (r *recordedErrors) Helper() {}
func (r *recordedErrors) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

func TestFailUnlessStrayProbe(t *testing.T) {
	mk := func(ua string) *http.Request {
		r, err := http.NewRequest("GET", "http://127.0.0.1/", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("User-Agent", ua)
		return r
	}
	var cli recordedErrors
	failUnlessStrayProbe(&cli, mk("Go-http-client/1.1"), "unexpected request %s %s", "GET", "/")
	if len(cli.msgs) != 1 || cli.msgs[0] != "unexpected request GET /" {
		t.Errorf("a CLI request to / must be reported once with the caller's message; got %q", cli.msgs)
	}
	var probe recordedErrors
	failUnlessStrayProbe(&probe, mk("node"), "unexpected request %s %s", "GET", "/")
	if len(probe.msgs) != 0 {
		t.Errorf("the port prober must not be reported; got %q", probe.msgs)
	}
}

func TestIsStrayProbe(t *testing.T) {
	mk := func(method, target, ua, auth string) *http.Request {
		r, err := http.NewRequest(method, "http://127.0.0.1"+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ua != "" {
			r.Header.Set("User-Agent", ua)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"the prober", mk("GET", "/", "node", ""), true},
		{"the CLI's own GET / (Go user agent)", mk("GET", "/", "Go-http-client/1.1", ""), false},
		{"the CLI's own GET / with a key", mk("GET", "/", "Go-http-client/1.1", "Bearer k"), false},
		{"node on another path", mk("GET", "/api/v1/teams", "node", ""), false},
		{"node with a query", mk("GET", "/?x=1", "node", ""), false},
		{"node with a credential", mk("GET", "/", "node", "Bearer k"), false},
		{"node POST /", mk("POST", "/", "node", ""), false},
	} {
		if got := isStrayProbe(tc.r); got != tc.want {
			t.Errorf("%s: isStrayProbe = %v, want %v", tc.name, got, tc.want)
		}
	}
}
