package cmd

// verifieddomains_test.go — wire-body and contract tests for
// `mio verified-domains`.
//
// Tests pin:
//   - Correct JSON:API data.type value ("verified_domains")
//   - Exact wire body for create ({hub_id, domain})
//   - Required-flag validation (--hub-id and --domain on create)
//   - Correct team-scoped paths for list / retrieve
//   - The custom verify action POSTs to .../{id}/verify
//   - Destructive-guard behaviour on delete (non-TTY needs --yes)

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ─── fixtures ─────────────────────────────────────────────────────────────────

const unverifiedDomainBody = `{
	"data":{
		"id":"vd_abc123",
		"type":"verified_domains",
		"attributes":{
			"hub_id":"hub_abc123",
			"domain":"example.com",
			"verified":false,
			"verification_token":"mio-verify=abc123token",
			"txt_record_host":"_mio-verify.example.com",
			"txt_record_value":"mio-verify=abc123token"
		}
	}
}`

const verifiedDomainBody = `{
	"data":{
		"id":"vd_abc123",
		"type":"verified_domains",
		"attributes":{
			"hub_id":"hub_abc123",
			"domain":"example.com",
			"verified":true
		}
	}
}`

// ─── create ───────────────────────────────────────────────────────────────────

// TestVerifiedDomains_Create_ExactBody pins the EXACT wire body for
// `mio verified-domains create`.
//
// CONTRACT: data.type = "verified_domains"; only {hub_id, domain} sent.
func TestVerifiedDomains_Create_ExactBody(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, unverifiedDomainBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "create",
			"--hub-id", "hub_abc123",
			"--domain", "example.com",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	assertExactBody(t, *gotBody, `{
		"data": {
			"type": "verified_domains",
			"attributes": {
				"hub_id": "hub_abc123",
				"domain": "example.com"
			}
		}
	}`)
}

// TestVerifiedDomains_Create_RequiredFlags pins that both --hub-id and --domain
// are required client-side and fire NO request when missing.
func TestVerifiedDomains_Create_RequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing hub-id", []string{"--domain", "example.com"}},
		{"missing domain", []string{"--hub-id", "hub_abc123"}},
		{"missing both", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, fired := newNoRequestServer(t, unverifiedDomainBody)
			args := append([]string{"verified-domains", "create"}, tc.args...)
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

// ─── list / retrieve ──────────────────────────────────────────────────────────

// TestVerifiedDomains_List_CallsCorrectPath verifies the list command hits the
// right team-scoped collection URL.
func TestVerifiedDomains_List_CallsCorrectPath(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "GET",
		PathPfx: "/api/v1/teams/t_team1/verified-domains",
		Status:  200,
		Body:    `{"data":[],"meta":{}}`,
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

// TestVerifiedDomains_Retrieve_CallsCorrectPath verifies retrieve appends the id
// to the collection path.
func TestVerifiedDomains_Retrieve_CallsCorrectPath(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "GET",
		PathPfx: "/api/v1/teams/t_team1/verified-domains/vd_abc123",
		Status:  200,
		Body:    unverifiedDomainBody,
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "retrieve", "vd_abc123")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

// ─── verify ───────────────────────────────────────────────────────────────────

// TestVerifiedDomains_Verify_PostsToVerifyPath pins that `verify <id>` issues a
// POST to .../{id}/verify and renders the returned (now-verified) resource.
func TestVerifiedDomains_Verify_PostsToVerifyPath(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "POST",
		PathPfx: "/api/v1/teams/t_team1/verified-domains/vd_abc123/verify",
		Status:  200,
		Body:    verifiedDomainBody,
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "verify", "vd_abc123")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}

// TestVerifiedDomains_Verify_NotYetVerified pins that a 422 (TXT record not yet
// visible) surfaces as ExitUsage and no panic.
func TestVerifiedDomains_Verify_NotYetVerified(t *testing.T) {
	srv := newMockServer(t, []mockHandler{{
		Method:  "POST",
		PathPfx: "/api/v1/teams/t_team1/verified-domains/vd_abc123/verify",
		Status:  422,
		Body:    `{"errors":[{"status":"422","detail":"TXT record not found yet"}]}`,
	}})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "verify", "vd_abc123")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage) for 422; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ─── delete ───────────────────────────────────────────────────────────────────

// TestVerifiedDomains_Delete_NonTTYWithoutYes pins the non-TTY guard.
func TestVerifiedDomains_Delete_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must NOT be called
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "delete", "vd_abc123")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

// TestVerifiedDomains_Delete_WithYes pins that --yes bypasses the guard and the
// DELETE is sent to the correct path.
func TestVerifiedDomains_Delete_WithYes(t *testing.T) {
	srv := newMockServer(t, []mockHandler{
		{Method: "DELETE", PathPfx: "/api/v1/teams/t_team1/verified-domains/vd_abc123", Status: 204, Body: ""},
	})

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "delete", "vd_abc123", "--yes")...)
	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
}
