package cmd

// hubs_scaffold_pages.go — the general pages[] apply loop of the scaffold
// pipeline (MIO-2672 Task 7), replacing the single-homepage shim. Every page
// of the hub template is applied CLIENT-SIDE: created with a §5.1 provenance
// marker ("pending"), its finally-interpolated draft tree PUT + published, and
// the marker flipped to "applied" (with the applied tree digest + draft
// version). This is the client-side FALLBACK path's core — the W2b backend-op
// probe (Task 9) will prefer the server-side op where it exists.
//
// Crash RECOVERY (MIO-2672 Task 8, spec §5.1): a re-run onto a half-applied
// hub reads back the provenance snapshot of any existing page at a manifest
// slug and dispatches on the PURE decideRecovery decision — create, resume,
// no-op, or conflict. No code path ever writes over a page whose action is
// not create/resumeFull (§2.2: never overwrite).

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ---- §5.1 crash-recovery decision (pure) -------------------------------------

// recoveryAction is the per-page verdict of the §5.1 crash-recovery contract.
type recoveryAction int

const (
	actionCreate     recoveryAction = iota
	actionResumeFull                // pending + no draft written: safe to set-tree + publish + marker
	actionNoop                      // applied + untouched: idempotent re-run
	actionConflict                  // everything else: never overwrite (§2.2)
)

// recoveredPage is the provenance snapshot read back from an existing page at a
// manifest slug: marker fields from meta.template_provenance + the current
// draft_version (tree-GET audience=author; 404 → 0 = no draft written).
type recoveredPage struct {
	id                  string
	appID               string
	state               string
	draftVersion        int
	appliedDraftVersion int
}

// decideRecovery is the PURE §5.1 per-boundary recovery decision (no HTTP, no
// context): given OUR applicationId and the snapshot of the page found at a
// manifest slug (nil = absent), it returns the one safe action.
//
// v1 NARROWED CLAIM: the client cannot read the raw published tree
// (?resolve=true returns a transformed tree), so appliedTreeDigest equality is
// NOT checkable client-side — "untouched" uses draft_version equality only.
// The spec's "resume the marker PATCH only" row (crashed after publish, before
// the marker PATCH) is unreachable client-side: the applied* fields are
// written in the same PATCH that flips the state, so that crash leaves
// provenanceState "pending" WITH a draft written — indistinguishable from a
// crash at the draft-PUT boundary (or a user's first edit, §10.8) — and it
// lands on CONFLICT. This is a deliberate narrowing-of-the-narrowing; the spec
// already accepts 409-with-guidance at these boundaries.
func decideRecovery(ourAppID string, p *recoveredPage) recoveryAction {
	switch {
	case p == nil:
		return actionCreate // no page at the slug — full create sequence
	case p.appID == "" || p.appID != ourAppID:
		return actionConflict // no marker / foreign application — never touch
	case p.state == "pending":
		if p.draftVersion == 0 {
			return actionResumeFull // our create landed, no draft yet — resume safely
		}
		return actionConflict // draft written: crashed write vs user edit — indistinguishable
	case p.state == "applied":
		if p.draftVersion == p.appliedDraftVersion {
			return actionNoop // converged already — idempotent re-run
		}
		return actionConflict // draft moved since our apply — user edits, keep out
	default:
		return actionConflict // unknown/garbage state — fail safe
	}
}

// recoveryConflictReason is decideRecovery's human-facing sibling: the per-case
// reason string for an actionConflict verdict, pure over the same inputs. Only
// meaningful when decideRecovery returned actionConflict.
func recoveryConflictReason(ourAppID string, p *recoveredPage) string {
	switch {
	case p.appID == "" || p.appID != ourAppID:
		return "foreign page at slug"
	case p.state == "pending":
		return "draft written since our create (crashed run or user edit)"
	case p.state == "applied":
		return "edited since our apply"
	default:
		return fmt.Sprintf("unknown provenance state %q", p.state)
	}
}

// ---- the apply loop -----------------------------------------------------------

// stepPages iterates the preflight page plan IN PLAN ORDER and applies each
// page client-side. The homepage is simply the entry whose ref.IsHomepage is
// true — no positional assumption (it happens to come first in the community
// template). In dry-run each page records its own plan entry under the shared
// "pages" step name, so the plan names every mutation it would run per page.
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
		detail := fmt.Sprintf("page %q: create + set tree + publish + mark applied (template %s%s)",
			pp.ref.Slug, pp.ref.PageTemplate, home)
		if err := sc.step("pages", detail, func() error { return applyPageClientSide(sc, pp) }); err != nil {
			// Every per-page failure names its page EXACTLY ONCE: the messages
			// below (including the conflict errors) are built without a slug
			// prefix, and this single wrap adds it uniformly.
			return errs.Wrap(errs.CodeOf(err), fmt.Errorf("page %q: %w", pp.ref.Slug, err))
		}
	}
	return nil
}

// applyPageClientSide applies ONE planned page: final interpolation → §5.1
// recovery decision at the manifest slug → create (marker "pending") or
// resume onto the crashed create → tree PUT → publish → marker PATCH
// ("applied"). Errors do not name the page — stepPages' wrap does.
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

	// 2. §5.1 recovery decision at the manifest slug: read back the provenance
	// snapshot of any existing page there (exhaustive walker + tree-GET) and
	// dispatch on the pure decideRecovery verdict. Anything not provably safe
	// is a conflict — never an overwrite.
	ourApp := catalog.ApplicationID(sc.hubID, sc.hubTmpl.ID)
	rp, rerr := sc.recoverPageAtSlug(pp.ref.Slug, ourApp)
	if rerr != nil {
		return rerr
	}
	var pageID string
	switch decideRecovery(ourApp, rp) {
	case actionConflict:
		return errs.New(errs.ExitUsage,
			"conflicts with existing page %s (%s); refusing to overwrite — inspect it or re-run with --hub %s after resolving",
			rp.id, recoveryConflictReason(ourApp, rp), sc.hubID)

	case actionNoop:
		// Idempotent re-run: the page is ours, applied, and untouched. Note the
		// skip; the end-of-run summary already counts it — its "Includes:" page
		// list is template-derived, so a converged page is reported either way.
		sc.notef("page %q already applied (untouched) — skipping", pp.ref.Slug)
		return nil

	case actionResumeFull:
		// Crashed after our create, before any draft write: reuse the existing
		// page id and run the remaining tree PUT + publish + marker PATCH on it.
		sc.notef("page %q: resuming onto existing page %s (pending, no draft written) — skipping create",
			pp.ref.Slug, rp.id)
		pageID = rp.id

	case actionCreate:
		// §5.1 homepage hazard: create_page(is_homepage=true) CLEARS any
		// existing homepage server-side, so before creating the IsHomepage
		// entry check the WHOLE hub for an is_homepage page. ANY homepage
		// found here is a conflict, unconditionally: the ours-at-manifest-slug
		// case is handled by the slug row above (resume/noop/conflict) and
		// never reaches this create arm, so whatever the pre-check finds is
		// either foreign or ours at an UNEXPECTED slug (a user slug rename or
		// a catalog-revision slug change — ApplicationID is hub+template only,
		// so a marker match does not prove the page is where this run expects
		// it). Creating would clear it and mint a duplicate; the marker is
		// read only to enrich the reason.
		if pp.ref.IsHomepage {
			hres, hfound, herr := sc.findHubPage(func(r client.Resource) bool {
				isHome, _ := r.Attributes["is_homepage"].(bool)
				return isHome
			})
			if herr != nil {
				return herr
			}
			if hfound {
				reason := fmt.Sprintf("existing homepage %s is not ours", hres.ID)
				if hApp, _, _ := provenanceMarkerFields(hres.Attributes); hApp == ourApp {
					reason = fmt.Sprintf("existing homepage %s carries our marker at an unexpected slug (a crashed run or a slug rename)", hres.ID)
				}
				return errs.New(errs.ExitUsage,
					"%s; refusing to create a new homepage (the create would clear it server-side) — inspect it or re-run with --hub %s after resolving",
					reason, sc.hubID)
			}
		}

		// 3. Create the page carrying the §5.1 provenance marker in "pending"
		// state, so a crash between this create and the applied-PATCH below
		// leaves a detectable half-applied page (the resumeFull arm keys on it).
		// buildPageAttrs is the shared `pages create` builder, so the scaffold
		// gets the same privacy-enum validation the command does.
		attrs, berr := buildPageAttrs(pageInputFor(pp.ref, title, templateMarker(sc, pp.ref, "pending")))
		if berr != nil {
			return berr
		}
		res, cerr := sc.cl.Create(sc.ctx, pagesPath(sc.teamID, sc.hubID, ""), attrs)
		if cerr != nil {
			return cerr
		}
		pageID = res.ID

	default:
		// Unreachable with the four actions above — defense in depth for the
		// never-overwrite rule: an unhandled verdict must never fall through
		// to the tree PUT below with an empty page id.
		return errs.New(errs.ExitGeneric, "internal: unhandled recovery action")
	}

	// 4. Set the draft tree with the first-set OCC sentinel If-Match: 0 —
	// correct for BOTH arms that reach here: a just-created page has never had
	// a draft, and resumeFull only fires when the read-back draft_version is 0
	// (no draft ever written), so the old resume flow's tree-GET → If-Match n
	// dance is unnecessary. Mirrors `pages tree set`.
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
	// own meta between the create (which sent only the marker) and here; a
	// competing writer lands in the conflict arm above on the next run.
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
// returned — what a re-run's decideRecovery compares to decide converged vs
// drifted (v1: the draft_version only; the digest is recorded for the
// server-side op and future audit tooling).
func templateMarkerApplied(sc *scaffoldContext, ref catalog.PageRef, treeDigest string, draftVersion int) map[string]any {
	m := templateMarker(sc, ref, "applied")
	tp := m["template_provenance"].(map[string]any)
	tp["appliedTreeDigest"] = treeDigest
	tp["appliedDraftVersion"] = draftVersion
	return m
}

// ---- recovery snapshot readers ------------------------------------------------

// recoverPageAtSlug reads back the §5.1 provenance snapshot of the page at a
// manifest slug: nil when the slug is free (→ actionCreate). Marker fields are
// parsed TOLERANTLY — a missing or garbage marker degrades to appID "", which
// decideRecovery maps to conflict, never a panic. The current draft_version
// (tree GET) is fetched only for a page carrying OUR marker: every other
// verdict is decided without it, so no extra HTTP is spent on a page we may
// not touch anyway.
func (sc *scaffoldContext) recoverPageAtSlug(slug, ourAppID string) (*recoveredPage, error) {
	res, found, err := sc.existingPageBySlug(slug)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	rp := &recoveredPage{id: res.ID}
	rp.appID, rp.state, rp.appliedDraftVersion = provenanceMarkerFields(res.Attributes)
	if rp.appID == ourAppID {
		dv, derr := sc.pageDraftVersion(res.ID)
		if derr != nil {
			return nil, derr
		}
		rp.draftVersion = dv
	}
	return rp, nil
}

// provenanceMarkerFields extracts the §5.1 marker fields off a page's
// attributes with TOLERANT type assertions: any missing or mis-shaped level
// (no meta, meta not an object, marker not an object, wrong value types)
// yields zero values — appID "" is decideRecovery's conflict signal.
func provenanceMarkerFields(attrs map[string]any) (appID, state string, appliedDV int) {
	meta, _ := attrs["meta"].(map[string]any)
	tp, _ := meta["template_provenance"].(map[string]any)
	appID, _ = tp["applicationId"].(string)
	state, _ = tp["provenanceState"].(string)
	if v, ok := attrInt(tp["appliedDraftVersion"]); ok {
		appliedDV = v
	}
	return appID, state, appliedDV
}

// findHubPage walks the hub's page list EXHAUSTIVELY — same
// meta.page.next_cursor convention + seen-cursor/maxPages stall guard as the
// other steps' lookups (see existingSpaceSlugs) — and returns the first page
// matching match. Every page lookup ("by slug" for the recovery decision; "by
// is_homepage" for the homepage-hazard pre-check) shares this single walker so
// the cursor loop is never duplicated.
func (sc *scaffoldContext) findHubPage(match func(r client.Resource) bool) (*client.Resource, bool, error) {
	query := url.Values{}
	seen := map[string]bool{}
	const maxPages = 1000 // hard ceiling; a hub never has this many pages
	for page := 0; page < maxPages; page++ {
		col, lerr := sc.cl.List(sc.ctx, pagesPath(sc.teamID, sc.hubID, ""), query)
		if lerr != nil {
			return nil, false, lerr
		}
		for _, r := range col.Data {
			if match(r) {
				return &r, true, nil
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
	return nil, false, nil
}

// existingPageBySlug returns the hub's page at slug (the full resource — the
// recovery decision reads the provenance marker off its attributes), or
// found=false when the slug is free.
func (sc *scaffoldContext) existingPageBySlug(slug string) (*client.Resource, bool, error) {
	return sc.findHubPage(func(r client.Resource) bool {
		s, _ := r.Attributes["slug"].(string)
		return s == slug
	})
}

// pageDraftVersion reads an existing page's current draft_version via a
// tree-get (RetrieveWithQuery on the author draft, mirroring `pages tree get`)
// — the authoritative OCC source the rest of the codebase uses, since the page
// LIST does not surface draft_version. A page that has never had a draft 404s
// on tree-get; that is TOLERATED as draft_version 0 (the first-set sentinel).
// Any other error propagates. Used by the §5.1 recovery snapshot — fresh
// creates never need it (their If-Match is always the sentinel 0).
func (sc *scaffoldContext) pageDraftVersion(pageID string) (int, error) {
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
