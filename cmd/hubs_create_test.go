package cmd

// hubs_create_test.go — contract tests for `mio hubs create` and
// `mio hubs update` flag-to-attribute mapping (MIO-844).
//
// The QA-confirmed bug: --name was sent as attributes.name (rejected by the
// API's additionalProperties:false schema), and --published was sent as
// attributes.published (schema requires is_private with inverted polarity).
//
// These tests pin the CORRECT behaviour:
//   --name "X"          → data.attributes.title = "X"       (NOT name)
//   --published=true    → data.attributes.is_private = false (NOT published, INVERTED)
//   --published=false   → data.attributes.is_private = true
//   --name / --published must NOT appear in data.attributes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// minimalHubBody is a canned 201/200 hubs response used by all hubs create/update tests.
const minimalHubBody = `{
	"data": {
		"id": "hub_new",
		"type": "hubs",
		"attributes": {
			"title": "My Community",
			"slug": "my-community",
			"is_private": false
		}
	}
}`

// captureHubRequest starts a test server that records the first request's
// method, path, and raw body, then replies with minimalHubBody at the given
// HTTP status code.
func captureHubRequest(t *testing.T, status int) (*httptest.Server, *string, *string, *[]byte) {
	t.Helper()
	var gotMethod, gotPath string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotMethod, &gotPath, &gotBody
}

// decodeHubAttrs unmarshals a captured request body and returns the
// data.attributes map. It fails the test if the body is not valid JSON or
// missing data.attributes.
func decodeHubAttrs(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var doc struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, body)
	}
	if doc.Data.Attributes == nil {
		t.Fatalf("data.attributes is nil; body=%q", body)
	}
	return doc.Data.Attributes
}

// ─── hubs create ─────────────────────────────────────────────────────────────

// TestHubsCreate_NameMapsToTitle verifies that --name sends data.attributes.title,
// NOT data.attributes.name (which the API rejects with additionalProperties:false).
//
// CONTRACT (MIO-844): --name "X" → data.attributes.title = "X"; no "name" key.
func TestHubsCreate_NameMapsToTitle(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "My Community",
			"--slug", "my-community",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}
	if *gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %q, want POST", *gotMethod)
	}
	if !strings.Contains(*gotPath, "/hubs") {
		t.Errorf("path %q does not contain /hubs", *gotPath)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	// --name must arrive as attributes.title.
	if attrs["title"] != "My Community" {
		t.Errorf("data.attributes.title = %v, want \"My Community\"", attrs["title"])
	}
	// attributes.name must NOT be present (additionalProperties:false rejects it).
	if _, hasName := attrs["name"]; hasName {
		t.Errorf("data.attributes.name must NOT be present; got attrs=%v", attrs)
	}
}

// TestHubsCreate_PublishedTrueSendsIsPrivateFalse verifies that
// --published=true maps to data.attributes.is_private=false (inverted polarity).
//
// CONTRACT (MIO-844): --published=true → is_private=false; no "published" key.
func TestHubsCreate_PublishedTrueSendsIsPrivateFalse(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Public Hub",
			"--published=true",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	// --published=true → is_private=false
	if attrs["is_private"] != false {
		t.Errorf("data.attributes.is_private = %v, want false (published=true → is_private=false)", attrs["is_private"])
	}
	// attributes.published must NOT be present.
	if _, hasPub := attrs["published"]; hasPub {
		t.Errorf("data.attributes.published must NOT be present; got attrs=%v", attrs)
	}
}

// TestHubsCreate_PublishedFalseSendsIsPrivateTrue verifies the opposite
// inversion: --published=false → is_private=true.
//
// CONTRACT (MIO-844): --published=false → is_private=true; no "published" key.
func TestHubsCreate_PublishedFalseSendsIsPrivateTrue(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Private Hub",
			"--published=false",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	// --published=false → is_private=true
	if attrs["is_private"] != true {
		t.Errorf("data.attributes.is_private = %v, want true (published=false → is_private=true)", attrs["is_private"])
	}
	if _, hasPub := attrs["published"]; hasPub {
		t.Errorf("data.attributes.published must NOT be present; got attrs=%v", attrs)
	}
}

// TestHubsCreate_UnsetPublishedOmitsIsPrivate verifies that when --published is
// NOT passed at all, is_private is NOT sent in the request body (partial-update
// semantics: only changed flags are serialised).
//
// CONTRACT: no --published flag → is_private absent from request body.
func TestHubsCreate_UnsetPublishedOmitsIsPrivate(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Hub Without Published Flag",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if _, hasPriv := attrs["is_private"]; hasPriv {
		t.Errorf("is_private must be absent when --published is not passed; got attrs=%v", attrs)
	}
	if _, hasPub := attrs["published"]; hasPub {
		t.Errorf("published must be absent when --published is not passed; got attrs=%v", attrs)
	}
}

// ─── hubs update ─────────────────────────────────────────────────────────────

// TestHubsUpdate_NameMapsToTitle verifies the same name→title mapping for
// `mio hubs update`.
//
// CONTRACT (MIO-844): hubs update --name "X" → data.attributes.title = "X"; no "name" key.
func TestHubsUpdate_NameMapsToTitle(t *testing.T) {
	srv, gotMethod, gotPath, gotBody := captureHubRequest(t, http.StatusOK)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--name", "Renamed Community",
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

	if attrs["title"] != "Renamed Community" {
		t.Errorf("data.attributes.title = %v, want \"Renamed Community\"", attrs["title"])
	}
	if _, hasName := attrs["name"]; hasName {
		t.Errorf("data.attributes.name must NOT be present; got attrs=%v", attrs)
	}
}

// TestHubsUpdate_PublishedInversionBothDirections verifies the polarity
// inversion in `mio hubs update` for both true and false.
//
// CONTRACT (MIO-844): hubs update --published=true → is_private=false (and vice versa).
func TestHubsUpdate_PublishedInversionBothDirections(t *testing.T) {
	for _, tc := range []struct {
		flag        string
		wantPrivate bool
	}{
		{"--published=true", false},
		{"--published=false", true},
	} {
		tc := tc
		t.Run(tc.flag, func(t *testing.T) {
			srv, _, _, gotBody := captureHubRequest(t, http.StatusOK)

			res := runContract(t, baseEnv(srv.URL),
				withTeam("t_team1",
					"hubs", "update", "hub_abc123",
					tc.flag,
				)...)

			if res.Code != errs.ExitOK {
				t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
			}

			attrs := decodeHubAttrs(t, *gotBody)

			if attrs["is_private"] != tc.wantPrivate {
				t.Errorf("%s: is_private = %v, want %v", tc.flag, attrs["is_private"], tc.wantPrivate)
			}
			if _, hasPub := attrs["published"]; hasPub {
				t.Errorf("%s: attributes.published must NOT be present; got attrs=%v", tc.flag, attrs)
			}
		})
	}
}

// ─── logo-url nesting (MIO-844 third drift) ───────────────────────────────────

// TestHubsCreate_LogoURLNested verifies that --logo-url maps to
// data.attributes.branding.logo_url and that no top-level logo_url or
// logo-url key appears in attributes. The backend schema (additionalProperties:
// false) rejects a top-level logo_url with 422.
//
// CONTRACT (MIO-844): --logo-url X → attributes.branding.logo_url = X;
// no top-level attributes.logo_url or attributes.logo-url key.
func TestHubsCreate_LogoURLNested(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Branded Hub",
			"--logo-url", "https://x/l.png",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	// Must appear as attributes.branding.logo_url.
	branding, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("data.attributes.branding is absent or not an object; attrs=%v", attrs)
	}
	if branding["logo_url"] != "https://x/l.png" {
		t.Errorf("branding.logo_url = %v, want \"https://x/l.png\"", branding["logo_url"])
	}

	// Must NOT appear at the top level.
	if _, hasTop := attrs["logo_url"]; hasTop {
		t.Errorf("top-level attributes.logo_url must NOT be present (backend 422); attrs=%v", attrs)
	}
	if _, hasTop := attrs["logo-url"]; hasTop {
		t.Errorf("top-level attributes.logo-url must NOT be present; attrs=%v", attrs)
	}
}

// TestHubsCreate_UnsetLogoURLOmitsBranding verifies that when --logo-url is
// NOT passed, no branding key is emitted at all (PATCH partial-update
// semantics: unchanged flags are never serialised).
//
// CONTRACT: no --logo-url flag → no attributes.branding key in request body.
func TestHubsCreate_UnsetLogoURLOmitsBranding(t *testing.T) {
	srv, _, _, gotBody := captureHubRequest(t, http.StatusCreated)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "create",
			"--name", "Hub Without Logo",
		)...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit code = %d, want %d (ExitOK); stderr=%q", res.Code, errs.ExitOK, res.Stderr)
	}

	attrs := decodeHubAttrs(t, *gotBody)

	if _, hasBranding := attrs["branding"]; hasBranding {
		t.Errorf("branding must be absent when --logo-url is not passed; got attrs=%v", attrs)
	}
	if _, hasTop := attrs["logo_url"]; hasTop {
		t.Errorf("top-level logo_url must be absent; got attrs=%v", attrs)
	}
}

// TestHubsUpdate_LogoURLErrorsFast verifies that `mio hubs update --logo-url`
// fails immediately with a usage error (exit 2) and sends NO HTTP request.
//
// The backend assigns branding wholesale (not merged), so a partial branding
// patch would silently clobber sibling keys. Rather than silently ignoring the
// flag (which would mislead the caller), the command now fails fast. (MIO-901)
//
// CONTRACT (MIO-844 / MIO-901): hubs update --logo-url X → ExitUsage (2),
// no PATCH fired, error message mentions logo-url/update.
func TestHubsUpdate_LogoURLErrorsFast(t *testing.T) {
	patchFired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		patchFired = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalHubBody))
	}))
	t.Cleanup(srv.Close)

	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1",
			"hubs", "update", "hub_abc123",
			"--name", "X",
			"--logo-url", "https://x/l.png",
		)...)

	// Must exit with a usage error — not success.
	if res.Code != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}

	// No HTTP request must have been fired.
	if patchFired {
		t.Error("PATCH must NOT be sent when --logo-url is passed to hubs update")
	}

	// The error message content (mentioning logo-url/update) is only available on
	// the real binary's stderr (main.go renders it through the JSON:API envelope);
	// the in-process driver silences errors at the cobra level. Message content is
	// therefore NOT asserted here — the two assertions above are sufficient to
	// prove fail-fast behaviour.
}
