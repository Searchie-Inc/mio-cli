package cmd

// hubs_policies_update_noop_test.go — MIO-2811.
//
// `settings.policies` is accepted at hub CREATE and popped on UPDATE
// (app/hubs/service.py: `incoming.pop("policies", None)  # client can never
// write policies here`). The CLI allowlists `policies` as a settings key — which
// is correct, it IS one — so validateBlobKeys will never flag it. The result was
// a documented flag that reports success and changes nothing.
//
// These pin the disclosure. Note what is NOT asserted: that the CLI re-routes
// the value. It deliberately does not — the conduit rule cuts against the CLI
// second-guessing the API, and only `enabled` has another endpoint anyway, so a
// re-route would fix one key and leave `show` just as inert.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const hubUpdateOKBody = `{"data":{"id":"hub_x","type":"hubs","attributes":{"name":"H","slug":"h"}}}`

// TestHubsUpdate_PoliciesInSettingsWarns — the update still succeeds (the rest of
// the blob is written), but stderr must say the policies key went nowhere.
func TestHubsUpdate_PoliciesInSettingsWarns(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x",
			"--settings-json", `{"policies":{"enabled":true,"show":true}}`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (the write itself is valid); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "settings.policies cannot be written by") {
		t.Errorf("stderr must disclose the silent no-op; got: %q", res.Stderr)
	}
	// The warning is only useful if it names the real doors.
	for _, want := range []string{"hubs policies gate", "hubs policies update", "hubs policies get"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("warning must name %q as the alternative; got: %q", want, res.Stderr)
		}
	}
	// It must also name the asymmetry — README documents the create form, so a
	// reader can reasonably assume update works the same way.
	if !strings.Contains(res.Stderr, "hubs create") {
		t.Errorf("warning must name the create-vs-update asymmetry; got: %q", res.Stderr)
	}
	// Warnings go to stderr so `-o json` on stdout stays parseable.
	if strings.Contains(res.Stdout, "cannot be written") {
		t.Errorf("the warning must not contaminate stdout; stdout=%q", res.Stdout)
	}
}

// TestHubsUpdate_PoliciesInSettingsStrictIsUsageError — under --strict-keys the
// same input is a usage error, matching how unknown keys escalate.
func TestHubsUpdate_PoliciesInSettingsStrictIsUsageError(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x", "--strict-keys",
			"--settings-json", `{"policies":{"enabled":true}}`)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage) under --strict-keys; stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must fire — the check runs before the retrieve/PATCH")
	}
}

// TestHubsUpdate_SettingsWithoutPoliciesIsSilent — the guard must not fire on
// every settings write. This is the case that must not regress: `policies` is a
// legitimate key and most updates do not carry it.
func TestHubsUpdate_SettingsWithoutPoliciesIsSilent(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x",
			"--settings-json", `{"registration":{"enabled":true}}`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "settings.policies") {
		t.Errorf("must not warn about policies when the caller did not send it; stderr=%q", res.Stderr)
	}
}

// TestHubsCreate_PoliciesInSettingsDoesNotWarn — create genuinely accepts
// policies.enabled/show, so warning there would be false. This is the assertion
// that stops the guard being applied to both paths indiscriminately.
func TestHubsCreate_PoliciesInSettingsDoesNotWarn(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "create", "--name", "H", "--slug", "h",
			"--settings-json", `{"policies":{"enabled":true,"show":true}}`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "cannot be written by") {
		t.Errorf("hubs create ACCEPTS settings.policies — warning there would be wrong; stderr=%q", res.Stderr)
	}
}

// --unset settings.policies.* is the SAME silent no-op by a different door: the
// backend restores the stored policies block wholesale on update
// (`merged["policies"] = current_settings["policies"]`), so deleting a key under
// it changes nothing. It is documented as "the only real delete", which makes an
// undisclosed no-op there more misleading than the flag's, not less.
func TestHubsUpdate_UnsetPoliciesWarns(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x", "--unset", "settings.policies.enabled")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "settings.policies cannot be written by") {
		t.Errorf("--unset settings.policies.* must disclose the no-op too; got: %q", res.Stderr)
	}
}

// The nested form and the bare blob path must both trip it.
func TestHubsUpdate_UnsetPoliciesBareBlobWarns(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x", "--unset", "settings.policies")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "settings.policies cannot be written by") {
		t.Errorf("bare --unset settings.policies must warn; got: %q", res.Stderr)
	}
}

// It must NOT fire for other unset paths — this is the over-correction to avoid.
func TestHubsUpdate_UnsetOtherKeyIsSilent(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x", "--unset", "settings.registration.enabled")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "settings.policies") {
		t.Errorf("must not warn for an unrelated --unset path; got: %q", res.Stderr)
	}
}

// A branding path that merely ends in "policies" must not trip the blob check.
func TestHubsUpdate_UnsetNonSettingsPoliciesIsSilent(t *testing.T) {
	srv, _, _, _, _ := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x", "--unset", "meta.policies")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "cannot be written by") {
		t.Errorf("meta.policies is a different blob and is writable; got: %q", res.Stderr)
	}
}
