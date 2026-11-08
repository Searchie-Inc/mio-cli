package cmd

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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
