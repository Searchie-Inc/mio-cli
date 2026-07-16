package cmd

// pages.go implements the `mio pages` command group and its `sections`
// sub-resource group. Both groups are hub-scoped: every command requires a
// resolved hub id (--hub flag or config current_hub) in addition to a team id.
//
// Routes (see docs/internal/api-surface.md "pages"):
//
//	pages:    CRUD + home  /api/teams/{team_id}/hubs/{hub_id}/pages[/{id}]
//	          home         /api/teams/{team_id}/hubs/{hub_id}/pages/home
//	          publish      /api/teams/{team_id}/hubs/{hub_id}/pages/{id}/publish
//	sections: create/list  /api/teams/{team_id}/hubs/{hub_id}/pages/{pid}/sections
//	          update/delete/reorder  …/sections[/{sid}]

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// pages <action>
	pagesCmd.AddCommand(
		pagesCreateCmd,
		pagesListCmd,
		pagesRetrieveCmd,
		pagesUpdateCmd,
		pagesDeleteCmd,
		pagesHomeCmd,
		pagesPublishCmd,
	)

	// pages sections <action>  (nested sub-resource)
	pagesSectionsCmd.AddCommand(
		pagesSectionsCreateCmd,
		pagesSectionsListCmd,
		pagesSectionsUpdateCmd,
		pagesSectionsDeleteCmd,
		pagesSectionsReorderCmd,
	)
	pagesCmd.AddCommand(pagesSectionsCmd)

	// pages tree <action>  (draft node-tree authoring, MIO-2258)
	pagesTreeCmd.AddCommand(pagesTreeGetCmd, pagesTreeSetCmd)
	pagesCmd.AddCommand(pagesTreeCmd)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(pagesCmd)
}

// ---- pages group ------------------------------------------------------------

var pagesCmd = &cobra.Command{
	Use:   "pages",
	Short: "Manage hub pages.",
	Long:  "Create, list, retrieve, update and delete pages within a hub. Hub-scoped: --hub is required.",
}

// pagesBase returns the collection path for pages within a hub.
// /api/teams/{team_id}/hubs/{hub_id}/pages
func pagesBase(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/pages", teamID, hubID)
}

// pagesPath returns /api/teams/{team_id}/hubs/{hub_id}/pages[/{id}].
func pagesPath(teamID, hubID, id string) string {
	base := pagesBase(teamID, hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

var pagesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a page.",
	Long:  "Create a new page in the active hub.",
	Example: `  mio pages create --hub hub_123 --title "Welcome" --slug welcome
  mio pages create --hub hub_123 --title "About" --slug about --privacy public`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		if err := setPageWriteAttrs(cmd, attrs); err != nil {
			return err
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --title")
		}

		res, err := c.client.Create(c.ctx, pagesPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var pagesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pages.",
	Long:  "List all pages in the active hub.",
	Example: `  mio pages list --hub hub_123
  mio pages list --hub hub_123 --limit 20`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, pagesPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var pagesRetrieveCmd = &cobra.Command{
	Use:   "retrieve <id>",
	Short: "Retrieve a page by id.",
	Long:  "Retrieve a single page from the active hub by its id.",
	Example: `  mio pages retrieve page_abc123 --hub hub_123
  mio pages retrieve page_abc123 --hub hub_123 --tree`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		path := pagesPath(teamID, hubID, args[0])

		// --tree requests the raw published node tree (page-trees resource) instead
		// of page metadata. The backend distinguishes the two responses via the
		// resolve=false query parameter on the same GET route.
		if cmd.Flags().Changed("tree") {
			v, ferr := cmd.Flags().GetBool("tree")
			if ferr == nil && v {
				q := url.Values{}
				q.Set("resolve", "false")
				res, err := c.client.RetrieveWithQuery(c.ctx, path, q)
				if err != nil {
					return err
				}
				return c.render(cmd, res)
			}
		}

		res, err := c.client.Retrieve(c.ctx, path)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var pagesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a page by id.",
	Long:  "Update one or more attributes of an existing page.",
	Example: `  mio pages update page_abc123 --hub hub_123 --title "New Title"
  mio pages update page_abc123 --hub hub_123 --is-home`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		if err := setPageWriteAttrs(cmd, attrs); err != nil {
			return err
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, pagesPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var pagesDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a page by id.",
	Long:    "Permanently delete a page from the active hub. Requires confirmation.",
	Example: `  mio pages delete page_abc123 --hub hub_123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete page %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, pagesPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted page %s.\n", args[0])
		return nil
	},
}

var pagesPublishCmd = &cobra.Command{
	Use:   "publish <id>",
	Short: "Publish a page draft.",
	Long: `Publish a page draft, making it live. The --if-match flag is required and must
match the current draft_version returned by a prior retrieve. The backend uses
it to guard against publishing a stale draft (optimistic concurrency control).

Returns a page-publishes resource with attributes: page_id, published_revision,
section_count, and gate_count.`,
	Example: `  mio pages publish page_abc123 --hub hub_123 --if-match 7`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// --if-match is declared required (MarkFlagRequired) so cobra enforces it
		// at parse time for normal CLI use. We also guard here explicitly so that
		// the in-process test harness (which shares a singleton cobra command tree
		// across tests) also gets the ExitUsage signal when the flag is absent.
		if !cmd.Flags().Changed("if-match") {
			return errs.New(errs.ExitUsage, "--if-match is required: supply the draft_version from a prior retrieve")
		}

		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		ifMatch, ferr := cmd.Flags().GetInt("if-match")
		if ferr != nil {
			return errs.New(errs.ExitUsage, "--if-match: %s", ferr.Error())
		}

		// POST …/pages/{id}/publish — no request body; optimistic lock via If-Match.
		// client.StyleEnvelope is the zero value of BodyStyle; it is irrelevant here
		// because the body is nil, but the named constant is clearer than a bare 0.
		path := pagesPath(teamID, hubID, args[0]) + "/publish"
		res, err := c.client.ActionWithHeaders(
			c.ctx,
			client.StyleEnvelope, // irrelevant; body is nil
			"POST",
			path,
			nil, // no body
			map[string]string{"If-Match": strconv.Itoa(ifMatch)},
		)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Published page %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

var pagesHomeCmd = &cobra.Command{
	Use:     "home",
	Short:   "Retrieve the hub home page.",
	Long:    "Retrieve the designated home page for the active hub.",
	Example: `  mio pages home --hub hub_123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, pagesBase(teamID, hubID)+"/home")
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	// Attribute flags for pages create/update.
	for _, cmd := range []*cobra.Command{pagesCreateCmd, pagesUpdateCmd} {
		cmd.Flags().String("title", "", "Page title.")
		cmd.Flags().String("slug", "", "URL slug for the page.")
		cmd.Flags().String("type", "", "Page type (default: generic).")
		cmd.Flags().String("privacy", "", "Page privacy: public, members, or private (default: members).")
		cmd.Flags().Int("position", 0, "Zero-based display position of the page.")
		cmd.Flags().Bool("is-home", false, "Whether this page is the hub home page (sends is_homepage).")
		cmd.Flags().String("settings", "", "Page settings as a JSON object or @file path.")
		cmd.Flags().String("meta", "", "Page metadata as a JSON object or @file path.")
	}
	addPaginationFlags(pagesListCmd)

	// --if-match is required on publish; cobra enforces the requirement at parse time.
	pagesPublishCmd.Flags().Int("if-match", 0, "Draft version number from a prior retrieve (optimistic concurrency lock). Required.")
	if err := pagesPublishCmd.MarkFlagRequired("if-match"); err != nil {
		panic("MarkFlagRequired if-match: " + err.Error())
	}

	// --tree on retrieve: return the raw published node tree (page-trees) instead of page metadata.
	pagesRetrieveCmd.Flags().Bool("tree", false, "Return the raw published node tree (page-trees) instead of page metadata.")

	// pages tree set flags (MIO-2258).
	pagesTreeSetCmd.Flags().String("file", "", "Path to the tree JSON file (optionally @-prefixed). Required.")
	pagesTreeSetCmd.Flags().Int("if-match", 0, "draft_version from a prior 'pages tree get' (optimistic concurrency lock). Required.")
	if err := pagesTreeSetCmd.MarkFlagRequired("if-match"); err != nil {
		panic("MarkFlagRequired if-match: " + err.Error())
	}
}

// pagesContext is the shared boilerplate for page commands: build the context,
// require auth, and resolve both the team id and the hub id.
func pagesContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", "", err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, "", "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", "", err
	}
	return c, teamID, hubID, nil
}

// ---- pages sections sub-resource -------------------------------------------

var pagesSectionsCmd = &cobra.Command{
	Use:   "sections",
	Short: "Manage a page's sections.",
	Long:  "Create, list, update, delete and reorder sections nested under a page.",
}

// sectionsBase returns the collection path for sections within a page.
// /api/teams/{team_id}/hubs/{hub_id}/pages/{pid}/sections
func sectionsBase(teamID, hubID, pid string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/pages/%s/sections", teamID, hubID, pid)
}

// sectionsPath returns …/sections[/{sid}].
func sectionsPath(teamID, hubID, pid, sid string) string {
	base := sectionsBase(teamID, hubID, pid)
	if sid != "" {
		return base + "/" + sid
	}
	return base
}

var pagesSectionsCreateCmd = &cobra.Command{
	Use:   "create <page_id>",
	Short: "Create a section on a page.",
	Long: `Add a new section to the specified page.

--type is required and is validated against the page-builder catalog's writable
section types — run 'mio pages catalog section-types --writable-only' to list
them. (For authoring a full page structure, prefer the tree door:
'mio pages catalog scaffold' + 'mio pages tree set'.)

--settings accepts a JSON object (or @file path) that controls the section's
display configuration (layout, colours, limits, etc.). The exact keys are
section-type-specific.`,
	Example: `  mio pages sections create page_abc123 --hub hub_123 --type text --position 0
  mio pages sections create page_abc123 --hub hub_123 --type grid --title "Featured Content" --settings '{"limit":6}'
  mio pages sections create page_abc123 --hub hub_123 --type grid --settings @section-settings.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate --type against the catalog-derived writable allow-list FIRST,
		// so a typo or non-writable type fails fast (ExitUsage) before any HTTP
		// or scope resolution — mirrors the publish command's --if-match pre-check.
		if !cmd.Flags().Changed("type") {
			return errs.New(errs.ExitUsage, "missing required flag: --type")
		}
		if err := validateSectionType(getString(cmd, "type")); err != nil {
			return err
		}

		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "type")
		setStringFlag(cmd, attrs, "title")
		setIntFlag(cmd, attrs, "position")
		setBoolFlag(cmd, attrs, "visible")

		if cmd.Flags().Changed("settings") {
			raw, _ := cmd.Flags().GetString("settings")
			parsed, perr := parseJSONFlag(raw)
			if perr != nil {
				return errs.Wrap(errs.ExitUsage, fmt.Errorf("--settings is not valid JSON: %w", perr))
			}
			attrs["settings"] = parsed
		}
		if cmd.Flags().Changed("meta") {
			raw, _ := cmd.Flags().GetString("meta")
			parsed, perr := parseJSONFlag(raw)
			if perr != nil {
				return errs.Wrap(errs.ExitUsage, fmt.Errorf("--meta is not valid JSON: %w", perr))
			}
			attrs["meta"] = parsed
		}

		res, err := c.client.Create(c.ctx, sectionsPath(teamID, hubID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var pagesSectionsListCmd = &cobra.Command{
	Use:     "list <page_id>",
	Short:   "List sections on a page.",
	Long:    "List all sections belonging to the specified page.",
	Example: `  mio pages sections list page_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, sectionsPath(teamID, hubID, args[0], ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var pagesSectionsUpdateCmd = &cobra.Command{
	Use:   "update <page_id> <section_id>",
	Short: "Update a section by id.",
	Long: `Update one or more attributes of an existing page section (PATCH semantics).

Note: --type is immutable after creation and cannot be changed via update.
Mutable fields: --title, --position, --visible, --settings, --meta.`,
	Example: `  mio pages sections update page_abc123 sec_xyz --hub hub_123 --title "Hero Banner"
  mio pages sections update page_abc123 sec_xyz --hub hub_123 --settings '{"limit":12}'`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setIntFlag(cmd, attrs, "position")
		setBoolFlag(cmd, attrs, "visible")

		if cmd.Flags().Changed("settings") {
			raw, _ := cmd.Flags().GetString("settings")
			parsed, perr := parseJSONFlag(raw)
			if perr != nil {
				return errs.Wrap(errs.ExitUsage, fmt.Errorf("--settings is not valid JSON: %w", perr))
			}
			attrs["settings"] = parsed
		}
		if cmd.Flags().Changed("meta") {
			raw, _ := cmd.Flags().GetString("meta")
			parsed, perr := parseJSONFlag(raw)
			if perr != nil {
				return errs.Wrap(errs.ExitUsage, fmt.Errorf("--meta is not valid JSON: %w", perr))
			}
			attrs["meta"] = parsed
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, sectionsPath(teamID, hubID, args[0], args[1]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var pagesSectionsDeleteCmd = &cobra.Command{
	Use:     "delete <page_id> <section_id>",
	Short:   "Delete a section by id.",
	Long:    "Permanently remove a section from a page. Requires confirmation.",
	Example: `  mio pages sections delete page_abc123 sec_xyz --hub hub_123 --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete section %s?", args[1])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, sectionsPath(teamID, hubID, args[0], args[1])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted section %s.\n", args[1])
		return nil
	},
}

var pagesSectionsReorderCmd = &cobra.Command{
	Use:     "reorder <page_id>",
	Short:   "Reorder sections on a page.",
	Long:    "Set the display order of sections on a page by providing the new ordered list of section ids.",
	Example: `  mio pages sections reorder page_abc123 --hub hub_123 --order sec_1,sec_2,sec_3`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		order, err := cmd.Flags().GetString("order")
		if err != nil {
			return errs.New(errs.ExitUsage, "--order: %s", err)
		}
		ids := splitCSV(order)
		if len(ids) == 0 {
			return errs.New(errs.ExitUsage, "nothing to reorder: set --order with a comma-separated list of section ids")
		}
		data := make([]map[string]any, len(ids))
		for i, id := range ids {
			data[i] = map[string]any{"id": id, "position": i}
		}

		// The backend reorder endpoint (PATCH .../sections) takes a bare
		// SectionReorderEnvelope { data: [{id, position}] }, NOT the standard
		// {data:{type,attributes}} envelope — send it raw. (MIO-2257)
		col, err := c.client.ActionCollectionRaw(c.ctx, http.MethodPatch,
			sectionsBase(teamID, hubID, args[0]), map[string]any{"data": data})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

func init() {
	// Create-only flag: --type is required on create but immutable on update.
	// The allowed values are derived from the catalog (no hardcoded list) so the
	// help never drifts from the page-builder vocabulary (MIO-2340).
	pagesSectionsCreateCmd.Flags().String("type", "",
		"Section type — one of the catalog writable types ("+writableSectionTypesHelp+"). Required.")

	// Shared mutable flags for create and update.
	for _, c := range []*cobra.Command{pagesSectionsCreateCmd, pagesSectionsUpdateCmd} {
		c.Flags().String("title", "", "Human-readable section title.")
		c.Flags().Int("position", 0, "Zero-based display position of the section within the page.")
		c.Flags().Bool("visible", true, "Whether the section is visible to hub members.")
		c.Flags().String("settings", "", "Section display settings as a JSON object or @file path (e.g. '{\"limit\":6}').")
		c.Flags().String("meta", "", "Arbitrary section metadata as a JSON object or @file path.")
	}

	addPaginationFlags(pagesSectionsListCmd)
	pagesSectionsReorderCmd.Flags().String("order", "", "Comma-separated list of section ids in the desired display order.")
}

// writableSectionTypesHelp is the catalog-derived, comma-joined list of writable
// section types, computed once from the embedded vendored catalog for help text
// — so the imperative-door help never drifts from the page-builder vocabulary
// (MIO-2340, replacing the former hardcoded 9-type strings). Empty only if the
// embedded catalog somehow fails to load (it is tested to always load).
var writableSectionTypesHelp = func() string {
	cat, err := catalog.Load()
	if err != nil {
		return ""
	}
	return strings.Join(cat.WritableSectionTypes(), ", ")
}()

// validateSectionType rejects a --type that is not a catalog writable section
// type (the imperative-door allow-list). It validates against the EMBEDDED
// vendored catalog — a fast, offline, network-free check on the write path; the
// backend remains the authority on the actual write (charter §6.3/§6.4/D10), so
// a slightly stale client-side list only ever fails a typo early, never a valid
// type wrongly (a genuinely new writable type still round-trips to the backend).
func validateSectionType(typ string) error {
	typ = strings.TrimSpace(typ)
	cat, err := catalog.Load()
	if err != nil {
		// The vendored catalog is embedded + tested; a load failure here is
		// unexpected. Don't block the write on a client-side defect — defer to
		// the backend's own validation.
		return nil
	}
	if !cat.IsWritableSectionType(typ) {
		return errs.New(errs.ExitUsage,
			"invalid --type %q: must be one of %s", typ, strings.Join(cat.WritableSectionTypes(), ", "))
	}
	return nil
}
