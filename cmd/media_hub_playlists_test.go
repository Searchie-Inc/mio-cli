package cmd

// media_hub_playlists_test.go — contract tests for `mio media hub-playlists`
// {publish,list,unpublish} (MIO-2259). Publishing a playlist to a hub writes a
// hub_media row — the load-bearing record the /content grid and homepage
// content-grid join on. The write path derives the JSON:API type "hub_media"
// (not "playlists") from the .../hubs/{hub}/playlists tail via a typeOverride.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// decodeDataTypeAttrs returns data.type and data.attributes from a request body.
func decodeDataTypeAttrs(t *testing.T, body []byte) (string, map[string]any) {
	t.Helper()
	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, body)
	}
	return doc.Data.Type, doc.Data.Attributes
}

// TestMediaHubPlaylistsPublish_Body verifies the publish POST hits
// .../hubs/{hub}/playlists with JSON:API type "hub_media" and the mapped
// attributes.
func TestMediaHubPlaylistsPublish_Body(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"media", "hub-playlists", "publish",
			"--playlist-id", "pl_abc",
			"--visibility", "public",
			"--position", "2",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/playlists") {
		t.Errorf("path %q does not end with /hubs/hub_123/playlists", *gotPath)
	}

	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_media" {
		t.Errorf("data.type = %q, want hub_media (typeOverride hubs/playlists)", typ)
	}
	if attrs["playlist_id"] != "pl_abc" {
		t.Errorf("playlist_id = %v, want pl_abc", attrs["playlist_id"])
	}
	if attrs["visibility"] != "public" {
		t.Errorf("visibility = %v, want public", attrs["visibility"])
	}
	if attrs["position"] != float64(2) {
		t.Errorf("position = %#v, want 2", attrs["position"])
	}
	// --playlist-id must NOT leak as the wrong key.
	if _, ok := attrs["playlist-id"]; ok {
		t.Errorf("attributes.playlist-id must not be present; got %v", attrs)
	}
}

// TestMediaHubPlaylistsPublish_RequiresPlaylistID verifies a missing
// --playlist-id exits ExitUsage with no request.
func TestMediaHubPlaylistsPublish_RequiresPlaylistID(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"media", "hub-playlists", "publish",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("missing --playlist-id must exit before any HTTP request")
	}
}

// TestMediaHubPlaylistsPublish_RejectsEmptyPlaylistID verifies an explicitly
// empty --playlist-id (not just an absent flag) is rejected with ExitUsage and
// fires no request (would otherwise POST playlist_id="").
func TestMediaHubPlaylistsPublish_RejectsEmptyPlaylistID(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"media", "hub-playlists", "publish", "--playlist-id", "  ",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an empty --playlist-id must exit before any HTTP request")
	}
}

// TestMediaHubPlaylistsPublish_RejectsInvalidVisibility verifies an
// out-of-enum --visibility is rejected client-side with no request.
func TestMediaHubPlaylistsPublish_RejectsInvalidVisibility(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"media", "hub-playlists", "publish",
			"--playlist-id", "pl_abc", "--visibility", "everyone",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("invalid --visibility must exit before any HTTP request")
	}
}

// TestMediaHubPlaylistsUnpublish_DeletesPath verifies unpublish sends a DELETE
// to .../hubs/{hub}/playlists/{playlist_id} (with --yes to skip the prompt).
func TestMediaHubPlaylistsUnpublish_DeletesPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"media", "hub-playlists", "unpublish", "pl_abc", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/playlists/pl_abc") {
		t.Errorf("path %q does not end with /hubs/hub_123/playlists/pl_abc", gotPath)
	}
}
