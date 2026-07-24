package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// TestScaffoldFromTemplate_SendsEnvelopeAndIdempotencyHeader verifies the full
// wire contract of the W2b op (MIO-2573 §5.1): POST to the /api/v1-rewritten
// path, the REQUIRED Idempotency-Key header, and a JSON:API envelope whose
// data.type is "template_scaffolds" with the four snake_case attributes.
func TestScaffoldFromTemplate_SendsEnvelopeAndIdempotencyHeader(t *testing.T) {
	var gotMethod, gotPath, gotIdemKey string
	var gotBody struct {
		Data struct {
			Type       string         `json:"type"`
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotIdemKey = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"type":"template_scaffolds","id":"hub_1","attributes":{` +
			`"hub_id":"hub_1","pages":[{"role":"homepage","page_id":"pg_1","published_revision":1}]}}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	res, err := c.ScaffoldFromTemplate(context.Background(), "t_1", "hub_1", ScaffoldFromTemplateRequest{
		HubTemplateID:  "tmpl_1",
		Name:           "My Hub",
		Slug:           "my-hub",
		CatalogDigest:  "sha256:abc",
		IdempotencyKey: "k-123",
	})
	if err != nil {
		t.Fatalf("ScaffoldFromTemplate error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/api/v1/teams/t_1/hubs/hub_1/pages/scaffold-from-template"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotIdemKey != "k-123" {
		t.Errorf("Idempotency-Key = %q, want %q", gotIdemKey, "k-123")
	}
	if gotBody.Data.Type != "template_scaffolds" {
		t.Errorf("data.type = %q, want %q", gotBody.Data.Type, "template_scaffolds")
	}
	wantAttrs := map[string]string{
		"hub_template_id": "tmpl_1",
		"name":            "My Hub",
		"slug":            "my-hub",
		"catalog_digest":  "sha256:abc",
	}
	for k, want := range wantAttrs {
		if got, _ := gotBody.Data.Attributes[k].(string); got != want {
			t.Errorf("data.attributes[%q] = %q, want %q", k, got, want)
		}
	}
	if len(gotBody.Data.Attributes) != len(wantAttrs) {
		t.Errorf("data.attributes has %d keys, want %d: %v", len(gotBody.Data.Attributes), len(wantAttrs), gotBody.Data.Attributes)
	}

	if res.HubID != "hub_1" {
		t.Errorf("HubID = %q, want %q", res.HubID, "hub_1")
	}
	if len(res.Pages) != 1 {
		t.Fatalf("len(Pages) = %d, want 1", len(res.Pages))
	}
	if res.Pages[0].Role != "homepage" {
		t.Errorf("Pages[0].Role = %q, want %q", res.Pages[0].Role, "homepage")
	}
	if res.Pages[0].PageID != "pg_1" {
		t.Errorf("Pages[0].PageID = %q, want %q", res.Pages[0].PageID, "pg_1")
	}
	if res.Pages[0].PublishedRevision != 1 {
		t.Errorf("Pages[0].PublishedRevision = %d, want 1", res.Pages[0].PublishedRevision)
	}
}

// TestScaffoldFromTemplate_404IsExitNotFound: the backend ships the op DORMANT
// (flag off → 404) and older backends lack the route entirely. The caller
// probes for exactly this code to fall back to client-side apply, so a 404
// must surface as ExitNotFound through the normal error path.
func TestScaffoldFromTemplate_404IsExitNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	_, err := c.ScaffoldFromTemplate(context.Background(), "t_1", "hub_1", ScaffoldFromTemplateRequest{
		HubTemplateID:  "tmpl_1",
		Name:           "My Hub",
		Slug:           "my-hub",
		CatalogDigest:  "sha256:abc",
		IdempotencyKey: "k-123",
	})
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if got := errs.CodeOf(err); got != errs.ExitNotFound {
		t.Errorf("CodeOf(err) = %d, want ExitNotFound (%d); err = %v", got, errs.ExitNotFound, err)
	}
}

// TestScaffoldFromTemplate_409IsExitUsage: a digest/page/fingerprint conflict
// is an actionable caller mistake → ExitUsage, with the server detail intact.
func TestScaffoldFromTemplate_409IsExitUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSONAPI)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"errors":[{"status":"409","code":"catalog_digest_mismatch",` +
			`"detail":"catalog digest sha256:abc does not match the server catalog"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	_, err := c.ScaffoldFromTemplate(context.Background(), "t_1", "hub_1", ScaffoldFromTemplateRequest{
		HubTemplateID:  "tmpl_1",
		Name:           "My Hub",
		Slug:           "my-hub",
		CatalogDigest:  "sha256:abc",
		IdempotencyKey: "k-123",
	})
	if err == nil {
		t.Fatal("expected error on 409, got nil")
	}
	if got := errs.CodeOf(err); got != errs.ExitUsage {
		t.Errorf("CodeOf(err) = %d, want ExitUsage (%d); err = %v", got, errs.ExitUsage, err)
	}
	if !strings.Contains(err.Error(), "does not match the server catalog") {
		t.Errorf("err = %q, want it to contain the server detail", err.Error())
	}
}

// TestBaseURL_ReturnsConfiguredOrigin: New trims the trailing slash, and
// BaseURL exposes the resulting origin (used to origin-scope the catalog cache).
func TestBaseURL_ReturnsConfiguredOrigin(t *testing.T) {
	if got := New("https://x.test/", "k").BaseURL(); got != "https://x.test" {
		t.Errorf("BaseURL() = %q, want %q", got, "https://x.test")
	}
}
