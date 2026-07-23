package cmd

// hubs_blob_keys_test.go — contract tests for the best-effort key validation of
// the hub presentation blobs (--branding-json / --settings-json / --meta-json)
// on `mio hubs create` and `mio hubs update` (MIO-2515).
//
// Contract pinned here:
//   default (no --strict-keys): an unknown key WARNS on stderr and the request
//     still fires (the blob is stored verbatim by the API — warn, don't block)
//   --strict-keys: an unknown key is a usage error (ExitUsage) with NO HTTP
//     request fired (validation runs before auth/retrieve)
//   known keys (incl. nested policies/registration/email/auth): no warning,
//     request fires normally
//   the warning goes to STDERR only, never stdout, so --output json/yaml is safe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// firedAnyServer starts a server that flips *fired on ANY request and answers
// with hubWithBlobsBody, used to assert that a strict-mode rejection fires no
// HTTP request at all (no retrieve, no PATCH/POST).
func firedAnyServer(t *testing.T) (*httptest.Server, *bool) {
	t.Helper()
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubWithBlobsBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &fired
}

// TestHubsUpdate_UnknownKeyWarnsByDefault verifies an unknown top-level settings
// key warns on stderr (naming the blob.key) yet still exits ExitOK and fires the
// PATCH — the blob is stored verbatim, so the CLI warns rather than blocking.
func TestHubsUpdate_UnknownKeyWarnsByDefault(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"registraton":{}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) — an unknown key should warn, not block; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Warning") || !strings.Contains(res.Stderr, "settings.registraton") {
		t.Errorf("stderr should carry a warning naming settings.registraton; got %q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must still fire in warn mode (the blob is stored verbatim)")
	}
	if strings.Contains(res.Stdout, "Warning") {
		t.Error("the warning must go to stderr only, never stdout (it would corrupt --output json/yaml)")
	}
}

// TestHubsUpdate_UnknownKeyStrictRejectsNoRequest verifies --strict-keys turns an
// unknown key into a usage error that fires NO HTTP request (no retrieve, no
// PATCH).
func TestHubsUpdate_UnknownKeyStrictRejectsNoRequest(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"registraton":{}}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage) under --strict-keys; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a strict unknown-key rejection must fire no HTTP request (validation runs before retrieve)")
	}
}

// TestHubsUpdate_KnownKeysNoWarning verifies known keys — including nested keys
// under the stable settings sections (registration.enabled) and known branding/
// meta keys — produce no warning and exit ExitOK.
func TestHubsUpdate_KnownKeysNoWarning(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--branding-json", `{"favicon_url":"https://x/f.ico"}`,
			"--settings-json", `{"registration":{"enabled":true}}`,
			"--meta-json", `{"discussions":{"enabled":false}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("known keys must not warn; stderr=%q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must fire for known keys")
	}
}

// TestHubsUpdate_MetaDirectMessagesAccepted (MIO-2612) verifies `directMessages`
// is an accepted --meta-json feature-guard key: it no longer warns "unknown key",
// and it passes under --strict-keys (which rejects an unknown key with no request
// before this fix). directMessages is a real portal flag (MIO-2579).
func TestHubsUpdate_MetaDirectMessagesAccepted(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--meta-json", `{"directMessages":{"enabled":true}}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) for a known meta key under --strict-keys; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("directMessages must not warn as an unknown key; stderr=%q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must fire for a known meta key")
	}
}

// TestHubsUpdate_UnknownNestedKeyStrictRejects verifies the ticket's own example:
// a misspelled sub-key under a stable section (settings.registration.enabld) no
// longer silently succeeds — under --strict-keys it is a usage error with no
// request.
func TestHubsUpdate_UnknownNestedKeyStrictRejects(t *testing.T) {
	srv, fired := firedAnyServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"registration":{"enabld":true}}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage) for a misspelled nested key under --strict-keys; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a strict nested unknown-key rejection must fire no HTTP request")
	}
}

// TestHubsUpdate_UnknownNestedKeyWarnsByDefault verifies a misspelled nested key
// warns (naming settings.registration.enabld) but still fires the PATCH in the
// default warn mode.
func TestHubsUpdate_UnknownNestedKeyWarnsByDefault(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"registration":{"enabld":true}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "settings.registration.enabld") {
		t.Errorf("stderr should name the misspelled nested key settings.registration.enabld; got %q", res.Stderr)
	}
	if len(*patchBody) == 0 {
		t.Error("the PATCH must still fire in warn mode")
	}
}

// TestHubsCreate_UnknownKeyWarnsByDefault verifies the create path mirror: an
// unknown key warns on stderr yet the POST still fires.
func TestHubsCreate_UnknownKeyWarnsByDefault(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--settings-json", `{"bogus":1}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Warning") || !strings.Contains(res.Stderr, "settings.bogus") {
		t.Errorf("stderr should warn naming settings.bogus; got %q", res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("the POST must still fire in warn mode; got method %q", *gotMethod)
	}
}

// TestHubsCreate_UnknownKeyStrictRejectsNoRequest verifies --strict-keys on
// create rejects an unknown key with ExitUsage and no POST.
func TestHubsCreate_UnknownKeyStrictRejectsNoRequest(t *testing.T) {
	srv, fired := firedFlagHubServer(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--settings-json", `{"bogus":1}`,
			"--strict-keys",
		)...)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage) under --strict-keys; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("a strict unknown-key rejection on create must fire no HTTP request")
	}
}

// TestHubsCreate_LogoURLDoesNotTripKeyValidation verifies the --logo-url merge
// into branding (logo_url) is a known key, so it does not produce a spurious
// warning even under --strict-keys.
func TestHubsCreate_LogoURLDoesNotTripKeyValidation(t *testing.T) {
	srv, gotMethod, _, _ := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--logo-url", "https://x/l.png",
			"--strict-keys",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) — logo_url is a known branding key; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Warning") {
		t.Errorf("--logo-url (logo_url) is a known key and must not warn; stderr=%q", res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("the POST must fire; got method %q", *gotMethod)
	}
}
