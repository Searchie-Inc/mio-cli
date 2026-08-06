package client

// hub_from_template.go — the server-side WHOLE-HUB scaffold op (MIO-2926,
// mio-backend #641), probed by `mio hubs scaffold` exactly as the pages op is
// (see scaffold_from_template.go).
//
// The probe IS the real POST — never a separate capability check. A dormant or
// legacy backend answers 405 with `Allow: GET`, deliberately byte-identical to
// a backend where the route does not exist: mio-backend's admin_router already
// registers GET|PATCH|DELETE /api/teams/{team_id}/hubs/{identifier}, which
// matches the literal "from-template" segment, so Starlette's
// method-not-allowed branch fires. Their `_FlagGatedAPIRoute` reproduces that
// shape on purpose, and an integration test compares the flag-off response
// against a freshly-probed random-identifier path so the two cannot diverge.
// We normalize it onto ExitNotFound for the caller to catch, matching the pages
// probe's contract.
//
// Two things about this op that are easy to get wrong and are NOT symmetric
// with the pages op:
//
//   - An EMPTY STRING override is rejected (400), not treated as "clear it".
//     The service applies an override only when truthy, so "" would do nothing
//     while still changing the idempotency fingerprint — the same key would
//     then 409 against the omitted form of a request that meant the same thing.
//     Omit instead; clearing branding is the hub PATCH endpoint's job.
//   - CreatedIDs is EMPTY on a replay. (The wire key is `created_resource_ids`
//     — NOT `created_ids`. Reading the wrong key yields an empty map with no
//     error, which is indistinguishable from a replay; pinned by test.) The ids live on the durable application
//     record, and the backend deliberately will not reconstruct them from a
//     stored summary because that would be a guess. `Replayed` is how a caller
//     tells the two apart — do not read an empty CreatedIDs as "created
//     nothing".

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ErrHubOpAbsent marks the ONE condition that may fall back to the client-side
// pipeline: the op is not on this backend (dormant flag or a backend that
// predates it), signalled by the bare 405.
//
// It exists because the exit code cannot carry this distinction. The op ALSO
// answers 404 `template_not_found`, and both 404 and 405 derive ExitNotFound —
// so a caller that branched on the code alone would treat a genuinely unknown
// template as "no op here" and silently apply client-side. Callers must test
// errors.Is(err, ErrHubOpAbsent), never errs.CodeOf(err) == errs.ExitNotFound.
var ErrHubOpAbsent = errors.New("hub scaffold op not available on this backend")

// HubFromTemplateOverrides are the operator's presentation overrides — the
// CLI's flags, as JSON. Every field is optional and an omitted field means "the
// template decides".
//
// Pointer fields, not bare values, because absent and zero are different on the
// wire for the first three. Publish is a plain bool: the backend documents it
// as a tri-state that collapses (absent and false both mean "stays private"),
// and folds all four keys into the idempotency fingerprint unconditionally, so
// sending `publish: false` explicitly is the SAME request as omitting it.
type HubFromTemplateOverrides struct {
	LogoURL             *string
	FaviconURL          *string
	RegistrationEnabled *bool
	Publish             bool
}

// HubFromTemplateRequest carries the scaffold attributes plus the REQUIRED
// Idempotency-Key header value (1–255 chars; missing/empty/oversized is a 400
// `idempotency_key_invalid`).
type HubFromTemplateRequest struct {
	HubTemplateID  string
	Name           string
	Slug           string
	CatalogDigest  string
	Overrides      HubFromTemplateOverrides
	IdempotencyKey string
}

// HubFromTemplateSummaryRow is one resource the op considered. Action is
// "created" or "skipped"; Reason is set only on a skip.
type HubFromTemplateSummaryRow struct {
	Resource string
	Action   string
	Reason   string
}

// HubFromTemplateResult is what the op reports back.
//
// CreatedIDs is keyed by resource TYPE ("hubs", "spaces", "pages", "playlists",
// "discussions", "contact_attribute_definitions") and holds only what THIS run
// created — empty on a replay, by design. Summary is ordered and is present on
// a replay, which is what lets a caller report the outcome either way.
type HubFromTemplateResult struct {
	HubID      string
	Summary    []HubFromTemplateSummaryRow
	CreatedIDs map[string][]string
	Replayed   bool
}

// HubFromTemplate POSTs /api/teams/{team_id}/hubs/from-template.
//
// Returns errs.ExitNotFound when the op is absent (the 405 probe signal) so the
// caller can fall back to the client-side pipeline. Every other error is
// surfaced unchanged: the op EXISTS but something is wrong, and falling back
// client-side against an unhealthy or disagreeing backend just smears partial
// state — the same discipline the pages probe follows.
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
	// Always sent: it is a plain bool server-side and folds into the
	// fingerprint either way, so being explicit costs nothing and keeps the
	// emitted body a faithful record of what was asked for.
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
			// The dormant/absent signal, and the ONLY one. Tagged with the
			// ErrHubOpAbsent sentinel rather than left to its exit code: 404
			// `template_not_found` derives the same ExitNotFound, and falling
			// back on THAT would apply client-side while the backend is telling
			// us the template does not exist. The server's detail is preserved
			// in the chain for the error envelope.
			return HubFromTemplateResult{}, errs.Wrap(errs.ExitNotFound,
				fmt.Errorf("%w: %w", ErrHubOpAbsent, err))
		}
		return HubFromTemplateResult{}, err
	}
	if res == nil {
		return HubFromTemplateResult{}, nil
	}

	out := HubFromTemplateResult{CreatedIDs: map[string][]string{}}
	out.HubID, _ = res.Attributes["hub_id"].(string)
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
