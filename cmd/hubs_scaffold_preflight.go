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
	"fmt"
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
// context for the apply steps (today the homepage shim; Task 7's pages[] loop).
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

// errNoHubTemplates is the shared pin-hint error for a catalog with no
// hubTemplates[] — the backend predates the 2.1 artifact (MIO-2666/W2a). Used
// by both scaffoldPreflight and `hubs templates` so the explanation cannot
// drift between the two surfaces. catalogFlagHint appends the --catalog
// escape-hatch pointer — the scaffold sets it; `hubs templates` has no such
// flag, so its message must not advertise one.
func errNoHubTemplates(cat *catalog.Catalog, catalogFlagHint bool) error {
	hint := ""
	if catalogFlagHint {
		hint = "; pass --catalog <file> to test against a local artifact"
	}
	return errs.New(errs.ExitUsage,
		"the backend's catalog (version %s, revision %d) contains no hub templates — it predates the 2.1 catalog (MIO-2666/W2a pin)%s",
		cat.Meta.CatalogVersion, cat.Meta.Revision, hint)
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
//     plan.
//
// On success it leaves sc.cat / sc.catalogSource / sc.hubTmpl / sc.plan2
// populated for the apply pipeline.
func scaffoldPreflight(cmd *cobra.Command, sc *scaffoldContext, templateID string) error {
	// 1. Name bound (VARCHAR(255) on the hub title column): fail before any HTTP.
	if utf8.RuneCountInString(sc.nameOverride) > catalog.MaxHubNameCP {
		return errs.New(errs.ExitUsage, "--name exceeds the %d-character limit", catalog.MaxHubNameCP)
	}

	// 2. Resolve the catalog. Mutating: this resolve drives writes, so it fails
	// closed — no stale-cache or vendored fallback — and a digest-mismatched
	// --catalog override is rejected. The Fetcher is wired ONLY when no override
	// is given: --catalog is exclusive, so no catalog GET may fire beside it.
	opts := catalog.ResolveOptions{
		OverrideFile: sc.catalogOverride,
		Mutating:     true,
		CacheDir:     catalogCacheDirFor(sc.cl.BaseURL()),
		Warnf: func(format string, a ...any) {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
		},
	}
	if sc.catalogOverride == "" {
		opts.Fetcher = catalogFetcher{c: sc.cl}
	}
	cat, src, err := catalog.Resolve(sc.ctx, opts)
	if err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "catalog: %s (version %s)\n", src, cat.Meta.CatalogVersion)
	sc.cat, sc.catalogSource = cat, src

	// 3. Hub-template existence.
	ht, ok := cat.HubTemplateByID(templateID)
	if !ok {
		if len(cat.HubTemplates) == 0 {
			return errNoHubTemplates(cat, true)
		}
		return errs.New(errs.ExitUsage,
			"hub template %q is not in the catalog — available: %s", templateID, strings.Join(cat.HubTemplateIDs(), ", "))
	}

	// 4. Hub-template invariants, against the SAME catalog the pages will be
	// instantiated from.
	if verr := ht.Validate(cat); verr != nil {
		return errs.Wrap(errs.ExitUsage, verr)
	}
	sc.hubTmpl = ht

	// 5. Instantiate the page plan once, then preliminarily interpolate the whole
	// plan. Preliminary vars: resume mode already holds the hub's ACTUAL
	// name/slug (fetched during resolution); create mode uses the --name/--slug
	// intent. These results are validation-only — the pages stage re-interpolates
	// with the FINAL post-create values.
	plan, perr := buildScaffoldPlan(cat, ht)
	if perr != nil {
		return perr
	}
	sc.plan2 = plan
	prelimName, prelimSlug := sc.nameOverride, sc.slugOverride
	if sc.hubID != "" {
		prelimName, prelimSlug = sc.hubName, sc.hubSlug
	}
	return validatePlanInterpolation(ht, sc.plan2, prelimName, prelimSlug)
}
