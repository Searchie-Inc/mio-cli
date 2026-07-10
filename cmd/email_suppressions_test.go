package cmd

// email_suppressions_test.go — contract tests for
// `mio email suppressions list|create|lift` (MIO-2269).
//
// create: POST /v1/hubs/{hub}/email-suppressions — enveloped, JSON:API type
//         "email_suppressions" (derived from the email-suppressions tail),
//         attributes.email_address mapped from --email.
// lift:   DELETE /v1/hubs/{hub}/email-suppressions/{id} — destructive (--yes).

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const suppressionBody = `{"data":{"id":"esp_1","type":"email_suppressions","attributes":{"email_address":"blocked@example.com","scope":"hub","reason":"admin_block","is_active":true,"suppressed_at":"2026-07-10T00:00:00Z"}}}`
const suppressionListBody = `{"data":[{"id":"esp_1","type":"email_suppressions","attributes":{"email_address":"blocked@example.com","scope":"hub","reason":"admin_block","is_active":true,"suppressed_at":"2026-07-10T00:00:00Z"}}],"meta":{"has_more":false}}`

// TestEmailSuppressionsList pins the list GET: method and hub-scoped path.
func TestEmailSuppressionsList(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureAdminReq(t, http.StatusOK, suppressionListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "email", "suppressions", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/email-suppressions") {
		t.Errorf("path %q does not end with /hubs/hub_123/email-suppressions", *gotPath)
	}
}

// TestEmailSuppressionsCreate_Body pins the create POST: method, path, JSON:API
// type "email_suppressions", and attributes.email_address from --email.
func TestEmailSuppressionsCreate_Body(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureAdminReq(t, http.StatusCreated, suppressionBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"email", "suppressions", "create", "--email", "blocked@example.com",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/email-suppressions") {
		t.Errorf("path %q does not end with /hubs/hub_123/email-suppressions", *gotPath)
	}

	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "email_suppressions" {
		t.Errorf("data.type = %q, want email_suppressions", typ)
	}
	if attrs["email_address"] != "blocked@example.com" {
		t.Errorf("data.attributes.email_address = %v, want blocked@example.com", attrs["email_address"])
	}
}

// TestEmailSuppressionsCreate_MissingEmail pins that a missing --email exits
// ExitUsage without firing any request.
func TestEmailSuppressionsCreate_MissingEmail(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "email", "suppressions", "create")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired when --email is missing")
	}
}

// TestEmailSuppressionsLift_WithYes pins the lift DELETE: method and path
// (including the suppression id).
func TestEmailSuppressionsLift_WithYes(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureAdminReq(t, http.StatusNoContent, "")

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"email", "suppressions", "lift", "esp_1", "--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/hubs/hub_123/email-suppressions/esp_1") {
		t.Errorf("path %q does not end with /hubs/hub_123/email-suppressions/esp_1", *gotPath)
	}
}

// TestEmailSuppressionsLift_NoYes pins that lift without --yes exits
// ExitNeedsConfir (5) without firing any request.
func TestEmailSuppressionsLift_NoYes(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "email", "suppressions", "lift", "esp_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired when --yes is missing")
	}
}
