package cmd

// media_playlist_items_test.go — contract tests for
// `mio media playlists items {add,list,remove,reorder}` (MIO-2513).
//
// add/reorder derive the JSON:API type "playlist_items" from the
// .../playlists/{id}/items tail via the playlists/items typeOverride (the bare
// "items" segment would otherwise resolve to the "playlists" parent type).
// reorder uses UpdateWithID so PlaylistItemUpdateData gets data.id in the body.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const playlistItemResourceBody = `{"data":{"type":"playlist_items","id":"it_1","attributes":{"playlist_id":"pl_1","file_id":"file_x","position":0}}}`

func TestPlaylistItemsAdd_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusCreated, playlistItemResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "add",
			"--playlist-id", "pl_1", "--file-id", "file_x", "--position", "2")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/teams/t_team1/playlists/pl_1/items"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "playlist_items" {
		t.Errorf("type=%q want playlist_items", typ)
	}
	if attrs["file_id"] != "file_x" {
		t.Errorf("file_id=%v want file_x", attrs["file_id"])
	}
	if attrs["position"] != float64(2) {
		t.Errorf("position=%v want 2", attrs["position"])
	}
}

func TestPlaylistItemsAdd_OmitsPositionWhenUnset(t *testing.T) {
	srv, _, _, _, body := captureAdminReq(t, http.StatusCreated, playlistItemResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "add",
			"--playlist-id", "pl_1", "--file-id", "file_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *body)
	if _, ok := attrs["position"]; ok {
		t.Errorf("position should be omitted when --position is unset, got %v", attrs["position"])
	}
}

func TestPlaylistItemsAdd_RequiresPlaylistID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "add", "--file-id", "file_x")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --playlist-id must exit before any HTTP request")
	}
}

func TestPlaylistItemsAdd_RequiresFileID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "add", "--playlist-id", "pl_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --file-id must exit before any HTTP request")
	}
}

func TestPlaylistItemsAdd_RejectsNegativePosition(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "add",
			"--playlist-id", "pl_1", "--file-id", "file_x", "--position", "-1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("negative --position must exit before any HTTP request")
	}
}

func TestPlaylistItemsReorder_RejectsNegativePosition(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "reorder", "it_1",
			"--playlist-id", "pl_1", "--position", "-1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("negative --position must exit before any HTTP request")
	}
}

func TestPlaylistItemsList_Path(t *testing.T) {
	srv, method, path, rawQuery, _ := captureAdminReq(t, http.StatusOK, `{"data":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "list",
			"--playlist-id", "pl_1", "--limit", "25")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/playlists/pl_1/items"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	q, err := url.ParseQuery(*rawQuery)
	if err != nil {
		t.Fatalf("bad query %q: %v", *rawQuery, err)
	}
	if q.Get("page[size]") != "25" {
		t.Errorf("page[size]=%q want 25", q.Get("page[size]"))
	}
}

func TestPlaylistItemsList_RequiresPlaylistID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "list")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --playlist-id must exit before any HTTP request")
	}
}

func TestPlaylistItemsReorder_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, playlistItemResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "reorder", "it_1",
			"--playlist-id", "pl_1", "--position", "3")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch {
		t.Errorf("method=%q want PATCH", *method)
	}
	if want := "/api/v1/teams/t_team1/playlists/pl_1/items/it_1"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "playlist_items" {
		t.Errorf("type=%q want playlist_items", typ)
	}
	if attrs["position"] != float64(3) {
		t.Errorf("position=%v want 3", attrs["position"])
	}
	// PlaylistItemUpdateData requires data.id in the body (backend rejects otherwise).
	var doc struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*body, &doc); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if doc.Data.ID != "it_1" {
		t.Errorf("data.id = %q, want it_1 (backend requires it in the body)", doc.Data.ID)
	}
}

func TestPlaylistItemsReorder_RequiresPosition(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "reorder", "it_1",
			"--playlist-id", "pl_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --position must exit before any HTTP request")
	}
}

func TestPlaylistItemsReorder_RequiresPlaylistID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "reorder", "it_1",
			"--position", "3")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --playlist-id must exit before any HTTP request")
	}
}

func TestPlaylistItemsRemove_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusNoContent, "")
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "remove", "it_1",
			"--playlist-id", "pl_1", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodDelete {
		t.Errorf("method=%q want DELETE", *method)
	}
	if want := "/api/v1/teams/t_team1/playlists/pl_1/items/it_1"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestPlaylistItemsRemove_RequiresPlaylistID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "items", "remove", "it_1", "--yes")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --playlist-id must exit before any HTTP request")
	}
}
