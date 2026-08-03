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

// TestHubsPoliciesGet_AlwaysTwoDocuments pins what the backend ACTUALLY does for
// an unconfigured hub: it returns both documents with rendered default text, not
// an empty list.
//
// An earlier version of this test fed `{"data":[]}` and asserted an empty render,
// on the belief that the admin read short-circuits when policies are unconfigured.
// It does not — that early return lives in the PORTAL read (`get_policies`).
// `get_admin_policies` calls `_project_policy_documents` unconditionally, which
// loops `for policy_type in ("tos", "privacy_policy")` and always appends both;
// the backend's own test is named `test_absent_settings_returns_two_disabled_defaults`.
// So the old test pinned a state the server cannot produce, with a hand-built stub
// as its only oracle — "asserting an unreachable state" from
// .claude/rules/verifying-guards.md.
func TestHubsPoliciesGet_AlwaysTwoDocuments(t *testing.T) {
	// The unconfigured shape, as the backend really serves it: both documents,
	// default text, gate off, require_acceptance null ("never recorded").
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

// TestHubsPoliciesGet_VersionIsNotADiscriminator is the guard for the Critical
// this verb shipped with. The help used to tell operators that "default-v1" means
// the platform default and their text is safe to overwrite. The backend assigns a
// version ONLY for tos AND ONLY when require_acceptance is set
// (app/hubs/service.py: `if policy_type == "tos" and require_acceptance:`), and
// projects an absent version as "default-v1". So a hand-written privacy policy
// reads "default-v1" while holding custom text — and an operator following that
// advice would run a resume and destroy it.
//
// This pins the shape that proves it: custom content under a default version.
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
	// Both must survive the render: the version that looks default, and the
	// content that proves it is not. Dropping either would let an operator
	// conclude the wrong thing from the same call.
	if items[0]["version"] != "default-v1" {
		t.Errorf("version = %v, want default-v1", items[0]["version"])
	}
	if got, _ := items[0]["content"].(string); !strings.Contains(got, "HAND WRITTEN") {
		t.Errorf("content must be rendered — it is the ONLY reliable check before a resume; got %q", got)
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

	// Same, with NO --team either. Codex flagged that the assertion above leans on
	// an explicit --team and claimed production would resolve the team over the
	// network first. Measured: it does not — with no team configured the
	// resolution fails locally, so this still exits 2 with nothing on the wire,
	// exactly as `hubs retrieve` and `hubs policies gate` do. Pinned so the
	// ordering cannot regress into a request-before-usage-error.
	srv2, fired2 := firedGuardServer(t)
	res2 := runContract(t, baseEnv(srv2.URL), "hubs", "policies", "get", "")
	if res2.Code != errs.ExitUsage {
		t.Errorf("no --team, blank hub: exit = %d, want %d (ExitUsage); stderr=%q", res2.Code, errs.ExitUsage, res2.Stderr)
	}
	if *fired2 {
		t.Error("no --team, blank hub: no HTTP request must fire before the usage error")
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
