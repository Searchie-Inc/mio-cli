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
//
// A resolve with Mutating set fails closed instead of degrading: writes must
// be driven by the CURRENT catalog from the target backend (or a digest-
// verified --catalog override), never by a stale cache or the vendored copy.

import (
	"context"
	"fmt"
	"net/url"
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

	// Mutating marks a resolve whose catalog will drive WRITES. It fails closed
	// where a read-only resolve degrades: a digest-mismatched OverrideFile is
	// rejected; a failed live fetch is an error (no stale-cache or vendored
	// fallback); absent both a Fetcher and an OverrideFile it errors
	// immediately. A 304-validated cache read is still allowed (the server
	// confirmed it current).
	Mutating bool
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

// CacheDirUnder returns base scoped to a backend origin, so a cache populated
// from one origin is never validated/read against another. Empty origin or
// base falls back to base unchanged (legacy unscoped layout). The segment is
// host[:port] reduced to filesystem-safe characters. cmd wires base from
// MIO_CATALOG_CACHE_DIR / DefaultCacheDir (a later task).
func CacheDirUnder(base, origin string) string {
	if base == "" || origin == "" {
		return base
	}
	return filepath.Join(base, sanitizeCacheSegment(origin))
}

// CacheDirForOrigin is CacheDirUnder over the default OS cache dir.
func CacheDirForOrigin(origin string) string {
	return CacheDirUnder(DefaultCacheDir(), origin)
}

// sanitizeCacheSegment reduces an origin (URL or bare host) to a single
// filesystem-safe path segment: the host[:port] with anything outside
// [A-Za-z0-9._-] replaced by '_'.
func sanitizeCacheSegment(s string) string {
	if u, err := url.Parse(strings.TrimSpace(s)); err == nil && u.Host != "" {
		s = u.Host
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
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
			if opts.Mutating {
				return nil, "", fmt.Errorf("catalog: --catalog %s: %w (a mutating command refuses a digest-mismatched catalog)", opts.OverrideFile, derr)
			}
			warn("catalog: --catalog %s: %v (using it anyway)", opts.OverrideFile, derr)
		}
		return cat, SourceOverride, nil
	}

	cache := diskCache{dir: opts.CacheDir}

	// adopt verifies a fetched body's digest, persists it as the new last-good
	// cache (keyed by the digest-derived ETag so body and ETag never desync), and
	// returns the parsed catalog. A rejected body returns the digest error; the
	// caller decides between warn-and-fall-back and Mutating's fail-closed.
	adopt := func(body []byte) (*Catalog, error) {
		cat, derr := parseAndVerify(body)
		if derr != nil {
			return nil, derr
		}
		// charter §5.2.1: the ETag equals the quoted meta.digest. Derive it from
		// the verified catalog rather than trusting the server's header, so the
		// cached body and ETag are always mutually consistent.
		cache.store(body, `"`+cat.Meta.Digest+`"`)
		return cat, nil
	}

	if !opts.Offline && opts.Fetcher != nil {
		res, err := opts.Fetcher.FetchCatalog(ctx, cache.etag())
		switch {
		case err != nil:
			if opts.Mutating {
				return nil, "", fmt.Errorf("catalog: live fetch failed and a mutating command cannot fall back to a stale copy: %w", err)
			}
			warn("catalog: live fetch failed (%v); falling back to cached/vendored copy", err)
		case res.NotModified:
			// A 304-validated cache read is allowed even under Mutating: the server
			// just confirmed the cached copy is current.
			if cat, ok := cache.load(); ok {
				return cat, SourceCache, nil
			}
			// The server says our cached ETag is current, but the cached body is
			// missing/corrupt. Re-fetch unconditionally to recover the live catalog
			// rather than silently dropping to a (possibly stale) vendored copy.
			warn("catalog: 304 with an unavailable local cache; re-fetching unconditionally")
			if fresh, ferr := opts.Fetcher.FetchCatalog(ctx, ""); ferr == nil && !fresh.NotModified {
				cat, aerr := adopt(fresh.Body)
				if aerr == nil {
					return cat, SourceLive, nil
				}
				if opts.Mutating {
					return nil, "", fmt.Errorf("catalog: live fetch failed and a mutating command cannot fall back to a stale copy: %w", aerr)
				}
				warn("catalog: rejecting fetched catalog (%v); falling back to cached/vendored copy", aerr)
			}
		default:
			cat, aerr := adopt(res.Body)
			if aerr == nil {
				return cat, SourceLive, nil
			}
			if opts.Mutating {
				return nil, "", fmt.Errorf("catalog: live fetch failed and a mutating command cannot fall back to a stale copy: %w", aerr)
			}
			warn("catalog: rejecting fetched catalog (%v); falling back to cached/vendored copy", aerr)
		}

		// Live fetch attempted but not adopted — try the last-good cache (never
		// for a mutating resolve: a stale last-good copy must not drive writes).
		if !opts.Mutating {
			if cat, ok := cache.load(); ok {
				return cat, SourceCache, nil
			}
		}
	}

	if opts.Mutating {
		return nil, "", fmt.Errorf("catalog: a mutating command requires a live catalog fetch or --catalog override")
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
