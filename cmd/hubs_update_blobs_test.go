package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const hubWithBlobsBody = `{
	"data": {
		"id": "hub_abc123",
		"type": "hubs",
		"attributes": {
			"title": "H", "slug": "h", "is_private": false,
			"branding": {"logo_url": "https://old/logo.png", "secondary": "#111111"},
			"settings": {"header": {"color": "#000000", "menuLayout": "tabs"}},
			"meta": {"discussions": {"enabled": true}, "moderation": {"enabled": false}}
		}
	}
}`

func rmwServer(t *testing.T) (*httptest.Server, *[]byte) {
	t.Helper()
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patchBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubWithBlobsBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &patchBody
}

func TestHubsUpdate_BrandingJSONDeepMerges(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--branding-json", `{"primary":"#6747E3"}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	b, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH branding is absent or not an object; attrs=%v", attrs)
	}
	if b["primary"] != "#6747E3" {
		t.Errorf("branding.primary = %v, want #6747E3", b["primary"])
	}
	if b["logo_url"] != "https://old/logo.png" {
		t.Errorf("branding.logo_url = %v, want the preserved existing value", b["logo_url"])
	}
	if b["secondary"] != "#111111" {
		t.Errorf("branding.secondary = %v, want the preserved existing value", b["secondary"])
	}
}

func TestHubsUpdate_LogoURLMergesRMW(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--logo-url", "https://new/logo.png",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK) — --logo-url should work on update now; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	b, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH branding is absent or not an object; attrs=%v", attrs)
	}
	if b["logo_url"] != "https://new/logo.png" {
		t.Errorf("branding.logo_url = %v, want the new value", b["logo_url"])
	}
	if b["secondary"] != "#111111" {
		t.Errorf("branding.secondary = %v, want the preserved existing value (RMW)", b["secondary"])
	}
}

func TestHubsUpdate_SettingsDeepMergeNested(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"header":{"color":"#ffffff"}}`,
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	s, _ := attrs["settings"].(map[string]any)
	header, ok := s["header"].(map[string]any)
	if !ok {
		t.Fatalf("settings.header is absent or not an object; settings=%v", s)
	}
	if header["color"] != "#ffffff" {
		t.Errorf("settings.header.color = %v, want #ffffff", header["color"])
	}
	if header["menuLayout"] != "tabs" {
		t.Errorf("settings.header.menuLayout = %v, want the preserved nested sibling 'tabs'", header["menuLayout"])
	}
}

func TestHubsUpdate_InvalidBrandingJSONNoRequest(t *testing.T) {
	fired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubWithBlobsBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--branding-json", `{not json`,
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("malformed --branding-json must exit before any HTTP request (no retrieve)")
	}
}
