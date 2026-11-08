package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const policyGateBody = `{"data":{"id":"hub_x","type":"hub_policy_gate","attributes":{"enabled":true}}}`

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
