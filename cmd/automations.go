package cmd

// automations.go implements the `mio automations` command group for managing
// marketing automations nested under a hub. Every sub-command is hub-scoped:
// both {team_id} and {hub_id} must be resolved from context.
//
// Routes (see docs/internal/api-surface.md "automations"):
//
//	create       POST   /api/teams/{team_id}/hubs/{hub_id}/automations
//	list         GET    /api/teams/{team_id}/hubs/{hub_id}/automations
//	retrieve     GET    /api/teams/{team_id}/hubs/{hub_id}/automations/{id}
//	update       PATCH  /api/teams/{team_id}/hubs/{hub_id}/automations/{id}
//	publish      POST   /api/teams/{team_id}/hubs/{hub_id}/automations/{id}/publish
//	activate     POST   /api/teams/{team_id}/hubs/{hub_id}/automations/{id}/activate
//	deactivate   POST   /api/teams/{team_id}/hubs/{hub_id}/automations/{id}/deactivate
//	versions     GET    /api/teams/{team_id}/hubs/{hub_id}/automations/{id}/versions
//	enrollments  GET    /api/teams/{team_id}/hubs/{hub_id}/automations/{id}/enrollments
//	enroll       POST   /api/teams/{team_id}/hubs/{hub_id}/automations/{id}/enrollments
//	fire-event   POST   /api/teams/{team_id}/hubs/{hub_id}/automations/events
//	test         POST   /api/teams/{team_id}/hubs/{hub_id}/automations/{id}/test

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// automations <action>
	automationsCmd.AddCommand(
		automationsCreateCmd,
		automationsListCmd,
		automationsRetrieveCmd,
		automationsUpdateCmd,
		automationsPublishCmd,
		automationsActivateCmd,
		automationsDeactivateCmd,
		automationsVersionsCmd,
		automationsEnrollmentsCmd,
		automationsEnrollCmd,
		automationsFireEventCmd,
		automationsTestCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(automationsCmd)
}

// ---- automations group -------------------------------------------------------

var automationsCmd = &cobra.Command{
	Use:   "automations",
	Short: "Manage hub automations.",
	Long:  "Create, list, retrieve, update and trigger marketing automations within a hub. Hub-scoped: --hub is required.",
}

// automationsBase returns the collection path for automations within a hub.
// /api/teams/{team_id}/hubs/{hub_id}/automations
func automationsBase(teamID, hubID string) string {
	return fmt.Sprintf("/api/teams/%s/hubs/%s/automations", teamID, hubID)
}

// automationsPath returns /api/teams/{team_id}/hubs/{hub_id}/automations[/{id}].
func automationsPath(teamID, hubID, id string) string {
	base := automationsBase(teamID, hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// automationsContext is the shared boilerplate for automations sub-commands:
// builds the context, requires auth, and resolves both team id and hub id.
func automationsContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
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

var automationsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an automation.",
	Long:  "Create a new automation in the active hub.",
	Example: `  mio automations create --hub hub_123 --name "Welcome Series" \
    --definition '{"nodes":[{"type":"exit","id":"n1","config":{}}],"edges":[],"triggers":[]}'

  mio automations create --hub hub_123 --name "Post-Purchase" --re-entry-mode after_exit \
    --definition @my-automation.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		var missing []string
		if !cmd.Flags().Changed("name") {
			missing = append(missing, "--name")
		}
		if !cmd.Flags().Changed("definition") {
			missing = append(missing, "--definition")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flags: %s", strings.Join(missing, ", "))
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "re-entry-mode")

		// --definition accepts JSON (inline or @file)
		if cmd.Flags().Changed("definition") {
			raw, ferr := cmd.Flags().GetString("definition")
			if ferr == nil {
				v, perr := parseJSONFlag(raw)
				if perr != nil {
					return errs.New(errs.ExitUsage, "--definition: invalid JSON: %s", perr.Error())
				}
				attrs["definition"] = v
			}
		}

		// --settings accepts JSON (inline or @file)
		if cmd.Flags().Changed("settings") {
			raw, ferr := cmd.Flags().GetString("settings")
			if ferr == nil {
				v, perr := parseJSONFlag(raw)
				if perr != nil {
					return errs.New(errs.ExitUsage, "--settings: invalid JSON: %s", perr.Error())
				}
				attrs["settings"] = v
			}
		}

		res, err := c.client.Create(c.ctx, automationsPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

var automationsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List automations.",
	Long:  "List all automations in the active hub.",
	Example: `  mio automations list --hub hub_123
  mio automations list --hub hub_123 --filter-status active`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		if cmd.Flags().Changed("filter-status") {
			if v, ferr := cmd.Flags().GetString("filter-status"); ferr == nil && v != "" {
				query.Set("filter[status]", v)
			}
		}

		col, err := c.client.List(c.ctx, automationsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- retrieve ---------------------------------------------------------------

var automationsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an automation by id.",
	Long:    "Retrieve a single automation from the active hub by its id.",
	Example: `  mio automations retrieve auto_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, automationsPath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- update -----------------------------------------------------------------

var automationsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an automation by id.",
	Long:  "Update one or more attributes of an existing automation.",
	Example: `  mio automations update auto_abc123 --hub hub_123 --name "New Name"
  mio automations update auto_abc123 --hub hub_123 --re-entry-mode interval`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "re-entry-mode")

		if cmd.Flags().Changed("definition") {
			raw, ferr := cmd.Flags().GetString("definition")
			if ferr == nil {
				v, perr := parseJSONFlag(raw)
				if perr != nil {
					return errs.New(errs.ExitUsage, "--definition: invalid JSON: %s", perr.Error())
				}
				attrs["definition"] = v
			}
		}

		if cmd.Flags().Changed("settings") {
			raw, ferr := cmd.Flags().GetString("settings")
			if ferr == nil {
				v, perr := parseJSONFlag(raw)
				if perr != nil {
					return errs.New(errs.ExitUsage, "--settings: invalid JSON: %s", perr.Error())
				}
				attrs["settings"] = v
			}
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, automationsPath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- publish ----------------------------------------------------------------

var automationsPublishCmd = &cobra.Command{
	Use:     "publish <id>",
	Short:   "Publish an automation (create a version snapshot).",
	Long:    "Publish the current draft of an automation, creating an immutable version snapshot.",
	Example: `  mio automations publish auto_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		path := automationsPath(teamID, hubID, args[0]) + "/publish"
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Published automation %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- activate ---------------------------------------------------------------

var automationsActivateCmd = &cobra.Command{
	Use:     "activate <id>",
	Short:   "Activate an automation.",
	Long:    "Activate a published automation so it starts processing new enrollments.",
	Example: `  mio automations activate auto_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		path := automationsPath(teamID, hubID, args[0]) + "/activate"
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Activated automation %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- deactivate -------------------------------------------------------------

var automationsDeactivateCmd = &cobra.Command{
	Use:     "deactivate <id>",
	Short:   "Deactivate an automation.",
	Long:    "Deactivate an active automation, stopping new enrollments.",
	Example: `  mio automations deactivate auto_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		path := automationsPath(teamID, hubID, args[0]) + "/deactivate"
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Deactivated automation %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- versions ---------------------------------------------------------------

var automationsVersionsCmd = &cobra.Command{
	Use:     "versions <id>",
	Short:   "List versions of an automation.",
	Long:    "List all published version snapshots for an automation.",
	Example: `  mio automations versions auto_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		path := automationsPath(teamID, hubID, args[0]) + "/versions"
		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, path, query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- enrollments (list) -----------------------------------------------------

var automationsEnrollmentsCmd = &cobra.Command{
	Use:   "enrollments <id>",
	Short: "List enrollments for an automation.",
	Long:  "List contacts enrolled in an automation, optionally filtered by status.",
	Example: `  mio automations enrollments auto_abc123 --hub hub_123
  mio automations enrollments auto_abc123 --hub hub_123 --filter-status stuck`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		path := automationsPath(teamID, hubID, args[0]) + "/enrollments"
		query := url.Values{}
		addPageFlags(cmd, query)

		if cmd.Flags().Changed("filter-status") {
			if v, ferr := cmd.Flags().GetString("filter-status"); ferr == nil && v != "" {
				query.Set("filter[status]", v)
			}
		}

		col, err := c.client.List(c.ctx, path, query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- enroll -----------------------------------------------------------------

var automationsEnrollCmd = &cobra.Command{
	Use:     "enroll <id>",
	Short:   "Enroll a contact in an automation.",
	Long:    "Manually enroll a team contact into an automation.",
	Example: `  mio automations enroll auto_abc123 --hub hub_123 --team-contact-id tcid_xyz`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "team-contact-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to enroll: set --team-contact-id")
		}

		path := automationsPath(teamID, hubID, args[0]) + "/enrollments"
		res, err := c.client.Create(c.ctx, path, attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- fire-event -------------------------------------------------------------

var automationsFireEventCmd = &cobra.Command{
	Use:   "fire-event",
	Short: "Fire a custom event to trigger automations.",
	Long:  "Fire a named event for a contact, which may trigger one or more automation entry conditions.",
	Example: `  mio automations fire-event --hub hub_123 --event-type purchase_completed --team-contact-id tcid_xyz --idempotency-key idem_abc
  mio automations fire-event --hub hub_123 --event-type webinar_attended --team-contact-id tcid_xyz --idempotency-key idem_def --payload '{"source":"webinar"}'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		// fire-event uses a flat body (not a JSON:API envelope): the backend
		// binds a flat model with event_type, team_contact_id, idempotency_key,
		// payload at the top level.
		body := map[string]any{}
		setStringFlag(cmd, body, "event-type")
		setStringFlag(cmd, body, "team-contact-id")
		setStringFlag(cmd, body, "idempotency-key")

		if cmd.Flags().Changed("payload") {
			raw, ferr := cmd.Flags().GetString("payload")
			if ferr == nil {
				v, perr := parseJSONFlag(raw)
				if perr != nil {
					return errs.New(errs.ExitUsage, "--payload: invalid JSON: %s", perr.Error())
				}
				body["payload"] = v
			}
		}

		if v, _ := body["event_type"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--event-type is required")
		}
		if v, _ := body["team_contact_id"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--team-contact-id is required")
		}
		if v, _ := body["idempotency_key"].(string); strings.TrimSpace(v) == "" {
			return errs.New(errs.ExitUsage, "--idempotency-key is required")
		}

		path := automationsBase(teamID, hubID) + "/events"
		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "POST", path, body)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Event fired.\n")
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- test -------------------------------------------------------------------

var automationsTestCmd = &cobra.Command{
	Use:     "test <id>",
	Short:   "Dry-run test an automation for a contact.",
	Long:    "Perform a dry-run execution of an automation for a given contact without creating real enrollments.",
	Example: `  mio automations test auto_abc123 --hub hub_123 --team-contact-id tcid_xyz`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := automationsContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "team-contact-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "--team-contact-id is required for test")
		}

		// The dry-run endpoint returns a FLAT report {"meta":…,"trace":[…]} with
		// no JSON:API `data` member, so it must be decoded as a raw document
		// (ActionRaw) — the resource decoder used by ActionWith errors "response
		// had no `data` member" on every successful run (MIO-2503). The trace is
		// exactly what an operator wants to see, so render it directly.
		path := automationsPath(teamID, hubID, args[0]) + "/test"
		report, err := c.client.ActionRaw(c.ctx, client.StyleFlat, "POST", path, attrs)
		if err != nil {
			return err
		}
		if report == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Test run completed for automation %s.\n", args[0])
			return nil
		}
		return c.render(cmd, report)
	},
}

// ---- flag registration -------------------------------------------------------

func init() {
	// Attribute flags for automations create/update.
	for _, cmd := range []*cobra.Command{automationsCreateCmd, automationsUpdateCmd} {
		cmd.Flags().String("name", "", "Automation name.")
		cmd.Flags().String("re-entry-mode", "", "Re-entry mode: never, after_exit, or interval.")
		cmd.Flags().String("definition", "", "Automation definition as JSON (inline or @file).")
		cmd.Flags().String("settings", "", "Automation settings as JSON (inline or @file).")
	}

	// Pagination on list commands.
	addPaginationFlags(automationsListCmd)
	addPaginationFlags(automationsVersionsCmd)
	addPaginationFlags(automationsEnrollmentsCmd)

	// filter-status on list and enrollments.
	automationsListCmd.Flags().String("filter-status", "", "Filter by status (e.g. draft, active, paused).")
	automationsEnrollmentsCmd.Flags().String("filter-status", "", "Filter by enrollment status (e.g. active, stuck, completed).")

	// enroll flags.
	automationsEnrollCmd.Flags().String("team-contact-id", "", "Team contact id to enroll.")

	// fire-event flags.
	automationsFireEventCmd.Flags().String("event-type", "", "Event type name (required).")
	automationsFireEventCmd.Flags().String("team-contact-id", "", "Team contact id to fire the event for (required).")
	automationsFireEventCmd.Flags().String("idempotency-key", "", "Idempotency key to prevent duplicate event delivery.")
	automationsFireEventCmd.Flags().String("payload", "", "Additional event payload as JSON (inline or @file).")

	// test flags.
	automationsTestCmd.Flags().String("team-contact-id", "", "Team contact id to use for the dry-run test.")
}
