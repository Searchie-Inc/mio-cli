package cmd

// segments.go implements the `mio segments` command group.
//
// Routes (see docs/internal/api-surface.md "segments"):
//
//	segments: CRUD /api/teams/{team_id}/segments[/{id}]
//	search:   POST /api/teams/{team_id}/segments/search  (preview conditions)
//	members:  GET  /api/teams/{team_id}/segments/{id}/members
//	count:    GET  /api/teams/{team_id}/segments/{id}/members/count

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// segments <action>
	segmentsCmd.AddCommand(
		segmentsCreateCmd,
		segmentsListCmd,
		segmentsRetrieveCmd,
		segmentsUpdateCmd,
		segmentsDeleteCmd,
		segmentsSearchCmd,
		segmentsMembersCmd,
		segmentsCountCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(segmentsCmd)
}

// ---- segments group ---------------------------------------------------------

var segmentsCmd = &cobra.Command{
	Use:   "segments",
	Short: "Manage contact segments.",
	Long:  "Create, list, retrieve, update, and delete contact segments for the active team. Also search segment members and preview conditions.",
}

// segmentsPath returns /api/teams/{team_id}/segments[/{id}].
func segmentsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/segments", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// segmentsMembersPath returns /api/teams/{team_id}/segments/{id}/members[/count].
func segmentsMembersPath(teamID, id, suffix string) string {
	base := fmt.Sprintf("/api/teams/%s/segments/%s/members", teamID, id)
	if suffix != "" {
		return base + "/" + suffix
	}
	return base
}

var segmentsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a segment.",
	Long:  "Create a new contact segment for the active team.",
	Example: `  # Create a static segment
  mio segments create --name "VIP Contacts" --segment-type static

  # Create a dynamic segment with a description
  mio segments create --name "High Engagement" --segment-type dynamic --description "Contacts with high engagement scores"`,
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
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "segment-type")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, segmentsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var segmentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List segments.",
	Long:  "List all contact segments for the active team.",
	Example: `  mio segments list
  mio segments list --limit 50`,
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

		col, err := c.client.List(c.ctx, segmentsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var segmentsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a segment by id.",
	Long:    "Retrieve a single contact segment by its id.",
	Example: `  mio segments retrieve seg_abc123`,
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

		res, err := c.client.Retrieve(c.ctx, segmentsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var segmentsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a segment by id.",
	Long:  "Update one or more fields on a contact segment. Only the flags you pass are changed (partial update).",
	Example: `  mio segments update seg_abc123 --name "Renamed Segment"
  mio segments update seg_abc123 --description "Updated description"`,
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
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "segment-type")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, segmentsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var segmentsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a segment by id.",
	Long:  "Permanently delete a contact segment. This action cannot be undone.",
	Example: `  mio segments delete seg_abc123
  mio segments delete seg_abc123 --yes`,
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

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete segment %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, segmentsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted segment %s.\n", args[0])
		return nil
	},
}

var segmentsSearchCmd = &cobra.Command{
	Use:   "search",
	Short: "Preview contacts matching segment conditions.",
	Long: `Post a condition tree to preview which contacts would match, before saving a
dynamic segment.

--conditions takes the full condition TREE as JSON, matching the backend write
shape: {"version":1,"groups":[{"logic":"AND","conditions":[ ... ]}]}. Each leaf
is a discriminated object identified by "type" (e.g. {"type":"email","operator":
"contains","value":"@example.com"}). Pagination is controlled with --page-size
and --page-after.`,
	Example: `  mio segments search --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":"@example.com"}]}]}'
  mio segments search --conditions @conditions.json --page-size 50`,
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

		if !cmd.Flags().Changed("conditions") {
			return errs.New(errs.ExitUsage, "nothing to search: --conditions is required (a JSON condition tree)")
		}
		rawConditions, _ := cmd.Flags().GetString("conditions")
		conditions, perr := parseJSONFlag(rawConditions)
		if perr != nil {
			return errs.Wrap(errs.ExitUsage, fmt.Errorf("--conditions is not valid JSON: %w", perr))
		}

		// Build the segment_search attributes: {conditions, page?}. page is only
		// included when the caller set a pagination flag (the backend defaults it).
		attributes := map[string]any{"conditions": conditions}
		page := map[string]any{}
		setMappedInt(cmd, page, "page-size", "size")
		setMappedString(cmd, page, "page-after", "after")
		if len(page) > 0 {
			attributes["page"] = page
		}

		path := segmentsPath(teamID, "") + "/search"
		payload := client.NewRawEnvelope("segment_search", attributes)
		col, err := c.client.ActionCollectionRaw(c.ctx, "POST", path, payload)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var segmentsMembersCmd = &cobra.Command{
	Use:   "members <id>",
	Short: "List members of a segment.",
	Long:  "List the contacts currently in a segment.",
	Example: `  mio segments members seg_abc123
  mio segments members seg_abc123 --limit 100`,
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

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, segmentsMembersPath(teamID, args[0], ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var segmentsCountCmd = &cobra.Command{
	Use:     "count <id>",
	Short:   "Count members of a segment.",
	Long:    "Return the total number of contacts currently in a segment.",
	Example: `  mio segments count seg_abc123`,
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

		res, err := c.client.Retrieve(c.ctx, segmentsMembersPath(teamID, args[0], "count"))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

func init() {
	// Attribute flags for create and update.
	for _, cmd := range []*cobra.Command{segmentsCreateCmd, segmentsUpdateCmd} {
		cmd.Flags().String("name", "", "Segment name.")
		cmd.Flags().String("description", "", "Segment description.")
		cmd.Flags().String("segment-type", "", "Segment type: static or dynamic.")
	}

	// Pagination flags for list and members.
	addPaginationFlags(segmentsListCmd)
	addPaginationFlags(segmentsMembersCmd)

	// Search flags.
	segmentsSearchCmd.Flags().String("conditions", "", `Condition tree as JSON: {"version":1,"groups":[...]}. Prefix with @ to read from a file (e.g. @conditions.json).`)
	segmentsSearchCmd.Flags().Int("page-size", 0, "Preview page size (1–200). Omit for the backend default.")
	segmentsSearchCmd.Flags().String("page-after", "", "Pagination cursor for the next page of preview results.")
}
