package client

// scaffold_from_template.go — the W2b one-step scaffold op (MIO-2573 §5.1).
// The CLI PROBES this op: a 404 (dormant flag or older backend) means "apply
// client-side instead" and is surfaced as ExitNotFound for the caller to catch.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ScaffoldFromTemplateRequest carries the scaffold attributes plus the
// REQUIRED Idempotency-Key header value (≤255 chars, enforced server-side).
type ScaffoldFromTemplateRequest struct {
	HubTemplateID  string
	Name           string
	Slug           string
	CatalogDigest  string
	IdempotencyKey string
}

// ScaffoldedPage is one page the op created, keyed by its template role.
type ScaffoldedPage struct {
	Role              string
	PageID            string
	PublishedRevision int
}

// ScaffoldFromTemplateResult is the decoded 201 response of the op.
type ScaffoldFromTemplateResult struct {
	HubID string
	Pages []ScaffoldedPage
}

// ScaffoldFromTemplate calls POST .../hubs/{hub}/pages/scaffold-from-template
// with a JSON:API envelope (data.type "template_scaffolds") and the required
// Idempotency-Key header. Errors flow through the client's normal status
// mapping: 404 → ExitNotFound (op absent — dormant flag or older backend; the
// caller falls back to client-side apply), 409/422 → ExitUsage. An empty 2xx
// body yields a zero-value result and nil error.
func (c *Client) ScaffoldFromTemplate(ctx context.Context, teamID, hubID string, req ScaffoldFromTemplateRequest) (ScaffoldFromTemplateResult, error) {
	path := fmt.Sprintf("/api/teams/%s/hubs/%s/pages/scaffold-from-template", teamID, hubID)
	attrs := map[string]any{
		"hub_template_id": req.HubTemplateID,
		"name":            req.Name,
		"slug":            req.Slug,
		"catalog_digest":  req.CatalogDigest,
	}
	res, err := c.ActionWithHeaders(ctx, StyleEnvelope, http.MethodPost, path, attrs,
		map[string]string{"Idempotency-Key": req.IdempotencyKey})
	if err != nil {
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
		page.PageID, _ = m["page_id"].(string)
		page.PublishedRevision = intFromAny(m["published_revision"])
		out.Pages = append(out.Pages, page)
	}
	return out, nil
}

// intFromAny coerces a decoded JSON numeric value to int. The client decodes
// with encoding/json defaults today (numbers arrive as float64), but
// json.Number and native int variants are handled too so the helper stays
// correct if the decoding strategy ever changes. Non-numeric values yield 0.
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
