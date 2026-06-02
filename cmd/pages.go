package cmd

// pages.go implements the `mio pages` command group and its `sections`
// sub-resource group. Both groups are hub-scoped: every command requires a
// resolved hub id (--hub flag or config current_hub) in addition to a team id.
//
// Routes (see docs/internal/api-surface.md "pages"):
//
//	pages:    CRUD + home  /api/teams/{team_id}/hubs/{hub_id}/pages[/{id}]
//	          home         /api/teams/{team_id}/hubs/{hub_id}/pages/home
//	sections: create/list  /api/teams/{team_id}/hubs/{hub_id}/pages/{pid}/sections
//	          update/delete/reorder  …/sections[/{sid}]

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

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
  mio pages create --hub hub_123 --title "About" --published`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "layout")
		setBoolFlag(cmd, attrs, "published")
		setBoolFlag(cmd, attrs, "is-home")

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
	Use:     "retrieve <id>",
	Short:   "Retrieve a page by id.",
	Long:    "Retrieve a single page from the active hub by its id.",
	Example: `  mio pages retrieve page_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, pagesPath(teamID, hubID, args[0]))
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
  mio pages update page_abc123 --hub hub_123 --published=false`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "layout")
		setBoolFlag(cmd, attrs, "published")
		setBoolFlag(cmd, attrs, "is-home")

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
		cmd.Flags().String("description", "", "Page description or meta description.")
		cmd.Flags().String("layout", "", "Page layout template name.")
		cmd.Flags().Bool("published", false, "Whether the page is published and publicly accessible.")
		cmd.Flags().Bool("is-home", false, "Whether this page is the hub home page.")
	}
	addPaginationFlags(pagesListCmd)
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
	Use:     "create <page_id>",
	Short:   "Create a section on a page.",
	Long:    "Add a new section to the specified page.",
	Example: `  mio pages sections create page_abc123 --hub hub_123 --type hero --position 0`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "type")
		setStringFlag(cmd, attrs, "name")
		setIntFlag(cmd, attrs, "position")
		setStringFlag(cmd, attrs, "content")
		setBoolFlag(cmd, attrs, "visible")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --type")
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
	Use:     "update <page_id> <section_id>",
	Short:   "Update a section by id.",
	Long:    "Update one or more attributes of an existing page section.",
	Example: `  mio pages sections update page_abc123 sec_xyz --hub hub_123 --name "Hero Banner"`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "type")
		setStringFlag(cmd, attrs, "name")
		setIntFlag(cmd, attrs, "position")
		setStringFlag(cmd, attrs, "content")
		setBoolFlag(cmd, attrs, "visible")

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

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "order")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to reorder: set --order with a comma-separated list of section ids")
		}

		res, err := c.client.Update(c.ctx, sectionsBase(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	// Attribute flags for sections create/update.
	for _, cmd := range []*cobra.Command{pagesSectionsCreateCmd, pagesSectionsUpdateCmd} {
		cmd.Flags().String("type", "", "Section type (e.g. hero, text, media, cta).")
		cmd.Flags().String("name", "", "Human-readable section name.")
		cmd.Flags().Int("position", 0, "Zero-based display position of the section within the page.")
		cmd.Flags().String("content", "", "Section content payload (JSON or plain text, depending on type).")
		cmd.Flags().Bool("visible", true, "Whether the section is visible to hub members.")
	}
	addPaginationFlags(pagesSectionsListCmd)
	pagesSectionsReorderCmd.Flags().String("order", "", "Comma-separated list of section ids in the desired display order.")
}
