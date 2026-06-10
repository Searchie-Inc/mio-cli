package cmd

// hubmemberships.go — `mio hub-memberships` command group.
//
// Provides team-admin operations on hub memberships. All routes require both
// --team and --hub in context, and a team-owner user JWT.
//
// Routes backed by app/community/routers/moderation_admin.py:
//
//	ban    POST /api/admin/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/ban
//	unban  POST /api/admin/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/unban
//	warn   POST /api/admin/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/warn
//
// NOTE: A team-admin list endpoint for hub members does not yet exist in the
// backend (GET /api/hubs/{hub_id}/members requires a contact JWT, not a team
// owner JWT). Once a team-admin list route is added to the backend, a `list`
// action can be wired here without breaking the existing sub-commands.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
)

func init() {
	hubMembershipsCmd.AddCommand(
		hubMembershipsBanCmd,
		hubMembershipsUnbanCmd,
		hubMembershipsWarnCmd,
	)
	rootCmd.AddCommand(hubMembershipsCmd)
}

// ---- hub-memberships group --------------------------------------------------

var hubMembershipsCmd = &cobra.Command{
	Use:   "hub-memberships",
	Short: "Manage hub member status.",
	Long: `Perform moderation actions on hub members.

All commands require --hub (and optionally --team if not auto-defaulted) and a
team-owner API key. Use 'mio community members' for the same actions within the
community admin surface.`,
	Example: `  mio hub-memberships ban contact_xyz --hub hub_abc123
  mio hub-memberships unban contact_xyz --hub hub_abc123
  mio hub-memberships warn contact_xyz --hub hub_abc123 --notes "Policy reminder"`,
}

// hubMembershipsContext builds context + resolves both team and hub ids.
func hubMembershipsContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
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

// memberModerationPath returns the action URL for a contact moderation action.
// /api/admin/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/{action}
func memberModerationPath(teamID, hubID, contactID, action string) string {
	return fmt.Sprintf(
		"/api/admin/teams/%s/hubs/%s/members/%s/%s",
		teamID, hubID, contactID, action,
	)
}

// ---- ban --------------------------------------------------------------------

var hubMembershipsBanCmd = &cobra.Command{
	Use:   "ban <contact_id>",
	Short: "Ban a hub member.",
	Long:  "Issue a hard ban against a hub member, blocking their access to the hub.",
	Example: `  mio hub-memberships ban contact_xyz --hub hub_abc123
  mio hub-memberships ban contact_xyz --hub hub_abc123 --notes "Repeated ToS violations"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := hubMembershipsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "notes")

		path := memberModerationPath(teamID, hubID, args[0], "ban")
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Banned member %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- unban ------------------------------------------------------------------

var hubMembershipsUnbanCmd = &cobra.Command{
	Use:   "unban <contact_id>",
	Short: "Unban a hub member.",
	Long:  "Lift a ban against a hub member, restoring their access to the hub.",
	Example: `  mio hub-memberships unban contact_xyz --hub hub_abc123
  mio hub-memberships unban contact_xyz --hub hub_abc123 --notes "Appealed, cleared"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := hubMembershipsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "notes")

		path := memberModerationPath(teamID, hubID, args[0], "unban")
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Unbanned member %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- warn -------------------------------------------------------------------

var hubMembershipsWarnCmd = &cobra.Command{
	Use:   "warn <contact_id>",
	Short: "Warn a hub member.",
	Long:  "Issue a formal warning to a hub member without banning them.",
	Example: `  mio hub-memberships warn contact_xyz --hub hub_abc123
  mio hub-memberships warn contact_xyz --hub hub_abc123 --notes "First offense"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := hubMembershipsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "notes")

		path := memberModerationPath(teamID, hubID, args[0], "warn")
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Warned member %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- flag registration ------------------------------------------------------

func init() {
	for _, cmd := range []*cobra.Command{
		hubMembershipsBanCmd,
		hubMembershipsUnbanCmd,
		hubMembershipsWarnCmd,
	} {
		cmd.Flags().String("notes", "", "Optional admin notes for the moderation action.")
	}
}
