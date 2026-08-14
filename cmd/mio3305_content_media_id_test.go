package cmd

// mio3305_content_media_id_test.go — regression tests for MIO-3305.
//
// The m.io API already accepts `media_id` on both
// ContentNodeCreateAttributes and ContentNodeUpdateAttributes (POST/PATCH
// /api/teams/{t}/hubs/{h}/content[/{id}]) but the CLI had no flag for it,
// forcing admins to fall back to raw API calls to link a content item (e.g.
// a workshop replay lesson) to an already-uploaded media asset.
//
// Fix: add --media-id to both `mio content create` and `mio content update`,
// mapped to attributes.media_id, following the same optional
// setStringFlag/PATCH-partial-update pattern as --description and --privacy.
//
// These tests follow the exact harness pattern established in
// write_path_drift_test.go's MIO-942 content section:
//   - captureWriteRequest + assertExactBody for wire-body exactness
//   - runContract / baseEnv / withTeam in-process harness

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestWritePath_ContentCreate_MediaIdExactBody pins the EXACT wire body for
// `mio content create --media-id`: attributes.media_id is sent alongside the
// other required attributes.
//
// CONTRACT (MIO-3305): content create --media-id X → attributes.media_id = X
func TestWritePath_ContentCreate_MediaIdExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Workshop Replay",
			"--node-type", "lesson",
			"--content-type", "video",
			"--media-id", "media_abc123",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "Workshop Replay",
				"node_type": "lesson",
				"content_type": "video",
				"media_id": "media_abc123"
			}
		}
	}`)
}

// TestWritePath_ContentCreate_MediaIdOmittedWhenNotSet pins that attributes.media_id
// is NOT sent when --media-id is not supplied — it is optional, matching every
// other non-required content create flag.
//
// CONTRACT (MIO-3305): content create without --media-id → no media_id key on the wire.
func TestWritePath_ContentCreate_MediaIdOmittedWhenNotSet(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Module 1",
			"--node-type", "container",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "Module 1",
				"node_type": "container"
			}
		}
	}`)
}

// TestWritePath_ContentUpdate_MediaIdExactBody pins the EXACT wire body for
// `mio content update --media-id` (PATCH partial-update path): only media_id
// is sent when it is the only flag supplied.
//
// CONTRACT (MIO-3305): content update --media-id X → {"data":{"type":"content_nodes","attributes":{"media_id":X}}}
func TestWritePath_ContentUpdate_MediaIdExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "update", "cnt_abc123",
			"--media-id", "media_abc123",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"media_id": "media_abc123"
			}
		}
	}`)
}

// TestWritePath_ContentUpdate_MediaIdCombinedWithTitle pins that --media-id
// composes with other partial-update flags: only the flags the user supplied
// are sent, media_id included.
//
// CONTRACT (MIO-3305): content update --title X --media-id Y sends both, nothing else.
func TestWritePath_ContentUpdate_MediaIdCombinedWithTitle(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "update", "cnt_abc123",
			"--title", "Workshop Replay (updated)",
			"--media-id", "media_xyz789",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "content_nodes",
			"attributes": {
				"title": "Workshop Replay (updated)",
				"media_id": "media_xyz789"
			}
		}
	}`)
}
