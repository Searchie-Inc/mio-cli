package cmd

// hubs_authoring_test.go — contract tests for the hubs authoring batch:
//   MIO-2516  hubs update --registration-enabled + retrieve surfaces the state
//   MIO-2517  hubs update --unset <dotted.path> (deletable blob keys)
//   MIO-2521  hubs create echoes private/published state (derived + stderr hint)
//   MIO-2522  hubs create/update --favicon-url (branding.favicon_url)
//
// Reuses the create/update test harness in hubs_create_test.go and
// hubs_update_blobs_test.go (captureHubRequest, decodeHubAttrs, rmwServer,
// hubWithBlobsBody) plus the runContract/withTeam/baseEnv driver.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// hubRespServer replies to every request with the given JSON:API body and
// captures the body of the first POST/PATCH it sees. It serves both the RMW GET
// (retrieve) and the following PATCH in update flows, and the POST in create
// flows, so a single fixture drives read-modify-write assertions.
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

// ─── MIO-2522: --favicon-url ─────────────────────────────────────────────────

// TestHubsCreate_FaviconURLNested verifies --favicon-url maps to
// attributes.branding.favicon_url and never appears at the top level.
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

// TestHubsCreate_FaviconAndLogoCompose verifies --favicon-url and --logo-url
// both land in the same branding object (neither clobbers the other).
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

// TestHubsUpdate_FaviconURLMergesRMW verifies --favicon-url merges into the
// hub's current branding (read-modify-write), keeping sibling branding keys.
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

// ─── MIO-2516: --registration-enabled + retrieve surface ─────────────────────

// TestHubsUpdate_RegistrationEnabledTrue verifies --registration-enabled sets
// settings.registration.enabled=true via RMW, preserving sibling settings keys.
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
	// Sibling settings.* key must survive (no clobber).
	header, ok := s["header"].(map[string]any)
	if !ok {
		t.Fatalf("settings.header must be preserved; settings=%v", s)
	}
	if header["menuLayout"] != "tabs" {
		t.Errorf("settings.header.menuLayout = %v, want preserved 'tabs'", header["menuLayout"])
	}
}

// TestHubsUpdate_RegistrationEnabledFalse verifies --registration-enabled=false
// sends settings.registration.enabled=false (distinguished from unset).
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

// TestHubsUpdate_RegistrationEnabledPreservesSiblingRegistrationKeys verifies
// that setting enabled does NOT clobber other settings.registration.* keys.
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

// TestHubsRetrieve_SurfacesRegistrationEnabledTrue verifies retrieve injects a
// derived registration_enabled=true when settings.registration.enabled is true.
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

// TestHubsRetrieve_SurfacesRegistrationEnabledFailClosed verifies the derived
// registration_enabled is false (fail-closed) when the key is absent.
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

// ─── MIO-2517: --unset ───────────────────────────────────────────────────────

// TestHubsUpdate_UnsetRemovesNestedSettingsKey verifies --unset removes a nested
// leaf, keeps the nested sibling, and touches no other blob.
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
	// Untouched blobs must not be sent at all (partial update).
	if _, has := attrs["branding"]; has {
		t.Errorf("branding must be absent (untouched blob); attrs=%v", attrs)
	}
	if _, has := attrs["meta"]; has {
		t.Errorf("meta must be absent (untouched blob); attrs=%v", attrs)
	}
}

// TestHubsUpdate_UnsetRemovesTopLevelBrandingKey verifies --unset removes a
// top-level blob key while keeping siblings.
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

// TestHubsUpdate_UnsetCommaListMultipleBlobs verifies a single comma-separated
// --unset removes keys across multiple blobs.
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

// TestHubsUpdate_UnsetWinsOverMergeSameCommand verifies the documented order:
// an --unset in the same command wins over a --settings-json merge.
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

// TestHubsUpdate_UnsetUnknownBlobNoRequest verifies an unknown blob prefix exits
// ExitUsage before any HTTP request.
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

// TestHubsUpdate_UnsetBareBlobNoRequest verifies a bare blob name (no key) is a
// usage error firing no request.
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

// TestHubsUpdate_UnsetEmptyNoRequest verifies a blank --unset value is a usage
// error firing no request.
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

// TestHubsUpdate_UnsetTrailingCommaNoRequest verifies a blank entry INSIDE a
// comma-list (e.g. a stray trailing comma "settings.header.color,") is a usage
// error firing no request — the blank must not be silently dropped so the valid
// part proceeds.
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

// TestHubsUpdate_RMWRequestSequence asserts the read-modify-write path issues
// exactly one GET (retrieve) then one PATCH (update) to the SAME hub resource —
// catching a regression that adds an extra GET or targets the wrong PATCH path.
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

// ─── MIO-2521: create discoverability (state + hint) ─────────────────────────

// TestHubsCreate_SurfacesPublishedDerivedPrivate verifies create injects a
// derived published=false when the created hub is private.
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

// TestHubsCreate_NoHintInJSONMode verifies the discoverability hint does NOT fire
// in json mode (the test harness default is non-TTY → json), keeping stdout pure
// and stderr free of the hint.
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
	// stdout must remain valid JSON (not corrupted by any hint).
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout=%q", err, res.Stdout)
	}
}

// TestHubsCreate_HintOnStderrTableModePrivate verifies that in table (human)
// mode a private hub prints the publish hint on stderr, not stdout.
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
	// The hint must be on stderr only, never stdout.
	if strings.Contains(res.Stdout, "Publish it with") {
		t.Errorf("the publish hint must not appear on stdout; stdout=%q", res.Stdout)
	}
}

// ─── MIO-2991: `hubs list` must inject the same derived fields as retrieve ───
//
// list shipped without injectHubDerivedState while retrieve/create/update all
// had it, so a caller enumerating hubs read registration_enabled and published
// as null. Because registration_enabled is FAIL-CLOSED, an absent value is
// indistinguishable from a genuine false — the gap reads as "registration is off
// everywhere" rather than as an error. There is no server-side fallback: the API
// has no registration_enabled field at all.
//
// Asserts on a MULTI-ITEM response deliberately: a per-item loop that only
// touches Data[0], or one that ranges by value over Collection.Data (mutating a
// copy), both pass a single-item check.
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
	// Every item must carry BOTH derived fields — never absent, which is what a
	// consumer reads as null.
	for i, h := range out {
		if _, ok := h["registration_enabled"]; !ok {
			t.Errorf("hub %d has no registration_enabled — a caller reads null, which is indistinguishable from a genuine false", i)
		}
		if _, ok := h["published"]; !ok {
			t.Errorf("hub %d has no published", i)
		}
	}
	// And the values must be right, not merely present — the LAST item matters
	// most: it is the one a Data[0]-only fix would miss.
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

// --raw deliberately bypasses the derived fields on every path (documented at
// cmd/hubs.go). The list fix must not change that.
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
