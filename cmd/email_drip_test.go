package cmd

// email_drip_test.go — contract tests for the new enrollment-mode flags on
// `mio email drip-campaigns create` and `mio email drip-campaigns update`.
//
// Reuses the in-process harness from contract_test.go.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// dripCampaignBody is a minimal drip-campaign resource response.
const dripCampaignBody = `{
	"data": {
		"id": "dc_1",
		"type": "drip_campaigns",
		"attributes": {
			"name": "Test Drip",
			"status": "draft",
			"enrollment_mode": "segment",
			"segment_id": "seg_1",
			"segment_check_interval_minutes": 30
		}
	}
}`

// TestEmailDripCreate_EnrollmentModeFlags verifies that when enrollment-mode
// flags are supplied, they appear in the PATCH body attributes with the correct
// snake_case keys and values.
//
//   - enrollment_mode == "segment"
//   - segment_id == "seg_1"
//   - segment_check_interval_minutes == 30 (integer)
//   - command exits 0
func TestEmailDripCreate_EnrollmentModeFlags(t *testing.T) {
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(dripCampaignBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		append(
			[]string{"--hub", "hub_123"},
			withTeam("t_team1",
				"email", "drip-campaigns", "create",
				"--name", "Test Drip",
				"--enrollment-mode", "segment",
				"--segment-id", "seg_1",
				"--segment-check-interval-minutes", "30",
			)...,
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
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

	if attrs["enrollment_mode"] != "segment" {
		t.Errorf("attributes.enrollment_mode = %v, want \"segment\"", attrs["enrollment_mode"])
	}
	if attrs["segment_id"] != "seg_1" {
		t.Errorf("attributes.segment_id = %v, want \"seg_1\"", attrs["segment_id"])
	}
	// JSON numbers decode as float64 in Go.
	if attrs["segment_check_interval_minutes"] != float64(30) {
		t.Errorf("attributes.segment_check_interval_minutes = %v (%T), want 30", attrs["segment_check_interval_minutes"], attrs["segment_check_interval_minutes"])
	}
}
