package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// registerMintServer returns a mock backend that answers the register + mint
// endpoints of the `mio register` auto-login flow. It records the register
// request body and whether the api-keys (mint) endpoint was reached.
//
// The access token carries a team_id claim (via makeLoginJWT) so the team
// resolves from the JWT without a GET /api/teams round-trip — mirroring how the
// backend auto-provisions a personal team on registration.
func registerMintServer(t *testing.T, teamID string, regBody *map[string]any, mintReached *bool) *httptest.Server {
	t.Helper()
	token := makeLoginJWT(t, teamID)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/register" && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(regBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			resp, _ := json.Marshal(map[string]any{
				"access_token": token,
				"token_type":   "Bearer",
				"expires_in":   900,
			})
			_, _ = w.Write(resp)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			*mintReached = true
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"key_1","type":"api_keys","attributes":{"secret":"mio_sk_registertest123"}}}`))
		default:
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRegister_HeadlessFullFlow pins the end-to-end contract of `mio register`
// with --email/--password/--first-name/--last-name: it POSTs a FLAT plain-JSON
// body to /api/v1/auth/register, then auto-logs-in by minting + storing a key,
// and confirms both registration and login in its stdout.
func TestRegister_HeadlessFullFlow(t *testing.T) {
	// Sandbox the config dir so the minted key + current_team land in a temp dir,
	// not the developer's real config. As with TestLogin_HeadlessFlags_FullMintFlow,
	// config.SetAPIKey falls back to the encrypted file keyring here (no native
	// keychain on CI); the test asserts a non-error outcome regardless of backend.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var regBody map[string]any
	mintReached := false
	srv := registerMintServer(t, "t_reg", &regBody, &mintReached)

	env := []string{
		"MIO_API_BASE_URL=" + srv.URL,
		"MIO_API_KEY=", // register is unauthenticated — no stored key required
	}
	res := runContract(t, env,
		"register",
		"--email", "newuser@test.member.dev",
		"--password", "s3cr3tpass",
		"--first-name", "Ada",
		"--last-name", "Lovelace",
	)

	if res.Code != errs.ExitOK {
		t.Fatalf("register full flow: exit code = %d, want %d (ExitOK); stderr=%q stdout=%q",
			res.Code, errs.ExitOK, res.Stderr, res.Stdout)
	}
	if !mintReached {
		t.Error("register must auto-login: the api-keys mint endpoint was never reached")
	}
	// Flat plain-JSON body: fields at top level, no JSON:API `data` envelope.
	if _, hasData := regBody["data"]; hasData {
		t.Errorf("register body must be flat plain JSON, not a `data` envelope: %#v", regBody)
	}
	if regBody["email"] != "newuser@test.member.dev" {
		t.Errorf("register body email = %v, want newuser@test.member.dev", regBody["email"])
	}
	if regBody["password"] != "s3cr3tpass" {
		t.Errorf("register body password = %v, want s3cr3tpass", regBody["password"])
	}
	if regBody["first_name"] != "Ada" {
		t.Errorf("register body first_name = %v, want Ada", regBody["first_name"])
	}
	if regBody["last_name"] != "Lovelace" {
		t.Errorf("register body last_name = %v, want Lovelace", regBody["last_name"])
	}
	// stdout confirms BOTH the registration and the auto-login.
	if !strings.Contains(res.Stdout, "newuser@test.member.dev") {
		t.Errorf("register stdout %q does not mention the email address", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "Registered") {
		t.Errorf("register stdout %q does not confirm registration", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "Logged in") {
		t.Errorf("register stdout %q does not confirm auto-login", res.Stdout)
	}
}

// TestRegister_OmitsEmptyNames verifies that first_name/last_name are absent
// from the wire body when their flags are not supplied (backend fields optional).
func TestRegister_OmitsEmptyNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var regBody map[string]any
	mintReached := false
	srv := registerMintServer(t, "t_reg", &regBody, &mintReached)

	env := []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY="}
	res := runContract(t, env,
		"register",
		"--email", "noname@test.member.dev",
		"--password", "s3cr3tpass",
	)
	if res.Code != errs.ExitOK {
		t.Fatalf("register (no names): exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if _, has := regBody["first_name"]; has {
		t.Errorf("first_name must be omitted when --first-name is unset; body: %#v", regBody)
	}
	if _, has := regBody["last_name"]; has {
		t.Errorf("last_name must be omitted when --last-name is unset; body: %#v", regBody)
	}
}

// TestRegister_HeadlessEnvVars pins that MIO_EMAIL + MIO_PASSWORD activate the
// headless register path (same env fallbacks as `mio login`).
func TestRegister_HeadlessEnvVars(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var regBody map[string]any
	mintReached := false
	srv := registerMintServer(t, "t_reg", &regBody, &mintReached)

	env := []string{
		"MIO_API_BASE_URL=" + srv.URL,
		"MIO_API_KEY=",
		"MIO_EMAIL=envuser@test.member.dev",
		"MIO_PASSWORD=s3cr3tpass",
	}
	res := runContract(t, env, "register")

	if res.Code != errs.ExitOK {
		t.Fatalf("register via env: exit code = %d, want %d (ExitOK); stderr=%q stdout=%q",
			res.Code, errs.ExitOK, res.Stderr, res.Stdout)
	}
	if regBody["email"] != "envuser@test.member.dev" {
		t.Errorf("register body email = %v, want envuser@test.member.dev (from MIO_EMAIL)", regBody["email"])
	}
	if !mintReached {
		t.Error("register via env must reach the mint endpoint")
	}
}

// TestRegister_RequiresEmailAndPassword pins that a missing email or password
// exits 2 (ExitUsage) and fires NO HTTP request — malformed input never touches
// the network.
func TestRegister_RequiresEmailAndPassword(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing email", []string{"--password", "s3cr3tpass"}},
		{"missing password", []string{"--email", "a@test.member.dev"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			srv, fired := firedServer(t)

			// Ensure no env-var credentials leak in and satisfy the missing flag.
			env := []string{
				"MIO_API_BASE_URL=" + srv.URL,
				"MIO_API_KEY=",
				"MIO_EMAIL=",
				"MIO_PASSWORD=",
			}
			args := append([]string{"register"}, tc.args...)
			res := runContract(t, env, args...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("no HTTP request must fire when email/password are missing")
			}
		})
	}
}

// TestRegister_ConflictSurfaces pins that a 409 (email already registered) exits
// 2 (ExitUsage), surfaces the backend's detail, and does NOT proceed to mint a
// key (the account was not created).
func TestRegister_ConflictSurfaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	mintReached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/register":
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errors":[{"status":"409","title":"ConflictError","detail":"Email 'taken@test.member.dev' is already registered."}]}`))
		case strings.HasSuffix(r.URL.Path, "/api-keys"):
			mintReached = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"k","type":"api_keys","attributes":{"secret":"x"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	env := []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY="}
	res := runContract(t, env,
		"register",
		"--email", "taken@test.member.dev",
		"--password", "s3cr3tpass",
	)

	if res.Code != errs.ExitUsage {
		t.Errorf("409 exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if mintReached {
		t.Error("mint must NOT be attempted after a failed registration")
	}
	// The in-process harness surfaces the error as an exit code, not stderr text
	// (main.go renders the JSON:API envelope on the os.Exit path). The precise
	// "already registered" wording is asserted at the client layer in
	// TestClient_RegisterSurfacesConflict. Here we only require a clean failure.
	if strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("error path must produce no stdout; got %q", res.Stdout)
	}
}

// TestRegister_PromptConfirmMismatch verifies that the interactive prompt flow
// rejects a mismatched password confirmation with a usage error and never
// proceeds. It drives promptRegistration directly with a scripted (non-TTY)
// reader — the confirmation reads from the SAME shared reader, so no input is
// lost between the two secret reads.
func TestRegister_PromptConfirmMismatch(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("secretpass\nDIFFERENT\n"))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	_, _, _, _, err := promptRegistration(cmd, "a@b.com", "", "")
	if err == nil {
		t.Fatal("expected an error for mismatched password confirmation")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("mismatch exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
	if !strings.Contains(err.Error(), "do not match") {
		t.Errorf("error = %q, want it to mention passwords do not match", err.Error())
	}
}

// TestRegister_PromptSuccess verifies the happy interactive path: a matching
// confirmation is accepted and the optional names are read from the SAME reader
// (proving the shared-reader fix — a fresh per-call bufio would have lost the
// confirmation and name lines to the password read's buffer).
func TestRegister_PromptSuccess(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("secretpass\nsecretpass\nAda\nLovelace\n"))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	// email supplied up front → no email prompt is consumed from the reader.
	email, password, firstName, lastName, err := promptRegistration(cmd, "a@b.com", "", "")
	if err != nil {
		t.Fatalf("promptRegistration error: %v", err)
	}
	if email != "a@b.com" {
		t.Errorf("email = %q, want a@b.com", email)
	}
	if password != "secretpass" {
		t.Errorf("password = %q, want secretpass", password)
	}
	if firstName != "Ada" {
		t.Errorf("firstName = %q, want Ada", firstName)
	}
	if lastName != "Lovelace" {
		t.Errorf("lastName = %q, want Lovelace", lastName)
	}
}

// TestRegister_TeamFlagOverridesClaim pins that an explicit --team wins over the
// team_id embedded in the register token claim when choosing the mint target —
// the documented "honors the global --team for the mint step" behavior. The
// token claims team t_claim; --team t_explicit must be where the key is minted.
func TestRegister_TeamFlagOverridesClaim(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	token := makeLoginJWT(t, "t_claim")
	var mintPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/register" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			resp, _ := json.Marshal(map[string]any{"access_token": token, "token_type": "Bearer"})
			_, _ = w.Write(resp)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			mintPath = r.URL.Path
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"k","type":"api_keys","attributes":{"secret":"mio_sk_teamflag"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	env := []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY="}
	res := runContract(t, env,
		withTeam("t_explicit",
			"register",
			"--email", "teamflag@test.member.dev",
			"--password", "s3cr3tpass",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if mintPath != "/api/v1/teams/t_explicit/api-keys" {
		t.Errorf("mint path = %q, want /api/v1/teams/t_explicit/api-keys (--team must override the token claim)", mintPath)
	}
}

// TestRegister_PromptReadsEmailWhenMissing exercises the interactive email
// prompt: with no email supplied up front, promptRegistration must consume the
// FIRST reader line as the email, then password, confirmation, and (empty)
// optional names — proving the shared-reader line ordering.
func TestRegister_PromptReadsEmailWhenMissing(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("prompted@test.member.dev\nsecretpass\nsecretpass\n\n\n"))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)

	email, password, firstName, lastName, err := promptRegistration(cmd, "", "", "")
	if err != nil {
		t.Fatalf("promptRegistration error: %v", err)
	}
	if email != "prompted@test.member.dev" {
		t.Errorf("email = %q, want prompted@test.member.dev (read from the first line)", email)
	}
	if password != "secretpass" {
		t.Errorf("password = %q, want secretpass", password)
	}
	if firstName != "" || lastName != "" {
		t.Errorf("names = %q/%q, want empty (blank lines)", firstName, lastName)
	}
}

// TestRegister_IgnoresConfiguredTeam pins that register mints under the NEWLY
// registered account's own team (from its token claim), NOT a stale current_team
// left in config by a prior login. Without --team, register must not inherit the
// configured team — the new account does not belong to it, so minting there would
// 403 and break "register + auto-login always".
func TestRegister_IgnoresConfiguredTeam(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	// Seed a stale current_team as if a prior `mio login` configured this machine.
	mioDir := filepath.Join(cfgDir, "mio")
	if err := os.MkdirAll(mioDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mioDir, "config.toml"), []byte("current_team = \"t_stale\"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	token := makeLoginJWT(t, "t_fresh") // the new account's OWN team
	var mintPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/auth/register" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			resp, _ := json.Marshal(map[string]any{"access_token": token, "token_type": "Bearer"})
			_, _ = w.Write(resp)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			mintPath = r.URL.Path
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"k","type":"api_keys","attributes":{"secret":"mio_sk_fresh"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	// No --team passed: the configured current_team ("t_stale") must NOT be used.
	env := []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY="}
	res := runContract(t, env,
		"register",
		"--email", "fresh@test.member.dev",
		"--password", "s3cr3tpass",
	)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if mintPath != "/api/v1/teams/t_fresh/api-keys" {
		t.Errorf("mint path = %q, want /api/v1/teams/t_fresh/api-keys — register must use the new account's token team, not the stale configured current_team", mintPath)
	}
}
