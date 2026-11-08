package client

import (
	"context"
	"io"
	"net/http"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const catalogPath = "/api/page-builder/catalog"

type CatalogResult struct {
	Body        []byte
	ETag        string
	NotModified bool
}

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
