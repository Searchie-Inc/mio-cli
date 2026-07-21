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

// Step bodies — no-ops for the skeleton (MIO-2543 Task 11). Each routes through
// sc.step so the dry-run branch is exercised (it records the stage and fires no
// HTTP). Phase 4 fills the fn body with the extracted builders + client calls
// and enriches the detail arg; the runner stays unchanged.
func stepHub(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("hub", "", func() error { return nil })
}
func stepBlobs(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("blobs", "", func() error { return nil })
}
func stepSpaces(sc *scaffoldContext, _ *hubtemplate.Template) error {
	return sc.step("spaces", "", func() error { return nil })
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

	var plan []planEntry
	sc := &scaffoldContext{
		ctx:              c.ctx,
		cl:               c.client,
		teamID:           teamID,
		spaceIDsBySlug:   map[string]string{},
		defIDsBySlug:     map[string]string{},
		playlistIDsByKey: map[string]string{},
		dryRun:           dryRun,
		plan:             &plan,
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
