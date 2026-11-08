package cmd

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

type teamRow struct{ id, name, ownerID string }

type mintServerConfig struct {
	teams      []teamRow
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

func testCmd(in string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(in))
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	return cmd, &errBuf
}

func TestMintAndStore_ClaimedTeamNotOwned_RecoversToOwnedTeam(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetGlobalFlags()

	token := makeLoginJWT(t, "team_not_owned")
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

	displayTeam, err := mintAndStore(cmd, cli, token, "", cfg, noStoredKeyGuard)
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

	_, err = mintAndStore(cmd, cli, token, "", cfg, noStoredKeyGuard)
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

	_, err = mintAndStore(cmd, cli, token, "team_explicit", cfg, noStoredKeyGuard)
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

func TestResolveOwnedTeamID_FiltersToTeamsOwnedBySubject(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	token := makeLoginJWT(t, "irrelevant")
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

func TestLogin_ConfiguredCurrentTeamNotOwned_StillRecovers(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	mioDir := filepath.Join(cfgDir, "mio")
	if err := os.MkdirAll(mioDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mioDir, "config.toml"), []byte("current_team = \"team_not_owned\"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	token := makeLoginJWT(t, "team_not_owned")
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

func TestMintAndStore_RecoveryWithMultipleOwnedTeams_DevNullStdinStaysNonInteractive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	resetGlobalFlags()

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	token := makeLoginJWT(t, "team_not_owned")
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
	cmd.SetIn(devNull)
	var errBuf bytes.Buffer
	cmd.SetErr(&errBuf)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	_, err = mintAndStore(cmd, cli, token, "", cfg, noStoredKeyGuard)
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
