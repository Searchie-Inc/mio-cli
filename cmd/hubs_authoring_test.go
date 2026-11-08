package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func hubRespServer(t *testing.T, body string) (*httptest.Server, *[]byte) {
	t.Helper()
	var writeBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			writeBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &writeBody
}

func TestHubsCreate_FaviconURLNested(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Branded Hub",
			"--favicon-url", "https://x/fav.ico",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	branding, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.branding is absent or not an object; attrs=%v", attrs)
	}
	if branding["favicon_url"] != "https://x/fav.ico" {
		t.Errorf("branding.favicon_url = %v, want \"https://x/fav.ico\"", branding["favicon_url"])
	}
	if _, hasTop := attrs["favicon_url"]; hasTop {
		t.Errorf("top-level attributes.favicon_url must NOT be present; attrs=%v", attrs)
	}
}

func TestHubsCreate_FaviconAndLogoCompose(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Branded Hub",
			"--logo-url", "https://x/l.png",
			"--favicon-url", "https://x/fav.ico",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *gotBody)
	branding, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("branding absent; attrs=%v", attrs)
	}
	if branding["logo_url"] != "https://x/l.png" {
		t.Errorf("branding.logo_url = %v, want the logo", branding["logo_url"])
	}
	if branding["favicon_url"] != "https://x/fav.ico" {
		t.Errorf("branding.favicon_url = %v, want the favicon", branding["favicon_url"])
	}
}

func TestHubsUpdate_FaviconURLMergesRMW(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--favicon-url", "https://new/fav.ico",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	b, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH branding absent or not an object; attrs=%v", attrs)
	}
	if b["favicon_url"] != "https://new/fav.ico" {
		t.Errorf("branding.favicon_url = %v, want the new value", b["favicon_url"])
	}
	if b["logo_url"] != "https://old/logo.png" {
		t.Errorf("branding.logo_url = %v, want the preserved existing value (RMW)", b["logo_url"])
	}
	if b["secondary"] != "#111111" {
		t.Errorf("branding.secondary = %v, want the preserved existing value (RMW)", b["secondary"])
	}
}

func TestHubsUpdate_RegistrationEnabledTrue(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--registration-enabled",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	s, ok := attrs["settings"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH settings absent or not an object; attrs=%v", attrs)
	}
	reg, ok := s["registration"].(map[string]any)
	if !ok {
		t.Fatalf("settings.registration absent or not an object; settings=%v", s)
	}
	if reg["enabled"] != true {
		t.Errorf("settings.registration.enabled = %v, want true", reg["enabled"])
	}
	header, ok := s["header"].(map[string]any)
	if !ok {
		t.Fatalf("settings.header must be preserved; settings=%v", s)
	}
	if header["menuLayout"] != "tabs" {
		t.Errorf("settings.header.menuLayout = %v, want preserved 'tabs'", header["menuLayout"])
	}
}

func TestHubsUpdate_RegistrationEnabledFalse(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--registration-enabled=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	s, _ := attrs["settings"].(map[string]any)
	reg, ok := s["registration"].(map[string]any)
	if !ok {
		t.Fatalf("settings.registration absent; settings=%v", s)
	}
	if reg["enabled"] != false {
		t.Errorf("settings.registration.enabled = %v, want false", reg["enabled"])
	}
}

func TestHubsUpdate_RegistrationEnabledPreservesSiblingRegistrationKeys(t *testing.T) {
	const body = `{
		"data": {
			"id": "hub_abc123",
			"type": "hubs",
			"attributes": {
				"title": "H", "slug": "h", "is_private": false,
				"settings": {"header": {"color": "#000"}, "registration": {"mode": "open", "enabled": false}}
			}
		}
	}`
	srv, patchBody := hubRespServer(t, body)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--registration-enabled",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	s, _ := attrs["settings"].(map[string]any)
	reg, ok := s["registration"].(map[string]any)
	if !ok {
		t.Fatalf("settings.registration absent; settings=%v", s)
	}
	if reg["enabled"] != true {
		t.Errorf("settings.registration.enabled = %v, want true", reg["enabled"])
	}
	if reg["mode"] != "open" {
		t.Errorf("settings.registration.mode = %v, want preserved 'open' (no sibling clobber)", reg["mode"])
	}
}

func TestHubsRetrieve_SurfacesRegistrationEnabledTrue(t *testing.T) {
	const body = `{
		"data": {
			"id": "hub_abc123",
			"type": "hubs",
			"attributes": {
				"title": "H", "slug": "h", "is_private": false,
				"settings": {"registration": {"enabled": true}}
			}
		}
	}`
	srv, _ := hubRespServer(t, body)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "retrieve", "hub_abc123")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout=%q", err, res.Stdout)
	}
	if out["registration_enabled"] != true {
		t.Errorf("registration_enabled = %v, want true; out=%v", out["registration_enabled"], out)
	}
}

func TestHubsRetrieve_SurfacesRegistrationEnabledFailClosed(t *testing.T) {
	const body = `{
		"data": {
			"id": "hub_abc123",
			"type": "hubs",
			"attributes": {"title": "H", "slug": "h", "is_private": true}
		}
	}`
	srv, _ := hubRespServer(t, body)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "retrieve", "hub_abc123")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout=%q", err, res.Stdout)
	}
	if out["registration_enabled"] != false {
		t.Errorf("registration_enabled = %v, want false (fail-closed)", out["registration_enabled"])
	}
	if out["published"] != false {
		t.Errorf("published = %v, want false (is_private=true)", out["published"])
	}
}

func TestHubsUpdate_UnsetRemovesNestedSettingsKey(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--unset", "settings.header.color",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	s, ok := attrs["settings"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH settings absent; attrs=%v", attrs)
	}
	header, ok := s["header"].(map[string]any)
	if !ok {
		t.Fatalf("settings.header absent; settings=%v", s)
	}
	if _, has := header["color"]; has {
		t.Errorf("settings.header.color must be removed; header=%v", header)
	}
	if header["menuLayout"] != "tabs" {
		t.Errorf("settings.header.menuLayout = %v, want preserved 'tabs'", header["menuLayout"])
	}
	if _, has := attrs["branding"]; has {
		t.Errorf("branding must be absent (untouched blob); attrs=%v", attrs)
	}
	if _, has := attrs["meta"]; has {
		t.Errorf("meta must be absent (untouched blob); attrs=%v", attrs)
	}
}

func TestHubsUpdate_UnsetRemovesTopLevelBrandingKey(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--unset", "branding.secondary",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	b, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH branding absent; attrs=%v", attrs)
	}
	if _, has := b["secondary"]; has {
		t.Errorf("branding.secondary must be removed; branding=%v", b)
	}
	if b["logo_url"] != "https://old/logo.png" {
		t.Errorf("branding.logo_url = %v, want preserved sibling", b["logo_url"])
	}
}

func TestHubsUpdate_UnsetCommaListMultipleBlobs(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--unset", "settings.header.color,branding.secondary",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	s, _ := attrs["settings"].(map[string]any)
	header, _ := s["header"].(map[string]any)
	if _, has := header["color"]; has {
		t.Errorf("settings.header.color must be removed; header=%v", header)
	}
	if header["menuLayout"] != "tabs" {
		t.Errorf("settings.header.menuLayout = %v, want preserved", header["menuLayout"])
	}
	b, _ := attrs["branding"].(map[string]any)
	if _, has := b["secondary"]; has {
		t.Errorf("branding.secondary must be removed; branding=%v", b)
	}
	if b["logo_url"] != "https://old/logo.png" {
		t.Errorf("branding.logo_url = %v, want preserved", b["logo_url"])
	}
}

func TestHubsUpdate_UnsetWinsOverMergeSameCommand(t *testing.T) {
	srv, patchBody := rmwServer(t)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--settings-json", `{"header":{"color":"#ffffff"}}`,
			"--unset", "settings.header.color",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	attrs := decodeHubAttrs(t, *patchBody)
	s, _ := attrs["settings"].(map[string]any)
	header, ok := s["header"].(map[string]any)
	if !ok {
		t.Fatalf("settings.header absent; settings=%v", s)
	}
	if _, has := header["color"]; has {
		t.Errorf("settings.header.color must be removed (unset wins over merge); header=%v", header)
	}
	if header["menuLayout"] != "tabs" {
		t.Errorf("settings.header.menuLayout = %v, want preserved nested sibling", header["menuLayout"])
	}
}

func TestHubsUpdate_UnsetUnknownBlobNoRequest(t *testing.T) {
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
			"--unset", "navigation.footer",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("--unset with an unknown blob must exit before any HTTP request")
	}
}

func TestHubsUpdate_UnsetBareBlobNoRequest(t *testing.T) {
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
			"--unset", "settings",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("--unset with a bare blob name must exit before any HTTP request")
	}
}

func TestHubsUpdate_UnsetEmptyNoRequest(t *testing.T) {
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
			"--unset", "  ",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("a blank --unset must exit before any HTTP request")
	}
}

func TestHubsUpdate_UnsetTrailingCommaNoRequest(t *testing.T) {
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
			"--unset", "settings.header.color,",
		)...)

	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
	if fired {
		t.Error("a blank comma-list entry must exit before any HTTP request")
	}
}

func TestHubsUpdate_RMWRequestSequence(t *testing.T) {
	var seq []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seq = append(seq, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hubWithBlobsBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--unset", "settings.header.color",
		)...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if len(seq) != 2 {
		t.Fatalf("request count = %d, want 2 (one GET retrieve + one PATCH update); seq=%v", len(seq), seq)
	}
	if !strings.HasPrefix(seq[0], "GET ") || !strings.HasPrefix(seq[1], "PATCH ") {
		t.Errorf("request sequence = %v, want [GET…, PATCH…]", seq)
	}
	getPath := strings.TrimPrefix(seq[0], "GET ")
	patchPath := strings.TrimPrefix(seq[1], "PATCH ")
	if getPath != patchPath {
		t.Errorf("GET path %q != PATCH path %q — RMW must target the same hub resource", getPath, patchPath)
	}
	if !strings.HasSuffix(patchPath, "/hubs/hub_abc123") {
		t.Errorf("PATCH path %q must target the hub resource …/hubs/hub_abc123", patchPath)
	}
}

func TestHubsCreate_SurfacesPublishedDerivedPrivate(t *testing.T) {
	const body = `{
		"data": {
			"id": "hub_new",
			"type": "hubs",
			"attributes": {"title": "H", "slug": "h", "is_private": true}
		}
	}`
	srv, _ := hubRespServer(t, body)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "create", "--name", "H")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout=%q", err, res.Stdout)
	}
	if out["published"] != false {
		t.Errorf("published = %v, want false (is_private=true)", out["published"])
	}
}

func TestHubsCreate_NoHintInJSONMode(t *testing.T) {
	srv, _, _, _ := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "hubs", "create", "--name", "H")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stderr, "Publish it with") || strings.Contains(res.Stderr, "Created hub") {
		t.Errorf("no create hint should appear in json mode; stderr=%q", res.Stderr)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout=%q", err, res.Stdout)
	}
}

func TestHubsCreate_HintOnStderrTableModePrivate(t *testing.T) {
	const body = `{
		"data": {
			"id": "hub_new",
			"type": "hubs",
			"attributes": {"title": "H", "slug": "my-slug", "is_private": true}
		}
	}`
	srv, _ := hubRespServer(t, body)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"--output", "table",
			"hubs", "create", "--name", "H",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "private") {
		t.Errorf("stderr should note the hub is private; stderr=%q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "--published") {
		t.Errorf("stderr should explain how to publish (--published); stderr=%q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "my-slug") {
		t.Errorf("stderr should surface the slug as the best-available reference; stderr=%q", res.Stderr)
	}
	if strings.Contains(res.Stdout, "Publish it with") {
		t.Errorf("the publish hint must not appear on stdout; stdout=%q", res.Stdout)
	}
}

func TestHubsList_SurfacesDerivedStateOnEveryItem(t *testing.T) {
	const body = `{
		"data": [
			{"id": "hub_1", "type": "hubs", "attributes": {
				"title": "A", "slug": "a", "is_private": false,
				"settings": {"registration": {"enabled": true}}}},
			{"id": "hub_2", "type": "hubs", "attributes": {
				"title": "B", "slug": "b", "is_private": true,
				"settings": {"registration": {"enabled": false}}}},
			{"id": "hub_3", "type": "hubs", "attributes": {
				"title": "C", "slug": "c", "is_private": false,
				"settings": {}}}
		]
	}`
	srv, _ := hubRespServer(t, body)

	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "hubs", "list")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("stdout is not a JSON list: %v; stdout=%q", err, res.Stdout)
	}
	if len(out) != 3 {
		t.Fatalf("rendered %d hubs, want 3", len(out))
	}
	for i, h := range out {
		if _, ok := h["registration_enabled"]; !ok {
			t.Errorf("hub %d has no registration_enabled — a caller reads null, which is indistinguishable from a genuine false", i)
		}
		if _, ok := h["published"]; !ok {
			t.Errorf("hub %d has no published", i)
		}
	}
	if out[0]["registration_enabled"] != true || out[0]["published"] != true {
		t.Errorf("hub_1: got registration_enabled=%v published=%v, want true/true", out[0]["registration_enabled"], out[0]["published"])
	}
	if out[1]["registration_enabled"] != false || out[1]["published"] != false {
		t.Errorf("hub_2 (private, registration off): got registration_enabled=%v published=%v, want false/false", out[1]["registration_enabled"], out[1]["published"])
	}
	if out[2]["registration_enabled"] != false {
		t.Errorf("hub_3 (no registration key) must fail closed to false, got %v", out[2]["registration_enabled"])
	}
}

func TestHubsList_RawBypassesDerivedState(t *testing.T) {
	const body = `{"data":[{"id":"hub_1","type":"hubs","attributes":{
		"title":"A","slug":"a","is_private":false,
		"settings":{"registration":{"enabled":true}}}}]}`
	srv, _ := hubRespServer(t, body)

	res := runContract(t, baseEnv(srv.URL), withTeam("t_team1", "hubs", "list", "--raw")...)
	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if strings.Contains(res.Stdout, "registration_enabled") {
		t.Errorf("--raw must render the untouched API envelope, with no derived fields; stdout=%q", res.Stdout)
	}
}
