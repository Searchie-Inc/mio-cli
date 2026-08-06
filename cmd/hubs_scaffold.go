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

	// policyGate is the hub-level policy-enforcement gate this run resolved from
	// the template's per-policy `enabled` declarations (MIO-2567): non-nil only
	// when at least one policy declared one, in which case stepPolicies PATCHed
	// .../policies/gate with it. nil means the template stated no intent and the
	// hub's gate was left untouched. The machine result reports it, because "the
	// ToS is written but nothing enforces it" is invisible in every other field.
	policyGate *bool

	// welcomePostID is the id of the template's welcome discussion as far as this
	// run knows it (MIO-2558): the one stepWelcomePost created, or the one its
	// title pre-check ADOPTED from an earlier run. "" when the template declares
	// no welcomePost — which is every catalog shipped so far.
	//
	// welcomePostStatus says WHICH of those happened (created / adopted /
	// adopted_deleted), because the two are materially different outcomes that an
	// id alone cannot distinguish — a resume that adopted a post the operator had
	// deleted looks identical to one that just posted a fresh welcome.
	welcomePostID, welcomePostStatus string

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
	// Renamed from "backend-gated" by MIO-2558: both things that step deferred
	// have shipped, and it now POSTs the template's welcome post. Leaving the old
	// name would have put "step \"backend-gated\" failed" on a failed POST and
	// described a mutating step as a deferral in the --dry-run plan.
	{"welcome-post", stepWelcomePost},
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
		// A strict blob-key rejection from HERE is about a TEMPLATE key, and the
		// shared message's "drop --strict-keys" tail names a flag `hubs scaffold`
		// does not have (nor does it have the --settings-json/--meta-json the
		// message opens with) — a dead end for the one person who has to act on
		// it. Re-point it at the template (MIO-2604). scaffoldStrictKeyErr is a
		// strict no-op for every OTHER error this call returns — notably the
		// PATCH's own failures, which keep their text AND their exit code.
		return scaffoldStrictKeyErr(err, scaffoldTemplateStrictKeyHint)
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
		existing, err := sc.existingSpacesBySlug()
		if err != nil {
			return err
		}
		for _, s := range t.Spaces {
			// Keyed on PRESENCE, not on a non-empty id: the map records every slug
			// the listing exposed, and a (hypothetical) space the API returned
			// without an id must still count as existing rather than be re-created.
			if _, exists := existing[s.Slug]; exists {
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

// existingSpacesBySlug returns slug→id for every space the admin spaces list
// exposes for the hub, following the backend's pagination cursor to
// exhaustion so the skip-if-exists pre-check cannot miss a space and create a
// duplicate (design §Idempotency; the list exposes no server-side slug filter).
//
// It carries the IDs, not just the slugs, because the welcome-post step
// (MIO-2558) has to turn a template space SLUG into the hub's real space id, and
// on a resume the space it targets was created by an earlier run — so
// sc.spaceIDsBySlug (deliberately "what THIS run created", see stepSpaces) is
// empty for it. One walk, two callers, no second pagination loop to keep in sync.
//
// Cursor convention (verified): the mio-backend standard pagination envelope
// (app/infrastructure/pagination.py) carries the next page's cursor at
// meta.page.next_cursor, gated by meta.page.has_more — the same cursor also
// appears in links.next. nextPageCursor reads that PROVEN field, NOT a bare
// meta.next (which is only a synthetic test-fixture shape, never emitted here).
//
// CORRECTION (MIO-2558, re-verified against mio-backend origin/main
// spaces_admin.py, 2026-07-29): an earlier revision of this comment claimed the
// admin spaces list "emits NO pagination meta … so nextPageCursor returns "" and
// this stops after one page today". That is FALSE and was worth catching, because
// this helper now has a second load-bearing caller (the welcome-post step's space
// resolution). admin_list_spaces returns _paginated_list_response →
// build_page_meta, which emits meta.page.{size,has_more,next_cursor} plus
// links.{self,next}; it fetches page_size+1 to compute has_more honestly, and
// page[after] is an OPAQUE encoded cursor (decode_cursor) that an empty value
// 422s rather than silently restarting at page 1. The CODE was always right — it
// follows the documented cursor — so nothing changes here but the comment: this
// walk is genuinely exhaustive, not exhaustive-by-construction-someday.
// The seen-cursor set + maxPages bound are a stall guard:
// a buggy server returning a stable/looping non-empty cursor can never spin here.
func (sc *scaffoldContext) existingSpacesBySlug() (map[string]string, error) {
	slugs := map[string]string{}
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
				slugs[s] = r.ID
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
// there is no next page, including when the response carries no pagination meta
// at all.
//
// That last clause used to name the admin spaces list as the example of a
// meta-less response. It is NOT one — admin_list_spaces returns
// _paginated_list_response → build_page_meta and emits the full
// meta.page.{size,has_more,next_cursor} (re-verified on mio-backend origin/main,
// 2026-07-29; see the CORRECTION on existingSpacesBySlug, which is where that
// same false claim was retracted). The fallback branch is generic defensiveness,
// not a description of any endpoint this CLI actually walks.
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

// templatePolicies is the fully RESOLVED policies block: the ordered
// per-policy PATCH bodies stepPolicies sends, and the hub-level enforcement
// gate collapsed out of their `enabled` declarations.
type templatePolicies struct {
	keys     []string    // template keys, sorted (deterministic apply + message order)
	policies []hubPolicy // one PATCH body per key, in keys order
	// gate is the collapsed per-policy `enabled`: nil when no policy declared
	// one. See resolveTemplatePolicies for the collapse and scaffoldPolicyGate
	// for why only a true is ever WRITTEN.
	gate *bool
}

// resolveTemplatePolicies parses + validates a template's whole policies block
// and collapses its per-policy `enabled` onto the single hub-level gate. It is
// a PURE function of the template: no context, no HTTP, no ordering
// requirements.
//
// That purity is the point. It is called from rebuildScaffoldPlan — the
// WRITE-FREE preflight, beside HubTemplate.Validate — so a malformed policies
// block fails before the hub is created, not at pipeline stage 5 of 9 with a
// hub and its blobs, spaces and onboarding defs already written and no
// rollback (MIO-2567 review). stepPolicies calls it again at apply time so the
// step stays self-contained for the unit-driven contexts that never run
// preflight; it is cheap and deterministic, so running it twice is free.
func resolveTemplatePolicies(t *catalog.HubTemplate) (templatePolicies, error) {
	out := templatePolicies{keys: make([]string, 0, len(t.Policies))}
	if len(t.Policies) == 0 {
		return out, nil
	}
	for k := range t.Policies {
		out.keys = append(out.keys, k)
	}
	sort.Strings(out.keys)

	out.policies = make([]hubPolicy, 0, len(out.keys))
	for _, key := range out.keys {
		pol, err := templateHubPolicy(key, t.Policies[key])
		if err != nil {
			return templatePolicies{}, err
		}
		out.policies = append(out.policies, pol)
	}
	gate, err := scaffoldPolicyGate(t.Policies, out.keys)
	if err != nil {
		return templatePolicies{}, err
	}
	out.gate = gate
	return out, nil
}

// stepPolicies applies the template's legal policies via the shared
// applyHubPolicies helper — one PATCH per policy, keys sorted for deterministic
// ordering (design §Apply pipeline step 5, MIO-2543 Task 16) — and then, when
// the template declares enforcement, flips the hub-level policy gate through
// the same PATCH .../policies/gate the `hubs policies gate` verb uses
// (MIO-2567). An empty/nil Policies map is a clean no-op.
//
// TWO WRITES, ONE STEP. The gate is recorded as its own plan entry (so
// --dry-run names it) but stays under the "policies" step name rather than
// becoming a tenth pipeline stage: it is the enforcement half of the same
// declaration, it has nothing to do when the template carries no policies, and
// the ordered step-name list is a published contract (the dry-run plan surfaces
// it). It MUST run after the content PATCHes — enabling the gate first would
// briefly require members to accept the default document the template is about
// to replace.
//
// RESUME CAVEAT (MIO-2567 review, and read the CONTENT SEMANTICS note on
// templateHubPolicy): applyHubPolicies always sends `content`, so a template
// that omits it RESETS that policy to the backend default on every apply — and
// for a TOS with require_acceptance that also normalizes the version, which
// RE-PROMPTS every member who had already accepted. The shipped community
// template omits content, so a resume onto a hub whose ToS an operator edited
// by hand reverts the text and re-prompts. That reset predates this change; what
// this change does is make it MEMBER-VISIBLE, because the gate it now flips is
// what turns re-prompting on. It is called out on every operator-facing surface
// rather than only here.
func stepPolicies(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if len(t.Policies) == 0 {
		return sc.step("policies", "no policies in template", func() error { return nil })
	}
	// Re-resolve rather than read a context field: the unit-driven step tests
	// build a scaffoldContext directly and never run preflight, and a step that
	// silently no-ops without it would be a worse trap than the double parse.
	res, err := resolveTemplatePolicies(t)
	if err != nil {
		return err
	}

	detail := fmt.Sprintf("PATCH %s — set policy(ies) [%s]",
		hubsPoliciesPath(sc.teamID, sc.hubIDOrPlaceholder()), strings.Join(res.keys, ", "))
	if serr := sc.step("policies", detail, func() error {
		for _, pol := range res.policies {
			if _, aerr := applyHubPolicies(sc.ctx, sc.cl, sc.teamID, sc.hubID, pol); aerr != nil {
				return aerr
			}
		}
		return nil
	}); serr != nil {
		return serr
	}

	// ENABLE-ONLY (see scaffoldPolicyGate). Nothing declared, or a declaration
	// that resolves to false: no gate request — but SAY SO, on the plan and on a
	// real run's stderr, exactly as stepPublish narrates its own skip. The whole
	// point of this ticket is that "documents written, enforcement unknown" must
	// never again be a state you can only discover from a member's 404.
	if res.gate == nil || !*res.gate {
		reason := policyGateSkipReason(res.gate)
		return sc.step("policies",
			"skipped — "+reason+"; enforcement gate not written (the hub's current setting stands)",
			func() error {
				sc.notef("policies: %s — enforcement gate NOT written; the hub's current setting stands (check or set it with `mio hubs policies gate %s --enabled`).",
					reason, sc.hubID)
				return nil
			})
	}

	gateDetail := fmt.Sprintf("PATCH %s — enable policy enforcement (settings.policies.enabled=true, collapsed from the template's per-policy enabled)",
		hubsPoliciesGatePath(sc.teamID, sc.hubIDOrPlaceholder()))
	return sc.step("policies", gateDetail, func() error {
		if _, gerr := applyHubPolicyGate(sc.ctx, sc.cl, sc.teamID, sc.hubID, true); gerr != nil {
			return gerr
		}
		sc.policyGate = res.gate
		// Narrate it (stderr, like every other progress note). The prose summary is
		// a byte-exact golden and stays untouched, but a real run must not flip a
		// member-facing enforcement switch in complete silence — "the ToS is there
		// and nothing enforces it" was invisible for a whole release precisely
		// because nothing on any surface said either way.
		sc.notef("policies: enforcement gate set to enabled=true (settings.policies.enabled).")
		return nil
	})
}

// policyGateSkipReason explains, in the operator's words, why no gate write
// happened — the two cases are genuinely different and conflating them is how
// this bug hid.
func policyGateSkipReason(gate *bool) string {
	if gate == nil {
		return "no policy declares \"enabled\""
	}
	return "the template declares \"enabled\": false, which the applier contract does not act on"
}

// scaffoldPolicyGate collapses the template's PER-POLICY `enabled` declarations
// onto the ONE hub-level enforcement gate the backend actually stores, over
// keys in the caller's deterministic order. It returns nil when no policy
// declares `enabled`.
//
// WHY A COLLAPSE (verified against mio-backend origin/main, MIO-2567): there is
// no per-policy enabled field in the stored shape. `_policies_enabled()`
// (app/hubs/service.py) reads exactly settings.policies.enabled, by identity
// (`is True`), and PATCH .../policies/gate → update_policy_gate() is its only
// writer; update_policy() writes content + version into
// settings.policies.{tos,privacy_policy} and never an enabled. So a template
// saying "terms enforced, privacy not" is describing a granularity the platform
// has never had.
//
// WHY A CONFLICT ERRORS: the collapse is lossy, and picking a winner silently is
// the exact failure mode this ticket exists to remove. `true` beating `false`
// would enforce a policy the author declared off; `false` beating `true` would
// ship the hub QA reported. Neither can be inferred, so the template is wrong
// and says so — from the WRITE-FREE preflight (ExitUsage), naming both keys.
//
// WHY ONLY A TRUE IS WRITTEN: the caller applies a resolved true and skips a
// resolved false. That is the ratified applier contract — mio-page-catalog
// catalog.schema.json describes a per-policy `enabled: true` as declaring
// "template INTENT only" and requires the applier to "call PATCH
// .../policies/gate when enabled is true", saying nothing about false. Acting
// on a false anyway would mean every resume of such a template DISABLES
// enforcement an operator had turned on by hand — the mirror image of the
// reason an UNDECLARED gate is left alone. Skipping it is not a silent drop: it
// is narrated on stderr and documented, and `hubs policies gate --enabled=false`
// is the verb that exists for actually disabling one.
//
// NOT a gate source: settings.policies.enabled, which the same community
// template also carries. The scaffold sends the settings blob through the
// generic hub PATCH, where the backend pops `policies` wholesale
// (service.py: `incoming.pop("policies", None)`), so that key is inert — a
// reading the ratified schema states outright ("a settings-blob PATCH does not
// enforce"). Re-routing a settings key to a different endpoint would be the CLI
// second-guessing the API rather than conducting it, and it would repair
// `enabled` while leaving its siblings (`show`, `tos`, `privacy_policy`) just as
// inert — a new asymmetry in place of the old one.
func scaffoldPolicyGate(policies map[string]any, keys []string) (*bool, error) {
	var gate *bool
	var declaredBy string
	for _, key := range keys {
		val, _ := policies[key].(map[string]any) // shape already checked by templateHubPolicy
		raw, present := val["enabled"]
		if !present {
			continue
		}
		enabled, ok := raw.(bool)
		if !ok {
			// The backend gate is identity-checked (`is True`), so a stringly-typed
			// "true" would never enforce anything. Reject it rather than let it read
			// as "no declaration".
			return nil, errs.New(errs.ExitUsage,
				"policy %q: \"enabled\" must be a JSON boolean, got %T", key, raw)
		}
		if gate != nil && *gate != enabled {
			return nil, errs.New(errs.ExitUsage,
				"policies[%q].enabled=%t conflicts with policies[%q].enabled=%t: policy enforcement is a single hub-level gate (settings.policies.enabled), not per-policy — make every policy that declares \"enabled\" agree",
				declaredBy, *gate, key, enabled)
		}
		gate, declaredBy = &enabled, key
	}
	return gate, nil
}

// templateHubPolicy maps one template policy entry (key + value) onto the
// hubPolicy the shared applyHubPolicies consumes. The key resolves to the backend
// policy_type via policyTypeAliases (an unknown key ERRORS — never a silent drop),
// and so does everything INSIDE the value: a non-object, a field outside
// catalog.HubPolicyFieldKey, or a field of the wrong JSON type is an ExitUsage
// error (MIO-2567 closed that asymmetry — the key rule and the field rule are
// now the same rule). catalog.HubTemplate.Validate rejects unknown fields at
// preflight too; this is the second line, and the only one for a value that
// never came through the catalog.
//
// `enabled` is READ HERE ONLY to be accepted, not applied: it is not part of the
// policies PATCH body (the backend stores no per-policy enabled) — it is
// collapsed onto the hub-level gate by scaffoldPolicyGate.
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
	val, ok := raw.(map[string]any)
	if !ok {
		return hubPolicy{}, errs.New(errs.ExitUsage,
			"policy %q must be an object, got %T", key, raw)
	}
	for f := range val {
		// ONE allow-list, the catalog's — the same set HubTemplate.Validate
		// enforces at preflight. A second copy here would be a copy to keep in
		// sync, and the whole bug was a field that lived on one list and in no
		// consumer.
		if !catalog.HubPolicyFieldKey(f) {
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: unknown field %q (allowed: %s)", key, f,
				strings.Join(catalog.HubPolicyFieldKeys(), ", "))
		}
	}

	p := hubPolicy{PolicyType: policyType}
	if c, present := val["content"]; present {
		content, ok := c.(string)
		if !ok {
			// A non-string content would otherwise leave p.Content nil, and
			// applyHubPolicies sends nil as JSON null — silently RESETTING the policy
			// to the backend default instead of applying what the template says.
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: \"content\" must be a string, got %T", key, c)
		}
		p.Content = &content
	}
	// "require_acceptance" is canonical; "required" is the friendly alias a
	// template may read better with. Either alone wins; both present and
	// DISAGREEING is a template contradiction with no defensible winner, so it
	// errors rather than letting the canonical name quietly beat the alias.
	for _, f := range []string{"require_acceptance", "required"} {
		v, present := val[f]
		if !present {
			continue
		}
		ra, ok := v.(bool)
		if !ok {
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: %q must be a JSON boolean, got %T", key, f, v)
		}
		if p.RequireAcceptance != nil && *p.RequireAcceptance != ra {
			return hubPolicy{}, errs.New(errs.ExitUsage,
				"policy %q: \"require_acceptance\"=%t contradicts its alias \"required\"=%t — set one, or make them agree",
				key, *p.RequireAcceptance, ra)
		}
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

// stepWelcomePost is the terminal step: it lands the template's welcome
// discussion in one of the hub's own spaces (design §Apply pipeline step 9,
// MIO-2543 Task 20, finished under MIO-2558).
//
// This step used to be `backend-gated`, a skip-with-note deferring two items to
// mio-backend. BOTH have since shipped to production, so the note was stale in
// both halves and this step now does real work for one of them:
//
//   - MIO-2540 (auto-assign the hub creator as owner-admin on create) landed in
//     6565362d and is server-side ONLY — the backend does it as part of the hub
//     create this pipeline already fires. There is nothing for the CLI to wire,
//     which is why nothing here mentions it any more: a note about a step that
//     silently already happens is worse than no note.
//   - MIO-2262 (0da17745) added POST .../hubs/{hub_id}/discussions, the
//     impersonation-free admin welcome-post route. That is the one below, driven
//     through the CLI's own `community discussions create` path shape.
//
// IDEMPOTENCY — the create endpoint has no upsert and no natural key, and its
// request schema (extra="forbid": space_id/title/body/is_published) has no meta
// field, so the pages step's provenance-marker trick is not available here. The
// pre-check is therefore a TITLE match within the target space: found ⇒ skip and
// adopt that id, so a resume never lands a second post.
//
// The title is compared — and posted — STRIPPED, because that is what the server
// stores: normalize_discussion_title (app/community/discussion_text.py) returns
// title.strip() as the canonical form. Matching the template's raw string against
// the stored one would mean a template title with any leading/trailing space
// never matches its own earlier post, and EVERY resume posts another copy — the
// exact failure this pre-check exists to prevent.
//
// strings.TrimSpace is NOT exactly Python's str.strip(): they agree on every
// ordinary whitespace character, but Python also strips the C0 separators
// U+001C-U+001F, which Go's unicode.IsSpace does not. A title padded with those
// would be sent by us with them and stored by the server without, so the
// comparison would never match and each resume would duplicate. Not chased: no
// plausible authored template contains a file/group/record/unit separator, and
// the alternative — hand-rolling Python's whitespace table in Go — is a worse
// bet than this note.
//
// The scan reads the admin list UNFILTERED and matches space_id client-side
// rather than passing filter[space_id]. That looks like the more expensive
// choice and is the correct one: the filtered branch runs through
// DiscussionsRepository.list_for_space, which appends
// `Discussion.is_removed.is_(False)` UNCONDITIONALLY (repositories/discussions.py
// :534, "Always hide soft-removed content"), so a moderation-REMOVED welcome post
// is invisible to it and every resume would duplicate. list_for_hub — the
// unfiltered branch — filters only deleted_at, and the router passes
// include_deleted=True, so it is the one view that sees drafts, soft-deleted AND
// removed rows. Cost: the walk covers the whole hub rather than one space.
//
// A match that is soft-deleted or removed is still adopted, deliberately: a
// welcome post the operator deleted or a moderator removed must not be
// resurrected by the next resume — the same "never fight the operator" direction
// the pages step takes with edited drafts. Because "adopted something the members
// cannot see" is a materially different outcome from "posted a fresh one", the
// run reports which happened (welcome_post_status) instead of leaving a caller to
// infer it from an id.
func stepWelcomePost(sc *scaffoldContext, t *catalog.HubTemplate) error {
	wp := t.WelcomePost
	if wp == nil {
		// No catalog ships a welcomePost yet (`community` at 0.14.1 has no such
		// key), so this is the branch every real run takes today. The CLI holds no
		// templates (spec §0) and must not invent post copy, so the step converges
		// to a clean no-op rather than posting something nobody authored.
		//
		// This branch is also where a MIS-SPELLED or not-yet-ratified key lands
		// (MIO-2812 — `welcomePost` is CLI-defined vocabulary that the catalog
		// schema accepts only via additionalProperties:true, so it can catch
		// neither a typo nor a rename). It records a plan-VISIBLE entry rather
		// than skipping quietly precisely because that is the only signal
		// available: an operator seeing "no welcome post in template" against a
		// template they believe declares one has their answer.
		return sc.step("welcome-post", "no welcome post in template", func() error { return nil })
	}
	// Strip once, here, so the plan detail, the pre-check and the POST body all
	// name the same string the server will store.
	title := strings.TrimSpace(wp.Title)
	detail := fmt.Sprintf("GET+POST %s — create welcome discussion %q in space %q (skip-if-title-exists)",
		discussionsAdminPath(sc.teamID, sc.hubIDOrPlaceholder(), ""), title, wp.Space)
	return sc.step("welcome-post", detail, func() error {
		// The space this run just created is already in hand (stepSpaces records
		// what it creates), so the common create-mode path costs no extra request
		// and cannot be tripped by read-after-write lag on a freshly-created space.
		// Only a RESUME — where the space predates this invocation and
		// spaceIDsBySlug is therefore empty for it — pays for the listing.
		spaceID := sc.spaceIDsBySlug[wp.Space]
		if spaceID == "" {
			spaces, lerr := sc.existingSpacesBySlug()
			if lerr != nil {
				return lerr
			}
			spaceID = spaces[wp.Space]
		}
		if spaceID == "" {
			// Unreachable on a healthy run: the template validator proves the slug is
			// one of spaces[], and stepSpaces converged them onto the hub. Reaching
			// here means the space vanished between the two steps (or was created
			// without an id), so fail loud rather than post into the wrong space.
			return errs.New(errs.ExitGeneric,
				"welcome post: space %q was not found on hub %s (it should have been created by the spaces step)",
				wp.Space, sc.hubID)
		}

		existing, ferr := sc.findDiscussionByTitle(spaceID, title)
		if ferr != nil {
			return ferr
		}
		if existing != nil {
			sc.welcomePostID = existing.ID // adopt the earlier run's post
			sc.welcomePostStatus = welcomePostAdopted
			if discussionSoftDeleted(*existing) {
				sc.welcomePostStatus = welcomePostAdoptedDeleted
				sc.notef("welcome post: %q already exists in space %q but is soft-deleted — left deleted (id %s)",
					title, wp.Space, existing.ID)
				return nil
			}
			sc.notef("welcome post: %q already exists in space %q — skipped (id %s)", title, wp.Space, existing.ID)
			return nil
		}

		attrs := map[string]any{
			"space_id": spaceID,
			"title":    title,
			// is_published is sent unconditionally: TemplateWelcomePost.Published
			// already carries the endpoint's own default (true) for a template that
			// omits the key, so this is always a value the template meant.
			"is_published": wp.Published,
		}
		if wp.Body != "" {
			attrs["body"] = wp.Body
		}
		res, cerr := sc.cl.Create(sc.ctx, discussionsAdminPath(sc.teamID, sc.hubID, ""), attrs)
		if cerr != nil {
			return cerr
		}
		sc.welcomePostID = res.ID
		sc.welcomePostStatus = welcomePostCreated
		return nil
	})
}

// welcomePostStatus values for the machine result (MIO-2558). They answer the one
// question an id alone cannot: did THIS run post the welcome discussion, or did it
// find one already there — and if so, is that one still visible?
//
// There is deliberately no "adopted_removed": moderation removal is NOT
// observable over this endpoint. _discussion_to_resource
// (routers/discussions_admin.py) serializes deleted_at but never is_removed, and
// its computed `status` is only published/scheduled/draft, so the CLI cannot tell
// a removed row from a live one. The unfiltered scan still SEES that row (which is
// the point — it stops the duplicate); it just cannot label it.
const (
	welcomePostCreated        = "created"
	welcomePostAdopted        = "adopted"
	welcomePostAdoptedDeleted = "adopted_deleted"
)

// discussionSoftDeleted reports whether an admin-listed discussion carries a
// non-null deleted_at. Adopted rather than replaced — never fight the operator —
// but the caller is told which it got.
func discussionSoftDeleted(r client.Resource) bool {
	v, ok := r.Attributes["deleted_at"]
	return ok && v != nil
}

// findDiscussionByTitle returns the first discussion in spaceID whose stored
// title equals title (compare STRIPPED — see stepWelcomePost), or nil when there
// is none. It walks the admin discussions list to exhaustion, because a hub's
// welcome post is its OLDEST discussion and both admin orderings put oldest last
// (last_activity_at DESC in the filtered branch, id DESC — UUIDv7, i.e. creation
// order — in the unfiltered one). In an active community it is therefore the last
// row on the last page, and a first-page-only check would re-create it on every
// resume.
//
// NO filter[space_id] — the match on space is client-side, off the serialized
// space_id attribute. That is not an oversight: the filtered branch runs
// list_for_space, which appends `Discussion.is_removed.is_(False)`
// unconditionally, so a moderation-removed post is invisible to it and the resume
// duplicates. The unfiltered branch (list_for_hub, include_deleted=True from the
// router) is the only view carrying drafts, soft-deleted AND removed rows. The
// price is scanning the hub rather than one space.
//
// CURSOR SHAPE (verified against mio-backend app/community/routers/
// discussions_admin.py::_list_response, read 2026-07-29): this endpoint does NOT
// use the standard meta.page envelope the spaces/contact-attribute lookups read
// via nextPageCursor. It emits a BARE top-level meta.next_cursor gated by
// meta.has_more, with the cursor itself a "<last_activity_at ISO-8601>|<id>"
// pair echoed back as page[after]. Reusing nextPageCursor here would read
// meta.page.next_cursor, find nothing, and silently stop after page 1 — hence
// the separate reader below. page[size] is asked at the backend's clamp ceiling
// (100) to bound the walk.
//
// AN INCOMPLETE SCAN IS AN ERROR, NOT AN END-OF-LIST. Only ONE way out of the
// loop means "I saw every row": the server said there is no next page. Every
// other exit means the walk stopped early, and because the sole caller CREATES a
// discussion when this returns no match, returning (nil, nil) from any of them
// converts "I could not finish looking" into "it is not there" and lands a
// duplicate welcome post. All three therefore return an error:
//
//   - meta.has_more true with no meta.next_cursor (see discussionsNextCursor);
//   - a repeated cursor — the server is not advancing;
//   - maxPages exhausted.
//
// The first is the one the backend can actually emit today; the other two are
// defence in depth. An earlier revision guarded only the first and left the other
// two breaking to (nil, nil) — i.e. it hardened the exit the server cannot reach
// and left the two it can, which is precisely the outcome this comment says must
// never happen.
//
// On the truncation case specifically: _list_response computes
// `has_more = len(discussions) == page_size` independently of the cursor, while
// _build_cursor returns None when the page's LAST row has a NULL
// last_activity_at, so {has_more: true, next_cursor: null} is a producible
// envelope meaning "there are more pages and I cannot give you one". It needs a
// NULL last_activity_at, and no writer in app/ produces one today
// (repositories/discussions.py::insert is the only Discussion(...) construction
// and always sets it; update_last_activity takes a non-optional value) — but the
// column is nullable with no server default and the backend's own readers COALESCE
// it defensively for legacy rows.
//
// An earlier revision dismissed that case as unreachable "for anything this CLI
// could have posted". Wrong twice over: the truncating row is whichever row lands
// LAST on a page, not the welcome post, and the exposure GREW when this walk moved
// off filter[space_id] — list_for_space sorts -last_activity, where Postgres puts
// NULLs first and the keyset excludes them from page 2 on, so truncation needed ~a
// full page of NULL rows in one space; list_for_hub orders by id DESC with no NULL
// predicate, so ONE such row at any page boundary anywhere in the hub ends the walk.
func (sc *scaffoldContext) findDiscussionByTitle(spaceID, title string) (*client.Resource, error) {
	seen := map[string]bool{}
	cursor := ""
	const maxPages = 1000 // hard ceiling (100k discussions at page_size 100); a stall guard, not a real bound
	for page := 0; page < maxPages; page++ {
		query := url.Values{}
		query.Set("page[size]", "100")
		if cursor != "" {
			query.Set("page[after]", cursor)
		}
		col, err := sc.cl.List(sc.ctx, discussionsAdminPath(sc.teamID, sc.hubID, ""), query)
		if err != nil {
			return nil, err
		}
		for i, r := range col.Data {
			if s, _ := r.Attributes["space_id"].(string); s != spaceID {
				continue
			}
			// Compare stripped on BOTH sides: the server stores title.strip(), but a
			// row written by anything other than this endpoint need not be stripped.
			if s, ok := r.Attributes["title"].(string); ok && strings.TrimSpace(s) == title {
				return &col.Data[i], nil
			}
		}
		next, nerr := discussionsNextCursor(col)
		if nerr != nil {
			return nil, nerr
		}
		if next == "" {
			return nil, nil // the ONLY complete-scan exit: the server has no next page
		}
		if seen[next] {
			return nil, errs.New(errs.ExitServer,
				"discussions list: the server repeated pagination cursor %q instead of advancing, so the scan "+
					"cannot be completed; refusing to continue rather than risk creating a duplicate welcome post. %s",
				next, welcomePostScanRecovery)
		}
		seen[next] = true
		cursor = next
	}
	return nil, errs.New(errs.ExitGeneric,
		"discussions list: gave up after %d pages without reaching the end of the hub's discussions, so the scan "+
			"cannot be completed; refusing to continue rather than risk creating a duplicate welcome post. %s",
		maxPages, welcomePostScanRecovery)
}

// welcomePostScanRecovery is the operator-facing tail shared by every
// incomplete-scan error. Re-running is NOT the recovery for these: the conditions
// are persistent (a null last_activity_at at a page boundary is stored data, a
// non-advancing cursor is a server bug), so every re-run fails identically and
// there is no flag to skip the step.
//
// Posting by hand works because of the ordering: list_for_hub is id DESC over
// UUIDv7 ids, i.e. newest first, so a just-created discussion is row 1 of page 1
// and the next scan adopts it long before reaching whatever boundary broke the
// walk. Every other scaffold step is idempotent, so re-running afterwards is safe.
const welcomePostScanRecovery = "Create the welcome post by hand with " +
	"`mio community discussions create --hub <hub> --space-id <space> --title <title>` and re-run the scaffold: " +
	"it is the newest discussion, so the next scan finds it on the first page and adopts it instead of posting again."

// discussionsNextCursor reads the admin discussions list's own pagination
// envelope: a top-level meta.next_cursor gated by meta.has_more (NOT the
// meta.page.* shape nextPageCursor handles). "" with a nil error means there is
// no next page.
//
// It ERRORS on has_more:true with no cursor — the truncation case documented on
// findDiscussionByTitle. Returning "" there would silently convert "I have more
// pages but cannot address them" into "that was everything", and the one caller
// creates a discussion when it finds nothing.
//
// ExitServer (7) rather than ExitGeneric: the request succeeded and the server
// answered with an envelope that contradicts itself, which is an upstream fault,
// not an unexpected local one. Note 7 is documented as "upstream server error
// (5xx)" and this arrives on a 200 — a deliberate widening to "the upstream's
// response is unusable", chosen because main.go's exitToStatus renders BOTH codes
// as "500" in the stderr envelope, so the exit code is the only signal that
// distinguishes them and 7 is the one that says "not your input".
func discussionsNextCursor(col *client.Collection) (string, error) {
	hasMore, present := col.Meta["has_more"].(bool)
	cur, _ := col.Meta["next_cursor"].(string)
	if present && hasMore && cur == "" {
		return "", errs.New(errs.ExitServer,
			"discussions list: the server reports more pages (meta.has_more=true) but returned no meta.next_cursor, "+
				"so the scan cannot be completed; refusing to continue rather than risk creating a duplicate welcome post "+
				"(the last row of a page has a null last_activity_at). %s", welcomePostScanRecovery)
	}
	if present && !hasMore {
		return "", nil
	}
	// No has_more at all (a response carrying no pagination meta): a bare cursor
	// is still followed, an absent one ends the walk.
	return cur, nil
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
  HUB_ID=$(mio hubs scaffold --template community --name "My Community" --slug my-community -o plain --jq .hub_id)`,
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
	// Same pre-auth, pre-HTTP position: an empty branding *_url — however it was
	// spelled, scalar flag or --branding-json — is rejected by the API on BOTH
	// write paths, so failing here costs nothing, whereas failing at the blobs
	// PATCH costs an orphaned half-built hub with no rollback.
	if uerr := validateScaffoldBrandingURLs(branding, changedString(cmd, "logo-url"), changedString(cmd, "favicon-url")); uerr != nil {
		return uerr
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

	// 4b. PROBE the whole-hub backend op (MIO-2976). In create mode the op builds
	//     the entire hub in ONE server-side transaction; when it is absent (the
	//     dormant flag, or a backend that predates it) this falls back to the
	//     nine-step pipeline below, which stays the legacy path and the path for
	//     every invocation the op cannot express (--hub, --dry-run, the branding
	//     overrides, --catalog). It runs AFTER the preflight on purpose: the
	//     preflight is write-free and resolves the catalog + template both paths
	//     need, so a bad template still fails before anything is written on
	//     either one.
	//
	//     On failure there is no recovery guidance to print: the op is one
	//     transaction and rolls back, so nothing was applied — the same reason
	//     the preflight returns directly above.
	opHandled, operr := maybeApplyViaHubOp(cmd, sc)
	if operr != nil {
		return errs.Wrap(errs.CodeOf(operr), scaffoldStepError(sc, "hub-op", operr))
	}

	// 5. Run the pipeline in order. Each step decides for itself whether to fire
	//    HTTP or (in dry-run) record its plan entry — see sc.step — so the runner
	//    just dispatches and, on failure, prints the recovery guidance and returns
	//    the step-tagged error (rendered once by main.go).
	for _, step := range scaffoldPipeline {
		if opHandled {
			break // the op already built the hub; every step would be a re-apply
		}
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

	// Welcome post (MIO-2558), printed ONLY when the template declares one — so the
	// summary of a run against every catalog shipped so far stays byte-for-byte
	// what it always was (TestScaffold_TableSummaryUnchanged), exactly like the
	// branding-overrides line below. Without it a created welcome post is the one
	// thing the run wrote that the human surface never mentions.
	if t.WelcomePost != nil {
		switch sc.welcomePostStatus {
		case welcomePostCreated:
			fmt.Fprintf(w, "  Welcome post: created %q (id %s).\n", strings.TrimSpace(t.WelcomePost.Title), sc.welcomePostID)
		case welcomePostAdopted:
			fmt.Fprintf(w, "  Welcome post: already present (id %s) — not re-posted.\n", sc.welcomePostID)
		case welcomePostAdoptedDeleted:
			fmt.Fprintf(w, "  Welcome post: already present but SOFT-DELETED (id %s) — left deleted, not re-posted.\n", sc.welcomePostID)
		}
	}

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
//   - policy_gate (MIO-2567) reports the hub-level enforcement gate this run
//     WROTE. The write is enable-only (see scaffoldPolicyGate), so the pipeline
//     produces exactly two values: true when it turned enforcement on, and null
//     when it wrote no gate at all — which covers BOTH "no policy declared
//     enabled" and "the declaration resolved to false", the two cases whose
//     shared outcome is that the hub's existing setting stands. It is the one
//     field that
//     distinguishes "the ToS document exists" (policies[]) from "members are
//     actually asked to accept it", which is precisely the pair QA could not
//     tell apart: content present, `policies_enabled` reading true off the
//     FE-facing derivation, and the member endpoint still answering
//     tos_acceptance_required:false;
//   - welcome_post_id + welcome_post_status (MIO-2558) are ADDITIVE, like every
//     field before them: the id of the template's welcome discussion as this run
//     knows it, and whether the run "created" it, "adopted" one already there, or
//     adopted one that is soft-deleted ("adopted_deleted") — a distinction the id
//     alone cannot carry. Both null when the template declares no welcomePost (the
//     case for every catalog shipped so far);
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
		"welcome_post_id":       nilIfEmpty(sc.welcomePostID),
		"welcome_post_status":   nilIfEmpty(sc.welcomePostStatus),
		"pages":                 pages,
		"spaces":                spaces,
		"onboarding_attributes": onboarding,
		"playlists":             playlists,
		"policies":              policies,
		"policy_gate":           policyGateResult(sc.policyGate),
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

// policyGateResult renders the gate this run WROTE for the machine result: the
// written boolean, or JSON null when no gate was written at all.
//
// The contract `jq .policy_gate` reads is TWO-valued, because the write is
// enable-only: true (this run turned enforcement on) and null (it did not
// touch the gate — no declaration, or a declaration that resolved to false).
// What must never happen is the null collapsing into false: "this run left the
// hub's enforcement alone" and "enforcement is off" are different statements,
// and answering the second for the first is the shape of the original bug.
//
// The *bool is kept rather than a plain bool because a resolved false is a real
// state the resolver can produce — it is simply not written today — so the
// encoding stays honest if the ratified applier contract ever gains a disable
// case. The output layer would round-trip a bare *bool to the same JSON; this
// states the contract where a reader can see it, as nilIfEmpty does for unknown
// ids (MIO-2567).
func policyGateResult(gate *bool) any {
	if gate == nil {
		return nil
	}
	return *gate
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
