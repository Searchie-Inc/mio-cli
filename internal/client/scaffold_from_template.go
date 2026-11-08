package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type ScaffoldFromTemplateRequest struct {
	HubTemplateID  string
	Name           string
	Slug           string
	CatalogDigest  string
	IdempotencyKey string
}

type ScaffoldedPage struct {
	Role              string
	Slug              string
	PageID            string
	PublishedRevision int
}

type ScaffoldFromTemplateResult struct {
	HubID string
	Pages []ScaffoldedPage
}

func (c *Client) ScaffoldFromTemplate(ctx context.Context, teamID, hubID string, req ScaffoldFromTemplateRequest) (ScaffoldFromTemplateResult, error) {
	path := fmt.Sprintf("/api/teams/%s/hubs/%s/pages/scaffold-from-template", teamID, hubID)
	attrs := map[string]any{
		"hub_template_id": req.HubTemplateID,
		"name":            req.Name,
		"slug":            req.Slug,
		"catalog_digest":  req.CatalogDigest,
	}
	res, status, err := c.actionWithHeadersStatus(ctx, StyleEnvelope, http.MethodPost, path, attrs,
		map[string]string{"Idempotency-Key": req.IdempotencyKey})
	if err != nil {
		if status == http.StatusMethodNotAllowed {
			return ScaffoldFromTemplateResult{}, errs.Wrap(errs.ExitNotFound, err)
		}
		return ScaffoldFromTemplateResult{}, err
	}
	if res == nil {
		return ScaffoldFromTemplateResult{}, nil
	}

	out := ScaffoldFromTemplateResult{}
	out.HubID, _ = res.Attributes["hub_id"].(string)
	pages, _ := res.Attributes["pages"].([]any)
	for _, p := range pages {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		var page ScaffoldedPage
		page.Role, _ = m["role"].(string)
		page.Slug, _ = m["slug"].(string)
		page.PageID, _ = m["page_id"].(string)
		page.PublishedRevision = intFromAny(m["published_revision"])
		out.Pages = append(out.Pages, page)
	}
	return out, nil
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		if f, err := n.Float64(); err == nil {
			return int(f)
		}
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
