package cmd

// mio3074_content_file_id_reconcile_test.go — contract tests for MIO-3074.
//
// Two gaps, one ticket:
//
//   1. `content create/update` accepted only --media-id (the Media PK). The id a
//      creator actually holds after an upload is the FILE id, and the backend
//      stores media_id WITHOUT validating it (MIO-3432) — so passing a file id
//      produced a lesson silently pointing at nothing rather than an error.
//      --file-id now resolves to the Media PK first, reusing the resolver
//      MIO-2519 added for `media playlists set-cover`.
//
//   2. A file living only in a playlist has no content node, so progress, My
//      List, comments and the page builder's single-file binding are all
//      missing for it. The backend heal endpoint shipped in MIO-3258; nothing
//      exposed it. `content reconcile` does.
//
// Harness follows media_playlist_cover_test.go (two-step resolve-then-write)
// and write_path_drift_test.go (exact wire bodies).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// fileResolveServer serves the admin file resource used for id resolution and
// captures the single write that follows it.
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
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &wrote
}

// CONTRACT (MIO-3074): content create --file-id X → the RESOLVED media PK lands
// on the wire as attributes.media_id. The file id itself must never be sent.
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

// CONTRACT (MIO-3074): the same resolution applies to update's PATCH.
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

// CONTRACT (MIO-3074): --media-id and --file-id are mutually exclusive, and the
// guard fires BEFORE any HTTP request — otherwise the resolution GET would run
// only to have its result discarded.
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

// CONTRACT (MIO-3074): a file whose media has not finished processing carries no
// media_id. Fail with a self-naming usage error rather than sending an empty
// media_id the backend would store verbatim (MIO-3432).
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

// The guards above are exercised with id-shaped --team/--hub values, which
// `isIDShaped` short-circuits with no API call (internal/client/resolve.go).
// That is the ONE path where an after-the-fact guard would still look correct.
// These use a hub NAME — a supported form that makes requireHub LIST over HTTP —
// so they fail if flag validation ever slips back behind contentContext.
//
// Found by Codex review; the original tests passed while the ordering was wrong.

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

// Findings from a blind review lane on PR #112 (Jarius, 2026-08-26). Each of
// these described a real defect that the first round of tests did not cover.

// CONTRACT: --media-id "" (or whitespace) must NOT reach the wire. setStringFlag
// neither trims nor rejects empty, and the backend stores media_id WITHOUT
// validating it (MIO-3432) — so an empty value created a lesson pointing at
// nothing, with a 201 and no error. That is the exact failure --file-id exists
// to prevent, through the flag the original guard forgot.
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

// CONTRACT: an empty --media-id is rejected on UPDATE too, and that reversal is
// deliberate. Treating empty as "unlink" reads well until you write the shell
// most people write:
//
//	mio content update $ID --media-id "$MEDIA"   # $MEDIA unset upstream
//
// cobra reports the flag as Changed with an empty value, so a Changed-based
// guard does NOT catch it — a silent clear would destroy a working link and
// exit 0. Verified: before this guard that call sent {"media_id":null} and
// returned success. An empty value is far more often a broken variable than an
// intent to unlink.
//
// Raised by a blind review lane on PR #112 (Jarius's successor, 2026-08-26).
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

// CONTRACT: unlinking is available, but only through a boolean no shell
// variable can expand into. It sends an explicit JSON null.
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
		withTeam("t_team1", "--hub", "hub_abc", "content", "update", "cnt_1", "--unset-media")...)
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

// CONTRACT: --unset-media and a relink flag together are contradictory.
func TestContentUpdate_UnsetMediaConflictsWithRelink(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_abc", "content", "update", "cnt_1",
			"--unset-media", "--media-id", "media_1")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("contradictory unlink/relink flags must fire before any request")
	}
}

// CONTRACT: a blank --playlist-id among good ones is an ERROR, not a silent
// drop. Dropping it would reconcile a shorter set than the caller named and
// still report success, with no way for them to notice.
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

// CONTRACT: the validate-before-resolve rule covers ALL of create's guards, not
// only the media pair. --title/--node-type are required client-side; they must
// fire before the hub-name resolution round trip too.
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

const reconcileResponse = `{"data":{"id":"hub_abc","type":"content_node_reconciliations","attributes":{"hub_id":"hub_abc","reconciliations":[]}}}`

// CONTRACT (MIO-3074): with no --playlist-id the command sends a BODYLESS POST.
// The backend derives the playlist set from scaffold provenance, and rejects an
// explicitly empty list (min_length=1) — so sending `{"playlist_ids":[]}` would
// turn the common case into a 422.
func TestContentReconcile_NoPlaylistIDsSendsNoBody(t *testing.T) {
	var body []byte
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/teams/t_team1/hubs/hub_abc/content/reconcile" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
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

// CONTRACT (MIO-3074): explicit ids are sent as attributes.playlist_ids under
// the type `content_node_reconciliations`. Deriving the type from the path would
// yield "content" and the backend (extra="forbid") would reject it.
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

// CONTRACT (MIO-3074): --playlist-id set to only blanks is a usage error, not a
// silently-empty list that the backend would 422.
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

// CONTRACT (MIO-3074): --playlist-id is a cobra StringSlice, so ONE flag
// carrying a comma-separated list expands to several ids, and surrounding
// whitespace is trimmed. llms.txt documents the flag as repeatable; this pins
// the comma form too, since an agent may reach for either.
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

// CONTRACT (MIO-3074): an empty-string --playlist-id is the same usage error as
// an all-blank one — never a silently-empty list the backend would 422.
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

// Guard against the wire body silently drifting to a bare array/string shape.
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
