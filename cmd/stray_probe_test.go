package cmd

// stray_probe_test.go — tolerate the dev box's localhost port prober.
//
// Several stubs fail the test on "any request I did not expect". On the shared
// dev box a Node process (a `fetch()` — User-Agent "node", Sec-Fetch-Mode: cors,
// B3/traceparent tracing headers) probes freshly opened localhost ports with a
// bare `GET /`, which landed on whichever stub had just taken the port and
// failed it: `unexpected request GET /`, in a different test each time and only
// under load (MIO-4172's TestReplaceWait_…, TestSkillFileBindingRecipe_…,
// TestUnreadableStoredKey_… — reproduced by running that test 6×20 in parallel,
// and the offending request's headers logged). It is not the CLI: nothing in
// non-test code opens a goroutine, and the CLI's own requests carry Go's default
// User-Agent and, with a key, an Authorization header.
//
// isStrayProbe is deliberately narrow so the stubs keep guarding: only a GET of
// exactly "/" from a "node" User-Agent with no credentials is ignored. A CLI
// request to "/" (Go user agent), a node request to any other path, or a probe
// carrying an Authorization header still fails the test.

import (
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

// failUnlessStrayProbe is t.Errorf for an "unexpected request" stub branch,
// except that the dev box's port prober is not a request the test sent.
func failUnlessStrayProbe(t *testing.T, r *http.Request, format string, args ...any) {
	t.Helper()
	if isStrayProbe(r) {
		return
	}
	t.Errorf(format, args...)
}

// TestIsStrayProbe pins how narrow the exemption is: each row that must NOT be
// ignored is a request the stubs are there to catch.
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
