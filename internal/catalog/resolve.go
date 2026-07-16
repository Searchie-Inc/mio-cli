package catalog

// resolve.go — catalog source resolution (MIO-2340, charter §6.1/§6.4).
//
// The CLI prefers the freshest catalog it can trust, degrading gracefully so
// scaffolding always works — even offline/air-gapped:
//
//	--catalog <file>   → that file, exclusively (SourceOverride)
//	--offline          → the embedded vendored copy, exclusively (SourceVendored)
//	otherwise          → live fetch (SourceLive) with the last-good on-disk cache
//	                     (SourceCache) as the conditional-GET target and the
//	                     first fallback, then the vendored copy (SourceVendored).
//
// A live or cached body is digest-verified before adoption (meta.digest ==
// recomputed); a mismatch is rejected and the resolver falls back. The backend
// remains the authority on writes (charter §6.3/§6.4), so a slightly stale CLI
// catalog only affects local scaffolding/validation, never correctness on the
// server.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Source identifies which catalog a Resolve call ended up using.
type Source string

const (
	SourceOverride Source = "override" // --catalog <file>
	SourceLive     Source = "live"     // fresh HTTP 200, digest-verified
	SourceCache    Source = "cache"    // last-good on-disk copy (304 or fetch failure)
	SourceVendored Source = "vendored" // embedded digest-pinned fallback
)

const (
	cacheBodyFile = "catalog.json"
	cacheETagFile = "catalog.etag"
)

// FetchResult is the client-agnostic outcome of a raw catalog fetch. cmd adapts
// the client's CatalogResult into this so internal/catalog carries no dependency
// on the HTTP layer.
type FetchResult struct {
	Body        []byte
	ETag        string
	NotModified bool
}

// Fetcher fetches the raw catalog over HTTP, honoring If-None-Match.
type Fetcher interface {
	FetchCatalog(ctx context.Context, ifNoneMatch string) (FetchResult, error)
}

// ResolveOptions configures catalog resolution.
type ResolveOptions struct {
	Offline      bool                          // force the vendored copy (no network, no cache)
	OverrideFile string                        // --catalog: use this file exclusively
	CacheDir     string                        // on-disk cache dir ("" disables caching)
	Fetcher      Fetcher                       // live fetch source (nil skips live fetch)
	Warnf        func(format string, a ...any) // non-fatal diagnostics ("" → stderr; nil → silent)
}

// DefaultCacheDir returns the per-user catalog cache directory, or "" if the OS
// cache dir cannot be determined (caching then simply degrades to off).
func DefaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "mio", "page-builder-catalog")
}

// Resolve loads the active catalog per the precedence documented above and
// reports which source was used.
func Resolve(ctx context.Context, opts ResolveOptions) (*Catalog, Source, error) {
	warn := opts.Warnf
	if warn == nil {
		warn = func(string, ...any) {}
	}

	if opts.OverrideFile != "" {
		body, err := os.ReadFile(opts.OverrideFile)
		if err != nil {
			return nil, "", fmt.Errorf("catalog: --catalog %s: %w", opts.OverrideFile, err)
		}
		cat, err := Parse(body)
		if err != nil {
			return nil, "", fmt.Errorf("catalog: --catalog %s: %w", opts.OverrideFile, err)
		}
		if derr := verifyCatalogDigest(cat); derr != nil {
			warn("catalog: --catalog %s: %v (using it anyway)", opts.OverrideFile, derr)
		}
		return cat, SourceOverride, nil
	}

	cache := diskCache{dir: opts.CacheDir}

	if !opts.Offline && opts.Fetcher != nil {
		res, err := opts.Fetcher.FetchCatalog(ctx, cache.etag())
		switch {
		case err != nil:
			warn("catalog: live fetch failed (%v); falling back to cached/vendored copy", err)
		case res.NotModified:
			if cat, ok := cache.load(); ok {
				return cat, SourceCache, nil
			}
			warn("catalog: server returned 304 but the local cache is unavailable; using vendored copy")
		default:
			cat, derr := parseAndVerify(res.Body)
			if derr != nil {
				warn("catalog: rejecting live catalog (%v); falling back to cached/vendored copy", derr)
			} else {
				cache.store(res.Body, res.ETag) // best-effort; failure is non-fatal
				return cat, SourceLive, nil
			}
		}

		// Live fetch attempted but not adopted — try the last-good cache.
		if cat, ok := cache.load(); ok {
			return cat, SourceCache, nil
		}
	}

	cat, err := Load()
	if err != nil {
		return nil, "", err
	}
	return cat, SourceVendored, nil
}

// parseAndVerify parses a catalog body and rejects it unless meta.digest matches
// the recomputed digest.
func parseAndVerify(body []byte) (*Catalog, error) {
	cat, err := Parse(body)
	if err != nil {
		return nil, err
	}
	if err := verifyCatalogDigest(cat); err != nil {
		return nil, err
	}
	return cat, nil
}

// verifyCatalogDigest checks that the catalog's declared meta.digest equals the
// digest recomputed from its content (charter §5.2.1) — integrity for any body
// that did not come from the embedded, already-trusted vendored copy.
func verifyCatalogDigest(cat *Catalog) error {
	if cat.Meta.Digest == "" {
		return fmt.Errorf("meta.digest is missing")
	}
	computed, err := Digest(cat.raw)
	if err != nil {
		return err
	}
	if computed != cat.Meta.Digest {
		return fmt.Errorf("digest mismatch (meta.digest=%s computed=%s)", cat.Meta.Digest, computed)
	}
	return nil
}

// diskCache is the last-good on-disk catalog cache (a body + its ETag). A "" dir
// disables it (all methods no-op).
type diskCache struct{ dir string }

func (d diskCache) etag() string {
	if d.dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(d.dir, cacheETagFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (d diskCache) load() (*Catalog, bool) {
	if d.dir == "" {
		return nil, false
	}
	body, err := os.ReadFile(filepath.Join(d.dir, cacheBodyFile))
	if err != nil {
		return nil, false
	}
	cat, err := parseAndVerify(body)
	if err != nil {
		return nil, false
	}
	return cat, true
}

func (d diskCache) store(body []byte, etag string) {
	if d.dir == "" {
		return
	}
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(d.dir, cacheBodyFile), body, 0o600); err != nil {
		return
	}
	if etag != "" {
		_ = os.WriteFile(filepath.Join(d.dir, cacheETagFile), []byte(etag), 0o600)
	}
}
