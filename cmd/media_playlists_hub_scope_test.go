package cmd

// media_playlists_hub_scope_test.go — MIO-3100: `media playlists list` accepts
// the global --hub and cannot honour it (the team-playlists route has no hub
// filter), so it says so instead of silently returning a team-wide listing the
// operator believes is scoped.
//
// The oracle is BOTH streams at once: the warning on stderr AND an unchanged,
// still-parseable listing on stdout with exit 0. Asserting the warning alone
// would pass over a "fix" that broke the listing or the exit code; asserting
// the listing alone is what the pre-MIO-3100 behaviour already satisfied.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// playlistsListServer answers the team-playlists listing and records the query
// it was sent, so a client-side filter smuggled in as a "fix" is visible.
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
	// The listing itself is unchanged: BOTH playlists, including the one whose
	// hub_id is null. A client-side filter would be the wrong fix — the route is
	// keyset-paginated, so filtering a page under-reports without saying so.
	if !strings.Contains(res.Stdout, "pl_a") || !strings.Contains(res.Stdout, "pl_b") {
		t.Errorf("the team-wide listing must be returned intact; stdout=%q", res.Stdout)
	}
	// …and no invented filter reached the wire.
	if strings.Contains(query, "hub") {
		t.Errorf("query = %q, want no hub filter — the API offers none, so sending one is inventing contract", query)
	}
	// stdout must remain ONE parseable JSON value. A substring check alone would
	// pass a "fix" that echoed the warning to stdout as well as stderr, which is
	// exactly what breaks the `--jq` pipelines the agent contract promises.
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

// TestPlaylistsList_ConfiguredHubDoesNotWarn is THE discriminating case, and the
// one the sibling below cannot cover: a config-file current_hub with no --hub on
// the command line.
//
// Without this, "keys on the explicit flag" is an untested claim — a fix that
// warned on the RESOLVED hub passes every other test in this file, because they
// all run with no configured hub at all, so explicit and resolved are both
// empty. That is the "probe set smaller than the claim" shape in
// .claude/rules/verifying-guards.md, and it was in this file until codex-review
// caught it.
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

// The warning keys on the EXPLICIT flag, never the resolved hub: a configured
// current_hub would otherwise warn on every invocation, which trains people to
// ignore it. This is the arm that makes the guard discriminating — a fix that
// warned on the resolved value would satisfy the test above and fail here.
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
