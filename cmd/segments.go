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
	"strings"

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
	Example: `  mio segments list
  mio segments create --name "VIP Contacts" --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":{"text":"@example.com"}}]}]}'
  mio segments retrieve seg_abc123
  mio segments members seg_abc123
  mio segments search --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":{"text":"@example.com"}}]}]}'`,
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
	Long: `Create a new contact segment for the active team.

--name and --conditions are required. --conditions accepts the full condition tree as JSON.
Each condition's "value" field is a typed object, not a plain string. For example, an email
condition uses {"text":"@example.com"} as the value:
  {"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":{"text":"@example.com"}}]}]}
Prefix the value with @ to read the JSON from a file (e.g. --conditions @conds.json).`,
	Example: `  # Create a segment with an email condition
  mio segments create --name "VIP Contacts" --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":{"text":"@example.com"}}]}]}'

  # Create a segment loading conditions from a file
  mio segments create --name "High Engagement" --description "Engaged contacts" --conditions @conditions.json`,
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

		// Both --name and --conditions are required by the backend
		// SegmentCreateAttributes schema; validate client-side so a
		// partial-required body never reaches the API.
		var missing []string
		if !cmd.Flags().Changed("name") {
			missing = append(missing, "--name")
		}
		if !cmd.Flags().Changed("conditions") {
			missing = append(missing, "--conditions")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flag(s): %s", strings.Join(missing, ", "))
		}

		rawConditions, _ := cmd.Flags().GetString("conditions")
		conditions, perr := parseJSONFlag(rawConditions)
		if perr != nil {
			return errs.Wrap(errs.ExitUsage, fmt.Errorf("--conditions is not valid JSON: %w", perr))
		}

		attrs := map[string]any{"conditions": conditions}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "description")
		setBoolFlag(cmd, attrs, "is-active")

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
	Long: `Update one or more fields on a contact segment. Only the flags you pass are changed (partial update).

--conditions accepts the full condition tree as JSON (same shape as create). Prefix with @ to read from a file.`,
	Example: `  mio segments update seg_abc123 --name "Renamed Segment"
  mio segments update seg_abc123 --description "Updated description"
  mio segments update seg_abc123 --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":{"text":"@example.com"}}]}]}'`,
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
		setBoolFlag(cmd, attrs, "is-active")

		if cmd.Flags().Changed("conditions") {
			rawConditions, _ := cmd.Flags().GetString("conditions")
			conditions, perr := parseJSONFlag(rawConditions)
			if perr != nil {
				return errs.Wrap(errs.ExitUsage, fmt.Errorf("--conditions is not valid JSON: %w", perr))
			}
			attrs["conditions"] = conditions
		}

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
is a discriminated object identified by "type". Each condition's "value" is a
typed object — NOT a plain scalar. For example, an email condition uses:
  {"type":"email","operator":"contains","value":{"text":"@example.com"}}
Pagination is controlled with --page-size and --page-after.`,
	Example: `  mio segments search --conditions '{"version":1,"groups":[{"logic":"AND","conditions":[{"type":"email","operator":"contains","value":{"text":"@example.com"}}]}]}'
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
	// NOTE: --segment-type was removed (MIO-938) — the backend SegmentCreateAttributes
	// schema uses extra="forbid" and does not accept a segment_type field; sending it
	// caused a 422 "extra inputs not permitted". Segment type is now determined
	// automatically by the backend based on whether conditions are provided.
	for _, cmd := range []*cobra.Command{segmentsCreateCmd, segmentsUpdateCmd} {
		cmd.Flags().String("name", "", "Segment name.")
		cmd.Flags().String("description", "", "Segment description.")
		cmd.Flags().Bool("is-active", false, "Whether the segment is active (default true on create).")
		cmd.Flags().String("conditions", "", `Condition tree as JSON: {"version":1,"groups":[{"logic":"AND","conditions":[...]}]}. Prefix with @ to read from a file (e.g. @conditions.json).`)
	}

	// Pagination flags for list and members.
	addPaginationFlags(segmentsListCmd)
	addPaginationFlags(segmentsMembersCmd)

	// Search flags.
	segmentsSearchCmd.Flags().String("conditions", "", `Condition tree as JSON: {"version":1,"groups":[...]}. Prefix with @ to read from a file (e.g. @conditions.json).`)
	segmentsSearchCmd.Flags().Int("page-size", 0, "Preview page size (1–200). Omit for the backend default.")
	segmentsSearchCmd.Flags().String("page-after", "", "Pagination cursor for the next page of preview results.")
}
