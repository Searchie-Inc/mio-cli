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
// exits 2 without hitting the API. "attending" (not "maybe") is the example
// here: "maybe" became a legal third RSVP state (MIO-maybe-rsvp), so it can
// no longer stand in for an invalid value — see TestEventsRSVPSet_Maybe for
// its positive-path coverage instead.
func TestEventsRSVPSet_InvalidStatus(t *testing.T) {
	srv := newMockServer(t, nil) // must not be called

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "rsvp", "set", "evt_1",
			"--status", "attending",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestEventsRSVPSet_Maybe verifies --status maybe is accepted and forwarded
// verbatim to the SAME wire contract as going/not_going: PUT to
// .../events/{id}/rsvp with a JSON:API envelope of type "event_rsvps" and
// attributes.status set. "Maybe" is a real third RSVP state (product ruling,
// Slack #8-mio-development 2026-09-07, thread 1788527517.683859): it keeps
// the event in the member's "My Events" tab without committing to "going".
// The CLI has no capacity or notification logic of its own (that lives in
// mio-backend) — it only needs to stop rejecting the value and pass it
// through, exactly like "going"/"not_going" above.
//
// This asserts method + path + envelope type, not just the status attribute
// (MIO-3739 review round): a body-only oracle stayed green when "maybe" was
// mutated to route to a different verb/path/envelope entirely, because
// nothing here could tell the two implementations apart — see
// .claude/rules/verifying-guards.md ("probe set smaller than the claim").
func TestEventsRSVPSet_Maybe(t *testing.T) {
	var gotBody []byte
	var gotMethod, gotPath string

	// The full wire path, including the /v1 the client injects — eventsPath's
	// doc comment describes the pre-version prefix only.
	const wantRSVPPath = "/api/v1/hubs/hub_123/events/evt_1/rsvp"

	const maybeRSVPBody = `{
		"data": {
			"id": "rsvp_1",
			"type": "event_rsvps",
			"attributes": {
				"status": "maybe"
			}
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(maybeRSVPBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "rsvp", "set", "evt_1",
			"--status", "maybe",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", gotMethod)
	}
	// Exact equality, not HasSuffix: the contract is that events paths carry
	// NO team_id segment (see eventsPath's doc comment). A suffix match is
	// satisfied by "/api/teams/t_team1/hubs/hub_123/events/evt_1/rsvp" too, so
	// it cannot fail in the one direction this assertion exists to catch.
	if gotPath != wantRSVPPath {
		t.Errorf("path = %q, want %q", gotPath, wantRSVPPath)
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
	if doc.Data.Attributes["status"] != "maybe" {
		t.Errorf("attributes.status = %v, want \"maybe\"", doc.Data.Attributes["status"])
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
// going|not_going|maybe — there is no separate "withdrawn" status), NOT 204.
// The command must render it (client.Action), not discard it (client.Delete
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

// TestEventsContext_ScopeResolutionUsesAPIKeyClient is the regression guard
// for the Important finding in Codex round 2 (MIO-3173): team/hub SCOPE
// RESOLUTION (--hub given as a name/slug rather than a raw id) must use the
// team-API-key client, never the contact-token client. Before the fix,
// eventsContext swapped in the contact bearer before calling
// requireTeam/requireHub, so a hub-slug lookup sent the contact JWT to the
// platform-scoped GET /api/teams/{team_id}/hubs route — a different expected
// audience than the contact-JWT-only Events v1 routes, which the backend
// rejects.
//
// --hub is passed as a SLUG ("my-hub", not "hub_123") specifically to force
// requireHub to actually call ResolveHub (an id-shaped value like "hub_123"
// passes through with no API call at all — see internal/client/resolve.go —
// so it wouldn't exercise this path).
func TestEventsContext_ScopeResolutionUsesAPIKeyClient(t *testing.T) {
	var gotHubListAuth, gotHubListPath string
	var gotEventsAuth, gotEventsPath string

	const hubListBody = `{
		"data": [
			{"id": "hub_abc", "type": "hubs", "attributes": {"name": "My Hub", "slug": "my-hub"}}
		],
		"meta": {"page": {"has_more": false}}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		if strings.Contains(r.URL.Path, "/teams/") {
			// Hub-slug resolution: GET /api/teams/{team_id}/hubs.
			gotHubListAuth = r.Header.Get("Authorization")
			gotHubListPath = r.URL.Path
			_, _ = w.Write([]byte(hubListBody))
			return
		}
		// The actual Events v1 request: GET /api/hubs/{hub_id}/events.
		gotEventsAuth = r.Header.Get("Authorization")
		gotEventsPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[],"meta":{"page":{"has_more":false}}}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "my-hub", "events", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(gotHubListPath, "/teams/t_team1/hubs") {
		t.Fatalf("hub-slug resolution never happened (or hit the wrong path); gotHubListPath=%q", gotHubListPath)
	}
	if gotHubListAuth != "Bearer test-key-contract" {
		t.Errorf("hub-slug resolution Authorization = %q, want the TEAM API KEY bearer "+
			"(%q) — scope resolution must never use the contact token", gotHubListAuth, "Bearer test-key-contract")
	}
	if !strings.HasSuffix(gotEventsPath, "/hubs/hub_abc/events") {
		t.Errorf("events path %q does not end with /hubs/hub_abc/events (the resolved hub id)", gotEventsPath)
	}
	if gotEventsAuth != "Bearer test-contact-token" {
		t.Errorf("events request Authorization = %q, want the CONTACT TOKEN bearer (%q)",
			gotEventsAuth, "Bearer test-contact-token")
	}
}

// TestEventsContext_NoContactToken_FailsFast verifies that with only a team
// API key configured (no MIO_CONTACT_TOKEN) events commands fail fast with
// ExitAuth and never reach the network — instead of round-tripping to a
// guaranteed 401 on every single command.
func TestEventsContext_NoContactToken_FailsFast(t *testing.T) {
	// Guard against ambient-environment leakage: if the machine running this
	// test happens to have MIO_CONTACT_TOKEN exported (e.g. a developer's own
	// shell profile), baseEnv alone would not clear it and this test's
	// "team key only" premise would silently stop being tested.
	t.Setenv(eventsContactTokenEnv, "")

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

// NOTE: an earlier version of this file had a
// TestEventsContext_NoCredentialsAtAll_FailsFast test (no API key AND no
// contact token, not anonymous). It was removed: with no MIO_API_KEY env var
// and no --api-key flag, config.Resolve falls through to the real OS
// keychain (GetAPIKey), which on an interactive macOS dev machine can block
// indefinitely on a Keychain access prompt that never gets a response in an
// automated/headless run — observed as a real >120s hang while gating round
// 3. eventsContext's fail-fast check (Step 1) never inspects
// c.resolved.APIKey at all, so that test exercised the exact same branch as
// TestEventsContext_NoContactToken_FailsFast below (API key present, no
// token) with zero additional code-path coverage — not worth the flake risk.
// The internal/config package's own tests already cover the real
// GetAPIKey/keychain behavior hermetically via withFileBackendOnly.

// TestEventsContext_AnonymousBypassesGate verifies --anonymous is honoured as
// a deliberate unauthenticated probe (mirroring requireAuth's MIO-2694
// precedent elsewhere in the CLI) even with no contact token configured: the
// request reaches the server with no Authorization header, rather than being
// blocked by the events-specific auth gate.
func TestEventsContext_AnonymousBypassesGate(t *testing.T) {
	// Guard against ambient-environment leakage (see the identical comment in
	// TestEventsContext_NoContactToken_FailsFast) — this test's premise is
	// specifically "no contact token", so an ambient one must not leak in.
	t.Setenv(eventsContactTokenEnv, "")

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

// TestEventsContext_AnonymousWinsOverContactToken is the regression guard for
// the Critical finding in Codex round 2 (MIO-3173): --anonymous MUST win over
// an ambient MIO_CONTACT_TOKEN, not the other way around. Before the fix, the
// switch checked "contactToken != \"\"" before "--anonymous", so a real
// contact token configured in the environment silently defeated an explicit
// --anonymous request and leaked a live bearer credential to whatever
// --api-base was in play. This test sets a real contact token (via eventsEnv)
// AND --anonymous together and asserts the request still carries NO
// Authorization header.
func TestEventsContext_AnonymousWinsOverContactToken(t *testing.T) {
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

	res := runContract(t, eventsEnv(srv.URL), // MIO_CONTACT_TOKEN IS set here
		withTeam("t_team1", "--hub", "hub_123", "--anonymous", "events", "list")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !called {
		t.Fatal("--anonymous must still reach the server (deliberate unauthenticated probe)")
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty — --anonymous must win over a "+
			"configured MIO_CONTACT_TOKEN, never send it despite an explicit --anonymous request", gotAuth)
	}
}

// ── events hosts (MIO-3740) ───────────────────────────────────────────────
//
// The backend makes an event's host CLIENT-SETTABLE and PLURAL:
//
//   - host_contact_id  (string)      — sugar for host_contact_ids=[value]
//   - host_contact_ids (list[str])   — 1..MAX_HOSTS_PER_EVENT (3) entries,
//     duplicates rejected
//   - the two are MUTUALLY EXCLUSIVE; sending both is a 422
//   - on create, omitting both stamps the authenticated caller as sole host
//
// (mio-backend app/hub_events/schemas.py: HubEventCreateAttributes /
// HubEventUpdateAttributes and their _reject_ambiguous_host_fields validator;
// the cap is MAX_HOSTS_PER_EVENT in app/hub_events/models.py.)
//
// These tests assert the WIRE — the exact request path and the exact
// attributes object — not merely that a request happened. The path is
// compared for EQUALITY, not with strings.HasSuffix: a suffix match would
// happily accept /api/v1/teams/{team}/hubs/hub_123/events, and events is the
// one resource whose route carries no team segment at all. The /v1 in the
// expected path is injected by the client (canonicalRequestPath), not by
// eventsPath.

const (
	eventsCreateWirePath = "/api/v1/hubs/hub_123/events"
	eventsUpdateWirePath = "/api/v1/hubs/hub_123/events/evt_1"
)

// recordedEventRequest is one request the host-flag mock server saw.
type recordedEventRequest struct {
	Method string
	Path   string
	Body   string
}

// newHostRecordingServer starts a mock server that records EVERY request it
// receives — including ones it would otherwise ignore — so a test can assert
// that a rejected flag combination sent NOTHING AT ALL. The returned slice
// pointer is read after runContract returns.
func newHostRecordingServer(t *testing.T, status int, body string) (*httptest.Server, *[]recordedEventRequest) {
	t.Helper()
	got := &[]recordedEventRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*got = append(*got, recordedEventRequest{Method: r.Method, Path: r.URL.Path, Body: string(raw)})
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// eventAttributesFrom decodes the JSON:API attributes object out of a recorded
// request body.
func eventAttributesFrom(t *testing.T, body string) map[string]any {
	t.Helper()
	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, body)
	}
	if doc.Data.Type != "hub_events" {
		t.Errorf("envelope type = %q, want \"hub_events\"", doc.Data.Type)
	}
	return doc.Data.Attributes
}

// wantHostContactIDs asserts attrs.host_contact_ids is exactly want (a JSON
// array of strings, in order) and that the singular host_contact_id is absent.
func wantHostContactIDs(t *testing.T, attrs map[string]any, want []string) {
	t.Helper()
	raw, ok := attrs["host_contact_ids"]
	if !ok {
		t.Fatalf("attributes has no host_contact_ids key; attributes=%v", attrs)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("host_contact_ids = %#v, want a JSON array (a comma-joined string would be one bad id)", raw)
	}
	if len(list) != len(want) {
		t.Fatalf("host_contact_ids = %v, want %v", list, want)
	}
	for i, w := range want {
		if list[i] != w {
			t.Errorf("host_contact_ids[%d] = %v, want %q", i, list[i], w)
		}
	}
	if _, present := attrs["host_contact_id"]; present {
		t.Errorf("host_contact_id must NOT be sent alongside host_contact_ids (the API 422s both together); attributes=%v", attrs)
	}
}

// eventsCreateArgs returns a minimal, valid `events create` invocation with the
// given extra flags appended.
func eventsCreateArgs(extra ...string) []string {
	base := []string{
		"--hub", "hub_123",
		"events", "create",
		"--title", "Community Meetup",
		"--starts-at", "2026-09-01T18:00:00Z",
		"--ends-at", "2026-09-01T20:00:00Z",
		"--timezone", "America/New_York",
		"--location-type", "url",
		"--location-url", "https://zoom.us/j/123",
	}
	return withTeam("t_team1", append(base, extra...)...)
}

// eventsUpdateArgs returns a minimal `events update` invocation with the given
// extra flags appended.
func eventsUpdateArgs(extra ...string) []string {
	base := []string{"--hub", "hub_123", "events", "update", "evt_1"}
	return withTeam("t_team1", append(base, extra...)...)
}

// TestEventsCreate_HostContactIDsCommaSeparated pins the plural flag onto the
// wire: --host-contact-ids a,b must serialize as the JSON array
// host_contact_ids: ["a","b"], to the exact events path.
func TestEventsCreate_HostContactIDsCommaSeparated(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusCreated, hubEventBody)

	res := runContract(t, eventsEnv(srv.URL), eventsCreateArgs("--host-contact-ids", "con_a,con_b")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(*got) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1: %+v", len(*got), *got)
	}
	req := (*got)[0]
	if req.Method != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", req.Method)
	}
	if req.Path != eventsCreateWirePath {
		t.Errorf("request path = %q, want exactly %q", req.Path, eventsCreateWirePath)
	}
	wantHostContactIDs(t, eventAttributesFrom(t, req.Body), []string{"con_a", "con_b"})
}

// TestEventsCreate_HostContactIDsRepeatedFlagSameBody pins cobra's StringSlice
// contract: the repeated form (--host-contact-ids a --host-contact-ids b) and
// the comma form must produce the SAME wire body, byte for byte.
func TestEventsCreate_HostContactIDsRepeatedFlagSameBody(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusCreated, hubEventBody)

	repeated := runContract(t, eventsEnv(srv.URL),
		eventsCreateArgs("--host-contact-ids", "con_a", "--host-contact-ids", "con_b")...)
	if repeated.Code != errs.ExitOK {
		t.Fatalf("repeated form: exit code = %d, want %d (ExitOK); stderr=%q", repeated.Code, errs.ExitOK, repeated.Stderr)
	}

	comma := runContract(t, eventsEnv(srv.URL), eventsCreateArgs("--host-contact-ids", "con_a,con_b")...)
	if comma.Code != errs.ExitOK {
		t.Fatalf("comma form: exit code = %d, want %d (ExitOK); stderr=%q", comma.Code, errs.ExitOK, comma.Stderr)
	}

	if len(*got) != 2 {
		t.Fatalf("server saw %d requests, want exactly 2: %+v", len(*got), *got)
	}
	if (*got)[0].Path != eventsCreateWirePath {
		t.Errorf("request path = %q, want exactly %q", (*got)[0].Path, eventsCreateWirePath)
	}
	wantHostContactIDs(t, eventAttributesFrom(t, (*got)[0].Body), []string{"con_a", "con_b"})

	if (*got)[0].Body != (*got)[1].Body {
		t.Errorf("repeated-flag body != comma-separated body:\n repeated=%s\n comma   =%s",
			(*got)[0].Body, (*got)[1].Body)
	}
}

// TestEventsCreate_HostContactIDSingular pins the singular sugar: it must send
// host_contact_id (a bare string) and must NOT also send host_contact_ids —
// the API 422s the pair.
func TestEventsCreate_HostContactIDSingular(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusCreated, hubEventBody)

	res := runContract(t, eventsEnv(srv.URL), eventsCreateArgs("--host-contact-id", "con_solo")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(*got) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1: %+v", len(*got), *got)
	}
	if (*got)[0].Path != eventsCreateWirePath {
		t.Errorf("request path = %q, want exactly %q", (*got)[0].Path, eventsCreateWirePath)
	}
	attrs := eventAttributesFrom(t, (*got)[0].Body)
	if attrs["host_contact_id"] != "con_solo" {
		t.Errorf("attributes.host_contact_id = %v, want \"con_solo\"", attrs["host_contact_id"])
	}
	if _, present := attrs["host_contact_ids"]; present {
		t.Errorf("--host-contact-id must NOT also send the plural host_contact_ids "+
			"(sending both is a 422); attributes=%v", attrs)
	}
}

// TestEventsCreate_NoHostFlagsSendsNeitherField pins the default: with neither
// host flag set, NEITHER field appears in the body, so the backend stamps the
// authenticated caller as sole host (today's behaviour, unchanged).
func TestEventsCreate_NoHostFlagsSendsNeitherField(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusCreated, hubEventBody)

	res := runContract(t, eventsEnv(srv.URL), eventsCreateArgs()...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(*got) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1: %+v", len(*got), *got)
	}
	attrs := eventAttributesFrom(t, (*got)[0].Body)
	if _, present := attrs["host_contact_id"]; present {
		t.Errorf("host_contact_id must be absent when no host flag is set; attributes=%v", attrs)
	}
	if _, present := attrs["host_contact_ids"]; present {
		t.Errorf("host_contact_ids must be absent when no host flag is set; attributes=%v", attrs)
	}
}

// TestEventsUpdate_HostContactIDsCommaSeparated is the update-side twin of the
// create test: the plural flag on the wire, at the exact single-resource path.
func TestEventsUpdate_HostContactIDsCommaSeparated(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusOK, hubEventBody)

	res := runContract(t, eventsEnv(srv.URL), eventsUpdateArgs("--host-contact-ids", "con_a,con_b")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(*got) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1: %+v", len(*got), *got)
	}
	req := (*got)[0]
	if req.Method != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", req.Method)
	}
	if req.Path != eventsUpdateWirePath {
		t.Errorf("request path = %q, want exactly %q", req.Path, eventsUpdateWirePath)
	}
	attrs := eventAttributesFrom(t, req.Body)
	wantHostContactIDs(t, attrs, []string{"con_a", "con_b"})
	// PATCH stays partial: the host set is the ONLY thing this request changes.
	if len(attrs) != 1 {
		t.Errorf("attributes = %v, want exactly 1 key (host_contact_ids)", attrs)
	}
}

// TestEventsUpdate_HostContactIDsRepeatedFlagSameBody — repeated form, update side.
func TestEventsUpdate_HostContactIDsRepeatedFlagSameBody(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusOK, hubEventBody)

	repeated := runContract(t, eventsEnv(srv.URL),
		eventsUpdateArgs("--host-contact-ids", "con_a", "--host-contact-ids", "con_b")...)
	if repeated.Code != errs.ExitOK {
		t.Fatalf("repeated form: exit code = %d, want %d (ExitOK); stderr=%q", repeated.Code, errs.ExitOK, repeated.Stderr)
	}
	comma := runContract(t, eventsEnv(srv.URL), eventsUpdateArgs("--host-contact-ids", "con_a,con_b")...)
	if comma.Code != errs.ExitOK {
		t.Fatalf("comma form: exit code = %d, want %d (ExitOK); stderr=%q", comma.Code, errs.ExitOK, comma.Stderr)
	}

	if len(*got) != 2 {
		t.Fatalf("server saw %d requests, want exactly 2: %+v", len(*got), *got)
	}
	if (*got)[0].Path != eventsUpdateWirePath {
		t.Errorf("request path = %q, want exactly %q", (*got)[0].Path, eventsUpdateWirePath)
	}
	wantHostContactIDs(t, eventAttributesFrom(t, (*got)[0].Body), []string{"con_a", "con_b"})
	if (*got)[0].Body != (*got)[1].Body {
		t.Errorf("repeated-flag body != comma-separated body:\n repeated=%s\n comma   =%s",
			(*got)[0].Body, (*got)[1].Body)
	}
}

// TestEventsUpdate_HostContactIDSingular — singular sugar, update side.
func TestEventsUpdate_HostContactIDSingular(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusOK, hubEventBody)

	res := runContract(t, eventsEnv(srv.URL), eventsUpdateArgs("--host-contact-id", "con_solo")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(*got) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1: %+v", len(*got), *got)
	}
	if (*got)[0].Path != eventsUpdateWirePath {
		t.Errorf("request path = %q, want exactly %q", (*got)[0].Path, eventsUpdateWirePath)
	}
	attrs := eventAttributesFrom(t, (*got)[0].Body)
	if attrs["host_contact_id"] != "con_solo" {
		t.Errorf("attributes.host_contact_id = %v, want \"con_solo\"", attrs["host_contact_id"])
	}
	if _, present := attrs["host_contact_ids"]; present {
		t.Errorf("--host-contact-id must NOT also send the plural host_contact_ids; attributes=%v", attrs)
	}
}

// TestEventsHostFlags_RejectedBeforeAnyRequest is the guard the whole feature
// rests on: every host-flag combination the API would 422 must exit ExitUsage
// LOCALLY, having sent the mock server NOTHING. A test that only checked the
// exit code would pass even if the CLI round-tripped the bad body first, so
// the "server saw 0 requests" assertion is the load-bearing half.
func TestEventsHostFlags_RejectedBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
	}{
		// Mutually exclusive — the API's _reject_ambiguous_host_fields 422s
		// the pair; cobra's MarkFlagsMutuallyExclusive rejects it here first.
		{"both host flags", []string{"--host-contact-id", "con_a", "--host-contact-ids", "con_b"}},
		{"both host flags, same id", []string{"--host-contact-id", "con_a", "--host-contact-ids", "con_a"}},
		// Over MAX_HOSTS_PER_EVENT (3).
		{"over the cap", []string{"--host-contact-ids", "con_a,con_b,con_c,con_d"}},
		{"over the cap, repeated form", []string{
			"--host-contact-ids", "con_a", "--host-contact-ids", "con_b",
			"--host-contact-ids", "con_c", "--host-contact-ids", "con_d",
		}},
		// Duplicates are rejected, never silently de-duplicated.
		{"duplicate id", []string{"--host-contact-ids", "con_a,con_a"}},
		{"duplicate id, repeated form", []string{"--host-contact-ids", "con_a", "--host-contact-ids", "con_a"}},
		// Blank entries: dropping one would ship a SHORTER host set than named.
		{"empty id between two real ones", []string{"--host-contact-ids", "con_a,,con_b"}},
		{"whitespace-only id", []string{"--host-contact-ids", "con_a, ,con_b"}},
		{"empty flag value", []string{"--host-contact-ids", ""}},
		// The singular flag's own blank case: an empty id resolves to no hub
		// member, so the API fails closed on it exactly as it does on a blank
		// list entry.
		{"empty singular id", []string{"--host-contact-id", ""}},
		{"whitespace-only singular id", []string{"--host-contact-id", "  "}},
	}

	for _, verb := range []struct {
		name string
		args func(extra ...string) []string
	}{
		{"create", eventsCreateArgs},
		{"update", eventsUpdateArgs},
	} {
		for _, tc := range cases {
			t.Run(verb.name+"/"+tc.name, func(t *testing.T) {
				srv, got := newHostRecordingServer(t, http.StatusOK, hubEventBody)

				res := runContract(t, eventsEnv(srv.URL), verb.args(tc.extra...)...)

				if res.Code != errs.ExitUsage {
					t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
				}
				if len(*got) != 0 {
					t.Errorf("server saw %d request(s), want 0 — a knowingly-invalid host set "+
						"must never reach the API: %+v", len(*got), *got)
				}
			})
		}
	}
}

// TestEventsHostFlags_ErrorsNameWhatIsWrong pins the MESSAGE, not just the exit
// code: the SilenceErrors root never writes it into the stderr buffer (main.go
// renders it), so this drives the tree through executeCLI and reads the raw
// error. An exit-code-only guard would accept "unknown flag" or a bare
// "invalid input" for every one of these.
func TestEventsHostFlags_ErrorsNameWhatIsWrong(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		want  []string
	}{
		{
			"both host flags",
			[]string{"--host-contact-id", "con_a", "--host-contact-ids", "con_b"},
			[]string{"host-contact-id", "host-contact-ids"},
		},
		{
			"over the cap",
			[]string{"--host-contact-ids", "con_a,con_b,con_c,con_d"},
			[]string{"--host-contact-ids", "at most 3", "got 4"},
		},
		{
			"duplicate id",
			[]string{"--host-contact-ids", "con_a,con_a"},
			[]string{"--host-contact-ids", "duplicate", "con_a"},
		},
		{
			"empty id",
			[]string{"--host-contact-ids", "con_a,,con_b"},
			[]string{"--host-contact-ids", "empty value"},
		},
		{
			"empty flag value",
			[]string{"--host-contact-ids", ""},
			[]string{"--host-contact-ids", "at least one host"},
		},
		{
			"empty singular id",
			[]string{"--host-contact-id", ""},
			[]string{"--host-contact-id", "empty value"},
		},
	}

	srv, got := newHostRecordingServer(t, http.StatusOK, hubEventBody)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := executeCLI(t, eventsEnv(srv.URL), eventsCreateArgs(tc.extra...)...)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if code := codeForExecuteErr(err); code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage)", code, errs.ExitUsage)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error message %q does not mention %q", err.Error(), want)
				}
			}
		})
	}

	if len(*got) != 0 {
		t.Errorf("server saw %d request(s), want 0: %+v", len(*got), *got)
	}
}

// TestEventsHostFlags_AtTheCapIsAccepted is the accept-side counterpart to the
// over-the-cap reject above. Without it, an off-by-one cap (">= 3" instead of
// "> 3") would still pass every rejection case in this file — a reject-only
// probe cannot tell the two implementations apart.
func TestEventsHostFlags_AtTheCapIsAccepted(t *testing.T) {
	srv, got := newHostRecordingServer(t, http.StatusCreated, hubEventBody)

	res := runContract(t, eventsEnv(srv.URL), eventsCreateArgs("--host-contact-ids", "con_a,con_b,con_c")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) — exactly MAX_HOSTS_PER_EVENT hosts is legal; stderr=%q",
			res.Code, errs.ExitOK, res.Stderr)
	}
	if len(*got) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1: %+v", len(*got), *got)
	}
	wantHostContactIDs(t, eventAttributesFrom(t, (*got)[0].Body), []string{"con_a", "con_b", "con_c"})
}

// TestEventsHostFlags_RejectedBeforeScopeResolution proves WHERE the check
// runs, not just that it runs. The other rejection tests pass --hub hub_123,
// an ID-SHAPED value that ResolveHub returns unchanged with no API call — so
// they would stay green even if the validation happened after eventsContext.
// Here --hub is a NAME, which eventsContext can only resolve by listing the
// team's hubs over HTTP. Zero requests is therefore the only outcome
// consistent with "a knowingly-invalid host set fires no request at all"; a
// validation placed after eventsContext shows up as exactly one GET.
func TestEventsHostFlags_RejectedBeforeScopeResolution(t *testing.T) {
	verbs := map[string][]string{
		"create": {
			"--hub", "marketing-hub",
			"events", "create",
			"--title", "Community Meetup",
			"--starts-at", "2026-09-01T18:00:00Z",
			"--ends-at", "2026-09-01T20:00:00Z",
			"--timezone", "America/New_York",
			"--location-type", "url",
			"--host-contact-ids", "con_a,con_a",
		},
		"update": {
			"--hub", "marketing-hub",
			"events", "update", "evt_1",
			"--host-contact-ids", "con_a,con_a",
		},
	}

	for name, args := range verbs {
		t.Run(name, func(t *testing.T) {
			srv, got := newHostRecordingServer(t, http.StatusOK, hubEventBody)

			err := executeCLI(t, eventsEnv(srv.URL), withTeam("t_team1", args...)...)
			if err == nil {
				t.Fatal("expected a usage error for the duplicate host id, got nil")
			}
			if code := codeForExecuteErr(err); code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); err=%v", code, errs.ExitUsage, err)
			}
			// The error must be the HOST one, not a hub-resolution failure —
			// otherwise "0 requests" could be true for the wrong reason.
			if !strings.Contains(err.Error(), "--host-contact-ids") {
				t.Errorf("error %q does not name --host-contact-ids; the host check must fire "+
					"before hub resolution, not after it", err.Error())
			}
			if len(*got) != 0 {
				t.Errorf("server saw %d request(s), want 0 — the host check must run BEFORE "+
					"eventsContext resolves a name-shaped --hub over HTTP: %+v", len(*got), *got)
			}
		})
	}
}
