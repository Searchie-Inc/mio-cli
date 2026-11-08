package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const policiesListBody = `{"data":[` +
	`{"id":"hub_x:tos","type":"policies","attributes":{"policy_type":"tos","content":"Custom terms.","version":"v_9f3a","enabled":true,"require_acceptance":true}},` +
	`{"id":"hub_x:privacy_policy","type":"policies","attributes":{"policy_type":"privacy_policy","content":"Default text.","version":"default-v1","enabled":true,"require_acceptance":null}}` +
	`]}`

func TestHubsPoliciesGet_PositionalHub(t *testing.T) {
	srv, gotMethod, gotPath, gotQuery, gotBody := captureAdminReq(t, http.StatusOK, policiesListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "hub_x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %s, want GET — the path is shared with the PATCH, so the method is what makes this a read", *gotMethod)
	}
	if want := "/api/v1/teams/t_team1/hubs/hub_x/policies"; *gotPath != want {
		t.Errorf("path = %q, want %q", *gotPath, want)
	}
	if *gotQuery != "" {
		t.Errorf("query = %q, want empty — this read takes no parameters", *gotQuery)
	}
	if len(*gotBody) != 0 {
		t.Errorf("body = %q, want empty on a GET", string(*gotBody))
	}
}

func TestHubsPoliciesGet_AmbientHub(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureAdminReq(t, http.StatusOK, policiesListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "--hub", "hub_amb")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", *gotMethod)
	}
	if want := "/api/v1/teams/t_team1/hubs/hub_amb/policies"; *gotPath != want {
		t.Errorf("path = %q, want %q — --hub must reach the path", *gotPath, want)
	}
}

func TestHubsPoliciesGet_RendersListWithVersions(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, policiesListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "hub_x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	var items []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &items); err != nil {
		t.Fatalf("stdout is not a JSON list: %v\nstdout=%s", err, res.Stdout)
	}
	if len(items) != 2 {
		t.Fatalf("rendered %d policies, want 2", len(items))
	}

	byType := map[string]map[string]any{}
	for _, it := range items {
		pt, _ := it["policy_type"].(string)
		byType[pt] = it
	}
	tos, ok := byType["tos"]
	if !ok {
		t.Fatalf("no tos policy in output: %s", res.Stdout)
	}
	if tos["version"] != "v_9f3a" {
		t.Errorf("tos version = %v, want v_9f3a — version is what distinguishes a hand-written document from a reverted one (MIO-2818)", tos["version"])
	}
	if tos["enabled"] != true {
		t.Errorf("tos enabled = %v, want true — the gate must be readable, which is the whole point of MIO-2815", tos["enabled"])
	}
	if pp := byType["privacy_policy"]; pp == nil || pp["version"] != "default-v1" {
		t.Errorf("privacy_policy version = %v, want default-v1", pp["version"])
	}
}

func TestHubsPoliciesGet_JQCapturesGate(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, policiesListBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "hub_x", "-o", "plain", "--jq", ".[0].enabled")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if got := strings.TrimSpace(res.Stdout); got != "true" {
		t.Errorf("-o plain --jq '.[0].enabled' = %q, want \"true\" — this is the idiom the help text documents", got)
	}
}

func TestHubsPoliciesGet_AlwaysTwoDocuments(t *testing.T) {
	const unconfigured = `{"data":[` +
		`{"id":"hub_x:tos","type":"policies","attributes":{"policy_type":"tos","content":"# Terms of Service for Acme","version":"default-v1","enabled":false,"require_acceptance":null}},` +
		`{"id":"hub_x:privacy_policy","type":"policies","attributes":{"policy_type":"privacy_policy","content":"# Privacy Policy for Acme","version":"default-v1","enabled":false,"require_acceptance":null}}` +
		`]}`
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, unconfigured)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "hub_x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &items); err != nil {
		t.Fatalf("stdout is not a JSON list: %v\nstdout=%s", err, res.Stdout)
	}
	if len(items) != 2 {
		t.Fatalf("an unconfigured hub still returns BOTH documents, got %d", len(items))
	}
	for _, it := range items {
		if it["require_acceptance"] != nil {
			t.Errorf("unconfigured require_acceptance must render as null (\"never recorded\"), got %v", it["require_acceptance"])
		}
		if it["enabled"] != false {
			t.Errorf("unconfigured gate must render false, got %v", it["enabled"])
		}
	}
}

func TestHubsPoliciesGet_VersionIsNotADiscriminator(t *testing.T) {
	const customUnderDefaultVersion = `{"data":[` +
		`{"id":"hub_x:privacy_policy","type":"policies","attributes":{"policy_type":"privacy_policy","content":"OUR HAND WRITTEN PRIVACY POLICY","version":"default-v1","enabled":true,"require_acceptance":false}}` +
		`]}`
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, customUnderDefaultVersion)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "hub_x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &items); err != nil {
		t.Fatalf("stdout is not a JSON list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0]["version"] != "default-v1" {
		t.Errorf("version = %v, want default-v1", items[0]["version"])
	}
	if got, _ := items[0]["content"].(string); !strings.Contains(got, "HAND WRITTEN") {
		t.Errorf("content must be rendered — it is the ONLY reliable check before a resume; got %q", got)
	}
}

func TestHubsPoliciesGet_BlankHubIsUsageErrorAndFiresNoRequest(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage) for a blank hub id; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired for a blank hub id — it is a local usage error")
	}

	srv2, fired2 := firedGuardServer(t)
	res2 := runContract(t, baseEnv(srv2.URL), "hubs", "policies", "get", "")
	if res2.Code != errs.ExitUsage {
		t.Errorf("no --team, blank hub: exit = %d, want %d (ExitUsage); stderr=%q", res2.Code, errs.ExitUsage, res2.Stderr)
	}
	if *fired2 {
		t.Error("no --team, blank hub: no HTTP request must fire before the usage error")
	}
}

func TestHubsPoliciesGet_TooManyArgsIsUsageError(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "hub_x", "extra")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage) with two positionals; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired on an arity usage error")
	}
}
