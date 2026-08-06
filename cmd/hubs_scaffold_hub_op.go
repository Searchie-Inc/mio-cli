package cmd

// hubs_scaffold_hub_op.go — the WHOLE-HUB backend-op branch (MIO-2976; the op
// is mio-backend MIO-2926 / #641).
//
// Same shape as the pages op in hubs_scaffold_op.go, one level up: in CREATE
// mode the runner PROBES POST /api/teams/{team}/hubs/from-template by simply
// calling it — the probe IS the real POST, never a separate capability check —
// and an absent op falls back to the nine-step client-side pipeline. Both paths
// produce a real hub; a missing op is never an error.
//
// WHAT MAKES THE ABSENCE SIGNAL DIFFERENT HERE. The pages probe treats 404 and
// 405 alike. This op cannot: it answers 404 `template_not_found` for an unknown
// template, and 404 and 405 derive the SAME ExitNotFound. So the fallback keys
// on client.ErrHubOpAbsent — set only for the bare 405 — and never on the exit
// code. Everything else surfaces: the op EXISTS but disagrees or is unhealthy,
// and applying client-side on top of that just smears partial state.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// hubOpRowKinds maps a summary row's `resource` onto the created_resource_ids
// key whose list that row appended to, mirroring service.py's `_Summary.created`
// call sites exactly. A row whose resource matches nothing here carries NO id
// (branding/settings/navigation/policy:*/policy_gate/publish call `created()`
// with no kind), and rows with action "skipped" never append an id either.
//
// prefix "" means the resource must match `name` exactly.
var hubOpRowKinds = []struct{ name, prefix, kind string }{
	{"hub", "", "hubs"},
	{"welcome_post", "", "discussions"},
	{"", "space:", "spaces"},
	{"", "page:", "pages"},
	{"", "playlist:", "playlists"},
	{"", "onboarding:", "contact_attribute_definitions"},
}

// hubOpRowKind returns the created_resource_ids key a row contributes to and the
// slug/key it names ("" for the singleton rows), or ok=false when the row
// carries no id at all.
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

// hubOpUnsupportedFlags are the flags whose effect the op's overrides{} block
// cannot carry. Their presence forces the client-side pipeline (ticket §2):
// running the op anyway would silently drop what the operator asked for, which
// is the one outcome worse than not using the op.
//
// --catalog is here for a different reason than the branding flags: the op
// applies from the SERVER's catalog, so a local catalog file cannot be honored
// at all — the digest we would send describes a manifest the server never read.
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

// hubOpSkipReason reports why THIS invocation cannot use the whole-hub op, or
// "" when the op should be probed. announce says whether the reason is worth a
// note: a run the operator's own FLAGS pushed off the op path must say so (§2 —
// otherwise the op silently drops what they asked for), whereas --dry-run is
// structural and self-evident from the command line, and the plan renderer
// deliberately emits no apply-time notes at all.
func hubOpSkipReason(cmd *cobra.Command, sc *scaffoldContext) (reason string, announce bool) {
	// §6 — the plan renderer needs no server op, and a dry run must stay
	// write-free. The op has no plan mode: calling it WOULD create the hub.
	if sc.dryRun {
		return "--dry-run renders the plan client-side (the op has no plan mode)", false
	}
	// §5 — the op is create-only by design; --hub applies onto an existing hub.
	if sc.hubID != "" {
		return "--hub applies onto an existing hub and the op is create-only", true
	}
	// The op REQUIRES both identity attributes and rejects an empty one
	// (name min_length 1, slug pattern ^[a-z0-9-]+$). The client-side path is
	// laxer — it lets the backend mint a slug from the title — so an invocation
	// that gives only one of the two is a client-side invocation.
	if sc.nameOverride == "" || sc.slugOverride == "" {
		return "the op requires both --name and --slug (it will not mint a slug)", true
	}
	if changed := hubOpUnsupportedFlags(cmd); len(changed) > 0 {
		return fmt.Sprintf("%s cannot be expressed in the op's overrides",
			strings.Join(changed, ", ")), true
	}
	// NOTE: an EMPTY --logo-url/--favicon-url does not appear here. It is not a
	// reason to prefer the client path — the API rejects an empty branding *_url
	// on hub create and hub update too — so it is refused outright, pre-HTTP, by
	// validateScaffoldURLOverrides.
	return "", false
}

// hubOpExpressibleFlags are the `hubs scaffold` flags the op CAN honour, either
// through its overrides{} block or its identity attributes. Together with
// hubOpUnsupportedFlags this must classify EVERY flag on the command — a flag in
// neither set is silently dropped when the op path is taken, because
// pflag's Changed() answers false for a name it does not know. Pinned by
// TestScaffoldHubOp_EveryScaffoldFlagIsClassified.
var hubOpExpressibleFlags = map[string]bool{
	"template":             true, // the op's hub_template_id
	"name":                 true, // its name (also gated: must be non-empty)
	"slug":                 true, // its slug (likewise)
	"publish":              true, // overrides.publish
	"logo-url":             true, // overrides.logo_url
	"favicon-url":          true, // overrides.favicon_url
	"registration-enabled": true, // overrides.registration_enabled
}

// hubOpStructuralFlags are flags the op has no equivalent for but which are
// already handled by their own skip branch, so they never reach the request.
//
// Kept separate from hubOpExpressibleFlags only so each name records WHY it is
// exempt; be honest about what that buys. Both maps are read by the completeness
// guard alone, so listing a flag in either one silences that guard without
// wiring anything — the whitelist-vs-whitelist escape hatch
// .claude/rules/verifying-guards.md warns about, which this split narrows but
// does not close. The guard's real job is catching a flag classified NOWHERE;
// a flag deliberately misfiled here is not something it can see.
var hubOpStructuralFlags = map[string]bool{
	"dry-run": true, // hubOpSkipReason returns before probing
}

// maybeApplyViaHubOp is the runner's single entry point into the op branch: it
// decides whether the op is usable for this invocation, probes it if so, and
// reports whether the client-side pipeline still has to run.
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

// applyViaHubOp probes the op for the WHOLE hub. Returns (true, nil) when the op
// built the hub (the pipeline is skipped entirely), (false, nil) when the op is
// absent — the ONLY fallback condition — and (false, err) for everything else.
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
		// §3 — deterministic, so re-running the same command converges on the
		// stored application instead of creating a second hub (MIO-2565).
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

// hubOpFingerprintMismatch is the backend error code for a reused
// Idempotency-Key carrying a changed request.
const hubOpFingerprintMismatch = "idempotency_fingerprint_mismatch"

// hubOpError adds the guidance the raw server error cannot carry. The
// fingerprint mismatch is the one an operator will actually hit and the one
// where "just re-run" is WRONG advice: the key is derived from team+template+
// name+slug, but the backend folds the catalog digest and the overrides into its
// fingerprint too, so the same command after a catalog pin move — or with a
// different --publish — reuses the key with a changed request and is refused.
// Nothing was applied; the fix is a new identity or the client-side path.
func hubOpError(err error) error {
	if client.HasAPIErrorCode(err, hubOpFingerprintMismatch) {
		// The CODE is named in the message on purpose. apiError.message() renders
		// detail-over-code, so without this the machine-readable token never
		// appears anywhere in the CLI's output — while the agent-facing docs tell
		// agents to branch on exactly that token.
		return errs.Wrap(errs.CodeOf(err), fmt.Errorf(
			"%w ["+hubOpFingerprintMismatch+"] (this hub name+slug was already scaffolded from this template with a DIFFERENT request — "+
				"the backend's catalog pin or your override flags have changed since. Nothing was applied. "+
				"Scaffold under a different --name/--slug, or re-run with --catalog to take the client-side path)", err))
	}
	return err
}

// recordHubOpResult translates the op's result into the same scaffoldContext
// state the client-side pipeline leaves behind, so `-o json` reports the same
// keys with the same meanings on both paths (§4).
//
// The op reports ids as created_resource_ids — a per-KIND ordered list with no
// slug attached — while the CLI's result is keyed by template slug. The two are
// reconcilable because service.py appends to created_ids[kind] in the SAME
// statement that appends the row, so the Nth created row of a kind pairs with
// created_ids[kind][N]. That pairing is only as good as hubOpRowKinds, so it is
// CHECKED rather than trusted: if the number of rows attributed to a kind does
// not match the number of ids returned for it, every id of that kind is dropped
// and the run says so. A missing id renders as null; a WRONG id would be acted
// on. Same stance templateSlugForRole takes for the pages op.
func recordHubOpResult(sc *scaffoldContext, res client.HubFromTemplateResult) {
	sc.hubID = res.HubID
	// Best knowledge until the read-back below improves on it: the op echoes no
	// identity, and these are exactly what we asked it for.
	sc.hubName, sc.hubSlug = sc.nameOverride, sc.slugOverride

	if res.Replayed {
		sc.notef("this hub was already scaffolded with these inputs — the backend replayed the stored result (no duplicate hub); per-resource ids are not part of a replay")
	}

	// Pass 1: attribute each id-bearing row to a kind, in row order.
	type attribution struct{ kind, name string }
	var attrs []attribution
	counts := map[string]int{}
	for _, row := range res.Summary {
		switch row.Action {
		case "created":
			kind, name, ok := hubOpRowKind(row.Resource)
			if !ok {
				continue // branding/settings/navigation/policy:*/policy_gate/publish
			}
			attrs = append(attrs, attribution{kind, name})
			counts[kind]++
		case "skipped":
			sc.notef("%s skipped by backend op: %s", row.Resource, row.Reason)
		}
	}

	// Pass 2: only attribute a kind whose row count matches the id count.
	//
	// A REPLAY is exempt from the note, not from the check: created_resource_ids
	// is empty by design on a replay, so every kind "disagrees" and the operator
	// would get a wall of anomaly warnings describing the documented normal. The
	// replay disclosure above already says the ids are not coming.
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

	// Pass 3: record, walking each kind's list in the order it was built.
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

	// The homepage id the summary cannot name: rows are slug-keyed, so resolve
	// the template's isHomepage entry to its slug and read the id back.
	for _, p := range sc.hubTmpl.Pages {
		if p.IsHomepage {
			sc.homePageID = sc.pageIDsBySlug[p.Slug]
			break
		}
	}

	recordHubOpPublishState(sc, res)
	readBackHubIdentity(sc)
}

// recordHubOpPublishState reads the publish outcome off the summary rather than
// off the --publish intent: the row is what the server DID.
//
// The policy gate is reported only when the op says it set one, and its VALUE
// comes from the same template declaration the server read (the summary carries
// created/skipped, never the boolean). Both sides resolve it from the catalog
// this run already validated, so they cannot disagree — and when they could
// (a resolve error), the gate stays nil rather than guessing.
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

// readBackHubIdentity GETs the finished hub so the reported slug/name/published
// state are OBSERVED rather than assumed.
//
// This is not belt-and-braces: the backend mints a unique slug when the
// requested one is taken (its own `hub_slug_unavailable` path is the last
// resort), so the created hub's slug can differ from what we sent — and
// hub_path is built from it. A wrong URL is worse than a slow one.
//
// Failure here is NOT a run failure: the hub exists and the op already
// succeeded. It degrades to the requested values with a note.
func readBackHubIdentity(sc *scaffoldContext) {
	res, err := sc.cl.Retrieve(sc.ctx, hubsPath(sc.teamID, sc.hubID))
	if err != nil {
		sc.notef("could not read the new hub back (%v) — reporting the requested name/slug; verify with `mio hubs retrieve %s`", err, sc.hubID)
		return
	}
	if slug, _ := res.Attributes["slug"].(string); slug != "" {
		if slug != sc.hubSlug {
			sc.notef("backend assigned slug %q (requested %q — it was taken)", slug, sc.hubSlug)
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
