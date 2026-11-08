package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func playlistsListServer(t *testing.T, gotQuery *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		if strings.HasSuffix(r.URL.Path, "/playlists") && r.Method == http.MethodGet {
			*gotQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[
				{"id":"pl_a","type":"playlists","attributes":{"title":"A","hub_id":"hub_1"}},
				{"id":"pl_b","type":"playlists","attributes":{"title":"B","hub_id":null}}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlaylistsList_ExplicitHubWarnsAndStaysTeamWide(t *testing.T) {
	var query string
	srv := playlistsListServer(t, &query)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "list", "--hub", "hub_1", "-o", "json")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0 — an unhonourable --hub is a WARNING, not a usage error; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, playlistsListNotHubScopedMsg) {
		t.Errorf("stderr must carry the not-hub-scoped warning; got %q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "hub-playlists list") {
		t.Errorf("the warning must name the hub-scoped alternative; got %q", res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pl_a") || !strings.Contains(res.Stdout, "pl_b") {
		t.Errorf("the team-wide listing must be returned intact; stdout=%q", res.Stdout)
	}
	if strings.Contains(query, "hub") {
		t.Errorf("query = %q, want no hub filter — the API offers none, so sending one is inventing contract", query)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Errorf("stdout must parse as a single JSON value; err=%v stdout=%q", err, res.Stdout)
	}
	if len(parsed) != 2 {
		t.Errorf("stdout listed %d playlists, want 2 (the team-wide set, unfiltered)", len(parsed))
	}
	if strings.Contains(res.Stdout, "warning") {
		t.Errorf("the warning belongs on stderr ONLY — stdout carries the value; stdout=%q", res.Stdout)
	}
}

func TestPlaylistsList_ConfiguredHubDoesNotWarn(t *testing.T) {
	var query string
	srv := playlistsListServer(t, &query)

	res := runContract(t, append(baseEnv(srv.URL), seedConfigHub(t, "019f0000-0000-7000-8000-000000000000")),
		withTeam("t_team1", "media", "playlists", "list", "-o", "json")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, playlistsListNotHubScopedMsg) {
		t.Errorf("a CONFIGURED current_hub is not an explicit --hub: warning on it would fire on every "+
			"invocation for anyone with a default hub, which is how a real signal becomes noise; stderr=%q", res.Stderr)
	}
}

func TestPlaylistsList_NoWarningWithoutAnExplicitHub(t *testing.T) {
	var query string
	srv := playlistsListServer(t, &query)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "list", "-o", "json")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stderr, playlistsListNotHubScopedMsg) {
		t.Errorf("no explicit --hub was passed, so nothing may warn; stderr=%q", res.Stderr)
	}
}
