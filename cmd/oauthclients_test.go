package cmd

// oauthclients_test.go — wire-body and contract tests for `mio oauth-clients`.
//
// Tests pin:
//   - Correct JSON:API data.type values ("oauth_clients", "oauth_client_redirect_uris")
//   - Exact wire bodies for create and redirect-uris add
//   - Required-flag validation (--name on create, --uri on redirect-uris add)
//   - Destructive-guard behaviour on delete and redirect-uris remove
//   - That platform-admin-only fields (first_party, allowed_scopes) are NOT
//     exposed as flags on the create command (unknown-flag → exit 2)

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── fixtures ─────────────────────────────────────────────────────────────────

const minimalOAuthClientBody = `{
	"data":{
		"id":"oc_abc123",
		"type":"oauth_clients",
		"attributes":{
			"name":"My App",
			"is_public":false,
			"client_id":"cid_abc",
			"client_secret":"s3cr3t_shown_once"
		}
	}
}`

const minimalRedirectURIBody = `{
	"data":{
		"id":"ru_xyz789",
		"type":"oauth_client_redirect_uris",
		"attributes":{
			"uri":"https://myapp.example.com/callback"
		}
	}
}`

// ─── oauth-clients create ─────────────────────────────────────────────────────

// TestOAuthClients_Create_ExactBody pins the EXACT wire body for
// `mio oauth-clients create --name "My App"`.
//
// CONTRACT: data.type = "oauth_clients"; only changed attrs are sent.
func TestOAuthClients_Create_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalOAuthClientBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "create",
			"--name", "My App",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "oauth_clients",
			"attributes": {
				"name": "My App"
			}
		}
	}`)
}

// TestOAuthClients_Create_PublicClientBody pins that --public sends is_public=true.
func TestOAuthClients_Create_PublicClientBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalOAuthClientBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "create",
			"--name", "Mobile App",
			"--public",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "oauth_clients",
			"attributes": {
				"name": "Mobile App",
				"is_public": true
			}
		}
	}`)
}

// TestOAuthClients_Create_WithRedirectURI pins that --redirect-uri is serialized
// as a single-element redirect_uris[] in attributes.
// Additional URIs are added via `mio oauth-clients redirect-uris add`.
func TestOAuthClients_Create_WithRedirectURI(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalOAuthClientBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "create",
			"--name", "My App",
			"--redirect-uri", "https://myapp.example.com/callback",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "oauth_clients",
			"attributes": {
				"name": "My App",
				"redirect_uris": ["https://myapp.example.com/callback"]
			}
		}
	}`)
}

// TestOAuthClients_Create_WithBranding pins that --brand-label and --logo-url
// map to their snake_case attribute keys.
func TestOAuthClients_Create_WithBranding(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalOAuthClientBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "create",
			"--name", "Partner Portal",
			"--brand-label", "Partner Login",
			"--logo-url", "https://cdn.example.com/logo.png",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "oauth_clients",
			"attributes": {
				"name": "Partner Portal",
				"brand_label": "Partner Login",
				"logo_url": "https://cdn.example.com/logo.png"
			}
		}
	}`)
}

// TestOAuthClients_Create_RequiredName pins that --name is required client-side.
func TestOAuthClients_Create_RequiredName(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalOAuthClientBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "create")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when --name is missing")
	}
}

// TestOAuthClients_Create_PlatformAdminFieldsNotExposed pins that first_party
// and allowed_scopes are NOT exposed as flags (unknown flag → exit 2).
func TestOAuthClients_Create_PlatformAdminFieldsNotExposed(t *testing.T) {
	for _, tc := range []struct {
		flag string
		val  string
	}{
		{"--first-party", ""},
		{"--allowed-scopes", "openid profile"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalOAuthClientBody)
			args := []string{"oauth-clients", "create", "--name", "X", tc.flag}
			if tc.val != "" {
				args = append(args, tc.val)
			}
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("%s: exit code = %d, want %d (ExitUsage); stderr=%q",
					tc.flag, res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Errorf("%s: POST must NOT be fired for a platform-admin flag", tc.flag)
			}
		})
	}
}

// ─── oauth-clients list / retrieve ───────────────────────────────────────────

// TestOAuthClients_List_CallsCorrectPath verifies the list command hits the
// right team-scoped collection URL and exits 0 on a 200 response.
func TestOAuthClients_List_CallsCorrectPath(t *testing.T) {
	const listBody = `{"data":[],"meta":{}}`
	hitPath := ""
	srv := newMockServer(t, []mockHandler{{
		Method:  "GET",
		PathPfx: "/api/v1/teams/t_team1/oauth-clients",
		Status:  200,
		Body:    listBody,
	}})
	// Use a path-capturing server alongside the mock.
	_ = hitPath

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

// TestOAuthClients_Retrieve_CallsCorrectPath verifies the retrieve command
// appends the id to the collection path and exits 0 on a 200 response.
func TestOAuthClients_Retrieve_CallsCorrectPath(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "GET",
		PathPfx: "/api/v1/teams/t_team1/oauth-clients/oc_abc123",
		Status:  200,
		Body:    minimalOAuthClientBody,
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "retrieve", "oc_abc123")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

// ─── oauth-clients delete ─────────────────────────────────────────────────────

// TestOAuthClients_Delete_NonTTYWithoutYes pins the non-TTY guard.
func TestOAuthClients_Delete_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must NOT be called
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "delete", "oc_abc123")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

// TestOAuthClients_Delete_WithYes pins that --yes bypasses the guard and the
// DELETE is sent.
func TestOAuthClients_Delete_WithYes(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Method: "DELETE", PathPfx: "/api/v1/teams/t_team1/oauth-clients/oc_abc123", Status: 204, Body: ""},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "delete", "oc_abc123", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

// ─── oauth-clients redirect-uris add ─────────────────────────────────────────

// TestOAuthClients_RedirectURIs_Add_ExactBody pins the wire body for
// `mio oauth-clients redirect-uris add`.
//
// CONTRACT: data.type = "oauth_client_redirect_uris"; uri → attributes.uri
func TestOAuthClients_RedirectURIs_Add_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalRedirectURIBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "redirect-uris", "add", "oc_abc123",
			"--uri", "https://myapp.example.com/callback",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "oauth_client_redirect_uris",
			"attributes": {
				"uri": "https://myapp.example.com/callback"
			}
		}
	}`)
}

// TestOAuthClients_RedirectURIs_Add_RequiredURI pins that --uri is required
// client-side.
func TestOAuthClients_RedirectURIs_Add_RequiredURI(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalRedirectURIBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "redirect-uris", "add", "oc_abc123")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when --uri is missing")
	}
}

// ─── oauth-clients redirect-uris remove ──────────────────────────────────────

// TestOAuthClients_RedirectURIs_Remove_NonTTYWithoutYes pins the non-TTY guard.
func TestOAuthClients_RedirectURIs_Remove_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must NOT be called
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "redirect-uris", "remove", "oc_abc123", "ru_xyz789")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

// TestOAuthClients_RedirectURIs_Remove_WithYes pins that --yes bypasses the
// guard and DELETE is sent to the correct path.
func TestOAuthClients_RedirectURIs_Remove_WithYes(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "DELETE",
		PathPfx: "/api/v1/teams/t_team1/oauth-clients/oc_abc123/redirect-uris/ru_xyz789",
		Status:  204,
		Body:    "",
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "redirect-uris", "remove", "oc_abc123", "ru_xyz789", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}
