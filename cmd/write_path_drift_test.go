package cmd

// write_path_drift_test.go — regression tests for the four write-path
// serializer bugs reported in MIO-937, MIO-938, MIO-941, MIO-942.
//
// All four bugs were the same class: wrong JSON:API data.type values and/or
// missing/incorrect required attribute flags. These tests pin the CORRECT
// wire behaviour so the drift class is caught immediately if re-introduced.
//
// MIO-937 (contacts): data.type must be "team_contacts" (not "contacts")
// MIO-938 (segments): segment_type must NOT be sent; --conditions required on create
// MIO-941 (products): attributes.type (product kind) must be sent; no stale status/published
// MIO-942 (content):  data.type must be "content_nodes"; --node-type maps to node_type

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ── helpers shared by all drift tests ─────────────────────────────────────────

// captureWriteRequest starts a test server that captures the first request's
// body and returns a pointer to it. The server replies with a canned resource
// document at the given HTTP status code; the resource type in the response is
// whatever the caller needs to return so the CLI's decoder is happy.
func captureWriteRequest(t *testing.T, status int, responseBody string) (*httptest.Server, *[]byte) {
	t.Helper()
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotBody
}

// decodeEnvelope parses a captured request body and returns data.type and
// data.attributes. It fails the test if the body is not a valid JSON:API
// resource document.
func decodeEnvelope(t *testing.T, body []byte) (typ string, attrs map[string]any) {
	t.Helper()
	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, body)
	}
	if doc.Data.Type == "" {
		t.Errorf("data.type is empty; body=%q", body)
	}
	return doc.Data.Type, doc.Data.Attributes
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-937 — contacts create / update type value
// ═══════════════════════════════════════════════════════════════════════════════

const minimalContactBody = `{"data":{"id":"ctt_1","type":"team_contacts","attributes":{"email":"x@test.member.dev"}}}`

// TestWritePath_ContactsCreate_TypeIsTeamContacts pins that `mio contacts create`
// sends data.type = "team_contacts" (not the bare URL segment "contacts").
//
// CONTRACT (MIO-937): contacts create → data.type = "team_contacts"
func TestWritePath_ContactsCreate_TypeIsTeamContacts(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContactBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "create", "--email", "x@test.member.dev")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	typ, attrs := decodeEnvelope(t, *gotBody)

	// CONTRACT: data.type must be "team_contacts".
	if typ != "team_contacts" {
		t.Errorf("MIO-937: data.type = %q, want \"team_contacts\"", typ)
	}
	// attributes.email must arrive correctly.
	if attrs["email"] != "x@test.member.dev" {
		t.Errorf("data.attributes.email = %v, want \"x@test.member.dev\"", attrs["email"])
	}
}

// TestWritePath_ContactsUpdate_TypeIsTeamContacts pins the same invariant for
// `mio contacts update` (PATCH path).
//
// CONTRACT (MIO-937): contacts update → data.type = "team_contacts"
func TestWritePath_ContactsUpdate_TypeIsTeamContacts(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalContactBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "contacts", "update", "ctt_1", "--first-name", "Alice")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	typ, attrs := decodeEnvelope(t, *gotBody)

	if typ != "team_contacts" {
		t.Errorf("MIO-937: data.type = %q, want \"team_contacts\"", typ)
	}
	if attrs["first_name"] != "Alice" {
		t.Errorf("data.attributes.first_name = %v, want Alice", attrs["first_name"])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-938 — segments create / update
// ═══════════════════════════════════════════════════════════════════════════════

const minimalSegmentBody = `{"data":{"id":"seg_1","type":"segment","attributes":{"name":"Test"}}}`

// TestWritePath_SegmentsCreate_NoSegmentType pins that `mio segments create`
// does NOT send a segment_type attribute. The backend SegmentCreateAttributes
// schema uses extra="forbid" and has no segment_type field; sending it caused
// a 422 "extra inputs not permitted" (MIO-938 QA repro).
//
// CONTRACT (MIO-938): segments create → no segment_type in attributes
func TestWritePath_SegmentsCreate_NoSegmentType(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalSegmentBody)
	conds := `{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@test.member.dev"}]}]}`

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "create",
			"--name", "Test Segment",
			"--conditions", conds,
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	typ, attrs := decodeEnvelope(t, *gotBody)

	if typ != "segment" {
		t.Errorf("MIO-938: data.type = %q, want \"segment\"", typ)
	}
	// segment_type must NOT be present.
	if _, has := attrs["segment_type"]; has {
		t.Errorf("MIO-938: attributes.segment_type must NOT be sent (extra inputs not permitted); attrs=%v", attrs)
	}
	// name must arrive correctly.
	if attrs["name"] != "Test Segment" {
		t.Errorf("data.attributes.name = %v, want \"Test Segment\"", attrs["name"])
	}
}

// TestWritePath_SegmentsCreate_ConditionsRequired pins that `mio segments create`
// without --conditions exits with ExitUsage (exit 2) and fires NO HTTP request.
//
// CONTRACT (MIO-938): segments create without --conditions → exit 2 (no API call)
func TestWritePath_SegmentsCreate_ConditionsRequired(t *testing.T) {
	requestFired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestFired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalSegmentBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "create", "--name", "Test Segment")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("MIO-938: exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if requestFired {
		t.Error("MIO-938: POST must NOT be fired when --conditions is absent")
	}
}

// TestWritePath_SegmentsCreate_ConditionsSentInAttributes pins that --conditions
// JSON is delivered inside data.attributes.conditions (not at the top level).
//
// CONTRACT (MIO-938): segments create --conditions '<tree>' → attributes.conditions is set
func TestWritePath_SegmentsCreate_ConditionsSentInAttributes(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalSegmentBody)
	conds := `{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@test.member.dev"}]}]}`

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "create",
			"--name", "Email Segment",
			"--conditions", conds,
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, *gotBody)

	if _, has := attrs["conditions"]; !has {
		t.Errorf("MIO-938: data.attributes.conditions is absent; attrs=%v", attrs)
	}
	// conditions must be a parsed object, not the raw string.
	if _, ok := attrs["conditions"].(map[string]any); !ok {
		t.Errorf("MIO-938: data.attributes.conditions is not a JSON object; got %T, attrs=%v",
			attrs["conditions"], attrs)
	}
}

// TestWritePath_SegmentsUpdate_NoSegmentType pins the same no-segment_type
// invariant on `mio segments update`.
//
// CONTRACT (MIO-938): segments update → no segment_type in attributes
func TestWritePath_SegmentsUpdate_NoSegmentType(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalSegmentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "segments", "update", "seg_abc123",
			"--name", "Renamed Segment",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, *gotBody)

	if _, has := attrs["segment_type"]; has {
		t.Errorf("MIO-938: attributes.segment_type must NOT be sent on update; attrs=%v", attrs)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-941 — products create / update
// ═══════════════════════════════════════════════════════════════════════════════

const minimalProductBody = `{"data":{"id":"prod_1","type":"products","attributes":{"name":"Course"}}}`

// TestWritePath_ProductsCreate_TypeSentAsAttribute pins that `mio products create`
// sends attributes.type (the product kind: course, membership, …). Before MIO-941
// there was no --type flag so the required field was never included, causing a 422.
//
// CONTRACT (MIO-941): products create --type course → data.attributes.type = "course"
func TestWritePath_ProductsCreate_TypeSentAsAttribute(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalProductBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "create",
			"--name", "Intro to Go",
			"--type", "course",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, *gotBody)

	if attrs["type"] != "course" {
		t.Errorf("MIO-941: data.attributes.type = %v, want \"course\"", attrs["type"])
	}
	if attrs["name"] != "Intro to Go" {
		t.Errorf("data.attributes.name = %v, want \"Intro to Go\"", attrs["name"])
	}
}

// TestWritePath_ProductsCreate_NoStaleStatus pins that the stale --status and
// --published flags (which no longer exist) do NOT appear as attributes in the
// wire body, and that using them causes a usage error (exit 2).
//
// CONTRACT (MIO-941): products create --status X → exit 2 (unknown flag); no API call
func TestWritePath_ProductsCreate_NoStaleStatusFlag(t *testing.T) {
	requestFired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestFired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalProductBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "create",
			"--name", "X",
			"--type", "course",
			"--status", "active", // stale flag — must not exist
		)...)

	// An unknown flag exits with ExitUsage (2) and must not fire the API.
	if res.Code != errs.ExitUsage {
		t.Errorf("MIO-941: exit code = %d, want %d (ExitUsage for removed --status flag); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if requestFired {
		t.Error("MIO-941: POST must NOT be fired when an unknown flag is passed")
	}
}

// TestWritePath_ProductsUpdate_TypeSentAsAttribute pins the same type mapping
// for `mio products update`.
//
// CONTRACT (MIO-941): products update --type membership → attributes.type = "membership"
func TestWritePath_ProductsUpdate_TypeSentAsAttribute(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusOK, minimalProductBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "products", "update", "prod_abc123",
			"--type", "membership",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, *gotBody)

	if attrs["type"] != "membership" {
		t.Errorf("MIO-941: data.attributes.type = %v, want \"membership\"", attrs["type"])
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// MIO-942 — content create
// ═══════════════════════════════════════════════════════════════════════════════

const minimalContentBody = `{"data":{"id":"cnt_1","type":"content_nodes","attributes":{"title":"Module 1"}}}`

// TestWritePath_ContentCreate_TypeIsContentNodes pins that `mio content create`
// sends data.type = "content_nodes" (not "content").
//
// CONTRACT (MIO-942): content create → data.type = "content_nodes"
func TestWritePath_ContentCreate_TypeIsContentNodes(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Module 1",
			"--node-type", "container",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	typ, _ := decodeEnvelope(t, *gotBody)

	if typ != "content_nodes" {
		t.Errorf("MIO-942: data.type = %q, want \"content_nodes\"", typ)
	}
}

// TestWritePath_ContentCreate_NodeTypeMapsToNodeTypeAttribute pins that
// --node-type sends attributes.node_type (snake_case, not node-type).
//
// CONTRACT (MIO-942): content create --node-type container → attributes.node_type = "container"
func TestWritePath_ContentCreate_NodeTypeMapsToNodeTypeAttribute(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Module 1",
			"--node-type", "container",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, *gotBody)

	if attrs["node_type"] != "container" {
		t.Errorf("MIO-942: data.attributes.node_type = %v, want \"container\"", attrs["node_type"])
	}
	// node-type (kebab) must NOT appear as a top-level attribute key.
	if _, has := attrs["node-type"]; has {
		t.Errorf("MIO-942: attributes must not carry the kebab key \"node-type\"; attrs=%v", attrs)
	}
}

// TestWritePath_ContentCreate_ContentTypeOptional pins that --content-type is
// optional and maps to attributes.content_type when provided.
//
// CONTRACT (MIO-942): content create --content-type video → attributes.content_type = "video"
func TestWritePath_ContentCreate_ContentTypeOptional(t *testing.T) {
	srv, gotBody := captureWriteRequest(t, http.StatusCreated, minimalContentBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "Welcome Video",
			"--node-type", "lesson",
			"--content-type", "video",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	_, attrs := decodeEnvelope(t, *gotBody)

	if attrs["content_type"] != "video" {
		t.Errorf("MIO-942: data.attributes.content_type = %v, want \"video\"", attrs["content_type"])
	}
}

// TestWritePath_ContentCreate_NoStaleStatusFlag pins that the stale --status
// flag no longer exists on content create (the backend ContentNodeCreateAttributes
// schema uses extra="forbid" and has no status field).
//
// CONTRACT (MIO-942): content create --status X → exit 2 (unknown flag)
func TestWritePath_ContentCreate_NoStaleStatusFlag(t *testing.T) {
	requestFired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestFired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalContentBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_abc",
			"content", "create",
			"--title", "X",
			"--node-type", "lesson",
			"--status", "draft", // stale flag — must not exist
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("MIO-942: exit code = %d, want %d (ExitUsage for removed --status flag); stderr=%q",
			res.Code, errs.ExitUsage, res.Stderr)
	}
	if requestFired {
		t.Error("MIO-942: POST must NOT be fired when an unknown flag is passed")
	}
}
