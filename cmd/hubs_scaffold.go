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
	"strings"

	"github.com/spf13/cobra"

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

	// homePageID + homeDraftVersion are minted/read by the Phase 4 homepage step
	// (stepHomepage: pages create → tree set via draft_version). Declared now so
	// the context shape is stable across the staged rollout.
	homePageID       string //nolint:unused // consumed by the Phase 4 homepage step
	homeDraftVersion int    //nolint:unused // consumed by the Phase 4 homepage step

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
		sc.isPrivate, _ = res.Attributes["is_private"].(bool)
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
func stepOnboarding(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("onboarding", "", func() error { return nil })
}
func stepPolicies(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("policies", "", func() error { return nil })
}
func stepPlaylists(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("playlists", "", func() error { return nil })
}
func stepHomepage(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("homepage", "", func() error { return nil })
}
func stepPublish(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("publish", "", func() error { return nil })
}
func stepBackendGated(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("backend-gated", "", func() error { return nil })
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
		sc.isPrivate, _ = res.Attributes["is_private"].(bool)
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

func init() {
	// Self-register on the hubs group (hubsCmd is defined in hubs.go). CORE flags
	// only (MIO-2543 Task 11); Task 21 adds --publish + the --favicon-url/
	// --logo-url/--registration-enabled overrides and the `hubs templates` list
	// command. --hub is the persistent root context flag inherited here, so it is
	// NOT redefined locally (a local redefinition would shadow the persistent one
	// and drop it from the resolved context).
	hubsScaffoldCmd.Flags().String("template", "", "Template id to apply (e.g. community). Required.")
	hubsScaffoldCmd.Flags().String("name", "", "Display name for the new hub (create mode).")
	hubsScaffoldCmd.Flags().String("slug", "", "URL slug for the new hub (create mode).")
	hubsScaffoldCmd.Flags().Bool("dry-run", false, "Print the ordered plan and make no changes.")
	_ = hubsScaffoldCmd.MarkFlagRequired("template")

	hubsCmd.AddCommand(hubsScaffoldCmd)
}
