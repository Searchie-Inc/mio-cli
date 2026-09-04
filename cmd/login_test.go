package cmd

// login_test.go — regression coverage for MIO-3585.
//
// `mio login` used to trust the JWT's team_id claim unconditionally when
// minting the API key. That claim tracks the account's last-active team
// (mio-backend TeamService.resolve_default_team_id) and only requires
// MEMBERSHIP — `teams switch` can point it at a team the caller belongs to
// without owning, and minting an API key is owner-gated server-side
// (require_team_owner). The backend then answered with its raw
// "Caller is not the owner of this team." 403, and the CLI surfaced it
// verbatim instead of recovering.
//
// The fix: mintAndStore retries once, against a team the caller actually
// OWNS (verified via GET /api/teams + comparing owner_id to the JWT's own
// `sub` claim), whenever the first mint attempt 403s and no explicit --team
// was given. An explicit --team is never second-guessed this way.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// teamsListServer wires GET /api/v1/teams to answer with the given teams
// (id, name, owner_id) and reports whether it was ever hit.
type teamRow struct{ id, name, ownerID string }

// mintServerConfig drives a single fake backend for the resolveTeamID/
// mintAndStore flow: a 403-then-200 (or 200 outright) sequence on
// /api-keys, plus a GET /api/v1/teams that answers with teams.
type mintServerConfig struct {
	teams []teamRow
	// mintStatus maps team id -> HTTP status the mint endpoint answers for
	// that team. Any team id absent here 404s (unexpected mint target).
	mintStatus map[string]int
}

func newMintServer(t *testing.T, cfg mintServerConfig) (srv *httptest.Server, mintedPaths *[]string, teamsListed *bool) {
	t.Helper()
	var paths []string
	var listed bool
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.URL.Path == "/api/v1/teams" && r.Method == http.MethodGet:
			listed = true
			data := make([]map[string]any, 0, len(cfg.teams))
			for _, tm := range cfg.teams {
				data = append(data, map[string]any{
					"id":   tm.id,
					"type": "teams",
					"attributes": map[string]any{
						"name":     tm.name,
						"owner_id": tm.ownerID,
					},
				})
			}
			body, _ := json.Marshal(map[string]any{"data": data})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			paths = append(paths, r.URL.Path)
			// Path shape: /api/v1/teams/{id}/api-keys
			parts := strings.Split(r.URL.Path, "/")
			teamID := ""
			if len(parts) >= 5 {
				teamID = parts[4]
			}
			status, ok := cfg.mintStatus[teamID]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"unexpected mint target"}]}`))
				return
			}
			if status == http.StatusForbidden {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"status":"403","detail":"Caller is not the owner of this team."}]}`))
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"data":{"id":"key_1","type":"api_keys","attributes":{"secret":"mio_sk_test_minted"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &paths, &listed
}

// testCmd returns a bare *cobra.Command wired the same way the interactive
// prompt tests in register_test.go drive one directly (no Execute/runContract
// round-trip), with a background context so cmd.Context() is non-nil.
func testCmd(in string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(in))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	return cmd, &errBuf
}

// TestMintAndStore_ClaimedTeamNotOwned_RecoversToOwnedTeam is the core
// MIO-3585 regression: the JWT names a team the caller belongs to but does
// not own, the first mint attempt 403s, and mintAndStore must recover by
// minting under the team the caller DOES own instead of surfacing the raw
// "Caller is not the owner of this team." error.
func TestMintAndStore_ClaimedTeamNotOwned_RecoversToOwnedTeam(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetGlobalFlags() // flags.team="" — the retry gate reads this global directly

	token := makeLoginJWT(t, "team_not_owned") // sub="user-test" (see makeLoginJWT)
	srv, mintedPaths, teamsListed := newMintServer(t, mintServerConfig{
		teams: []teamRow{
			{id: "team_not_owned", name: "Shared Team", ownerID: "someone-else"},
			{id: "team_owned", name: "My Team", ownerID: "user-test"},
		},
		mintStatus: map[string]int{
			"team_not_owned": http.StatusForbidden,
			"team_owned":     http.StatusCreated,
		},
	})

	cli := client.New(srv.URL, "", client.WithHTTPClient(srv.Client()))
	cmd, errBuf := testCmd("")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	displayTeam, err := mintAndStore(cmd, cli, token, "", cfg)
	if err != nil {
		t.Fatalf("mintAndStore error: %v", err)
	}
	if !strings.Contains(displayTeam, "team_owned") {
		t.Errorf("displayTeam = %q, want it to name team_owned", displayTeam)
	}
	if !*teamsListed {
		t.Error("GET /api/v1/teams was never called — recovery path did not run")
	}
	want := []string{"/api/v1/teams/team_not_owned/api-keys", "/api/v1/teams/team_owned/api-keys"}
	if len(*mintedPaths) != 2 || (*mintedPaths)[0] != want[0] || (*mintedPaths)[1] != want[1] {
		t.Errorf("mint attempts = %v, want %v (claimed team first, then the owned team)", *mintedPaths, want)
	}
	if !strings.Contains(errBuf.String(), "isn't one you own") {
		t.Errorf("stderr = %q, want a note explaining the recovery", errBuf.String())
	}
	if cfg.CurrentTeam != "team_owned" {
		t.Errorf("cfg.CurrentTeam = %q, want team_owned", cfg.CurrentTeam)
	}
}

// TestMintAndStore_ClaimedTeamNotOwned_NoOwnedTeamAtAll pins the case where
// the caller owns NO team at all (only membership elsewhere): mintAndStore
// must not retry blindly — it must surface a clear, actionable error instead
// of a second doomed mint attempt.
func TestMintAndStore_ClaimedTeamNotOwned_NoOwnedTeamAtAll(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetGlobalFlags()

	token := makeLoginJWT(t, "team_not_owned")
	srv, mintedPaths, _ := newMintServer(t, mintServerConfig{
		teams: []teamRow{
			{id: "team_not_owned", name: "Shared Team", ownerID: "someone-else"},
		},
		mintStatus: map[string]int{
			"team_not_owned": http.StatusForbidden,
		},
	})

	cli := client.New(srv.URL, "", client.WithHTTPClient(srv.Client()))
	cmd, _ := testCmd("")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	_, err = mintAndStore(cmd, cli, token, "", cfg)
	if err == nil {
		t.Fatal("mintAndStore must fail when the caller owns no team")
	}
	if got := errs.CodeOf(err); got != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth)", got, errs.ExitAuth)
	}
	if !strings.Contains(err.Error(), "--team") {
		t.Errorf("error = %q, want it to point at --team as the way out", err.Error())
	}
	if len(*mintedPaths) != 1 {
		t.Errorf("mint attempts = %v, want exactly one (no blind retry with no owned team)", *mintedPaths)
	}
}

// TestMintAndStore_ExplicitTeamFlagNotOwned_DoesNotRetry pins that an
// explicit --team is never second-guessed: a 403 against a caller-supplied
// team must propagate as-is, with no GET /api/teams recovery attempt. The
// retry gate reads the flags.team GLOBAL directly (not the flagTeamID
// parameter, which login also feeds from a config-resolved value) — see
// TestLogin_ConfiguredCurrentTeamNotOwned_StillRecovers for why that
// distinction matters — so this test must set flags.team itself to exercise
// a genuinely explicit flag.
func TestMintAndStore_ExplicitTeamFlagNotOwned_DoesNotRetry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetGlobalFlags()
	flags.team = "team_explicit"
	t.Cleanup(resetGlobalFlags)

	token := makeLoginJWT(t, "team_claim_irrelevant")
	srv, mintedPaths, teamsListed := newMintServer(t, mintServerConfig{
		teams: []teamRow{
			{id: "team_owned", name: "My Team", ownerID: "user-test"},
		},
		mintStatus: map[string]int{
			"team_explicit": http.StatusForbidden,
		},
	})

	cli := client.New(srv.URL, "", client.WithHTTPClient(srv.Client()))
	cmd, _ := testCmd("")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	_, err = mintAndStore(cmd, cli, token, "team_explicit", cfg)
	if err == nil {
		t.Fatal("mintAndStore must fail when the explicit --team is rejected")
	}
	if got := errs.HTTPStatusOf(err); got != http.StatusForbidden {
		t.Errorf("HTTPStatusOf = %d, want 403 (the backend's own rejection, unmodified)", got)
	}
	if !strings.Contains(err.Error(), "Caller is not the owner") {
		t.Errorf("error = %q, want the backend's own detail preserved", err.Error())
	}
	if *teamsListed {
		t.Error("GET /api/v1/teams must NOT be called — an explicit --team is never second-guessed")
	}
	if len(*mintedPaths) != 1 || (*mintedPaths)[0] != "/api/v1/teams/team_explicit/api-keys" {
		t.Errorf("mint attempts = %v, want exactly one against team_explicit", *mintedPaths)
	}
}

// TestResolveOwnedTeamID_FiltersToTeamsOwnedBySubject is a focused unit test
// on the recovery helper itself: given a mix of owned and merely-member-of
// teams, it must pick out only the one whose owner_id matches the JWT's sub.
func TestResolveOwnedTeamID_FiltersToTeamsOwnedBySubject(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	token := makeLoginJWT(t, "irrelevant") // sub="user-test"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if r.URL.Path != "/api/v1/teams" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := json.Marshal(map[string]any{"data": []map[string]any{
			{"id": "t_member", "type": "teams", "attributes": map[string]any{"name": "Member Only", "owner_id": "someone-else"}},
			{"id": "t_owned", "type": "teams", "attributes": map[string]any{"name": "Mine", "owner_id": "user-test"}},
		}})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	cli := client.New(srv.URL, "", client.WithHTTPClient(srv.Client()))
	cmd, _ := testCmd("")

	id, name, err := resolveOwnedTeamID(cmd, cli, token)
	if err != nil {
		t.Fatalf("resolveOwnedTeamID error: %v", err)
	}
	if id != "t_owned" {
		t.Errorf("id = %q, want t_owned (the caller's owner_id match)", id)
	}
	if name != "Mine" {
		t.Errorf("name = %q, want Mine", name)
	}
}

// TestLogin_ConfiguredCurrentTeamNotOwned_StillRecovers pins the scenario
// MIO-3585 actually names: a PRIOR `mio login`/`teams switch` left
// current_team in config pointing at a team the caller belongs to but does
// not own (the same shape as the JWT's stale team_id claim). Without an
// explicit --team flag on THIS invocation, `login` must still recover via
// resolveOwnedTeamID rather than silently disabling the fix — mintAndStore's
// retry gate must key off the actual --team flag, not the config-resolved
// team, or a stored current_team quietly reproduces the pre-fix 403.
func TestLogin_ConfiguredCurrentTeamNotOwned_StillRecovers(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	// Seed a stale current_team pointing at a team the account does not own —
	// exactly what `mio teams switch <shared-team>` would leave behind.
	mioDir := filepath.Join(cfgDir, "mio")
	if err := os.MkdirAll(mioDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mioDir, "config.toml"), []byte("current_team = \"team_not_owned\"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	token := makeLoginJWT(t, "team_not_owned") // sub="user-test"; claim agrees with the stale config
	var mintedPaths []string
	var teamsListed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.URL.Path == "/api/v1/auth/login" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			resp, _ := json.Marshal(map[string]any{"access_token": token, "token_type": "bearer"})
			_, _ = w.Write(resp)
		case r.URL.Path == "/api/v1/teams" && r.Method == http.MethodGet:
			teamsListed = true
			body, _ := json.Marshal(map[string]any{"data": []map[string]any{
				{"id": "team_not_owned", "type": "teams", "attributes": map[string]any{"name": "Shared Team", "owner_id": "someone-else"}},
				{"id": "team_owned", "type": "teams", "attributes": map[string]any{"name": "My Team", "owner_id": "user-test"}},
			}})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case strings.HasSuffix(r.URL.Path, "/api-keys") && r.Method == http.MethodPost:
			mintedPaths = append(mintedPaths, r.URL.Path)
			if strings.Contains(r.URL.Path, "team_not_owned") {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":[{"status":"403","detail":"Caller is not the owner of this team."}]}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"id":"key_1","type":"api_keys","attributes":{"secret":"mio_sk_recovered"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"status":"404","detail":"not found"}]}`))
		}
	}))
	t.Cleanup(srv.Close)

	// No --team on this invocation: config's stale current_team must not be
	// treated as an explicit choice that blocks recovery.
	env := []string{
		"MIO_API_BASE_URL=" + srv.URL,
		"MIO_API_KEY=",
	}
	res := runContract(t, env, "login", "--email", "alice@test.member.dev", "--password", "s3cr3t")

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q stdout=%q", res.Code, errs.ExitOK, res.Stderr, res.Stdout)
	}
	if !teamsListed {
		t.Error("GET /api/v1/teams was never called — recovery did not run despite no explicit --team flag")
	}
	want := []string{"/api/v1/teams/team_not_owned/api-keys", "/api/v1/teams/team_owned/api-keys"}
	if len(mintedPaths) != 2 || mintedPaths[0] != want[0] || mintedPaths[1] != want[1] {
		t.Errorf("mint attempts = %v, want %v", mintedPaths, want)
	}
}

// TestMintAndStore_RecoveryWithMultipleOwnedTeams_DevNullStdinStaysNonInteractive
// pins the review-round-2 finding: the ownership-403 recovery's "multiple
// owned teams" branch must behave as NON-interactive when stdin is /dev/null
// — the ordinary headless/CI/container stdin, and what cmd.InOrStdin()
// defaults to in production when no one has called cmd.SetIn (e.g. every
// real `mio login --email --password` invocation). /dev/null is a character
// device, which root.go's isTTY incorrectly treats as a terminal; before the
// isInteractiveStdin fix this branch would read /dev/null as if it were a
// TTY, prompt, read immediate EOF, and fail with a bare "invalid choice"
// instead of the documented "you own multiple teams — re-run with --team
// <id>:" listing. A *strings.Reader (this file's other tests, via testCmd)
// can't reproduce this: it isn't an *os.File at all, so it already fails
// isTTY's type assertion regardless of which check is used — only a REAL
// character-device file exposes the distinction.
func TestMintAndStore_RecoveryWithMultipleOwnedTeams_DevNullStdinStaysNonInteractive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetGlobalFlags()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	token := makeLoginJWT(t, "team_not_owned") // sub="user-test"
	srv, mintedPaths, teamsListed := newMintServer(t, mintServerConfig{
		teams: []teamRow{
			{id: "team_not_owned", name: "Shared Team", ownerID: "someone-else"},
			{id: "team_x", name: "X", ownerID: "user-test"},
			{id: "team_y", name: "Y", ownerID: "user-test"},
		},
		mintStatus: map[string]int{"team_not_owned": http.StatusForbidden},
	})

	cli := client.New(srv.URL, "", client.WithHTTPClient(srv.Client()))
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(devNull) // a REAL *os.File, char device, NOT a terminal
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	_, err = mintAndStore(cmd, cli, token, "", cfg)
	if err == nil {
		t.Fatal("mintAndStore must fail when the caller owns multiple teams and none was chosen")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
	if strings.Contains(err.Error(), "invalid choice") {
		t.Errorf("error = %q — /dev/null stdin must never be treated as an interactive TTY", err.Error())
	}
	if !strings.Contains(err.Error(), "you own multiple teams") || !strings.Contains(err.Error(), "team_x") || !strings.Contains(err.Error(), "team_y") {
		t.Errorf("error = %q, want the non-interactive multi-team listing naming team_x and team_y", err.Error())
	}
	if !*teamsListed {
		t.Error("GET /api/v1/teams was never called")
	}
	if len(*mintedPaths) != 1 || (*mintedPaths)[0] != "/api/v1/teams/team_not_owned/api-keys" {
		t.Errorf("mint attempts = %v, want exactly one against team_not_owned", *mintedPaths)
	}
}
