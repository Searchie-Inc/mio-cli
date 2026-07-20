package cmd

// hubmemberships.go — `mio hub-memberships` command group.
//
// Provides team-admin operations on hub memberships. All routes require both
// --team and --hub in context, and a team-owner user JWT.
//
// Routes backed by app/community/routers/moderation_admin.py:
//
//	list   GET  /api/admin/teams/{team_id}/hubs/{hub_id}/members            (MIO-2284)
//	ban    POST /api/admin/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/ban
//	unban  POST /api/admin/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/unban
//	warn   POST /api/admin/teams/{team_id}/hubs/{hub_id}/members/{contact_id}/warn
//
// `list` returns members across ALL statuses (the team-admin read added in
// MIO-2284); it is the API-key counterpart to the contact-JWT-only
// GET /api/hubs/{hub_id}/members.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	hubMembershipsCmd.AddCommand(
		hubMembershipsListCmd,
		hubMembershipsBanCmd,
		hubMembershipsUnbanCmd,
		hubMembershipsWarnCmd,
	)
	rootCmd.AddCommand(hubMembershipsCmd)
}

// hubMembershipStatuses are the moderation statuses accepted by --filter-status,
// mirroring the backend CHECK constraint (ck_hub_memberships_status).
var hubMembershipStatuses = map[string]bool{
	"active": true, "banned": true, "soft_banned": true, "left": true,
}

// ---- list -------------------------------------------------------------------

var hubMembershipsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List hub members (all statuses).",
	Long: `List the active hub's members for operator verification, across all
moderation statuses (active, banned, soft_banned, left). Requires --hub and a
team-owner API key.

Cursor pagination: --limit sets page[size] (max 100), --after sets page[after]
(a contact id from the previous page). Narrow to a single status with
--filter-status.`,
	Example: `  mio hub-memberships list --hub hub_abc123
  mio hub-memberships list --hub hub_abc123 --filter-status active --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate --filter-status BEFORE resolving auth/team/hub so a bad value
		// fails with a usage error and fires no HTTP request (repo contract).
		query := url.Values{}
		if cmd.Flags().Changed("filter-status") {
			status, _ := cmd.Flags().GetString("filter-status")
			if status != "" {
				if !hubMembershipStatuses[status] {
					return errs.New(errs.ExitUsage, "invalid --filter-status %q: must be active, banned, soft_banned, or left", status)
				}
				query.Set("filter[status]", status)
			}
		}

		c, teamID, hubID, err := hubMembershipsContext(cmd)
		if err != nil {
			return err
		}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, hubMembersPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- hub-memberships group --------------------------------------------------

var hubMembershipsCmd = &cobra.Command{
	Use:   "hub-memberships",
	Short: "Manage hub member status.",
	Long: `Perform moderation actions on hub members.

The <contact_id> positional on every action below is the GLOBAL contact id — the
.attributes.contact_id field from 'mio contacts', NOT its .id (that is the
team-contact id and these verbs will 404 on it).

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
	Long: `Issue a hard ban against a hub member, blocking their access to the hub.

<contact_id> is the GLOBAL contact id (the .attributes.contact_id from
'mio contacts', NOT its .id).`,
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
			return hintGlobalContactID(err)
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
	Long: `Lift a ban against a hub member, restoring their access to the hub.

<contact_id> is the GLOBAL contact id (the .attributes.contact_id from
'mio contacts', NOT its .id).`,
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
			return hintGlobalContactID(err)
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
	Long: `Issue a formal warning to a hub member without banning them.

<contact_id> is the GLOBAL contact id (the .attributes.contact_id from
'mio contacts', NOT its .id).`,
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
			return hintGlobalContactID(err)
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

	addPaginationFlags(hubMembershipsListCmd)
	hubMembershipsListCmd.Flags().String("filter-status", "", "Filter by moderation status: active, banned, soft_banned, or left.")
}
