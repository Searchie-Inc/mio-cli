package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const hubUpdateOKBody = `{"data":{"id":"hub_x","type":"hubs","attributes":{"name":"H","slug":"h"}}}`

func TestHubsUpdate_PoliciesInSettingsWarns(t *testing.T) {
	srv, _, _, _, gotBody := captureAdminReq(t, http.StatusOK, hubUpdateOKBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "update", "hub_x",
			"--settings-json", `{"policies":{"enabled":true,"show":true},"registration":{"enabled":true}}`)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (the write itself is valid); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "settings.policies cannot be written by") {
		t.Errorf("stderr must disclose the silent no-op; got: %q", res.Stderr)
	}
	for _, want := range []string{"hubs policies gate", "hubs policies update", "hubs policies get"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("warning must name %q as the alternative; got: %q", want, res.Stderr)
		}
	}
	if !strings.Contains(res.Stderr, "hubs create") {
		t.Errorf("warning must name the create-vs-update asymmetry; got: %q", res.Stderr)
	}
	if strings.Contains(res.Stdout, "cannot be written") {
		t.Errorf("the warning must not contaminate stdout; stdout=%q", res.Stdout)
	}
	body := string(*gotBody)
	if !strings.Contains(body, `"registration"`) {
		t.Errorf("the sibling settings key must still reach the wire — warning about policies must not drop the rest of the patch; body=%s", body)
	}
}

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
