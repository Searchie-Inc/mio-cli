package cmd

// webhookendpoints.go implements the `mio webhook-endpoints` command group for
// managing outbound webhook endpoint registrations within a hub.
// Hub-scoped: every command requires a resolved hub id and team id.
//
// Routes (see docs/internal/api-surface.md "webhook-endpoints"):
//
//	create POST   /api/teams/{team_id}/hubs/{hub_id}/webhook-endpoints
//	list   GET    /api/teams/{team_id}/hubs/{hub_id}/webhook-endpoints
//	delete DELETE /api/teams/{team_id}/hubs/{hub_id}/webhook-endpoints/{id}

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// webhook-endpoints <action>
	webhookEndpointsCmd.AddCommand(
		webhookEndpointsCreateCmd,
		webhookEndpointsListCmd,
		webhookEndpointsDeleteCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(webhookEndpointsCmd)
}

// ---- webhook-endpoints group ------------------------------------------------

var webhookEndpointsCmd = &cobra.Command{
	Use:   "webhook-endpoints",
	Short: "Manage hub webhook endpoints.",
	Long:  "Create, list, and delete outbound webhook endpoint registrations for a hub. Hub-scoped: --hub is required.",
}

// webhookEndpointsBase returns the collection path for webhook endpoints.
// /api/teams/{team_id}/hubs/{hub_id}/webhook-endpoints
func webhookEndpointsBase(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/webhook-endpoints", teamID, hubID)
}

// webhookEndpointsPath returns …/webhook-endpoints[/{id}].
func webhookEndpointsPath(teamID, hubID, id string) string {
	base := webhookEndpointsBase(teamID, hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// webhookEndpointsContext is the shared boilerplate for webhook-endpoints
// sub-commands: builds the context, requires auth, and resolves both team id
// and hub id.
func webhookEndpointsContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
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

// ---- create -----------------------------------------------------------------

var webhookEndpointsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a webhook endpoint.",
	Long:  "Register a new outbound webhook endpoint for the active hub.",
	Example: `  mio webhook-endpoints create --hub hub_123 --name "My Webhook" --target-url https://example.com/hook
  mio webhook-endpoints create --hub hub_123 --name "Signed Hook" --target-url https://example.com/hook --signing-secret s3cr3t`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := webhookEndpointsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "target-url")
		setStringFlag(cmd, attrs, "signing-secret")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name and --target-url")
		}

		res, err := c.client.Create(c.ctx, webhookEndpointsPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

var webhookEndpointsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List webhook endpoints.",
	Long:    "List all registered webhook endpoints for the active hub.",
	Example: `  mio webhook-endpoints list --hub hub_123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := webhookEndpointsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, webhookEndpointsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- delete -----------------------------------------------------------------

var webhookEndpointsDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a webhook endpoint.",
	Long:    "Permanently remove a webhook endpoint registration. Requires confirmation.",
	Example: `  mio webhook-endpoints delete we_abc123 --hub hub_123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := webhookEndpointsContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete webhook endpoint %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, webhookEndpointsPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted webhook endpoint %s.\n", args[0])
		return nil
	},
}

// ---- flag registration ------------------------------------------------------

func init() {
	// create flags.
	webhookEndpointsCreateCmd.Flags().String("name", "", "Webhook endpoint name.")
	webhookEndpointsCreateCmd.Flags().String("target-url", "", "Destination URL that receives the webhook POST.")
	// signing-secret is write-only: the backend never returns it after creation.
	webhookEndpointsCreateCmd.Flags().String("signing-secret", "", "Optional signing secret used to verify webhook payloads (write-only; never returned by the API).")

	// pagination on list.
	addPaginationFlags(webhookEndpointsListCmd)
}
