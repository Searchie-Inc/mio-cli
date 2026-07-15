package cmd

// media_playlist_cover_test.go — contract test for `mio media playlists set-cover`
// (MIO-2289). Sets a playlist's cover by POSTing a playlist-cover-attachment
// (type "attachments") pinned to target_type=playlist / role=thumbnail.

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestPlaylistSetCover_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusCreated,
		`{"data":{"type":"attachments","id":"att_x","attributes":{"role":"thumbnail"}}}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "set-cover", "pl_1", "--media-id", "file_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/teams/t_team1/playlist-cover-attachments"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "attachments" {
		t.Errorf("type=%q want attachments", typ)
	}
	if attrs["media_id"] != "file_x" {
		t.Errorf("media_id=%v want file_x", attrs["media_id"])
	}
	if attrs["target_type"] != "playlist" {
		t.Errorf("target_type=%v want playlist", attrs["target_type"])
	}
	if attrs["target_id"] != "pl_1" {
		t.Errorf("target_id=%v want pl_1", attrs["target_id"])
	}
	if attrs["role"] != "thumbnail" {
		t.Errorf("role=%v want thumbnail", attrs["role"])
	}
}

func TestPlaylistSetCover_RequiresMediaID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "set-cover", "pl_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --media-id must exit before any HTTP request")
	}
}
