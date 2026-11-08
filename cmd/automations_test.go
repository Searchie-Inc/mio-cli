package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

func TestAutomationsCreate_MissingName(t *testing.T) {
	srv := newMockServer(t, nil)
	const defJSON = `{"nodes":[{"type":"exit","id":"n1","config":{}}],"edges":[],"triggers":[]}`

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "create",
			"--definition", defJSON,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

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

func TestAutomationsFireEvent_MetaOnly202(t *testing.T) {
	var gotBody []byte
	var gotMethod string
	var gotPath string

	const acceptedBody = `{
		"meta": {"status": "accepted", "event_id": "evt_abc123", "enrollments_triggered": 2}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(acceptedBody))
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
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/automations/events") {
		t.Errorf("path %q does not end with /automations/events", gotPath)
	}

	if !strings.Contains(res.Stdout, "event_id") || !strings.Contains(res.Stdout, "evt_abc123") {
		t.Errorf("stdout does not contain rendered meta (event_id); stdout=%q", res.Stdout)
	}

	var flat map[string]any
	if err := json.Unmarshal(gotBody, &flat); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	if _, hasData := flat["data"]; hasData {
		t.Error("fire-event body must NOT have a top-level \"data\" key (must be flat)")
	}
}

func TestAutomationsFireEvent_MissingEventType(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "fire-event",
			"--team-contact-id", "tcid_xyz",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

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

func TestWebhookEndpointsDelete_RequiresYes(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "delete", "we_1",
		)...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

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

func TestAutomationsTest_FlatBody(t *testing.T) {
	var gotBody []byte
	var gotPath string

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

	if _, hasData := flat["data"]; hasData {
		t.Error("automations test body must NOT have a top-level \"data\" key (must be flat, not a JSON:API envelope)")
	}
	if flat["team_contact_id"] != "tcid_xyz" {
		t.Errorf("team_contact_id = %v, want \"tcid_xyz\"", flat["team_contact_id"])
	}
}

func TestAutomationsFireEvent_MissingIdempotencyKey(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "fire-event",
			"--event-type", "purchase_completed",
			"--team-contact-id", "tcid_xyz",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestWebhookEndpointsCreate_MissingName(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
			"--target-url", "https://example.com/hook",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestWebhookEndpointsCreate_MissingTargetURL(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"webhook-endpoints", "create",
			"--name", "My Hook",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestAutomationsFireEvent_EmptyIdempotencyKey(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"automations", "fire-event",
			"--event-type", "purchase_completed",
			"--team-contact-id", "tcid_xyz",
			"--idempotency-key", "",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

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
