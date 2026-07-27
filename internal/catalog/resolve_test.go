package catalog

// resolve_test.go — catalog source resolution precedence (MIO-2340): the CLI
// prefers a fresh live-fetched catalog, falls back to the last-good on-disk
// cache, and finally to the embedded digest-pinned vendored copy — degrading
// gracefully so scaffolding works offline/air-gapped. --catalog overrides
// everything; --offline forces the vendored copy.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeFetcher is a scripted Fetcher for the resolver tests.
type fakeFetcher struct {
	res    FetchResult
	err    error
	called bool
	gotINM string
}

func (f *fakeFetcher) FetchCatalog(_ context.Context, ifNoneMatch string) (FetchResult, error) {
	f.called = true
	f.gotINM = ifNoneMatch
	if f.err != nil {
		return FetchResult{}, f.err
	}
	return f.res, nil
}

func vendoredBytes(t *testing.T) []byte {
	t.Helper()
	return vendoredCatalogJSON // valid digest by construction
}

func TestResolve_Live200_Valid_AdoptsAndCaches(t *testing.T) {
	dir := t.TempDir()
	f := &fakeFetcher{res: FetchResult{Body: vendoredBytes(t), ETag: `"` + pinnedDigest + `"`}}
	cat, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceLive {
		t.Errorf("source = %q, want live", src)
	}
	if cat.Meta.CatalogVersion != "0.10.0" {
		t.Errorf("catalogVersion = %q", cat.Meta.CatalogVersion)
	}
	// Cache must be written for the next invocation.
	if _, err := os.Stat(filepath.Join(dir, cacheBodyFile)); err != nil {
		t.Errorf("live fetch did not write the cache body: %v", err)
	}
}

func TestResolve_Live200_DigestMismatch_FallsBackToVendored(t *testing.T) {
	dir := t.TempDir()
	// Parses fine, but meta.digest disagrees with the recomputed digest.
	bad := []byte(`{"meta":{"digest":"sha256:wrong"},"templates":[],"pageTemplates":[],"sectionTypes":[],"pageTypes":[]}`)
	f := &fakeFetcher{res: FetchResult{Body: bad, ETag: `"sha256:wrong"`}}
	var warned bool
	cat, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Warnf: func(string, ...any) { warned = true }})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceVendored {
		t.Errorf("source = %q, want vendored (mismatch must be rejected)", src)
	}
	if !warned {
		t.Error("a digest mismatch should warn")
	}
	if len(cat.Templates) != 8 {
		t.Errorf("fell back to the wrong catalog: %d templates", len(cat.Templates))
	}
}

func TestResolve_Live304_UsesCache(t *testing.T) {
	dir := t.TempDir()
	// Seed the cache with a valid (vendored) body + etag.
	seedCache(t, dir, vendoredBytes(t), `"`+pinnedDigest+`"`)
	f := &fakeFetcher{res: FetchResult{NotModified: true}}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceCache {
		t.Errorf("source = %q, want cache", src)
	}
	if f.gotINM == "" {
		t.Error("expected the cached ETag to be sent as If-None-Match")
	}
}

func TestResolve_FetchError_WithCache_UsesCache(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, vendoredBytes(t), `"`+pinnedDigest+`"`)
	f := &fakeFetcher{err: errors.New("network down")}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Warnf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceCache {
		t.Errorf("source = %q, want cache (fetch failed but cache is warm)", src)
	}
}

func TestResolve_FetchError_NoCache_UsesVendored(t *testing.T) {
	f := &fakeFetcher{err: errors.New("network down")}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: t.TempDir(), Warnf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceVendored {
		t.Errorf("source = %q, want vendored", src)
	}
}

func TestResolve_Offline_ForcesVendored_NoFetch(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, vendoredBytes(t), `"etag"`)
	f := &fakeFetcher{res: FetchResult{Body: vendoredBytes(t)}}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Offline: true})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceVendored {
		t.Errorf("source = %q, want vendored (offline)", src)
	}
	if f.called {
		t.Error("--offline must not hit the network")
	}
}

func TestResolve_OverrideFile_Wins(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(fp, vendoredBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{res: FetchResult{Body: vendoredBytes(t)}}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, OverrideFile: fp, CacheDir: dir})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceOverride {
		t.Errorf("source = %q, want override", src)
	}
	if f.called {
		t.Error("--catalog override must not hit the network")
	}
}

func TestResolve_NilFetcher_UsesVendored(t *testing.T) {
	_, src, err := Resolve(context.Background(), ResolveOptions{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceVendored {
		t.Errorf("source = %q, want vendored (no fetcher)", src)
	}
}

// refetchFetcher answers 304 to a conditional request (ifNoneMatch != "") and
// 200 with body to an unconditional one — modeling the recovery path when a
// cached ETag exists but the cached body is gone.
type refetchFetcher struct {
	body  []byte
	calls int
}

func (f *refetchFetcher) FetchCatalog(_ context.Context, ifNoneMatch string) (FetchResult, error) {
	f.calls++
	if ifNoneMatch != "" {
		return FetchResult{NotModified: true}, nil
	}
	return FetchResult{Body: f.body, ETag: `"ignored-by-resolver"`}, nil
}

func TestResolve_Live304_CacheBodyMissing_Refetches(t *testing.T) {
	dir := t.TempDir()
	// Seed only the ETag (no body): cache.load() will fail but etag() returns it,
	// so the first fetch is conditional → 304.
	if err := os.WriteFile(filepath.Join(dir, cacheETagFile), []byte(`"stale"`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &refetchFetcher{body: vendoredBytes(t)}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Warnf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceLive {
		t.Errorf("source = %q, want live (recovered via unconditional refetch)", src)
	}
	if f.calls != 2 {
		t.Errorf("expected 2 fetches (conditional 304 + unconditional refetch); got %d", f.calls)
	}
}

func TestResolve_Live200_CachesDigestDerivedETag(t *testing.T) {
	dir := t.TempDir()
	// The server sends a WRONG ETag; the resolver must persist the digest-derived
	// ETag instead, so the cached body and ETag can never desync.
	f := &fakeFetcher{res: FetchResult{Body: vendoredBytes(t), ETag: `"server-sent-wrong"`}}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceLive {
		t.Fatalf("source = %q, want live", src)
	}
	got, err := os.ReadFile(filepath.Join(dir, cacheETagFile))
	if err != nil {
		t.Fatalf("read cached etag: %v", err)
	}
	want := `"` + pinnedDigest + `"`
	if string(got) != want {
		t.Errorf("cached etag = %q, want digest-derived %q (not the server's header)", got, want)
	}
}

// seedCache writes a body+etag into the cache dir the way Resolve expects.
func seedCache(t *testing.T, dir string, body []byte, etag string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, cacheBodyFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cacheETagFile), []byte(etag), 0o600); err != nil {
		t.Fatal(err)
	}
}
