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
	"strings"
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
	// Assert against the vendored copy's own version rather than a literal: this
	// test proves Resolve ADOPTED the body it was handed, not what today's pin
	// happens to be (that is parity_test's digest pin). Keeps a re-pin to
	// catalog.json + fixtures + CATALOG_REF with no unrelated test churn.
	if want := loadForTest(t).Meta.CatalogVersion; cat.Meta.CatalogVersion != want {
		t.Errorf("catalogVersion = %q, want %q (the vendored pin)", cat.Meta.CatalogVersion, want)
	}
	// Cache must be written for the next invocation.
	if _, err := os.Stat(filepath.Join(dir, cacheBodyFile)); err != nil {
		t.Errorf("live fetch did not write the cache body: %v", err)
	}
}

func TestResolve_Live200_DigestMismatch_FallsBackToVendored(t *testing.T) {
	dir := t.TempDir()
	f := &fakeFetcher{res: FetchResult{Body: badDigestBody(), ETag: `"sha256:wrong"`}}
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

// badDigestBody parses fine, but its meta.digest disagrees with the recomputed
// digest, so digest verification must reject it.
func badDigestBody() []byte {
	return []byte(`{"meta":{"digest":"sha256:wrong"},"templates":[],"pageTemplates":[],"sectionTypes":[],"pageTypes":[]}`)
}

func TestResolve_Mutating_OverrideDigestMismatchFailsClosed(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(fp, badDigestBody(), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := Resolve(context.Background(), ResolveOptions{OverrideFile: fp, Mutating: true})
	if err == nil {
		t.Fatal("mutating resolve accepted a digest-mismatched --catalog override")
	}
	if !strings.Contains(err.Error(), "a mutating command refuses a digest-mismatched catalog") {
		t.Errorf("error = %q, want the mutating refusal message", err)
	}

	// Non-mutating keeps the existing warn-and-use behavior.
	var warned bool
	_, src, err := Resolve(context.Background(), ResolveOptions{OverrideFile: fp, Warnf: func(string, ...any) { warned = true }})
	if err != nil {
		t.Fatalf("non-mutating Resolve: %v", err)
	}
	if src != SourceOverride {
		t.Errorf("source = %q, want override", src)
	}
	if !warned {
		t.Error("non-mutating digest mismatch should warn")
	}
}

func TestResolve_Mutating_LiveFetchErrorFailsNoFallback(t *testing.T) {
	dir := t.TempDir()
	// Warm cache: the test must prove the fallback is REFUSED, not just absent.
	seedCache(t, dir, vendoredBytes(t), `"sha256:faae8f12a9236644b9868b75ef1d245002736228b51f2f8eefa1a43ddd7bd392"`)
	f := &fakeFetcher{err: errors.New("network down")}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Mutating: true, Warnf: func(string, ...any) {}})
	if err == nil {
		t.Fatalf("mutating resolve fell back to %q after a fetch error; want an error", src)
	}
	if !strings.Contains(err.Error(), "cannot fall back to a stale copy") {
		t.Errorf("error = %q, want the no-stale-fallback message", err)
	}
}

func TestResolve_Mutating_BadLiveDigestFailsNoFallback(t *testing.T) {
	dir := t.TempDir()
	// Warm cache: a rejected live body must NOT continue to cache/vendored.
	seedCache(t, dir, vendoredBytes(t), `"sha256:faae8f12a9236644b9868b75ef1d245002736228b51f2f8eefa1a43ddd7bd392"`)
	f := &fakeFetcher{res: FetchResult{Body: badDigestBody(), ETag: `"sha256:wrong"`}}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Mutating: true, Warnf: func(string, ...any) {}})
	if err == nil {
		t.Fatalf("mutating resolve fell back to %q after a digest-rejected live body; want an error", src)
	}
	if !strings.Contains(err.Error(), "cannot fall back to a stale copy") {
		t.Errorf("error = %q, want the no-stale-fallback message", err)
	}
}

func TestResolve_Mutating_304UsesValidatedCache(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, vendoredBytes(t), `"sha256:faae8f12a9236644b9868b75ef1d245002736228b51f2f8eefa1a43ddd7bd392"`)
	f := &fakeFetcher{res: FetchResult{NotModified: true}}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Mutating: true})
	if err != nil {
		t.Fatalf("Resolve: %v (a 304-validated cache read is allowed under Mutating)", err)
	}
	if src != SourceCache {
		t.Errorf("source = %q, want cache", src)
	}
}

func TestResolve_Mutating_304CacheBodyMissing_Refetches(t *testing.T) {
	dir := t.TempDir()
	// ETag only, no body: 304 validates a cache we cannot read back, so the
	// resolver must recover via an unconditional refetch — under Mutating too.
	if err := os.WriteFile(filepath.Join(dir, cacheETagFile), []byte(`"stale"`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &refetchFetcher{body: vendoredBytes(t)}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Mutating: true, Warnf: func(string, ...any) {}})
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

// failRefetchFetcher answers 304 to a conditional request and errors on the
// unconditional refetch — a fetch failure inside the 304-recovery path.
type failRefetchFetcher struct{ calls int }

func (f *failRefetchFetcher) FetchCatalog(_ context.Context, ifNoneMatch string) (FetchResult, error) {
	f.calls++
	if ifNoneMatch != "" {
		return FetchResult{NotModified: true}, nil
	}
	return FetchResult{}, errors.New("refetch: connection reset")
}

func TestResolve_Mutating_304RefetchError_SurfacesCause(t *testing.T) {
	dir := t.TempDir()
	// ETag only, no body: the 304 validates a cache we cannot read back, and the
	// recovery refetch then fails. The error must carry the real fetch failure,
	// not the generic no-fetcher message.
	if err := os.WriteFile(filepath.Join(dir, cacheETagFile), []byte(`"stale"`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &failRefetchFetcher{}
	_, src, err := Resolve(context.Background(), ResolveOptions{Fetcher: f, CacheDir: dir, Mutating: true, Warnf: func(string, ...any) {}})
	if err == nil {
		t.Fatalf("mutating resolve used %q after a failed 304 recovery; want an error", src)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error = %q, want the underlying refetch failure surfaced", err)
	}
	if strings.Contains(err.Error(), "requires a live catalog fetch or --catalog override") {
		t.Errorf("error = %q; the generic no-fetcher message must not swallow the real cause", err)
	}
	if f.calls != 2 {
		t.Errorf("expected 2 fetches (conditional 304 + failed unconditional refetch); got %d", f.calls)
	}
}

func TestResolve_Mutating_NoFetcherNoOverrideFails(t *testing.T) {
	_, src, err := Resolve(context.Background(), ResolveOptions{CacheDir: t.TempDir(), Mutating: true})
	if err == nil {
		t.Fatalf("mutating resolve used %q with no fetcher and no override; want an error", src)
	}
	if !strings.Contains(err.Error(), "requires a live catalog fetch or --catalog override") {
		t.Errorf("error = %q, want the live-or-override message", err)
	}
}

func TestCacheDirUnder_SeparatesOrigins(t *testing.T) {
	base := filepath.Join("some", "base")

	a := CacheDirUnder(base, "https://api.member.dev")
	b := CacheDirUnder(base, "https://api.other.dev")
	if a == b {
		t.Errorf("distinct origins share a cache dir: %q", a)
	}
	if want := filepath.Join(base, "api.member.dev"); a != want {
		t.Errorf("CacheDirUnder = %q, want %q (URL host extraction)", a, want)
	}
	if got, want := CacheDirUnder(base, "http://localhost:8000"), filepath.Join(base, "localhost_8000"); got != want {
		t.Errorf("CacheDirUnder = %q, want %q (port separator sanitized)", got, want)
	}
	if got, want := CacheDirUnder(base, "https://API.Member.DEV"), filepath.Join(base, "api.member.dev"); got != want {
		t.Errorf("CacheDirUnder = %q, want %q (hosts are case-insensitive; segment lowercased)", got, want)
	}
	if got, want := CacheDirUnder(base, "wéird stuff"), filepath.Join(base, "w_ird_stuff"); got != want {
		t.Errorf("CacheDirUnder = %q, want %q (weird chars sanitized to _)", got, want)
	}
	if got := CacheDirUnder(base, ""); got != base {
		t.Errorf("empty origin: got %q, want base %q unchanged", got, base)
	}
	if got := CacheDirUnder("", "https://api.member.dev"); got != "" {
		t.Errorf("empty base: got %q, want \"\" unchanged", got)
	}
	if got := CacheDirForOrigin(""); got != DefaultCacheDir() {
		t.Errorf("CacheDirForOrigin(\"\") = %q, want DefaultCacheDir() %q", got, DefaultCacheDir())
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
