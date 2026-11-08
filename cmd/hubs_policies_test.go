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

func resetPoliciesCmdFlags(t *testing.T) {
	t.Helper()
	for _, name := range []string{"policy-type", "content", "reset-content", "require-acceptance"} {
		if fl := hubsPoliciesUpdateCmd.Flags().Lookup(name); fl != nil {
			fl.Changed = false
			_ = fl.Value.Set(fl.DefValue)
		}
	}
}

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

func TestHubsPoliciesUpdate_InvalidPolicyType(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	srv := newMockServer(t, nil)

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

	contentRaw, present := data.Attributes["content"]
	if !present {
		t.Errorf("data.attributes.content is ABSENT; want JSON null; attrs=%v", data.Attributes)
	} else if string(contentRaw) != "null" {
		t.Errorf("data.attributes.content = %s, want null", contentRaw)
	}
}

func TestHubsPoliciesUpdate_BothContentFlags(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	srv := newMockServer(t, nil)

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

func TestHubsPoliciesUpdate_NeitherContentFlag(t *testing.T) {
	t.Cleanup(func() { resetPoliciesCmdFlags(t) })

	srv := newMockServer(t, nil)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "policies", "update", "hub_x",
			"--policy-type", "tos",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}
