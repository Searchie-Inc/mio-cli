package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var hubOpRowKinds = []struct{ name, prefix, kind string }{
	{"hub", "", "hubs"},
	{"welcome_post", "", "discussions"},
	{"", "space:", "spaces"},
	{"", "page:", "pages"},
	{"", "playlist:", "playlists"},
	{"", "onboarding:", "contact_attribute_definitions"},
	{"", "content_node:", "content_nodes"},
}

func hubOpRowKind(resource string) (kind, name string, ok bool) {
	for _, m := range hubOpRowKinds {
		if m.prefix == "" {
			if resource == m.name {
				return m.kind, "", true
			}
			continue
		}
		if strings.HasPrefix(resource, m.prefix) {
			return m.kind, strings.TrimPrefix(resource, m.prefix), true
		}
	}
	return "", "", false
}

func hubOpUnsupportedFlags(cmd *cobra.Command) []string {
	names := make([]string, 0, len(scaffoldBrandingFlags)+2)
	for _, f := range scaffoldBrandingFlags {
		names = append(names, f.flag)
	}
	names = append(names, "branding-json", "catalog")

	var changed []string
	for _, n := range names {
		if cmd.Flags().Changed(n) {
			changed = append(changed, "--"+n)
		}
	}
	return changed
}

func hubOpSkipReason(cmd *cobra.Command, sc *scaffoldContext) (reason string, announce bool) {
	if sc.dryRun {
		return "--dry-run renders the plan client-side (the op has no plan mode)", false
	}
	if sc.hubID != "" {
		return "--hub applies onto an existing hub and the op is create-only", true
	}
	if sc.nameOverride == "" || sc.slugOverride == "" {
		return "the op requires both --name and --slug (it will not mint a slug)", true
	}
	if changed := hubOpUnsupportedFlags(cmd); len(changed) > 0 {
		return fmt.Sprintf("%s cannot be expressed in the op's overrides",
			strings.Join(changed, ", ")), true
	}
	return "", false
}

var hubOpExpressibleFlags = map[string]bool{
	"template":             true,
	"name":                 true,
	"slug":                 true,
	"publish":              true,
	"logo-url":             true,
	"favicon-url":          true,
	"registration-enabled": true,
}

var hubOpStructuralFlags = map[string]bool{
	"dry-run":          true,
	"reapply-template": true,
}

func maybeApplyViaHubOp(cmd *cobra.Command, sc *scaffoldContext) (handled bool, err error) {
	reason, announce := hubOpSkipReason(cmd, sc)
	if reason != "" {
		if announce {
			sc.notef("not using the server-side hub scaffold op: %s — applying client-side", reason)
		}
		return false, nil
	}
	return applyViaHubOp(sc)
}

func applyViaHubOp(sc *scaffoldContext) (bool, error) {
	req := client.HubFromTemplateRequest{
		HubTemplateID: sc.hubTmpl.ID,
		Name:          sc.nameOverride,
		Slug:          sc.slugOverride,
		CatalogDigest: sc.cat.Meta.Digest,
		Overrides: client.HubFromTemplateOverrides{
			LogoURL:             sc.logoOverride,
			FaviconURL:          sc.faviconOverride,
			RegistrationEnabled: sc.registrationOverride,
			Publish:             sc.publish,
		},
		IdempotencyKey: catalog.CreateApplicationID(sc.teamID, sc.hubTmpl.ID, sc.nameOverride, sc.slugOverride),
	}

	res, err := sc.cl.HubFromTemplate(sc.ctx, sc.teamID, req)
	switch {
	case err == nil:
		recordHubOpResult(sc, res)
		return true, nil
	case errors.Is(err, client.ErrHubOpAbsent):
		sc.notef("hub scaffold op not available on this backend — applying client-side")
		return false, nil
	default:
		return false, hubOpError(err)
	}
}

const hubOpFingerprintMismatch = "idempotency_fingerprint_mismatch"

func hubOpError(err error) error {
	if client.HasAPIErrorCode(err, hubOpFingerprintMismatch) {
		return errs.Wrap(errs.CodeOf(err), fmt.Errorf(
			"%w ["+hubOpFingerprintMismatch+"] (this hub name+slug was already scaffolded from this template with a DIFFERENT request — "+
				"the backend's catalog pin or your override flags have changed since. Nothing was applied. "+
				"Scaffold under a different --name/--slug, or re-run with --catalog to take the client-side path)", err))
	}
	return err
}

func recordHubOpResult(sc *scaffoldContext, res client.HubFromTemplateResult) {
	sc.hubID = res.HubID
	sc.hubName, sc.hubSlug = sc.nameOverride, sc.slugOverride

	if res.Replayed {
		sc.notef("this hub was already scaffolded with these inputs — the backend replayed the stored result (no duplicate hub); per-resource ids are not part of a replay")
	}

	type attribution struct{ kind, name string }
	var attrs []attribution
	counts := map[string]int{}
	for _, row := range res.Summary {
		switch row.Action {
		case "created":
			kind, name, ok := hubOpRowKind(row.Resource)
			if !ok {
				continue
			}
			attrs = append(attrs, attribution{kind, name})
			counts[kind]++
		case "skipped":
			sc.notef("%s skipped by backend op: %s", row.Resource, row.Reason)
		}
	}

	trusted := map[string]bool{}
	for kind, n := range counts {
		if got := len(res.CreatedIDs[kind]); got != n {
			if !res.Replayed {
				sc.notef("backend op returned %d %s id(s) for %d created row(s) — ids for %s not recorded (they will read null)",
					got, kind, n, kind)
			}
			continue
		}
		trusted[kind] = true
	}

	next := map[string]int{}
	for _, a := range attrs {
		if !trusted[a.kind] {
			continue
		}
		id := res.CreatedIDs[a.kind][next[a.kind]]
		next[a.kind]++
		switch a.kind {
		case "pages":
			sc.recordPageID(a.name, id)
		case "spaces":
			sc.spaceIDsBySlug[a.name] = id
		case "playlists":
			sc.playlistIDsByKey[a.name] = id
		case "contact_attribute_definitions":
			sc.defIDsBySlug[a.name] = id
		case "discussions":
			sc.welcomePostID, sc.welcomePostStatus = id, "created"
		}
	}
	recordHubOpContentNodes(sc, res, trusted["content_nodes"])

	for _, p := range sc.hubTmpl.Pages {
		if p.IsHomepage {
			sc.homePageID = sc.pageIDsBySlug[p.Slug]
			break
		}
	}

	recordHubOpPublishState(sc, res)
	readBackHubIdentity(sc, res.Replayed)
}

func recordHubOpPublishState(sc *scaffoldContext, res client.HubFromTemplateResult) {
	for _, row := range res.Summary {
		switch row.Resource {
		case "publish":
			sc.isPrivate, sc.isPrivateKnown = row.Action != "created", true
		case "policy_gate":
			if row.Action != "created" {
				continue
			}
			if pol, err := resolveTemplatePolicies(&sc.hubTmpl); err == nil {
				sc.policyGate = pol.gate
			}
		}
	}
}

func readBackHubIdentity(sc *scaffoldContext, replayed bool) {
	res, err := sc.cl.Retrieve(sc.ctx, hubsPath(sc.teamID, sc.hubID))
	if err != nil {
		sc.notef("could not read the new hub back (%v) — reporting the requested name/slug; verify with `mio hubs retrieve %s`", err, sc.hubID)
		return
	}
	if slug, _ := res.Attributes["slug"].(string); slug != "" {
		if slug != sc.hubSlug {
			if replayed {
				sc.notef("the stored hub %s has slug %q (this run requested %q)", sc.hubID, slug, sc.hubSlug)
			} else {
				sc.notef("backend assigned slug %q (requested %q — it was taken, here or by another team); the hub is %s. If you meant to resume a hub of yours instead of creating a new one, re-run with --hub <its id>", slug, sc.hubSlug, sc.hubID)
			}
		}
		sc.hubSlug = slug
	}
	if title, _ := res.Attributes["title"].(string); title != "" {
		sc.hubName = title
	}
	if p, ok := res.Attributes["is_private"].(bool); ok {
		sc.isPrivate, sc.isPrivateKnown = p, true
	}
}
