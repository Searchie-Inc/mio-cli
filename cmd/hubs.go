package cmd

// hubs.go — `mio hubs` command group.
//
// Routes (see docs/internal/api-surface.md "hubs"):
//
//	create   POST  /api/teams/{team_id}/hubs
//	list     GET   /api/teams/{team_id}/hubs
//	retrieve GET   /api/teams/{team_id}/hubs/{id}
//	update   PATCH /api/teams/{team_id}/hubs/{id}
//	delete   DELETE /api/teams/{team_id}/hubs/{id}
//
// All routes are team-scoped. Hub id comes from a positional argument (not the
// --hub context flag) so operators can manage any hub, not just the active one.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	hubsCmd.AddCommand(
		hubsCreateCmd,
		hubsListCmd,
		hubsRetrieveCmd,
		hubsUpdateCmd,
		hubsDeleteCmd,
	)

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

// ---- create -----------------------------------------------------------------

var hubsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a hub.",
	Long:  "Create a new hub for the active team.",
	Example: `  mio hubs create --name "My Community" --slug my-community
  mio hubs create --name "Support Hub" --slug support --description "Help articles"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "logo-url")
		setBoolFlag(cmd, attrs, "published")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, hubsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
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
		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "logo-url")
		setBoolFlag(cmd, attrs, "published")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, hubsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
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
		cmd.Flags().String("logo-url", "", "URL of the hub's logo image.")
		cmd.Flags().Bool("published", false, "Whether the hub is publicly published.")
	}

	addPaginationFlags(hubsListCmd)
}
