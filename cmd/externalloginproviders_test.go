package cmd

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const minimalExternalLoginProviderBody = `{
	"data":{
		"id":"elp_abc123",
		"type":"external_login_providers",
		"attributes":{
			"kind":"google",
			"slug":"google",
			"display_name":"Sign in with Google",
			"enabled":true,
			"client_secret_set":true,
			"callback_url":"https://api.member.dev/api/auth/external/google/callback"
		}
	}
}`

const genericOIDCProviderBody = `{
	"data":{
		"id":"elp_def456",
		"type":"external_login_providers",
		"attributes":{
			"kind":"generic_oidc",
			"slug":"company-sso",
			"display_name":"Company SSO",
			"enabled":true,
			"client_secret_set":true,
			"callback_url":"https://api.member.dev/api/auth/external/company-sso/callback"
		}
	}
}`

func TestExternalLoginProviders_Create_GoogleExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "google",
			"--display-name", "Sign in with Google",
			"--client-id", "google-client-id",
			"--client-secret", "google-client-secret",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "external_login_providers",
			"attributes": {
				"kind": "google",
				"display_name": "Sign in with Google",
				"client_id": "google-client-id",
				"client_secret": "google-client-secret"
			}
		}
	}`)
}

func TestExternalLoginProviders_Create_GenericOIDCWithDiscoveryURL(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, genericOIDCProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "generic_oidc",
			"--display-name", "Company SSO",
			"--client-id", "oidc-client-id",
			"--client-secret", "oidc-secret",
			"--discovery-url", "https://sso.company.com/.well-known/openid-configuration",
			"--scopes", "openid email profile",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "external_login_providers",
			"attributes": {
				"kind": "generic_oidc",
				"display_name": "Company SSO",
				"client_id": "oidc-client-id",
				"client_secret": "oidc-secret",
				"discovery_url": "https://sso.company.com/.well-known/openid-configuration",
				"scopes": "openid email profile"
			}
		}
	}`)
}

func TestExternalLoginProviders_Create_WithClaimMap(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, genericOIDCProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "generic_oidc",
			"--display-name", "Company SSO",
			"--client-id", "oidc-client-id",
			"--client-secret", "oidc-secret",
			"--claim-map", `{"given_name":"first_name","family_name":"last_name","email":"email"}`,
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "external_login_providers",
			"attributes": {
				"kind": "generic_oidc",
				"display_name": "Company SSO",
				"client_id": "oidc-client-id",
				"client_secret": "oidc-secret",
				"claim_map": {
					"given_name": "first_name",
					"family_name": "last_name",
					"email": "email"
				}
			}
		}
	}`)
}

func TestExternalLoginProviders_Create_WithSlug(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "google",
			"--slug", "my-google",
			"--display-name", "My Google Login",
			"--client-id", "google-client-id",
			"--client-secret", "google-client-secret",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "external_login_providers",
			"attributes": {
				"kind": "google",
				"slug": "my-google",
				"display_name": "My Google Login",
				"client_id": "google-client-id",
				"client_secret": "google-client-secret"
			}
		}
	}`)
}

func TestExternalLoginProviders_Create_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing kind", []string{"--display-name", "Google"}},
		{"missing display-name", []string{"--kind", "google"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalExternalLoginProviderBody)
			args := append([]string{"external-login-providers", "create"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("POST must NOT be fired when required flags are missing")
			}
		})
	}
}

func TestExternalLoginProviders_Create_RequiresClientCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing client-id", []string{"--kind", "google", "--display-name", "Google", "--client-secret", "s"}},
		{"missing client-secret", []string{"--kind", "google", "--display-name", "Google", "--client-id", "c"}},
		{"missing both credentials", []string{"--kind", "google", "--display-name", "Google"}},
		{"blank client-id", []string{"--kind", "google", "--display-name", "Google", "--client-id", "  ", "--client-secret", "s"}},
		{"blank client-secret", []string{"--kind", "google", "--display-name", "Google", "--client-id", "c", "--client-secret", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, minimalExternalLoginProviderBody)
			args := append([]string{"external-login-providers", "create"}, tc.args...)
			res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", args...)...)
			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if *fired {
				t.Error("POST must NOT be fired when client credentials are missing")
			}
		})
	}
}

func TestExternalLoginProviders_Create_InvalidClaimMapJSON(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "generic_oidc",
			"--display-name", "Bad Provider",
			"--client-id", "c",
			"--client-secret", "s",
			"--claim-map", "not-json",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when --claim-map is invalid JSON")
	}
}

func TestExternalLoginProviders_Create_NullClaimMapRejected(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "generic_oidc",
			"--display-name", "Null Claim Map",
			"--client-id", "c",
			"--client-secret", "s",
			"--claim-map", "null",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when --claim-map is null")
	}
}

func TestExternalLoginProviders_Update_NullClaimMapRejected(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "update", "elp_abc123",
			"--claim-map", "null",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("PATCH must NOT be fired when --claim-map is null")
	}
}

func TestExternalLoginProviders_List_CallsCorrectPath(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "GET",
		PathPfx: "/api/v1/teams/t_team1/external-login-providers",
		Status:  200,
		Body:    `{"data":[],"meta":{}}`,
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

func TestExternalLoginProviders_Retrieve_CallsCorrectPath(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "GET",
		PathPfx: "/api/v1/teams/t_team1/external-login-providers/elp_abc123",
		Status:  200,
		Body:    minimalExternalLoginProviderBody,
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "retrieve", "elp_abc123")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

func TestExternalLoginProviders_Update_PartialBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "update", "elp_abc123",
			"--display-name", "Updated Google Login",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "external_login_providers",
			"attributes": {
				"display_name": "Updated Google Login"
			}
		}
	}`)
}

func TestExternalLoginProviders_Update_EnabledFalse(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "update", "elp_abc123",
			"--enabled=false",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "external_login_providers",
			"attributes": {
				"enabled": false
			}
		}
	}`)
}

func TestExternalLoginProviders_Update_NothingToUpdate(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "update", "elp_abc123")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("PATCH must NOT be fired when no flags are provided")
	}
}

func TestExternalLoginProviders_Update_RotateSecret(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "update", "elp_abc123",
			"--client-secret", "new-rotated-secret",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "external_login_providers",
			"attributes": {
				"client_secret": "new-rotated-secret"
			}
		}
	}`)
}

func TestExternalLoginProviders_Delete_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "delete", "elp_abc123")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

func TestExternalLoginProviders_Delete_WithYes(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Method: "DELETE", PathPfx: "/api/v1/teams/t_team1/external-login-providers/elp_abc123", Status: 204, Body: ""},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "delete", "elp_abc123", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}
