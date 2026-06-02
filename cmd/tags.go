package cmd

// tags.go implements the `mio tags` command group.
//
// Routes (see docs/internal/api-surface.md "tags"):
//
//	tags CRUD  /api/teams/{team_id}/tags[/{id}]
//	assign     POST /api/teams/{team_id}/contacts/{tcid}/tags
//	assign-bulk POST /api/teams/{team_id}/contacts/{tcid}/tags/bulk
//	remove     DELETE /api/teams/{team_id}/contacts/{tcid}/tags/{tag_id}

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// tags <action>
	tagsCmd.AddCommand(
		tagsCreateCmd,
		tagsListCmd,
		tagsRetrieveCmd,
		tagsUpdateCmd,
		tagsDeleteCmd,
		tagsAssignCmd,
		tagsAssignBulkCmd,
		tagsRemoveCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(tagsCmd)
}

// ---- tags group -------------------------------------------------------------

var tagsCmd = &cobra.Command{
	Use:   "tags",
	Short: "Manage tags.",
	Long:  "Create, list, retrieve, update, delete, and assign tags for the active team.",
}

// tagsPath returns /api/teams/{team_id}/tags[/{id}].
func tagsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/tags", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// contactTagsPath returns /api/teams/{team_id}/contacts/{tcid}/tags[/{tag_id}].
func contactTagsPath(teamID, contactID, tagID string) string {
	base := fmt.Sprintf("/api/teams/%s/contacts/%s/tags", teamID, contactID)
	if tagID != "" {
		return base + "/" + tagID
	}
	return base
}

var tagsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a tag.",
	Long:  "Create a new tag for the active team.",
	Example: `  # Create a tag named "VIP"
  mio tags create --name VIP

  # Create a tag with a color
  mio tags create --name VIP --color "#FF0000"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "color")
		setStringFlag(cmd, attrs, "description")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, tagsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var tagsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tags.",
	Long:  "List all tags for the active team.",
	Example: `  mio tags list
  mio tags list --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, tagsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var tagsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a tag by id.",
	Long:    "Retrieve a single tag by its id.",
	Example: `  mio tags retrieve tag_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, tagsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var tagsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a tag by id.",
	Long:  "Update one or more attributes of a tag. Only the flags you supply are changed.",
	Example: `  mio tags update tag_abc123 --name "High Value"
  mio tags update tag_abc123 --color "#00FF00"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "color")
		setStringFlag(cmd, attrs, "description")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, tagsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var tagsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a tag by id.",
	Long:  "Permanently delete a tag. Pass --yes to skip the confirmation prompt in non-interactive environments.",
	Example: `  mio tags delete tag_abc123
  mio tags delete tag_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete tag %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, tagsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted tag %s.\n", args[0])
		return nil
	},
}

var tagsAssignCmd = &cobra.Command{
	Use:     "assign <contact_id>",
	Short:   "Assign a tag to a contact.",
	Long:    "Assign a single tag to a contact by contact id. Requires --tag-id.",
	Example: `  mio tags assign contact_xyz789 --tag-id tag_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "tag-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to assign: set --tag-id")
		}

		res, err := c.client.Create(c.ctx, contactTagsPath(teamID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var tagsAssignBulkCmd = &cobra.Command{
	Use:     "assign-bulk <contact_id>",
	Short:   "Bulk-assign tags to a contact.",
	Long:    "Assign multiple tags to a contact in a single request. Requires --tag-ids.",
	Example: `  mio tags assign-bulk contact_xyz789 --tag-ids "tag_abc123,tag_def456"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "tag-ids")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to assign: set --tag-ids")
		}

		res, err := c.client.Create(c.ctx, contactTagsPath(teamID, args[0], "")+"/bulk", attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var tagsRemoveCmd = &cobra.Command{
	Use:   "remove <contact_id> <tag_id>",
	Short: "Remove a tag from a contact.",
	Long:  "Remove a tag assignment from a contact. Pass --yes to skip the confirmation prompt in non-interactive environments.",
	Example: `  mio tags remove contact_xyz789 tag_abc123
  mio tags remove contact_xyz789 tag_abc123 --yes`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}
		teamID, err := c.requireTeam()
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Remove tag %s from contact %s?", args[1], args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, contactTagsPath(teamID, args[0], args[1])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed tag %s from contact %s.\n", args[1], args[0])
		return nil
	},
}

func init() {
	// Attribute flags for create/update.
	for _, cmd := range []*cobra.Command{tagsCreateCmd, tagsUpdateCmd} {
		cmd.Flags().String("name", "", "Tag name.")
		cmd.Flags().String("color", "", "Tag color as a hex code, e.g. #FF0000.")
		cmd.Flags().String("description", "", "Tag description.")
	}
	addPaginationFlags(tagsListCmd)

	// assign flags.
	tagsAssignCmd.Flags().String("tag-id", "", "ID of the tag to assign to the contact.")

	// assign-bulk flags.
	tagsAssignBulkCmd.Flags().String("tag-ids", "", "Comma-separated list of tag IDs to assign to the contact.")
}
