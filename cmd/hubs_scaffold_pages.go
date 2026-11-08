package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

type recoveryAction int

const (
	actionCreate recoveryAction = iota
	actionResumeFull
	actionNoop
	actionConflict
)

type recoveredPage struct {
	id                  string
	appID               string
	state               string
	draftVersion        int
	appliedDraftVersion int
	isHome              bool
}

func decideRecovery(ourAppID string, p *recoveredPage) recoveryAction {
	switch {
	case p == nil:
		return actionCreate
	case p.appID == "" || p.appID != ourAppID:
		return actionConflict
	case p.state == "pending":
		if p.draftVersion == 0 {
			return actionResumeFull
		}
		return actionConflict
	case p.state == "applied":
		if p.draftVersion == p.appliedDraftVersion {
			return actionNoop
		}
		return actionConflict
	default:
		return actionConflict
	}
}

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

func stepPages(sc *scaffoldContext, _ *catalog.HubTemplate) error {
	if sc.pagePlan == nil || len(sc.pagePlan.pages) == 0 {
		return sc.step("pages", "no pages in template", func() error { return nil })
	}

	if !sc.dryRun {
		if missing := unresolvedBindingKeys(sc); len(missing) > 0 {
			return errs.New(errs.ExitUsage,
				"pages bind playlist dataSource key(s) %s, but this run created no playlist for them and none could be matched on the hub by title — the section(s) would render empty and be marked applied, so no page was written. Re-run against a hub without conflicting playlists, or give the hub a playlist titled as the template's so it can be matched",
				strings.Join(missing, ", "))
		}
	}

	if !sc.dryRun && sc.catalogOverride == "" && sc.cat != nil {
		if keys := playlistBindingKeys(sc); len(keys) > 0 {
			sc.notef("not using the scaffold-from-template op: the pages bind playlist dataSource key(s) %s, which the op could only match by playlist title — applying client-side with the ids this run holds",
				strings.Join(keys, ", "))
		} else {
			done, err := applyViaServerOp(sc)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}

	for i := range sc.pagePlan.pages {
		pp := sc.pagePlan.pages[i]
		home := ""
		if pp.ref.IsHomepage {
			home = ", homepage"
		}
		bind := ""
		if keys := catalog.PlaylistDataSourceKeys(pp.rawTree); len(keys) > 0 {
			bind = fmt.Sprintf(", binding playlist dataSource key(s) [%s] to the ids this run creates", strings.Join(keys, ", "))
		}
		detail := fmt.Sprintf("page %q: apply via backend op if available, else create + set tree%s + publish + mark applied; re-runs follow §5.1 recovery (template %s%s)",
			pp.ref.Slug, bind, pp.ref.PageTemplate, home)
		if err := sc.step("pages", detail, func() error { return applyPageClientSide(sc, pp) }); err != nil {
			return errs.Wrap(errs.CodeOf(err), fmt.Errorf("page %q: %w", pp.ref.Slug, err))
		}
	}
	return nil
}

func (sc *scaffoldContext) foreignHomepage(exceptID string) (*client.Resource, bool, error) {
	return sc.findHubPage(func(r client.Resource) bool {
		isHome, _ := r.Attributes["is_homepage"].(bool)
		return isHome && r.ID != exceptID
	})
}

func (sc *scaffoldContext) homepageClaimedError(pageID string) error {
	hres, found, err := sc.foreignHomepage(pageID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return errs.New(errs.ExitUsage,
		"existing homepage %s is not this page; refusing to make %s the homepage (it would clear %s server-side) — inspect it or re-run with --hub %s after resolving",
		hres.ID, pageID, hres.ID, sc.hubID)
}

func (sc *scaffoldContext) setHomepage(pageID string) error {
	if cerr := sc.homepageClaimedError(pageID); cerr != nil {
		return cerr
	}
	_, err := sc.cl.Update(sc.ctx, pagesPath(sc.teamID, sc.hubID, pageID),
		map[string]any{"is_homepage": true})
	return err
}

func applyPageClientSide(sc *scaffoldContext, pp plannedPage) error {
	title, terr := catalog.InterpolateTitle(pp.ref.Title, sc.hubName, sc.hubSlug)
	if terr != nil {
		return errs.Wrap(errs.ExitUsage, terr)
	}
	tree := catalog.CloneNode(pp.rawTree)
	if ierr := catalog.InterpolateTreeValues(tree, sc.hubName, sc.hubSlug); ierr != nil {
		return errs.Wrap(errs.ExitUsage, ierr)
	}

	if bound, unresolved := catalog.BindPlaylistDataSources(tree, sc.playlistIDsByKey); len(unresolved) > 0 {
		sc.notef("page %q: %d playlist dataSource(s) bound; no playlist created this run for key(s) %s — those sections will render unbound",
			pp.ref.Slug, bound, strings.Join(unresolved, ", "))
	}
	treeObj := map[string]any{"root": tree}

	ourApp := catalog.ApplicationID(sc.hubID, sc.hubTmpl.ID)
	rp, rerr := sc.recoverPageAtSlug(pp.ref.Slug, ourApp)
	if rerr != nil {
		return rerr
	}
	var pageID string
	needHome := pp.ref.IsHomepage
	switch decideRecovery(ourApp, rp) {
	case actionConflict:
		return errs.New(errs.ExitUsage,
			"conflicts with existing page %s (%s); refusing to overwrite — inspect it or re-run with --hub %s after resolving",
			rp.id, recoveryConflictReason(ourApp, rp), sc.hubID)

	case actionNoop:
		sc.recordPageID(pp.ref.Slug, rp.id)
		if pp.ref.IsHomepage && !rp.isHome {
			if cerr := sc.homepageClaimedError(rp.id); cerr != nil {
				return cerr
			}
			sc.notef("page %q already applied (untouched) — setting it as the homepage", pp.ref.Slug)
			return sc.setHomepage(rp.id)
		}
		sc.notef("page %q already applied (untouched) — skipping", pp.ref.Slug)
		return nil

	case actionResumeFull:
		sc.notef("page %q: resuming onto existing page %s (pending, no draft written) — skipping create",
			pp.ref.Slug, rp.id)
		pageID = rp.id
		needHome = pp.ref.IsHomepage && !rp.isHome
		if needHome {
			if cerr := sc.homepageClaimedError(rp.id); cerr != nil {
				return cerr
			}
		}

	case actionCreate:
		if pp.ref.IsHomepage {
			hres, hfound, herr := sc.foreignHomepage("")
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
		return errs.New(errs.ExitGeneric, "internal: unhandled recovery action")
	}

	sc.recordPageID(pp.ref.Slug, pageID)

	tres, perr := sc.cl.ActionWithHeaders(
		sc.ctx, client.StyleEnvelope, "PUT",
		pagesTreePath(sc.teamID, sc.hubID, pageID),
		map[string]any{"tree": treeObj},
		map[string]string{"If-Match": "0"},
	)
	if perr != nil {
		return perr
	}
	dv := 0
	if tres != nil {
		if v, ok := attrInt(tres.Attributes["draft_version"]); ok {
			dv = v
		}
	}
	if pp.ref.IsHomepage {
		sc.homePageID, sc.homeDraftVersion = pageID, dv
	}

	if _, err := sc.cl.ActionWithHeaders(
		sc.ctx, client.StyleEnvelope, "POST",
		pagesPath(sc.teamID, sc.hubID, pageID)+"/publish",
		nil,
		map[string]string{"If-Match": strconv.Itoa(dv)},
	); err != nil {
		return err
	}

	digest, derr := catalog.TreeDigest(treeObj)
	if derr != nil {
		return errs.Wrap(errs.ExitGeneric, derr)
	}
	if _, err := sc.cl.Update(sc.ctx, pagesPath(sc.teamID, sc.hubID, pageID),
		map[string]any{"meta": templateMarkerApplied(sc, pp.ref, digest, dv)}); err != nil {
		return err
	}

	if needHome {
		return sc.setHomepage(pageID)
	}
	return nil
}

func pageInputFor(ref catalog.PageRef, title string, marker map[string]any) PageInput {
	slug := ref.Slug
	in := PageInput{Title: &title, Slug: &slug, Meta: marker}
	if ref.Privacy != "" {
		priv := ref.Privacy
		in.Privacy = &priv
	}
	return in
}

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

func templateMarkerApplied(sc *scaffoldContext, ref catalog.PageRef, treeDigest string, draftVersion int) map[string]any {
	m := templateMarker(sc, ref, "applied")
	tp := m["template_provenance"].(map[string]any)
	tp["appliedTreeDigest"] = treeDigest
	tp["appliedDraftVersion"] = draftVersion
	return m
}

func (sc *scaffoldContext) recoverPageAtSlug(slug, ourAppID string) (*recoveredPage, error) {
	res, found, err := sc.existingPageBySlug(slug)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	rp := &recoveredPage{id: res.ID}
	rp.isHome, _ = res.Attributes["is_homepage"].(bool)
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

func (sc *scaffoldContext) findHubPage(match func(r client.Resource) bool) (*client.Resource, bool, error) {
	query := url.Values{}
	seen := map[string]bool{}
	const maxPages = 1000
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

func (sc *scaffoldContext) existingPageBySlug(slug string) (*client.Resource, bool, error) {
	return sc.findHubPage(func(r client.Resource) bool {
		s, _ := r.Attributes["slug"].(string)
		return s == slug
	})
}

func (sc *scaffoldContext) pageDraftVersion(pageID string) (int, error) {
	q := url.Values{}
	q.Set("audience", "author")
	q.Set("resolve", "true")
	res, err := sc.cl.RetrieveWithQuery(sc.ctx, pagesTreePath(sc.teamID, sc.hubID, pageID), q)
	if err != nil {
		if errs.CodeOf(err) == errs.ExitNotFound {
			return 0, nil
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
