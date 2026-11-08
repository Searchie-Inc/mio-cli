package cmd

import (
	"fmt"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

const opAbsentNote = "scaffold-from-template op not available — applying client-side"

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

func applyViaServerOp(sc *scaffoldContext) (bool, error) {
	req := client.ScaffoldFromTemplateRequest{
		HubTemplateID:  sc.hubTmpl.ID,
		Name:           sc.hubName,
		Slug:           sc.hubSlug,
		CatalogDigest:  sc.cat.Meta.Digest,
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

func retryServerOpAfterRefetch(sc *scaffoldContext, req client.ScaffoldFromTemplateRequest, opErr error) (bool, error) {
	rejected := func(retried bool) error {
		var advice string
		switch {
		case client.HasAPIErrorCode(opErr, "catalog_digest_mismatch"):
			if retried {
				advice = " (the retry with the refetched digest was also rejected; the backend pin may have moved again — re-run)"
			} else {
				advice = " (the backend's catalog digest differs from the one this run sent — re-run)"
			}
		case client.HasAPIErrorCode(opErr, "page_conflict"), client.HasAPIErrorCode(opErr, "reserved_page_conflict"):
			advice = " (inspect the hub's pages)"
		case client.HasAPIErrorCode(opErr, "invalid_scaffold_request"), client.HasAPIErrorCode(opErr, "template_not_scaffoldable"):
			advice = " (inspect the template and its instantiation)"
		}
		return errs.Wrap(errs.CodeOf(opErr), fmt.Errorf("scaffold-from-template rejected the request: %w%s", opErr, advice))
	}

	cat, src, rerr := catalog.Resolve(sc.ctx, scaffoldResolveOptions(sc, sc.notef))
	if rerr != nil {
		sc.notef("catalog refetch after the rejection failed: %v", rerr)
		return false, rejected(false)
	}
	if cat.Meta.Digest == sc.cat.Meta.Digest {
		return false, rejected(false)
	}

	sc.notef("backend catalog pin moved (revision %d → %d) — retrying the op with the fresh digest",
		sc.cat.Meta.Revision, cat.Meta.Revision)
	if perr := rebuildScaffoldPlan(sc, cat, src, req.HubTemplateID, sc.hubName, sc.hubSlug); perr != nil {
		return false, perr
	}
	sc.cat = cat

	if keys := playlistBindingKeys(sc); len(keys) > 0 {
		sc.notef("the refetched catalog's template binds playlist dataSource key(s) %s, which the op could only match by playlist title — applying the new plan client-side instead of retrying",
			strings.Join(keys, ", "))
		return false, nil
	}

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
		sc.notef("retry with the refetched digest also failed: %v", err)
		return false, rejected(true)
	}
}

func recordServerOpResult(sc *scaffoldContext, res client.ScaffoldFromTemplateResult) {
	if len(res.Pages) == 0 {
		sc.notef("scaffold-from-template op succeeded but returned no page listing — the pages were applied server-side; homepage id unknown to this run")
		return
	}
	for _, p := range res.Pages {
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
