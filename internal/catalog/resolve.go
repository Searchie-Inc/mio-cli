package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Source string

const (
	SourceOverride Source = "override"
	SourceLive     Source = "live"
	SourceCache    Source = "cache"
	SourceVendored Source = "vendored"
)

const (
	cacheBodyFile = "catalog.json"
	cacheETagFile = "catalog.etag"
)

type FetchResult struct {
	Body        []byte
	ETag        string
	NotModified bool
}

type Fetcher interface {
	FetchCatalog(ctx context.Context, ifNoneMatch string) (FetchResult, error)
}

type ResolveOptions struct {
	Offline      bool
	OverrideFile string
	CacheDir     string
	Fetcher      Fetcher
	Warnf        func(format string, a ...any)

	Mutating bool
}

func DefaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "mio", "page-builder-catalog")
}

func CacheDirUnder(base, origin string) string {
	if base == "" || origin == "" {
		return base
	}
	return filepath.Join(base, sanitizeCacheSegment(origin))
}

func CacheDirForOrigin(origin string) string {
	return CacheDirUnder(DefaultCacheDir(), origin)
}

func sanitizeCacheSegment(s string) string {
	if u, err := url.Parse(strings.TrimSpace(s)); err == nil && u.Host != "" {
		s = strings.ToLower(u.Host)
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

	adopt := func(body []byte) (*Catalog, error) {
		cat, derr := parseAndVerify(body)
		if derr != nil {
			return nil, derr
		}
		cache.store(body, `"`+cat.Meta.Digest+`"`)
		return cat, nil
	}

	if !opts.Offline && opts.Fetcher != nil {
		res, err := opts.Fetcher.FetchCatalog(ctx, cache.etag())
		switch {
		case err != nil:
			if opts.Mutating {
				return nil, "", mutatingFetchFailed(err)
			}
			warn("catalog: live fetch failed (%v); falling back to cached/vendored copy", err)
		case res.NotModified:
			if cat, ok := cache.load(); ok {
				return cat, SourceCache, nil
			}
			warn("catalog: 304 with an unavailable local cache; re-fetching unconditionally")
			fresh, ferr := opts.Fetcher.FetchCatalog(ctx, "")
			switch {
			case ferr != nil:
				if opts.Mutating {
					return nil, "", mutatingFetchFailed(ferr)
				}
			case fresh.NotModified:
				if opts.Mutating {
					return nil, "", mutatingFetchFailed(errors.New("the backend answered 304 twice with no usable local cache"))
				}
			default:
				cat, aerr := adopt(fresh.Body)
				if aerr == nil {
					return cat, SourceLive, nil
				}
				if opts.Mutating {
					return nil, "", mutatingRejected(aerr)
				}
				warn("catalog: rejecting fetched catalog (%v); falling back to cached/vendored copy", aerr)
			}
		default:
			cat, aerr := adopt(res.Body)
			if aerr == nil {
				return cat, SourceLive, nil
			}
			if opts.Mutating {
				return nil, "", mutatingRejected(aerr)
			}
			warn("catalog: rejecting fetched catalog (%v); falling back to cached/vendored copy", aerr)
		}

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

func mutatingFetchFailed(err error) error {
	return fmt.Errorf("catalog: live fetch failed and a mutating command cannot fall back to a stale copy: %w", err)
}

func mutatingRejected(err error) error {
	return fmt.Errorf("catalog: fetched catalog rejected and a mutating command cannot fall back to a stale copy: %w", err)
}

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
