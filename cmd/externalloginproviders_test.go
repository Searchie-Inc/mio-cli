package cmd

// externalloginproviders_test.go — wire-body and contract tests for
// `mio external-login-providers`.
//
// Tests pin:
//   - Correct JSON:API data.type value ("external_login_providers")
//   - Exact wire bodies for create (first-party + generic provider flavours)
//   - Required-flag validation (--kind and --display-name on create)
//   - Partial-update (PATCH) semantics: only changed flags are sent
//   - Destructive-guard behaviour on delete
//   - --claim-map JSON-object parsing

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── fixtures ─────────────────────────────────────────────────────────────────

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

// ─── create: Google (first-party) ─────────────────────────────────────────────

// TestExternalLoginProviders_Create_GoogleExactBody pins the EXACT wire body
// for `mio external-login-providers create --kind google`.
//
// CONTRACT: data.type = "external_login_providers"; only changed attrs sent.
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

// TestExternalLoginProviders_Create_GenericOIDCWithDiscoveryURL pins the wire
// body when creating a generic_oidc provider using --discovery-url.
//
// CONTRACT: discovery_url and scopes must be present in attributes.
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

// TestExternalLoginProviders_Create_WithClaimMap pins that --claim-map is
// decoded as a JSON object and serialized into attributes.claim_map.
func TestExternalLoginProviders_Create_WithClaimMap(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, genericOIDCProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "generic_oidc",
			"--display-name", "Company SSO",
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
				"claim_map": {
					"given_name": "first_name",
					"family_name": "last_name",
					"email": "email"
				}
			}
		}
	}`)
}

// TestExternalLoginProviders_Create_WithSlug pins that --slug is forwarded
// when provided.
func TestExternalLoginProviders_Create_WithSlug(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "google",
			"--slug", "my-google",
			"--display-name", "My Google Login",
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
				"display_name": "My Google Login"
			}
		}
	}`)
}

// TestExternalLoginProviders_Create_RequiredFlags pins that both --kind and
// --display-name are required client-side.
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

// TestExternalLoginProviders_Create_InvalidClaimMapJSON pins that a non-JSON
// --claim-map value exits with ExitUsage and fires no request.
func TestExternalLoginProviders_Create_InvalidClaimMapJSON(t *testing.T) {
	srv, fired := newNoRequestServer(t, minimalExternalLoginProviderBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "create",
			"--kind", "generic_oidc",
			"--display-name", "Bad Provider",
			"--claim-map", "not-json",
		)...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("POST must NOT be fired when --claim-map is invalid JSON")
	}
}

// ─── list / retrieve ──────────────────────────────────────────────────────────

// TestExternalLoginProviders_List_CallsCorrectPath verifies the list command
// hits the right team-scoped collection URL.
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

// TestExternalLoginProviders_Retrieve_CallsCorrectPath verifies the retrieve
// command appends the id to the collection path.
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

// ─── update ───────────────────────────────────────────────────────────────────

// TestExternalLoginProviders_Update_PartialBody pins PATCH semantics: only
// the flags that were set are serialized (display-name only in this case).
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

// TestExternalLoginProviders_Update_EnabledFalse pins that --enabled=false
// sends "enabled": false in the attributes map.
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

// TestExternalLoginProviders_Update_NothingToUpdate pins that calling update
// with no flags set exits with ExitUsage and fires no request.
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

// TestExternalLoginProviders_Update_RotateSecret pins that --client-secret
// on update serializes client_secret into the PATCH body.
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

// ─── delete ───────────────────────────────────────────────────────────────────

// TestExternalLoginProviders_Delete_NonTTYWithoutYes pins the non-TTY guard.
func TestExternalLoginProviders_Delete_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must NOT be called
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "external-login-providers", "delete", "elp_abc123")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

// TestExternalLoginProviders_Delete_WithYes pins that --yes bypasses the guard
// and the DELETE is sent to the correct path.
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
