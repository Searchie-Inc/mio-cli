package cmd

// hubs_scaffold.go — `mio hubs scaffold`, the template-driven full-experience
// hub seeder (MIO-2543). See docs/superpowers/specs/2026-07-21-hubs-scaffold-design.md.
//
// This file is the ORCHESTRATOR: the command shell, the mutable
// scaffoldContext threaded through every step, the ordered pipeline runner, and
// the --dry-run plan. Each step body (hub/blobs/spaces/…) is fully implemented
// from the extracted attribute-builders + client calls the individual
// `mio hubs`/`community`/`media`/`pages` commands use, so the scaffold stays
// strictly CLI-only and never re-invokes a command's RunE; in --dry-run a step
// records its plan entry instead of firing HTTP (see sc.step).
//
// Design invariants pinned here (hubs_scaffold_test.go):
//   - the CLI holds NO templates (MIO-2672, spec §0): the hub template comes
//     from the LIVE catalog of the target backend, resolved + validated +
//     preliminarily interpolated by the WRITE-FREE preflight
//     (hubs_scaffold_preflight.go) BEFORE stepHub creates anything — an
//     unknown/invalid template exits ExitUsage after only the catalog GET;
//   - auth + team + (in resume mode) the target hub are resolved ONCE, up front,
//     and shared by every step via scaffoldContext — steps 2-8 cannot build
//     their request without the ids this resolution mints;
//   - --dry-run records + prints the ordered plan and fires no MUTATION.

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
)

// ---- context (the data-flow spine) ------------------------------------------

// scaffoldContext is the mutable state threaded through the pipeline. Auth/team
// are resolved once; the hub id is filled at step 1 (create) or by resume-mode
// resolution; each later step reads the ids it needs and writes back the ids it
// mints (space ids by slug, def ids by slug, playlist ids by template key, the
// homepage page id + its draft version). This context — not "each step in
// isolation" — is what makes the pipeline runnable.
type scaffoldContext struct {
	ctx    context.Context
	cl     *client.Client
	teamID string

	hubID, hubSlug string
	// hubName is the hub's ACTUAL title — the {{hub_name}} interpolation value.
	// Resume mode: from the resume GET; create mode: from the create response,
	// falling back to nameOverride when the response omits the title.
	hubName   string
	isPrivate bool
	// isPrivateKnown reports whether isPrivate was actually observed from the server
	// (create response / resume GET / publish PATCH) rather than left at its bool
	// zero value. The summary keys the LIVE/PRIVATE label off the REAL state
	// (isPrivate), so it must not read a never-populated false as "public": when the
	// state is unknown it falls back to PRIVATE (the safe direction).
	isPrivateKnown bool

	// Create-mode identity overrides from the core --name/--slug flags (Task 11).
	// The template carries no hub identity — it is the reusable "experience", and
	// the operator names the specific instance — so name/slug come from flags.
	nameOverride, slugOverride string

	// Presentation overrides from the Phase-5 flags (--favicon-url/--logo-url →
	// branding, --registration-enabled → settings.registration.enabled). A nil
	// pointer means "not overridden" (the template value stands). They are read
	// existence-guarded in runHubsScaffold (changedString/changedBool return nil
	// for a flag that is not registered yet), so the blobs step honors them the
	// moment Task 21 registers the flags — no wiring change needed there.
	faviconOverride, logoOverride *string
	registrationOverride          *bool

	// branding is the scaffold-time branding OVERRIDE LAYER (MIO-2604): the
	// palette flags (--primary-color/--secondary-color/…) plus --branding-json,
	// with the --primary-color → header_color cascade already resolved. It is
	// layered over the template's branding block by stepBlobs
	// (hubs_scaffold_branding.go owns the whole shape). The ZERO value means "no
	// overrides" and every method on it is nil-map safe, so the unit-style step
	// tests that hand-build a scaffoldContext need no changes.
	branding scaffoldBranding

	spaceIDsBySlug, defIDsBySlug, playlistIDsByKey map[string]string

	// homePageID + homeDraftVersion are minted by the pages step (stepPages,
	// hubs_scaffold_pages.go) for the pages[] entry marked isHomepage: the tree
	// PUT returns the fresh draft_version into homeDraftVersion (the If-Match
	// OCC token its publish uses, MIO-2636), and the summary + W0 publish guard
	// read both after the run.
	homePageID       string
	homeDraftVersion int

	// pageIDsBySlug records the page id this run minted OR recovered for each
	// template pages[] slug — the per-page ids of the machine-readable result
	// (MIO-2574). BOTH apply branches fill it: the client-side loop on every
	// verdict that leaves a real page (create/resumeFull/noop), and the backend
	// op by mapping its role-keyed listing back onto the template's slugs. Only
	// ids actually observed land here, so a recorded id is always a real one.
	pageIDsBySlug map[string]string

	// publish is the --publish intent (Task 21 registers the flag; read
	// existence-guarded in runHubsScaffold, so it defaults false until then). When
	// false the publish step is a skip-with-note and the hub stays private.
	publish bool

	// Preflight state (MIO-2672, hubs_scaffold_preflight.go): the resolved live
	// catalog + its provenance, the hub template sourced from it, and the
	// instantiated (not-yet-interpolated) page plan. catalogOverride carries the
	// --catalog escape-hatch path ("" = resolve live from the target backend).
	cat             *catalog.Catalog
	hubTmpl         catalog.HubTemplate
	pagePlan        *scaffoldPlan
	catalogOverride string

	dryRun bool
	plan   *[]planEntry // collected when dryRun

	// noteW receives operator-facing progress notes from a REAL run (the §5.1
	// recovery skips: resume-onto-existing, already-applied no-op). Dry-run
	// decisions never reach it — recovery is decided at apply time, so the plan
	// cannot know them. runHubsScaffold points it at the command's stderr; a
	// nil noteW (unit-driven contexts) discards.
	noteW io.Writer
}

// notef writes one operator-facing note line (no-op when noteW is unset).
func (sc *scaffoldContext) notef(format string, args ...any) {
	if sc.noteW == nil {
		return
	}
	fmt.Fprintf(sc.noteW, format+"\n", args...)
}

// recordPageID stores the page id observed for a template page slug. It
// allocates the map on first use — the same tolerance notef has for a nil
// noteW: the unit-style step tests build a scaffoldContext carrying only the
// maps the step under test needs, and a result-only field must never turn one
// of those into a nil-map panic.
func (sc *scaffoldContext) recordPageID(slug, pageID string) {
	if slug == "" || pageID == "" {
		return
	}
	if sc.pageIDsBySlug == nil {
		sc.pageIDsBySlug = map[string]string{}
	}
	sc.pageIDsBySlug[slug] = pageID
}

// planEntry is one line of the --dry-run plan: the step name and a human
// detail (the target path + static attrs, with placeholder ids for resources
// not yet created).
type planEntry struct {
	step   string
	detail string
}

// recordPlan appends a plan entry (dry-run only; a nil plan is a no-op).
func (sc *scaffoldContext) recordPlan(step, detail string) {
	if sc.plan == nil {
		return
	}
	*sc.plan = append(*sc.plan, planEntry{step: step, detail: detail})
}

// step runs one pipeline stage: in dry-run it records the stage in the plan and
// fires NO HTTP; otherwise it runs fn. The dry-run branch lives HERE (per stage)
// — not in the runner — so a step self-annotates its own plan detail (target
// path + static attrs + placeholder ids), which only the step body knows; the
// runner just dispatches.
func (sc *scaffoldContext) step(name, detail string, fn func() error) error {
	if sc.dryRun {
		sc.recordPlan(name, detail)
		return nil
	}
	return fn()
}

// ---- step model + ordered pipeline ------------------------------------------

// scaffoldStep is one named stage of the apply pipeline: a fully-implemented
// run body that fires real HTTP (or, in dry-run, records its plan entry).
type scaffoldStep struct {
	name string
	run  func(sc *scaffoldContext, t *catalog.HubTemplate) error
}

// scaffoldPipeline is the ordered apply pipeline (design §Apply pipeline). The
// names + order are a contract the dry-run plan surfaces.
var scaffoldPipeline = []scaffoldStep{
	{"hub", stepHub},
	{"blobs", stepBlobs},
	{"spaces", stepSpaces},
	{"onboarding", stepOnboarding},
	{"policies", stepPolicies},
	{"playlists", stepPlaylists},
	{"pages", stepPages},
	{"publish", stepPublish},
	{"backend-gated", stepBackendGated},
}

// stepHub creates the hub (create mode) or records that an existing one is being
// reused (resume mode). It writes ONLY the hub's core identity (name/slug) — the
// presentation blobs (branding/favicon/settings/registration/navigation) are the
// province of stepBlobs, so nothing is applied twice (design §Apply pipeline
// step 1; the create/blobs split is documented on stepBlobs). On create it
// captures the server-assigned id + slug + title + is_private into the
// context, which every later step consumes (MIO-2543 Task 12; the title is the
// FINAL {{hub_name}} interpolation value the blobs/pages steps use).
func stepHub(sc *scaffoldContext, _ *catalog.HubTemplate) error {
	// Resume mode: the runner already resolved --hub and GET-populated hubID/
	// hubSlug/isPrivate, so there is nothing to create. Record the reuse and skip.
	if sc.hubID != "" {
		return sc.step("hub", fmt.Sprintf("using existing hub %s (resume — no create)", sc.hubID),
			func() error { return nil })
	}

	// Create mode. The template holds no hub identity; name/slug come from the
	// --name/--slug overrides. buildHubCreateAttrs is shared with `hubs create`
	// and rejects a fully-empty body (ExitUsage), so an invocation with no identity
	// flags fails fast rather than POSTing an empty create.
	detail := fmt.Sprintf("POST %s — create hub (identity only: name=%q slug=%q)",
		hubsPath(sc.teamID, ""), sc.nameOverride, sc.slugOverride)
	return sc.step("hub", detail, func() error {
		base := map[string]any{}
		if sc.nameOverride != "" {
			base["title"] = sc.nameOverride // --name maps to the hub `title` attribute
		}
		if sc.slugOverride != "" {
			base["slug"] = sc.slugOverride
		}
		// io.Discard for warnW: no blobs are passed here (identity only), so the
		// best-effort blob-key warning path is never reached.
		attrs, err := buildHubCreateAttrs(hubCreateParams{Base: base}, io.Discard)
		if err != nil {
			return err
		}
		res, err := sc.cl.Create(sc.ctx, hubsPath(sc.teamID, ""), attrs)
		if err != nil {
			return err
		}
		sc.hubID = res.ID
		sc.hubSlug, _ = res.Attributes["slug"].(string)
		sc.hubName, _ = res.Attributes["title"].(string)
		if sc.hubName == "" {
			sc.hubName = sc.nameOverride // response omitted the title — fall back to the intent
		}
		if p, ok := res.Attributes["is_private"].(bool); ok {
			sc.isPrivate, sc.isPrivateKnown = p, true
		}
		return nil
	})
}

// stepBlobs applies the template's presentation blobs onto the hub created (or
// resumed) by stepHub, via the shared read-modify-write helper applyHubBlobs
// (design §Apply pipeline step 2, MIO-2543 Task 13).
//
// CREATE/BLOBS SPLIT: stepHub writes identity only; stepBlobs owns ALL of
// branding (+ --favicon-url/--logo-url overrides), settings (incl.
// registration.enabled honoring --registration-enabled), and the navigation
// REPLACE. Each blob is therefore applied exactly once, in exactly one place —
// no double application. The template has no `meta` blob (the catalog
// hubTemplates[] schema carries no Meta field), so none is sent.
//
// It runs in STRICT key mode (a bad template branding/settings key ERRORS, not
// warns — the whole point of the feature is that a malformed template is caught,
// not silently dropped) and passes SlugKnown:true with the hub's own slug so the
// navigation href validator scopes links to THIS hub. Navigation is carried ONLY
// via blobPatches.Navigation (the seam's single nav source); applyHubBlobs
// validates its hub-scoped hrefs and injects it into the PATCH itself.
// scopeNavHrefs rewrites the template's hub-relative navigation hrefs to be
// scoped under the hub's own slug, so a slug-agnostic template href ("/content")
// becomes a within-hub link ("/{slug}/content") that passes the CLI's MIO-2270
// hub-scoping validation. Only header/footer type=="url" items with a leading
// "/" same-origin path that is NOT already scoped are rewritten; absolute
// http(s):// hrefs, protocol-relative "//" hrefs, non-url items, and
// already-scoped hrefs pass through unchanged. Mutates nav in place — stepBlobs
// therefore hands it a deep CLONE of the template's navigation, never the
// preflight-resolved template itself.
func scopeNavHrefs(nav map[string]any, slug string) {
	if slug == "" || nav == nil {
		return
	}
	prefix := "/" + slug
	for _, bucket := range []string{"header", "footer"} {
		items, ok := nav[bucket].([]any)
		if !ok {
			continue
		}
		for _, it := range items {
			obj, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := obj["type"].(string); t != "url" {
				continue
			}
			href, _ := obj["href"].(string)
			href = strings.TrimSpace(href)
			norm := strings.ReplaceAll(href, `\`, "/")
			if !strings.HasPrefix(norm, "/") || strings.HasPrefix(norm, "//") {
				continue // absolute / protocol-relative / non-path — leave as-is
			}
			if hrefScopedToHub(href, slug) {
				continue // already "/{slug}…"
			}
			if href == "/" {
				obj["href"] = prefix
			} else {
				obj["href"] = prefix + href
			}
		}
	}
}

func stepBlobs(sc *scaffoldContext, t *catalog.HubTemplate) error {
	// The plan detail names the palette the run would apply (MIO-2604) so
	// --dry-run is honest about the branding, not just the step. planDetail is
	// empty when no override was passed, keeping the override-free plan line
	// byte-identical to what it always was.
	detail := fmt.Sprintf("PATCH %s — branding+settings+navigation (strict keys)%s",
		hubsPath(sc.teamID, sc.hubIDOrPlaceholder()), sc.branding.planDetail())
	return sc.step("blobs", detail, func() error {
		// Navigation is location (c) of the {{hub_name}}/{{hub_slug}} token
		// contract (MIO-2573 §4.3: header/footer item LABELS), interpolated at
		// APPLY time with the FINAL hub name/slug — stepBlobs runs after stepHub,
		// so both values are the server-observed ones. Work on a deep CLONE
		// (CloneNode(nil) is nil-safe): the template is preflight-resolved shared
		// state, and neither interpolation nor scopeNavHrefs may mutate it.
		nav := catalog.CloneNode(t.Navigation)
		// applyHubBlobs runs the hub-scoped href check but, by design, leaves the
		// navigation SHAPE check to the CALLER (see blobPatches' doc comment): both
		// existing callers (hubs create/update) call validateNavigationBlob first,
		// and the scaffold must too. Without it a malformed template menu — a
		// non-array header/footer bucket, or items missing "type" — is neither
		// shape- nor href-validated (validateNavigationHrefs silently skips a
		// non-array bucket), gets PATCHed, and is then silently dropped by the hub
		// renderer: exactly the silent-drop trap this feature exists to eliminate.
		if nav != nil {
			if err := catalog.InterpolateNavigation(nav, sc.hubName, sc.hubSlug); err != nil {
				return errs.Wrap(errs.ExitUsage, err)
			}
			if err := validateNavigationBlob(nav); err != nil {
				return err
			}
			// A template is authored slug-agnostically (e.g. href "/content"), but
			// mio-hub mounts each hub under "/{slug}" and the CLI's MIO-2270 check
			// requires a hub-relative menu href to stay within this hub. The
			// scaffold knows the slug (from create or the resume GET), so rewrite
			// the template's hub-relative hrefs to "/{slug}/…" before applying.
			scopeNavHrefs(nav, sc.hubSlug)
		}
		// Branding is the template's block with the operator's override layer
		// merged on top (MIO-2604): --branding-json first, then the scalar palette
		// flags, then the --primary-color → header_color cascade — all resolved by
		// hubs_scaffold_branding.go. It MERGES, so a template key the operator did
		// not name survives; applyHubBlobs then deep-merges the whole thing onto
		// the hub's CURRENT branding and applies Favicon/Logo last (disjoint keys,
		// so those land exactly where they always did).
		_, err := applyHubBlobs(sc.ctx, sc.cl, sc.teamID, sc.hubID, sc.hubSlug, blobPatches{
			Branding:     sc.branding.applyTo(t.Branding),
			Settings:     t.Settings,
			Navigation:   nav,
			SlugKnown:    true,
			Favicon:      sc.faviconOverride,
			Logo:         sc.logoOverride,
			Registration: sc.registrationOverride,
			Strict:       true,
		}, io.Discard)
		return err
	})
}

// stepSpaces creates the template's discussion spaces, skipping any whose slug
// already exists on the hub (design §Apply pipeline step 3, MIO-2543 Task 14).
//
// The skip-if-exists pre-check is EXHAUSTIVE: the admin spaces list has no
// server-side slug filter, so a first-page-only scan could miss a space beyond
// page 1 and then create a duplicate (or 409). existingSpaceSlugs follows the
// pagination cursor (meta.next) to exhaustion before deciding create-vs-skip
// (design §Idempotency "lookup must be exhaustive, not first-page"). Each created
// space's id is recorded by slug so later steps can reference it.
func stepSpaces(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Spaces) == 0 {
		return sc.step("spaces", "no spaces in template", func() error { return nil })
	}
	slugs := make([]string, len(t.Spaces))
	for i, s := range t.Spaces {
		slugs[i] = s.Slug
	}
	detail := fmt.Sprintf("GET+POST %s — create missing space(s) [%s] (skip-if-slug-exists, exhaustive)",
		spacesPath(sc.teamID, sc.hubIDOrPlaceholder(), ""), strings.Join(slugs, ", "))
	return sc.step("spaces", detail, func() error {
		existing, err := sc.existingSpaceSlugs()
		if err != nil {
			return err
		}
		for _, s := range t.Spaces {
			if existing[s.Slug] {
				continue // already present — skip (idempotent resume)
			}
			attrs, berr := buildSpaceAttrs(templateSpaceInput(s))
			if berr != nil {
				return berr
			}
			res, cerr := sc.cl.Create(sc.ctx, spacesPath(sc.teamID, sc.hubID, ""), attrs)
			if cerr != nil {
				return cerr
			}
			sc.spaceIDsBySlug[s.Slug] = res.ID
		}
		return nil
	})
}

// hubIDOrPlaceholder returns the resolved hub id, or the "<hub_id>" placeholder
// when it is not yet known (dry-run create mode, where stepHub has not run) — so
// the plan detail is honest about which ids are unresolved (design §Dry-run
// fidelity).
func (sc *scaffoldContext) hubIDOrPlaceholder() string {
	if sc.hubID != "" {
		return sc.hubID
	}
	return "<hub_id>"
}

// existingSpaceSlugs returns the set of slugs of every space the admin spaces
// list exposes for the hub, following the backend's pagination cursor to
// exhaustion so the skip-if-exists pre-check cannot miss a space and create a
// duplicate (design §Idempotency; the list exposes no server-side slug filter).
//
// Cursor convention (verified): the mio-backend standard pagination envelope
// (app/infrastructure/pagination.py) carries the next page's cursor at
// meta.page.next_cursor, gated by meta.page.has_more — the same cursor also
// appears in links.next. nextPageCursor reads that PROVEN field, NOT a bare
// meta.next (which is only a synthetic test-fixture shape, never emitted here).
//
// NOTE (verified against mio-backend spaces_admin.py + services/spaces.py): the
// admin spaces list currently emits NO pagination meta — admin_list_spaces calls
// list_spaces_for_hub and _list_response drops the cursor — so nextPageCursor
// returns "" and this stops after one page today. Following the documented cursor
// keeps the pre-check exhaustive-by-construction the moment the endpoint starts
// paging; until then, exhaustiveness is bounded by that first page (see the
// caller's concern note). The seen-cursor set + maxPages bound are a stall guard:
// a buggy server returning a stable/looping non-empty cursor can never spin here.
func (sc *scaffoldContext) existingSpaceSlugs() (map[string]bool, error) {
	slugs := map[string]bool{}
	seen := map[string]bool{}
	query := url.Values{}
	const maxPages = 1000 // hard ceiling (~20k spaces at page_size 20); a hub never has that many
	for page := 0; page < maxPages; page++ {
		col, err := sc.cl.List(sc.ctx, spacesPath(sc.teamID, sc.hubID, ""), query)
		if err != nil {
			return nil, err
		}
		for _, r := range col.Data {
			if s, ok := r.Attributes["slug"].(string); ok && s != "" {
				slugs[s] = true
			}
		}
		next := nextPageCursor(col)
		if next == "" || seen[next] {
			break // no more pages, or a repeated cursor (buggy server) — stop
		}
		seen[next] = true
		query = url.Values{}
		query.Set("page[after]", next)
	}
	return slugs, nil
}

// nextPageCursor extracts the cursor for the NEXT page from a collection per the
// mio-backend pagination envelope: meta.page.next_cursor, gated by
// meta.page.has_more (an explicit has_more:false stops paging). It returns "" when
// there is no next page (including when the response carries no pagination meta,
// as the admin spaces list does today).
func nextPageCursor(col *client.Collection) string {
	page, ok := col.Meta["page"].(map[string]any)
	if !ok {
		return ""
	}
	if hasMore, present := page["has_more"].(bool); present && !hasMore {
		return ""
	}
	cur, _ := page["next_cursor"].(string)
	return cur
}

// templateSpaceInput maps a template Space onto the SpaceInput the shared
// buildSpaceAttrs consumes. Only the fields the template models are set; the
// pointer fields stay nil (unset) for everything else, preserving buildSpaceAttrs'
// partial-write semantics. buildSpaceAttrs still enforces the access_level /
// posting_permission enum checks, so a scaffold gets the same validation the
// `community spaces create` command does.
func templateSpaceInput(s catalog.TemplateSpace) SpaceInput {
	in := SpaceInput{Slug: &s.Slug}
	if s.Name != "" {
		in.Name = &s.Name
	}
	if s.Description != "" {
		in.Description = &s.Description
	}
	if s.AccessLevel != "" {
		in.AccessLevel = &s.AccessLevel
	}
	if s.PostingPermission != "" {
		in.PostingPermission = &s.PostingPermission
	}
	return in
}

// stepOnboarding builds the hub's onboarding schema: for each template attribute
// it creates the contact-attribute definition (skipping any whose slug already
// exists on the team — reusing that def's id) and then ENABLEs it on the hub via a
// hub-config CREATE (design §Apply pipeline step 4, MIO-2543 Task 15).
//
// Two idempotency mechanisms, one per resource:
//   - Definitions are team-scoped and have no server-side slug filter, so the
//     skip-if-slug-exists pre-check is EXHAUSTIVE (existingContactAttrDefs follows
//     the pagination cursor to exhaustion, same convention + stall guard as
//     stepSpaces). A def found on any page is reused, never duplicated.
//   - The hub-config CREATE is a backend UPSERT (mio-backend
//     contact_attributes.service.upsert_hub_config → hub_config_repo.upsert), so
//     POSTing the same (hub, definition) again just re-applies the config — no 409,
//     no duplicate. That is why the step can always POST the config without a
//     pre-check and stay idempotent on resume.
//
// The hub-config POST goes to the COLLECTION path (empty def segment) with
// definition_id IN THE BODY — the MIO-2502 fix; the /{definition_id}-suffixed path
// only supports PATCH/DELETE. is_in_onboarding + is_required carry the template's
// intent (mapping Required through too, so a required-in-template attribute is
// never silently dropped).
func stepOnboarding(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Onboarding) == 0 {
		return sc.step("onboarding", "no onboarding attributes in template", func() error { return nil })
	}
	slugs := make([]string, len(t.Onboarding))
	for i, d := range t.Onboarding {
		slugs[i] = d.Slug
	}
	detail := fmt.Sprintf("GET+POST %s then POST %s — create missing def(s) [%s] (skip-if-slug-exists, exhaustive) + enable on hub (definition_id in body, is_in_onboarding)",
		contactAttributesDefsPath(sc.teamID, ""),
		contactAttributesHubConfigPath(sc.teamID, sc.hubIDOrPlaceholder(), ""),
		strings.Join(slugs, ", "))
	return sc.step("onboarding", detail, func() error {
		existing, err := sc.existingContactAttrDefs()
		if err != nil {
			return err
		}
		for _, d := range t.Onboarding {
			defID := existing[d.Slug]
			if defID == "" {
				attrs, berr := buildAttrDefCreateAttrs(templateAttrDefInput(d))
				if berr != nil {
					return berr
				}
				res, cerr := sc.cl.Create(sc.ctx, contactAttributesDefsPath(sc.teamID, ""), attrs)
				if cerr != nil {
					return cerr
				}
				defID = res.ID
			}
			sc.defIDsBySlug[d.Slug] = defID

			// Enable on the hub via the COLLECTION path with the def id in the body
			// (MIO-2502). The backend upserts, so re-running is idempotent.
			inOnboarding, required := d.InOnboarding, d.Required
			cfg := buildHubConfigAttrs(defID, HubConfigInput{
				IsInOnboarding: &inOnboarding,
				IsRequired:     &required,
			})
			if _, cerr := sc.cl.Create(sc.ctx, contactAttributesHubConfigPath(sc.teamID, sc.hubID, ""), cfg); cerr != nil {
				return cerr
			}
		}
		return nil
	})
}

// existingContactAttrDefs returns slug→id for every contact-attribute definition
// on the team, following the backend's pagination cursor to exhaustion (same
// meta.page.next_cursor convention + seen-cursor/maxPages stall guard as
// existingSpaceSlugs) so the onboarding skip-if-exists pre-check reuses a def
// created on a prior run instead of duplicating it. The defs list exposes no
// server-side slug filter, so a first-page-only scan would be a resume bug.
func (sc *scaffoldContext) existingContactAttrDefs() (map[string]string, error) {
	bySlug := map[string]string{}
	seen := map[string]bool{}
	query := url.Values{}
	const maxPages = 1000 // hard ceiling; a team never has this many attribute defs
	for page := 0; page < maxPages; page++ {
		col, err := sc.cl.List(sc.ctx, contactAttributesDefsPath(sc.teamID, ""), query)
		if err != nil {
			return nil, err
		}
		for _, r := range col.Data {
			if s, ok := r.Attributes["slug"].(string); ok && s != "" {
				bySlug[s] = r.ID
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
	return bySlug, nil
}

// templateAttrDefInput maps a template AttrDef onto the AttrDefInput the shared
// buildAttrDefCreateAttrs consumes. Slug + FieldType are always present (the
// template validator guarantees them); Name is set only when non-empty.
// buildAttrDefCreateAttrs still validates the field_type enum, so the scaffold
// gets the same check `contact-attributes create` does.
func templateAttrDefInput(d catalog.TemplateAttrDef) AttrDefInput {
	in := AttrDefInput{Slug: &d.Slug, FieldType: &d.FieldType}
	if d.Name != "" {
		in.Name = &d.Name
	}
	return in
}

// policyTypeAliases resolves a template policy key to the backend policy_type
// enum. The template models policies as a map keyed by policy identifier; both the
// canonical backend types ("tos"/"privacy_policy") and friendly aliases
// ("terms"/"privacy") are accepted so a template reads naturally while still
// hitting the validated enum. A key absent here is an ERROR (fail loud).
var policyTypeAliases = map[string]string{
	"tos":              "tos",
	"terms":            "tos",
	"terms_of_service": "tos",
	"privacy":          "privacy_policy",
	"privacy_policy":   "privacy_policy",
}

// stepPolicies applies the template's legal policies via the shared
// applyHubPolicies helper — one PATCH per policy, keys sorted for deterministic
// ordering (design §Apply pipeline step 5, MIO-2543 Task 16). An empty/nil
// Policies map is a clean no-op.
func stepPolicies(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Policies) == 0 {
		return sc.step("policies", "no policies in template", func() error { return nil })
	}
	keys := make([]string, 0, len(t.Policies))
	for k := range t.Policies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	detail := fmt.Sprintf("PATCH %s — set policy(ies) [%s]",
		hubsPoliciesPath(sc.teamID, sc.hubIDOrPlaceholder()), strings.Join(keys, ", "))
	return sc.step("policies", detail, func() error {
		for _, key := range keys {
			pol, err := templateHubPolicy(key, t.Policies[key])
			if err != nil {
				return err
			}
			if _, err := applyHubPolicies(sc.ctx, sc.cl, sc.teamID, sc.hubID, pol); err != nil {
				return err
			}
		}
		return nil
	})
}

// templateHubPolicy maps one template policy entry (key + value) onto the
// hubPolicy the shared applyHubPolicies consumes. The key resolves to the backend
// policy_type via policyTypeAliases (an unknown key ERRORS — never a silent drop).
//
// CONTENT SEMANTICS (template authors take note): a template policy that OMITS
// "content" does NOT mean "leave the existing policy content unchanged" — it maps
// to a nil hubPolicy.Content, and applyHubPolicies ALWAYS sends content (nil →
// JSON null), so an omitted content RESETS the policy to the backend default on
// every apply. This is correct-by-contract for the declarative scaffold (the
// template is the source of truth), but it means a template that only wants to
// flip require_acceptance still reverts custom content to default. To keep custom
// content, put it in the template. "require_acceptance" (or its friendly alias
// "required") sets require_acceptance; nil omits it (partial update).
func templateHubPolicy(key string, raw any) (hubPolicy, error) {
	policyType, ok := policyTypeAliases[key]
	if !ok {
		return hubPolicy{}, errs.New(errs.ExitUsage,
			"invalid policy %q: must be one of tos, terms, privacy, privacy_policy", key)
	}
	p := hubPolicy{PolicyType: policyType}
	val, _ := raw.(map[string]any)
	if c, ok := val["content"].(string); ok {
		content := c
		p.Content = &content
	}
	if ra, ok := val["require_acceptance"].(bool); ok {
		p.RequireAcceptance = &ra
	} else if ra, ok := val["required"].(bool); ok {
		p.RequireAcceptance = &ra
	}
	return p, nil
}

// stepPlaylists creates the template's playlists, adds their items, and publishes
// them to the hub — GATED on the O1 decision (option c: playlists are create-only,
// so if the hub already has ≥1 published playlist the ENTIRE step is skipped; that
// gate IS the idempotency mechanism — item-level idempotency is therefore N/A).
// On a fresh hub it creates each team playlist, adds an item per file id, and
// publishes it with published_at set to NOW unconditionally (sidesteps MIO-2536)
// and visibility public, POSTing playlist_id in the BODY to the hub-playlists
// COLLECTION path (design §Apply pipeline step 6, MIO-2543 Task 17).
func stepPlaylists(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Playlists) == 0 {
		return sc.step("playlists", "no playlists in template", func() error { return nil })
	}
	keys := make([]string, len(t.Playlists))
	for i, p := range t.Playlists {
		keys[i] = p.Key
	}
	detail := fmt.Sprintf("GET %s (gate — skip all if the hub already has published playlists) else create playlist(s) [%s], add items per file, and publish each to the hub (playlist_id in body, visibility=public, published_at=now)",
		hubPlaylistsPath(sc.teamID, sc.hubIDOrPlaceholder(), ""), strings.Join(keys, ", "))
	return sc.step("playlists", detail, func() error {
		// O1 gate: if the hub already has ≥1 published playlist, skip the whole step.
		// One page suffices — any data means non-empty (create-only, no resume merge).
		existing, err := sc.cl.List(sc.ctx, hubPlaylistsPath(sc.teamID, sc.hubID, ""), url.Values{})
		if err != nil {
			return err
		}
		if len(existing.Data) > 0 {
			return nil
		}
		for _, p := range t.Playlists {
			in := PlaylistInput{Title: &p.Title}
			if p.Visibility != "" {
				vis := p.Visibility
				in.Visibility = &vis
			}
			res, cerr := sc.cl.Create(sc.ctx, playlistsPath(sc.teamID, ""), buildPlaylistCreateAttrs(in))
			if cerr != nil {
				return cerr
			}
			playlistID := res.ID
			sc.playlistIDsByKey[p.Key] = playlistID

			for _, fileID := range p.FileIDs {
				if _, ierr := sc.cl.Create(sc.ctx, playlistItemsPath(sc.teamID, playlistID, ""),
					map[string]any{"file_id": fileID}); ierr != nil {
					return ierr
				}
			}

			// Publish to the hub: published_at set unconditionally (sidesteps
			// MIO-2536), visibility public, playlist_id in the body → COLLECTION path.
			pubAttrs, berr := buildHubMediaPublishAttrs("public", time.Now().UTC(), nil)
			if berr != nil {
				return berr
			}
			pubAttrs["playlist_id"] = playlistID
			if _, perr := sc.cl.Create(sc.ctx, hubPlaylistsPath(sc.teamID, sc.hubID, ""), pubAttrs); perr != nil {
				return perr
			}
		}
		return nil
	})
}

// attrInt coerces a JSON:API attribute to an int. JSON numbers decode as float64
// through the client's standard resource decode; int is accepted for
// hand-built values. A non-numeric/absent value reports ok=false.
func attrInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// stepPublish flips the hub public (is_private:false) — but ONLY when --publish is
// set. Without it the scaffold leaves the hub PRIVATE (a deliberate default: a
// half-configured hub should not go live by accident) and records a skip note
// pointing at --publish (design §Apply pipeline step 8, MIO-2543 Task 19).
//
// It PATCHes via publishedStateAttrs(true) — the single source of truth for the
// published→is_private inversion — so "published" (not a writable attribute) is
// never sent; only is_private is.
func stepPublish(sc *scaffoldContext, _ *catalog.HubTemplate) error {
	if !sc.publish {
		return sc.step("publish",
			"skipped — hub stays private (pass --publish to go live)",
			func() error { return nil })
	}
	detail := fmt.Sprintf("PATCH %s — set is_private=false (publish the hub public)",
		hubsPath(sc.teamID, sc.hubIDOrPlaceholder()))
	return sc.step("publish", detail, func() error {
		res, err := sc.cl.Update(sc.ctx, hubsPath(sc.teamID, sc.hubID), publishedStateAttrs(true))
		if err != nil {
			return err
		}
		// Reflect the new public state back into the context. A successful publish
		// PATCH means the hub IS public regardless of what the response echoes, so the
		// state is now definitively known.
		sc.isPrivate, sc.isPrivateKnown = false, true
		if res != nil {
			if p, ok := res.Attributes["is_private"].(bool); ok {
				sc.isPrivate = p
			}
		}
		return nil
	})
}

// stepBackendGated is the terminal skip-with-note: the welcome discussion post
// (backend MIO-2262 admin create-discussion) and the auto-assign-admin flow
// (backend MIO-2540) are not CLI-doable — no endpoint exists yet — so this step
// fires NO request and records exactly what is deferred and why, so an operator
// (or a future revision) knows precisely which endpoints to wire in when they land
// (design §Apply pipeline step 9, MIO-2543 Task 20).
func stepBackendGated(sc *scaffoldContext, _ *catalog.HubTemplate) error {
	return sc.step("backend-gated",
		"welcome post (MIO-2262) and auto-admin (MIO-2540) skipped — need backend endpoints; wire in when they land",
		func() error { return nil })
}

// scaffoldAfterResolve, when non-nil, is invoked with the fully-resolved
// scaffoldContext just before the pipeline runs. It is a TEST SEAM only (so a
// test can assert the resolved context — e.g. hubSlug populated in resume mode)
// and is nil in production.
var scaffoldAfterResolve func(*scaffoldContext)

// ---- command ----------------------------------------------------------------

var hubsScaffoldCmd = &cobra.Command{
	Use:   "scaffold",
	Short: "Scaffold a full-experience hub from a template.",
	Long: `Create a hub and apply a full-experience template — branding/logo/favicon,
navigation, registration, discussion spaces, onboarding schema, policies,
playlists and a homepage — by orchestrating the CLI's own request-builders and
client layer (strictly CLI-only; never raw REST).

Create mode (default) uses --name/--slug to create a new hub. Resume/target mode
uses --hub <id> to apply onto an existing hub (also how you resume after a
mid-pipeline failure). --dry-run prints the ordered plan and makes no changes.

Brand the hub in the SAME command: --primary-color/--secondary-color/
--text-color/--background-color/--header-color/--header-accent, plus
--logo-url/--favicon-url/--social-image-url, each merge over the template's
branding block (they do not replace it). --branding-json merges a whole object
the same way, and the scalar flags win over the keys in it. --primary-color also
fills header_color unless you give a header color yourself.

Output follows --output: json (the default off a TTY) and plain emit the result
— the new hub's id/slug plus the page, space, onboarding and playlist ids the
run created — while table prints the human summary. Progress notes always go to
stderr, so a json stdout stays parseable.`,
	Example: `  mio hubs scaffold --template community --name "My Community" --slug my-community
  mio hubs scaffold --template community --name "My Community" --slug my-community --dry-run
  mio hubs scaffold --template community --name Acme --slug acme --primary-color '#B91C1C' --secondary-color '#F59E0B'
  mio hubs scaffold --template community --name Acme --slug acme --branding-json '{"primary":"#B91C1C","font_body":"Inter"}'
  mio hubs scaffold --template community --hub hub_abc123
  HUB_ID=$(mio hubs scaffold --template community --name "My Community" --slug my-community --jq .hub_id)`,
	Args: cobra.NoArgs,
	RunE: runHubsScaffold,
}

func runHubsScaffold(cmd *cobra.Command, _ []string) error {
	// 1. Read the template ID. Existence + validity are checked by
	//    scaffoldPreflight against the TARGET BACKEND's live catalog (spec §0:
	//    the CLI holds no templates), so there is nothing to load here.
	templateID, ferr := cmd.Flags().GetString("template")
	if ferr != nil {
		return errs.New(errs.ExitUsage, "--template: %s", ferr.Error())
	}

	// 1b. Resolve the branding override layer (MIO-2604) BEFORE auth/team, so a
	//     malformed --branding-json or an unknown branding key exits ExitUsage
	//     with no HTTP request at all — the same pre-auth discipline
	//     `hubs update` follows for its blob flags. The cascade
	//     (--primary-color → header_color) is resolved here too, once, so every
	//     consumer — stepBlobs, the dry-run plan, the summary, the machine
	//     result and the resume command — reads the SAME answer.
	branding, berr := resolveScaffoldBranding(cmd)
	if berr != nil {
		return berr
	}

	// 2. Resolve auth + team ONCE (shared by every step). With an id-shaped
	//    --team this fires no HTTP.
	c, teamID, err := hubsContext(cmd)
	if err != nil {
		return err
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Name/slug are core flags (Task 11), always registered. The favicon/logo/
	// registration overrides are Phase-5 flags (Task 21); changedString/changedBool
	// return nil for a not-yet-registered flag, so reading them here is safe and
	// forward-compatible — stepBlobs honors them automatically once Task 21 lands.
	nameOverride, _ := cmd.Flags().GetString("name")
	slugOverride, _ := cmd.Flags().GetString("slug")

	// --publish is a Phase-5 flag (Task 21). GetBool returns (false, err) for a
	// not-yet-registered flag, so reading it existence-guarded defaults publish to
	// false — the scaffold leaves the hub private until Task 21 registers the flag.
	publish, _ := cmd.Flags().GetBool("publish")

	catalogOverride, _ := cmd.Flags().GetString("catalog")

	var plan []planEntry
	sc := &scaffoldContext{
		ctx:                  c.ctx,
		cl:                   c.client,
		teamID:               teamID,
		nameOverride:         nameOverride,
		slugOverride:         slugOverride,
		faviconOverride:      changedString(cmd, "favicon-url"),
		logoOverride:         changedString(cmd, "logo-url"),
		registrationOverride: changedBool(cmd, "registration-enabled"),
		branding:             branding,
		spaceIDsBySlug:       map[string]string{},
		defIDsBySlug:         map[string]string{},
		playlistIDsByKey:     map[string]string{},
		publish:              publish,
		catalogOverride:      strings.TrimSpace(catalogOverride),
		dryRun:               dryRun,
		plan:                 &plan,
		noteW:                cmd.ErrOrStderr(),
	}

	// 3. Resume/target mode: an EXPLICIT --hub applies onto an existing hub. The
	//    discriminator MUST be the explicit flag (flags.hub, set only by --hub),
	//    NOT c.resolved.HubID — the latter merges a config/profile default hub
	//    (`mio config set current_hub`), so gating on it would silently turn the headline
	//    CREATE invocation into a RESUME for any user with a default hub. ResolveHub
	//    short-circuits an id-shaped value (no lookup), so a bare id leaves hubSlug
	//    empty — GET the hub to populate hubSlug + isPrivate, which the navigation
	//    href validators (step 2) require. Create mode leaves the hub for stepHub
	//    (Phase 4). --hub is the persistent root flag inherited by every subcommand.
	if flags.hub != "" {
		hubID, herr := c.client.ResolveHub(sc.ctx, teamID, flags.hub)
		if herr != nil {
			return errs.Wrap(errs.ExitUsage, herr)
		}
		res, rerr := c.client.Retrieve(sc.ctx, hubsPath(teamID, hubID))
		if rerr != nil {
			return rerr
		}
		sc.hubID = hubID
		sc.hubSlug, _ = res.Attributes["slug"].(string)
		sc.hubName, _ = res.Attributes["title"].(string)
		if p, ok := res.Attributes["is_private"].(bool); ok {
			sc.isPrivate, sc.isPrivateKnown = p, true
		}
	}

	if scaffoldAfterResolve != nil {
		scaffoldAfterResolve(sc)
	}

	// 4. WRITE-FREE preflight (MIO-2573 §5): resolve the catalog LIVE from the
	//    target backend (or the --catalog override), source + validate the hub
	//    template from it, instantiate the page plan, and preliminarily
	//    interpolate the whole plan — all BEFORE stepHub writes anything. On
	//    failure return directly: nothing was written, so there is no recovery
	//    guidance to print.
	if perr := scaffoldPreflight(cmd, sc, templateID); perr != nil {
		return perr
	}

	// 5. Run the pipeline in order. Each step decides for itself whether to fire
	//    HTTP or (in dry-run) record its plan entry — see sc.step — so the runner
	//    just dispatches and, on failure, prints the recovery guidance and returns
	//    the step-tagged error (rendered once by main.go).
	for _, step := range scaffoldPipeline {
		if serr := step.run(sc, &sc.hubTmpl); serr != nil {
			printScaffoldRecovery(cmd.ErrOrStderr(), sc, templateID)
			return errs.Wrap(errs.CodeOf(serr), scaffoldStepError(sc, step.name, serr))
		}
	}

	// 6. Emit the result. BOTH surfaces are format-driven (MIO-2574): the prose
	//    below is the `table` rendering, and json/plain go through the shared
	//    output layer so an agent reads the hub id off stdout instead of
	//    string-scraping the summary (or, as QA had to, re-listing hubs
	//    out-of-band to find what it had just created). Off a TTY the resolved
	//    default is already json — the agent contract AGENTS.md documents and
	//    every other command honors; this command was the one that ignored it.
	//    Every progress line the run emits already goes to STDERR (sc.notef, the
	//    catalog provenance + warnings, the recovery guidance), so stdout carries
	//    the rendered value and nothing else.
	if dryRun {
		if scaffoldHumanOutput(c) {
			printScaffoldPlan(cmd.OutOrStdout(), templateID, plan)
			return nil
		}
		return c.render(cmd, scaffoldPlanResult(templateID, plan))
	}
	// Real run: echo the finished hub's reference + published state + a recap so
	// the operator knows what landed and how to go live (design §Apply pipeline
	// step 8 + §Command surface).
	if scaffoldHumanOutput(c) {
		printScaffoldSummary(cmd.OutOrStdout(), sc, &sc.hubTmpl, templateID)
		return nil
	}
	return c.render(cmd, scaffoldResult(sc, templateID))
}

// scaffoldHumanOutput reports whether this invocation wants the prose summary/
// plan rather than the machine-readable result: the `table` format (the TTY
// default), and only with no --jq in play. --jq is a filter over the RESULT, so
// honoring it means rendering the structured value — exactly what every other
// command does — never prose a gojq program cannot address.
func scaffoldHumanOutput(c *cmdContext) bool {
	return c.out.Format == output.FormatTable && c.out.JQ == ""
}

// scaffoldStepError tags a mid-pipeline failure with the failing step and, once
// a hub EXISTS, with its id. The id belongs in the error itself because the
// JSON:API envelope main.go writes to stderr is the machine-readable FAILURE
// contract, and a scaffold that dies after step 1 has already created a hub
// that is never rolled back (§Idempotency & recovery): losing that id is
// precisely the pain MIO-2574 is about. The operator-facing resume command
// still comes from printScaffoldRecovery — this is the same fact, in the one
// place a machine reads.
func scaffoldStepError(sc *scaffoldContext, step string, err error) error {
	if sc.hubID == "" {
		return fmt.Errorf("scaffold: step %q failed: %w", step, err)
	}
	return fmt.Errorf("scaffold: step %q failed (hub %s was created and is NOT rolled back): %w",
		step, sc.hubID, err)
}

// printScaffoldPlan writes the ordered dry-run plan. Each step is named on its
// own numbered line (with an optional detail Phase 4 supplies).
func printScaffoldPlan(w io.Writer, templateID string, plan []planEntry) {
	fmt.Fprintf(w, "Scaffold plan for template %q (dry-run — no changes made):\n", templateID)
	for i, e := range plan {
		if e.detail != "" {
			fmt.Fprintf(w, "  %d. %s — %s\n", i+1, e.step, e.detail)
		} else {
			fmt.Fprintf(w, "  %d. %s\n", i+1, e.step)
		}
	}
}

// printScaffoldRecovery implements the recovery contract (design §Idempotency &
// recovery): scaffold does not roll back, so on a step failure it prints the
// created hub id (if any) and the exact command to resume onto it. It does NOT
// print the error text — main.go renders that once from the returned error, so
// printing it here too would double it on stderr.
func printScaffoldRecovery(w io.Writer, sc *scaffoldContext, templateID string) {
	if sc.hubID == "" {
		return
	}
	fmt.Fprintf(w, "Hub %s was created but the scaffold did not finish (no rollback).\n", sc.hubID)
	// Reconstruct an EXACT resume command that preserves the caller's intent —
	// team, --publish, and the presentation overrides — so following it verbatim
	// doesn't leave a requested-published hub private or revert an override back
	// to the template default.
	parts := []string{"mio hubs scaffold", "--hub " + sc.hubID, "--template " + templateID}
	if sc.teamID != "" {
		parts = append(parts, "--team "+sc.teamID)
	}
	if sc.publish {
		parts = append(parts, "--publish")
	}
	if sc.faviconOverride != nil {
		parts = append(parts, fmt.Sprintf("--favicon-url %q", *sc.faviconOverride))
	}
	if sc.logoOverride != nil {
		parts = append(parts, fmt.Sprintf("--logo-url %q", *sc.logoOverride))
	}
	if sc.registrationOverride != nil {
		parts = append(parts, fmt.Sprintf("--registration-enabled=%t", *sc.registrationOverride))
	}
	// The branding override layer (MIO-2604) is part of that intent too: resuming
	// without it would rebuild the hub in the TEMPLATE's palette, silently undoing
	// the whole point of the flags. flagArgs echoes what the operator passed and
	// omits the cascaded header_color — the resume re-derives it from
	// --primary-color.
	parts = append(parts, sc.branding.flagArgs()...)
	fmt.Fprintf(w, "Resume with: %s\n", strings.Join(parts, " "))
}

// printScaffoldSummary writes the end-of-run summary for a REAL (non-dry-run)
// scaffold (design §Apply pipeline step 8 + §Command surface):
//
//   - the hub's reference: its slug + id. NOTE (MIO-2521): the public hub URL is
//     NOT returned by the API and cannot be derived by the CLI, so we echo the
//     slug + id + the HOST-RELATIVE form (<your-hub-frontend-host>/<slug>) instead
//     of fabricating an absolute URL that could point at the wrong host;
//   - the published/private state: read from the REAL server state (isPrivate),
//     NOT the --publish intent — so a resume onto an already-public hub reports
//     LIVE, and a publish that left it private is never mislabeled. When the state
//     was never observed (isPrivateKnown false) it falls back to PRIVATE (safe);
//   - a recap of the hub's SHAPE from the template (space/playlist/onboarding/
//     policy counts + homepage). Worded "Includes:" — not "Applied:" — because on a
//     resume the skip-if-exists / O1-gate steps converge WITHOUT re-creating, so the
//     line must describe what the hub contains, not claim this run wrote it all.
func printScaffoldSummary(w io.Writer, sc *scaffoldContext, t *catalog.HubTemplate, templateID string) {
	fmt.Fprintf(w, "Scaffolded hub %q (id %s) from template %q.\n", sc.hubSlug, sc.hubID, templateID)

	// Shape recap: counts come from the template (the experience the hub converged
	// to), so a resume run still reports the full shape rather than only the
	// resources this invocation happened to create.
	parts := []string{
		fmt.Sprintf("%d space(s)", len(t.Spaces)),
		fmt.Sprintf("%d playlist(s)", len(t.Playlists)),
	}
	if len(t.Onboarding) > 0 {
		parts = append(parts, fmt.Sprintf("%d onboarding attribute(s)", len(t.Onboarding)))
	}
	if len(t.Policies) > 0 {
		parts = append(parts, fmt.Sprintf("%d policy(ies)", len(t.Policies)))
	}
	// Every template page the pages step applies, slug by slug, with the
	// homepage entry marked (MIO-2672 Task 7: the recap covers ALL pages[]
	// entries, not just the homepage).
	if len(t.Pages) > 0 {
		names := make([]string, len(t.Pages))
		for i, p := range t.Pages {
			names[i] = p.Slug
			if p.IsHomepage {
				names[i] += " (homepage)"
			}
		}
		parts = append(parts, "page(s): "+strings.Join(names, ", "))
	}
	fmt.Fprintf(w, "  Includes: %s.\n", strings.Join(parts, ", "))

	// Branding overrides (MIO-2604), printed ONLY when the operator passed some —
	// so the summary of an override-free run stays byte-for-byte what it always
	// was (TestScaffold_TableSummaryUnchanged). When they did, the line is the one
	// place that shows the cascade actually fired.
	if !sc.branding.empty() {
		fmt.Fprintf(w, "  Branding overrides: %s.\n", sc.branding.describe())
	}

	// Published state, read from the REAL server-observed is_private (published =
	// !isPrivate). Only claim LIVE when the state is KNOWN and public; an unknown
	// state falls back to PRIVATE (the safe direction) rather than a bool-zero LIVE.
	published := sc.isPrivateKnown && !sc.isPrivate
	if published {
		fmt.Fprintln(w, "  State: LIVE — the hub is published (public).")
	} else {
		fmt.Fprintf(w, "  State: PRIVATE — the hub is not published yet. Publish with: mio hubs update %s --published (or re-run scaffold with --publish).\n", sc.hubID)
	}

	// Public URL (MIO-2521): host-relative only — the CLI cannot know the hub
	// frontend host, so the operator substitutes it.
	fmt.Fprintf(w, "  Public URL: <your-hub-frontend-host>/%s (the API does not return the hub's public URL; substitute your hub frontend host).\n", sc.hubSlug)
}

// ---- machine-readable result (MIO-2574) --------------------------------------

// scaffoldResult builds the value `--output json|plain` renders for a REAL
// scaffold run: the same facts printScaffoldSummary narrates, in a shape an
// agent can read a single field off.
//
// Shape decisions worth knowing before changing anything here:
//
//   - hub_id leads, because the reported bug is exactly that an agent could not
//     recover the id of the hub it had just created and had to scrape
//     `mio hubs list` out-of-band to find it;
//   - hub_path, NOT hub_url: the API does not return the hub's public URL and
//     the CLI cannot know the hub frontend host (MIO-2521, the reason the prose
//     prints "<your-hub-frontend-host>/<slug>"), so we emit the host-relative
//     path and never fabricate an absolute URL that could name the wrong host;
//   - published mirrors the summary's LIVE/PRIVATE label — read from the REAL
//     server-observed is_private, fail-safe (state unknown ⇒ not published) —
//     and uses the CLI's own `published` vocabulary, the derived field
//     injectHubDerivedState already adds to a rendered hub resource;
//   - the per-resource arrays follow the TEMPLATE's order, like the summary's
//     template-derived "Includes:" recap: they describe what the hub CONTAINS,
//     so a resume run reports the full shape rather than only what this
//     invocation happened to create. An id this run never learned (a space that
//     already existed, a page the backend op did not list) is JSON null, never
//     an empty string — a present value is always a real id;
//   - every key is emitted unconditionally, empty arrays included: a machine
//     contract whose keys come and go with the template is not one an agent can
//     write `.spaces | length` against;
//   - branding_overrides (MIO-2604) reports the RESOLVED override layer — the
//     palette flags and --branding-json keys, CASCADE INCLUDED — so a caller can
//     see what the CLI actually sent (notably the header_color it derived from
//     --primary-color) without a second GET. It is the override layer, not the
//     hub's final branding: template defaults the operator never touched are not
//     in it, and `mio hubs retrieve <id> --jq .branding` remains the way to read
//     the whole blob back. `{}` when nothing was overridden.
func scaffoldResult(sc *scaffoldContext, templateID string) map[string]any {
	t := &sc.hubTmpl

	pages := make([]any, 0, len(t.Pages))
	for _, p := range t.Pages {
		pages = append(pages, map[string]any{
			"slug":          p.Slug,
			"role":          p.Role,
			"page_template": p.PageTemplate,
			"is_homepage":   p.IsHomepage,
			"page_id":       nilIfEmpty(sc.pageIDsBySlug[p.Slug]),
		})
	}
	spaces := make([]any, 0, len(t.Spaces))
	for _, s := range t.Spaces {
		spaces = append(spaces, map[string]any{
			"slug":     s.Slug,
			"name":     s.Name,
			"space_id": nilIfEmpty(sc.spaceIDsBySlug[s.Slug]),
		})
	}
	onboarding := make([]any, 0, len(t.Onboarding))
	for _, d := range t.Onboarding {
		onboarding = append(onboarding, map[string]any{
			"slug":          d.Slug,
			"definition_id": nilIfEmpty(sc.defIDsBySlug[d.Slug]),
		})
	}
	playlists := make([]any, 0, len(t.Playlists))
	for _, p := range t.Playlists {
		playlists = append(playlists, map[string]any{
			"key":         p.Key,
			"title":       p.Title,
			"playlist_id": nilIfEmpty(sc.playlistIDsByKey[p.Key]),
		})
	}
	policies := make([]string, 0, len(t.Policies))
	for k := range t.Policies {
		policies = append(policies, k)
	}
	sort.Strings(policies) // same deterministic ordering stepPolicies applies in

	out := map[string]any{
		"dry_run":               false,
		"hub_id":                sc.hubID,
		"hub_slug":              sc.hubSlug,
		"hub_name":              sc.hubName,
		"hub_path":              nilIfEmpty(hubPublicPath(sc.hubSlug)),
		"published":             sc.isPrivateKnown && !sc.isPrivate,
		"template_id":           templateID,
		"catalog_revision":      nil,
		"branding_overrides":    sc.branding.resolved(),
		"homepage_page_id":      nilIfEmpty(scaffoldHomepageID(sc)),
		"pages":                 pages,
		"spaces":                spaces,
		"onboarding_attributes": onboarding,
		"playlists":             playlists,
		"policies":              policies,
	}
	// The catalog revision the template was sourced from — the provenance the
	// stderr "catalog: …" line carries for a human, so a machine run can record
	// which catalog produced this hub. nil when no catalog was resolved (only
	// reachable from a hand-built context; production always has one).
	if sc.cat != nil {
		out["catalog_revision"] = sc.cat.Meta.Revision
	}
	return out
}

// scaffoldHomepageID returns the homepage page id as far as THIS run knows it:
// the id recorded for the slug of the template's isHomepage entry, falling back
// to the homePageID the pages step captured (the backend-op branch sets that
// one from the op's ROLE-keyed listing, which may name a role no template entry
// claims). "" when neither is known — an op response that listed no pages, or a
// hand-built context — which the caller renders as null.
func scaffoldHomepageID(sc *scaffoldContext) string {
	for _, p := range sc.hubTmpl.Pages {
		if p.IsHomepage {
			if id := sc.pageIDsBySlug[p.Slug]; id != "" {
				return id
			}
		}
	}
	return sc.homePageID
}

// hubPublicPath is the host-relative public path of a hub — the machine-readable
// half of the summary's "<your-hub-frontend-host>/<slug>" line (MIO-2521). Empty
// slug in, empty path out (never a bare "/").
func hubPublicPath(slug string) string {
	if slug == "" {
		return ""
	}
	return "/" + slug
}

// nilIfEmpty maps "" onto a JSON null so an unknown id is DISTINGUISHABLE from a
// known-empty one. `jq -e .homepage_page_id` on a run that never learned the id
// must fail, not hand back a truthy empty string.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// scaffoldPlanResult is the --dry-run plan as data: the same ordered step list
// printScaffoldPlan narrates. It exists so `--output json` is valid JSON for
// EVERY scaffold invocation — a dry-run that printed prose onto a json stdout
// would break the same `| jq` pipeline the real run just learned to serve.
func scaffoldPlanResult(templateID string, plan []planEntry) map[string]any {
	steps := make([]any, 0, len(plan))
	for _, e := range plan {
		steps = append(steps, map[string]any{"step": e.step, "detail": e.detail})
	}
	return map[string]any{
		"dry_run":     true,
		"template_id": templateID,
		"steps":       steps,
	}
}

// hubsTemplatesCmd lists the hub templates the TARGET BACKEND's catalog offers
// `hubs scaffold --template <id>` (spec §0: the CLI holds no templates, so the
// listing is fetched from the backend). The resolve is LIVE-OR-FAIL (Mutating:
// the listing is documented backend-live, so a fetch failure surfaces as
// itself — with its typed exit code — instead of silently degrading to a stale
// cache or the vendored copy and producing a misleading listing; a
// 304-validated cache read is still fine). It renders through the standard
// output layer (c.render) so --output json|table|plain and --jq all work like
// any other list command.
var hubsTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List the hub templates from the target backend's catalog.",
	Long: `List the hub templates that 'mio hubs scaffold --template <id>' can apply.

Templates live in the page-builder catalog served by the backend the CLI is
pointed at, so this command needs credentials and lists exactly what a
scaffold against that backend would see.`,
	Example: `  mio hubs templates
  mio hubs templates --output json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		cat, src, rerr := catalog.Resolve(c.ctx, catalog.ResolveOptions{
			Mutating: true, // live-or-fail: a listing must never silently degrade to a stale copy
			CacheDir: catalogCacheDirFor(c.client.BaseURL()),
			Fetcher:  catalogFetcher{c: c.client},
			Warnf:    catalogWarnf(cmd),
		})
		if rerr != nil {
			return wrapCatalogResolveErr(rerr, false) // no --catalog flag here
		}
		printCatalogProvenance(cmd, src, cat)
		if len(cat.HubTemplates) == 0 {
			return errNoHubTemplates(cat, src, false) // this command has no --catalog flag to point at
		}
		rows := make([]any, 0, len(cat.HubTemplates))
		for _, ht := range cat.HubTemplates {
			rows = append(rows, map[string]any{"id": ht.ID, "label": ht.Label, "pages": len(ht.Pages)})
		}
		return c.render(cmd, rows)
	},
}

func init() {
	// Self-register on the hubs group (hubsCmd is defined in hubs.go). --hub is the
	// persistent root context flag inherited here, so it is NOT redefined locally (a
	// local redefinition would shadow the persistent one and drop it from the
	// resolved context).
	//
	// CORE flags (MIO-2543 Task 11): --template/--name/--slug/--dry-run.
	hubsScaffoldCmd.Flags().String("template", "", "Template id to apply (e.g. community). Required.")
	hubsScaffoldCmd.Flags().String("name", "", "Display name for the new hub (create mode).")
	hubsScaffoldCmd.Flags().String("slug", "", "URL slug for the new hub (create mode).")
	hubsScaffoldCmd.Flags().Bool("dry-run", false, "Print the ordered plan and make no changes.")
	_ = hubsScaffoldCmd.MarkFlagRequired("template")

	// MIO-2672: the scaffold sources its hub template from the target backend's
	// LIVE catalog; --catalog <file> is the ONLY escape hatch (no --offline —
	// a mutating command never runs off a stale copy).
	hubsScaffoldCmd.Flags().String("catalog", "",
		"Path to a catalog.json file to scaffold from instead of the backend's live catalog (digest-verified; fails closed on mismatch).")

	// Phase-5 flags (MIO-2543 Task 21): the --publish gate + the presentation
	// overrides. runHubsScaffold already reads these existence-guarded
	// (GetBool("publish"), changedString/changedBool) and threads them into the
	// scaffoldContext, from which stepBlobs applies favicon/logo/registration and
	// stepPublish reads publish — so registering them here is all the wiring the
	// overrides need to flow end-to-end.
	hubsScaffoldCmd.Flags().Bool("publish", false,
		"Publish the hub as the final step (go live). Default off — the hub stays private so you can review first.")
	hubsScaffoldCmd.Flags().String("favicon-url", "", "Override the template's branding.favicon_url.")
	hubsScaffoldCmd.Flags().String("logo-url", "", "Override the template's branding.logo_url.")
	hubsScaffoldCmd.Flags().Bool("registration-enabled", false, "Override the template's settings.registration.enabled.")

	// MIO-2604: the PALETTE overrides (+ --branding-json), so a branded hub is one
	// command rather than a scaffold followed by a hand-authored
	// `hubs update --branding-json`. Same shape as --favicon-url/--logo-url above:
	// each flag merges over the template's branding block, it does not replace it.
	// See hubs_scaffold_branding.go for the precedence order, the
	// --primary-color → header_color cascade, and why no value format is checked.
	registerScaffoldBrandingFlags(hubsScaffoldCmd)

	hubsCmd.AddCommand(hubsScaffoldCmd)
	hubsCmd.AddCommand(hubsTemplatesCmd)
}
