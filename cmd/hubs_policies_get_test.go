package cmd

// hubs_policies_get_test.go — contract tests for `mio hubs policies get`
// (MIO-2815).
//
// get: GET /api/v1/teams/{team_id}/hubs/{hub}/policies — the team-owner ADMIN read
//      (admin_get_hub_policies, MIO-2394), which reports the ACTUAL stored gate
//      state. The member-portal read is not interchangeable: it serves defaults
//      and forces enabled=true for public display, so it cannot answer "is
//      enforcement really on?".
//
// The path is byte-identical to the policies PATCH — admin_router is prefixed
// /api/teams/{team_id}/hubs and the route is /{identifier}/policies — so the
// method is the ONLY thing distinguishing the read from the write. That makes
// pinning the method the load-bearing assertion here. (The client rewrites the
// helper's /api/... to /api/v1/..., internal/client/client.go, so the wire path
// asserted below carries the version segment the helper does not.)

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// Two documents, each repeating the single hub-level gate, with distinct
// versions: "default-v1" (reset to the platform default) vs a custom one. That
// asymmetry is the point of the verb — it is how an operator tells a
// hand-written ToS from one a scaffold resume reverted (MIO-2818).
const policiesListBody = `{"data":[` +
	`{"id":"hub_x:tos","type":"policies","attributes":{"policy_type":"tos","content":"Custom terms.","version":"v_9f3a","enabled":true,"require_acceptance":true}},` +
	`{"id":"hub_x:privacy_policy","type":"policies","attributes":{"policy_type":"privacy_policy","content":"Default text.","version":"default-v1","enabled":true,"require_acceptance":null}}` +
	`]}`

// TestHubsPoliciesGet_PositionalHub pins method + path + exit code.
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

// TestHubsPoliciesGet_AmbientHub covers the --hub form; the hub must reach the
// path exactly as the positional form does (MIO-2732 shared context helper).
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

// TestHubsPoliciesGet_RendersListWithVersions is the reason the verb exists: an
// operator must be able to read the gate and tell a custom document from a
// reverted one. Asserts the rendered payload is a LIST carrying version and
// enabled — not merely that a request fired.
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

// TestHubsPoliciesGet_JQCapturesGate pins the documented capture idiom. The
// response is a LIST, so `--jq .enabled` (as originally proposed) yields null —
// the help text says `.[0].enabled`, and that must actually work.
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

// TestHubsPoliciesGet_EmptyWhenUnconfigured pins the documented empty case. The
// backend returns an empty data array when policies are unconfigured, which must
// render as an empty list rather than an error — an agent has to be able to tell
// "nothing stored" from "disabled".
func TestHubsPoliciesGet_EmptyWhenUnconfigured(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, `{"data":[]}`)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "get", "hub_x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (an unconfigured hub is not an error); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &items); err != nil {
		t.Fatalf("stdout is not a JSON list: %v\nstdout=%s", err, res.Stdout)
	}
	if len(items) != 0 {
		t.Errorf("want an empty list for an unconfigured hub, got %d items", len(items))
	}
}

// TestHubsPoliciesGet_BlankHubIsUsageErrorAndFiresNoRequest — a blank positional
// hub id is a pure local usage error and must exit 2 before any HTTP request.
//
// Note this is the BLANK case, not the ABSENT one. With no hub supplied at all,
// requireHub legitimately hits the network: it auto-defaults to the team's only
// hub (singleHubDefault, cmd/root.go), so a hub-list request firing there is
// correct behaviour rather than a leak. Asserting "no request" on that path
// would pin the wrong property.
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
}

// TestHubsPoliciesGet_TooManyArgsIsUsageError — the verb takes at most one
// positional hub id.
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
