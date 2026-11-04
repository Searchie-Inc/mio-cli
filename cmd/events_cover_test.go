package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestEventsCoverAndCancelWire(t *testing.T) {
	create := []string{"events", "create", "--title", "Event", "--starts-at", "start", "--ends-at", "end", "--timezone", "UTC", "--location-type", "url"}
	cases := []struct {
		name              string
		args              []string
		code              int
		method, path, typ string
		attrs             map[string]any
		noBody            bool
	}{
		{"create media", append(append([]string{}, create...), "--cover-media-id", "media_1"), 0, "POST", "/api/v1/hubs/hub_123/events", "hub_events", map[string]any{"cover_media_id": "media_1"}, false},
		{"create empty cover url", append(append([]string{}, create...), "--cover-image-url", ""), 0, "POST", "/api/v1/hubs/hub_123/events", "hub_events", map[string]any{"cover_image_url": ""}, false},
		{"create blank media", append(append([]string{}, create...), "--cover-media-id", "  "), errs.ExitUsage, "", "", "", nil, false},
		{"update media", []string{"events", "update", "evt_1", "--cover-media-id", "media_1"}, 0, "PATCH", "/api/v1/hubs/hub_123/events/evt_1", "hub_events", map[string]any{"cover_media_id": "media_1"}, false},
		{"update blank media", []string{"events", "update", "evt_1", "--cover-media-id", ""}, errs.ExitUsage, "", "", "", nil, false},
		{"clear media", []string{"events", "update", "evt_1", "--clear-cover-media", "--yes"}, 0, "PATCH", "/api/v1/hubs/hub_123/events/evt_1", "hub_events", map[string]any{"cover_media_id": nil}, false},
		{"clear requires yes", []string{"events", "update", "evt_1", "--clear-cover-media"}, errs.ExitNeedsConfir, "", "", "", nil, false},
		{"clear conflicts", []string{"events", "update", "evt_1", "--clear-cover-media", "--cover-media-id", "media_1", "--yes"}, errs.ExitUsage, "", "", "", nil, false},
		{"empty cover url", []string{"events", "update", "evt_1", "--cover-image-url", ""}, 0, "PATCH", "/api/v1/hubs/hub_123/events/evt_1", "hub_events", map[string]any{"cover_image_url": ""}, false},
		{"cancel message", []string{"events", "cancel", "evt_1", "--yes", "--message", "  Rescheduled!  "}, 0, "POST", "/api/v1/hubs/hub_123/events/evt_1/cancel", "hub_event_cancellations", map[string]any{"message": "  Rescheduled!  "}, false},
		{"cancel silent", []string{"events", "cancel", "evt_1", "--yes"}, 0, "POST", "/api/v1/hubs/hub_123/events/evt_1/cancel", "", nil, true},
		{"cancel blank", []string{"events", "cancel", "evt_1", "--yes", "--message", "  "}, errs.ExitUsage, "", "", "", nil, false},
		{"cancel requires yes", []string{"events", "cancel", "evt_1", "--message", "Notice"}, errs.ExitNeedsConfir, "", "", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var count int
			var method, path string
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				method = r.Method
				path = r.URL.Path
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/vnd.api+json")
				_, _ = w.Write([]byte(hubEventBody))
			}))
			t.Cleanup(srv.Close)
			args := withTeam("t_team1", append([]string{"--hub", "hub_123"}, tc.args...)...)
			res, err := runResume(t, eventsEnv(srv.URL), args...)
			if res.Code != tc.code {
				t.Fatalf("exit=%d want %d; error=%v", res.Code, tc.code, err)
			}
			if tc.code != 0 {
				if count != 0 {
					t.Errorf("requests=%d want 0", count)
				}
				return
			}
			if count != 1 || method != tc.method || path != tc.path {
				t.Fatalf("requests=%d method=%q path=%q; want 1 %s %s", count, method, path, tc.method, tc.path)
			}
			if tc.noBody {
				if len(body) != 0 {
					t.Errorf("body=%q want no body", body)
				}
				return
			}
			var doc struct {
				Data struct {
					Type       string         `json:"type"`
					Attributes map[string]any `json:"attributes"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &doc); err != nil {
				t.Fatalf("decode body %q: %v", body, err)
			}
			if doc.Data.Type != tc.typ {
				t.Errorf("type=%q want %q", doc.Data.Type, tc.typ)
			}
			if tc.method == "POST" && tc.path == "/api/v1/hubs/hub_123/events" {
				for key, want := range tc.attrs {
					got, ok := doc.Data.Attributes[key]
					if !ok || got != want {
						t.Errorf("attributes.%s=%v (present=%v), want %v", key, got, ok, want)
					}
				}
				return
			}
			if !reflect.DeepEqual(doc.Data.Attributes, tc.attrs) {
				t.Errorf("attributes=%#v want %#v", doc.Data.Attributes, tc.attrs)
			}
		})
	}
}
