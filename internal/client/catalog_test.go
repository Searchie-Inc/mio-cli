package client

// catalog_test.go — the raw page-builder catalog fetch (MIO-2340). Unlike every
// other client verb the catalog endpoint returns PLAIN JSON (not JSON:API) and
// participates in HTTP caching: an If-None-Match echo of the ETag yields 304,
// which must surface as NotModified (not an error) so the CLI can reuse its
// on-disk cache.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchCatalog_200_ReturnsBodyETagAndRewritesPath(t *testing.T) {
	var gotPath, gotAccept, gotAuth, gotINM string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept, gotAuth, gotINM = r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Authorization"), r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"sha256:deadbeef"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"meta":{"catalogVersion":"9.9.9"}}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "mio_sk_test_abc")
	res, err := c.FetchCatalog(context.Background(), "")
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}
	if res.NotModified {
		t.Error("200 must not report NotModified")
	}
	if string(res.Body) != `{"meta":{"catalogVersion":"9.9.9"}}` {
		t.Errorf("Body = %q", res.Body)
	}
	if res.ETag != `"sha256:deadbeef"` {
		t.Errorf("ETag = %q, want quoted digest", res.ETag)
	}
	if gotPath != "/api/v1/page-builder/catalog" {
		t.Errorf("path = %q, want /api/v1/page-builder/catalog (rewrite)", gotPath)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json (plain JSON, not JSON:API)", gotAccept)
	}
	if gotAuth != "Bearer mio_sk_test_abc" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotINM != "" {
		t.Errorf("If-None-Match = %q, want empty when no etag supplied", gotINM)
	}
}

func TestFetchCatalog_304_NotModified(t *testing.T) {
	var gotINM string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		if gotINM == `"sha256:cached"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	res, err := c.FetchCatalog(context.Background(), `"sha256:cached"`)
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}
	if !res.NotModified {
		t.Error("304 must report NotModified")
	}
	if len(res.Body) != 0 {
		t.Errorf("304 body = %q, want empty", res.Body)
	}
	if gotINM != `"sha256:cached"` {
		t.Errorf("If-None-Match = %q, want the supplied etag", gotINM)
	}
}

func TestFetchCatalog_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := newTestClient(srv, "k")
	_, err := c.FetchCatalog(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention the status", err)
	}
}
