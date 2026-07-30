package cmd

// hubs_policies_gate_test.go — contract tests for `mio hubs policies gate`
// (MIO-2269).
//
// gate: PATCH .../hubs/{hub}/policies/gate — enveloped, JSON:API type
//       "hub_policy_gate" (derived from the two-segment policies/gate tail),
//       attributes {enabled: bool}.
//
// (`policies get` is NOT YET implemented — MIO-2815. This comment used to say it
// was intentional, on the grounds that the only policies GET was the hub portal
// route, which requires member auth and rejects admin API keys. That is wrong:
// GET /api/v1/teams/{team_id}/hubs/{identifier}/policies exists
// (admin_get_hub_policies, MIO-2394) and takes the SAME owner credentials as the
// gate verb below, reporting both documents with the ACTUAL stored gate state.
// It is the only way to see whether a `hubs scaffold` resume reverted
// hand-edited legal text, so the missing verb is a gap, not a decision.)

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const policyGateBody = `{"data":{"id":"hub_x","type":"hub_policy_gate","attributes":{"enabled":true}}}`

// TestHubsPoliciesGate_Enabled pins the gate PATCH with --enabled: method, path,
// JSON:API type "hub_policy_gate", attributes.enabled == true.
func TestHubsPoliciesGate_Enabled(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureAdminReq(t, http.StatusOK, policyGateBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "gate", "hub_x", "--enabled")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/teams/t_team1/hubs/hub_x/policies/gate") {
		t.Errorf("path %q does not end with /policies/gate", *gotPath)
	}

	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_policy_gate" {
		t.Errorf("data.type = %q, want hub_policy_gate", typ)
	}
	if attrs["enabled"] != true {
		t.Errorf("data.attributes.enabled = %v, want true", attrs["enabled"])
	}
}

// TestHubsPoliciesGate_Disabled pins that --enabled=false sends enabled: false.
func TestHubsPoliciesGate_Disabled(t *testing.T) {
	srv, _, _, _, gotBody := captureAdminReq(t, http.StatusOK, policyGateBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "gate", "hub_x", "--enabled=false")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *gotBody)
	if attrs["enabled"] != false {
		t.Errorf("data.attributes.enabled = %v, want false", attrs["enabled"])
	}
}

// TestHubsPoliciesGate_MissingEnabled pins that omitting --enabled exits
// ExitUsage without firing any request (a bare bool defaults to false, so it
// must be given explicitly).
func TestHubsPoliciesGate_MissingEnabled(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "policies", "gate", "hub_x")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired when --enabled is missing")
	}
}
