package cmd

// hubs_blobs_test.go — contract tests for the presentation-blob flags on
// `mio hubs create`: --branding-json / --navigation-json / --settings-json /
// --meta-json (MIO-2254). These blobs are opaque JSONB on the hub; the CLI
// passes them through verbatim so an operator/agent can author a hub's
// branding, navigation, settings and feature-guard meta in one POST.
//
// Contract pinned here:
//   --branding-json '{...}'   → data.attributes.branding   = that object
//   --navigation-json '{...}' → data.attributes.navigation = that object
//   --settings-json '{...}'   → data.attributes.settings   = that object
//   --meta-json '{...}'       → data.attributes.meta        = that object
//   numeric values survive as numbers (not stringified)
//   --logo-url MERGES into --branding-json (does not clobber it)
//   malformed JSON or a non-object value → ExitUsage, no HTTP request fired

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestHubsCreate_BrandingJSONFlag verifies --branding-json is sent verbatim as
// data.attributes.branding, with numeric values preserved as numbers (the
// MIO-2235 regression: string font sizes were silently dropped by the FE).
func TestHubsCreate_BrandingJSONFlag(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Branded",
			"--branding-json", `{"primary":"#6747E3","heading_font_size":32}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)
	branding, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.branding is absent or not an object; attrs=%v", attrs)
	}
	if branding["primary"] != "#6747E3" {
		t.Errorf("branding.primary = %v, want \"#6747E3\"", branding["primary"])
	}
	// Numbers must round-trip as numbers, not strings.
	if branding["heading_font_size"] != float64(32) {
		t.Errorf("branding.heading_font_size = %#v, want numeric 32", branding["heading_font_size"])
	}
}

// TestHubsCreate_NavigationSettingsMetaJSONFlags verifies the other three blob
// flags each land at their top-level attribute as objects.
func TestHubsCreate_NavigationSettingsMetaJSONFlags(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Full",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/","position":0}]}`,
			"--settings-json", `{"policies":{"enabled":true}}`,
			"--meta-json", `{"discussions":{"enabled":true}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
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

	if s, ok := attrs["settings"].(map[string]any); !ok {
		t.Errorf("data.attributes.settings is absent or not an object; attrs=%v", attrs)
	} else if pol, _ := s["policies"].(map[string]any); pol["enabled"] != true {
		t.Errorf("settings.policies.enabled = %v, want true", pol["enabled"])
	}

	if m, ok := attrs["meta"].(map[string]any); !ok {
		t.Errorf("data.attributes.meta is absent or not an object; attrs=%v", attrs)
	} else if d, _ := m["discussions"].(map[string]any); d["enabled"] != true {
		t.Errorf("meta.discussions.enabled = %v, want true", d["enabled"])
	}
}

// TestHubsCreate_LogoURLMergesIntoBrandingJSON verifies that --logo-url and
// --branding-json compose: logo_url is merged INTO the branding object rather
// than replacing it (branding is a whole-blob field server-side, so the CLI
// must send one merged object).
func TestHubsCreate_LogoURLMergesIntoBrandingJSON(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Branded",
			"--branding-json", `{"primary":"#6747E3"}`,
			"--logo-url", "https://x/l.png",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)
	branding, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.branding is absent or not an object; attrs=%v", attrs)
	}
	if branding["primary"] != "#6747E3" {
		t.Errorf("branding.primary = %v, want \"#6747E3\" (--logo-url must not clobber --branding-json)", branding["primary"])
	}
	if branding["logo_url"] != "https://x/l.png" {
		t.Errorf("branding.logo_url = %v, want \"https://x/l.png\"", branding["logo_url"])
	}
}

// TestHubsCreate_InvalidBlobJSONErrorsFast verifies malformed JSON and
// non-object values (arrays/scalars) fail fast with ExitUsage and fire NO
// HTTP request.
func TestHubsCreate_InvalidBlobJSONErrorsFast(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
		val  string
	}{
		{"malformed", "--branding-json", `{not json`},
		{"array-not-object", "--navigation-json", `[1,2,3]`},
		{"scalar-not-object", "--settings-json", `"nope"`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
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
					tc.flag, tc.val,
				)...)

			if res.Code != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
			}
			if fired {
				t.Errorf("no HTTP request must be fired for invalid %s", tc.flag)
			}
		})
	}
}

// TestHubsCreate_InvalidBlobJSONNoResolveRequest verifies that a malformed blob
// flag fails fast with ExitUsage BEFORE any HTTP call — even when --team is a
// NAME/SLUG that would otherwise trigger a team-resolution GET /api/teams inside
// hubsContext. Regression for the codex-review finding: flag validation must run
// before auth/team resolution, else a bad --branding-json still fires a request.
func TestHubsCreate_InvalidBlobJSONNoResolveRequest(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		"hubs", "create",
		"--team", "acme-name", // NOT id-shaped → would trigger ResolveTeam GET /api/teams
		"--name", "X",
		"--branding-json", `{not json`,
	)

	if res.Code != errs.ExitUsage {
		t.Fatalf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("malformed --branding-json must exit before any HTTP request, even when --team is a name that needs resolution")
	}
}

// TestHubsCreate_BlobJSONFromFile verifies a blob flag reads its JSON from an
// @file path (large blobs like a full navigation tree live in a file).
func TestHubsCreate_BlobJSONFromFile(t *testing.T) {
	dir := t.TempDir()
	fp := dir + "/nav.json"
	if err := os.WriteFile(fp, []byte(`{"header":[{"type":"url","label":"Home","href":"/","position":0}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "F",
			"--navigation-json", "@"+fp,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)
	nav, ok := attrs["navigation"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.navigation is absent or not an object; attrs=%v", attrs)
	}
	if header, ok := nav["header"].([]any); !ok || len(header) != 1 {
		t.Errorf("navigation.header from @file should be a 1-item array; got %#v", nav["header"])
	}
}
