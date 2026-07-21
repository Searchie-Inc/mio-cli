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
