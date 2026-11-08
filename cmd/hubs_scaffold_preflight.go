package cmd

import (
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type plannedPage struct {
	ref     catalog.PageRef
	rawTree map[string]any
}

type scaffoldPlan struct {
	pages []plannedPage
}

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

func wrapCatalogResolveErr(err error, fromOverrideFile bool) error {
	if code := errs.CodeOf(err); code != errs.ExitGeneric {
		return errs.Wrap(code, err)
	}
	if fromOverrideFile {
		return errs.Wrap(errs.ExitUsage, err)
	}
	return errs.Wrap(errs.ExitGeneric, err)
}

func errNoHubTemplates(cat *catalog.Catalog, src catalog.Source, catalogFlagHint bool) error {
	hint := ""
	if catalogFlagHint {
		hint = "; pass --catalog <file> to test against a local artifact"
	}
	return errs.New(errs.ExitUsage,
		"the %s catalog (version %s, revision %d) contains no hub templates — it predates the 2.1 catalog (MIO-2666/W2a pin)%s",
		src, cat.Meta.CatalogVersion, cat.Meta.Revision, hint)
}

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

func scaffoldPreflight(cmd *cobra.Command, sc *scaffoldContext, templateID string) error {
	if utf8.RuneCountInString(sc.nameOverride) > catalog.MaxHubNameCP {
		return errs.New(errs.ExitUsage, "--name exceeds the %d-character limit", catalog.MaxHubNameCP)
	}

	cat, src, err := catalog.Resolve(sc.ctx, scaffoldResolveOptions(sc, catalogWarnf(cmd)))
	if err != nil {
		return wrapCatalogResolveErr(err, sc.catalogOverride != "")
	}
	printCatalogProvenance(cmd, src, cat)
	sc.cat = cat

	prelimName, prelimSlug := sc.nameOverride, sc.slugOverride
	if sc.hubID != "" {
		prelimName, prelimSlug = sc.hubName, sc.hubSlug
	}
	if rerr := rebuildScaffoldPlan(sc, cat, src, templateID, prelimName, prelimSlug); rerr != nil {
		return rerr
	}

	if _, perr := resolveTemplatePolicies(&sc.hubTmpl); perr != nil {
		return perr
	}

	if sc.resume {
		return preflightResumePages(sc)
	}
	return nil
}
