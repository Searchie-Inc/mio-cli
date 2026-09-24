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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/99designs/keyring"

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

// seedLegacyStoredKey writes a v0.1 blob at blob, the way a v0.1 install
// (2026-06-01 .. 06-09) left it: the key encrypted under the passphrase v0.1
// hardcoded ("mio-cli", published in this repo's history), with no per-install
// key file beside it. It returns the blob's bytes and identity.
func seedLegacyStoredKey(t *testing.T, blob string) ([]byte, os.FileInfo) {
	t.Helper()
	ring, err := keyring.Open(keyring.Config{
		ServiceName:      "mio-cli",
		AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
		FileDir:          filepath.Dir(blob),
		FilePasswordFunc: func(string) (string, error) { return "mio-cli", nil },
	})
	if err != nil {
		t.Fatalf("open a v0.1 file keyring: %v", err)
	}
	if err := ring.Set(keyring.Item{Key: filepath.Base(blob), Data: []byte("mio_sk_live_v01_legacy")}); err != nil {
		t.Fatalf("seed the legacy blob: %v", err)
	}
	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("read the seeded blob: %v", err)
	}
	info, err := os.Lstat(blob)
	if err != nil {
		t.Fatalf("stat the seeded blob: %v", err)
	}
	return data, info
}

// TestLegacyStoredKey_EveryCommandReportsItAndLeavesItInPlace: every command
// that reads the stored key — the root key resolution every resource command
// goes through, `whoami`, and `mio auth token` — reports a v0.1 blob as an
// unusable credential (exit 3, naming the store and `mio login`) and leaves it
// where it is, byte for byte (MIO-2995). No read path deletes, moves or
// renames a stored credential; `mio login` replaces it
// (TestLogin_ReplacesAnUnusableStoredKey). config's
// TestLegacyBlob_EveryReadReportsItAndLeavesItInPlace covers the package-level
// readers.
func TestLegacyStoredKey_EveryCommandReportsItAndLeavesItInPlace(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"root key resolution (contacts list)", []string{"--team", "t_team1", "contacts", "list"}},
		{"whoami", []string{"whoami"}},
		{"auth token", []string{"auth", "token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, blob := isolatedStore(t)
			want, wantInfo := seedLegacyStoredKey(t, blob)
			srv, _ := noRequestServer(t)

			res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL}, tc.args...)
			if res.Code != errs.ExitAuth {
				t.Fatalf("exit = %d, want %d (ExitAuth) for a legacy stored key; err = %v", res.Code, errs.ExitAuth, err)
			}
			if !errors.Is(err, config.ErrLegacyCredentials) {
				t.Errorf("err = %v, want ErrLegacyCredentials", err)
			}
			if err != nil && (!strings.Contains(err.Error(), blob) || !strings.Contains(err.Error(), "mio login")) {
				t.Errorf("the legacy-key error must name the store it read (%s) and say to run `mio login`: %v", blob, err)
			}
			if res.Stdout != "" {
				t.Errorf("stdout = %q, want empty", res.Stdout)
			}

			info, serr := os.Lstat(blob)
			if serr != nil {
				t.Fatalf("after %v the legacy blob is gone (%v): a read must report it and leave it in place", tc.args, serr)
			}
			if !os.SameFile(info, wantInfo) {
				t.Fatalf("after %v %s is no longer the file that held the legacy blob: a read must not move or replace it", tc.args, blob)
			}
			if got, rerr := os.ReadFile(blob); rerr != nil || !bytes.Equal(got, want) {
				t.Fatalf("after %v the legacy blob's bytes changed (read err %v)", tc.args, rerr)
			}
			if entries, _ := os.ReadDir(filepath.Dir(blob)); len(entries) != 1 {
				t.Fatalf("after %v the keyring dir holds %d entries, want only the blob", tc.args, len(entries))
			}
		})
	}
}

// unusableStoredKeys are the stored blobs every read reports as unusable (exit
// 3) and leaves in place, so `mio login` and `mio register` must replace them:
// one that does not decode (the empty file a reader sees while a mio from
// v0.22.0 or earlier rewrites it in place), and a v0.1 legacy blob.
var unusableStoredKeys = []struct {
	name string
	seed func(t *testing.T, blob string)
}{
	{"unreadable blob", func(t *testing.T, blob string) {
		if err := config.SetAPIKey("mio_sk_live_stale"); err != nil {
			t.Fatalf("SetAPIKey: %v", err)
		}
		if err := os.WriteFile(blob, nil, 0o600); err != nil {
			t.Fatalf("empty the blob: %v", err)
		}
	}},
	{"legacy blob", func(t *testing.T, blob string) { seedLegacyStoredKey(t, blob) }},
}

// TestLogin_ReplacesAnUnusableStoredKey: `mio login --email --password` reads
// the store first, and used to ABORT (exit 1) on a blob that did not decode, so
// the only way out was deleting the file by hand. It must treat an unusable
// stored key like no key and overwrite it — a legacy blob included, now that
// the read leaves one in place instead of deleting it.
func TestLogin_ReplacesAnUnusableStoredKey(t *testing.T) {
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

	for _, s := range unusableStoredKeys {
		t.Run(s.name, func(t *testing.T) {
			_, blob := isolatedStore(t)
			s.seed(t, blob)
			if _, err := config.GetAPIKey(); !config.StoredKeyUnusable(err) {
				t.Fatalf("precondition: reading the seeded blob = %v, want an unusable-stored-key error", err)
			}

			res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL},
				"login", "--email", "a@test.member.dev", "--password", "s3cr3t")
			if res.Code != errs.ExitOK {
				t.Fatalf("login over an unusable stored key (%s): exit = %d, want 0; err = %v; stderr=%q", s.name, res.Code, err, res.Stderr)
			}
			got, gerr := config.GetAPIKey()
			if gerr != nil || got != "mio_sk_live_fresh" {
				t.Fatalf("after login the store holds (%q, %v), want the freshly minted key", got, gerr)
			}
		})
	}
}

// TestLogin_EnvKeyReplacesABlobItCannotRead pins the way out that `mio auth
// token --help` documents for a blob the filesystem will not let mio read
// (permission denied: exit 1, not a verdict on the key). `MIO_API_KEY=<key>
// mio login` never reads the old blob, so it must write a fresh one over it,
// back at 0600. (A password login reads the store first and cannot.)
func TestLogin_EnvKeyReplacesABlobItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX file modes that bind the current user")
	}
	_, blob := isolatedStore(t)
	if err := config.SetAPIKey("mio_sk_live_old"); err != nil {
		t.Fatalf("SetAPIKey: %v", err)
	}
	if err := os.Chmod(blob, 0); err != nil {
		t.Fatalf("chmod the blob: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blob, 0o600) })
	if _, err := config.GetAPIKey(); err == nil || config.StoredKeyUnusable(err) {
		t.Fatalf("precondition: reading a mode-000 blob = %v, want a filesystem error rather than a credential verdict", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"who@example.com","id":"u1"}`))
	}))
	t.Cleanup(srv.Close)

	res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY=mio_sk_live_new"}, "login")
	if res.Code != errs.ExitOK {
		t.Fatalf("MIO_API_KEY=<key> mio login over a blob it cannot read: exit = %d, want 0 (the documented recovery); err = %v; stderr=%q",
			res.Code, err, res.Stderr)
	}
	if got, gerr := config.GetAPIKey(); gerr != nil || got != "mio_sk_live_new" {
		t.Fatalf("after login the store holds (%q, %v), want the new key", got, gerr)
	}
	if fi, serr := os.Stat(blob); serr != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the replaced blob: stat = (%v, %v), want mode 0600", fi, serr)
	}
}

// TestRegister_ReplacesAnUnusableStoredKey: `mio register` reads the store
// before minting, exactly like login, and must likewise treat an unusable
// stored key — unreadable or legacy — as "no key" and overwrite it instead of
// aborting with exit 1.
func TestRegister_ReplacesAnUnusableStoredKey(t *testing.T) {
	for _, s := range unusableStoredKeys {
		t.Run(s.name, func(t *testing.T) {
			_, blob := isolatedStore(t)
			s.seed(t, blob)
			if _, err := config.GetAPIKey(); !config.StoredKeyUnusable(err) {
				t.Fatalf("precondition: reading the seeded blob = %v, want an unusable-stored-key error", err)
			}
			var regBody map[string]any
			mintReached := false
			srv := registerMintServer(t, "t_reg", &regBody, &mintReached)

			res, err := runCaptured(t, []string{"MIO_API_BASE_URL=" + srv.URL},
				"register", "--email", "new@test.member.dev", "--password", "s3cr3tpass")
			if res.Code != errs.ExitOK {
				t.Fatalf("register over an unusable stored key (%s): exit = %d, want 0; err = %v; stderr=%q", s.name, res.Code, err, res.Stderr)
			}
			got, gerr := config.GetAPIKey()
			if gerr != nil || got != "mio_sk_registertest123" {
				t.Fatalf("after register the store holds (%q, %v), want the freshly minted key", got, gerr)
			}
		})
	}
}

// TestAuthToken_PrintsTheStoredKeyAndNothingElse is the headless recovery path:
// export the key once (authTokenExport), then stop touching the store.
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
