package cmd

// hubs_scaffold_op.go — the W2b backend-op branch of the pages step (MIO-2672
// Task 9, spec §0). stepPages PROBES the backend's one-step
// POST …/pages/scaffold-from-template op by simply calling it — the probe IS
// the real POST, never a separate capability/HEAD check — and a 404/405 (op
// absent: dormant flag, or the path shadowed by a GET route on older
// backends; the client normalizes both onto ExitNotFound) falls back to the
// client-side loop in hubs_scaffold_pages.go. This is the design's ONLY
// runtime branch: BOTH paths produce a real hub; a missing op is never an
// error.

import (
	"fmt"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// opAbsentNote is the operator note both probe misses share — the initial
// 404/405 and the freak retry miss (op disappeared between the two POSTs).
const opAbsentNote = "scaffold-from-template op not available — applying client-side"

// ---- what the server ops do not apply (MIO-3065) ------------------------------
//
// The ops are a SECOND implementation of this pipeline, in another repo, and
// they were written against the vocabulary that existed then. Three
// hubTemplates[] declarations are ignored by them today (verified on mio-backend
// origin/main, 2026-08-10):
//
//   - spaces[].icon           — app/hub_scaffold/service.py `_TemplateSpace`
//                               models name/slug/description/access_level/
//                               posting_permission and no icon;
//   - playlists[].documents   — its `_TemplatePlaylist` models key/title/
//                               visibility/file_ids and no documents;
//   - a playlist dataSource   — `dataSource` appears nowhere in app/ outside the
//     `key`                     vendored catalog itself, on either op path.
//
// Taking an op that drops what the template asked for produces a hub that looks
// built and is not — the one outcome hubOpUnsupportedFlags already calls worse
// than not using the op. So a template declaring any of them takes the
// client-side path, ANNOUNCED, until mio-backend reaches parity (MIO-3080).
//
// Both functions are deliberately data-driven off the resolved template rather
// than keyed on a template id: when a template stops declaring these, or the ops
// start applying them and these checks are deleted, nothing about `starter` is
// special-cased anywhere.

// playlistBindingKeys returns the playlist dataSource keys the planned pages
// declare — the fill contract the CLIENT resolves after stepPlaylists. Any at
// all means a server-applied page would be written with the catalog's empty id
// and compile to a section bound to nothing.
func playlistBindingKeys(sc *scaffoldContext) []string {
	if sc.pagePlan == nil {
		return nil
	}
	var keys []string
	seen := map[string]bool{}
	for _, pp := range sc.pagePlan.pages {
		for _, k := range catalog.PlaylistDataSourceKeys(pp.rawTree) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// hubOpUnappliedVocabulary lists, in template order, every declaration the
// WHOLE-HUB op would drop. Empty means the op can express this template.
//
// The pages op gets its own, narrower check (see stepPages): by the time it is
// probed the client-side spaces and playlists steps have already run, so the
// only thing still at stake there is the fill contract.
func hubOpUnappliedVocabulary(sc *scaffoldContext) []string {
	var out []string
	for _, s := range sc.hubTmpl.Spaces {
		if s.Icon != "" {
			out = append(out, "spaces[].icon")
			break
		}
	}
	for _, p := range sc.hubTmpl.Playlists {
		if len(p.Documents) > 0 {
			out = append(out, "playlists[].documents")
			break
		}
	}
	if len(playlistBindingKeys(sc)) > 0 {
		out = append(out, "a playlist dataSource key")
	}
	return out
}

// applyViaServerOp tries the backend op for the WHOLE pages[] plan. Returns
// (true, nil) when the op handled it (stepPages is done), (false, nil) when
// the op is absent — the 404/405 probe signal, surfaced by the client as
// ExitNotFound; the caller runs the client-side loop — and (false, err) for
// everything the op path must surface:
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
		sc.notef(opAbsentNote)
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
// rejection all surface the ORIGINAL error with guidance; a plan-rebuild
// failure against the fresh catalog surfaces the REBUILD error instead (it is
// the more actionable one: the new catalog is what the retry cannot proceed
// against); the retry's own 404/405 (freak case: the op disappeared between
// the two POSTs) falls back client-side like any other probe miss.
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

	// The pin moved under us: re-run the preflight tail (existence + invariants
	// + plan + interpolation) against the fresh catalog, so a retry never sends
	// a digest for a catalog the plan was not built from — and so the
	// client-side loop, if the retry 404s, applies the NEW plan. The context
	// adopts the fresh catalog only AFTER the rebuild succeeds (no half-updated
	// context on a rebuild failure).
	sc.notef("backend catalog pin moved (revision %d → %d) — retrying the op with the fresh digest",
		sc.cat.Meta.Revision, cat.Meta.Revision)
	if perr := rebuildScaffoldPlan(sc, cat, src, req.HubTemplateID, sc.hubName, sc.hubSlug); perr != nil {
		return false, perr
	}
	sc.cat = cat

	req.CatalogDigest = cat.Meta.Digest
	res, err := sc.cl.ScaffoldFromTemplate(sc.ctx, sc.teamID, sc.hubID, req)
	switch {
	case err == nil:
		recordServerOpResult(sc, res)
		return true, nil
	case errs.CodeOf(err) == errs.ExitNotFound:
		sc.notef(opAbsentNote)
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
		// Record the id under the template SLUG (MIO-2574) so both apply
		// branches key the machine-readable result the same way — the
		// client-side loop is slug-keyed.
		//
		// MIO-2749: the op now returns the slug itself, so take it directly.
		// templateSlugForRole is only the fallback for a backend that predates
		// that change and still sends role alone; it cannot resolve a role two
		// template entries share, so about/faq stay unrecorded there.
		slug := p.Slug
		if slug == "" {
			slug = templateSlugForRole(sc, p.Role)
		}
		sc.notef("page %q applied by backend op (page %s, published revision %d)", p.Role, p.PageID, p.PublishedRevision)
		sc.recordPageID(slug, p.PageID)
		if p.Role == "homepage" && sc.homePageID == "" {
			sc.homePageID = p.PageID
		}
	}
	if sc.homePageID == "" {
		sc.notef("scaffold-from-template op returned no homepage entry — homepage id unknown to this run")
	}
}

// templateSlugForRole maps a role from the op's page listing back onto the
// template pages[] slug that declared it, and does so ONLY when the role is
// UNAMBIGUOUS — exactly one template entry claims it.
//
// LEGACY-BACKEND FALLBACK ONLY (MIO-2749). A current backend returns the slug
// on every entry and recordServerOpResult uses that directly; this runs only
// when the response carries no slug, i.e. a backend older than mio-backend
// #631. Keep it: the CLI talks to whatever backend it is pointed at, and this
// file already tolerates version skew elsewhere (the op-absent 404/405 probe).
//
// The ambiguity is real, not theoretical: the shipped community template gives
// its secondary pages the SAME role ("secondary" for both about and faq), so a
// first-match mapping would attribute one page's id to the other page's slug —
// a wrong id is far worse than a missing one for a caller that acts on it. An
// unmatched or ambiguous role yields "" and is simply not recorded (recordPageID
// drops it), leaving that entry's page_id null in the result. The homepage —
// the id callers actually chase — carries a unique role in every template we
// serve, and has its own role-keyed fallback in scaffoldHomepageID besides.
func templateSlugForRole(sc *scaffoldContext, role string) string {
	if role == "" {
		return ""
	}
	slug, matches := "", 0
	for _, p := range sc.hubTmpl.Pages {
		if p.Role == role {
			slug, matches = p.Slug, matches+1
		}
	}
	if matches != 1 {
		return ""
	}
	return slug
}
