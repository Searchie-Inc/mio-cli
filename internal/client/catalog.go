package client

// catalog.go — raw page-builder catalog fetch (MIO-2340, charter §6.1).
//
// GET /api/page-builder/catalog (auto-rewritten to /api/v1/… by
// canonicalRequestPath) returns the RAW catalog.json body — plain JSON, not a
// JSON:API document — with an ETag equal to the catalog digest. It participates
// in HTTP conditional-GET caching: a client that echoes a prior ETag via
// If-None-Match gets a 304 with no body. This is deliberately kept off the
// JSON:API do() path (which decodes an envelope and treats every non-2xx,
// including 304, as an error).

import (
	"context"
	"io"
	"net/http"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// catalogPath is the (pre-rewrite) catalog endpoint. canonicalRequestPath maps
// it to /api/v1/page-builder/catalog.
const catalogPath = "/api/page-builder/catalog"

// CatalogResult is the outcome of a raw catalog fetch. On a 304 NotModified is
// true and Body is empty (the caller reuses its cache); otherwise Body holds the
// raw catalog.json bytes and ETag holds the server's ETag (the digest, quoted).
type CatalogResult struct {
	Body        []byte
	ETag        string
	NotModified bool
}

// FetchCatalog fetches the raw page-builder catalog. When ifNoneMatch is
// non-empty it is sent as the If-None-Match header so an unchanged catalog
// answers 304 (NotModified). Bearer auth is attached when the client has a key,
// but the endpoint is hub-agnostic and may be public — callers should treat a
// fetch failure as a soft signal and fall back to a cached/vendored catalog.
func (c *Client) FetchCatalog(ctx context.Context, ifNoneMatch string) (CatalogResult, error) {
	u := c.baseURL + canonicalRequestPath(catalogPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return CatalogResult{}, errs.Wrap(errs.ExitGeneric, err)
	}
	req.Header.Set("Accept", contentTypeJSON)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return CatalogResult{}, errs.Wrap(errs.ExitGeneric, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return CatalogResult{ETag: firstNonEmpty(resp.Header.Get("ETag"), ifNoneMatch), NotModified: true}, nil
	}

	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return CatalogResult{}, errs.Wrap(errs.ExitGeneric, rerr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CatalogResult{}, c.errorForResponse(resp.StatusCode, body)
	}
	return CatalogResult{Body: body, ETag: resp.Header.Get("ETag")}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
