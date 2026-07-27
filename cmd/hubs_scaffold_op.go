package cmd

// hubs_scaffold_op.go — the W2b backend-op branch of the pages step (MIO-2672
// Task 9, spec §0). stepPages PROBES the backend's one-step
// POST …/pages/scaffold-from-template op by simply calling it — the probe IS
// the real POST, never a separate capability/HEAD check — and a 404 (dormant
// feature flag or older backend) falls back to the client-side loop in
// hubs_scaffold_pages.go. This is the design's ONLY runtime branch: BOTH paths
// produce a real hub; a missing op is never an error.

import (
	"fmt"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// applyViaServerOp tries the backend op for the WHOLE pages[] plan. Returns
// (true, nil) when the op handled it (stepPages is done), (false, nil) when
// the op is absent — the 404 probe signal; the caller runs the client-side
// loop — and (false, err) for everything the op path must surface:
//
//   - ExitUsage (409/422/400): the likely cause is the backend's catalog pin
//     moving between our preflight resolve and this POST, so the request is
//     retried ONCE after a refetch — and only when the digest actually moved
//     (retryServerOpAfterRefetch);
//   - anything else (5xx, transport): the op EXISTS but the backend is
//     unhealthy — NEVER fall back client-side; a client-side apply against an
//     unhealthy backend just smears partial state.
func applyViaServerOp(sc *scaffoldContext) (bool, error) {
	req := client.ScaffoldFromTemplateRequest{
		HubTemplateID: sc.hubTmpl.ID,
		Name:          sc.hubName,
		Slug:          sc.hubSlug,
		CatalogDigest: sc.cat.Meta.Digest,
		// The deterministic idempotency key (64-char hex, within the op's
		// 255-char limit): a re-run of the same hub+template converges on the
		// backend's stored application instead of double-applying.
		IdempotencyKey: catalog.ApplicationID(sc.hubID, sc.hubTmpl.ID),
	}
	res, err := sc.cl.ScaffoldFromTemplate(sc.ctx, sc.teamID, sc.hubID, req)
	switch {
	case err == nil:
		recordServerOpResult(sc, res)
		return true, nil
	case errs.CodeOf(err) == errs.ExitNotFound:
		sc.notef("scaffold-from-template op not available — applying client-side")
		return false, nil
	case errs.CodeOf(err) == errs.ExitUsage:
		return retryServerOpAfterRefetch(sc, req, err)
	default:
		return false, err
	}
}

// retryServerOpAfterRefetch is the 409-refetch-once flow: re-resolve the
// catalog with the SAME options shape as the preflight (scaffoldResolveOptions
// — one construction site, no drift) and, ONLY if the digest actually moved,
// rebuild the plan from the fresh catalog (the shared preflight tail, with the
// FINAL hub vars — they are known post-create) and retry the op exactly once
// with the new digest. An unchanged digest, a failed refetch, or a second op
// rejection all surface the ORIGINAL error with guidance; the retry's own 404
// (freak case: the op disappeared between the two POSTs) falls back
// client-side like any other probe miss.
func retryServerOpAfterRefetch(sc *scaffoldContext, req client.ScaffoldFromTemplateRequest, opErr error) (bool, error) {
	rejected := func() error {
		return errs.Wrap(errs.CodeOf(opErr), fmt.Errorf(
			"scaffold-from-template rejected the request: %w (if this is a digest mismatch the backend pin may have moved — re-run; otherwise inspect the hub's pages)", opErr))
	}

	cat, src, rerr := catalog.Resolve(sc.ctx, scaffoldResolveOptions(sc, sc.notef))
	if rerr != nil {
		sc.notef("catalog refetch after the rejection failed: %v", rerr)
		return false, rejected()
	}
	if cat.Meta.Digest == sc.cat.Meta.Digest {
		// The pin did not move — the rejection was not our staleness. Surface it.
		return false, rejected()
	}

	// The pin moved under us: adopt the fresh catalog and re-run the preflight
	// tail (existence + invariants + plan + interpolation) against it, so a
	// retry never sends a digest for a catalog the plan was not built from —
	// and so the client-side loop, if the retry 404s, applies the NEW plan.
	sc.notef("backend catalog pin moved (revision %d → %d) — retrying the op with the fresh digest",
		sc.cat.Meta.Revision, cat.Meta.Revision)
	sc.cat, sc.catalogSource = cat, src
	if perr := scaffoldPlanFromCatalog(sc, cat, src, req.HubTemplateID, sc.hubName, sc.hubSlug); perr != nil {
		return false, perr
	}

	req.CatalogDigest = cat.Meta.Digest
	res, err := sc.cl.ScaffoldFromTemplate(sc.ctx, sc.teamID, sc.hubID, req)
	switch {
	case err == nil:
		recordServerOpResult(sc, res)
		return true, nil
	case errs.CodeOf(err) == errs.ExitNotFound:
		sc.notef("scaffold-from-template op not available — applying client-side")
		return false, nil
	default:
		// ONE retry ever. A second rejection (any kind) surfaces the ORIGINAL
		// error with guidance; the retry's own failure is noted, not swallowed.
		sc.notef("retry with the refetched digest also failed: %v", err)
		return false, rejected()
	}
}

// recordServerOpResult translates the op's success into the same context state
// + operator notes the client-side loop leaves behind. published_revision is a
// PUBLISHED revision, NOT a draft version — sc.homeDraftVersion stays 0, and
// nothing downstream may conflate the two (printScaffoldSummary reads neither
// field, so empty/zero keeps its output sensible).
//
// Task-4 review note: a 201 whose body violates the contract can yield empty
// Pages — that must NOT be read as "nothing created". The hub IS scaffolded
// (the op returned success), so a missing/homepage-less listing degrades to a
// WARNING note and an empty sc.homePageID, never a failure.
func recordServerOpResult(sc *scaffoldContext, res client.ScaffoldFromTemplateResult) {
	if len(res.Pages) == 0 {
		sc.notef("scaffold-from-template op succeeded but returned no page listing — the pages were applied server-side; homepage id unknown to this run")
		return
	}
	for _, p := range res.Pages {
		sc.notef("page %q applied by backend op (page %s, published revision %d)", p.Role, p.PageID, p.PublishedRevision)
		if p.Role == "homepage" && sc.homePageID == "" {
			sc.homePageID = p.PageID
		}
	}
	if sc.homePageID == "" {
		sc.notef("scaffold-from-template op returned no homepage entry — homepage id unknown to this run")
	}
}
