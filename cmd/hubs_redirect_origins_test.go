package cmd

// hubs_redirect_origins_test.go — contract tests for
// `mio hubs redirect-origins get|set` (MIO-2269 / MIO-616).
//
// set is a full-replace PUT whose JSON:API type is "hub_redirect_origin_allowlists"
// (derived from the redirect-origins tail via a typeOverride) and whose
// attributes carry an origins ARRAY.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const redirectOriginsBody = `{"data":{"id":"hub_x:redirect-origins","type":"hub_redirect_origin_allowlists","attributes":{"origins":["https://app.example.com"]}}}`

// TestHubsRedirectOriginsGet pins the GET: method, team-scoped path suffix, exit 0.
func TestHubsRedirectOriginsGet(t *testing.T) {
	srv, gotMethod, gotPath, _, _ := captureAdminReq(t, http.StatusOK, redirectOriginsBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "redirect-origins", "get", "hub_x")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodGet {
		t.Errorf("HTTP method = %q, want GET", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/teams/t_team1/hubs/hub_x/redirect-origins") {
		t.Errorf("path %q does not end with /teams/t_team1/hubs/hub_x/redirect-origins", *gotPath)
	}
}

// TestHubsRedirectOriginsSet_Body pins the PUT: method, path, JSON:API type,
// and the parsed origins array.
func TestHubsRedirectOriginsSet_Body(t *testing.T) {
	srv, gotMethod, gotPath, _, gotBody := captureAdminReq(t, http.StatusOK, redirectOriginsBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "redirect-origins", "set", "hub_x",
			"--origins", "https://app.example.com, https://portal.example.com",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", *gotMethod)
	}
	if !strings.HasSuffix(*gotPath, "/teams/t_team1/hubs/hub_x/redirect-origins") {
		t.Errorf("path %q does not end with /redirect-origins", *gotPath)
	}

	typ, attrs := decodeDataTypeAttrs(t, *gotBody)
	if typ != "hub_redirect_origin_allowlists" {
		t.Errorf("data.type = %q, want hub_redirect_origin_allowlists", typ)
	}
	origins, ok := attrs["origins"].([]any)
	if !ok {
		t.Fatalf("data.attributes.origins is not an array; attrs=%v", attrs)
	}
	if len(origins) != 2 || origins[0] != "https://app.example.com" || origins[1] != "https://portal.example.com" {
		t.Errorf("origins = %v, want [https://app.example.com https://portal.example.com] (trimmed)", origins)
	}
}

// TestHubsRedirectOriginsSet_Clear pins that --clear sends origins: [] (present,
// empty array — not absent, not null).
func TestHubsRedirectOriginsSet_Clear(t *testing.T) {
	srv, gotMethod, _, _, gotBody := captureAdminReq(t, http.StatusOK, redirectOriginsBody)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "redirect-origins", "set", "hub_x", "--clear")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPut {
		t.Errorf("HTTP method = %q, want PUT", *gotMethod)
	}

	// Distinguish an empty array from absent/null.
	var doc struct {
		Data struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*gotBody, &doc); err != nil {
		t.Fatalf("request body not valid JSON: %v; body=%q", err, *gotBody)
	}
	originsRaw, present := doc.Data.Attributes["origins"]
	if !present {
		t.Fatalf("data.attributes.origins ABSENT; want empty array; body=%q", *gotBody)
	}
	if string(originsRaw) != "[]" {
		t.Errorf("data.attributes.origins = %s, want []", originsRaw)
	}
}

// TestHubsRedirectOriginsSet_Neither pins that neither --origins nor --clear
// exits ExitUsage without firing any request.
func TestHubsRedirectOriginsSet_Neither(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "redirect-origins", "set", "hub_x")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired when neither --origins nor --clear is given")
	}
}

// TestHubsRedirectOriginsSet_Both pins that --origins together with --clear
// exits ExitUsage without firing any request.
func TestHubsRedirectOriginsSet_Both(t *testing.T) {
	srv, fired := firedGuardServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "redirect-origins", "set", "hub_x",
			"--origins", "https://a.example.com", "--clear")...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if *fired {
		t.Error("no HTTP request must be fired when both --origins and --clear are given")
	}
}
