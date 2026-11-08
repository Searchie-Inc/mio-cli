package cmd

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

func TestVerifiedDomains_Delete_NonTTYWithoutYes(t *testing.T) {
	srv := newMockServer(t, nil)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "verified-domains", "delete", "vd_abc123")...)
	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

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
