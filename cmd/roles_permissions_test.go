package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const rolePermRoleBody = `{"data":{"id":"role_x","type":"roles","attributes":{"name":"Editor","slug":"editor"}}}`

func TestRolesPermissionsAssign_FlatBody(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureAdminReq(t, http.StatusCreated, rolePermRoleBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"roles", "permissions", "assign", "role_x",
			"--slug", "content.publish",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/roles/role_x/permissions") {
		t.Errorf("path %q does not end with /roles/role_x/permissions", *gotPath)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(*gotBody, &raw); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, *gotBody)
	}
	if _, hasData := raw["data"]; hasData {
		t.Errorf("assign body must be FLAT (no JSON:API envelope); got data key; body=%q", *gotBody)
	}
	slugRaw, ok := raw["slug"]
	if !ok {
		t.Fatalf("assign body missing top-level 'slug'; body=%q", *gotBody)
	}
	if string(slugRaw) != `"content.publish"` {
		t.Errorf("slug = %s, want \"content.publish\"", slugRaw)
	}
}

func TestRolesPermissionsAssign_MissingSlug(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "roles", "permissions", "assign", "role_x")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired when --slug is missing")
	}
}

func TestRolesPermissionsRemove_WithYes(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureAdminReq(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"roles", "permissions", "remove", "role_x", "content.publish", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/roles/role_x/permissions/content.publish") {
		t.Errorf("path %q does not end with /roles/role_x/permissions/content.publish", *gotPath)
	}
}

func TestRolesPermissionsRemove_NoYes(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "roles", "permissions", "remove", "role_x", "content.publish")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired when --yes is missing")
	}
}
