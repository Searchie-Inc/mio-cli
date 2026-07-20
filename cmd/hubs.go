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
	"fmt"
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
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "name", "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setMappedBoolInverted(cmd, attrs, "published", "is_private")
		// discussions_default_title/description are typed columns (MIO-2274) — a
		// plain partial update; an empty string clears to null.
		setNullableMappedString(cmd, attrs, "discussions-default-title", "discussions_default_title")
		setNullableMappedString(cmd, attrs, "discussions-default-description", "discussions_default_description")

		// Presentation-blob flags: opaque JSONB objects passed through verbatim so
		// an operator or agent can author a hub's branding, navigation, settings and
		// feature-guard meta in the same POST. (MIO-2254)
		for _, jf := range []struct{ flag, key string }{
			{"branding-json", "branding"},
			{"navigation-json", "navigation"},
			{"settings-json", "settings"},
			{"meta-json", "meta"},
		} {
			if err := setMappedJSONObjectFlag(cmd, attrs, jf.flag, jf.key); err != nil {
				return err
			}
		}

		// Untyped header/footer items are dropped by the hub renderer, so reject
		// them up front rather than shipping a menu that renders empty. (MIO-2255)
		// Hub-relative url hrefs must stay within this hub's "/{slug}" mount — on
		// create the slug is the --slug flag value. (MIO-2270)
		if nav, ok := attrs["navigation"].(map[string]any); ok {
			if err := validateNavigationBlob(nav); err != nil {
				return err
			}
			slug, _ := attrs["slug"].(string)
			if err := validateNavigationHrefs(nav, slug); err != nil {
				return err
			}
		}

		// --logo-url merges into the branding object rather than replacing it, so it
		// composes with --branding-json (the backend assigns branding wholesale, so
		// the CLI must send one already-merged object).
		if cmd.Flags().Changed("logo-url") {
			logo, err := cmd.Flags().GetString("logo-url")
			if err != nil {
				return errs.New(errs.ExitUsage, "--logo-url: %s", err)
			}
			branding, _ := attrs["branding"].(map[string]any)
			if branding == nil {
				branding = map[string]any{}
			}
			branding["logo_url"] = logo
			attrs["branding"] = branding
		}

		// --favicon-url merges into the branding object (branding.favicon_url is an
		// accepted branding key), exactly like --logo-url, so it composes with
		// --branding-json (the backend assigns branding wholesale). (MIO-2522)
		if cmd.Flags().Changed("favicon-url") {
			favicon, ferr := cmd.Flags().GetString("favicon-url")
			if ferr != nil {
				return errs.New(errs.ExitUsage, "--favicon-url: %s", ferr)
			}
			branding, _ := attrs["branding"].(map[string]any)
			if branding == nil {
				branding = map[string]any{}
			}
			branding["favicon_url"] = favicon
			attrs["branding"] = branding
		}

		// Best-effort key validation for the opaque JSONB blobs: an unknown key is
		// stored verbatim by the API (no server-side schema), so a typo silently
		// has no effect. Warn by default (stderr, so --output json/yaml is never
		// corrupted); --strict-keys turns it into a usage error. Runs BEFORE
		// hubsContext() so a strict rejection fires no HTTP request. (MIO-2515)
		strictKeys, _ := cmd.Flags().GetBool("strict-keys")
		if b, ok := attrs["branding"].(map[string]any); ok {
			if err := validateBlobKeys(cmd, "branding", b, brandingKeys, nil, strictKeys); err != nil {
				return err
			}
		}
		if s, ok := attrs["settings"].(map[string]any); ok {
			if err := validateBlobKeys(cmd, "settings", s, settingsKeys, settingsNestedKeys, strictKeys); err != nil {
				return err
			}
		}
		if m, ok := attrs["meta"].(map[string]any); ok {
			if err := validateBlobKeys(cmd, "meta", m, metaKeys, nil, strictKeys); err != nil {
				return err
			}
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
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
		// flag exits with a usage error and fires no HTTP request.
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "name", "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setMappedBoolInverted(cmd, attrs, "published", "is_private")
		setNullableMappedString(cmd, attrs, "discussions-default-title", "discussions_default_title")
		setNullableMappedString(cmd, attrs, "discussions-default-description", "discussions_default_description")

		// --navigation-json authors the header/footer menu — a whole-blob REPLACE
		// (MIO-2255), validated for the typed shape the mio-hub parser requires.
		// The hub-scoped href check (MIO-2270) needs the hub's slug, which is not
		// known client-side, so it runs after the retrieve below.
		if err := setMappedJSONObjectFlag(cmd, attrs, "navigation-json", "navigation"); err != nil {
			return err
		}
		nav, navSet := attrs["navigation"].(map[string]any)
		// navSlug is the hub's FINAL slug when --slug is set in this same update
		// (setStringFlag only populates attrs["slug"] when the flag changed); it is
		// authoritative over the hub's current slug, so hrefs must scope to it.
		navSlug, navSlugFromFlag := attrs["slug"].(string)
		if navSet {
			if err := validateNavigationBlob(nav); err != nil {
				return err
			}
			// When --slug is changing in this update, validate hrefs against the new
			// slug now — no retrieve needed, so this fires no request (MIO-2270).
			if navSlugFromFlag {
				if err := validateNavigationHrefs(nav, navSlug); err != nil {
					return err
				}
			}
		}

		// --branding-json / --settings-json / --meta-json are read-modify-write:
		// these blobs are assigned WHOLESALE server-side, so a partial edit would
		// clobber sibling keys. Parse now (fail fast on bad JSON); the deep-merge
		// against the hub's current blobs happens after the retrieve below. (MIO-2256)
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
		logoChanged := cmd.Flags().Changed("logo-url")
		var logo string
		if logoChanged {
			logo, err = cmd.Flags().GetString("logo-url")
			if err != nil {
				return errs.New(errs.ExitUsage, "--logo-url: %s", err)
			}
		}
		// --favicon-url merges into branding (branding.favicon_url), mirroring
		// --logo-url. (MIO-2522)
		faviconChanged := cmd.Flags().Changed("favicon-url")
		var favicon string
		if faviconChanged {
			favicon, err = cmd.Flags().GetString("favicon-url")
			if err != nil {
				return errs.New(errs.ExitUsage, "--favicon-url: %s", err)
			}
		}
		// --registration-enabled sets settings.registration.enabled. A bare bool
		// cannot distinguish false-from-unset, so gate on Changed() (same pattern as
		// the policies gate --enabled flag) and read the value only when set; the
		// nested write below preserves sibling settings.* AND registration.* keys.
		// (MIO-2516)
		registrationChanged := cmd.Flags().Changed("registration-enabled")
		var regEnabled bool
		if registrationChanged {
			regEnabled, err = cmd.Flags().GetBool("registration-enabled")
			if err != nil {
				return errs.New(errs.ExitUsage, "--registration-enabled: %s", err)
			}
		}
		// --unset deletes keys from the branding/settings/meta blobs by dotted path
		// (read-modify-write). Parsed/validated pre-auth so a bad path exits
		// ExitUsage and fires no HTTP request. (MIO-2517)
		unsetPaths, err := parseUnsetFlag(cmd)
		if err != nil {
			return err
		}
		rmw := branding != nil || settings != nil || meta != nil || logoChanged ||
			faviconChanged || registrationChanged || len(unsetPaths) > 0

		// Best-effort key validation on the INCOMING blob keys only (never the
		// hub's retrieved/merged blob — older hubs legitimately carry unlisted
		// keys the user did not touch). Warn by default (stderr); --strict-keys
		// makes an unknown key a usage error. Runs BEFORE hubsContext()/retrieve
		// so a strict rejection fires no HTTP request. (MIO-2515)
		strictKeys, _ := cmd.Flags().GetBool("strict-keys")
		if err := validateBlobKeys(cmd, "branding", branding, brandingKeys, nil, strictKeys); err != nil {
			return err
		}
		if err := validateBlobKeys(cmd, "settings", settings, settingsKeys, settingsNestedKeys, strictKeys); err != nil {
			return err
		}
		if err := validateBlobKeys(cmd, "meta", meta, metaKeys, nil, strictKeys); err != nil {
			return err
		}

		if len(attrs) == 0 && !rmw {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		// Retrieve the hub when we need its current whole-blob fields (RMW) or its
		// slug (to validate hub-scoped navigation hrefs when --slug is NOT also
		// being changed, MIO-2270) — one GET serves both. Read-modify-write fetches
		// the current branding/settings/meta, deep-merges the provided keys on top,
		// and PATCHes the merged object so untouched siblings survive; --logo-url
		// merges into branding here — this is what unblocks it on update (MIO-901).
		navNeedsRetrieve := navSet && !navSlugFromFlag
		if rmw || navNeedsRetrieve {
			cur, rerr := c.client.Retrieve(c.ctx, hubsPath(teamID, args[0]))
			if rerr != nil {
				return rerr
			}
			// Validate hub-relative navigation hrefs against the hub's current slug
			// before issuing the PATCH, so a bad link fails with ExitUsage and no
			// write happens (MIO-2270). Skipped above when --slug is changing, which
			// is validated pre-auth against the new slug.
			if navNeedsRetrieve {
				slug, _ := cur.Attributes["slug"].(string)
				if err := validateNavigationHrefs(nav, slug); err != nil {
					return err
				}
			}
			// Deterministic apply ORDER per blob (documented): (1) --*-json deep-merge,
			// (2) scalar convenience overrides (--logo-url/--favicon-url/
			// --registration-enabled), (3) --unset removals LAST — so an explicit unset
			// in the same command wins over a merge.
			if branding != nil || logoChanged || faviconChanged {
				b := attrMap(cur.Attributes["branding"])
				if branding != nil {
					b = deepMergeMap(b, branding)
				}
				if logoChanged {
					b["logo_url"] = logo
				}
				if faviconChanged {
					b["favicon_url"] = favicon
				}
				attrs["branding"] = b
			}
			if settings != nil || registrationChanged {
				s := attrMap(cur.Attributes["settings"])
				if settings != nil {
					s = deepMergeMap(s, settings)
				}
				if registrationChanged {
					// Preserve sibling registration.* keys: copy the current sub-object and
					// set only enabled.
					reg := attrMap(s["registration"])
					reg["enabled"] = regEnabled
					s["registration"] = reg
				}
				attrs["settings"] = s
			}
			if meta != nil {
				attrs["meta"] = deepMergeMap(attrMap(cur.Attributes["meta"]), meta)
			}
			// --unset removals apply LAST on each staged blob. When a blob is touched
			// ONLY by unset, seed it from the hub's current blob; deleteAtPath copies
			// each node it descends so the retrieved resource is never mutated.
			for _, u := range unsetPaths {
				blob, ok := attrs[u.blob].(map[string]any)
				if !ok {
					blob = attrMap(cur.Attributes[u.blob])
				}
				attrs[u.blob] = deleteAtPath(blob, u.segments)
			}
		}

		res, err := c.client.Update(c.ctx, hubsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		injectHubDerivedState(res)
		return c.render(cmd, res)
	},
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
		"", "Hub navigation as a JSON object — header/footer menu items (each needs a \"type\"). Inline JSON or @file. Replaces the hub's navigation.")
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

		attrs := map[string]any{
			"policy_type": policyType,
		}

		if resetChanged {
			// Explicitly send content: null to revert to the backend default.
			// A Go map value of nil marshals to JSON null.
			attrs["content"] = nil
		} else {
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
			attrs["content"] = content
		}

		// --require-acceptance is only included when the flag was explicitly set.
		// Sending it with policy_type=privacy_policy is a backend 422; we let the
		// backend enforce that constraint rather than pre-blocking here.
		setBoolFlag(cmd, attrs, "require-acceptance")

		hubID := args[0]
		res, err := c.client.Update(c.ctx, hubsPoliciesPath(teamID, hubID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
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
