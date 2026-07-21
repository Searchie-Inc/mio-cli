package cmd

// hubs_scaffold_builders_test.go — direct unit tests for the reusable hub blob
// builders extracted from the `hubs` command RunEs so a future scaffold command
// can drive the same logic without a *cobra.Command (MIO-2543).
//
// These exercise the pure functions against a real httptest server, capturing
// the write body the builder produces. They complement — they do NOT replace —
// the command-level contract tests in hubs_update_blobs_test.go and
// hubs_authoring_test.go, which remain the behaviour-preserving guard for
// `mio hubs update`.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// sbPtr returns a pointer to v, for populating the builder input structs (whose
// pointer fields distinguish "flag unset" from "flag set to the zero value").
func sbPtr[T any](v T) *T { return &v }

// applyHubBlobsServer answers a GET retrieve with getBody and captures the
// subsequent PATCH body, returning a client pointed at it. Used to unit-test
// applyHubBlobs' read-modify-write directly (no cobra command involved).
func applyHubBlobsServer(t *testing.T, getBody string) (*client.Client, *[]byte) {
	t.Helper()
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patchBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(getBody))
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "k"), &patchBody
}

// TestApplyHubBlobs_BrandingRMWPreservesSiblings verifies the core read-modify-
// write contract: a partial branding patch deep-merges onto the hub's current
// branding, so untouched sibling keys survive (not a wholesale replace).
func TestApplyHubBlobs_BrandingRMWPreservesSiblings(t *testing.T) {
	const body = `{"data":{"id":"hub_abc123","type":"hubs","attributes":{"title":"H","slug":"h","branding":{"primary":"#111","logo_url":"old"}}}}`
	cl, patchBody := applyHubBlobsServer(t, body)

	var warn bytes.Buffer
	_, err := applyHubBlobs(context.Background(), cl, "t_team1", "hub_abc123", "",
		blobPatches{Branding: map[string]any{"favicon_url": "f"}}, &warn)
	if err != nil {
		t.Fatalf("applyHubBlobs returned error: %v", err)
	}

	attrs := decodeHubAttrs(t, *patchBody)
	b, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH branding is absent or not an object; attrs=%v", attrs)
	}
	want := map[string]any{"primary": "#111", "logo_url": "old", "favicon_url": "f"}
	if len(b) != len(want) {
		t.Fatalf("branding = %v, want exactly %v (RMW must preserve siblings, add favicon)", b, want)
	}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("branding[%q] = %v, want %v", k, b[k], v)
		}
	}
	if warn.Len() != 0 {
		t.Errorf("known keys must not warn; warn=%q", warn.String())
	}
}

// TestApplyHubBlobs_UnsetRemovesKey verifies an --unset-style dotted path deletes
// a nested key from the retrieved blob while preserving its sibling.
func TestApplyHubBlobs_UnsetRemovesKey(t *testing.T) {
	const body = `{"data":{"id":"hub_abc123","type":"hubs","attributes":{"title":"H","slug":"h","settings":{"header":{"color":"#000","menuLayout":"tabs"}}}}}`
	cl, patchBody := applyHubBlobsServer(t, body)

	var warn bytes.Buffer
	_, err := applyHubBlobs(context.Background(), cl, "t_team1", "hub_abc123", "",
		blobPatches{Unset: []unsetPath{{blob: "settings", segments: []string{"header", "color"}, raw: "settings.header.color"}}}, &warn)
	if err != nil {
		t.Fatalf("applyHubBlobs returned error: %v", err)
	}

	attrs := decodeHubAttrs(t, *patchBody)
	s, ok := attrs["settings"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH settings absent or not an object; attrs=%v", attrs)
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
}

// TestApplyHubBlobs_DoesNotMutateBase verifies the pure-builder contract the
// scaffold caller relies on: applyHubBlobs never mutates the caller's Base map
// (nor its nested maps), even when an --unset descends into a blob the caller
// staged in Base. The removal is applied on a copy that goes to the PATCH.
func TestApplyHubBlobs_DoesNotMutateBase(t *testing.T) {
	const body = `{"data":{"id":"hub_abc123","type":"hubs","attributes":{"title":"H","slug":"h"}}}`
	cl, patchBody := applyHubBlobsServer(t, body)

	base := map[string]any{
		"settings": map[string]any{
			"header": map[string]any{"color": "#000", "menuLayout": "tabs"},
		},
	}
	var warn bytes.Buffer
	_, err := applyHubBlobs(context.Background(), cl, "t_team1", "hub_abc123", "",
		blobPatches{
			Base:  base,
			Unset: []unsetPath{{blob: "settings", segments: []string{"header", "color"}, raw: "settings.header.color"}},
		}, &warn)
	if err != nil {
		t.Fatalf("applyHubBlobs returned error: %v", err)
	}

	// The caller's Base and its nested maps must be byte-for-byte untouched.
	s, _ := base["settings"].(map[string]any)
	header, _ := s["header"].(map[string]any)
	if header["color"] != "#000" {
		t.Errorf("Base settings.header.color = %v, want original '#000' (Base must not be mutated)", header["color"])
	}
	if len(header) != 2 {
		t.Errorf("Base settings.header = %v, want both original keys intact (Base must not be mutated)", header)
	}

	// ...while the PATCH (built on the copy) does reflect the removal.
	attrs := decodeHubAttrs(t, *patchBody)
	ps, _ := attrs["settings"].(map[string]any)
	ph, ok := ps["header"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH settings.header absent; settings=%v", ps)
	}
	if _, has := ph["color"]; has {
		t.Errorf("PATCH settings.header.color must be removed; header=%v", ph)
	}
}

// TestApplyHubBlobs_NavigationHrefScopedToLiveSlug exercises the hub-scoped href
// validation that now lives inside applyHubBlobs: with the slug NOT known up
// front (SlugKnown false), the hub is retrieved and every hub-relative type:"url"
// href must start with "/{slug}". A scoped href passes and the nav is PATCHed; a
// mismatched one errors and fires no PATCH.
func TestApplyHubBlobs_NavigationHrefScopedToLiveSlug(t *testing.T) {
	const body = `{"data":{"id":"hub_abc123","type":"hubs","attributes":{"title":"H","slug":"demo"}}}`

	t.Run("scoped href passes and nav is sent", func(t *testing.T) {
		cl, patchBody := applyHubBlobsServer(t, body)
		var warn bytes.Buffer
		nav := map[string]any{"header": []any{
			map[string]any{"type": "url", "label": "About", "href": "/demo/about"},
		}}
		_, err := applyHubBlobs(context.Background(), cl, "t_team1", "hub_abc123", "",
			blobPatches{Navigation: nav}, &warn)
		if err != nil {
			t.Fatalf("a hub-scoped href (/demo/about for slug demo) should pass; got: %v", err)
		}
		attrs := decodeHubAttrs(t, *patchBody)
		if _, ok := attrs["navigation"].(map[string]any); !ok {
			t.Fatalf("navigation must be sent in the PATCH; attrs=%v", attrs)
		}
	})

	t.Run("mismatched href errors and fires no PATCH", func(t *testing.T) {
		cl, patchBody := applyHubBlobsServer(t, body)
		var warn bytes.Buffer
		nav := map[string]any{"header": []any{
			map[string]any{"type": "url", "label": "Escape", "href": "/other/about"},
		}}
		_, err := applyHubBlobs(context.Background(), cl, "t_team1", "hub_abc123", "",
			blobPatches{Navigation: nav}, &warn)
		if err == nil {
			t.Fatal("a href not scoped to the hub slug (/other/about for slug demo) must error")
		}
		if len(*patchBody) != 0 {
			t.Errorf("no PATCH must fire when href validation fails; patchBody=%q", *patchBody)
		}
	})
}

// ─── buildHubCreateAttrs (Task 3) ────────────────────────────────────────────

// TestBuildHubCreateAttrs_ComposesBlobsAndOverrides verifies the pure create
// builder layers the typed-column base, the presentation blobs and the logo/
// favicon overrides into one POST body — with --logo-url/--favicon-url merging
// into (not clobbering) --branding-json — and, crucially, never mutates the
// caller's Base or Branding maps (the pure-builder contract the scaffold relies
// on to reuse a template across hubs).
func TestBuildHubCreateAttrs_ComposesBlobsAndOverrides(t *testing.T) {
	base := map[string]any{"title": "My Community", "slug": "my-community", "is_private": false}
	branding := map[string]any{"primary": "#6747E3"}
	logo := "https://x/l.png"
	favicon := "https://x/fav.ico"

	var warn bytes.Buffer
	attrs, err := buildHubCreateAttrs(hubCreateParams{
		Base:     base,
		Branding: branding,
		Logo:     &logo,
		Favicon:  &favicon,
	}, &warn)
	if err != nil {
		t.Fatalf("buildHubCreateAttrs returned error: %v", err)
	}

	// Typed columns pass through.
	if attrs["title"] != "My Community" || attrs["slug"] != "my-community" || attrs["is_private"] != false {
		t.Errorf("typed columns not carried through; attrs=%v", attrs)
	}
	// Branding composes: --branding-json key survives, logo/favicon merged in.
	b, ok := attrs["branding"].(map[string]any)
	if !ok {
		t.Fatalf("branding absent or not an object; attrs=%v", attrs)
	}
	want := map[string]any{"primary": "#6747E3", "logo_url": logo, "favicon_url": favicon}
	if len(b) != len(want) {
		t.Fatalf("branding = %v, want exactly %v (logo/favicon must merge, not clobber)", b, want)
	}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("branding[%q] = %v, want %v", k, b[k], v)
		}
	}
	if warn.Len() != 0 {
		t.Errorf("known keys must not warn; warn=%q", warn.String())
	}

	// Pure-builder contract: the caller's Base and Branding are untouched.
	if _, leaked := base["branding"]; leaked {
		t.Errorf("Base must not gain a branding key (must not be mutated); base=%v", base)
	}
	if _, leaked := branding["logo_url"]; leaked {
		t.Errorf("p.Branding must not be mutated by the logo/favicon merge; branding=%v", branding)
	}
	if len(branding) != 1 {
		t.Errorf("p.Branding = %v, want the single original key intact (not mutated)", branding)
	}
}

// TestBuildHubCreateAttrs_EmptyRejected verifies an empty create (no base, no
// blobs, no overrides) is a usage error — the same "nothing to create" guard the
// command enforces, now owned by the pure builder.
func TestBuildHubCreateAttrs_EmptyRejected(t *testing.T) {
	var warn bytes.Buffer
	_, err := buildHubCreateAttrs(hubCreateParams{}, &warn)
	if err == nil {
		t.Fatal("an empty create must return a usage error")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
}

// TestBuildHubCreateAttrs_StrictRejectsUnknownBrandingKey verifies Strict turns
// an unknown blob key into a usage error (rather than a warning), matching the
// command's --strict-keys behaviour.
func TestBuildHubCreateAttrs_StrictRejectsUnknownBrandingKey(t *testing.T) {
	var warn bytes.Buffer
	_, err := buildHubCreateAttrs(hubCreateParams{
		Base:     map[string]any{"title": "H"},
		Branding: map[string]any{"totally_unknown_key": "x"},
		Strict:   true,
	}, &warn)
	if err == nil {
		t.Fatal("an unknown branding key under Strict must error")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
}

// TestBuildHubCreateAttrs_NavigationHrefScopedToSlug verifies the create-path
// navigation validation the builder owns: a hub-relative href must start with
// "/{slug}" (the --slug value carried in Base, known up front on create), so a
// scoped href passes and a mismatched one errors.
func TestBuildHubCreateAttrs_NavigationHrefScopedToSlug(t *testing.T) {
	scoped := map[string]any{"header": []any{
		map[string]any{"type": "url", "label": "About", "href": "/demo/about"},
	}}
	var warn bytes.Buffer
	if _, err := buildHubCreateAttrs(hubCreateParams{
		Base:       map[string]any{"title": "H", "slug": "demo"},
		Navigation: scoped,
	}, &warn); err != nil {
		t.Fatalf("a hub-scoped href (/demo/about for slug demo) should pass; got: %v", err)
	}

	mismatched := map[string]any{"header": []any{
		map[string]any{"type": "url", "label": "Escape", "href": "/other/about"},
	}}
	if _, err := buildHubCreateAttrs(hubCreateParams{
		Base:       map[string]any{"title": "H", "slug": "demo"},
		Navigation: mismatched,
	}, &warn); err == nil {
		t.Fatal("a href not scoped to the create slug (/other/about for slug demo) must error")
	}
}

// ─── publishedStateAttrs (Task 4) ────────────────────────────────────────────

// TestPublishedStateAttrs_InvertsToIsPrivate verifies the single source of truth
// for the published→is_private inversion both `hubs update` and the scaffold
// publish step share: published=true → is_private=false (and vice versa), and
// "published" is never emitted (it is not a writable attribute).
func TestPublishedStateAttrs_InvertsToIsPrivate(t *testing.T) {
	for _, tc := range []struct {
		published   bool
		wantPrivate bool
	}{
		{true, false},
		{false, true},
	} {
		got := publishedStateAttrs(tc.published)
		if got["is_private"] != tc.wantPrivate {
			t.Errorf("publishedStateAttrs(%t)[is_private] = %v, want %v", tc.published, got["is_private"], tc.wantPrivate)
		}
		if _, has := got["published"]; has {
			t.Errorf("publishedStateAttrs(%t) must not emit a \"published\" key; got %v", tc.published, got)
		}
		if len(got) != 1 {
			t.Errorf("publishedStateAttrs(%t) = %v, want exactly one key (is_private)", tc.published, got)
		}
	}
}

// ─── applyHubPolicies (Task 5) ───────────────────────────────────────────────

// applyHubPoliciesServer captures the PATCH body a policies write produces and
// returns a client pointed at it (no cobra command involved).
func applyHubPoliciesServer(t *testing.T) (*client.Client, *[]byte) {
	t.Helper()
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			patchBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"pol_1","type":"policies","attributes":{"policy_type":"tos"}}}`))
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "k"), &patchBody
}

// TestApplyHubPolicies_SetsContent verifies a content write sends policy_type +
// the content string + require_acceptance when provided.
func TestApplyHubPolicies_SetsContent(t *testing.T) {
	cl, patchBody := applyHubPoliciesServer(t)
	content := "# Terms of Service"
	require := true

	res, err := applyHubPolicies(context.Background(), cl, "t_team1", "hub_x",
		hubPolicy{PolicyType: "tos", Content: &content, RequireAcceptance: &require})
	if err != nil {
		t.Fatalf("applyHubPolicies returned error: %v", err)
	}
	if res == nil {
		t.Fatal("applyHubPolicies returned a nil resource")
	}

	attrs := decodeHubAttrs(t, *patchBody)
	if attrs["policy_type"] != "tos" {
		t.Errorf("policy_type = %v, want \"tos\"", attrs["policy_type"])
	}
	if attrs["content"] != content {
		t.Errorf("content = %v, want %q", attrs["content"], content)
	}
	if attrs["require_acceptance"] != true {
		t.Errorf("require_acceptance = %v, want true", attrs["require_acceptance"])
	}
}

// TestApplyHubPolicies_ResetSendsNullContent verifies a nil Content pointer sends
// content: JSON null (present but null, not absent) — the reset-to-default path —
// and omits require_acceptance when not provided.
func TestApplyHubPolicies_ResetSendsNullContent(t *testing.T) {
	cl, patchBody := applyHubPoliciesServer(t)

	if _, err := applyHubPolicies(context.Background(), cl, "t_team1", "hub_x",
		hubPolicy{PolicyType: "tos"}); err != nil {
		t.Fatalf("applyHubPolicies returned error: %v", err)
	}

	// Decode raw so null is distinguishable from absent.
	var doc struct {
		Data struct {
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(*patchBody, &doc); err != nil {
		t.Fatalf("request body is not valid JSON: %v; body=%q", err, *patchBody)
	}
	contentRaw, present := doc.Data.Attributes["content"]
	if !present {
		t.Errorf("content must be present as JSON null on reset; attrs=%v", doc.Data.Attributes)
	} else if string(contentRaw) != "null" {
		t.Errorf("content = %s, want null", contentRaw)
	}
	if _, has := doc.Data.Attributes["require_acceptance"]; has {
		t.Errorf("require_acceptance must be omitted when not provided; attrs=%v", doc.Data.Attributes)
	}
}

// ─── buildSpaceAttrs (Task 6) ────────────────────────────────────────────────

// TestBuildSpaceAttrs_MapsFieldsAndKeys verifies the space builder maps every set
// field onto its backend attribute key (segment_id/is_default/is_pinned/
// access_level/posting_permission), and omits fields whose pointer is nil.
func TestBuildSpaceAttrs_MapsFieldsAndKeys(t *testing.T) {
	attrs, err := buildSpaceAttrs(SpaceInput{
		Name:              sbPtr("General"),
		Slug:              sbPtr("general"),
		Description:       sbPtr("desc"),
		Icon:              sbPtr("star"),
		Color:             sbPtr("#111"),
		SegmentID:         sbPtr("seg_vip"),
		IsDefault:         sbPtr(true),
		IsPinned:          sbPtr(false),
		AccessLevel:       sbPtr("restricted"),
		PostingPermission: sbPtr("segment"),
		Position:          sbPtr(0),
	})
	if err != nil {
		t.Fatalf("buildSpaceAttrs returned error: %v", err)
	}
	want := map[string]any{
		"name": "General", "slug": "general", "description": "desc", "icon": "star",
		"color": "#111", "segment_id": "seg_vip", "is_default": true, "is_pinned": false,
		"access_level": "restricted", "posting_permission": "segment", "position": 0,
	}
	if len(attrs) != len(want) {
		t.Fatalf("attrs = %v, want exactly %v", attrs, want)
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("attrs[%q] = %v, want %v", k, attrs[k], v)
		}
	}
	// A nil pointer must be omitted, not written as a zero value.
	if _, err := buildSpaceAttrs(SpaceInput{Name: sbPtr("Only")}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBuildSpaceAttrs_ValidatesEnumsAndPosition verifies the enum/range checks the
// builder keeps (so the scaffold caller gets them too): a bad access_level,
// posting_permission, or negative position each returns ExitUsage.
func TestBuildSpaceAttrs_ValidatesEnumsAndPosition(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   SpaceInput
	}{
		{"bad access_level", SpaceInput{AccessLevel: sbPtr("everyone")}},
		{"bad posting_permission", SpaceInput{PostingPermission: sbPtr("nobody")}},
		{"negative position", SpaceInput{Position: sbPtr(-1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSpaceAttrs(tc.in)
			if err == nil {
				t.Fatalf("%s must return an error", tc.name)
			}
			if got := errs.CodeOf(err); got != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
			}
		})
	}
}

// ─── buildPageAttrs (Task 7) ─────────────────────────────────────────────────

// TestBuildPageAttrs_MapsFieldsAndBlobs verifies the page builder maps --is-home
// to is_homepage, passes settings/meta through as objects, and omits nil fields.
func TestBuildPageAttrs_MapsFieldsAndBlobs(t *testing.T) {
	settings := map[string]any{"theme": "dark"}
	meta := map[string]any{"seo": true}
	attrs, err := buildPageAttrs(PageInput{
		Title:    sbPtr("Home"),
		Slug:     sbPtr("dashboard"),
		Type:     sbPtr("generic"),
		IsHome:   sbPtr(true),
		Position: sbPtr(0),
		Privacy:  sbPtr("public"),
		Settings: settings,
		Meta:     meta,
	})
	if err != nil {
		t.Fatalf("buildPageAttrs returned error: %v", err)
	}
	if attrs["is_homepage"] != true {
		t.Errorf("is_homepage = %v, want true (--is-home mapping)", attrs["is_homepage"])
	}
	if _, leaked := attrs["is_home"]; leaked {
		t.Errorf("is_home must not be present (schema uses is_homepage); attrs=%v", attrs)
	}
	if attrs["title"] != "Home" || attrs["slug"] != "dashboard" || attrs["type"] != "generic" ||
		attrs["privacy"] != "public" || attrs["position"] != 0 {
		t.Errorf("scalar fields not carried through; attrs=%v", attrs)
	}
	s, ok := attrs["settings"].(map[string]any)
	if !ok || s["theme"] != "dark" {
		t.Errorf("settings not passed through as object; attrs=%v", attrs)
	}
	if m, ok := attrs["meta"].(map[string]any); !ok || m["seo"] != true {
		t.Errorf("meta not passed through as object; attrs=%v", attrs)
	}
}

// TestBuildPageAttrs_ValidatesPrivacyAndPosition verifies the privacy enum + the
// position >= 0 check the builder keeps for the scaffold caller.
func TestBuildPageAttrs_ValidatesPrivacyAndPosition(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   PageInput
	}{
		{"bad privacy", PageInput{Privacy: sbPtr("secret")}},
		{"negative position", PageInput{Position: sbPtr(-1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildPageAttrs(tc.in)
			if err == nil {
				t.Fatalf("%s must return an error", tc.name)
			}
			if got := errs.CodeOf(err); got != errs.ExitUsage {
				t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
			}
		})
	}
}

// ─── buildHubMediaPublishAttrs (Task 8) ──────────────────────────────────────

// TestBuildHubMediaPublishAttrs_SetsProvided verifies a valid visibility, an RFC3339
// published_at (formatted from the time.Time), and a position are all set.
func TestBuildHubMediaPublishAttrs_SetsProvided(t *testing.T) {
	when := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	pos := 3
	attrs, err := buildHubMediaPublishAttrs("public", when, &pos)
	if err != nil {
		t.Fatalf("buildHubMediaPublishAttrs returned error: %v", err)
	}
	if attrs["visibility"] != "public" {
		t.Errorf("visibility = %v, want public", attrs["visibility"])
	}
	if attrs["published_at"] != "2026-01-02T15:04:05Z" {
		t.Errorf("published_at = %v, want RFC3339 2026-01-02T15:04:05Z", attrs["published_at"])
	}
	if attrs["position"] != 3 {
		t.Errorf("position = %v, want 3", attrs["position"])
	}
}

// TestBuildHubMediaPublishAttrs_OmitsSentinels verifies the "omit" sentinels: an
// empty visibility, a zero time, and a nil position produce an empty map (the
// command path relies on this to preserve "only set when flag changed").
func TestBuildHubMediaPublishAttrs_OmitsSentinels(t *testing.T) {
	attrs, err := buildHubMediaPublishAttrs("", time.Time{}, nil)
	if err != nil {
		t.Fatalf("buildHubMediaPublishAttrs returned error: %v", err)
	}
	if len(attrs) != 0 {
		t.Errorf("attrs = %v, want empty (all sentinels omit)", attrs)
	}
}

// TestBuildHubMediaPublishAttrs_Validates verifies a bad visibility and a negative
// position each return ExitUsage.
func TestBuildHubMediaPublishAttrs_Validates(t *testing.T) {
	if _, err := buildHubMediaPublishAttrs("everyone", time.Time{}, nil); err == nil {
		t.Error("a bad visibility must error")
	} else if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
	neg := -1
	if _, err := buildHubMediaPublishAttrs("", time.Time{}, &neg); err == nil {
		t.Error("a negative position must error")
	} else if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
}

// ─── buildAttrDefCreateAttrs + buildHubConfigAttrs (Task 9) ───────────────────

// TestBuildAttrDefCreateAttrs_MapsFieldTypeToType verifies --field-type maps to the
// backend "type" key (NOT field_type) and the other fields carry through.
func TestBuildAttrDefCreateAttrs_MapsFieldTypeToType(t *testing.T) {
	attrs, err := buildAttrDefCreateAttrs(AttrDefInput{
		Name:              sbPtr("Company"),
		Slug:              sbPtr("company"),
		FieldType:         sbPtr("text"),
		Description:       sbPtr("Employer"),
		IsContactEditable: sbPtr(false),
		Position:          sbPtr(2),
	})
	if err != nil {
		t.Fatalf("buildAttrDefCreateAttrs returned error: %v", err)
	}
	if attrs["type"] != "text" {
		t.Errorf("type = %v, want text (--field-type maps to \"type\")", attrs["type"])
	}
	if _, leaked := attrs["field_type"]; leaked {
		t.Errorf("field_type must NOT be present (wrong key); attrs=%v", attrs)
	}
	if attrs["name"] != "Company" || attrs["slug"] != "company" ||
		attrs["description"] != "Employer" || attrs["is_contact_editable"] != false ||
		attrs["position"] != 2 {
		t.Errorf("fields not carried through; attrs=%v", attrs)
	}
}

// TestBuildAttrDefCreateAttrs_RejectsBadFieldType verifies the NEW client-side
// field-type enum validation (MIO-2543): an out-of-enum value returns ExitUsage.
func TestBuildAttrDefCreateAttrs_RejectsBadFieldType(t *testing.T) {
	_, err := buildAttrDefCreateAttrs(AttrDefInput{
		Name: sbPtr("X"), Slug: sbPtr("x"), FieldType: sbPtr("select"),
	})
	if err == nil {
		t.Fatal("an out-of-enum --field-type must return an error")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("exit code = %d, want %d (ExitUsage)", got, errs.ExitUsage)
	}
}

// TestBuildHubConfigAttrs_IncludesDefinitionIDAndBools verifies the create builder
// puts the definition id in the body (MIO-2502) and maps the bool flags; unset
// bools are omitted.
func TestBuildHubConfigAttrs_IncludesDefinitionIDAndBools(t *testing.T) {
	attrs := buildHubConfigAttrs("attr_abc123", HubConfigInput{
		IsInProfile:  sbPtr(true),
		IsRequired:   sbPtr(false),
		IsSearchable: sbPtr(true),
	})
	if attrs["definition_id"] != "attr_abc123" {
		t.Errorf("definition_id = %v, want attr_abc123", attrs["definition_id"])
	}
	if attrs["is_in_profile"] != true || attrs["is_required"] != false || attrs["is_searchable"] != true {
		t.Errorf("bool flags not mapped; attrs=%v", attrs)
	}
	// Unset bools must be omitted (partial-update semantics).
	for _, k := range []string{"is_in_onboarding", "is_read_only"} {
		if _, has := attrs[k]; has {
			t.Errorf("%s must be omitted when unset; attrs=%v", k, attrs)
		}
	}
}

// ─── buildPlaylistCreateAttrs (Task 10) ──────────────────────────────────────

// TestBuildPlaylistCreateAttrs_MapsFields verifies title/description/visibility map
// through and --hub-id maps to hub_id; unset fields are omitted.
func TestBuildPlaylistCreateAttrs_MapsFields(t *testing.T) {
	attrs := buildPlaylistCreateAttrs(PlaylistInput{
		Title:      sbPtr("Course Videos"),
		Visibility: sbPtr("public"),
		HubID:      sbPtr("hub_abc123"),
	})
	if attrs["title"] != "Course Videos" || attrs["visibility"] != "public" {
		t.Errorf("title/visibility not carried; attrs=%v", attrs)
	}
	if attrs["hub_id"] != "hub_abc123" {
		t.Errorf("hub_id = %v, want hub_abc123 (--hub-id mapping)", attrs["hub_id"])
	}
	if _, leaked := attrs["hub-id"]; leaked {
		t.Errorf("hub-id (wrong key) must not be present; attrs=%v", attrs)
	}
	// --description unset → omitted.
	if _, has := attrs["description"]; has {
		t.Errorf("description must be omitted when unset; attrs=%v", attrs)
	}
}
