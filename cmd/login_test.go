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
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
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
// team must propagate as-is, with no GET /api/teams recovery attempt.
func TestMintAndStore_ExplicitTeamFlagNotOwned_DoesNotRetry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
