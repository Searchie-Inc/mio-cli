package cmd

// credential_store_test.go — MIO-2995: when the CLI cannot find or read a
// stored API key it must say which credential store it consulted, `whoami`
// must name the store a key came from, and `mio auth token` must hand the
// stored key to a headless session exactly once, on stdout only.
//
// Every test here gives itself a fresh XDG_CONFIG_HOME, and TestMain pins the
// keyring to the encrypted file backend, so nothing reads or writes the
// developer's real ~/.config/mio, OS keychain or Secret Service.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// isolatedStore points the config dir at a fresh temp dir, clears MIO_API_KEY
// and returns the path of the file-keyring blob under it.
func isolatedStore(t *testing.T) (cfgHome, blob string) {
	t.Helper()
	cfgHome = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv(config.EnvAPIKey, "")
	return cfgHome, filepath.Join(cfgHome, "mio", "keyring", "api-key")
}

// runCaptured is runContract that also returns the error Execute produced, so
// a test can read the message main.go would render.
func runCaptured(t *testing.T, env []string, args ...string) (contractResult, error) {
	t.Helper()
	restore := overlayEnv(t, env)
	defer restore()
	resetGlobalFlags()

	root := RootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	defer root.SetArgs(nil)

	err := root.Execute()
	return contractResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: codeForExecuteErr(err)}, err
}

// noRequestServer fails the test if anything reaches it.
func noRequestServer(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return srv, &hit
}

// TestNoAPIKeyFound_NamesTheStoreItRead pins MIO-2995's diagnosis gap. The
// release binaries keep the key in a FILE under the config dir, so a shell
// whose XDG_CONFIG_HOME (or HOME) differs from the one `mio login` ran in finds
// no key — which QA read as an intermittently failing macOS Keychain. The exit-3
// message must name the store it looked in and the variable that chose it, so
// that mismatch is visible in the error itself. The historical prefix is kept
// verbatim: agents and docs match on it.
func TestNoAPIKeyFound_NamesTheStoreItRead(t *testing.T) {
	_, blob := isolatedStore(t)
	srv, _ := noRequestServer(t)

	res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL}, "--team", "t_team1", "contacts", "list")
	if res.Code != errs.ExitAuth {
		t.Fatalf("exit = %d, want %d (ExitAuth); err = %v", res.Code, errs.ExitAuth, err)
	}
	msg := err.Error()
	const prefix = "no API key found: set --api-key, export MIO_API_KEY, or run `mio login`"
	if !strings.HasPrefix(msg, prefix) {
		t.Errorf("message lost its stable prefix %q: %q", prefix, msg)
	}
	if !strings.Contains(msg, blob) {
		t.Errorf("the no-key message does not name the store it read (%s): %q", blob, msg)
	}
	if !strings.Contains(msg, "$XDG_CONFIG_HOME") {
		t.Errorf("the no-key message does not say which variable chose the config dir: %q", msg)
	}
}

// TestWhoami_KeySourceNamesTheBackend: a stored key used to be reported as
// key_source "keychain" whatever held it — on a release macOS binary that is
// always the file keyring, and the wrong name sent MIO-2995's diagnosis after
// Keychain ACLs. The flag / env labels are unchanged; agents branch on them.
func TestWhoami_KeySourceNamesTheBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"who@example.com","id":"u1"}`))
	}))
	t.Cleanup(srv.Close)

	keySourceOf := func(t *testing.T, res contractResult) string {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
			t.Fatalf("whoami stdout is not JSON: %v; stdout=%q stderr=%q", err, res.Stdout, res.Stderr)
		}
		ks, _ := out["key_source"].(string)
		return ks
	}

	t.Run("stored key", func(t *testing.T) {
		_, blob := isolatedStore(t)
		if err := config.SetAPIKey("mio_sk_live_whoami_stored"); err != nil {
			t.Fatalf("SetAPIKey: %v", err)
		}
		res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL}, "whoami")
		if res.Code != errs.ExitOK {
			t.Fatalf("whoami exit = %d; err = %v", res.Code, err)
		}
		if got, want := keySourceOf(t, res), "file keyring ("+blob+")"; got != want {
			t.Errorf("key_source = %q, want %q", got, want)
		}
	})
	t.Run("env", func(t *testing.T) {
		isolatedStore(t)
		res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY=mio_sk_live_env"}, "whoami")
		if res.Code != errs.ExitOK {
			t.Fatalf("whoami exit = %d; err = %v", res.Code, err)
		}
		if got := keySourceOf(t, res); got != "env (MIO_API_KEY)" {
			t.Errorf("key_source = %q, want %q", got, "env (MIO_API_KEY)")
		}
	})
	t.Run("flag", func(t *testing.T) {
		isolatedStore(t)
		res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL}, "--api-key", "mio_sk_live_flag", "whoami")
		if res.Code != errs.ExitOK {
			t.Fatalf("whoami exit = %d; err = %v", res.Code, err)
		}
		if got := keySourceOf(t, res); got != "flag (--api-key)" {
			t.Errorf("key_source = %q, want %q", got, "flag (--api-key)")
		}
	})
}

// TestUnreadableStoredKey_ExitsAuthAndKeepsTheBlob: a blob that does not decode
// — here the empty file a reader sees while an older mio rewrites it in place —
// is an unusable credential (exit 3, re-authenticate), and the read must not
// delete it. Before MIO-2995 this exited 1 with a bare JOSE parse error.
func TestUnreadableStoredKey_ExitsAuthAndKeepsTheBlob(t *testing.T) {
	_, blob := isolatedStore(t)
	if err := config.SetAPIKey("mio_sk_live_about_to_be_unreadable"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := os.WriteFile(blob, nil, 0o600); err != nil {
		t.Fatalf("empty the blob: %v", err)
	}
	srv, _ := noRequestServer(t)

	res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL}, "--team", "t_team1", "contacts", "list")
	if res.Code != errs.ExitAuth {
		t.Fatalf("exit = %d, want %d (ExitAuth) for an unreadable stored key; err = %v", res.Code, errs.ExitAuth, err)
	}
	if !strings.Contains(err.Error(), blob) {
		t.Errorf("the unreadable-key error does not name the blob it read (%s): %v", blob, err)
	}
	if _, statErr := os.Stat(blob); statErr != nil {
		t.Errorf("reading an unreadable blob removed it: %v", statErr)
	}
}

// TestLogin_ReplacesAnUnreadableStoredKey: `mio login --email --password` reads
// the store first, and used to ABORT (exit 1) on a blob that did not decode, so
// the only way out was deleting the file by hand. It must treat an unusable
// stored key like no key and overwrite it.
func TestLogin_ReplacesAnUnreadableStoredKey(t *testing.T) {
	_, blob := isolatedStore(t)
	if err := config.SetAPIKey("mio_sk_live_stale"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := os.WriteFile(blob, nil, 0o600); err != nil {
		t.Fatalf("empty the blob: %v", err)
	}

	token := makeLoginJWT(t, "team_owned")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/login" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			resp, _ := json.Marshal(map[string]any{"access_token": token, "token_type": "bearer"})
			_, _ = w.Write(resp)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"key_1","type":"api_keys","attributes":{"secret":"mio_sk_live_fresh"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL},
		"login", "--email", "a@test.member.dev", "--password", "s3cr3t")
	if res.Code != errs.ExitOK {
		t.Fatalf("login over an unreadable stored key: exit = %d, want 0; err = %v; stderr=%q", res.Code, err, res.Stderr)
	}
	got, gerr := config.GetAPIKey()
	if gerr != nil || got != "mio_sk_live_fresh" {
		t.Fatalf("after login the store holds (%q, %v), want the freshly minted key", got, gerr)
	}
}

// TestAuthToken_PrintsTheStoredKeyAndNothingElse is the headless recovery path:
// `export MIO_API_KEY=$(mio auth token)` once, then stop touching the store.
// stdout is exactly the key and a newline whatever --output says; the key
// appears nowhere else; and no request is made (it must work offline).
func TestAuthToken_PrintsTheStoredKeyAndNothingElse(t *testing.T) {
	isolatedStore(t)
	const key = "mio_sk_live_exported_once"
	if err := config.SetAPIKey(key); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	srv, hit := noRequestServer(t)

	for _, extra := range [][]string{nil, {"-o", "json"}, {"-o", "table"}} {
		args := append(append([]string{}, extra...), "auth", "token")
		res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL}, args...)
		if res.Code != errs.ExitOK {
			t.Fatalf("%v: exit = %d; err = %v", args, res.Code, err)
		}
		if res.Stdout != key+"\n" {
			t.Errorf("%v: stdout = %q, want exactly the stored key and a newline", args, res.Stdout)
		}
		if strings.Contains(res.Stderr, key) {
			t.Errorf("%v: the key leaked onto stderr: %q", args, res.Stderr)
		}
	}
	if hit.Load() {
		t.Error("auth token made a network request; it must only read the local store")
	}
}

// TestAuthToken_NoStoredKey: exit 3, NOTHING on stdout (so `$(mio auth token)`
// captures an empty string rather than an error message), and the error names
// the store it read.
func TestAuthToken_NoStoredKey(t *testing.T) {
	_, blob := isolatedStore(t)

	res, err := runCaptured(t, nil, "auth", "token")
	if res.Code != errs.ExitAuth {
		t.Fatalf("exit = %d, want %d (ExitAuth); err = %v", res.Code, errs.ExitAuth, err)
	}
	if res.Stdout != "" {
		t.Errorf("stdout = %q, want empty when no key is stored", res.Stdout)
	}
	if !strings.Contains(err.Error(), blob) {
		t.Errorf("the error does not name the store it read (%s): %v", blob, err)
	}
}

// TestAuthToken_ReadsTheStoreNotTheEnvironment: the verb exports what `mio
// login` stored. A MIO_API_KEY already in the environment is not echoed back
// (that would make the verb a no-op exactly when a stale key is set); a
// stderr note says the env var takes precedence, without printing either key.
func TestAuthToken_ReadsTheStoreNotTheEnvironment(t *testing.T) {
	isolatedStore(t)
	const stored, env = "mio_sk_live_in_the_store", "mio_sk_live_in_the_env"
	if err := config.SetAPIKey(stored); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}

	res, err := runCaptured(t, []string{"MIO_API_KEY=" + env}, "auth", "token")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; err = %v", res.Code, err)
	}
	if res.Stdout != stored+"\n" {
		t.Errorf("stdout = %q, want the STORED key", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "MIO_API_KEY") {
		t.Errorf("stderr = %q, want a note that MIO_API_KEY is set and takes precedence", res.Stderr)
	}
	if strings.Contains(res.Stderr, stored) || strings.Contains(res.Stderr, env) {
		t.Errorf("stderr = %q must not contain either key", res.Stderr)
	}
}

// TestAuthToken_UnreadableStoredKey: exit 3, nothing on stdout, blob kept.
func TestAuthToken_UnreadableStoredKey(t *testing.T) {
	_, blob := isolatedStore(t)
	if err := config.SetAPIKey("mio_sk_live_soon_unreadable"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := os.WriteFile(blob, []byte("not-a-jwe"), 0o600); err != nil {
		t.Fatalf("corrupt the blob: %v", err)
	}

	res, err := runCaptured(t, nil, "auth", "token")
	if res.Code != errs.ExitAuth {
		t.Fatalf("exit = %d, want %d (ExitAuth); err = %v", res.Code, errs.ExitAuth, err)
	}
	if res.Stdout != "" {
		t.Errorf("stdout = %q, want empty", res.Stdout)
	}
	if _, statErr := os.Stat(blob); statErr != nil {
		t.Errorf("auth token removed the unreadable blob: %v", statErr)
	}
}
