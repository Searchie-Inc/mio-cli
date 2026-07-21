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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

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
