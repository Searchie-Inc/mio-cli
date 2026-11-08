package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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
	if branding["heading_font_size"] != float64(32) {
		t.Errorf("branding.heading_font_size = %#v, want numeric 32", branding["heading_font_size"])
	}
}

func TestHubsCreate_NavigationSettingsMetaJSONFlags(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Full",
			"--slug", "full",
			"--navigation-json", `{"header":[{"type":"url","label":"Home","href":"/full/","position":0}]}`,
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
		"--team", "acme-name",
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

func TestHubsCreate_BlobJSONFromFile(t *testing.T) {
	dir := t.TempDir()
	fp := dir + "/nav.json"
	if err := os.WriteFile(fp, []byte(`{"header":[{"type":"url","label":"Home","href":"/f/","position":0}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "F",
			"--slug", "f",
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
