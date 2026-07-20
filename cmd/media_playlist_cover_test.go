package cmd

// media_playlist_cover_test.go — contract test for `mio media playlists set-cover`
// (MIO-2289, MIO-2519). Sets a playlist's cover by first resolving the file's
// media_id (GET /files/{id}) then POSTing a playlist-cover-attachment (type
// "attachments") pinned to target_type=playlist / role=thumbnail. The media_id
// on the wire is the RESOLVED Media PK, never the file id.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestPlaylistSetCover_ResolvesFileMediaID(t *testing.T) {
	var getHit, postHit bool
	var postBody []byte
	var postPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/teams/t_team1/files/file_x":
			getHit = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"file_x","type":"files","attributes":{"title":"Clip","media_id":"media_pk_1"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/teams/t_team1/playlist-cover-attachments":
			postHit = true
			postBody, _ = io.ReadAll(r.Body)
			postPath = r.URL.Path
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"type":"attachments","id":"att_x","attributes":{"role":"thumbnail"}}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "set-cover", "pl_1", "--file-id", "file_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !getHit || !postHit {
		t.Fatalf("get=%v post=%v — both must fire", getHit, postHit)
	}
	if want := "/api/v1/teams/t_team1/playlist-cover-attachments"; postPath != want {
		t.Errorf("path=%q want %q", postPath, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, postBody)
	if typ != "attachments" {
		t.Errorf("type=%q want attachments", typ)
	}
	// The RESOLVED Media PK must be sent, not the file id.
	if attrs["media_id"] != "media_pk_1" {
		t.Errorf("media_id=%v want media_pk_1 (resolved from the file, not the file id)", attrs["media_id"])
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

func TestPlaylistSetCover_RequiresFileID(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "set-cover", "pl_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --file-id must exit before any HTTP request")
	}
}

// TestPlaylistSetCover_FileWithoutMedia asserts that a file that has no media_id
// yet (e.g. still processing) fails with a self-naming usage error AFTER the
// file GET but BEFORE any cover POST.
func TestPlaylistSetCover_FileWithoutMedia(t *testing.T) {
	var postHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/teams/t_team1/files/file_x":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"id":"file_x","type":"files","attributes":{"title":"Clip"}}}`))
		case r.Method == http.MethodPost:
			postHit = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "playlists", "set-cover", "pl_1", "--file-id", "file_x")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if postHit {
		t.Error("a file with no media_id must not fire the cover POST")
	}
}
