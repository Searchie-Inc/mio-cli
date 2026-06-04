package cmd

// hubs_policies_test.go — contract tests for `mio hubs policies update`.
//
// Reuses the in-process harness from contract_test.go.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// resetPoliciesCmdFlags resets the Changed state and value of every flag on
// hubsPoliciesUpdateCmd back to its default. The global rootCmd is a singleton
// in the in-process harness; flag.Changed persists across Execute() calls
// unless explicitly cleared between tests.
func resetPoliciesCmdFlags(t *testing.T) {
	t.Helper()
	for _, name := range []string{"policy-type", "content", "reset-content", "require-acceptance"} {
		if fl := hubsPoliciesUpdateCmd.Flags().Lookup(name); fl != nil {
			fl.Changed = false
			_ = fl.Value.Set(fl.DefValue)
		}
	}
}

// hubsPoliciesBody is a minimal policies resource response.
const hubsPoliciesBody = `{
	"data": {
		"id": "pol_1",
		"type": "policies",
		"attributes": {
			"policy_type": "tos",
			"content": "# Terms",
			"require_acceptance": false
		}
	}
}`

// TestHubsPoliciesUpdate_PatchBody verifies that:
//   - the HTTP method is PATCH
//   - the path ends in /hubs/hub_x/policies
//   - the request body's data.type == "policies"
//   - data.attributes.policy_type == "tos"
//   - data.attributes.content is present and non-empty
//   - the command exits 0
func TestHubsPoliciesUpdate_PatchBody(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubsPoliciesBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "policies", "update", "hub_x",
			"--policy-type", "tos",
			"--content", "# Terms of Service",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_x/policies") {
		t.Errorf("path %q does not end with /hubs/hub_x/policies", gotPath)
	}

	// Decode the request body and assert envelope shape.
	var doc struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	if doc.Data.Type != "policies" {
		t.Errorf("data.type = %q, want \"policies\"", doc.Data.Type)
	}
	if doc.Data.Attributes["policy_type"] != "tos" {
		t.Errorf("data.attributes.policy_type = %v, want \"tos\"", doc.Data.Attributes["policy_type"])
	}
	if doc.Data.Attributes["content"] == nil || doc.Data.Attributes["content"] == "" {
		t.Errorf("data.attributes.content is missing or empty; attrs=%v", doc.Data.Attributes)
	}
}

// TestHubsPoliciesUpdate_InvalidPolicyType verifies that an unrecognised
// --policy-type value exits 2 (ExitUsage) without making an API call.
func TestHubsPoliciesUpdate_InvalidPolicyType(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	srv := newMockServer(t, nil) // request must not reach the server

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "policies", "update", "hub_x",
			"--policy-type", "bogus",
			"--content", "some content",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestHubsPoliciesUpdate_ResetContent verifies that --reset-content sends a
// PATCH with data.attributes.content == JSON null (present but null, not absent).
func TestHubsPoliciesUpdate_ResetContent(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubsPoliciesBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "policies", "update", "hub_x",
			"--policy-type", "tos",
			"--reset-content",
		)...)

	if res.Code != errs.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/hubs/hub_x/policies") {
		t.Errorf("path %q does not end with /hubs/hub_x/policies", gotPath)
	}

	// Decode body as a raw JSON document so we can distinguish null from absent.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &raw); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, gotBody)
	}
	dataRaw, ok := raw["data"]
	if !ok {
		t.Fatalf("request body missing top-level 'data' key; body=%q", gotBody)
	}
	var data struct {
		Type       string                     `json:"type"`
		Attributes map[string]json.RawMessage `json:"attributes"`
	}
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		t.Fatalf("data is not valid JSON: %v; data=%q", dataRaw, dataRaw)
	}

	// data.attributes.content must be present AND equal to JSON null.
	contentRaw, present := data.Attributes["content"]
	if !present {
		t.Errorf("data.attributes.content is ABSENT; want JSON null; attrs=%v", data.Attributes)
	} else if string(contentRaw) != "null" {
		t.Errorf("data.attributes.content = %s, want null", contentRaw)
	}
}

// TestHubsPoliciesUpdate_BothContentFlags verifies that providing both
// --content and --reset-content exits 2 (ExitUsage) without making an API call.
func TestHubsPoliciesUpdate_BothContentFlags(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	srv := newMockServer(t, nil) // request must not reach the server

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "policies", "update", "hub_x",
			"--policy-type", "tos",
			"--content", "# Terms",
			"--reset-content",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

// TestHubsPoliciesUpdate_NeitherContentFlag verifies that providing neither
// --content nor --reset-content exits 2 (ExitUsage) without making an API call.
func TestHubsPoliciesUpdate_NeitherContentFlag(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	srv := newMockServer(t, nil) // request must not reach the server

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "policies", "update", "hub_x",
			"--policy-type", "tos",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}
