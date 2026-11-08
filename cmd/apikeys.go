package cmd

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	apikeysCmd.AddCommand(
		apikeysCreateCmd,
		apikeysListCmd,
		apikeysRetrieveCmd,
		apikeysUpdateCmd,
		apikeysDeleteCmd,
	)

	rootCmd.AddCommand(apikeysCmd)
}

var apikeysCmd = &cobra.Command{
	Use:   "api-keys",
	Short: "Manage API keys for the active team.",
	Long: `Create, list, retrieve, and delete API keys scoped to the active team.

The secret value is returned only once at creation time. Store it immediately —
subsequent retrieve and list calls will not re-expose the secret.`,
}

func apikeysPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/api-keys", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

var apikeysCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an API key.",
	Long: `Create a new API key for the active team.

The secret is returned exactly once in the response. Store it immediately —
it cannot be retrieved again after this call.

Managing keys (create, list, retrieve, update, delete) needs a USER access token:
pass the access_token from POST /api/v1/auth/login with --api-key. A team API key,
including the stored one, is refused with 403 (exit 3).`,
	Example: `  # Create a key with a name only (no expiry)
  mio api-keys create --name="CI deploy key"

  # Create a key that expires on a specific date
  mio api-keys create --name="Staging key" --expires-at="2027-01-01T00:00:00Z"

  # A key that may author hub events without a member token (MIO-3610)
  mio api-keys create --name="Events bot" --can-author-events`,
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
		if cmd.Flags().Changed("expires-at") {
			if v, err2 := cmd.Flags().GetString("expires-at"); err2 == nil {
				attrs["expires_at"] = v
			}
		}

		setBoolFlag(cmd, attrs, "can-moderate")
		setBoolFlag(cmd, attrs, "can-author-events")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.CreateWith(c.ctx, client.StyleFlat, apikeysPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var apikeysListCmd = &cobra.Command{
	Use:   "list",
	Short: "List API keys for the active team.",
	Long:  "List all API keys belonging to the active team. Secret values are not included.",
	Example: `  mio api-keys list
  mio api-keys list --limit=20
  mio api-keys list --after=<cursor>`,
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

		col, err := c.client.List(c.ctx, apikeysPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var apikeysRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an API key by id.",
	Long:    "Retrieve metadata for a single API key. The secret value is not included.",
	Example: `  mio api-keys retrieve apk_abc123`,
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

		res, err := c.client.Retrieve(c.ctx, apikeysPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var apikeysUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Change an API key's permission flags.",
	Long: `Change what an existing API key may do. Only the flags you pass are sent; an
omitted flag is left as it is, and at least one must be given.

  --can-author-events   let the key create/update/cancel hub events (the team-key
                        route used by 'mio events create|update|cancel' when no
                        MIO_CONTACT_TOKEN is set — MIO-3610)
  --can-moderate        let the key use the member-moderation endpoints

Pass =false to revoke a flag (--can-author-events=false). Only the team owner
may do this, and it needs a USER access token: pass the access_token from
POST /api/v1/auth/login with --api-key — a team API key, including the stored
one, is refused with 403 (exit 3). Find a key's id with 'mio api-keys list'.`,
	Example: `  mio api-keys update apk_abc123 --can-author-events
  mio api-keys update apk_abc123 --can-moderate=false`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(args[0]) == "" {
			return errs.New(errs.ExitUsage, "api key id is blank (an unset variable?)")
		}
		attrs := map[string]any{}
		setBoolFlag(cmd, attrs, "can-moderate")
		setBoolFlag(cmd, attrs, "can-author-events")
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: pass --can-author-events and/or --can-moderate")
		}
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
		res, err := c.client.UpdateWith(c.ctx, client.StyleFlat, apikeysPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var apikeysDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete (revoke) an API key by id.",
	Long: `Permanently revoke an API key. Any requests using the revoked key will
immediately begin receiving 401 Unauthorized responses.

This action is irreversible. Pass --yes to skip the confirmation prompt in
non-interactive environments.`,
	Example: `  # Interactive — prompts for confirmation
  mio api-keys delete apk_abc123

  # Non-interactive (CI) — skip the prompt
  mio api-keys delete apk_abc123 --yes`,
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

		if err := confirmDestructive(cmd, fmt.Sprintf("Revoke API key %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, apikeysPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked API key %s.\n", args[0])
		return nil
	},
}

func init() {
	apikeysCreateCmd.Flags().String("name", "", "Human-readable name for the API key.")
	apikeysCreateCmd.Flags().String("expires-at", "", "Expiry timestamp in RFC 3339 format (e.g. 2027-01-01T00:00:00Z). Omit for a non-expiring key.")
	apikeysCreateCmd.Flags().Bool("can-moderate", false, "Let the key use the member-moderation endpoints (default false).")
	apikeysCreateCmd.Flags().Bool("can-author-events", false, "Let the key create/update/cancel hub events without a member token (default false).")
	apikeysUpdateCmd.Flags().Bool("can-moderate", false, "Set the can_moderate flag (=false to revoke).")
	apikeysUpdateCmd.Flags().Bool("can-author-events", false, "Set the can_author_events flag (=false to revoke).")

	addPaginationFlags(apikeysListCmd)
}
