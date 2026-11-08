package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var ErrHubOpAbsent = errors.New("hub scaffold op not available on this backend")

var errUnidentifiedHub = errors.New(
	"the hub scaffold op reported success but its response did not identify a hub. " +
		"A hub MAY have been created — check `mio hubs list`, or re-run the identical command " +
		"(the idempotency key makes a re-run a replay, not a duplicate)")

type HubFromTemplateOverrides struct {
	LogoURL             *string
	FaviconURL          *string
	RegistrationEnabled *bool
	Publish             bool
}

type HubFromTemplateRequest struct {
	HubTemplateID  string
	Name           string
	Slug           string
	CatalogDigest  string
	Overrides      HubFromTemplateOverrides
	IdempotencyKey string
}

type HubFromTemplateSummaryRow struct {
	Resource string
	Action   string
	Reason   string
}

type HubFromTemplateResult struct {
	HubID      string
	Summary    []HubFromTemplateSummaryRow
	CreatedIDs map[string][]string
	Replayed   bool
}

const hubScaffoldType = "hub_scaffolds"

func unidentifiedHubErr(status int) error {
	ce := errs.Wrap(errs.ExitServer, errUnidentifiedHub)
	ce.HTTPStatus = status
	return ce
}

func (c *Client) HubFromTemplate(ctx context.Context, teamID string, req HubFromTemplateRequest) (HubFromTemplateResult, error) {
	path := fmt.Sprintf("/api/teams/%s/hubs/from-template", teamID)

	overrides := map[string]any{}
	if req.Overrides.LogoURL != nil {
		overrides["logo_url"] = *req.Overrides.LogoURL
	}
	if req.Overrides.FaviconURL != nil {
		overrides["favicon_url"] = *req.Overrides.FaviconURL
	}
	if req.Overrides.RegistrationEnabled != nil {
		overrides["registration_enabled"] = *req.Overrides.RegistrationEnabled
	}
	overrides["publish"] = req.Overrides.Publish

	attrs := map[string]any{
		"hub_template_id": req.HubTemplateID,
		"name":            req.Name,
		"slug":            req.Slug,
		"catalog_digest":  req.CatalogDigest,
		"overrides":       overrides,
	}

	res, status, err := c.actionWithHeadersStatus(ctx, StyleEnvelope, http.MethodPost, path, attrs,
		map[string]string{"Idempotency-Key": req.IdempotencyKey})
	if err != nil {
		if status == http.StatusMethodNotAllowed {
			return HubFromTemplateResult{}, errs.Wrap(errs.ExitNotFound,
				fmt.Errorf("%w: %w", ErrHubOpAbsent, err))
		}
		return HubFromTemplateResult{}, err
	}
	if res == nil {
		return HubFromTemplateResult{}, unidentifiedHubErr(status)
	}

	if res.Type != "" && res.Type != hubScaffoldType {
		return HubFromTemplateResult{}, unidentifiedHubErr(status)
	}

	out := HubFromTemplateResult{CreatedIDs: map[string][]string{}}
	out.HubID, _ = res.Attributes["hub_id"].(string)
	if out.HubID == "" {
		out.HubID = res.ID
	}
	if out.HubID == "" {
		return HubFromTemplateResult{}, unidentifiedHubErr(status)
	}
	out.Replayed, _ = res.Attributes["replayed"].(bool)

	if rows, ok := res.Attributes["summary"].([]any); ok {
		for _, r := range rows {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			row := HubFromTemplateSummaryRow{}
			row.Resource, _ = m["resource"].(string)
			row.Action, _ = m["action"].(string)
			row.Reason, _ = m["reason"].(string)
			out.Summary = append(out.Summary, row)
		}
	}
	if created, ok := res.Attributes["created_resource_ids"].(map[string]any); ok {
		for kind, v := range created {
			ids, ok := v.([]any)
			if !ok {
				continue
			}
			for _, id := range ids {
				if s, ok := id.(string); ok {
					out.CreatedIDs[kind] = append(out.CreatedIDs[kind], s)
				}
			}
		}
	}
	return out, nil
}
