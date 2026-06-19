package cmd

// analytics.go implements the `mio analytics` command group.
//
// All analytics routes are hub-scoped admin reads — no create/update/delete
// operations exist on this surface. Both commands require a resolved team id
// and hub id (--hub or config current_hub).
//
// Routes (see docs/internal/api-surface.md "analytics"):
//
//	overview GET /api/teams/{team_id}/hubs/{hub_id}/analytics/overview
//	email    GET /api/teams/{team_id}/hubs/{hub_id}/analytics/email

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func init() {
	analyticsCmd.AddCommand(
		analyticsOverviewCmd,
		analyticsEmailCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(analyticsCmd)
}

// ---- analytics group --------------------------------------------------------

var analyticsCmd = &cobra.Command{
	Use:   "analytics",
	Short: "Inspect hub analytics.",
	Long:  "Read analytics data for a hub: membership overview and email deliverability KPIs.",
}

// analyticsBasePath returns /api/teams/{team_id}/hubs/{hub_id}/analytics.
func analyticsBasePath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/analytics", teamID, hubID)
}

// ---- analytics overview -----------------------------------------------------

var analyticsOverviewCmd = &cobra.Command{
	Use:   "overview",
	Short: "Retrieve membership analytics overview for a hub.",
	Long: `Retrieve membership summary analytics for the active hub over a date range.

Returns: total_active_members, new_members, active_members, top_spaces.
Default window: last 30 days (matching /overview backend default).

--from and --to accept ISO-8601 date-time strings (e.g. 2026-06-01T00:00:00Z).
Naive date-times are treated as UTC by the backend.`,
	Example: `  # Default 30-day window
  mio analytics overview --hub hub_123

  # Custom date range
  mio analytics overview --hub hub_123 --from 2026-05-01T00:00:00Z --to 2026-06-01T00:00:00Z

  # Output as JSON for downstream processing
  mio analytics overview --hub hub_123 --output json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := analyticsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		if cmd.Flags().Changed("from") {
			v, _ := cmd.Flags().GetString("from")
			query.Set("from", v)
		}
		if cmd.Flags().Changed("to") {
			v, _ := cmd.Flags().GetString("to")
			query.Set("to", v)
		}

		path := analyticsBasePath(teamID, hubID) + "/overview"

		res, err := c.client.RetrieveWithQuery(c.ctx, path, query)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- analytics email --------------------------------------------------------

var analyticsEmailCmd = &cobra.Command{
	Use:   "email",
	Short: "Retrieve email deliverability KPIs for a hub.",
	Long: `Retrieve email deliverability KPIs for the active hub over a date range.

Returns: sent, delivered, bounced_hard, bounced_soft, complained, unsubscribed,
bounce_rate, complaint_rate.
Default window: last 24 hours (matching /email backend default).

--from and --to accept ISO-8601 date-time strings (e.g. 2026-06-01T00:00:00Z).
Naive date-times are treated as UTC by the backend.`,
	Example: `  # Default 24h window
  mio analytics email --hub hub_123

  # Custom date range
  mio analytics email --hub hub_123 --from 2026-06-18T00:00:00Z --to 2026-06-19T00:00:00Z

  # Output as JSON for downstream processing
  mio analytics email --hub hub_123 --output json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := analyticsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		if cmd.Flags().Changed("from") {
			v, _ := cmd.Flags().GetString("from")
			query.Set("from", v)
		}
		if cmd.Flags().Changed("to") {
			v, _ := cmd.Flags().GetString("to")
			query.Set("to", v)
		}

		path := analyticsBasePath(teamID, hubID) + "/email"

		res, err := c.client.RetrieveWithQuery(c.ctx, path, query)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// analyticsContext is the shared boilerplate for analytics subcommands: build
// the context, require auth, and resolve both the team id and hub id.
func analyticsContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
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

func init() {
	// Date range flags shared by both analytics commands.
	for _, c := range []*cobra.Command{analyticsOverviewCmd, analyticsEmailCmd} {
		c.Flags().String("from", "", "Start of the date range (ISO-8601, e.g. 2026-06-01T00:00:00Z). Default: 30d ago for overview, 24h ago for email.")
		c.Flags().String("to", "", "End of the date range (ISO-8601, e.g. 2026-06-19T00:00:00Z). Default: now.")
	}
}
