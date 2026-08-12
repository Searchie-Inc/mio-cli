package cmd

// events_test.go — contract tests for the `mio events` command group
// (Events v1 API, MIO-3173).
//
// Covers:
//   - events create: body shape (JSON:API envelope, type "hub_events",
//     snake_case attrs), required-flag validation, and the NO team_id segment
//     path shape (regression guard — events is the first resource whose base
//     path is /api/hubs/{hub_id}/events with no /teams/{team_id} prefix).
//   - events list: filter[status]/sort query params, --status/--sort
//     enum validation, --after cursor
//   - events retrieve / update: GET/PATCH path + partial-update semantics,
//     including that an explicit --attendee-list-visible=false serializes
//   - events cancel: POST .../cancel action verb (nil body)
//   - events rsvp set: PUT .../rsvp, body type "event_rsvps", --status validation
//   - events rsvp withdraw: DELETE .../rsvp, --yes gate, and that the response
//     body IS rendered (the API returns 200 with an RSVP body, not 204 — this
//     command must use client.Action, never client.Delete, which would discard it)
//   - events rsvps list: GET .../rsvps
//   - eventsContext auth: MIO_CONTACT_TOKEN is used as the bearer (Authorization
//     header) instead of the team API key, and every command fails fast with
//     ExitAuth when only a team API key is configured (Codex round 1, MIO-3173 —
//     every Events v1 route requires a contact identity a team key cannot provide)
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

// eventsEnv returns the env vars needed for `mio events` commands: the usual
// API-key/base-url pair PLUS MIO_CONTACT_TOKEN. Every Events v1 route requires
// a contact identity (see eventsContext in events.go) — a team API key alone
// 401s on all of them, so every events test that expects to reach the mock
// server must supply a contact token. Tests that specifically exercise the
// no-contact-token failure path use baseEnv directly instead.
func eventsEnv(apiBase string) []string {
	return append(baseEnv(apiBase), "MIO_CONTACT_TOKEN=test-contact-token")
}

// ── fixtures ─────────────────────────────────────────────────────────────────

// hubEventBody uses "scheduled" for status — the real backend event-status
// enum is scheduled|cancelled (never "upcoming"; "upcoming"/"past" are only
// --status FILTER values on list, not attribute values on a resource).
const hubEventBody = `{
	"data": {
		"id": "evt_1",
		"type": "hub_events",
		"attributes": {
			"title": "Community Meetup",
			"starts_at": "2026-09-01T18:00:00Z",
			"ends_at": "2026-09-01T20:00:00Z",
			"timezone": "America/New_York",
			"location_type": "url",
			"status": "scheduled"
		}
	}
}`

const eventRSVPBody = `{
	"data": {
		"id": "rsvp_1",
		"type": "event_rsvps",
		"attributes": {
			"status": "going"
		}
	}
}`

// ── events create ──────────────────────────────────────────────────────────

// TestEventsCreate_BodyShape verifies create sends a JSON:API envelope with
// type "hub_events" and the correct snake_case attribute keys, to a path with
// NO /teams/{team_id} segment.
func TestEventsCreate_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotPath string
	var gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(hubEventBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "create",
			"--title", "Community Meetup",
			"--starts-at", "2026-09-01T18:00:00Z",
			"--ends-at", "2026-09-01T20:00:00Z",
			"--timezone", "America/New_York",
			"--location-type", "url",
			"--location-url", "https://zoom.us/j/123",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", gotMethod)
	}

	// Regression guard: events is hub-scoped ONLY — no /teams/{team_id} segment.
	if strings.Contains(gotPath, "/teams/") {
		t.Errorf("path %q must NOT contain a /teams/ segment (events has no team_id in its route)", gotPath)
	}
	if !strings.Contains(gotPath, "/hubs/hub_123/events") {
		t.Errorf("path %q does not contain /hubs/hub_123/events", gotPath)
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

	if doc.Data.Type != "hub_events" {
		t.Errorf("envelope type = %q, want \"hub_events\"", doc.Data.Type)
	}
	attrs := doc.Data.Attributes
	if attrs["title"] != "Community Meetup" {
		t.Errorf("attributes.title = %v, want \"Community Meetup\"", attrs["title"])
	}
	if attrs["starts_at"] != "2026-09-01T18:00:00Z" {
		t.Errorf("attributes.starts_at = %v, want RFC3339 string", attrs["starts_at"])
	}
	if attrs["ends_at"] != "2026-09-01T20:00:00Z" {
		t.Errorf("attributes.ends_at = %v, want RFC3339 string", attrs["ends_at"])
	}
	if attrs["timezone"] != "America/New_York" {
		t.Errorf("attributes.timezone = %v, want \"America/New_York\"", attrs["timezone"])
	}
	if attrs["location_type"] != "url" {
		t.Errorf("attributes.location_type = %v, want \"url\"", attrs["location_type"])
	}
	if attrs["location_url"] != "https://zoom.us/j/123" {
		t.Errorf("attributes.location_url = %v, want the zoom URL", attrs["location_url"])
	}
}

// TestEventsCreate_MissingRequiredFlags verifies that omitting any of the
// required flags (title, starts-at, ends-at, timezone, location-type) exits 2.
func TestEventsCreate_MissingRequiredFlags(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "create",
			"--title", "Community Meetup",
			"--starts-at", "2026-09-01T18:00:00Z",
			// --ends-at, --timezone, --location-type intentionally omitted
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestEventsCreate_OptionalAttrs verifies optional flags (capacity, visibility,
// segment-id, rsvp-tag-id, attendee-list-visible, description, cover-image-url)
// are translated to the correct snake_case attribute keys when set.
func TestEventsCreate_OptionalAttrs(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(hubEventBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "create",
			"--title", "Members-Only Session",
			"--starts-at", "2026-09-01T18:00:00Z",
			"--ends-at", "2026-09-01T20:00:00Z",
			"--timezone", "America/New_York",
			"--location-type", "address",
			"--location-address", "123 Main St",
			"--description", "A great session",
			"--cover-image-url", "https://example.com/cover.png",
			"--capacity", "50",
			"--visibility", "segment",
			"--segment-id", "seg_abc",
			"--rsvp-tag-id", "tag_xyz",
			"--attendee-list-visible",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	attrs := doc.Data.Attributes

	if attrs["location_address"] != "123 Main St" {
		t.Errorf("attributes.location_address = %v", attrs["location_address"])
	}
	if attrs["description"] != "A great session" {
		t.Errorf("attributes.description = %v", attrs["description"])
	}
	if attrs["cover_image_url"] != "https://example.com/cover.png" {
		t.Errorf("attributes.cover_image_url = %v", attrs["cover_image_url"])
	}
	if attrs["capacity"] != float64(50) {
		t.Errorf("attributes.capacity = %v, want 50", attrs["capacity"])
	}
	if attrs["visibility"] != "segment" {
		t.Errorf("attributes.visibility = %v, want \"segment\"", attrs["visibility"])
	}
	if attrs["segment_id"] != "seg_abc" {
		t.Errorf("attributes.segment_id = %v", attrs["segment_id"])
	}
	if attrs["rsvp_tag_id"] != "tag_xyz" {
		t.Errorf("attributes.rsvp_tag_id = %v", attrs["rsvp_tag_id"])
	}
	if attrs["attendee_list_visible"] != true {
		t.Errorf("attributes.attendee_list_visible = %v, want true", attrs["attendee_list_visible"])
	}
}

// ── events list ───────────────────────────────────────────────────────────

// TestEventsList_QueryParams verifies --status and --sort map to
// filter[status] and sort query params, and the path has no /teams/ segment.
func TestEventsList_QueryParams(t *testing.T) {
	var gotPath string
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":{"has_more":false}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "list",
			"--status", "upcoming",
			"--sort", "-starts_at",
			"--limit", "10",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(gotPath, "/teams/") {
		t.Errorf("path %q must NOT contain a /teams/ segment", gotPath)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/events") {
		t.Errorf("path %q does not end with /hubs/hub_123/events", gotPath)
	}
	if !strings.Contains(gotQuery, "filter%5Bstatus%5D=upcoming") {
		t.Errorf("query %q missing filter[status]=upcoming", gotQuery)
	}
	if !strings.Contains(gotQuery, "sort=-starts_at") {
		t.Errorf("query %q missing sort=-starts_at", gotQuery)
	}
	if !strings.Contains(gotQuery, "page%5Bsize%5D=10") {
		t.Errorf("query %q missing page[size]=10", gotQuery)
	}
}

// ── events retrieve ───────────────────────────────────────────────────────

// TestEventsRetrieve_Path verifies retrieve issues a GET to the correct
// single-resource path.
func TestEventsRetrieve_Path(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubEventBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "retrieve", "evt_1")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/events/evt_1") {
		t.Errorf("path %q does not end with /hubs/hub_123/events/evt_1", gotPath)
	}
}

// ── events update ─────────────────────────────────────────────────────────

// TestEventsUpdate_BodyShape verifies update sends a PATCH with only the
// changed flags, type "hub_events".
func TestEventsUpdate_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubEventBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "update", "evt_1",
			"--title", "Updated Title",
			"--capacity", "100",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/events/evt_1") {
		t.Errorf("path %q does not end with /hubs/hub_123/events/evt_1", gotPath)
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
	if doc.Data.Type != "hub_events" {
		t.Errorf("envelope type = %q, want \"hub_events\"", doc.Data.Type)
	}
	if len(doc.Data.Attributes) != 2 {
		t.Errorf("attributes = %v, want exactly 2 keys (title, capacity)", doc.Data.Attributes)
	}
	if doc.Data.Attributes["title"] != "Updated Title" {
		t.Errorf("attributes.title = %v, want \"Updated Title\"", doc.Data.Attributes["title"])
	}
	if doc.Data.Attributes["capacity"] != float64(100) {
		t.Errorf("attributes.capacity = %v, want 100", doc.Data.Attributes["capacity"])
	}
}

// TestEventsUpdate_NothingToUpdate verifies that update with no field flags
// set exits 2.
func TestEventsUpdate_NothingToUpdate(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "update", "evt_1")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── events cancel ─────────────────────────────────────────────────────────

// TestEventsCancel_PostToCancelPath verifies cancel sends a nil-body POST to
// .../cancel and renders the returned resource.
func TestEventsCancel_PostToCancelPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte

	const cancelledBody = `{
		"data": {
			"id": "evt_1",
			"type": "hub_events",
			"attributes": {"status": "cancelled"}
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(cancelledBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "cancel", "evt_1", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/events/evt_1/cancel") {
		t.Errorf("path %q does not end with /hubs/hub_123/events/evt_1/cancel", gotPath)
	}
	if len(strings.TrimSpace(string(gotBody))) != 0 {
		t.Errorf("cancel request body = %q, want empty (nil body action)", gotBody)
	}
	if !strings.Contains(res.Stdout, "cancelled") {
		t.Errorf("stdout does not contain rendered cancelled resource: %q", res.Stdout)
	}
}

// TestEventsCancel_RequiresYes verifies cancel without --yes exits 5 in a
// non-TTY shell and never calls the API.
func TestEventsCancel_RequiresYes(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "cancel", "evt_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

// ── events rsvp set ───────────────────────────────────────────────────────

// TestEventsRSVPSet_BodyShape verifies rsvp set sends a PUT to .../rsvp with
// a JSON:API envelope of type "event_rsvps" and attributes.status set.
func TestEventsRSVPSet_BodyShape(t *testing.T) {
	var gotBody []byte
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(eventRSVPBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "rsvp", "set", "evt_1",
			"--status", "going",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/events/evt_1/rsvp") {
		t.Errorf("path %q does not end with /hubs/hub_123/events/evt_1/rsvp", gotPath)
	}
	if strings.Contains(gotPath, "/teams/") {
		t.Errorf("path %q must NOT contain a /teams/ segment", gotPath)
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
	if doc.Data.Type != "event_rsvps" {
		t.Errorf("envelope type = %q, want \"event_rsvps\"", doc.Data.Type)
	}
	if doc.Data.Attributes["status"] != "going" {
		t.Errorf("attributes.status = %v, want \"going\"", doc.Data.Attributes["status"])
	}
}

// TestEventsRSVPSet_NotGoing verifies --status not_going is accepted and
// forwarded verbatim.
func TestEventsRSVPSet_NotGoing(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(eventRSVPBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "rsvp", "set", "evt_1",
			"--status", "not_going",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	if doc.Data.Attributes["status"] != "not_going" {
		t.Errorf("attributes.status = %v, want \"not_going\"", doc.Data.Attributes["status"])
	}
}

// TestEventsRSVPSet_MissingStatus verifies omitting --status exits 2.
func TestEventsRSVPSet_MissingStatus(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "rsvp", "set", "evt_1")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestEventsRSVPSet_InvalidStatus verifies an out-of-enum --status value
// exits 2 without hitting the API.
func TestEventsRSVPSet_InvalidStatus(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "rsvp", "set", "evt_1",
			"--status", "maybe",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// ── events rsvp withdraw ──────────────────────────────────────────────────

// TestEventsRSVPWithdraw_RequiresYes verifies withdraw without --yes exits 5
// and never calls the API.
func TestEventsRSVPWithdraw_RequiresYes(t *testing.T) {
	srv := newMockServer(t, nil) // DELETE must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "rsvp", "withdraw", "evt_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

// TestEventsRSVPWithdraw_WithYes_RendersBody is the regression guard for the
// 200-with-body contract: the withdraw endpoint returns 200 with the RSVP
// resource transitioned to "not_going" (the real backend RSVP-status enum is
// going|not_going — there is no separate "withdrawn" status), NOT 204. The
// command must render it (client.Action), not discard it (client.Delete
// would swallow the body on a 200).
func TestEventsRSVPWithdraw_WithYes_RendersBody(t *testing.T) {
	var gotMethod, gotPath string

	const withdrawnBody = `{
		"data": {
			"id": "rsvp_1",
			"type": "event_rsvps",
			"attributes": {"status": "not_going"}
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK) // 200, NOT 204 — this is the real contract
		_, _ = w.Write([]byte(withdrawnBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "rsvp", "withdraw", "evt_1", "--yes")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("HTTP method = %q, want DELETE", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/events/evt_1/rsvp") {
		t.Errorf("path %q does not end with /hubs/hub_123/events/evt_1/rsvp", gotPath)
	}
	// The 200 response body must be rendered, not discarded.
	if !strings.Contains(res.Stdout, "not_going") {
		t.Errorf("stdout does not contain the rendered RSVP body (status=not_going); stdout=%q", res.Stdout)
	}
}

// ── events rsvps list ─────────────────────────────────────────────────────

// TestEventsRSVPsList_GetPath verifies rsvps list issues a GET to
// .../events/{id}/rsvps.
func TestEventsRSVPsList_GetPath(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":{"has_more":false}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "rsvps", "list", "evt_1", "--limit", "5")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_123/events/evt_1/rsvps") {
		t.Errorf("path %q does not end with /hubs/hub_123/events/evt_1/rsvps", gotPath)
	}
}

// ── events list: --status / --sort enum validation ───────────────────────────

// TestEventsList_InvalidStatus verifies an out-of-enum --status value exits 2
// without hitting the API — the backend silently mistreats unknown values
// rather than rejecting them (Codex round 1, MIO-3173).
func TestEventsList_InvalidStatus(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "list", "--status", "live")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestEventsList_InvalidSort verifies an out-of-enum --sort value exits 2
// without hitting the API.
func TestEventsList_InvalidSort(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "list", "--sort", "title")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestEventsList_AfterCursor verifies --after maps to page[after], alongside
// --limit → page[size] (already covered by TestEventsList_QueryParams).
func TestEventsList_AfterCursor(t *testing.T) {
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":{"has_more":false}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "list", "--after", "cursor_abc123")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(gotQuery, "page%5Bafter%5D=cursor_abc123") {
		t.Errorf("query %q missing page[after]=cursor_abc123", gotQuery)
	}
}

// ── events update: explicit --attendee-list-visible=false ────────────────────

// TestEventsUpdate_AttendeeListVisibleFalseSerializes verifies that an
// EXPLICIT --attendee-list-visible=false is sent in the request body (present
// with value false), not silently dropped because false looks like the flag's
// zero value. setBoolFlag gates on cmd.Flags().Changed, not on the value, so
// this should already work — this test pins it as a regression guard.
func TestEventsUpdate_AttendeeListVisibleFalseSerializes(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubEventBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "update", "evt_1",
			"--attendee-list-visible=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	v, ok := doc.Data.Attributes["attendee_list_visible"]
	if !ok {
		t.Fatalf("attributes.attendee_list_visible is absent, want explicit false present; body=%q", gotBody)
	}
	if v != false {
		t.Errorf("attributes.attendee_list_visible = %v, want false", v)
	}
}

// ── eventsContext auth: contact-token bearer swap ─────────────────────────────

// TestEventsContext_UsesContactTokenBearer is the regression guard for the
// Critical finding in Codex round 1 (MIO-3173): every Events v1 route requires
// a contact identity, which a team API key cannot provide. When
// MIO_CONTACT_TOKEN is configured, events commands MUST send it as the
// Authorization bearer — not the team API key.
func TestEventsContext_UsesContactTokenBearer(t *testing.T) {
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":{"has_more":false}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotAuth != "Bearer test-contact-token" {
		t.Errorf("Authorization header = %q, want %q (must bearer-swap to the contact token, "+
			"never the team API key)", gotAuth, "Bearer test-contact-token")
	}
}

// TestEventsContext_NoContactToken_FailsFast verifies that with only a team
// API key configured (no MIO_CONTACT_TOKEN) events commands fail fast with
// ExitAuth and never reach the network — instead of round-tripping to a
// guaranteed 401 on every single command.
func TestEventsContext_NoContactToken_FailsFast(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, baseEnv(srv.URL), // API key only, no MIO_CONTACT_TOKEN
		withTeam("t_team1", "--hub", "hub_123", "events", "list")...)

	if res.Code != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth); stderr=%q", res.Code, errs.ExitAuth, res.Stderr)
	}
}

// TestEventsContext_NoContactToken_HonestErrorMessage pins the exact guidance
// of the fail-fast auth error: it must point the caller at MIO_CONTACT_TOKEN
// and must NOT tell them to "run `mio login`" — that command mints and stores
// only a team API key today (see the package doc comment in events.go) and
// can never satisfy this precondition, so that advice would be actively
// misleading (corrective round, MIO-3173). Drives the real binary via
// buildBinary/runBinary (like the TestContract_ErrorEnvelope_* tests in
// contract_test.go) because the rendered JSON:API envelope is written by
// main.go to os.Stderr after os.Exit — only a subprocess can capture it; the
// in-process runContract harness never populates res.Stderr for a RunE error.
func TestEventsContext_NoContactToken_HonestErrorMessage(t *testing.T) {
	bin := buildBinary(t)

	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
		// No MIO_CONTACT_TOKEN. The auth gate fires before any network call,
		// so the API base is never actually dialed.
		"MIO_API_BASE_URL=http://127.0.0.1:1",
	}, "--team", "t_team1", "--hub", "hub_123", "events", "list")

	if exitCode != errs.ExitAuth {
		t.Fatalf("exit code = %d, want %d (ExitAuth); stderr=%q", exitCode, errs.ExitAuth, stderr)
	}

	raw := strings.TrimSpace(stderr)
	var envelope struct {
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("stderr not valid JSON:API envelope: %v; stderr=%q", err, raw)
	}
	if len(envelope.Errors) == 0 {
		t.Fatalf("error envelope has empty errors array; stderr=%q", raw)
	}
	detail := envelope.Errors[0].Detail

	if !strings.Contains(detail, "MIO_CONTACT_TOKEN") {
		t.Errorf("error detail does not mention MIO_CONTACT_TOKEN; detail=%q", detail)
	}
	if strings.Contains(detail, "run `mio login`") {
		t.Errorf("error detail must NOT tell the caller to run `mio login` — that command "+
			"cannot produce a contact token; detail=%q", detail)
	}
}

// TestEventsContext_NoCredentialsAtAll_FailsFast verifies the same fail-fast
// gate fires when NEITHER a team API key NOR a contact token is configured
// (as opposed to an API key being present but insufficient).
func TestEventsContext_NoCredentialsAtAll_FailsFast(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	// "MIO_API_KEY=" (empty value) unsets the var via overlayEnv's convention,
	// guarding against ambient-environment leakage into the test.
	res := runContract(t, []string{"MIO_API_BASE_URL=" + srv.URL, "MIO_API_KEY="}, // no key, no token
		withTeam("t_team1", "--hub", "hub_123", "events", "list")...)

	if res.Code != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth); stderr=%q", res.Code, errs.ExitAuth, res.Stderr)
	}
}

// TestEventsContext_AnonymousBypassesGate verifies --anonymous is honoured as
// a deliberate unauthenticated probe (mirroring requireAuth's MIO-2694
// precedent elsewhere in the CLI) even with no contact token configured: the
// request reaches the server with no Authorization header, rather than being
// blocked by the events-specific auth gate.
func TestEventsContext_AnonymousBypassesGate(t *testing.T) {
	var called bool
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":{"has_more":false}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL), // API key present, but --anonymous overrides it
		withTeam("t_team1", "--hub", "hub_123", "--anonymous", "events", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !called {
		t.Fatal("--anonymous must still reach the server (deliberate unauthenticated probe)")
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty under --anonymous", gotAuth)
	}
}
