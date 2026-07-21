package cmd

// hubs_scaffold.go — `mio hubs scaffold`, the template-driven full-experience
// hub seeder (MIO-2543). See docs/superpowers/specs/2026-07-21-hubs-scaffold-design.md.
//
// This file is the ORCHESTRATOR SKELETON: the command shell, the mutable
// scaffoldContext threaded through every step, the ordered pipeline runner, and
// the --dry-run plan. The per-step BODIES are no-ops here — Phase 4 fills each
// step (hub/blobs/spaces/…) with the extracted attribute-builders + client
// calls the individual `mio hubs`/`community`/`media`/`pages` commands use, so
// the scaffold stays strictly CLI-only and never re-invokes a command's RunE.
//
// Design invariants pinned by this skeleton (hubs_scaffold_test.go):
//   - the template is loaded + validated BEFORE any HTTP, so an unknown/invalid
//     template exits ExitUsage without touching the network;
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
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/hubtemplate"
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
	isPrivate      bool
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

	spaceIDsBySlug, defIDsBySlug, playlistIDsByKey map[string]string

	// homePageID + homeDraftVersion are minted/read by the homepage step
	// (stepHomepage: pages create → tree set via draft_version). homeDraftVersion
	// carries the draft_version the tree PUT returns; it is currently WRITE-ONLY —
	// forward-looking state for a future page-publish/edit step that would need the
	// fresh OCC token (no reader today, deliberately).
	homePageID       string
	homeDraftVersion int

	// publish is the --publish intent (Task 21 registers the flag; read
	// existence-guarded in runHubsScaffold, so it defaults false until then). When
	// false the publish step is a skip-with-note and the hub stays private.
	publish bool

	dryRun bool
	plan   *[]planEntry // collected when dryRun
}

// planEntry is one line of the --dry-run plan: the step name and an optional
// human detail (Phase 4 fills detail with the target path + static attrs +
// placeholder ids for resources not yet created).
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
// path + static attrs + placeholder ids), which only the step body knows. Phase
// 4 enriches the detail arg and fills fn; the runner needs no change.
func (sc *scaffoldContext) step(name, detail string, fn func() error) error {
	if sc.dryRun {
		sc.recordPlan(name, detail)
		return nil
	}
	return fn()
}

// ---- step model + ordered pipeline ------------------------------------------

// scaffoldStep is one named stage of the apply pipeline. Phase 4 fills each run
// body; today they are no-ops so the runner + dry-run plan can be exercised.
type scaffoldStep struct {
	name string
	run  func(sc *scaffoldContext, t *hubtemplate.Template) error
}

// scaffoldPipeline is the ordered apply pipeline (design §Apply pipeline). The
// names + order are a contract the dry-run plan surfaces; the bodies are no-ops
// until Phase 4.
var scaffoldPipeline = []scaffoldStep{
	{"hub", stepHub},
	{"blobs", stepBlobs},
	{"spaces", stepSpaces},
	{"onboarding", stepOnboarding},
	{"policies", stepPolicies},
	{"playlists", stepPlaylists},
	{"homepage", stepHomepage},
	{"publish", stepPublish},
	{"backend-gated", stepBackendGated},
}

// stepHub creates the hub (create mode) or records that an existing one is being
// reused (resume mode). It writes ONLY the hub's core identity (name/slug) — the
// presentation blobs (branding/favicon/settings/registration/navigation) are the
// province of stepBlobs, so nothing is applied twice (design §Apply pipeline
// step 1; the create/blobs split is documented on stepBlobs). On create it
// captures the server-assigned id + slug + is_private into the context, which
// every later step consumes (MIO-2543 Task 12).
func stepHub(sc *scaffoldContext, _ *hubtemplate.Template) error {
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
// no double application. The template has no `meta` blob (the hubtemplate schema
// carries no Meta field), so none is sent.
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
// already-scoped hrefs pass through unchanged. Mutates nav in place — the
// template is loaded fresh per invocation, so there is no shared state.
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

func stepBlobs(sc *scaffoldContext, t *hubtemplate.Template) error {
	detail := fmt.Sprintf("PATCH %s — branding+settings+navigation (strict keys)",
		hubsPath(sc.teamID, sc.hubIDOrPlaceholder()))
	return sc.step("blobs", detail, func() error {
		// applyHubBlobs runs the hub-scoped href check but, by design, leaves the
		// navigation SHAPE check to the CALLER (see blobPatches' doc comment): both
		// existing callers (hubs create/update) call validateNavigationBlob first,
		// and the scaffold must too. Without it a malformed template menu — a
		// non-array header/footer bucket, or items missing "type" — is neither
		// shape- nor href-validated (validateNavigationHrefs silently skips a
		// non-array bucket), gets PATCHed, and is then silently dropped by the hub
		// renderer: exactly the silent-drop trap this feature exists to eliminate.
		if t.Navigation != nil {
			if err := validateNavigationBlob(t.Navigation); err != nil {
				return err
			}
			// A template is authored slug-agnostically (e.g. href "/content"), but
			// mio-hub mounts each hub under "/{slug}" and the CLI's MIO-2270 check
			// requires a hub-relative menu href to stay within this hub. The
			// scaffold knows the slug (from create or the resume GET), so rewrite
			// the template's hub-relative hrefs to "/{slug}/…" before applying.
			scopeNavHrefs(t.Navigation, sc.hubSlug)
		}
		_, err := applyHubBlobs(sc.ctx, sc.cl, sc.teamID, sc.hubID, sc.hubSlug, blobPatches{
			Branding:     t.Branding,
			Settings:     t.Settings,
			Navigation:   t.Navigation,
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
func stepSpaces(sc *scaffoldContext, t *hubtemplate.Template) error {
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
func templateSpaceInput(s hubtemplate.Space) SpaceInput {
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
func stepOnboarding(sc *scaffoldContext, t *hubtemplate.Template) error {
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
func templateAttrDefInput(d hubtemplate.AttrDef) AttrDefInput {
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
func stepPolicies(sc *scaffoldContext, t *hubtemplate.Template) error {
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
func stepPlaylists(sc *scaffoldContext, t *hubtemplate.Template) error {
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

// homepageTitle / homepageSlug are the conventional identity of the scaffolded
// homepage. The template's HomepageRef carries only the catalog template to
// instantiate (the experience), not a page name — the operator's hub already has
// its own identity — so the page itself gets a stable, conventional title+slug.
const (
	homepageTitle = "Home"
	// The homepage is marked by is_homepage:true, not by its slug — and the
	// backend RESERVES the slug "home" (rejects it, telling you to use the
	// is_homepage flag). So the page carries a non-reserved slug.
	homepageSlug  = "homepage"
)

// stepHomepage builds the hub's homepage: it creates a "home" page (reusing an
// existing one on resume) and PUTs its draft node-tree, instantiated OFFLINE from
// the vendored page-builder catalog (design §Apply pipeline step 7, MIO-2543 Task
// 18).
//
// OFFLINE-ONLY catalog: the homepage tree is instantiated from the embedded,
// digest-pinned vendored catalog (catalog.Load) — NEVER a live fetch — so a
// scaffold is fully self-contained and needs no network beyond the hub's own API.
//
// IDEMPOTENCY / If-Match: the homepage is looked up by page slug (is_homepage /
// slug) with an EXHAUSTIVE page list. On a fresh hub no home page exists, so the
// step creates it and PUTs the first tree with If-Match 0 (the first-set OCC
// sentinel the backend accepts only while a page has never had a draft). On resume
// the existing home page is reused (no duplicate) and its current draft_version is
// the If-Match token. The tree PUT can never SILENTLY clobber an existing draft:
// if the detected draft_version is stale the backend rejects the PUT as a loud
// conflict (pages_tree.go's OCC contract), so a resume mismatch surfaces as an
// error to re-run, not a silent overwrite.
func stepHomepage(sc *scaffoldContext, t *hubtemplate.Template) error {
	if t.Homepage == nil || t.Homepage.Template == "" {
		// Template.Validate guarantees a homepage on any LOADED template; this guards
		// a hand-built in-memory template so the step is a clean no-op, never a panic.
		return sc.step("homepage", "no homepage in template", func() error { return nil })
	}
	detail := fmt.Sprintf("POST %s then PUT %s — create the %q page from catalog template %q (offline) and set its draft tree (If-Match 0 first set, else the existing draft_version)",
		pagesPath(sc.teamID, sc.hubIDOrPlaceholder(), ""),
		pagesTreePath(sc.teamID, sc.hubIDOrPlaceholder(), "<page_id>"),
		homepageSlug, t.Homepage.Template)
	return sc.step("homepage", detail, func() error {
		// 1. Resolve + instantiate the homepage tree OFFLINE first, so a bad catalog
		//    ref fails LOUD before any HTTP (never creating an orphan page).
		treeObj, err := instantiateHomepageTree(t.Homepage)
		if err != nil {
			return err
		}

		// 2. Resume detection: reuse an existing home page rather than creating a
		//    duplicate. Its draft_version (the tree PUT's If-Match OCC token) is read
		//    via a tree-get, NOT the page list — the list does not surface it.
		existingID, found, lerr := sc.existingHomepage()
		if lerr != nil {
			return lerr
		}
		ifMatch := 0
		if found {
			sc.homePageID = existingID
			dv, derr := sc.homepageDraftVersion(existingID)
			if derr != nil {
				return derr
			}
			ifMatch = dv
		} else {
			// 3. Create the page with the homepage identity (title/slug/is_homepage +
			//    privacy from the template). buildPageAttrs is the shared `pages create`
			//    builder, so the scaffold gets the same privacy-enum validation.
			attrs, berr := buildPageAttrs(homepagePageInput(t.Homepage))
			if berr != nil {
				return berr
			}
			res, cerr := sc.cl.Create(sc.ctx, pagesPath(sc.teamID, sc.hubID, ""), attrs)
			if cerr != nil {
				return cerr
			}
			sc.homePageID = res.ID
		}

		// 4. Set the draft tree with the If-Match OCC header (mirrors `pages tree
		//    set`). The tree PUT derives the page_draft_trees JSON:API type from the
		//    path via the client's type override.
		res, terr := sc.cl.ActionWithHeaders(
			sc.ctx, client.StyleEnvelope, "PUT",
			pagesTreePath(sc.teamID, sc.hubID, sc.homePageID),
			map[string]any{"tree": treeObj},
			map[string]string{"If-Match": strconv.Itoa(ifMatch)},
		)
		if terr != nil {
			return terr
		}
		// Capture the new draft_version the PUT returns (the OCC token a later
		// publish/edit would use); default to the If-Match on a bodyless response.
		sc.homeDraftVersion = ifMatch
		if res != nil {
			if dv, ok := attrInt(res.Attributes["draft_version"]); ok {
				sc.homeDraftVersion = dv
			}
		}
		return nil
	})
}

// instantiateHomepageTree resolves the homepage's catalog template from the
// VENDORED (offline) catalog and instantiates it into a fresh, settable page tree
// wrapped as {"root":…} (mirroring `pages catalog scaffold` for a page template).
// A ref that is unknown, or that names a section template (no page root), fails
// loud with ExitUsage — never a silent empty homepage.
func instantiateHomepageTree(h *hubtemplate.HomepageRef) (map[string]any, error) {
	cat, err := catalog.Load()
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, err)
	}
	tmpl, ok := cat.TemplateByID(h.Template)
	if !ok {
		return nil, errs.New(errs.ExitUsage,
			"homepage template %q is not in the page-builder catalog — run 'mio pages catalog templates' for valid ids", h.Template)
	}
	if !tmpl.IsPage {
		return nil, errs.New(errs.ExitUsage,
			"homepage template %q is a section template, not a page template — the homepage needs a whole-page root", h.Template)
	}
	node, err := catalog.InstantiateTemplate(tmpl, h.Variant, catalog.NewUUIDv7Gen())
	if err != nil {
		return nil, errs.Wrap(errs.ExitGeneric, err)
	}
	return map[string]any{"root": node}, nil
}

// homepagePageInput maps the template HomepageRef onto the PageInput the shared
// buildPageAttrs consumes: the conventional Home title/slug, is_homepage true, and
// the optional privacy (validated by buildPageAttrs).
func homepagePageInput(h *hubtemplate.HomepageRef) PageInput {
	title, slug, isHome := homepageTitle, homepageSlug, true
	in := PageInput{Title: &title, Slug: &slug, IsHome: &isHome}
	if h.Privacy != "" {
		priv := h.Privacy
		in.Privacy = &priv
	}
	return in
}

// existingHomepage finds the hub's homepage page with an EXHAUSTIVE page list
// (same cursor convention + stall guard as the other steps' lookups) so a resume
// reuses it instead of creating a duplicate. It matches ONLY is_homepage:true —
// the authoritative "this is the homepage" signal — so it never hijacks an
// unrelated page that merely shares the conventional "home" slug. It returns just
// the page id; the caller reads draft_version separately via a tree-get, because
// the page list does not surface draft_version.
func (sc *scaffoldContext) existingHomepage() (id string, found bool, err error) {
	query := url.Values{}
	seen := map[string]bool{}
	const maxPages = 1000 // hard ceiling; a hub never has this many pages
	for page := 0; page < maxPages; page++ {
		col, lerr := sc.cl.List(sc.ctx, pagesPath(sc.teamID, sc.hubID, ""), query)
		if lerr != nil {
			return "", false, lerr
		}
		for _, r := range col.Data {
			if isHome, _ := r.Attributes["is_homepage"].(bool); isHome {
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

// homepageDraftVersion reads an existing page's current draft_version via a
// tree-get (RetrieveWithQuery on the author draft, mirroring `pages tree get`) —
// the authoritative OCC source the rest of the codebase uses (pages tree get /
// pages publish), since the page LIST does not surface draft_version. A page that
// has never had a draft 404s on tree-get; that is TOLERATED as draft_version 0
// (the first-set sentinel), so a resume onto a created-but-never-tree-set page
// still sets the first tree. Any other error propagates.
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
func stepPublish(sc *scaffoldContext, _ *hubtemplate.Template) error {
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
func stepBackendGated(sc *scaffoldContext, _ *hubtemplate.Template) error {
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
mid-pipeline failure). --dry-run prints the ordered plan and makes no changes.`,
	Example: `  mio hubs scaffold --template community --name "My Community" --slug my-community
  mio hubs scaffold --template community --name "My Community" --slug my-community --dry-run
  mio hubs scaffold --template community --hub hub_abc123`,
	Args: cobra.NoArgs,
	RunE: runHubsScaffold,
}

func runHubsScaffold(cmd *cobra.Command, _ []string) error {
	// 1. Load + validate the template BEFORE any HTTP so an unknown or malformed
	//    template exits ExitUsage without touching the network (design §Error
	//    handling; TestScaffold_UnknownTemplate).
	templateID, ferr := cmd.Flags().GetString("template")
	if ferr != nil {
		return errs.New(errs.ExitUsage, "--template: %s", ferr.Error())
	}
	tmpl, err := hubtemplate.Load(templateID)
	if err != nil {
		return errs.Wrap(errs.ExitUsage, err)
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
		spaceIDsBySlug:       map[string]string{},
		defIDsBySlug:         map[string]string{},
		playlistIDsByKey:     map[string]string{},
		publish:              publish,
		dryRun:               dryRun,
		plan:                 &plan,
	}

	// 3. Resume/target mode: an EXPLICIT --hub applies onto an existing hub. The
	//    discriminator MUST be the explicit flag (flags.hub, set only by --hub),
	//    NOT c.resolved.HubID — the latter merges a config/profile default hub
	//    (`mio config set hub`), so gating on it would silently turn the headline
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
		if p, ok := res.Attributes["is_private"].(bool); ok {
			sc.isPrivate, sc.isPrivateKnown = p, true
		}
	}

	if scaffoldAfterResolve != nil {
		scaffoldAfterResolve(sc)
	}

	// 4. Run the pipeline in order. Each step decides for itself whether to fire
	//    HTTP or (in dry-run) record its plan entry — see sc.step — so the runner
	//    just dispatches and, on failure, prints the recovery guidance and returns
	//    the step-tagged error (rendered once by main.go).
	for _, step := range scaffoldPipeline {
		if serr := step.run(sc, tmpl); serr != nil {
			printScaffoldRecovery(cmd.ErrOrStderr(), sc, templateID)
			return errs.Wrap(errs.CodeOf(serr),
				fmt.Errorf("scaffold: step %q failed: %w", step.name, serr))
		}
	}

	if dryRun {
		printScaffoldPlan(cmd.OutOrStdout(), templateID, plan)
	} else {
		// Real run: echo the finished hub's reference + published state + a recap so
		// the operator knows what landed and how to go live (design §Apply pipeline
		// step 8 + §Command surface). Dry-run keeps the plan output untouched.
		printScaffoldSummary(cmd.OutOrStdout(), sc, tmpl, templateID)
	}
	return nil
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
	if sc.hubID != "" {
		fmt.Fprintf(w, "Hub %s was created but the scaffold did not finish (no rollback).\n", sc.hubID)
		fmt.Fprintf(w, "Resume with: mio hubs scaffold --hub %s --template %s\n", sc.hubID, templateID)
	}
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
func printScaffoldSummary(w io.Writer, sc *scaffoldContext, t *hubtemplate.Template, templateID string) {
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
	if t.Homepage != nil && t.Homepage.Template != "" {
		parts = append(parts, "homepage set")
	}
	fmt.Fprintf(w, "  Includes: %s.\n", strings.Join(parts, ", "))

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

// hubsTemplatesCmd lists the built-in hub scaffold templates that
// `hubs scaffold --template <id>` can apply. The templates are embedded in the
// binary (internal/hubtemplate), so this is a purely OFFLINE listing — it needs
// no API key. It renders through the standard output layer (c.render) so
// --output json|table|plain and --jq all work like any other list command.
var hubsTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List the built-in hub scaffold templates.",
	Long: `List the built-in hub scaffold templates that 'mio hubs scaffold --template <id>'
can apply. Each template is embedded in the binary, so this command needs no
network access or credentials.`,
	Example: `  mio hubs templates
  mio hubs templates --output json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		ids := hubtemplate.List()
		rows := make([]any, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, map[string]any{"id": id})
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

	hubsCmd.AddCommand(hubsScaffoldCmd)
	hubsCmd.AddCommand(hubsTemplatesCmd)
}
