package cmd

// media_attachments_test.go — contract tests for the `mio media attachments`
// admin command group (MIO-2289). Team-scoped attachment admin surface:
// list (with media_id/target filters), show, update (position/role), delete.

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const attachmentResourceBody = `{"data":{"type":"attachments","id":"att_x","attributes":{"role":"thumbnail","position":0}}}`

func TestAttachmentsList_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK, `{"data":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/attachments"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestAttachmentsList_Filters(t *testing.T) {
	srv, _, _, rawQuery, _ := captureAdminReq(t, http.StatusOK, `{"data":[]}`)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "list",
			"--media-id", "media_x", "--target-type", "playlist", "--target-id", "pl_1",
			"--limit", "10")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	q, err := url.ParseQuery(*rawQuery)
	if err != nil {
		t.Fatalf("bad query %q: %v", *rawQuery, err)
	}
	if q.Get("media_id") != "media_x" {
		t.Errorf("media_id=%q want media_x", q.Get("media_id"))
	}
	if q.Get("target_type") != "playlist" {
		t.Errorf("target_type=%q want playlist", q.Get("target_type"))
	}
	if q.Get("target_id") != "pl_1" {
		t.Errorf("target_id=%q want pl_1", q.Get("target_id"))
	}
	if q.Get("page[size]") != "10" {
		t.Errorf("page[size]=%q want 10", q.Get("page[size]"))
	}
}

func TestAttachmentsShow_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusOK, attachmentResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "show", "att_x")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodGet {
		t.Errorf("method=%q want GET", *method)
	}
	if want := "/api/v1/teams/t_team1/attachments/att_x"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}

func TestAttachmentsUpdate_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusOK, attachmentResourceBody)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "update", "att_x",
			"--position", "2", "--role", "thumbnail")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPatch {
		t.Errorf("method=%q want PATCH", *method)
	}
	if want := "/api/v1/teams/t_team1/attachments/att_x"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "attachments" {
		t.Errorf("type=%q want attachments", typ)
	}
	if attrs["position"] != float64(2) {
		t.Errorf("position=%v want 2", attrs["position"])
	}
	if attrs["role"] != "thumbnail" {
		t.Errorf("role=%v want thumbnail", attrs["role"])
	}
}

func TestAttachmentsUpdate_RequiresField(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "update", "att_x")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("a no-op update must exit before any HTTP request")
	}
}

func TestAttachmentsUpdate_RejectsBadRole(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "update", "att_x", "--role", "bogus")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("an invalid --role must exit before any HTTP request")
	}
}

func TestAttachmentsDelete_Path(t *testing.T) {
	srv, method, path, _, _ := captureAdminReq(t, http.StatusNoContent, "")
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "media", "attachments", "delete", "att_x", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodDelete {
		t.Errorf("method=%q want DELETE", *method)
	}
	if want := "/api/v1/teams/t_team1/attachments/att_x"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}
}
