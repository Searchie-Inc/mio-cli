package cmd

// activity.go implements the `mio activity` command group.
//
// All activity routes are hub-scoped admin reads — no create/update/delete
// operations exist on this surface. Both commands require a resolved team id
// and hub id (--hub or config current_hub).
//
// Routes (see docs/internal/api-surface.md "activity"):
//
//	contact     GET /api/teams/{team_id}/hubs/{hub_id}/activity/contacts/{contact_id}
//	top-engaged GET /api/teams/{team_id}/hubs/{hub_id}/activity/top-engaged

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func init() {
	activityCmd.AddCommand(
		activityContactCmd,
		activityTopEngagedCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(activityCmd)
}

// ---- activity group ---------------------------------------------------------

var activityCmd = &cobra.Command{
	Use:   "activity",
	Short: "Inspect hub activity.",
	Long:  "Read activity data for a hub: per-contact history and top-engaged contact rankings.",
}

// activityBasePath returns /api/teams/{team_id}/hubs/{hub_id}/activity.
func activityBasePath(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/activity", teamID, hubID)
}

// ---- activity contact -------------------------------------------------------

var activityContactCmd = &cobra.Command{
	Use:   "contact <contact_id>",
	Short: "Retrieve activity for a contact within a hub.",
	Long: `Retrieve the activity history for a specific contact within the active hub.

<contact_id> is the GLOBAL contact id — the .attributes.contact_id field from
'mio contacts', NOT its .id (that is the team-contact id and this route will 404
on it).

Requires --hub (or a configured current hub) and --team (or a configured current team).`,
	Example: `  # Retrieve activity for contact abc123 in hub hub456
  mio activity contact abc123 --hub hub456

  # Output as JSON for agent processing
  mio activity contact abc123 --hub hub456 --output json

  # Filter with jq
  mio activity contact abc123 --hub hub456 --jq '.events[]'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := activityContext(cmd)
		if err != nil {
			return err
		}

		path := fmt.Sprintf("%s/contacts/%s", activityBasePath(teamID, hubID), args[0])

		res, err := c.client.Retrieve(c.ctx, path)
		if err != nil {
			return hintGlobalContactID(err)
		}
		return c.render(cmd, res)
	},
}

// ---- activity top-engaged ---------------------------------------------------

var activityTopEngagedCmd = &cobra.Command{
	Use:   "top-engaged",
	Short: "List the top-engaged contacts in a hub.",
	Long: `List the most engaged contacts within the active hub, ranked by activity.

Requires --hub (or a configured current hub) and --team (or a configured current team).`,
	Example: `  # List top-engaged contacts in hub hub456
  mio activity top-engaged --hub hub456

  # Paginate results
  mio activity top-engaged --hub hub456 --limit 20

  # Output as a table on the terminal
  mio activity top-engaged --hub hub456 --output table`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := activityContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		path := fmt.Sprintf("%s/top-engaged", activityBasePath(teamID, hubID))

		col, err := c.client.List(c.ctx, path, query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// activityContext is the shared boilerplate for activity subcommands: build the
// context, require auth, and resolve both the team id and hub id.
func activityContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
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
	addPaginationFlags(activityTopEngagedCmd)
}
