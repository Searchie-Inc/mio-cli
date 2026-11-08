package cmd

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

func TestOAuthClients_List_CallsCorrectPath(t *testing.T) {
	const listBody = `{"data":[],"meta":{}}`
	hitPath := ""
	srv := newMockServer(t, []mockHandler{{
		Method:  "GET",
		PathPfx: "/api/v1/teams/t_team1/oauth-clients",
		Status:  200,
		Body:    listBody,
	}})
	_ = hitPath

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

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

func TestOAuthClients_Delete_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "delete", "oc_abc123")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

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

func TestOAuthClients_RedirectURIs_Remove_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "oauth-clients", "redirect-uris", "remove", "oc_abc123", "ru_xyz789")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

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
