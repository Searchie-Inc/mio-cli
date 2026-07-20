package cmd

// automations_test.go — contract tests for `mio automations` and
// `mio webhook-endpoints` command groups.
//
// Covers:
//   - automations create: body shape (JSON:API envelope, snake_case attributes)
//   - automations publish: HTTP method, path suffix, nil-body action
//   - automations enroll: body shape (JSON:API envelope with automation_enrollments type)
//   - automations fire-event: flat body (no envelope), required flags
//   - webhook-endpoints create: body shape, signing_secret included
//   - webhook-endpoints delete: DELETE method called, --yes required
//
// Reuses the in-process harness from contract_test.go (runContract,
// newMockServer, baseEnv, withTeam).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

const automationBody = `{
	"data": {
		"id": "auto_1",
		"type": "automations",
		"attributes": {
			"name": "Welcome Series",
			"status": "draft",
			"re_entry_mode": "once"
		}
	}
}`

const enrollmentBody = `{
	"data": {
		"id": "enr_1",
		"type": "automation_enrollments",
		"attributes": {
			"team_contact_id": "tcid_xyz",
			"status": "active"
		}
	}
}`

const webhookEndpointBody = `{
	"data": {
		"id": "we_1",
		"type": "webhook_endpoints",
		"attributes": {
			"name": "My Hook",
			"target_url": "https://example.com/hook"
		}
	}
}`

// ── automations create ────────────────────────────────────────────────────────

// TestAutomationsCreate_BodyShape verifies that create sends a JSON:API
// envelope with type "automations" and the correct snake_case attribute keys.
// --definition is now required (MIO-968c).
func TestAutomationsCreate_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(automationBody))
	}))
	t.Cleanup(srv.Close)

	const defJSON = `{"nodes":[{"type":"exit","id":"n1","config":{}}],"edges":[],"triggers":[]}`

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "create",
			"--name", "Welcome Series",
			"--re-entry-mode", "once",
			"--definition", defJSON,
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(gotPath, "/automations") {
		t.Errorf("path %q does not contain /automations", gotPath)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}

	if doc.Data.Type != "automations" {
		t.Errorf("envelope type = %q, want \"automations\"", doc.Data.Type)
	}
	attrs := doc.Data.Attributes
	if attrs["name"] != "Welcome Series" {
		t.Errorf("attributes.name = %v, want \"Welcome Series\"", attrs["name"])
	}
	if attrs["re_entry_mode"] != "once" {
		t.Errorf("attributes.re_entry_mode = %v, want \"once\"", attrs["re_entry_mode"])
	}
	if attrs["definition"] == nil {
		t.Errorf("attributes.definition must be present (required field)")
	}
}

// TestAutomationsCreate_MissingName verifies that omitting --name exits 2.
func TestAutomationsCreate_MissingName(t *testing.T) {
	srv := newMockServer(t, nil)
	const defJSON = `{"nodes":[{"type":"exit","id":"n1","config":{}}],"edges":[],"triggers":[]}`

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "create",
			// --name intentionally omitted; --definition provided to isolate the test
			"--definition", defJSON,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── automations publish ───────────────────────────────────────────────────────

// TestAutomationsPublish_PostToPublishPath verifies that publish sends a POST
// to the correct path and handles a 201 response with a resource body.
func TestAutomationsPublish_PostToPublishPath(t *testing.T) {
	var gotMethod string
	var gotPath string

	const publishBody = `{
		"data": {
			"id": "ver_1",
			"type": "automations",
			"attributes": {"version": 1, "status": "published"}
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(publishBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "publish", "auto_1",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/automations/auto_1/publish") {
		t.Errorf("path %q does not end with /automations/auto_1/publish", gotPath)
	}
}

// TestAutomationsPublish_NoBodyPrints verifies that a 204 No Content response
// causes a fallback message to be printed instead of crashing.
func TestAutomationsPublish_NoBodyPrints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "publish", "auto_1",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "auto_1") {
		t.Errorf("stdout %q does not mention auto_1", res.Stdout)
	}
}

// ── automations enroll ────────────────────────────────────────────────────────

// TestAutomationsEnroll_BodyShape verifies that enroll sends a JSON:API
// envelope with type "automation_enrollments" and the correct attribute key.
func TestAutomationsEnroll_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(enrollmentBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "enroll", "auto_1",
			"--team-contact-id", "tcid_xyz",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.HasSuffix(gotPath, "/automations/auto_1/enrollments") {
		t.Errorf("path %q does not end with /automations/auto_1/enrollments", gotPath)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}

	if doc.Data.Type != "automation_enrollments" {
		t.Errorf("envelope type = %q, want \"automation_enrollments\"", doc.Data.Type)
	}
	attrs := doc.Data.Attributes
	if attrs["team_contact_id"] != "tcid_xyz" {
		t.Errorf("attributes.team_contact_id = %v, want \"tcid_xyz\"", attrs["team_contact_id"])
	}
}

// ── automations fire-event ────────────────────────────────────────────────────

// TestAutomationsFireEvent_FlatBody verifies that fire-event sends a flat
// JSON body (NOT a JSON:API envelope) with snake_case keys.
func TestAutomationsFireEvent_FlatBody(t *testing.T) {
	var gotBody []byte
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "fire-event",
			"--event-type", "purchase_completed",
			"--team-contact-id", "tcid_xyz",
			"--idempotency-key", "idem_abc",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.HasSuffix(gotPath, "/automations/events") {
		t.Errorf("path %q does not end with /automations/events", gotPath)
	}

	var flat map[string]any
	if err := json.Unmarshal(gotBody, &flat); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}

	// Must be flat — no "data" wrapper.
	if _, hasData := flat["data"]; hasData {
		t.Error("fire-event body must NOT have a top-level \"data\" key (must be flat)")
	}
	if flat["event_type"] != "purchase_completed" {
		t.Errorf("event_type = %v, want \"purchase_completed\"", flat["event_type"])
	}
	if flat["team_contact_id"] != "tcid_xyz" {
		t.Errorf("team_contact_id = %v, want \"tcid_xyz\"", flat["team_contact_id"])
	}
	if flat["idempotency_key"] != "idem_abc" {
		t.Errorf("idempotency_key = %v, want \"idem_abc\"", flat["idempotency_key"])
	}
}

// TestAutomationsFireEvent_MissingEventType verifies that omitting --event-type exits 2.
func TestAutomationsFireEvent_MissingEventType(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "fire-event",
			"--team-contact-id", "tcid_xyz",
			// --event-type intentionally omitted
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── webhook-endpoints create ──────────────────────────────────────────────────

// TestWebhookEndpointsCreate_BodyShape verifies that create sends a JSON:API
// envelope with type "webhook_endpoints" and the correct attribute keys,
// including signing_secret.
func TestWebhookEndpointsCreate_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(webhookEndpointBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
			"--name", "My Hook",
			"--target-url", "https://example.com/hook",
			"--signing-secret", "s3cr3t",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.HasSuffix(gotPath, "/webhook-endpoints") {
		t.Errorf("path %q does not end with /webhook-endpoints", gotPath)
	}

	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}

	if doc.Data.Type != "webhook_endpoints" {
		t.Errorf("envelope type = %q, want \"webhook_endpoints\"", doc.Data.Type)
	}
	attrs := doc.Data.Attributes
	if attrs["name"] != "My Hook" {
		t.Errorf("attributes.name = %v, want \"My Hook\"", attrs["name"])
	}
	if attrs["target_url"] != "https://example.com/hook" {
		t.Errorf("attributes.target_url = %v, want URL", attrs["target_url"])
	}
	if attrs["signing_secret"] != "s3cr3t" {
		t.Errorf("attributes.signing_secret = %v, want \"s3cr3t\"", attrs["signing_secret"])
	}
}

// TestWebhookEndpointsCreate_MissingFlags verifies that omitting all flags exits 2.
func TestWebhookEndpointsCreate_MissingFlags(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── webhook-endpoints delete ──────────────────────────────────────────────────

// TestWebhookEndpointsDelete_RequiresYes verifies that delete without --yes
// exits 5 (ExitNeedsConfir) in non-TTY mode.
func TestWebhookEndpointsDelete_RequiresYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must not be called

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "delete", "we_1",
			// --yes intentionally omitted
		)...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

// TestWebhookEndpointsDelete_WithYes verifies that delete with --yes sends
// DELETE to the correct path and exits 0.
func TestWebhookEndpointsDelete_WithYes(t *testing.T) {
	var gotMethod string
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "delete", "we_1",
			"--yes",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/webhook-endpoints/we_1") {
		t.Errorf("path %q does not end with /webhook-endpoints/we_1", gotPath)
	}
}

// ── automations test (dry-run) ────────────────────────────────────────────────

// TestAutomationsTest_FlatBody is the regression guard for MIO-2503. The
// POST .../automations/{id}/test endpoint intentionally returns a FLAT report
// {"meta":{…},"trace":[…]} with NO JSON:API `data` member. The command must
// decode that flat document and render meta+trace, exiting 0 — NOT run it
// through the resource decoder (which errors "response had no `data` member" on
// every successful dry-run). This test drives the REAL flat response shape and
// asserts both the flat request body and that the rendered report reaches
// stdout.
func TestAutomationsTest_FlatBody(t *testing.T) {
	var gotBody []byte
	var gotPath string

	// Real backend contract: DryRunResponse {meta: dict, trace: list} at 200,
	// Content-Type application/vnd.api+json, no top-level `data`.
	const testResponseBody = `{
		"meta": {"steps_executed": 3, "dry_run": true},
		"trace": [
			{"node_id": "n1", "node_type": "entry", "outcome": "matched"},
			{"node_id": "n2", "node_type": "exit", "outcome": "reached"}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(testResponseBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "test", "auto_1",
			"--team-contact-id", "tcid_xyz",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.HasSuffix(gotPath, "/automations/auto_1/test") {
		t.Errorf("path %q does not end with /automations/auto_1/test", gotPath)
	}

	// The flat meta+trace report must be rendered to stdout (default JSON off a
	// non-TTY), not swallowed by a resource-decode failure.
	if !strings.Contains(res.Stdout, "steps_executed") {
		t.Errorf("stdout does not contain rendered meta (steps_executed); stdout=%q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "n1") || !strings.Contains(res.Stdout, "trace") {
		t.Errorf("stdout does not contain rendered trace; stdout=%q", res.Stdout)
	}

	var flat map[string]any
	if err := json.Unmarshal(gotBody, &flat); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}

	// Regression guard: request must be flat — no "data" wrapper (no envelope).
	if _, hasData := flat["data"]; hasData {
		t.Error("automations test body must NOT have a top-level \"data\" key (must be flat, not a JSON:API envelope)")
	}
	if flat["team_contact_id"] != "tcid_xyz" {
		t.Errorf("team_contact_id = %v, want \"tcid_xyz\"", flat["team_contact_id"])
	}
}

// ── fire-event: missing idempotency-key ───────────────────────────────────────

// TestAutomationsFireEvent_MissingIdempotencyKey verifies that omitting
// --idempotency-key exits 2 (ExitUsage) even when --event-type and
// --team-contact-id are provided.
func TestAutomationsFireEvent_MissingIdempotencyKey(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "fire-event",
			"--event-type", "purchase_completed",
			"--team-contact-id", "tcid_xyz",
			// --idempotency-key intentionally omitted
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── webhook-endpoints create: missing required flags ──────────────────────────

// TestWebhookEndpointsCreate_MissingName verifies that omitting --name (while
// supplying --target-url) exits 2 (ExitUsage).
func TestWebhookEndpointsCreate_MissingName(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
			"--target-url", "https://example.com/hook",
			// --name intentionally omitted
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestWebhookEndpointsCreate_MissingTargetURL verifies that omitting --target-url
// (while supplying --name) exits 2 (ExitUsage).
func TestWebhookEndpointsCreate_MissingTargetURL(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
			"--name", "My Hook",
			// --target-url intentionally omitted
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── empty required-flag rejection (whitespace/empty string) ──────────────────

// TestAutomationsFireEvent_EmptyIdempotencyKey verifies that passing
// --idempotency-key "" (explicitly empty) exits 2 (ExitUsage), not 0.
// This tests the fix for the "changed but empty" blind spot where nil checks
// passed because setStringFlag stored "" instead of nil.
func TestAutomationsFireEvent_EmptyIdempotencyKey(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "fire-event",
			"--event-type", "purchase_completed",
			"--team-contact-id", "tcid_xyz",
			"--idempotency-key", "", // explicitly empty
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestWebhookEndpointsCreate_EmptyName verifies that --name "" (explicitly
// empty) exits 2 (ExitUsage).
func TestWebhookEndpointsCreate_EmptyName(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
			"--name", "",
			"--target-url", "https://example.com/hook",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestWebhookEndpointsCreate_EmptyTargetURL verifies that --target-url ""
// (explicitly empty) exits 2 (ExitUsage).
func TestWebhookEndpointsCreate_EmptyTargetURL(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
			"--name", "My Hook",
			"--target-url", "",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}
