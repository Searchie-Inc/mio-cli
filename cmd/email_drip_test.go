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
	"strings"
	"sync/atomic"
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

// dripStatusServer counts requests and records the last body.
func dripStatusServer(t *testing.T, hits *int32, body *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		*body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = w.Write([]byte(dripCampaignBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runContractErr is runContract but also returns the error Execute produced
// (the harness silences it, and main.go is what normally prints it).
func runContractErr(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()
	restore := overlayEnv(t, env)
	defer restore()
	resetGlobalFlags()
	root := RootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	defer root.SetArgs(nil)
	err := root.Execute()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return codeForExecuteErr(err), msg
}

func TestEmailDripUpdate_StatusFlagRefusedBeforeAnyRequest(t *testing.T) {
	var hits int32
	var body []byte
	srv := dripStatusServer(t, &hits, &body)
	code, msg := runContractErr(t, baseEnv(srv.URL), append([]string{"--hub", "hub_123"},
		withTeam("t_team1", "email", "drip-campaigns", "update", "dc_1", "--status", "active")...)...)
	if code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage; err=%q", code, msg)
	}
	if !strings.Contains(msg, "activate") {
		t.Errorf("error should point at activate; got %q", msg)
	}
	if hits != 0 {
		t.Errorf("server received %d requests, want 0", hits)
	}
}

func TestEmailDripCreate_StatusFlagRefusedBeforeAnyRequest(t *testing.T) {
	var hits int32
	var body []byte
	srv := dripStatusServer(t, &hits, &body)
	code, msg := runContractErr(t, baseEnv(srv.URL), append([]string{"--hub", "hub_123"},
		withTeam("t_team1", "email", "drip-campaigns", "create", "--name", "X", "--status", "draft")...)...)
	if code != errs.ExitUsage {
		t.Errorf("exit code = %d, want ExitUsage; err=%q", code, msg)
	}
	if !strings.Contains(msg, "activate") {
		t.Errorf("error should point at activate; got %q", msg)
	}
	if hits != 0 {
		t.Errorf("server received %d requests, want 0", hits)
	}
}

func TestEmailDripUpdate_NameOnlySendsNoStatus(t *testing.T) {
	var hits int32
	var body []byte
	srv := dripStatusServer(t, &hits, &body)
	res := runContract(t, baseEnv(srv.URL), append([]string{"--hub", "hub_123"},
		withTeam("t_team1", "email", "drip-campaigns", "update", "dc_1", "--name", "X")...)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d; stderr=%q", res.Code, res.Stderr)
	}
	if hits != 1 {
		t.Errorf("requests = %d, want 1", hits)
	}
	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("bad body %q: %v", body, err)
	}
	if doc.Data.Attributes["name"] != "X" {
		t.Errorf("name = %v, want X", doc.Data.Attributes["name"])
	}
	if _, ok := doc.Data.Attributes["status"]; ok {
		t.Errorf("body must not carry status: %v", doc.Data.Attributes)
	}
}

func TestEmailDripUpdate_HelpHidesStatus(t *testing.T) {
	res := runContract(t, baseEnv("http://127.0.0.1:1"), "email", "drip-campaigns", "update", "--help")
	if strings.Contains(res.Stdout+res.Stderr, "--status") {
		t.Errorf("help should not mention --status:\n%s%s", res.Stdout, res.Stderr)
	}
}
