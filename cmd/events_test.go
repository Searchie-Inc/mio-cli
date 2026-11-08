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

func eventsEnv(apiBase string) []string {
	return append(baseEnv(apiBase), "MIO_CONTACT_TOKEN=test-contact-token")
}

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

func TestEventsCreate_MissingRequiredFlags(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1",
			"--hub", "hub_123",
			"events", "create",
			"--title", "Community Meetup",
			"--starts-at", "2026-09-01T18:00:00Z",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

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

func TestEventsUpdate_NothingToUpdate(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "update", "evt_1")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

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

func TestEventsCancel_RequiresYes(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "cancel", "evt_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

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

func TestEventsRSVPSet_MissingStatus(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "rsvp", "set", "evt_1")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestEventsRSVPSet_InvalidStatus(t *testing.T) {
	srv := newMockServer(t, nil)

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

func TestEventsRSVPSet_Maybe(t *testing.T) {
	var gotBody []byte
	var gotMethod, gotPath string

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

func TestEventsRSVPWithdraw_RequiresYes(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "rsvp", "withdraw", "evt_1")...)

	if res.Code != errs.ExitNeedsConfir {
		t.Errorf("exit code = %d, want %d (ExitNeedsConfir); stderr=%q", res.Code, errs.ExitNeedsConfir, res.Stderr)
	}
}

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
		w.WriteHeader(http.StatusOK)
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
	if !strings.Contains(res.Stdout, "not_going") {
		t.Errorf("stdout does not contain the rendered RSVP body (status=not_going); stdout=%q", res.Stdout)
	}
}

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

func TestEventsList_InvalidStatus(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "list", "--status", "live")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestEventsList_InvalidSort(t *testing.T) {
	srv := newMockServer(t, nil)

	res := runContract(t, eventsEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "list", "--sort", "title")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

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
			gotHubListAuth = r.Header.Get("Authorization")
			gotHubListPath = r.URL.Path
			_, _ = w.Write([]byte(hubListBody))
			return
		}
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

func TestEventsContext_NoContactToken_FailsFast(t *testing.T) {
	t.Setenv(eventsContactTokenEnv, "")

	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "events", "list")...)

	if res.Code != errs.ExitAuth {
		t.Errorf("exit code = %d, want %d (ExitAuth); stderr=%q", res.Code, errs.ExitAuth, res.Stderr)
	}
}

func TestEventsContext_NoContactToken_HonestErrorMessage(t *testing.T) {
	bin := buildBinary(t)

	_, stderr, exitCode := runBinary(t, bin, []string{
		"MIO_API_KEY=test-key",
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

func TestEventsContext_AnonymousBypassesGate(t *testing.T) {
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

	res := runContract(t, baseEnv(srv.URL),
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

	res := runContract(t, eventsEnv(srv.URL),
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

const (
	eventsCreateWirePath = "/api/v1/hubs/hub_123/events"
	eventsUpdateWirePath = "/api/v1/hubs/hub_123/events/evt_1"
)

type recordedEventRequest struct {
	Method string
	Path   string
	Body   string
}

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

func eventsUpdateArgs(extra ...string) []string {
	base := []string{"--hub", "hub_123", "events", "update", "evt_1"}
	return withTeam("t_team1", append(base, extra...)...)
}

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
	if len(attrs) != 1 {
		t.Errorf("attributes = %v, want exactly 1 key (host_contact_ids)", attrs)
	}
}

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

func TestEventsHostFlags_RejectedBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
	}{
		{"both host flags", []string{"--host-contact-id", "con_a", "--host-contact-ids", "con_b"}},
		{"both host flags, same id", []string{"--host-contact-id", "con_a", "--host-contact-ids", "con_a"}},
		{"over the cap", []string{"--host-contact-ids", "con_a,con_b,con_c,con_d"}},
		{"over the cap, repeated form", []string{
			"--host-contact-ids", "con_a", "--host-contact-ids", "con_b",
			"--host-contact-ids", "con_c", "--host-contact-ids", "con_d",
		}},
		{"duplicate id", []string{"--host-contact-ids", "con_a,con_a"}},
		{"duplicate id, repeated form", []string{"--host-contact-ids", "con_a", "--host-contact-ids", "con_a"}},
		{"empty id between two real ones", []string{"--host-contact-ids", "con_a,,con_b"}},
		{"whitespace-only id", []string{"--host-contact-ids", "con_a, ,con_b"}},
		{"empty flag value", []string{"--host-contact-ids", ""}},
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
