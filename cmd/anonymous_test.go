package cmd

// anonymous_test.go — MIO-2694: `--anonymous` must actually SEND an
// unauthenticated request.
//
// The flag is documented as "Ignore MIO_API_KEY and the stored key; resolve as
// unauthenticated (for diagnostics)", but the resolver order was
// [key-required precondition] → [--anonymous], so requireAuth killed every
// invocation with "no API key found" before the HTTP client ever ran. The proof
// it never reached the network: pointing --anonymous at a dead host produced the
// same key error with no connection attempt, while the identical command with a
// (fake) key produced a real dial error.
//
// These tests pin the corrected ordering from BOTH sides: the request is made,
// and it carries no Authorization header.
//
// They reuse the in-process harness from contract_test.go (runContract /
// baseEnv / withTeam) and firedGuardServer from mio2269_common_test.go.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// deadHost is a loopback port nothing listens on, so a request to it fails at
// dial with a NETWORK error. It is preferred over an unroutable hostname
// because it needs no DNS and fails instantly.
const deadHost = "http://127.0.0.1:1"

// TestAnonymous_ReachesHTTPWithNoAuthorizationHeader is the core MIO-2694
// contract: with a key in the env, --anonymous still issues the request, and the
// request carries NO Authorization header. This also subsumes the old MIO-2648
// wiring guard — an Authorization header here would mean `Anonymous:
// flags.anonymous` was dropped from newContext's Overrides.
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

	// baseEnv sets MIO_API_KEY; --anonymous must drop it. --team is explicit so
	// the assertion is about auth, not team auto-defaulting.
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

// TestAnonymous_LetsTheAPIDecide401: the CLI no longer pre-empts the API's
// verdict. A 401 from the server maps to ExitAuth through the normal HTTP error
// path — the same exit code as before the fix, but now it is the SERVER's answer
// rather than a local precondition, which is the entire diagnostic value.
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

// TestAnonymous_DeadHostIsANetworkErrorNotAKeyError reproduces the ticket's
// smoking gun. Before the fix, --anonymous against a dead host returned the
// "no API key found" auth error (exit 3) with no connection attempt; the same
// command WITH a key produced a real dial failure. Both must now fail the same
// way — at the network (ExitGeneric), not at a local precondition.
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

	// Parity check: an explicit (fake) key against the same dead host has always
	// produced the network error. --anonymous must now match it exactly.
	keyed := runContract(t, baseEnv(deadHost),
		withTeam("t_team1", "--api-key", "fake", "--api-base", deadHost, "hubs", "list")...)
	if anon.Code != keyed.Code {
		t.Errorf("dead-host exit codes diverge: --anonymous=%d, --api-key=%d — both must be the network error",
			anon.Code, keyed.Code)
	}
}

// TestAnonymous_ExplicitAPIKeyStillWins pins the documented exception in the
// flag help: "An explicit --api-key still wins." --anonymous suppresses the env
// and keychain fallbacks only.
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

// TestAnonymous_WhoamiReportsNoKeySource keeps the diagnostic honest: whoami's
// key_source read the env/keychain directly, so under --anonymous it named a key
// the CLI was deliberately NOT sending.
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

// TestAnonymous_NoKeyErrorMessageIsGone drives the real binary so the JSON:API
// error envelope main.go writes on the os.Exit path can be read: against a dead
// host, --anonymous must surface the DIAL failure, never "no API key found".
// Only a subprocess can capture that envelope.
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
	// The dial failure names the transport; assert on "connect" (the Go net error
	// for a refused loopback port is "connect: connection refused") rather than a
	// full string so this does not pin a platform-specific phrasing.
	if !strings.Contains(stderr, "connect") {
		t.Errorf("expected a network error naming the dial failure; stderr=%q", stderr)
	}
}
