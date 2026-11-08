package cmd

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	communitySpacesCmd.AddCommand(
		communitySpacesListCmd,
		communitySpacesCreateCmd,
		communitySpacesRetrieveCmd,
		communitySpacesUpdateCmd,
		communitySpacesDeleteCmd,
		communitySpacesReorderCmd,
	)
	communityCmd.AddCommand(communitySpacesCmd)

	communityDiscussionsCmd.AddCommand(
		communityDiscussionsListCmd,
		communityDiscussionsCreateCmd,
		communityDiscussionsRetrieveCmd,
		communityDiscussionsUpdateCmd,
		communityDiscussionsDeleteCmd,
	)
	communityCmd.AddCommand(communityDiscussionsCmd)

	communityMembersCmd.AddCommand(
		communityMembersBanCmd,
		communityMembersUnbanCmd,
		communityMembersWarnCmd,
	)
	communityCmd.AddCommand(communityMembersCmd)

	rootCmd.AddCommand(communityCmd)
}

var communityCmd = &cobra.Command{
	Use:   "community",
	Short: "Manage community content and members.",
	Long: `Manage community spaces, discussions, and member moderation within a hub.

All community admin operations are hub-scoped: both --team and --hub are required
(or resolvable from context).`,
	Example: `  mio community spaces list --hub hub_abc123
  mio community discussions list --hub hub_abc123
  mio community members ban contact_xyz --hub hub_abc123 --yes`,
}

func communityContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
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

func communityAdminBase(teamID, hubID string) string {
	return fmt.Sprintf("/api/admin/teams/%s/hubs/%s", teamID, hubID)
}

var communitySpacesCmd = &cobra.Command{
	Use:   "spaces",
	Short: "Manage community spaces.",
	Long:  "List, create, retrieve, update and delete spaces within a hub.",
}

func spacesPath(teamID, hubID, id string) string {
	base := communityAdminBase(teamID, hubID) + "/spaces"
	if id != "" {
		return base + "/" + id
	}
	return base
}

var communitySpacesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List spaces in a hub.",
	Long:  "List all spaces in the active hub. Requires team-admin privileges.",
	Example: `  mio community spaces list --hub hub_abc123
  mio community spaces list --hub hub_abc123 --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, spacesPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var communitySpacesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a space in a hub.",
	Long:  "Create a new community space. Requires team-admin privileges.",
	Example: `  mio community spaces create --hub hub_abc123 --name "General" --slug general
  mio community spaces create --hub hub_abc123 --name "Announcements" --slug announcements --description "Official announcements"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		attrs := map[string]any{}
		if err := setSpaceWriteAttrs(cmd, attrs); err != nil {
			return err
		}
		if _, ok := attrs["name"]; !ok {
			return errs.New(errs.ExitUsage, "--name is required to create a space")
		}
		if _, ok := attrs["slug"]; !ok {
			return errs.New(errs.ExitUsage, "--slug is required to create a space")
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, spacesPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communitySpacesRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a space by id.",
	Long:    "Retrieve a single community space by its id.",
	Example: `  mio community spaces retrieve space_abc123 --hub hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, spacesPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communitySpacesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a space by id.",
	Long:  "Update one or more fields on a space. Only the flags you provide are changed (partial update).",
	Example: `  mio community spaces update space_abc123 --hub hub_abc123 --name "New Name"
  mio community spaces update space_abc123 --hub hub_abc123 --access-level restricted --segment-id seg_vip`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		attrs := map[string]any{}
		if err := setSpaceWriteAttrs(cmd, attrs); err != nil {
			return err
		}
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Update(c.ctx, spacesPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var communitySpacesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a space by id.",
	Long:  "Permanently delete a community space. Pass --yes to skip the confirmation prompt.",
	Example: `  mio community spaces delete space_abc123 --hub hub_abc123
  mio community spaces delete space_abc123 --hub hub_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete space %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, spacesPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted space %s.\n", args[0])
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{communitySpacesCreateCmd, communitySpacesUpdateCmd} {
		cmd.Flags().String("name", "", "Space display name.")
		cmd.Flags().String("slug", "", "Space URL slug (unique within the hub).")
		cmd.Flags().String("description", "", "Short description of the space.")
		cmd.Flags().String("access-level", "", "Access level: public or restricted (default public).")
		cmd.Flags().String("posting-permission", "", "Who can post: any_member, admins_only, admins_and_moderators (owner, admins and moderators), or segment (default any_member).")
		cmd.Flags().Int("position", 0, "Zero-based display position within the hub.")
		cmd.Flags().String("segment-id", "", "Segment id gating a restricted space.")
		cmd.Flags().Bool("is-default", false, "Whether this is a default space.")
		cmd.Flags().Bool("is-pinned", false, "Whether the space is pinned.")
		cmd.Flags().String("icon", "", "Space icon name.")
		cmd.Flags().String("color", "", "Space accent color as a hex value (e.g. #6747E3).")
	}
	communitySpacesReorderCmd.Flags().String("order", "", "Comma-separated list of space ids in the desired display order.")

	addPaginationFlags(communitySpacesListCmd)
}

var communityDiscussionsCmd = &cobra.Command{
	Use:   "discussions",
	Short: "Manage community discussions.",
	Long:  "List, create, retrieve, update and delete discussion posts within a hub. Requires team-admin privileges.",
}

func discussionsAdminPath(teamID, hubID, id string) string {
	base := communityAdminBase(teamID, hubID) + "/discussions"
	if id != "" {
		return base + "/" + id
	}
	return base
}

var communityDiscussionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List discussions in a hub.",
	Long:  "List all discussions in the active hub (admin view). Requires team-admin privileges.",
	Example: `  mio community discussions list --hub hub_abc123
  mio community discussions list --hub hub_abc123 --limit 25`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		if cmd.Flags().Changed("filter-status") {
			if v, ferr := cmd.Flags().GetString("filter-status"); ferr == nil && v != "" {
				query.Set("filter[status]", v)
			}
		}
		if cmd.Flags().Changed("filter-space") {
			if v, ferr := cmd.Flags().GetString("filter-space"); ferr == nil && v != "" {
				query.Set("filter[space_id]", v)
			}
		}

		col, err := c.client.List(c.ctx, discussionsAdminPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		injectDiscussionDerivedStateAll(col)
		return c.render(cmd, col)
	},
}

func injectDiscussionDerivedState(res *client.Resource) {
	if res == nil || res.Attributes == nil {
		return
	}
	v, present := res.Attributes["deleted_at"]
	if !present {
		return
	}
	res.Attributes["deleted"] = v != nil
}

func injectDiscussionDerivedStateAll(col *client.Collection) {
	if col == nil {
		return
	}
	for i := range col.Data {
		injectDiscussionDerivedState(&col.Data[i])
	}
}

var communityDiscussionsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a discussion in a hub space (authored as you).",
	Long: `Create a discussion post in one of the hub's spaces — the admin welcome-post
route (MIO-2262).

The AUTHOR is derived server-side from your credentials (a team-owner key posts
as the team owner's contact); there is deliberately no flag to author as someone
else. The post lands in the target space regardless of that space's
posting_permission, because the backend treats this route as an admin write.

--space-id and --title are required. Omitting --is-published publishes the post
immediately (the backend default); pass --is-published=false to leave a draft.`,
	Example: `  mio community discussions create --hub hub_abc123 --space-id space_abc --title "Welcome!"
  mio community discussions create --hub hub_abc123 --space-id space_abc --title "Welcome!" --body "Say hi in the comments."
  mio community discussions create --hub hub_abc123 --space-id space_abc --title "Draft" --is-published=false`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "space-id")
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "body")
		setBoolFlag(cmd, attrs, "is-published")

		if _, ok := attrs["space_id"]; !ok {
			return errs.New(errs.ExitUsage, "--space-id is required to create a discussion")
		}
		if _, ok := attrs["title"]; !ok {
			return errs.New(errs.ExitUsage, "--title is required to create a discussion")
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, discussionsAdminPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		injectDiscussionDerivedState(res)
		return c.render(cmd, res)
	},
}

var communityDiscussionsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a discussion by id.",
	Long:    "Retrieve a single discussion post by its id (admin view).",
	Example: `  mio community discussions retrieve disc_abc123 --hub hub_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, discussionsAdminPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		injectDiscussionDerivedState(res)
		return c.render(cmd, res)
	},
}

var communityDiscussionsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a discussion's moderation state by id.",
	Long: `Update a discussion's moderation state (admin). Only the flags you provide
are changed.

The admin PATCH endpoint sets moderation state only — pin, lock, and broadcast —
not title/body (those are authored by the member and are not admin-editable).`,
	Example: `  mio community discussions update disc_abc123 --hub hub_abc123 --is-pinned=true
  mio community discussions update disc_abc123 --hub hub_abc123 --is-locked=true
  mio community discussions update disc_abc123 --hub hub_abc123 --is-broadcast=false`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		attrs := map[string]any{}
		setBoolFlag(cmd, attrs, "is-pinned")
		setBoolFlag(cmd, attrs, "is-locked")
		setBoolFlag(cmd, attrs, "is-broadcast")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one of --is-pinned/--is-locked/--is-broadcast")
		}

		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Update(c.ctx, discussionsAdminPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		injectDiscussionDerivedState(res)
		return c.render(cmd, res)
	},
}

var communityDiscussionsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a discussion by id.",
	Long:  "Permanently delete a discussion post (admin). Pass --yes to skip the confirmation prompt.",
	Example: `  mio community discussions delete disc_abc123 --hub hub_abc123
  mio community discussions delete disc_abc123 --hub hub_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete discussion %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, discussionsAdminPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted discussion %s.\n", args[0])
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{communityDiscussionsUpdateCmd} {
		cmd.Flags().Bool("is-pinned", false, "Pin (true) or unpin (false) the discussion.")
		cmd.Flags().Bool("is-locked", false, "Lock (true) or unlock (false) the discussion.")
		cmd.Flags().Bool("is-broadcast", false, "Mark (true) or unmark (false) the discussion as a broadcast announcement.")
	}

	communityDiscussionsCreateCmd.Flags().String("space-id", "", "Id of the space to post in (required).")
	communityDiscussionsCreateCmd.Flags().String("title", "", "Discussion title, max 280 characters (required).")
	communityDiscussionsCreateCmd.Flags().String("body", "", "Markdown body of the post.")
	communityDiscussionsCreateCmd.Flags().Bool("is-published", true, "Publish immediately (default). Pass --is-published=false to save a draft.")

	addPaginationFlags(communityDiscussionsListCmd)
	communityDiscussionsListCmd.Flags().String("filter-status", "",
		"IGNORED by the API (it accepts no status filter) — filter client-side on .status: published, scheduled or draft.")
	communityDiscussionsListCmd.Flags().String("filter-space", "", "Filter by space id.")

}

var communityMembersCmd = &cobra.Command{
	Use:   "members",
	Short: "Moderate hub members.",
	Long: `Perform moderation actions on hub members: ban, unban, or warn. Requires team-admin privileges.

The <contact_id> positional on every action below is the GLOBAL contact id, NOT
the .id from 'mio contacts' (that is the team-contact id and these verbs will
404 on it). Capture it with:
  CID=$(mio contacts retrieve <team-contact-id> -o plain --jq .contact_id)`,
}

func memberActionPath(teamID, hubID, contactID, action string) string {
	return fmt.Sprintf("%s/members/%s/%s", communityAdminBase(teamID, hubID), contactID, action)
}

var communityMembersBanCmd = &cobra.Command{
	Use:   "ban <contact_id>",
	Short: "Ban a hub member.",
	Long: `Issue a hard ban against a hub member. The contact will be blocked from accessing the hub.

<contact_id> is the GLOBAL contact id, NOT the .id from 'mio contacts'. Capture
it with:
  CID=$(mio contacts retrieve <team-contact-id> -o plain --jq .contact_id)`,
	Example: `  mio community members ban contact_xyz --hub hub_abc123 --yes
  mio community members ban contact_xyz --hub hub_abc123 --notes "Spam policy violation" --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "notes")

		if err := confirmDestructive(cmd, fmt.Sprintf("Ban member %s?", args[0])); err != nil {
			return err
		}

		path := memberActionPath(teamID, hubID, args[0], "ban")
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return hintGlobalContactID(err)
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Banned member %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

var communityMembersUnbanCmd = &cobra.Command{
	Use:   "unban <contact_id>",
	Short: "Unban a hub member.",
	Long: `Lift a ban against a hub member, restoring their access.

<contact_id> is the GLOBAL contact id, NOT the .id from 'mio contacts'. Capture
it with:
  CID=$(mio contacts retrieve <team-contact-id> -o plain --jq .contact_id)`,
	Example: `  mio community members unban contact_xyz --hub hub_abc123 --yes
  mio community members unban contact_xyz --hub hub_abc123 --notes "Reviewed and cleared" --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "notes")

		if err := confirmDestructive(cmd, fmt.Sprintf("Unban member %s?", args[0])); err != nil {
			return err
		}

		path := memberActionPath(teamID, hubID, args[0], "unban")
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return hintGlobalContactID(err)
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Unbanned member %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

var communityMembersWarnCmd = &cobra.Command{
	Use:   "warn <contact_id>",
	Short: "Warn a hub member.",
	Long: `Issue a formal warning to a hub member without banning them.

<contact_id> is the GLOBAL contact id, NOT the .id from 'mio contacts'. Capture
it with:
  CID=$(mio contacts retrieve <team-contact-id> -o plain --jq .contact_id)`,
	Example: `  mio community members warn contact_xyz --hub hub_abc123 --yes
  mio community members warn contact_xyz --hub hub_abc123 --notes "First offense warning" --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := communityContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "notes")

		if err := confirmDestructive(cmd, fmt.Sprintf("Warn member %s?", args[0])); err != nil {
			return err
		}

		path := memberActionPath(teamID, hubID, args[0], "warn")
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return hintGlobalContactID(err)
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Warned member %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	for _, cmd := range []*cobra.Command{
		communityMembersBanCmd,
		communityMembersUnbanCmd,
		communityMembersWarnCmd,
	} {
		cmd.Flags().String("notes", "", "Optional admin notes for the moderation action.")
	}
}
