package cmd

// hubs_scaffold_preflight.go — the up-front, WRITE-FREE stage of the scaffold
// pipeline (MIO-2573 §5, invariant §2.1.3). The CLI holds no templates (spec
// §0): the catalog is fetched LIVE from the very backend the hub is being
// created on (--catalog <file> is the only escape hatch, and it fails closed on
// a digest mismatch because this is a mutating command). Everything validatable
// without writing — hub-template existence + invariants, the 255-cp name bound,
// token resolution and caps over a PRELIMINARY interpolation of the whole plan —
// runs here, before stepHub creates anything.

import (
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// plannedPage is one pages[] entry of the scaffold plan: the catalog PageRef it
// came from plus its instantiated-ONCE (fresh UUIDs), NOT-yet-interpolated node
// tree. Interpolation happens twice on CLONES of rawTree — preliminarily here
// (validation only) and finally at apply time with the post-create hub vars —
// so rawTree itself is never mutated and a resume re-run sees pristine trees.
type plannedPage struct {
	ref     catalog.PageRef
	rawTree map[string]any
}

// scaffoldPlan is the instantiated page plan the preflight leaves on the
// context (sc.pagePlan) for stepPages' pages[] apply loop.
type scaffoldPlan struct {
	pages []plannedPage
}

// buildScaffoldPlan instantiates every hub-template pages[] ref into a
// plannedPage. A ref whose pageTemplate is missing from the catalog, or names a
// section template, is ExitUsage — defense-in-depth under HubTemplate.Validate,
// kept here because this function is also re-run standalone after a 409
// re-resolve (Task 9), where the catalog may have changed under us.
func buildScaffoldPlan(cat *catalog.Catalog, ht catalog.HubTemplate) (*scaffoldPlan, error) {
	plan := &scaffoldPlan{}
	for _, ref := range ht.Pages {
		tmpl, ok := cat.TemplateByID(ref.PageTemplate)
		if !ok {
			return nil, errs.New(errs.ExitUsage,
				"hub template %q: page %q references pageTemplate %q, which is not in the catalog", ht.ID, ref.Slug, ref.PageTemplate)
		}
		if !tmpl.IsPage {
			return nil, errs.New(errs.ExitUsage,
				"hub template %q: page %q pageTemplate %q is a section template, not a page template", ht.ID, ref.Slug, ref.PageTemplate)
		}
		node, err := catalog.InstantiateTemplate(tmpl, "", catalog.NewUUIDv7Gen())
		if err != nil {
			return nil, errs.Wrap(errs.ExitGeneric, err)
		}
		plan.pages = append(plan.pages, plannedPage{ref: ref, rawTree: node})
	}
	return plan, nil
}

// validatePlanInterpolation runs the PRELIMINARY interpolation pass over the
// whole plan — every page title, every instantiated tree, and the template's
// navigation labels — purely for validation (unknown tokens, post-substitution
// caps). It operates on CLONES only and never mutates ht or plan: the apply
// steps re-interpolate with the FINAL post-create hub name/slug.
func validatePlanInterpolation(ht catalog.HubTemplate, plan *scaffoldPlan, hubName, hubSlug string) error {
	for _, pp := range plan.pages {
		if _, err := catalog.InterpolateTitle(pp.ref.Title, hubName, hubSlug); err != nil {
			return errs.Wrap(errs.ExitUsage, err)
		}
		if err := catalog.InterpolateTreeValues(catalog.CloneNode(pp.rawTree), hubName, hubSlug); err != nil {
			return errs.Wrap(errs.ExitUsage, err)
		}
	}
	if ht.Navigation != nil {
		if err := catalog.InterpolateNavigation(catalog.CloneNode(ht.Navigation), hubName, hubSlug); err != nil {
			return errs.Wrap(errs.ExitUsage, err)
		}
	}
	return nil
}

// wrapCatalogResolveErr maps a catalog.Resolve failure onto the right exit
// code. catalog.Resolve preserves the client's typed HTTP errors through %w
// (401/403 → ExitAuth, 404 → ExitNotFound, 429 → ExitRateLimited, 5xx →
// ExitServer), so a typed code found in the chain WINS — a blanket
// ExitGeneric wrap would shadow it, because CodeOf reads the OUTERMOST
// CLIError. With no typed code in the chain, a failure loading a
// user-supplied --catalog file (read/parse/digest mismatch) is usage-style;
// anything else stays generic.
func wrapCatalogResolveErr(err error, fromOverrideFile bool) error {
	if code := errs.CodeOf(err); code != errs.ExitGeneric {
		return errs.Wrap(code, err)
	}
	if fromOverrideFile {
		return errs.Wrap(errs.ExitUsage, err)
	}
	return errs.Wrap(errs.ExitGeneric, err)
}

// errNoHubTemplates is the shared pin-hint error for a catalog with no
// hubTemplates[] — the catalog predates the 2.1 artifact (MIO-2666/W2a). Used
// by both scaffoldPreflight and `hubs templates` so the explanation cannot
// drift between the two surfaces. src attributes the catalog correctly: a
// resolve can also answer from a 304-validated cache or a --catalog override,
// and the message must not blame "the backend's catalog" for a local copy.
// catalogFlagHint appends the --catalog escape-hatch pointer — the scaffold
// sets it; `hubs templates` has no such flag, so its message must not
// advertise one.
func errNoHubTemplates(cat *catalog.Catalog, src catalog.Source, catalogFlagHint bool) error {
	hint := ""
	if catalogFlagHint {
		hint = "; pass --catalog <file> to test against a local artifact"
	}
	return errs.New(errs.ExitUsage,
		"the %s catalog (version %s, revision %d) contains no hub templates — it predates the 2.1 catalog (MIO-2666/W2a pin)%s",
		src, cat.Meta.CatalogVersion, cat.Meta.Revision, hint)
}

// scaffoldResolveOptions builds the ResolveOptions EVERY mutating scaffold
// resolve shares — the preflight and the 409-refetch in applyViaServerOp
// (hubs_scaffold_op.go) construct them from THIS one place, so the two shapes
// cannot drift: same Mutating fail-closed semantics, same origin-scoped cache
// dir, same override/fetcher exclusivity (--catalog is exclusive, so no
// catalog GET may fire beside it). warnf is the only per-site input (the
// preflight has a cobra stderr; the op retry only has sc.notef).
func scaffoldResolveOptions(sc *scaffoldContext, warnf func(string, ...any)) catalog.ResolveOptions {
	opts := catalog.ResolveOptions{
		OverrideFile: sc.catalogOverride,
		Mutating:     true,
		CacheDir:     catalogCacheDirFor(sc.cl.BaseURL()),
		Warnf:        warnf,
	}
	if sc.catalogOverride == "" {
		opts.Fetcher = catalogFetcher{c: sc.cl}
	}
	return opts
}

// rebuildScaffoldPlan is the preflight TAIL (steps 3-5 below), and it MUTATES
// sc: source templateID from cat — existence with the available ids, or the
// no-hubTemplates pin hint — check its invariants against the SAME catalog,
// instantiate the page plan, and validate its interpolation with (hubName,
// hubSlug). On success sc.hubTmpl + sc.pagePlan are updated. Shared by
// scaffoldPreflight (PRELIMINARY vars) and the 409-refetch retry in
// applyViaServerOp (FINAL vars, known post-create) so the re-resolve reruns
// exactly the preflight's checks — one call, never a copy.
func rebuildScaffoldPlan(sc *scaffoldContext, cat *catalog.Catalog, src catalog.Source, templateID, hubName, hubSlug string) error {
	ht, ok := cat.HubTemplateByID(templateID)
	if !ok {
		if len(cat.HubTemplates) == 0 {
			return errNoHubTemplates(cat, src, true)
		}
		return errs.New(errs.ExitUsage,
			"hub template %q is not in the catalog — available: %s", templateID, strings.Join(cat.HubTemplateIDs(), ", "))
	}
	if verr := ht.Validate(cat); verr != nil {
		return errs.Wrap(errs.ExitUsage, verr)
	}
	sc.hubTmpl = ht

	plan, perr := buildScaffoldPlan(cat, ht)
	if perr != nil {
		return perr
	}
	sc.pagePlan = plan
	return validatePlanInterpolation(ht, plan, hubName, hubSlug)
}

// scaffoldPreflight runs every write-free check, in cheapest-first order:
//
//  1. the --name 255-code-point bound (no HTTP before this);
//  2. catalog resolution — LIVE from the target backend (Mutating: fail closed,
//     never a stale cache/vendored fallback), or the --catalog override;
//  3. hub-template existence (with the available ids, or the no-hubTemplates
//     pin hint);
//  4. hub-template invariants (Validate against the same catalog);
//  5. the instantiated page plan + a preliminary interpolation of the whole
//     plan;
//  6. the policies block — key, per-field JSON types, and the per-policy
//     `enabled` collapse (MIO-2567; Validate covers only the field NAMES).
//
// On success it leaves sc.cat / sc.hubTmpl / sc.pagePlan
// populated for the apply pipeline.
func scaffoldPreflight(cmd *cobra.Command, sc *scaffoldContext, templateID string) error {
	// 1. Name bound (VARCHAR(255) on the hub title column): fail before any HTTP.
	if utf8.RuneCountInString(sc.nameOverride) > catalog.MaxHubNameCP {
		return errs.New(errs.ExitUsage, "--name exceeds the %d-character limit", catalog.MaxHubNameCP)
	}

	// 2. Resolve the catalog. Mutating: this resolve drives writes, so it fails
	// closed — no stale-cache or vendored fallback — and a digest-mismatched
	// --catalog override is rejected (see scaffoldResolveOptions).
	cat, src, err := catalog.Resolve(sc.ctx, scaffoldResolveOptions(sc, catalogWarnf(cmd)))
	if err != nil {
		return wrapCatalogResolveErr(err, sc.catalogOverride != "")
	}
	printCatalogProvenance(cmd, src, cat)
	sc.cat = cat

	// 3-5. Template existence + invariants + page plan + a PRELIMINARY
	// interpolation of the whole plan. Preliminary vars: resume mode already
	// holds the hub's ACTUAL name/slug (fetched during resolution); create mode
	// uses the --name/--slug intent. These results are validation-only — the
	// pages stage re-interpolates with the FINAL post-create values.
	prelimName, prelimSlug := sc.nameOverride, sc.slugOverride
	if sc.hubID != "" {
		prelimName, prelimSlug = sc.hubName, sc.hubSlug
	}
	if rerr := rebuildScaffoldPlan(sc, cat, src, templateID, prelimName, prelimSlug); rerr != nil {
		return rerr
	}

	// 6. The policies block — the policy KEY, each field's JSON type, and the
	//    per-policy `enabled` collapse. Pure functions of the template, like every
	//    check above, and this is the last moment they are free: leaving them in
	//    stepPolicies (pipeline stage 5 of 9) meant a contradictory template exited
	//    2 only AFTER the hub, its blobs, its spaces and its onboarding defs were
	//    written and could not be rolled back (MIO-2567 review). HubTemplate.Validate
	//    covers only the field NAMES, so these two classes are caught nowhere else.
	//
	//    DELIBERATELY NOT in rebuildScaffoldPlan, beside Validate, even though that
	//    is where it reads most naturally: rebuildScaffoldPlan has a SECOND caller,
	//    retryServerOpAfterRefetch (hubs_scaffold_op.go), which re-resolves the
	//    catalog at stage 7 after a backend-op 409 — with the hub, blobs, spaces,
	//    onboarding, policies and playlists all already written. Validating policies
	//    there would abort a pages retry over a policies block this run has already
	//    applied successfully, recreating the exact unrollbackable failure this move
	//    exists to prevent. The retry needs the page plan rebuilt, nothing else.
	//
	//    Discarded on success: stepPolicies re-resolves at apply time (cheap,
	//    deterministic) so the step stays runnable without a preflight.
	_, perr := resolveTemplatePolicies(&sc.hubTmpl)
	return perr
}
