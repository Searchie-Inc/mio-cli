package cmd

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/config"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	teamsCmd.AddCommand(
		teamsCreateCmd,
		teamsListCmd,
		teamsRetrieveCmd,
		teamsUpdateCmd,
		teamsDeleteCmd,
		teamsSwitchCmd,
	)

	teamsMembersCmd.AddCommand(
		teamsMembersListCmd,
		teamsMembersAddCmd,
		teamsMembersRemoveCmd,
	)
	teamsCmd.AddCommand(teamsMembersCmd)

	rootCmd.AddCommand(teamsCmd)
}

var teamsCmd = &cobra.Command{
	Use:   "teams",
	Short: "Manage teams.",
	Long:  "Create, list, retrieve, update, delete and switch between teams. Also manage team membership.",
}

func teamsPath(id string) string {
	if id != "" {
		return "/api/teams/" + id
	}
	return "/api/teams"
}

var teamsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new team.",
	Long: `Create a new team. --name and --slug are both required: they are the only
two fields the API's team create takes. The slug must be unique across all
teams; a taken slug answers 409 (exit 2). A team's slug is set here, once —
'mio teams update' cannot change it.

A team is account-level, so the API creates one only for a USER session and
answers an API key with 403 (exit 3): "This operation requires user (JWT)
authentication, not an API key." That includes the key 'mio login' stores. To
create a team from the CLI, pass a user access token (the access_token that
POST /api/v1/auth/login returns) as the credential for this one call with
--api-key, or create the team in the member.dev dashboard.`,
	Example: `  # Create a team. Name and slug are both required, and the credential must be
  # a user access token: the API refuses an API key here with 403 (exit 3).
  mio teams create --name="Acme Corp" --slug=acme --api-key "$ACCESS_TOKEN"

  # Capture the new team's id
  TEAM_ID=$(mio teams create --name="Acme Corp" --slug=acme --api-key "$ACCESS_TOKEN" -o plain --jq .id)`,
	Args:    cobra.NoArgs,
	PreRunE: applyTeamsSubdomainAlias,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "slug")

		res, err := c.client.Create(c.ctx, teamsPath(""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func applyTeamsSubdomainAlias(cmd *cobra.Command, _ []string) error {
	fs := cmd.Flags()
	if !fs.Changed("subdomain") {
		return nil
	}
	sub, _ := fs.GetString("subdomain")
	if fs.Changed("slug") {
		if slug, _ := fs.GetString("slug"); slug != sub {
			return errs.New(errs.ExitUsage,
				"--slug %q and --subdomain %q disagree: --subdomain is a deprecated alias of --slug, so pass only --slug",
				slug, sub)
		}
	} else if err := fs.Set("slug", sub); err != nil {
		return errs.Wrap(errs.ExitGeneric, err)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "warning: --subdomain is deprecated and sends slug; use --slug")
	return nil
}

var teamsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List teams.",
	Long:  "List all teams accessible to the authenticated user.",
	Example: `  mio teams list
  mio teams list --limit=20`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, teamsPath(""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var teamsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a team by id.",
	Long:    "Retrieve the full details of a single team by its id.",
	Example: `  mio teams retrieve team_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, teamsPath(args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var teamsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a team by id.",
	Long: `Rename a team. --name is the only field the API's team update takes: a
team's slug is set once, by 'mio teams create --slug', and cannot be changed.

Only the team's owner can rename it. An API key works for the team it is bound
to; a key bound to any other team is refused with 403 (exit 3).`,
	Example: `  mio teams update team_abc123 --name="New Name"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, teamsPath(args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var teamsDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a team by id.",
	Long:    "Permanently delete a team. This action is irreversible. Pass --yes to skip the confirmation prompt.",
	Example: `  mio teams delete team_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete team %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, teamsPath(args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted team %s.\n", args[0])
		return nil
	},
}

var teamsSwitchCmd = &cobra.Command{
	Use:   "switch <id>",
	Short: "Switch the active team.",
	Long: `Switch the active team. This performs the server-side switch AND updates the
local CLI context: it writes the team as the current team in config and clears
the current hub (hubs are team-scoped, so the old hub no longer applies).

To set the local team context WITHOUT a server-side switch, use
'mio config set current_team <id>'.`,
	Example: `  mio teams switch team_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		res, err := c.client.Action(c.ctx, "POST", teamsPath(args[0])+"/switch", nil)
		if err != nil {
			return err
		}

		cfg, cerr := config.Load()
		if cerr != nil {
			return errs.Wrap(errs.ExitGeneric, cerr)
		}
		cfg.CurrentTeam = args[0]
		cfg.CurrentHub = ""
		if serr := cfg.Save(); serr != nil {
			return errs.Wrap(errs.ExitGeneric, serr)
		}

		if isTTY(cmd.ErrOrStderr()) {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"Switched to team %s (updated local context; cleared current hub).\n", args[0])
		}

		if res != nil {
			return c.render(cmd, res)
		}
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{teamsCreateCmd, teamsUpdateCmd} {
		cmd.Flags().String("name", "", "Team display name.")
	}
	teamsCreateCmd.Flags().String("slug", "", "Team slug: unique across all teams, set once at creation.")
	teamsCreateCmd.Flags().String("subdomain", "", "Deprecated alias of --slug.")
	if err := teamsCreateCmd.Flags().MarkHidden("subdomain"); err != nil {
		panic("MarkHidden subdomain: " + err.Error())
	}
	markFlagsRequired(teamsCreateCmd, "name", "slug")
	addPaginationFlags(teamsListCmd)
}

var teamsMembersCmd = &cobra.Command{
	Use:   "members",
	Short: "Manage team membership.",
	Long:  "List, add, and remove members from a team.",
}

func teamsMembersPath(teamID, userID string) string {
	base := fmt.Sprintf("/api/teams/%s/members", teamID)
	if userID != "" {
		return base + "/" + userID
	}
	return base
}

var teamsMembersListCmd = &cobra.Command{
	Use:   "list <team_id>",
	Short: "List members of a team.",
	Long:  "List all members belonging to the specified team.",
	Example: `  mio teams members list team_abc123
  mio teams members list team_abc123 --limit=50`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, teamsMembersPath(args[0], ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var teamsMembersAddCmd = &cobra.Command{
	Use:   "add <team_id>",
	Short: "Add a member to a team.",
	Long:  "Add a user to a team by user id. Optionally assign a role.",
	Example: `  mio teams members add team_abc123 --user-id=usr_xyz
  mio teams members add team_abc123 --user-id=usr_xyz --role-id=role_admin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "user-id")
		setStringFlag(cmd, attrs, "role-id")

		if _, ok := attrs["user_id"]; !ok {
			return errs.New(errs.ExitUsage, "nothing to add: --user-id is required")
		}

		res, err := c.client.Create(c.ctx, teamsMembersPath(args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var teamsMembersRemoveCmd = &cobra.Command{
	Use:     "remove <team_id> <user_id>",
	Short:   "Remove a member from a team.",
	Long:    "Remove a user from a team. Pass --yes to skip the confirmation prompt.",
	Example: `  mio teams members remove team_abc123 usr_xyz --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newContext(cmd)
		if err != nil {
			return err
		}
		if err := c.requireAuth(); err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Remove user %s from team %s?", args[1], args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, teamsMembersPath(args[0], args[1])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed user %s from team %s.\n", args[1], args[0])
		return nil
	},
}

func init() {
	teamsMembersAddCmd.Flags().String("user-id", "", "User id to add to the team.")
	teamsMembersAddCmd.Flags().String("role-id", "", "Role id to assign to the new member (optional).")
	addPaginationFlags(teamsMembersListCmd)
}
