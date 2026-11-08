package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
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

	rootCmd.AddCommand(tagsCmd)
}

var tagsCmd = &cobra.Command{
	Use:   "tags",
	Short: "Manage tags.",
	Long:  "Create, list, retrieve, update, delete, and assign tags for the active team.",
}

func tagsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/tags", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

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
	Example: `  # Create a tag named "VIP" with slug "vip"
  mio tags create --name VIP --slug vip

  # Create a tag with a color
  mio tags create --name VIP --slug vip --color "#FF0000"`,
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
		setStringFlag(cmd, attrs, "slug")
		setStringFlag(cmd, attrs, "color")
		setStringFlag(cmd, attrs, "description")

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
		setStringFlag(cmd, attrs, "slug")
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
	Use:   "assign [contact_id]",
	Short: "Assign a tag to a contact.",
	Long: `Assign a single tag to a contact.

Identify the CONTACT by a raw id positional argument OR by --email <addr>
(resolved to the contact id via the server-side email filter). Identify the TAG
by --tag-id <id> OR --tag <name-or-id> (a name/slug is resolved to its id).`,
	Example: `  mio tags assign ctt_xyz789 --tag-id tag_abc123
  mio tags assign --email alice@example.com --tag VIP`,
	Args: cobra.MaximumNArgs(1),
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

		contactID, err := resolveContactArg(c, cmd, teamID, args, 0)
		if err != nil {
			return err
		}
		tagID, err := resolveTagFlag(c, cmd, teamID)
		if err != nil {
			return err
		}
		if tagID == "" {
			return errs.New(errs.ExitUsage, "nothing to assign: set --tag <name-or-id> or --tag-id <id>")
		}

		attrs := map[string]any{"tag_id": tagID}
		col, err := c.client.ActionCollection(c.ctx, http.MethodPost, contactTagsPath(teamID, contactID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
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

		if !cmd.Flags().Changed("tag-ids") {
			return errs.New(errs.ExitUsage, "nothing to assign: set --tag-ids")
		}
		rawIDs, _ := cmd.Flags().GetString("tag-ids")
		ids := make([]string, 0)
		for _, part := range strings.Split(rawIDs, ",") {
			if id := strings.TrimSpace(part); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return errs.New(errs.ExitUsage, "nothing to assign: --tag-ids must contain at least one tag id")
		}

		attrs := map[string]any{"tag_ids": ids}
		col, err := c.client.ActionCollection(c.ctx, http.MethodPost, contactTagsPath(teamID, args[0], "")+"/bulk", attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var tagsRemoveCmd = &cobra.Command{
	Use:   "remove [contact_id] [tag_id]",
	Short: "Remove a tag from a contact.",
	Long: `Remove a tag assignment from a contact.

Identify the CONTACT by a raw id positional argument OR by --email <addr>.
Identify the TAG by a raw id positional argument OR by --tag <name-or-id>.
The two-positional form 'remove <contact_id> <tag_id>' keeps working. Pass --yes
to skip the confirmation prompt in non-interactive environments.`,
	Example: `  mio tags remove ctt_xyz789 tag_abc123 --yes
  mio tags remove --email alice@example.com --tag VIP --yes`,
	Args: cobra.MaximumNArgs(2),
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

		contactID, err := resolveContactArg(c, cmd, teamID, args, 0)
		if err != nil {
			return err
		}
		tagID, err := resolveTagArgOrFlag(c, cmd, teamID, args, 1)
		if err != nil {
			return err
		}
		if tagID == "" {
			return errs.New(errs.ExitUsage, "nothing to remove: provide a tag id positional or --tag <name-or-id>")
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Remove tag %s from contact %s?", tagID, contactID)); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, contactTagsPath(teamID, contactID, tagID)); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed tag %s from contact %s.\n", tagID, contactID)
		return nil
	},
}

func resolveContactArg(c *cmdContext, cmd *cobra.Command, teamID string, args []string, argIdx int) (string, error) {
	hasPos := len(args) > argIdx && args[argIdx] != ""
	email := flagValue(cmd, "email")
	switch {
	case hasPos && email != "":
		return "", errs.New(errs.ExitUsage, "specify the contact by positional id OR --email, not both")
	case hasPos:
		return args[argIdx], nil
	case email != "":
		id, err := c.client.ResolveContactByEmail(c.ctx, teamID, email)
		if err != nil {
			return "", errs.Wrap(errs.ExitUsage, err)
		}
		return id, nil
	default:
		return "", errs.New(errs.ExitUsage, "no contact specified: pass a contact id or --email <addr>")
	}
}

func resolveTagFlag(c *cmdContext, cmd *cobra.Command, teamID string) (string, error) {
	tagName := flagValue(cmd, "tag")
	tagID := flagValue(cmd, "tag-id")
	if tagName != "" && tagID != "" {
		return "", errs.New(errs.ExitUsage, "specify the tag by --tag OR --tag-id, not both")
	}
	if tagName != "" {
		id, err := c.client.ResolveTag(c.ctx, teamID, tagName)
		if err != nil {
			return "", errs.Wrap(errs.ExitUsage, err)
		}
		return id, nil
	}
	return tagID, nil
}

func resolveTagArgOrFlag(c *cmdContext, cmd *cobra.Command, teamID string, args []string, argIdx int) (string, error) {
	if len(args) > argIdx && args[argIdx] != "" {
		if flagValue(cmd, "tag") != "" || flagValue(cmd, "tag-id") != "" {
			return "", errs.New(errs.ExitUsage, "specify the tag by positional id OR --tag/--tag-id, not both")
		}
		return args[argIdx], nil
	}
	return resolveTagFlag(c, cmd, teamID)
}

func init() {
	for _, cmd := range []*cobra.Command{tagsCreateCmd, tagsUpdateCmd} {
		cmd.Flags().String("name", "", "Tag name.")
		cmd.Flags().String("slug", "", "Tag slug (lowercase alphanumeric, hyphens, underscores). Required on create.")
		cmd.Flags().String("color", "", "Tag color as a hex code, e.g. #FF0000.")
		cmd.Flags().String("description", "", "Tag description.")
	}
	markFlagsRequired(tagsCreateCmd, "name", "slug")
	addPaginationFlags(tagsListCmd)

	tagsAssignCmd.Flags().String("tag-id", "", "Raw id of the tag to assign (alternative to --tag).")
	tagsAssignCmd.Flags().String("tag", "", "Tag name, slug, or id to assign to the contact.")
	tagsAssignCmd.Flags().String("email", "", "Contact email address (alternative to the contact id positional).")

	tagsRemoveCmd.Flags().String("tag", "", "Tag name, slug, or id to remove (alternative to the tag id positional).")
	tagsRemoveCmd.Flags().String("email", "", "Contact email address (alternative to the contact id positional).")

	tagsAssignBulkCmd.Flags().String("tag-ids", "", "Comma-separated list of tag IDs to assign to the contact.")
}
