package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func fileResolveServer(t *testing.T, filePath, writePath, mediaID, writeResponse string, writeStatus int) (*httptest.Server, *[]byte, *bool) {
	t.Helper()
	var body []byte
	var wrote bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == filePath:
			w.WriteHeader(http.StatusOK)
			attrs := `{"title":"Clip"}`
			if mediaID != "" {
				attrs = `{"title":"Clip","media_id":"` + mediaID + `"}`
			}
			_, _ = w.Write([]byte(`{"data":{"id":"file_x","type":"files","attributes":` + attrs + `}}`))
		case r.URL.Path == writePath:
			wrote = true
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(writeStatus)
			_, _ = w.Write([]byte(writeResponse))
		default:
			failUnlessStrayProbe(t, r, "unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &wrote
}

func TestContentCreate_FileIDResolvesToMediaPK(t *testing.T) {
	srv, body, wrote := fileResolveServer(t,
		"/api/v1/teams/t_team1/files/file_x",
		"/api/v1/teams/t_team1/hubs/hub_abc/content",
		"media_pk_1", minimalContentBody, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Workshop Replay",
			"--node-type", "lesson",
			"--file-id", "file_x",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !*wrote {
		t.Fatal("create request never fired")
	}
	assertExactBody(t, *body, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "Workshop Replay",
				"node_type": "lesson",
				"media_id": "media_pk_1"
			}
		}
	}`)
}

func TestContentUpdate_FileIDResolvesToMediaPK(t *testing.T) {
	srv, body, wrote := fileResolveServer(t,
		"/api/v1/teams/t_team1/files/file_x",
		"/api/v1/teams/t_team1/hubs/hub_abc/content/cnt_1",
		"media_pk_1", minimalContentBody, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "update", "cnt_1",
			"--file-id", "file_x",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !*wrote {
		t.Fatal("update request never fired")
	}
	assertExactBody(t, *body, `{
		"data": {
			"type": "content_nodes",
			"attributes": { "media_id": "media_pk_1" }
		}
	}`)
}

func TestContentCreate_MediaIDAndFileIDAreMutuallyExclusive(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "X", "--node-type", "lesson",
			"--media-id", "media_pk_1",
			"--file-id", "file_x",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("mutually-exclusive flags must exit before any HTTP request")
	}
}

func TestContentCreate_FileWithoutMediaIsRejected(t *testing.T) {
	srv, _, wrote := fileResolveServer(t,
		"/api/v1/teams/t_team1/files/file_x",
		"/api/v1/teams/t_team1/hubs/hub_abc/content",
		"", minimalContentBody, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "X", "--node-type", "lesson",
			"--file-id", "file_x",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *wrote {
		t.Error("no content node may be created for a file with no media")
	}
}

func TestContentCreate_MutualExclusionFiresBeforeHubResolution(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "my-hub-by-name",
			"content", "create",
			"--title", "X", "--node-type", "lesson",
			"--media-id", "media_pk_1",
			"--file-id", "file_x",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("contradictory flags must fire before the hub-name resolution request")
	}
}

func TestContentUpdate_MutualExclusionFiresBeforeHubResolution(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "my-hub-by-name",
			"content", "update", "cnt_1",
			"--media-id", "media_pk_1",
			"--file-id", "file_x",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("contradictory flags must fire before the hub-name resolution request")
	}
}

func TestContentReconcile_BlankPlaylistIDFiresBeforeHubResolution(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "my-hub-by-name", "content", "reconcile",
			"--playlist-id", "  ")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("an all-blank --playlist-id must fire before the hub-name resolution request")
	}
}

func TestContentCreate_EmptyMediaIDRejected(t *testing.T) {
	for _, val := range []string{"", "   "} {
		srv, fired := firedGuardServer(t)
		res := runContract(t, baseEnv(srv.URL),
			withTeam("t_team1",
				"--hub", "hub_abc",
				"content", "create",
				"--title", "X", "--node-type", "lesson",
				"--media-id", val,
			)...)
		if res.Code != errs.ExitUsage {
			t.Errorf("--media-id %q: exit=%d want ExitUsage; stderr=%q", val, res.Code, res.Stderr)
		}
		if *fired {
			t.Errorf("--media-id %q: an empty media id must never reach the wire", val)
		}
	}
}

func TestContentUpdate_EmptyMediaIDRejectedNotTreatedAsClear(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "update", "cnt_1",
			"--title", "Keep This", "--media-id", "")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("an unset shell variable must never silently clear a working media link")
	}
}

func TestContentUpdate_UnsetMediaSendsNull(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalContentBody))
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "update", "cnt_1", "--unset-media", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	assertExactBody(t, body, `{
		"data": {
			"type": "content_nodes",
			"attributes": { "media_id": null }
		}
	}`)
}

func TestContentUpdate_UnsetMediaNeedsConfirmation(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "update", "cnt_1", "--unset-media")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit=%d want ExitNeedsConfir; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("an unconfirmed unlink must send nothing")
	}
}

func TestContentUpdate_UnsetMediaConflictsWithRelink(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "update", "cnt_1",
			"--unset-media", "--media-id", "media_1", "--yes")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("contradictory unlink/relink flags must fire before any request")
	}
}

func TestContentReconcile_BlankAmongGoodIDsRejected(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile",
			"--playlist-id", "pl_a", "--playlist-id", "  ", "--playlist-id", "pl_b")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("a blank id among good ones must not silently reconcile a shorter set")
	}
}

func TestContentCreate_MissingRequiredFlagsFireBeforeHubResolution(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "my-hub-by-name", "content", "create", "--node-type", "lesson")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("a missing required flag must fire before the hub-name resolution request")
	}
}

const reconcileResponse = `{"data":{"id":"hub_abc","type":"content_node_reconciliations","attributes":{"hub_id":"hub_abc","results":[]}}}`

func TestContentReconcile_NoPlaylistIDsSendsNoBody(t *testing.T) {
	var body []byte
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/teams/t_team1/hubs/hub_abc/content/reconcile" {
			failUnlessStrayProbe(t, r, "unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		hit = true
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(reconcileResponse))
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if !hit {
		t.Fatal("reconcile request never fired")
	}
	if len(body) != 0 {
		t.Errorf("body=%q want empty — a bodyless POST means 'use scaffold provenance'", body)
	}
}

func TestContentReconcile_ExplicitPlaylistIDsWireShape(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(reconcileResponse))
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile",
			"--playlist-id", "pl_a", "--playlist-id", "pl_b")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	assertExactBody(t, body, `{
		"data": {
			"type": "content_node_reconciliations",
			"attributes": { "playlist_ids": ["pl_a", "pl_b"] }
		}
	}`)
}

func TestContentReconcile_BlankPlaylistIDsRejectedLocally(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile",
			"--playlist-id", "  ")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("an all-blank --playlist-id must exit before any HTTP request")
	}
}

func TestContentReconcile_CommaSeparatedAndTrimmed(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(reconcileResponse))
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile",
			"--playlist-id", " pl_a , pl_b ")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	assertExactBody(t, body, `{
		"data": {
			"type": "content_node_reconciliations",
			"attributes": { "playlist_ids": ["pl_a", "pl_b"] }
		}
	}`)
}

func TestContentReconcile_EmptyStringPlaylistIDRejected(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile",
			"--playlist-id", "")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("an empty --playlist-id must exit before any HTTP request")
	}
}

func TestContentReconcile_PlaylistIDsIsAStringArray(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(reconcileResponse))
	}))
	defer srv.Close()

	if res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile",
			"--playlist-id", "pl_a")...); res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}

	var env struct {
		Data struct {
			Attributes struct {
				PlaylistIDs []string `json:"playlist_ids"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("body is not the expected envelope shape: %v; body=%q", err, body)
	}
	if got := env.Data.Attributes.PlaylistIDs; len(got) != 1 || got[0] != "pl_a" {
		t.Errorf("playlist_ids=%v want [pl_a]", got)
	}
}

func TestContentUpdate_NodeAddressedAsReconcile_SendsNodeType(t *testing.T) {
	var body []byte
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"cnt_1","type":"content_nodes","attributes":{"title":"Renamed"}}}`))
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "update", "reconcile",
			"--title", "Renamed")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method=%q want PATCH", gotMethod)
	}
	if gotPath != "/api/v1/teams/t_team1/hubs/hub_abc/content/reconcile" {
		t.Errorf("path=%q want the node-update path", gotPath)
	}
	assertExactBody(t, body, `{
		"data": {
			"type": "content_nodes",
			"attributes": { "title": "Renamed" }
		}
	}`)
}

func TestContentReconcile_NoProvenance422IsUsageExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"status":"422","code":"no_playlist_provenance","detail":"Hub 'hub_abc' has no HubTemplateApplication provenance for playlists, and no playlist_ids were supplied."}]}`))
	}))
	defer srv.Close()

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "reconcile")...)
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit=%d want %d (ExitUsage) for a 422; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}
