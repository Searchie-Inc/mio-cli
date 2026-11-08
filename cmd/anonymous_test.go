package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const deadHost = "http://127.0.0.1:1"

func TestAnonymous_ReachesHTTPWithNoAuthorizationHeader(t *testing.T) {
	var (
		fired   atomic.Bool
		gotAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fired.Store(true)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--anonymous", "hubs", "list")...)

	if !fired.Load() {
		t.Fatal("--anonymous never reached the HTTP layer — the key-required precondition fired first (MIO-2694)")
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (--anonymous must send no credentials)", gotAuth)
	}
	if res.Code != errs.ExitOK {
		t.Errorf("exit=%d want ExitOK — the API's own 200 must be honoured; stderr=%q", res.Code, res.Stderr)
	}
}

func TestAnonymous_LetsTheAPIDecide401(t *testing.T) {
	var fired atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired.Store(true)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errors":[{"status":"401","detail":"Not authenticated"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "--anonymous", "hubs", "list")...)
	if !fired.Load() {
		t.Fatal("--anonymous must send the request and let the API answer")
	}
	if res.Code != errs.ExitAuth {
		t.Errorf("exit=%d want ExitAuth (3) from the API's 401; stderr=%q", res.Code, res.Stderr)
	}
}

func TestAnonymous_DeadHostIsANetworkErrorNotAKeyError(t *testing.T) {
	anon := runContract(t, baseEnv(deadHost),
		withTeam("t_team1", "--anonymous", "--api-base", deadHost, "hubs", "list")...)
	if anon.Code == errs.ExitAuth {
		t.Fatalf("--anonymous against a dead host exited ExitAuth (3) — it never left the CLI (MIO-2694)")
	}
	if anon.Code != errs.ExitGeneric {
		t.Errorf("--anonymous dead-host exit=%d, want ExitGeneric (1) from the dial failure; stderr=%q",
			anon.Code, anon.Stderr)
	}

	keyed := runContract(t, baseEnv(deadHost),
		withTeam("t_team1", "--api-key", "fake", "--api-base", deadHost, "hubs", "list")...)
	if anon.Code != keyed.Code {
		t.Errorf("dead-host exit codes diverge: --anonymous=%d, --api-key=%d — both must be the network error",
			anon.Code, keyed.Code)
	}
}

func TestAnonymous_ExplicitAPIKeyStillWins(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(`{"data":[],"meta":{}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--anonymous", "--api-key", "explicit-key", "hubs", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotAuth != "Bearer explicit-key" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer explicit-key")
	}
}

func TestAnonymous_WhoamiReportsNoKeySource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"anon@example.com"}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL), "--anonymous", "whoami")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "none (--anonymous)") {
		t.Errorf("whoami key_source did not report the anonymous resolution; stdout=%q", res.Stdout)
	}
}

func TestAnonymous_NoKeyErrorMessageIsGone(t *testing.T) {
	bin := buildBinary(t)
	_, stderr, code := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		"MIO_API_BASE_URL=" + deadHost,
	}, "--team", "t_team1", "--anonymous", "hubs", "list")

	if strings.Contains(stderr, "no API key found") {
		t.Errorf("--anonymous still dies on the key precondition; stderr=%q", stderr)
	}
	if code != errs.ExitGeneric {
		t.Errorf("exit=%d want ExitGeneric (1) from the dial failure; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "connect") {
		t.Errorf("expected a network error naming the dial failure; stderr=%q", stderr)
	}
}
