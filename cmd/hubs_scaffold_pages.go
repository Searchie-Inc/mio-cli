package cmd

// hubs_scaffold_pages.go — the general pages[] apply loop of the scaffold
// pipeline (MIO-2672 Task 7), replacing the single-homepage shim. Every page
// of the hub template is applied CLIENT-SIDE: created with a §5.1 provenance
// marker ("pending"), its finally-interpolated draft tree PUT + published, and
// the marker flipped to "applied" (with the applied tree digest + draft
// version). This is the client-side FALLBACK path's core — the W2b backend-op
// probe (Task 9) will prefer the server-side op where it exists.
//
// Crash RECOVERY (resume onto a half-applied hub) is Task 8: in THIS task an
// existing page at a manifest slug is a fail-safe conflict ERROR, never an
// overwrite — so the loop only ever writes pages it created itself.

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// stepPages iterates the preflight page plan IN PLAN ORDER and applies each
// page client-side. The homepage is simply the entry whose ref.IsHomepage is
// true — no positional assumption (it happens to come first in the community
// template). In dry-run each page records its own plan entry under the shared
// "pages" step name, so the plan names every page it would create.
func stepPages(sc *scaffoldContext, _ *catalog.HubTemplate) error {
	if sc.pagePlan == nil || len(sc.pagePlan.pages) == 0 {
		// scaffoldPreflight always leaves a plan on a RESOLVED template
		// (Validate requires pages); this guards a hand-built in-memory context
		// so the step is a clean no-op, never a panic.
		return sc.step("pages", "no pages in template", func() error { return nil })
	}
	for i := range sc.pagePlan.pages {
		pp := sc.pagePlan.pages[i]
		home := ""
		if pp.ref.IsHomepage {
			home = ", homepage"
		}
		detail := fmt.Sprintf("create page %q (template %s%s)", pp.ref.Slug, pp.ref.PageTemplate, home)
		if err := sc.step("pages", detail, func() error { return applyPageClientSide(sc, pp) }); err != nil {
			return err
		}
	}
	return nil
}

// applyPageClientSide applies ONE planned page: final interpolation → slug
// conflict check → create (marker "pending") → tree PUT → publish → marker
// PATCH ("applied").
func applyPageClientSide(sc *scaffoldContext, pp plannedPage) error {
	// 1. FINAL interpolation. The preflight validated the plan with the
	// PRELIMINARY vars (--name/--slug intent); these are the post-create
	// ACTUALS (the server-observed hub title + slug), so a bad token or a
	// post-substitution cap fails LOUD here, before any HTTP for this page —
	// never creating an orphan. The tree is interpolated on a CLONE of the
	// plan's instantiated-once rawTree, so the plan stays pristine.
	title, terr := catalog.InterpolateTitle(pp.ref.Title, sc.hubName, sc.hubSlug)
	if terr != nil {
		return errs.Wrap(errs.ExitUsage, terr)
	}
	tree := catalog.CloneNode(pp.rawTree)
	if ierr := catalog.InterpolateTreeValues(tree, sc.hubName, sc.hubSlug); ierr != nil {
		return errs.Wrap(errs.ExitUsage, ierr)
	}
	treeObj := map[string]any{"root": tree}

	// 2. Existence check by slug — EXHAUSTIVE page list (shared walker). Task 7
	// is fail-safe: a page already at a manifest slug is a CONFLICT, never an
	// overwrite. Task 8 replaces this error with the decideRecovery dispatch
	// (resume semantics onto a half-applied hub).
	if _, found, lerr := sc.existingPageBySlug(pp.ref.Slug); lerr != nil {
		return lerr
	} else if found {
		return errs.New(errs.ExitUsage,
			"page %q already exists on hub %s — refusing to overwrite (crash recovery lands with MIO-2672 Task 8)",
			pp.ref.Slug, sc.hubID)
	}

	// 3. Create the page carrying the §5.1 provenance marker in "pending"
	// state, so a crash between this create and the applied-PATCH below leaves
	// a detectable half-applied page (Task 8's recovery keys on it).
	// buildPageAttrs is the shared `pages create` builder, so the scaffold gets
	// the same privacy-enum validation the command does.
	attrs, berr := buildPageAttrs(pageInputFor(pp.ref, title, templateMarker(sc, pp.ref, "pending")))
	if berr != nil {
		return berr
	}
	res, cerr := sc.cl.Create(sc.ctx, pagesPath(sc.teamID, sc.hubID, ""), attrs)
	if cerr != nil {
		return cerr
	}
	pageID := res.ID

	// 4. Set the draft tree with the first-set OCC sentinel If-Match: 0 — the
	// page was created moments ago by THIS run (step 2 guarantees no reuse), so
	// it can never have a prior draft. Mirrors `pages tree set`.
	tres, perr := sc.cl.ActionWithHeaders(
		sc.ctx, client.StyleEnvelope, "PUT",
		pagesTreePath(sc.teamID, sc.hubID, pageID),
		map[string]any{"tree": treeObj},
		map[string]string{"If-Match": "0"},
	)
	if perr != nil {
		return perr
	}
	// Capture the new draft_version the PUT returns (the OCC token the publish
	// below uses); a bodyless response leaves the first-set sentinel 0.
	dv := 0
	if tres != nil {
		if v, ok := attrInt(tres.Attributes["draft_version"]); ok {
			dv = v
		}
	}
	if pp.ref.IsHomepage {
		// Summary + W0 publish-guard compatibility: the homepage entry's id and
		// draft version live on the context.
		sc.homePageID, sc.homeDraftVersion = pageID, dv
	}

	// 5. Publish the draft (MIO-2636): the backend serves NO resolved tree
	// until a draft is published, so without this the page renders the
	// null-tree "No content available" fallback. If-Match = the draft_version
	// the PUT just returned (mirrors `mio pages publish`); no body.
	if _, err := sc.cl.ActionWithHeaders(
		sc.ctx, client.StyleEnvelope, "POST",
		pagesPath(sc.teamID, sc.hubID, pageID)+"/publish",
		nil,
		map[string]string{"If-Match": strconv.Itoa(dv)},
	); err != nil {
		return err
	}

	// 6. Flip the marker to "applied". The digest is computed over the EXACT
	// tree body that was PUT (treeObj — the same value, canonicalized), so a
	// re-run can byte-compare what it would write against what was written.
	// NOTE: this PATCH replaces the WHOLE meta blob — that is safe because we
	// own meta between the create (which sent only the marker) and here;
	// competing writers land in Task 8's conflict path.
	digest, derr := catalog.TreeDigest(treeObj)
	if derr != nil {
		return errs.Wrap(errs.ExitGeneric, derr)
	}
	if _, err := sc.cl.Update(sc.ctx, pagesPath(sc.teamID, sc.hubID, pageID),
		map[string]any{"meta": templateMarkerApplied(sc, pp.ref, digest, dv)}); err != nil {
		return err
	}
	return nil
}

// pageInputFor maps a catalog PageRef (plus the interpolated title and the
// provenance marker) onto the shared `pages create` builder input. IsHome is
// set ONLY for the homepage entry, so no other page ever claims is_homepage;
// Privacy is passed through only when the ref carries one (buildPageAttrs
// validates the enum).
func pageInputFor(ref catalog.PageRef, title string, marker map[string]any) PageInput {
	slug := ref.Slug
	in := PageInput{Title: &title, Slug: &slug, Meta: marker}
	if ref.IsHomepage {
		isHome := true
		in.IsHome = &isHome
	}
	if ref.Privacy != "" {
		priv := ref.Privacy
		in.Privacy = &priv
	}
	return in
}

// templateMarker builds the §5.1 provenance marker written into page.meta at
// create ("pending") — the SAME shape the backend op writes, so either path's
// pages are recognizable by the other (camelCase keys inside the marker).
func templateMarker(sc *scaffoldContext, ref catalog.PageRef, state string) map[string]any {
	return map[string]any{
		"template_provenance": map[string]any{
			"applicationId":   catalog.ApplicationID(sc.hubID, sc.hubTmpl.ID),
			"hubTemplateId":   sc.hubTmpl.ID,
			"pageTemplateId":  ref.PageTemplate,
			"catalogRevision": sc.cat.Meta.Revision,
			"provenanceState": state,
		},
	}
}

// templateMarkerApplied is the "applied" §5.1 marker: templateMarker plus the
// digest of the exact tree body that was PUT and the draft_version that PUT
// returned — what a re-run (Task 8) compares to decide converged vs drifted.
func templateMarkerApplied(sc *scaffoldContext, ref catalog.PageRef, treeDigest string, draftVersion int) map[string]any {
	m := templateMarker(sc, ref, "applied")
	tp := m["template_provenance"].(map[string]any)
	tp["appliedTreeDigest"] = treeDigest
	tp["appliedDraftVersion"] = draftVersion
	return m
}

// findHubPage walks the hub's page list EXHAUSTIVELY — same
// meta.page.next_cursor convention + seen-cursor/maxPages stall guard as the
// other steps' lookups (see existingSpaceSlugs) — and returns the first page
// matching match. Every page lookup ("by slug" here; "by is_homepage" returns
// with Task 8's decideRecovery) shares this single walker so the cursor loop
// is never duplicated.
func (sc *scaffoldContext) findHubPage(match func(r client.Resource) bool) (id string, found bool, err error) {
	query := url.Values{}
	seen := map[string]bool{}
	const maxPages = 1000 // hard ceiling; a hub never has this many pages
	for page := 0; page < maxPages; page++ {
		col, lerr := sc.cl.List(sc.ctx, pagesPath(sc.teamID, sc.hubID, ""), query)
		if lerr != nil {
			return "", false, lerr
		}
		for _, r := range col.Data {
			if match(r) {
				return r.ID, true, nil
			}
		}
		next := nextPageCursor(col)
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		query = url.Values{}
		query.Set("page[after]", next)
	}
	return "", false, nil
}

// existingPageBySlug reports whether the hub already has a page at slug — the
// Task-7 fail-safe conflict check (and Task 8's recovery lookup).
func (sc *scaffoldContext) existingPageBySlug(slug string) (string, bool, error) {
	return sc.findHubPage(func(r client.Resource) bool {
		s, _ := r.Attributes["slug"].(string)
		return s == slug
	})
}

// homepageDraftVersion reads an existing page's current draft_version via a
// tree-get (RetrieveWithQuery on the author draft, mirroring `pages tree get`)
// — the authoritative OCC source the rest of the codebase uses, since the page
// LIST does not surface draft_version. A page that has never had a draft 404s
// on tree-get; that is TOLERATED as draft_version 0 (the first-set sentinel).
// Any other error propagates. Used by crash recovery (Task 8) — stepPages'
// fresh creates never need it (their If-Match is always the sentinel 0).
func (sc *scaffoldContext) homepageDraftVersion(pageID string) (int, error) {
	q := url.Values{}
	q.Set("audience", "author")
	q.Set("resolve", "true")
	res, err := sc.cl.RetrieveWithQuery(sc.ctx, pagesTreePath(sc.teamID, sc.hubID, pageID), q)
	if err != nil {
		if errs.CodeOf(err) == errs.ExitNotFound {
			return 0, nil // no draft yet → first-set sentinel
		}
		return 0, err
	}
	if res != nil {
		if dv, ok := attrInt(res.Attributes["draft_version"]); ok {
			return dv, nil
		}
	}
	return 0, nil
}
