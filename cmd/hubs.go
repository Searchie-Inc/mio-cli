package cmd

// hubs.go — `mio hubs` command group.
//
// Routes (see docs/internal/api-surface.md "hubs"):
//
//	create                   POST   /api/teams/{team_id}/hubs
//	list                     GET    /api/teams/{team_id}/hubs
//	retrieve                 GET    /api/teams/{team_id}/hubs/{id}
//	update                   PATCH  /api/teams/{team_id}/hubs/{id}
//	delete                   DELETE /api/teams/{team_id}/hubs/{id}
//	policies update          PATCH  /api/teams/{team_id}/hubs/{hub_id}/policies
//	policies gate            PATCH  /api/teams/{team_id}/hubs/{hub_id}/policies/gate
//	redirect-origins get     GET    /api/teams/{team_id}/hubs/{hub_id}/redirect-origins
//	redirect-origins set     PUT    /api/teams/{team_id}/hubs/{hub_id}/redirect-origins
//	email-settings get       GET    /api/teams/{team_id}/hubs/{hub_id}/email-settings
//	email-settings update    PATCH  /api/teams/{team_id}/hubs/{hub_id}/email-settings
//
// All routes are team-scoped. Hub id comes from a positional argument (not the
// --hub context flag) so operators can manage any hub, not just the active one.
//
// NOTE: there is no admin/team-scoped policies READ. The only policies GET is
// the hub portal route /api/hubs/{hub_id}/policies, which requires member
// (contact) auth and rejects admin API keys with 401 — so it is intentionally
// not exposed here (see MIO-2269 deferred items).

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
	"github.com/Searchie-Inc/mio-cli/internal/output"
)

func init() {
	hubsCmd.AddCommand(
		hubsCreateCmd,
		hubsListCmd,
		hubsRetrieveCmd,
		hubsUpdateCmd,
		hubsDeleteCmd,
	)

	// hubs policies <action>  (nested sub-resource)
	hubsPoliciesCmd.AddCommand(
		hubsPoliciesUpdateCmd,
		hubsPoliciesGateCmd,
	)
	hubsCmd.AddCommand(hubsPoliciesCmd)

	// hubs redirect-origins <action>  (magic-link allowlist, MIO-616)
	hubsRedirectOriginsCmd.AddCommand(
		hubsRedirectOriginsGetCmd,
		hubsRedirectOriginsSetCmd,
	)
	hubsCmd.AddCommand(hubsRedirectOriginsCmd)

	// hubs email-settings <action>  (per-hub sender identity, MIO-1229)
	hubsEmailSettingsCmd.AddCommand(
		hubsEmailSettingsGetCmd,
		hubsEmailSettingsUpdateCmd,
	)
	hubsCmd.AddCommand(hubsEmailSettingsCmd)

	rootCmd.AddCommand(hubsCmd)
}

// ---- hubs group -------------------------------------------------------------

var hubsCmd = &cobra.Command{
	Use:   "hubs",
	Short: "Manage hubs.",
	Long:  "Create, list, retrieve, update and delete hubs for the active team.",
	Example: `  mio hubs list
  mio hubs create --name "My Community" --slug my-community
  mio hubs retrieve hub_abc123
  mio hubs update hub_abc123 --name "Renamed Community"
  mio hubs delete hub_abc123`,
}

// hubsPath returns /api/teams/{team_id}/hubs[/{id}].
func hubsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// hubsContext is shared boilerplate: build context, require auth, resolve team.
func hubsContext(cmd *cobra.Command) (*cmdContext, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, "", err
	}
	return c, teamID, nil
}

// injectHubDerivedState adds convenience booleans to a hub resource's rendered
// attributes so the CLI surfaces state the backend only encodes indirectly:
//
//   - registration_enabled — settings.registration.enabled === true, fail-closed
//     (any missing/non-object/non-true value is false), mirroring the backend read
//     path (app/hubs/registration.py, MIO-761) and the CLI's own
//     --registration-enabled vocabulary (MIO-2516).
//   - published — the inverse of is_private, in the CLI's --published vocabulary
//     (MIO-2521); only added when is_private is present.
//
// These are DERIVED views for readability; they are never sent back on a write,
// and --raw output bypasses them (it renders the untouched API envelope). It
// mirrors how the backend already ships a derived policies_enabled field.
func injectHubDerivedState(res *client.Resource) {
	if res == nil || res.Attributes == nil {
		return
	}
	res.Attributes["registration_enabled"] = hubRegistrationEnabled(res.Attributes)
	if v, ok := res.Attributes["is_private"].(bool); ok {
		res.Attributes["published"] = !v
	}
}

// hubRegistrationEnabled reports settings.registration.enabled === true,
// fail-closed: any missing key, non-object node, or non-true value is false.
func hubRegistrationEnabled(attrs map[string]any) bool {
	settings, ok := attrs["settings"].(map[string]any)
	if !ok {
		return false
	}
	reg, ok := settings["registration"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := reg["enabled"].(bool)
	return enabled
}

// printHubCreateHint writes a human-only (stderr) note after `hubs create`
// explaining the hub's private/published state and how to publish it. It runs
// only in table (human) format so --output json/yaml on stdout is never
// corrupted (same rationale as the blob-key warnings).
//
// HONESTY (MIO-2521): the hub's public URL is NOT echoed. The create response
// carries no domain/url field (verified against app/hubs/schemas.py HubAttributes
// and models.py — there is no domain column) and the CLI knows only the API base,
// not the hub-frontend host, so a fabricated URL scheme would be dishonest. The
// slug is surfaced as the best-available public reference and the limitation is
// stated plainly.
func printHubCreateHint(cmd *cobra.Command, res *client.Resource) {
	if res == nil {
		return
	}
	slug, _ := res.Attributes["slug"].(string)
	isPrivate, _ := res.Attributes["is_private"].(bool)
	w := cmd.ErrOrStderr()
	if isPrivate {
		fmt.Fprintf(w, "Created hub %s — private/unpublished (not reachable by members yet).\n", res.ID)
		if slug != "" {
			fmt.Fprintf(w, "  Slug: %s\n", slug)
		}
		fmt.Fprintf(w, "  Publish it with: mio hubs update %s --published\n", res.ID)
	} else {
		fmt.Fprintf(w, "Created hub %s — published.\n", res.ID)
		if slug != "" {
			fmt.Fprintf(w, "  Slug: %s\n", slug)
		}
	}
	fmt.Fprintln(w, "  Note: the public hub URL is not returned by the API and cannot be derived by the CLI; combine the slug above with your hub-frontend host.")
}

// ---- create -----------------------------------------------------------------

var hubsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a hub.",
	Long:  "Create a new hub for the active team.",
	Example: `  mio hubs create --name "My Community" --slug my-community
  mio hubs create --name "Support Hub" --slug support --description "Help articles"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Build and validate attributes BEFORE resolving auth/team: a malformed
		// flag must exit with a usage error and fire NO HTTP request, even when
		// --team is a name/slug that would otherwise trigger a resolution GET.
		// The typed-column base is assembled here with the shared flag helpers
		// (which encode the partial-update "only changed flags" semantics); the
		// presentation blobs, logo/favicon overrides and all shape validation are
		// applied by buildHubCreateAttrs so the scaffold (MIO-2543) can build the
		// same POST body without a *cobra.Command.
		base := map[string]any{}
		setMappedString(cmd, base, "name", "title")
		setStringFlag(cmd, base, "slug")
		setStringFlag(cmd, base, "description")
		setMappedBoolInverted(cmd, base, "published", "is_private")
		// discussions_default_title/description are typed columns (MIO-2274) — a
		// plain partial update; an empty string clears to null.
		setNullableMappedString(cmd, base, "discussions-default-title", "discussions_default_title")
		setNullableMappedString(cmd, base, "discussions-default-description", "discussions_default_description")

		// Presentation-blob flags: opaque JSONB objects passed through verbatim so
		// an operator or agent can author a hub's branding, navigation, settings and
		// feature-guard meta in the same POST. Parsed in a stable order so the first
		// malformed blob is the one reported. (MIO-2254)
		branding, err := parseJSONObjectFlag(cmd, "branding-json")
		if err != nil {
			return err
		}
		navigation, err := parseJSONObjectFlag(cmd, "navigation-json")
		if err != nil {
			return err
		}
		settings, err := parseJSONObjectFlag(cmd, "settings-json")
		if err != nil {
			return err
		}
		meta, err := parseJSONObjectFlag(cmd, "meta-json")
		if err != nil {
			return err
		}

		// --logo-url / --favicon-url merge into the branding object (a nil pointer
		// means the flag was not given, so branding is left untouched); the merge
		// itself lives in buildHubCreateAttrs. (MIO-2522)
		var logo *string
		if cmd.Flags().Changed("logo-url") {
			v, ferr := cmd.Flags().GetString("logo-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--logo-url: %s", ferr)
			}
			logo = &v
		}
		var favicon *string
		if cmd.Flags().Changed("favicon-url") {
			v, ferr := cmd.Flags().GetString("favicon-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--favicon-url: %s", ferr)
			}
			favicon = &v
		}
		strictKeys, _ := cmd.Flags().GetBool("strict-keys")

		// buildHubCreateAttrs runs BEFORE hubsContext() below so any blob/nav/key
		// validation failure exits with a usage error and fires no HTTP request,
		// even when --team is a name/slug that would otherwise trigger a resolution
		// GET (MIO-2254/2255/2515).
		attrs, err := buildHubCreateAttrs(hubCreateParams{
			Base:       base,
			Branding:   branding,
			Navigation: navigation,
			Settings:   settings,
			Meta:       meta,
			Logo:       logo,
			Favicon:    favicon,
			Strict:     strictKeys,
		}, cmd.ErrOrStderr())
		if err != nil {
			return err
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Create(c.ctx, hubsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		// Surface the private/published state as a derived attribute so it is
		// explicit in json/yaml/table output (MIO-2521).
		injectHubDerivedState(res)
		if rerr := c.render(cmd, res); rerr != nil {
			return rerr
		}
		// Human-only discoverability hint (table format): a freshly created hub is
		// private by default with no signal it is unreachable. Written to stderr so
		// --output json/yaml on stdout is never corrupted (same rationale as the
		// blob-key warnings). (MIO-2521)
		if c.out.Format == output.FormatTable {
			printHubCreateHint(cmd, res)
		}
		return nil
	},
}

// hubCreateParams carries the pre-parsed inputs for `hubs create`, decoupled from
// *cobra.Command so both the command and the scaffold (MIO-2543) can build the
// same POST body. The CALLER owns flag parsing (each --*-json blob, --logo-url/
// --favicon-url); buildHubCreateAttrs owns the shape validation the command
// performs — the navigation blob + hub-scoped href check (the slug is the create
// --slug value, known up front, so no retrieve is needed) and the best-effort
// blob-key check. It mirrors blobPatches, the update-side equivalent.
type hubCreateParams struct {
	// Base is the typed-column attribute set the caller already built with the
	// set* flag helpers (title/slug/description/is_private/discussions_*). The
	// blobs and logo/favicon merges are layered onto a COPY of it, so the caller's
	// map is never mutated.
	Base map[string]any

	// Branding/Navigation/Settings/Meta are the parsed --*-json blobs, passed
	// through verbatim (nil = flag not given).
	Branding   map[string]any
	Navigation map[string]any
	Settings   map[string]any
	Meta       map[string]any

	// Logo/Favicon are the scalar convenience overrides merged into branding
	// (a nil pointer means the flag was not given).
	Logo    *string // --logo-url → branding.logo_url
	Favicon *string // --favicon-url → branding.favicon_url

	// Strict selects strict blob-key validation (an unknown key errors instead of
	// warning). Only the INCOMING blob keys are inspected.
	Strict bool
}

// buildHubCreateAttrs assembles the POST body for `hubs create` from p: it layers
// the presentation blobs onto a COPY of the typed-column base, validates the
// navigation blob + hub-scoped hrefs (the slug is the create --slug value, known
// up front), merges the --logo-url/--favicon-url overrides into branding, runs
// the best-effort blob-key check, and rejects an empty create. It is a pure
// builder: it takes no *cobra.Command and mutates neither p.Base nor p.Branding.
// warnW receives the best-effort blob-key warnings.
func buildHubCreateAttrs(p hubCreateParams, warnW io.Writer) (map[string]any, error) {
	// Start from a COPY of the caller's base attrs so the blob merges never mutate
	// p.Base (the scaffold may reuse a template's base across hubs).
	attrs := make(map[string]any, len(p.Base)+4)
	for k, v := range p.Base {
		attrs[k] = v
	}
	if p.Branding != nil {
		attrs["branding"] = p.Branding
	}
	if p.Navigation != nil {
		attrs["navigation"] = p.Navigation
	}
	if p.Settings != nil {
		attrs["settings"] = p.Settings
	}
	if p.Meta != nil {
		attrs["meta"] = p.Meta
	}

	// Untyped header/footer items are dropped by the hub renderer, so reject them
	// up front rather than shipping a menu that renders empty (MIO-2255). Hub-
	// relative url hrefs must stay within this hub's "/{slug}" mount — on create
	// the slug is the --slug flag value carried in Base (MIO-2270).
	if nav, ok := attrs["navigation"].(map[string]any); ok {
		if err := validateNavigationBlob(nav); err != nil {
			return nil, err
		}
		slug, _ := attrs["slug"].(string)
		if err := validateNavigationHrefs(nav, slug); err != nil {
			return nil, err
		}
	}

	// --logo-url / --favicon-url merge into the branding object rather than
	// replacing it, so they compose with --branding-json (the backend assigns
	// branding wholesale, so the CLI must send one already-merged object). The
	// merge is applied on a COPY so p.Branding is never mutated. (MIO-2522)
	if p.Logo != nil || p.Favicon != nil {
		branding := attrMap(attrs["branding"])
		if p.Logo != nil {
			branding["logo_url"] = *p.Logo
		}
		if p.Favicon != nil {
			branding["favicon_url"] = *p.Favicon
		}
		attrs["branding"] = branding
	}

	// Best-effort key validation for the opaque JSONB blobs: an unknown key is
	// stored verbatim by the API (no server-side schema), so a typo silently has
	// no effect. Warn by default (to warnW, so --output json/yaml on stdout is
	// never corrupted); Strict turns it into a usage error. (MIO-2515)
	if b, ok := attrs["branding"].(map[string]any); ok {
		if err := validateBlobKeys(warnW, "branding", b, brandingKeys, nil, p.Strict); err != nil {
			return nil, err
		}
	}
	if s, ok := attrs["settings"].(map[string]any); ok {
		if err := validateBlobKeys(warnW, "settings", s, settingsKeys, settingsNestedKeys, p.Strict); err != nil {
			return nil, err
		}
	}
	if m, ok := attrs["meta"].(map[string]any); ok {
		if err := validateBlobKeys(warnW, "meta", m, metaKeys, nil, p.Strict); err != nil {
			return nil, err
		}
	}

	if len(attrs) == 0 {
		return nil, errs.New(errs.ExitUsage, "nothing to create: set at least --name")
	}

	return attrs, nil
}

// ---- list -------------------------------------------------------------------

var hubsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List hubs.",
	Long:    "List all hubs for the active team.",
	Example: `  mio hubs list`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, hubsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- retrieve ---------------------------------------------------------------

var hubsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a hub by id.",
	Long:    "Retrieve a single hub by its id.",
	Example: `  mio hubs retrieve hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, hubsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		// Surface the registration-enabled and published state as derived
		// convenience attributes (fail-closed), mirroring the backend's own derived
		// policies_enabled field (MIO-2516 surface side, MIO-2521).
		injectHubDerivedState(res)
		return c.render(cmd, res)
	},
}

// ---- update -----------------------------------------------------------------

var hubsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a hub by id.",
	Long:  "Update one or more fields on a hub. Only the flags you provide are changed (partial update).",
	Example: `  mio hubs update hub_abc123 --name "New Name"
  mio hubs update hub_abc123 --published=true`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Build and validate attributes BEFORE resolving auth/team so a malformed
		// flag exits with a usage error and fires no HTTP request. The read-modify-
		// write of the whole-blob JSONB fields (branding/settings/meta), the nav
		// REPLACE and the --unset removals are handled by applyHubBlobs below.
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "name", "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		// --published → is_private (inverted), via publishedStateAttrs so the
		// scaffold publish step (MIO-2543) shares the exact inversion. Gated on
		// Changed() so an unset flag stays out of the PATCH (partial update); a
		// GetBool failure on a registered bool flag cannot occur, so it is silently
		// skipped, exactly as setMappedBoolInverted did.
		if cmd.Flags().Changed("published") {
			if published, perr := cmd.Flags().GetBool("published"); perr == nil {
				for k, v := range publishedStateAttrs(published) {
					attrs[k] = v
				}
			}
		}
		setNullableMappedString(cmd, attrs, "discussions-default-title", "discussions_default_title")
		setNullableMappedString(cmd, attrs, "discussions-default-description", "discussions_default_description")

		// --navigation-json authors the header/footer menu — a whole-blob REPLACE
		// (MIO-2255). The SHAPE check (typed items the mio-hub parser requires) runs
		// pre-auth so a malformed menu fires NO HTTP request, not even a team-
		// resolution GET. The hub-scoped href check (MIO-2270) needs the hub's final
		// slug and is applied inside applyHubBlobs, which also injects the validated
		// navigation blob into the PATCH — so navigation is passed ONLY via
		// blobPatches.Navigation and is never put in Base.
		nav, err := parseJSONObjectFlag(cmd, "navigation-json")
		if err != nil {
			return err
		}
		// navSlug is the hub's FINAL slug when --slug is set in this same update
		// (setStringFlag only populates attrs["slug"] when the flag changed); it is
		// authoritative over the hub's current slug, so hrefs must scope to it.
		navSlug, navSlugKnown := attrs["slug"].(string)
		if nav != nil {
			if err := validateNavigationBlob(nav); err != nil {
				return err
			}
		}

		// --branding-json / --settings-json / --meta-json are read-modify-write:
		// these blobs are assigned WHOLESALE server-side, so a partial edit would
		// clobber sibling keys. Parse now (fail fast on bad JSON); the deep-merge
		// against the hub's current blobs happens inside applyHubBlobs. (MIO-2256)
		branding, err := parseJSONObjectFlag(cmd, "branding-json")
		if err != nil {
			return err
		}
		settings, err := parseJSONObjectFlag(cmd, "settings-json")
		if err != nil {
			return err
		}
		meta, err := parseJSONObjectFlag(cmd, "meta-json")
		if err != nil {
			return err
		}
		// --logo-url / --favicon-url merge into branding (branding.logo_url /
		// branding.favicon_url); a nil pointer means the flag was not given, so that
		// override leaves the blob untouched. (MIO-901, MIO-2522)
		var logo *string
		if cmd.Flags().Changed("logo-url") {
			v, ferr := cmd.Flags().GetString("logo-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--logo-url: %s", ferr)
			}
			logo = &v
		}
		var favicon *string
		if cmd.Flags().Changed("favicon-url") {
			v, ferr := cmd.Flags().GetString("favicon-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--favicon-url: %s", ferr)
			}
			favicon = &v
		}
		// --registration-enabled sets settings.registration.enabled. A bare bool
		// cannot distinguish false-from-unset, so gate on Changed() (same pattern as
		// the policies gate --enabled flag) and read the value only when set;
		// applyHubBlobs preserves sibling settings.* AND registration.* keys.
		// (MIO-2516)
		var registration *bool
		if cmd.Flags().Changed("registration-enabled") {
			v, ferr := cmd.Flags().GetBool("registration-enabled")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--registration-enabled: %s", ferr)
			}
			registration = &v
		}
		// --unset deletes keys from the branding/settings/meta blobs by dotted path
		// (read-modify-write). Parsed/validated pre-auth so a bad path exits
		// ExitUsage and fires no HTTP request. (MIO-2517)
		unsetPaths, err := parseUnsetFlag(cmd)
		if err != nil {
			return err
		}
		strictKeys, _ := cmd.Flags().GetBool("strict-keys")

		rmw := branding != nil || settings != nil || meta != nil || logo != nil ||
			favicon != nil || registration != nil || len(unsetPaths) > 0

		// navigation is a whole-blob REPLACE carried by blobPatches.Navigation (no
		// longer in attrs), so it counts as a change here too.
		if len(attrs) == 0 && !rmw && nav == nil {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		res, err := applyHubBlobs(c.ctx, c.client, teamID, args[0], navSlug, blobPatches{
			Base:         attrs,
			Branding:     branding,
			Settings:     settings,
			Meta:         meta,
			Navigation:   nav,
			SlugKnown:    navSlugKnown,
			Logo:         logo,
			Favicon:      favicon,
			Registration: registration,
			Unset:        unsetPaths,
			Strict:       strictKeys,
		}, cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		injectHubDerivedState(res)
		return c.render(cmd, res)
	},
}

// publishedStateAttrs maps the CLI's --published intent onto the single writable
// backend attribute, is_private, with the polarity inverted (published=true →
// is_private=false). "published" itself is NOT a writable attribute, so it is
// never emitted. Both `hubs update`'s --published handling and the scaffold's
// publish step (MIO-2543) route through this one helper so the inversion has a
// single source of truth.
func publishedStateAttrs(published bool) map[string]any {
	return map[string]any{"is_private": !published}
}

// blobPatches carries the parsed, pre-validated blob edits for a hub read-modify-
// write update, decoupled from *cobra.Command so both `hubs update` and future
// scaffold commands (MIO-2543) can drive the same retrieve → deep-merge → scalar
// override → unset → PATCH.
//
// The CALLER owns the pre-auth input checks that must fire NO HTTP request (not
// even a team-resolution GET): parsing each flag, and validating the navigation
// SHAPE with validateNavigationBlob. applyHubBlobs owns the rest — the blob-key
// check and the hub-scoped href check (which needs the hub's final slug: the
// --slug value when it is changing, else the live slug from the retrieve).
type blobPatches struct {
	// Base is the typed-column attribute set the caller already built
	// (title/slug/description/is_private/discussions_*). The merged blobs, the
	// navigation REPLACE and the --unset removals are layered onto a COPY of it,
	// so the caller's map is never mutated. Navigation is NOT carried here — set
	// it via Navigation below so it is validated and injected as one unit.
	Base map[string]any

	// Branding/Settings/Meta are the parsed --*-json patches, deep-merged onto the
	// hub's current blob (nil = flag not given, so that blob is untouched).
	Branding map[string]any
	Settings map[string]any
	Meta     map[string]any

	// Navigation is the parsed navigation blob (whole-blob REPLACE); nil = not
	// given. It is the SINGLE source of navigation: applyHubBlobs validates its
	// hub-scoped hrefs and injects it into the PATCH body itself, so callers must
	// NOT also place a "navigation" key in Base.
	Navigation map[string]any

	// SlugKnown reports that hubSlug (the applyHubBlobs param) is authoritative and
	// known up front (on `hubs update` it came from --slug), so it is used directly
	// for href scoping and no retrieve is needed solely to learn the hub's slug.
	SlugKnown bool

	// Logo/Favicon/Registration are the scalar convenience overrides, applied
	// AFTER the --*-json deep-merge (a nil pointer means the flag was not given).
	Logo         *string // --logo-url → branding.logo_url
	Favicon      *string // --favicon-url → branding.favicon_url
	Registration *bool   // --registration-enabled → settings.registration.enabled

	// Unset are the parsed --unset paths, applied LAST (after merges + overrides).
	Unset []unsetPath

	// Strict selects strict blob-key validation (an unknown key errors instead of
	// warning). Only the INCOMING patch keys are inspected, never the merged blob.
	Strict bool
}

// applyHubBlobs performs the hub read-modify-write: it validates the incoming
// blob keys and hub-scoped navigation hrefs, retrieves the hub's current blobs
// when a deep-merge/unset (or the live-slug href check) needs them, layers the
// patches on in the documented order — (1) --*-json deep-merge, (2) scalar
// overrides, (3) --unset removals LAST — and PATCHes the merged object so
// untouched siblings survive.
//
// hubSlug is the hub's FINAL slug when it is known up front (p.SlugKnown true —
// on `hubs update` that is the --slug value), else ""; when the slug is not
// known up front the retrieved slug is used for href scoping. warnW receives the
// best-effort blob-key warnings (the command path passes cmd.ErrOrStderr()).
//
// It is a pure builder: it takes no *cobra.Command and does not mutate p.Base.
func applyHubBlobs(ctx context.Context, cl *client.Client, teamID, hubID, hubSlug string, p blobPatches, warnW io.Writer) (*client.Resource, error) {
	navSet := p.Navigation != nil

	// Hub-scoped href check for the known-slug case: the final slug is authoritative
	// (from --slug), so validate now (no retrieve needed for the check, MIO-2270).
	// The unknown-slug case is validated against the live slug after the retrieve.
	if navSet && p.SlugKnown {
		if err := validateNavigationHrefs(p.Navigation, hubSlug); err != nil {
			return nil, err
		}
	}

	// Best-effort key validation on the INCOMING patch keys only (never the merged
	// blob — older hubs legitimately carry unlisted keys the caller did not touch).
	// Runs BEFORE the retrieve so a strict rejection fires no PATCH/GET. (MIO-2515)
	if err := validateBlobKeys(warnW, "branding", p.Branding, brandingKeys, nil, p.Strict); err != nil {
		return nil, err
	}
	if err := validateBlobKeys(warnW, "settings", p.Settings, settingsKeys, settingsNestedKeys, p.Strict); err != nil {
		return nil, err
	}
	if err := validateBlobKeys(warnW, "meta", p.Meta, metaKeys, nil, p.Strict); err != nil {
		return nil, err
	}

	// Start the PATCH body from a COPY of the caller's base attrs (typed columns)
	// so the merged blobs and unset removals never mutate p.Base. Navigation is a
	// whole-blob REPLACE: inject the validated blob here (the SINGLE source), so it
	// always ships when set — regardless of whether the retrieve below runs.
	attrs := make(map[string]any, len(p.Base)+1)
	for k, v := range p.Base {
		attrs[k] = v
	}
	if navSet {
		attrs["navigation"] = p.Navigation
	}

	rmw := p.Branding != nil || p.Settings != nil || p.Meta != nil ||
		p.Logo != nil || p.Favicon != nil || p.Registration != nil || len(p.Unset) > 0
	navNeedsRetrieve := navSet && !p.SlugKnown

	// Retrieve the hub when we need its current whole-blob fields (RMW) or its slug
	// (to validate hub-scoped navigation hrefs when --slug is NOT also changing) —
	// one GET serves both.
	if rmw || navNeedsRetrieve {
		cur, err := cl.Retrieve(ctx, hubsPath(teamID, hubID))
		if err != nil {
			return nil, err
		}
		// Validate hub-relative navigation hrefs against the hub's current slug
		// before the PATCH, so a bad link fails with ExitUsage and no write happens
		// (MIO-2270). The --slug-changing case was already validated above.
		if navNeedsRetrieve {
			slug, _ := cur.Attributes["slug"].(string)
			if err := validateNavigationHrefs(p.Navigation, slug); err != nil {
				return nil, err
			}
		}
		// Deterministic apply ORDER per blob (documented): (1) --*-json deep-merge,
		// (2) scalar convenience overrides (--logo-url/--favicon-url/
		// --registration-enabled), (3) --unset removals LAST — so an explicit unset
		// in the same command wins over a merge.
		if p.Branding != nil || p.Logo != nil || p.Favicon != nil {
			b := attrMap(cur.Attributes["branding"])
			if p.Branding != nil {
				b = deepMergeMap(b, p.Branding)
			}
			if p.Logo != nil {
				b["logo_url"] = *p.Logo
			}
			if p.Favicon != nil {
				b["favicon_url"] = *p.Favicon
			}
			attrs["branding"] = b
		}
		if p.Settings != nil || p.Registration != nil {
			s := attrMap(cur.Attributes["settings"])
			if p.Settings != nil {
				s = deepMergeMap(s, p.Settings)
			}
			if p.Registration != nil {
				// Preserve sibling registration.* keys: copy the current sub-object and
				// set only enabled.
				reg := attrMap(s["registration"])
				reg["enabled"] = *p.Registration
				s["registration"] = reg
			}
			attrs["settings"] = s
		}
		if p.Meta != nil {
			attrs["meta"] = deepMergeMap(attrMap(cur.Attributes["meta"]), p.Meta)
		}
		// --unset removals apply LAST on each staged blob. When a blob is touched
		// ONLY by unset, seed it from the hub's current blob; deleteAtPath copies
		// each node it descends so the retrieved resource is never mutated.
		for _, u := range p.Unset {
			blob, ok := attrs[u.blob].(map[string]any)
			if !ok {
				blob = attrMap(cur.Attributes[u.blob])
			}
			attrs[u.blob] = deleteAtPath(blob, u.segments)
		}
	}

	return cl.Update(ctx, hubsPath(teamID, hubID), attrs)
}

// ---- delete -----------------------------------------------------------------

var hubsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a hub by id.",
	Long:  "Permanently delete a hub. This action cannot be undone. Pass --yes to skip the confirmation prompt.",
	Example: `  mio hubs delete hub_abc123
  mio hubs delete hub_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete hub %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, hubsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted hub %s.\n", args[0])
		return nil
	},
}

// ---- flag registration ------------------------------------------------------

func init() {
	// Shared writable attribute flags for create and update.
	for _, cmd := range []*cobra.Command{hubsCreateCmd, hubsUpdateCmd} {
		cmd.Flags().String("name", "", "Hub display name.")
		cmd.Flags().String("slug", "", "Hub URL slug (unique within the team).")
		cmd.Flags().String("description", "", "Short description of the hub.")
		cmd.Flags().String("logo-url", "", "URL of the hub's logo image (branding.logo_url).")
		cmd.Flags().String("favicon-url", "", "URL of the hub's favicon image (branding.favicon_url). Read-modify-write on update; sibling branding keys are preserved.")
		cmd.Flags().Bool("published", false, "Whether the hub is publicly published.")
		cmd.Flags().String("discussions-default-title", "", "Default title for the hub's discussions surface (MIO-2274). Pass \"\" to clear.")
		cmd.Flags().String("discussions-default-description", "", "Default description for the hub's discussions surface. Pass \"\" to clear.")
		// MIO-2515: an unknown key in --branding-json/--settings-json/--meta-json is
		// stored verbatim (the API has no schema for these blobs), so a typo looks
		// like success. By default the CLI WARNS naming the bad key + the accepted
		// set; --strict-keys makes it a usage error. The allowlist is best-effort —
		// the hub frontend is the authoritative render schema (see the flag help of
		// each *-json flag and docs/internal/api-surface.md for the accepted keys).
		cmd.Flags().Bool("strict-keys", false, "Reject unknown keys in --branding-json/--settings-json/--meta-json with an error instead of a warning (best-effort allowlist; accepted keys are listed in each *-json flag's help and docs/internal/api-surface.md).")
	}

	// Presentation-blob flags, all authorable on create. The accepted keys are
	// listed inline so `--help` surfaces the (best-effort) schema; an unknown key
	// warns by default and errors under --strict-keys (MIO-2515).
	for _, f := range []struct{ name, desc string }{
		{"branding-json", "Hub branding as a JSON object — colors, fonts, logo. Inline JSON or @file. Accepted keys: " + brandingKeysHelp + ". Unknown keys warn (error with --strict-keys)."},
		{"navigation-json", "Hub navigation as a JSON object — header/footer menu items. Inline JSON or @file."},
		{"settings-json", "Hub settings as a JSON object — header/footer chrome, appearance, policies. Inline JSON or @file. Accepted top-level keys: " + settingsKeysHelp + ". Unknown keys warn (error with --strict-keys)."},
		{"meta-json", "Hub meta as a JSON object — feature guards. Inline JSON or @file. Accepted keys: " + metaKeysHelp + ". Unknown keys warn (error with --strict-keys)."},
	} {
		hubsCreateCmd.Flags().String(f.name, "", f.desc)
	}

	// On `hubs update`: navigation is a whole-blob REPLACE (MIO-2255); branding /
	// settings / meta are read-modify-write DEEP-MERGES (MIO-2256) so a partial
	// edit does not clobber sibling keys, and --logo-url merges into branding.
	hubsUpdateCmd.Flags().String("navigation-json",
		"", "Hub navigation as a JSON object — header/footer menu items (each needs a \"type\"). Inline JSON or @file. Replaces the hub's navigation. For item-by-item edits use 'mio hubs navigation add/remove/reorder'.")
	hubsUpdateCmd.Flags().String("branding-json",
		"", "Hub branding keys to merge (read-modify-write) as a JSON object. Inline JSON or @file. Accepted keys: "+brandingKeysHelp+". Unknown keys warn (error with --strict-keys).")
	hubsUpdateCmd.Flags().String("settings-json",
		"", "Hub settings keys to merge (read-modify-write) as a JSON object. Inline JSON or @file. Accepted top-level keys: "+settingsKeysHelp+". Unknown keys warn (error with --strict-keys).")
	hubsUpdateCmd.Flags().String("meta-json",
		"", "Hub meta keys to merge (read-modify-write) as a JSON object. Inline JSON or @file. Accepted keys: "+metaKeysHelp+". Unknown keys warn (error with --strict-keys).")

	// MIO-2516: first-class flag for settings.registration.enabled (read-modify-
	// write, preserves sibling settings keys). A bare bool cannot tell false from
	// unset, so it is gated on Changed() — pass --registration-enabled=false to
	// disable explicitly. `hubs retrieve` surfaces the derived registration_enabled.
	hubsUpdateCmd.Flags().Bool("registration-enabled", false, "Enable (--registration-enabled) or disable (--registration-enabled=false) member self-registration (settings.registration.enabled). Read-modify-write: sibling settings/registration keys are preserved.")

	// MIO-2517: delete keys from the branding/settings/meta blobs. The *-json flags
	// are merge-only (a null persists as literal null, {} is a no-op); --unset is
	// the only real delete. Applied AFTER --*-json merges and scalar flags, so an
	// unset in the same command wins.
	hubsUpdateCmd.Flags().StringArray("unset", nil, "Delete a key from a hub blob by dotted path, e.g. --unset settings.registration.enabled (the first segment selects the blob: branding|settings|meta). Repeatable and comma-separated. Read-modify-write; applied AFTER --*-json merges and scalar flags.")

	addPaginationFlags(hubsListCmd)
}

// ---- hubs policies sub-resource ---------------------------------------------

var hubsPoliciesCmd = &cobra.Command{
	Use:   "policies",
	Short: "Manage hub legal policies.",
	Long:  "Create or update legal policies (Terms of Service, Privacy Policy) for a hub.",
}

// hubsPoliciesPath returns /api/teams/{team_id}/hubs/{hub_id}/policies.
func hubsPoliciesPath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/policies", teamID, hubID)
}

// validPolicyTypes is the set of accepted --policy-type values. Validation is
// done client-side so a typo exits with ExitUsage rather than a 422 round-trip.
var validPolicyTypes = map[string]bool{
	"tos":            true,
	"privacy_policy": true,
}

var hubsPoliciesUpdateCmd = &cobra.Command{
	Use:   "update <hub_id>",
	Short: "Update (or create) a hub legal policy.",
	Long: `Create or replace a hub legal policy (Terms of Service or Privacy Policy).

The hub identifier is a positional argument (not the --hub context flag) so you
can target any hub regardless of the active context.

Policy content may be supplied inline or read from a file by prefixing the path
with '@':  --content @policy.md

Exactly one of --content or --reset-content must be provided:
  --content        Supply the policy body (inline string or @file).
  --reset-content  Revert the policy to the backend default (sends content: null).`,
	Example: `  mio hubs policies update hub_abc123 --policy-type tos --content "# Terms of Service\n…"
  mio hubs policies update hub_abc123 --policy-type tos --content @tos.md --require-acceptance
  mio hubs policies update hub_abc123 --policy-type privacy_policy --content @privacy.md
  mio hubs policies update hub_abc123 --policy-type tos --reset-content`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		policyType, ferr := cmd.Flags().GetString("policy-type")
		if ferr != nil {
			return errs.New(errs.ExitUsage, "--policy-type: %s", ferr.Error())
		}
		if !validPolicyTypes[policyType] {
			return errs.New(errs.ExitUsage,
				"--policy-type %q is not valid: must be one of tos, privacy_policy", policyType)
		}

		contentChanged := cmd.Flags().Changed("content")
		resetChanged := cmd.Flags().Changed("reset-content")

		// Exactly one of --content / --reset-content must be provided.
		if contentChanged && resetChanged {
			return errs.New(errs.ExitUsage, "--content and --reset-content are mutually exclusive: provide exactly one")
		}
		if !contentChanged && !resetChanged {
			return errs.New(errs.ExitUsage, "provide exactly one of --content or --reset-content")
		}

		policy := hubPolicy{PolicyType: policyType}

		if !resetChanged {
			rawContent, ferr := cmd.Flags().GetString("content")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--content: %s", ferr.Error())
			}

			// Support the @path convention: a value starting with '@' is treated as a
			// file path whose contents become the policy text.
			content := rawContent
			if after, ok := strings.CutPrefix(rawContent, "@"); ok {
				b, rerr := os.ReadFile(after)
				if rerr != nil {
					return errs.New(errs.ExitGeneric, "read %s: %s", after, rerr.Error())
				}
				content = string(b)
			}
			policy.Content = &content
		}
		// A --reset-content leaves policy.Content nil; applyHubPolicies sends that as
		// content: null to revert to the backend default.

		// --require-acceptance is only included when the flag was explicitly set.
		// Sending it with policy_type=privacy_policy is a backend 422; we let the
		// backend enforce that constraint rather than pre-blocking here.
		if cmd.Flags().Changed("require-acceptance") {
			if ra, rerr := cmd.Flags().GetBool("require-acceptance"); rerr == nil {
				policy.RequireAcceptance = &ra
			}
		}

		res, err := applyHubPolicies(c.ctx, c.client, teamID, args[0], policy)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// hubPolicy carries the resolved inputs for a single hub legal-policy write,
// decoupled from *cobra.Command so both `hubs policies update` and the scaffold
// (MIO-2543) can PATCH the same body. The CALLER owns the flag ergonomics —
// validating --policy-type, the --content/--reset-content mutual exclusion, and
// the @file read — leaving applyHubPolicies to build and send the request.
type hubPolicy struct {
	// PolicyType is the validated policy kind ("tos" | "privacy_policy").
	PolicyType string

	// Content is the policy body. A nil pointer RESETS the policy (sends content:
	// null to revert to the backend default); a non-nil pointer sets the content
	// verbatim (an empty string is a valid, explicit empty body).
	Content *string

	// RequireAcceptance, when non-nil, sets require_acceptance; nil omits it from
	// the write (partial update).
	RequireAcceptance *bool
}

// applyHubPolicies builds the policies PATCH body from p and writes it to
// /api/teams/{team_id}/hubs/{hub_id}/policies. content is ALWAYS present in the
// body: the value when p.Content is set, JSON null (a Go nil) when it is not, so
// a reset is an explicit null rather than an omitted field. It is a pure builder:
// it takes no *cobra.Command.
func applyHubPolicies(ctx context.Context, cl *client.Client, teamID, hubID string, p hubPolicy) (*client.Resource, error) {
	attrs := map[string]any{
		"policy_type": p.PolicyType,
	}
	if p.Content != nil {
		attrs["content"] = *p.Content
	} else {
		// A Go map value of nil marshals to JSON null (revert to backend default).
		attrs["content"] = nil
	}
	if p.RequireAcceptance != nil {
		attrs["require_acceptance"] = *p.RequireAcceptance
	}
	return cl.Update(ctx, hubsPoliciesPath(teamID, hubID), attrs)
}

func init() {
	hubsPoliciesUpdateCmd.Flags().String("policy-type", "", "Policy type: tos or privacy_policy. Required.")
	_ = hubsPoliciesUpdateCmd.MarkFlagRequired("policy-type")

	hubsPoliciesUpdateCmd.Flags().String("content", "", "Policy body in Markdown. Prefix with '@' to read from a file (e.g. @tos.md). Mutually exclusive with --reset-content.")
	hubsPoliciesUpdateCmd.Flags().Bool("reset-content", false, "Revert the policy to the backend default (sends content: null). Mutually exclusive with --content.")

	hubsPoliciesUpdateCmd.Flags().Bool("require-acceptance", false, "Require hub members to accept the policy before accessing content (TOS only).")
}

// ---- hubs policies gate (MIO-2020) ------------------------------------------

// hubsPoliciesGatePath returns /api/teams/{team_id}/hubs/{hub_id}/policies/gate.
func hubsPoliciesGatePath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/policies/gate", teamID, hubID)
}

var hubsPoliciesGateCmd = &cobra.Command{
	Use:   "gate <hub_id>",
	Short: "Toggle the hub policy enforcement gate.",
	Long: `Enable or disable hub-level policy enforcement (settings.policies.enabled).

This only flips the enforcement gate; it does not change policy content, the
TOS version, or member acceptance state. The hub identifier is a positional
argument (not the --hub context flag).

--enabled is required and must be given explicitly:
  --enabled           turn enforcement ON
  --enabled=false     turn enforcement OFF`,
	Example: `  mio hubs policies gate hub_abc123 --enabled
  mio hubs policies gate hub_abc123 --enabled=false`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Require --enabled explicitly BEFORE resolving auth/team so a usage
		// error fires no HTTP request. A bare bool flag defaults to false, so we
		// cannot distinguish "off" from "unset" without the Changed check.
		if !cmd.Flags().Changed("enabled") {
			return errs.New(errs.ExitUsage, "missing required flag: --enabled (use --enabled or --enabled=false)")
		}
		enabled, ferr := cmd.Flags().GetBool("enabled")
		if ferr != nil {
			return errs.New(errs.ExitUsage, "--enabled: %s", ferr.Error())
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		// Enveloped PATCH: the backend HubPolicyGateEnvelope pins data.type to
		// "hub_policy_gate" (derived from the .../policies/gate tail).
		res, err := c.client.Action(c.ctx, "PATCH",
			hubsPoliciesGatePath(teamID, args[0]), map[string]any{"enabled": enabled})
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Set policy gate on hub %s to enabled=%t.\n", args[0], enabled)
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	hubsPoliciesGateCmd.Flags().Bool("enabled", false, "Enable (--enabled) or disable (--enabled=false) policy enforcement. Required.")
}

// ---- hubs redirect-origins sub-resource (MIO-616) ---------------------------

var hubsRedirectOriginsCmd = &cobra.Command{
	Use:   "redirect-origins",
	Short: "Manage the magic-link redirect-origin allowlist.",
	Long:  "Read or full-replace the magic-link redirect-origin allowlist for a hub (owner-only, MIO-616).",
}

// hubsRedirectOriginsPath returns /api/teams/{team_id}/hubs/{hub_id}/redirect-origins.
func hubsRedirectOriginsPath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/redirect-origins", teamID, hubID)
}

var hubsRedirectOriginsGetCmd = &cobra.Command{
	Use:     "get <hub_id>",
	Short:   "Read the redirect-origin allowlist for a hub.",
	Long:    "Return the current magic-link redirect-origin allowlist for a hub (may be empty). Owner-only.",
	Example: `  mio hubs redirect-origins get hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, hubsRedirectOriginsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var hubsRedirectOriginsSetCmd = &cobra.Command{
	Use:   "set <hub_id>",
	Short: "Full-replace the redirect-origin allowlist for a hub.",
	Long: `Full-replace the magic-link redirect-origin allowlist for a hub (owner-only).

This is an atomic replacement: the supplied origins REPLACE the entire stored
list. Every entry is validated at write time (scheme, host, IPv6 brackets, no
wildcards); an invalid entry returns a 422 with a per-index error pointer and
nothing is persisted.

Provide the new list as a comma-separated --origins value, or pass --clear to
empty the allowlist (which rejects all magic-link redirects at runtime).
Exactly one of --origins or --clear is required.`,
	Example: `  mio hubs redirect-origins set hub_abc123 --origins "https://app.example.com,https://portal.example.com"
  mio hubs redirect-origins set hub_abc123 --clear`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate the origins/clear contract BEFORE resolving auth/team so a
		// usage error fires no HTTP request.
		originsChanged := cmd.Flags().Changed("origins")
		clearChanged := cmd.Flags().Changed("clear")
		if originsChanged && clearChanged {
			return errs.New(errs.ExitUsage, "--origins and --clear are mutually exclusive: provide exactly one")
		}
		if !originsChanged && !clearChanged {
			return errs.New(errs.ExitUsage, "provide the allowlist with --origins or --clear")
		}

		// Default to an empty (non-nil) slice so --clear sends origins: [] and
		// the JSON marshals to an array, never null.
		origins := []string{}
		if originsChanged {
			raw, ferr := cmd.Flags().GetString("origins")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--origins: %s", ferr.Error())
			}
			// Split the comma-separated value, trimming whitespace and dropping
			// empty entries so a trailing comma or padded value is harmless.
			for _, part := range strings.Split(raw, ",") {
				if o := strings.TrimSpace(part); o != "" {
					origins = append(origins, o)
				}
			}
			if len(origins) == 0 {
				return errs.New(errs.ExitUsage, "--origins is empty: pass at least one origin, or use --clear to empty the allowlist")
			}
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		// Enveloped PUT: the backend RedirectOriginsUpdateEnvelope pins data.type
		// to "hub_redirect_origin_allowlists" (derived from the redirect-origins
		// tail).
		res, err := c.client.Action(c.ctx, "PUT",
			hubsRedirectOriginsPath(teamID, args[0]), map[string]any{"origins": origins})
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Replaced redirect-origin allowlist for hub %s (%d origin(s)).\n", args[0], len(origins))
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	hubsRedirectOriginsSetCmd.Flags().String("origins", "", "Comma-separated list of allowed redirect origins. Full-replaces the stored list.")
	hubsRedirectOriginsSetCmd.Flags().Bool("clear", false, "Clear the allowlist (send an empty list). Mutually exclusive with --origins.")
}

// ---- hubs email-settings sub-resource (MIO-1229) ----------------------------

var hubsEmailSettingsCmd = &cobra.Command{
	Use:   "email-settings",
	Short: "Manage per-hub email sender identity.",
	Long:  "Get or update the per-hub email sender identity (from_name, reply_to) for a hub (MIO-1229).",
}

// hubsEmailSettingsPath returns /api/teams/{team_id}/hubs/{hub_id}/email-settings.
func hubsEmailSettingsPath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/email-settings", teamID, hubID)
}

var hubsEmailSettingsGetCmd = &cobra.Command{
	Use:   "get <hub_id>",
	Short: "Get the per-hub email sender identity.",
	Long: `Retrieve the email sender identity (from_name, reply_to) for a hub.

Per-hub sender settings override the team-level defaults for all emails
sent from that hub (MIO-1229).`,
	Example: `  mio hubs email-settings get hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, hubsEmailSettingsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var hubsEmailSettingsUpdateCmd = &cobra.Command{
	Use:   "update <hub_id>",
	Short: "Update the per-hub email sender identity.",
	Long: `Update the email sender identity (from_name, reply_to) for a hub.

Only the flags you provide are changed (partial update). Pass an empty
string to explicitly clear a field (e.g. --reply-to="" clears the reply-to
address). Merges into the hub's settings.email; other settings keys are
preserved (MIO-1229).`,
	Example: `  mio hubs email-settings update hub_abc123 --from-name "My Community"
  mio hubs email-settings update hub_abc123 --from-name "Support" --reply-to support@example.com
  mio hubs email-settings update hub_abc123 --reply-to ""`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setNullableMappedString(cmd, attrs, "from-name", "from_name")
		setNullableMappedString(cmd, attrs, "reply-to", "reply_to")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least --from-name or --reply-to")
		}

		res, err := c.client.Update(c.ctx, hubsEmailSettingsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	hubsEmailSettingsUpdateCmd.Flags().String("from-name", "", "Sender display name override for this hub (from_name).")
	hubsEmailSettingsUpdateCmd.Flags().String("reply-to", "", "Reply-to email address override for this hub. Pass an empty string to clear.")
}
