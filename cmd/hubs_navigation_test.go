package cmd

// hubs_navigation_test.go — contract tests for authoring the hub navigation
// menu via `mio hubs update --navigation-json` and the shared typed-item
// validation (MIO-2255). The mio-hub parser silently drops header/footer items
// that lack a "type", so the CLI rejects untyped items up front rather than
// letting a caller ship a menu that renders empty.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestHubsUpdate_NavigationJSONFlag verifies --navigation-json is sent as
// data.attributes.navigation on a PATCH, with the typed header items intact.
func TestHubsUpdate_NavigationJSONFlag(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/demo/","position":0}],"footer":[{"type":"url","label":"Privacy","href":"/demo/privacy","position":0}]}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPatch {
		t.Errorf("HTTP method = %q, want PATCH", *gotMethod)
	}
	if !strings.Contains(*gotPath, "hub_abc123") {
		t.Errorf("path %q does not contain hub_abc123", *gotPath)
	}

	attrs := decodeHubAttrs(t, *gotBody)
	nav, ok := attrs["navigation"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.navigation is absent or not an object; attrs=%v", attrs)
	}
	header, ok := nav["header"].([]any)
	if !ok || len(header) != 1 {
		t.Fatalf("navigation.header should be a 1-item array; got %#v", nav["header"])
	}
	if item, _ := header[0].(map[string]any); item["type"] != "url" || item["label"] != "Home" {
		t.Errorf("navigation.header[0] = %#v, want a url item labeled Home", header[0])
	}
}

// TestHubsUpdate_NavigationRejectsUntypedItem verifies a header/footer item
// missing "type" is rejected with ExitUsage and fires NO HTTP request.
func TestHubsUpdate_NavigationRejectsUntypedItem(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			// legacy untyped item {id,label,route} — dropped by the FE parser.
			"--navigation-json", `{"header":[{"id":"n1","label":"Home","route":"/"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an untyped navigation item must be rejected before any HTTP request")
	}
}

// TestHubsUpdate_NavigationBucketMustBeArray verifies a non-array header/footer
// bucket is rejected with ExitUsage and no request.
func TestHubsUpdate_NavigationBucketMustBeArray(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--navigation-json", `{"header":{"not":"an-array"}}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("a non-array navigation bucket must be rejected before any HTTP request")
	}
}

// TestHubsUpdate_NavigationRejectBeforeTeamResolve verifies an invalid menu is
// rejected with ExitUsage BEFORE team resolution — even when --team is a
// name/slug that would otherwise trigger a ResolveTeam GET /api/teams. Guards
// the validate-before-resolve ordering against regression (cf. MIO-2254).
func TestHubsUpdate_NavigationRejectBeforeTeamResolve(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		"hubs", "update", "hub_abc123",
		"--team", "acme-name", // NOT id-shaped → would trigger ResolveTeam GET
		"--navigation-json", `{"header":[{"label":"Home","route":"/"}]}`,
	)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an invalid --navigation-json must be rejected before any HTTP request, even with a team name that needs resolution")
	}
}

// TestHubsCreate_NavigationRejectsUntypedItem verifies the same typed-item
// validation applies on `hubs create` (consistency with update).
func TestHubsCreate_NavigationRejectsUntypedItem(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "X",
			"--navigation-json", `{"footer":[{"label":"Privacy","url":"/privacy"}]}`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("an untyped navigation item must be rejected before any HTTP request on create too")
	}
}
