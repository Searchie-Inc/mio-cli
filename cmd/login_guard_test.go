package cmd

// login_guard_test.go — MIO-3572: login/register must not silently overwrite
// the one stored API key. The oracle is the wire plus the credential store:
// every test records the requests the stub saw and reads the stored key back,
// so "the key was kept" and "nothing irreversible was sent" are observed, not
// assumed.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const (
	guardOldKey    = "mio_sk_old_alpha"
	guardMinted    = "mio_sk_minted_new"
	guardNewKey    = "mio_sk_pasted_beta"
	guardAlphaTeam = "team_alpha"
	guardBetaTeam  = "team_beta"
)

// guardBackend is a fake mio API recording every request ("METHOD path").
type guardBackend struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []string
	token string // JWT handed out by /auth/login and /auth/register
}

func (b *guardBackend) saw(prefix string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.reqs {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

// newGuardBackend: the login/register JWT claims loginTeam. GET /auth/me answers
// per bearer key: old→team_alpha, pasted→team_beta, "mio_sk_dead"→401,
// "mio_sk_flaky"→503.
func newGuardBackend(t *testing.T, loginTeam string) *guardBackend {
	t.Helper()
	b := &guardBackend{token: makeLoginJWT(t, loginTeam)}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.reqs = append(b.reqs, r.Method+" "+r.URL.Path)
		b.mu.Unlock()
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/register":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": b.token, "token_type": "Bearer", "expires_in": 900})
		case r.URL.Path == "/api/v1/auth/me":
			w.Header().Set("Content-Type", "application/json")
			switch bearer {
			case guardOldKey:
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "team_id": guardAlphaTeam})
			case guardNewKey, guardMinted:
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "u2", "team_id": guardBetaTeam})
			case "mio_sk_dead":
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"errors":[{"status":"401","detail":"invalid key"}]}`))
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"errors":[{"status":"503","detail":"upstream down"}]}`))
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/teams/") && !strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			name := map[string]string{guardAlphaTeam: "Alpha QA", guardBetaTeam: "Beta"}[strings.TrimPrefix(r.URL.Path, "/api/v1/teams/")]
			_, _ = w.Write([]byte(`{"data":{"id":"x","type":"teams","attributes":{"name":"` + name + `"}}}`))
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"key_1","type":"api_keys","attributes":{"secret":"` + guardMinted + `"}}}`))
		default:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// storedKey reads the credential store back.
func storedKey(t *testing.T) string {
	t.Helper()
	k, err := config.GetAPIKey()
	if err != nil {
		t.Fatalf("read stored key: %v", err)
	}
	return k
}

func seedStoredKey(t *testing.T, key string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if key != "" {
		if err := config.SetAPIKey(key); err != nil {
			t.Fatalf("seed stored key: %v", err)
		}
	}
}

func guardEnv(b *guardBackend, extra ...string) []string {
	return append([]string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY="}, extra...)
}

// loginHeadless runs `mio login --email --password [extra...]`.
func loginHeadless(t *testing.T, b *guardBackend, extra ...string) contractResult {
	t.Helper()
	args := append([]string{"login", "--email", "a@test.member.dev", "--password", "pw"}, extra...)
	return runContract(t, guardEnv(b), args...)
}

func TestLoginGuard_StoredKeyForAnotherTeam_NonInteractiveRefusesBeforeMinting(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam) // logging into a DIFFERENT team than the stored key's

	res, err := runResume(t, guardEnv(b), "login", "--email", "a@test.member.dev", "--password", "pw")
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("exit = %d, want %d (needs confirmation); err=%v", res.Code, errs.ExitNeedsConfir, err)
	}
	if got := storedKey(t); got != guardOldKey {
		t.Errorf("stored key = %q, want the old key %q KEPT", got, guardOldKey)
	}
	if b.saw("POST /api/v1/teams/") {
		t.Errorf("an API key was minted before the confirmation was answered (orphan key); requests=%v", b.reqs)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	for _, want := range []string{"REPLACE", "Alpha QA", guardAlphaTeam, "--yes"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q must mention %q", msg, want)
		}
	}
}

func TestLoginGuard_WithYes_Replaces(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)
	res := loginHeadless(t, b, "--yes")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if got := storedKey(t); got != guardMinted {
		t.Errorf("stored key = %q, want the newly minted key", got)
	}
}

func TestLoginGuard_SameTeam_NoPrompt(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardAlphaTeam) // same team as the stored key
	res := loginHeadless(t, b)
	if res.Code != errs.ExitOK {
		t.Fatalf("an ordinary re-login must not need confirmation: exit = %d, stderr=%q", res.Code, res.Stderr)
	}
	if got := storedKey(t); got != guardMinted {
		t.Errorf("stored key = %q, want the re-minted key", got)
	}
}

func TestLoginGuard_NoStoredKey_NeverAsksTheAPI(t *testing.T) {
	seedStoredKey(t, "")
	b := newGuardBackend(t, guardBetaTeam)
	res := loginHeadless(t, b)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if b.saw("GET /api/v1/auth/me") {
		t.Errorf("with nothing stored the guard must not look anything up; requests=%v", b.reqs)
	}
}

func TestLoginGuard_StoredKeyTheAPIRejectsOrCannotVerify_StillAsks(t *testing.T) {
	for _, tc := range []struct{ name, key, want string }{
		{"rejected (401)", "mio_sk_dead", "did not accept"},
		{"unverifiable (503)", "mio_sk_flaky", "could not be verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedStoredKey(t, tc.key)
			b := newGuardBackend(t, guardBetaTeam)
			res, err := runResume(t, guardEnv(b), "login", "--email", "a@test.member.dev", "--password", "pw")
			if res.Code != errs.ExitNeedsConfir || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("exit=%d err=%v, want exit 5 mentioning %q", res.Code, err, tc.want)
			}
			if got := storedKey(t); got != tc.key {
				t.Errorf("stored key = %q, want %q KEPT", got, tc.key)
			}
		})
	}
}

func TestRegisterGuard_AsksBeforeTheAccountIsCreated(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)
	args := []string{"register", "--email", "n@test.member.dev", "--password", "s3cr3tpass"}
	res, _ := runResume(t, guardEnv(b), args...)
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("exit = %d, want %d", res.Code, errs.ExitNeedsConfir)
	}
	if b.saw("POST /api/v1/auth/register") {
		t.Errorf("the account was created before the stored-key question was answered — a refusal now leaves a registered account and no key; requests=%v", b.reqs)
	}
	if got := storedKey(t); got != guardOldKey {
		t.Errorf("stored key = %q, want the old key kept", got)
	}

	b2 := newGuardBackend(t, guardBetaTeam)
	res = runContract(t, guardEnv(b2), append(args, "--yes")...)
	if res.Code != errs.ExitOK || !b2.saw("POST /api/v1/auth/register") {
		t.Fatalf("with --yes register must proceed: exit=%d requests=%v stderr=%q", res.Code, b2.reqs, res.Stderr)
	}
	if got := storedKey(t); got != guardMinted {
		t.Errorf("stored key = %q, want the minted key", got)
	}
}

func TestRegisterGuard_NoStoredKey_Proceeds(t *testing.T) {
	seedStoredKey(t, "")
	b := newGuardBackend(t, guardBetaTeam)
	res := runContract(t, guardEnv(b), "register", "--email", "n@test.member.dev", "--password", "s3cr3tpass")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d; stderr=%q", res.Code, res.Stderr)
	}
	if !b.saw("POST /api/v1/auth/register") || storedKey(t) != guardMinted {
		t.Errorf("register must create the account and store the minted key: requests=%v stored=%q", b.reqs, storedKey(t))
	}
}

func TestLoginGuard_PastedOrEnvKey(t *testing.T) {
	t.Run("a key for another team is confirmed", func(t *testing.T) {
		seedStoredKey(t, guardOldKey)
		b := newGuardBackend(t, guardBetaTeam)
		res := runContract(t, []string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY=" + guardNewKey}, "login")
		if res.Code != errs.ExitNeedsConfir {
			t.Fatalf("exit = %d, want %d", res.Code, errs.ExitNeedsConfir)
		}
		if got := storedKey(t); got != guardOldKey {
			t.Errorf("stored key = %q, want the old key kept", got)
		}
		res = runContract(t, []string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY=" + guardNewKey}, "login", "--yes")
		if res.Code != errs.ExitOK || storedKey(t) != guardNewKey {
			t.Errorf("with --yes the pasted key must be stored: exit=%d stored=%q", res.Code, storedKey(t))
		}
	})
	t.Run("storing the key that is already stored is silent", func(t *testing.T) {
		seedStoredKey(t, guardOldKey)
		b := newGuardBackend(t, guardAlphaTeam)
		res := runContract(t, []string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY=" + guardOldKey}, "login")
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
		}
	})
}

// interactive answers: the prompt is shown only on a terminal; a character
// device as stdout stands in for one (the same trick isTTY's check allows).
func TestLoginGuard_InteractiveAnswers(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	for _, tc := range []struct {
		name, answer string
		wantErr      bool
	}{{"y proceeds", "y\n", false}, {"yes proceeds", "YES\n", false}, {"n aborts", "n\n", true}, {"empty aborts", "\n", true}, {"EOF aborts", "", true}, {"partial parse 'yes no' aborts", "yes no\n", true}} {
		t.Run(tc.name, func(t *testing.T) {
			seedStoredKey(t, guardOldKey)
			b := newGuardBackend(t, guardBetaTeam)
			resetGlobalFlags()
			cmd, errBuf := testCmd("")
			cmd.SetOut(devnull)
			// A caller-owned bufio.Reader that already holds the answer: the guard
			// must read THIS reader (the interactive login wraps stdin in one).
			stdinIsTerminal = func(*cobra.Command) bool { return true }
			t.Cleanup(func() { stdinIsTerminal = isInteractiveStdin })
			err := confirmReplaceStoredKey(cmd, b.srv.URL, guardBetaTeam, bufio.NewReader(strings.NewReader(tc.answer)), true)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && errs.CodeOf(err) != errs.ExitNeedsConfir {
				t.Errorf("exit code = %d, want %d", errs.CodeOf(err), errs.ExitNeedsConfir)
			}
			if !strings.Contains(errBuf.String(), "Alpha QA") || !strings.Contains(errBuf.String(), "[y/N]") {
				t.Errorf("prompt = %q, want it to name the team and ask [y/N]", errBuf.String())
			}
		})
	}
}

// After the 403-recovery the target team changes; the guard must be asked about
// the NEW team too (a stored key for it must not be replaced unasked).
func TestMintAndStore_Guard_AskedAgainAfterOwnershipRecovery(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetGlobalFlags()
	token := makeLoginJWT(t, "team_not_owned")
	srv, _, _ := newMintServer(t, mintServerConfig{
		teams:      []teamRow{{id: "team_not_owned", name: "Shared", ownerID: "someone"}, {id: "team_owned", name: "Mine", ownerID: "user-test"}},
		mintStatus: map[string]int{"team_not_owned": http.StatusForbidden, "team_owned": http.StatusCreated},
	})
	cli := client.New(srv.URL, "", client.WithHTTPClient(srv.Client()))
	cmd, _ := testCmd("")
	cfg, _ := config.Load()
	var asked []string
	guard := func(team string) error { asked = append(asked, team); return nil }
	if _, err := mintAndStore(cmd, cli, token, "", cfg, guard); err != nil {
		t.Fatalf("mintAndStore: %v", err)
	}
	if strings.Join(asked, ",") != "team_not_owned,team_owned" {
		t.Errorf("guard asked about %v, want [team_not_owned team_owned] (the guess, then the team recovery chose)", asked)
	}
	_ = cobra.Command{}
}

// ── review of MIO-3572 (Codex gpt-6.1-sol, xhigh) ────────────────────────────

// A piped "y" must not approve a replacement just because stdout is a character
// device (generic confirmDestructive treats /dev/null as a terminal).
func TestLoginGuard_PipedYesWithDevNullStdoutDoesNotApprove(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)
	resetGlobalFlags()
	cmd, _ := testCmd("y\n") // stdin is not a terminal (a *strings.Reader)
	cmd.SetOut(devnull)      // stdout IS a character device
	err = confirmReplaceStoredKey(cmd, b.srv.URL, guardBetaTeam, cmd.InOrStdin(), true)
	if err == nil || errs.CodeOf(err) != errs.ExitNeedsConfir {
		t.Fatalf("err = %v, want exit 5 (a real terminal on stdin is required, or --yes)", err)
	}
}

// register must not send the stored key anywhere: it documents that it sends no
// key, and the answer is "ask" either way (it always creates a new team).
func TestRegisterGuard_DoesNotSendTheStoredKeyToTheAPI(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)
	res, _ := runResume(t, guardEnv(b), "register", "--email", "n@test.member.dev", "--password", "s3cr3tpass")
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("exit = %d, want 5", res.Code)
	}
	if b.saw("GET /api/v1/auth/me") {
		t.Errorf("register looked the stored key up against the API (sending it to %s); requests=%v", b.srv.URL, b.reqs)
	}
}

// A stored credential mio cannot READ (permissions) is replaced by the documented
// recovery (MIO-2995: `MIO_API_KEY=<key> mio login`; the old credential cannot be
// exported either) — but never silently: the note names what is being replaced.
// (Codex's review wanted a confirmation here; the documented recovery wins, and
// TestLogin_EnvKeyReplacesABlobItCannotRead pins it.)
func TestLoginGuard_UnreadableStoredCredential_ReplacedWithAVisibleNote(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	seedStoredKey(t, guardOldKey)
	keyFile := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mio", "keyring", "api-key")
	if err := os.Chmod(keyFile, 0o000); err != nil {
		t.Skipf("no file keyring at %s (%v) — this build stores the key elsewhere", keyFile, err)
	}
	t.Cleanup(func() { _ = os.Chmod(keyFile, 0o600) })
	b := newGuardBackend(t, guardBetaTeam)
	res := runContract(t, []string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY=" + guardNewKey}, "login")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 (the documented recovery); stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "replacing a stored credential mio could not read") {
		t.Errorf("the replacement must be announced, stderr=%q", res.Stderr)
	}
	if got := storedKey(t); got != guardNewKey {
		t.Errorf("stored key = %q, want the pasted key", got)
	}
}

// The prompt must not echo a response body: a proxy that reflects request
// headers would put the stored key into the message.
func TestLoginGuard_UnverifiableKey_PromptNeverEchoesTheResponseBody(t *testing.T) {
	seedStoredKey(t, "mio_sk_reflect")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/auth/me") {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"errors":[{"status":"503","detail":"upstream said Authorization: Bearer ` + r.Header.Get("Authorization")[7:] + `"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": makeLoginJWT(t, guardBetaTeam), "token_type": "Bearer"})
	}))
	t.Cleanup(srv.Close)
	res, err := runResume(t, []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY="}, "login", "--email", "a@test.member.dev", "--password", "pw")
	if res.Code != errs.ExitNeedsConfir || err == nil {
		t.Fatalf("exit = %d err = %v, want 5", res.Code, err)
	}
	if strings.Contains(err.Error(), "mio_sk_reflect") || strings.Contains(res.Stderr, "mio_sk_reflect") {
		t.Errorf("the stored key leaked into the refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("want the status named, got %v", err)
	}
}

// A refusal must leave config.toml (current_team) untouched too.
func TestLoginGuard_RefusalLeavesCurrentTeamUntouched(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mio")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("current_team = \"team_old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newGuardBackend(t, guardBetaTeam)
	res, _ := runResume(t, guardEnv(b), "login", "--email", "a@test.member.dev", "--password", "pw")
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("exit = %d, want 5", res.Code)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentTeam != "team_old" {
		t.Errorf("current_team = %q after a REFUSED login, want team_old untouched", cfg.CurrentTeam)
	}
}

// The pasted --api-key path is guarded like the env path.
func TestLoginGuard_ApiKeyFlag(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)
	res := runContract(t, guardEnv(b), "login", "--api-key", guardNewKey)
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("exit = %d, want 5", res.Code)
	}
	if got := storedKey(t); got != guardOldKey {
		t.Errorf("stored key = %q, want the old key kept", got)
	}
}

// Refusal in the ownership-recovery branch must stop the retry mint and keep the
// stored key — the callback list alone cannot tell "asked" from "obeyed".
func TestMintAndStore_Guard_RefusalAfterOwnershipRecoveryStopsTheRetry(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	resetGlobalFlags()
	token := makeLoginJWT(t, "team_not_owned")
	srv, mintedPaths, _ := newMintServer(t, mintServerConfig{
		teams:      []teamRow{{id: "team_not_owned", name: "Shared", ownerID: "someone"}, {id: "team_owned", name: "Mine", ownerID: "user-test"}},
		mintStatus: map[string]int{"team_not_owned": http.StatusForbidden, "team_owned": http.StatusCreated},
	})
	cli := client.New(srv.URL, "", client.WithHTTPClient(srv.Client()))
	cmd, _ := testCmd("")
	cfg, _ := config.Load()
	calls := 0
	guard := func(string) error {
		calls++
		if calls == 2 {
			return errs.New(errs.ExitNeedsConfir, "declined")
		}
		return nil
	}
	if _, err := mintAndStore(cmd, cli, token, "", cfg, guard); err == nil {
		t.Fatal("a refusing second guard must fail mintAndStore")
	}
	if len(*mintedPaths) != 1 {
		t.Errorf("mint attempts = %v, want only the first (the 403'd one): the retry was sent after the refusal", *mintedPaths)
	}
	if got := storedKey(t); got != guardOldKey {
		t.Errorf("stored key = %q, want the old key kept", got)
	}
}

// A network failure while verifying the stored key must be reported without the
// transport error's text (it can carry URLs and request details).
func TestLoginGuard_UnreachableAPI_PromptSaysOnlyThatTheRequestFailed(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close() // connection refused from here on
	resetGlobalFlags()
	cmd, _ := testCmd("")
	err := confirmReplaceStoredKey(cmd, url, guardBetaTeam, cmd.InOrStdin(), true)
	if err == nil || errs.CodeOf(err) != errs.ExitNeedsConfir {
		t.Fatalf("err = %v, want exit 5", err)
	}
	if !strings.Contains(err.Error(), "the request failed") || strings.Contains(err.Error(), "connect: connection refused") {
		t.Errorf("want the generic 'the request failed' and no transport text, got: %v", err)
	}
}
