package cmd

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

type guardBackend struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []string
	bearers []string
	token   string
}

func (b *guardBackend) sawBearer(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range b.bearers {
		if k == key {
			return true
		}
	}
	return false
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

func newGuardBackend(t *testing.T, loginTeam string) *guardBackend {
	t.Helper()
	b := &guardBackend{token: makeLoginJWT(t, loginTeam)}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		b.mu.Lock()
		b.reqs = append(b.reqs, r.Method+" "+r.URL.Path)
		if bearer != "" {
			b.bearers = append(b.bearers, bearer)
		}
		b.mu.Unlock()
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
			case "mio_sk_noteam":
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "u3"})
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

func loginHeadless(t *testing.T, b *guardBackend, extra ...string) contractResult {
	t.Helper()
	args := append([]string{"login", "--email", "a@test.member.dev", "--password", "pw"}, extra...)
	return runContract(t, guardEnv(b), args...)
}

func TestLoginGuard_StoredKeyForAnotherTeam_NonInteractiveRefusesBeforeMinting(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)

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
	b := newGuardBackend(t, guardAlphaTeam)
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
	if b2.sawBearer(guardOldKey) {
		t.Errorf("register sent the stored key to the API (it documents that it sends no key); requests=%v", b2.reqs)
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
	t.Run("a different key is confirmed — without sending the STORED key anywhere", func(t *testing.T) {
		seedStoredKey(t, guardOldKey)
		b := newGuardBackend(t, guardBetaTeam)
		res, err := runResume(t, []string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY=" + guardNewKey}, "login")
		if res.Code != errs.ExitNeedsConfir {
			t.Fatalf("exit = %d, want %d", res.Code, errs.ExitNeedsConfir)
		}
		if got := storedKey(t); got != guardOldKey {
			t.Errorf("stored key = %q, want the old key kept", got)
		}
		if b.sawBearer(guardOldKey) {
			t.Errorf("the STORED key was sent to the API during a login that carries an explicit key; requests=%v", b.reqs)
		}
		if err == nil || !strings.Contains(err.Error(), "the stored API key") {
			t.Errorf("refusal %v must use the generic wording (no lookup was made)", err)
		}
		res = runContract(t, []string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY=" + guardNewKey}, "login", "--yes")
		if res.Code != errs.ExitOK || storedKey(t) != guardNewKey {
			t.Errorf("with --yes the pasted key must be stored: exit=%d stored=%q", res.Code, storedKey(t))
		}
	})
	t.Run("storing the key that is already stored is silent, even if the API reports no team", func(t *testing.T) {
		seedStoredKey(t, "mio_sk_noteam")
		b := newGuardBackend(t, guardAlphaTeam)
		res := runContract(t, []string{"MIO_API_BASE_URL=" + b.srv.URL, "MIO_API_KEY=mio_sk_noteam"}, "login")
		if res.Code != errs.ExitOK {
			t.Fatalf("exit = %d, want 0 (replacing a key with itself needs no question); stderr=%q", res.Code, res.Stderr)
		}
	})
}

func TestLoginGuard_Anonymous_NeverSendsTheStoredKey(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)
	res, err := runResume(t, guardEnv(b), "login", "--anonymous", "--email", "a@test.member.dev", "--password", "pw")
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("password login: exit = %d (err=%v), want 5", res.Code, err)
	}
	res, _ = runResume(t, guardEnv(b), "login", "--anonymous", "--api-key", guardNewKey)
	if res.Code != errs.ExitNeedsConfir {
		t.Fatalf("explicit-key login: exit = %d, want 5", res.Code)
	}
	if b.sawBearer(guardOldKey) {
		t.Errorf("--anonymous login sent the stored key to the API; requests=%v", b.reqs)
	}
	if got := storedKey(t); got != guardOldKey {
		t.Errorf("stored key = %q, want the old key kept", got)
	}
}

func TestLoginGuard_InteractiveAnswers(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	for _, tc := range []struct {
		name, answer string
		wantErr      bool
	}{{"y proceeds", "y\n", false}, {"yes proceeds", "YES\n", false}, {"n aborts", "n\n", true}, {"empty aborts", "\n", true}, {"EOF aborts", "", true}, {"an answer cut short by EOF is not consent", "yes", true}, {"partial parse 'yes no' aborts", "yes no\n", true}} {
		t.Run(tc.name, func(t *testing.T) {
			seedStoredKey(t, guardOldKey)
			b := newGuardBackend(t, guardBetaTeam)
			resetGlobalFlags()
			cmd, errBuf := testCmd("")
			cmd.SetOut(devnull)
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

func TestLoginGuard_PipedYesWithDevNullStdoutDoesNotApprove(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	seedStoredKey(t, guardOldKey)
	b := newGuardBackend(t, guardBetaTeam)
	resetGlobalFlags()
	cmd, _ := testCmd("y\n")
	cmd.SetOut(devnull)
	err = confirmReplaceStoredKey(cmd, b.srv.URL, guardBetaTeam, cmd.InOrStdin(), true)
	if err == nil || errs.CodeOf(err) != errs.ExitNeedsConfir {
		t.Fatalf("err = %v, want exit 5 (a real terminal on stdin is required, or --yes)", err)
	}
}

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
	_, merr := mintAndStore(cmd, cli, token, "", cfg, guard)
	if merr == nil || errs.CodeOf(merr) != errs.ExitNeedsConfir {
		t.Fatalf("a refusing second guard must fail mintAndStore with its own error (exit 5), got %v", merr)
	}
	if calls != 2 {
		t.Fatalf("guard called %d times, want 2 (before the first mint, and after the recovery)", calls)
	}
	if len(*mintedPaths) != 1 {
		t.Errorf("mint attempts = %v, want only the first (the 403'd one): the retry was sent after the refusal", *mintedPaths)
	}
	if got := storedKey(t); got != guardOldKey {
		t.Errorf("stored key = %q, want the old key kept", got)
	}
}

func TestLoginGuard_UnreachableAPI_PromptSaysOnlyThatTheRequestFailed(t *testing.T) {
	seedStoredKey(t, guardOldKey)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
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
