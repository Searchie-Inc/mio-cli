package cmd

// apikeys.go implements the `mio api-keys` command group.
//
// Routes (see docs/internal/api-surface.md "api-keys"):
//
//	create   POST   /api/teams/{team_id}/api-keys           {name, expires_at?}
//	list     GET    /api/teams/{team_id}/api-keys
//	retrieve GET    /api/teams/{team_id}/api-keys/{id}
//	delete   DELETE /api/teams/{team_id}/api-keys/{id}      (revoke, destructive)
//
// The secret is returned by the server only on create; subsequent retrieves
// will not re-expose it. All routes are team-scoped; no hub context is needed.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	apikeysCmd.AddCommand(
		apikeysCreateCmd,
		apikeysListCmd,
		apikeysRetrieveCmd,
		apikeysDeleteCmd,
	)

	rootCmd.AddCommand(apikeysCmd)
}

// ---- api-keys group ---------------------------------------------------------

var apikeysCmd = &cobra.Command{
	Use:   "api-keys",
	Short: "Manage API keys for the active team.",
	Long: `Create, list, retrieve, and delete API keys scoped to the active team.

The secret value is returned only once at creation time. Store it immediately —
subsequent retrieve and list calls will not re-expose the secret.`,
}

// apikeysPath returns /api/teams/{team_id}/api-keys[/{id}].
func apikeysPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/api-keys", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ---- create -----------------------------------------------------------------

var apikeysCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an API key.",
	Long: `Create a new API key for the active team.

The secret is returned exactly once in the response. Store it immediately —
it cannot be retrieved again after this call.`,
	Example: `  # Create a key with a name only (no expiry)
  mio api-keys create --name="CI deploy key"

  # Create a key that expires on a specific date
  mio api-keys create --name="Staging key" --expires-at="2027-01-01T00:00:00Z"`,
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
		// expires-at (kebab flag name) maps to expires_at (JSON:API attribute key).
		if cmd.Flags().Changed("expires-at") {
			if v, err2 := cmd.Flags().GetString("expires-at"); err2 == nil {
				attrs["expires_at"] = v
			}
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, apikeysPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

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

// ---- retrieve ---------------------------------------------------------------

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

// ---- delete (revoke) --------------------------------------------------------

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
	// Attribute flags for create.
	apikeysCreateCmd.Flags().String("name", "", "Human-readable name for the API key.")
	apikeysCreateCmd.Flags().String("expires-at", "", "Expiry timestamp in RFC 3339 format (e.g. 2027-01-01T00:00:00Z). Omit for a non-expiring key.")

	// Pagination flags for list.
	addPaginationFlags(apikeysListCmd)
}
