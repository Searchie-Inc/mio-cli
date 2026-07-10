package cmd

// email.go — email command group for the mio CLI.
//
// All routes are hub-scoped (base: /v1/hubs/{hub_id}/…).  The command tree is:
//
//	email drip-campaigns  create/list/retrieve/update/delete/activate/pause
//	email steps           create/list/update/delete            (under a drip campaign)
//	email templates       create/list/retrieve/update/delete/preview
//	email config          set/get/delete/test
//	email enrollments     create/list/exit/list-by-contact     (under a drip campaign / by contact)
//	email stats           get
//	email suppressions    list/create/lift                     (hub-scoped admin block list)
//
// Self-registered: init() attaches emailCmd to rootCmd. No other file is touched.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// drip-campaigns sub-commands
	emailDripCampaignsCmd.AddCommand(
		emailDripCreateCmd,
		emailDripListCmd,
		emailDripRetrieveCmd,
		emailDripUpdateCmd,
		emailDripDeleteCmd,
		emailDripActivateCmd,
		emailDripPauseCmd,
	)

	// steps sub-commands (nested under drip-campaigns)
	emailStepsCmd.AddCommand(
		emailStepsCreateCmd,
		emailStepsListCmd,
		emailStepsUpdateCmd,
		emailStepsDeleteCmd,
	)

	// templates sub-commands
	emailTemplatesCmd.AddCommand(
		emailTemplatesCreateCmd,
		emailTemplatesListCmd,
		emailTemplatesRetrieveCmd,
		emailTemplatesUpdateCmd,
		emailTemplatesDeleteCmd,
		emailTemplatesPreviewCmd,
	)

	// config sub-commands
	emailConfigCmd.AddCommand(
		emailConfigSetCmd,
		emailConfigGetCmd,
		emailConfigDeleteCmd,
		emailConfigTestCmd,
	)

	// enrollments sub-commands
	emailEnrollmentsCmd.AddCommand(
		emailEnrollmentsCreateCmd,
		emailEnrollmentsListCmd,
		emailEnrollmentsExitCmd,
		emailEnrollmentsListByContactCmd,
	)

	// stats sub-commands
	emailStatsCmd.AddCommand(
		emailStatsGetCmd,
	)

	// suppressions sub-commands
	emailSuppressionsCmd.AddCommand(
		emailSuppressionsListCmd,
		emailSuppressionsCreateCmd,
		emailSuppressionsLiftCmd,
	)

	// Attach all sub-groups to email root.
	emailCmd.AddCommand(
		emailDripCampaignsCmd,
		emailStepsCmd,
		emailTemplatesCmd,
		emailConfigCmd,
		emailEnrollmentsCmd,
		emailStatsCmd,
		emailSuppressionsCmd,
	)

	// Self-register on root.
	rootCmd.AddCommand(emailCmd)
}

// ---- root group -------------------------------------------------------------

var emailCmd = &cobra.Command{
	Use:   "email",
	Short: "Manage email features for a hub.",
	Long:  "Manage drip campaigns, email templates, email config, enrollments, and email stats for the active hub.",
}

// emailHubPath builds /v1/hubs/{hub_id}/{suffix}.
func emailHubPath(hubID, suffix string) string {
	base := fmt.Sprintf("/v1/hubs/%s", hubID)
	if suffix != "" {
		return base + "/" + suffix
	}
	return base
}

// emailContext is the shared setup for all email sub-commands: resolve context,
// require auth, and require a hub id.
func emailContext(cmd *cobra.Command) (*cmdContext, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", err
	}
	return c, hubID, nil
}

// ---- drip-campaigns ---------------------------------------------------------

var emailDripCampaignsCmd = &cobra.Command{
	Use:   "drip-campaigns",
	Short: "Manage drip campaigns.",
	Long:  "Create, list, retrieve, update, delete, activate, and pause drip campaigns for the active hub.",
}

// dripPath returns /v1/hubs/{hub_id}/drip-campaigns[/{id}].
func dripPath(hubID, id string) string {
	if id != "" {
		return emailHubPath(hubID, "drip-campaigns/"+id)
	}
	return emailHubPath(hubID, "drip-campaigns")
}

var emailDripCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a drip campaign.",
	Long:  "Create a new drip campaign for the active hub.",
	Example: `  mio email drip-campaigns create --name="Welcome Series" --status=draft
  mio email drip-campaigns create --name="Onboarding" --description="New member flow"
  mio email drip-campaigns create --name="Segment Drip" --enrollment-mode=segment --segment-id=seg_abc --segment-check-interval-minutes=60`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "status")
		// Enrollment-mode flags.
		setStringFlag(cmd, attrs, "enrollment-mode")
		setStringFlag(cmd, attrs, "trigger-event-type")
		setStringFlag(cmd, attrs, "segment-id")
		setIntFlag(cmd, attrs, "segment-check-interval-minutes")
		setBoolFlag(cmd, attrs, "allow-reenrollment")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, dripPath(hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailDripListCmd = &cobra.Command{
	Use:   "list",
	Short: "List drip campaigns.",
	Long:  "List all drip campaigns for the active hub.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, dripPath(hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var emailDripRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a drip campaign by id.",
	Long:    "Retrieve a single drip campaign by its id for the active hub.",
	Example: `  mio email drip-campaigns retrieve dc_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, dripPath(hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailDripUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a drip campaign by id.",
	Long:  "Update a drip campaign's attributes for the active hub. Only the flags you set are changed.",
	Example: `  mio email drip-campaigns update dc_abc123 --name="New Name" --status=active
  mio email drip-campaigns update dc_abc123 --enrollment-mode=segment --segment-id=seg_abc`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "status")
		// Enrollment-mode flags.
		setStringFlag(cmd, attrs, "enrollment-mode")
		setStringFlag(cmd, attrs, "trigger-event-type")
		setStringFlag(cmd, attrs, "segment-id")
		setIntFlag(cmd, attrs, "segment-check-interval-minutes")
		setBoolFlag(cmd, attrs, "allow-reenrollment")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, dripPath(hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailDripDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a drip campaign by id.",
	Long:    "Permanently delete a drip campaign. Requires --yes in non-interactive shells.",
	Example: `  mio email drip-campaigns delete dc_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete drip campaign %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, dripPath(hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted drip campaign %s.\n", args[0])
		return nil
	},
}

var emailDripActivateCmd = &cobra.Command{
	Use:     "activate <id>",
	Short:   "Activate a drip campaign.",
	Long:    "Activate a drip campaign, making it eligible to enroll contacts.",
	Example: `  mio email drip-campaigns activate dc_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		path := emailHubPath(hubID, "drip-campaigns/"+args[0]+"/activate")
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Activated drip campaign %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

var emailDripPauseCmd = &cobra.Command{
	Use:     "pause <id>",
	Short:   "Pause a drip campaign.",
	Long:    "Pause an active drip campaign, suspending new email delivery.",
	Example: `  mio email drip-campaigns pause dc_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		path := emailHubPath(hubID, "drip-campaigns/"+args[0]+"/pause")
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Paused drip campaign %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	for _, cmd := range []*cobra.Command{emailDripCreateCmd, emailDripUpdateCmd} {
		cmd.Flags().String("name", "", "Drip campaign name.")
		cmd.Flags().String("description", "", "Drip campaign description.")
		cmd.Flags().String("status", "", "Drip campaign status (e.g. draft, active, paused).")
		// Enrollment-mode flags — control how contacts enter the campaign.
		cmd.Flags().String("enrollment-mode", "", "Enrollment trigger mode: event, segment, or both.")
		cmd.Flags().String("trigger-event-type", "", "Event type that triggers enrollment when enrollment-mode is event or both.")
		cmd.Flags().String("segment-id", "", "Segment whose members are auto-enrolled when enrollment-mode is segment or both.")
		cmd.Flags().Int("segment-check-interval-minutes", 0, "How often (in minutes) the backend re-checks the segment for new members to enroll.")
		cmd.Flags().Bool("allow-reenrollment", false, "Allow a contact to be re-enrolled after they have already completed or exited the campaign.")
	}
	addPaginationFlags(emailDripListCmd)
}

// ---- steps (nested under drip-campaigns) ------------------------------------

var emailStepsCmd = &cobra.Command{
	Use:   "steps",
	Short: "Manage steps within a drip campaign.",
	Long:  "Create, list, update, and delete steps nested under a drip campaign.",
}

// stepsPath returns /v1/hubs/{hub_id}/drip-campaigns/{campaign_id}/steps[/{step_id}].
func stepsPath(hubID, campaignID, stepID string) string {
	base := emailHubPath(hubID, "drip-campaigns/"+campaignID+"/steps")
	if stepID != "" {
		return base + "/" + stepID
	}
	return base
}

var emailStepsCreateCmd = &cobra.Command{
	Use:     "create <campaign_id>",
	Short:   "Create a step in a drip campaign.",
	Long:    "Create a new step in the specified drip campaign.",
	Example: `  mio email steps create dc_abc123 --template-id=tmpl_xyz --delay-days=3`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "template-id")
		setIntFlag(cmd, attrs, "delay-days")
		setIntFlag(cmd, attrs, "position")
		setStringFlag(cmd, attrs, "subject")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --template-id")
		}

		res, err := c.client.Create(c.ctx, stepsPath(hubID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailStepsListCmd = &cobra.Command{
	Use:     "list <campaign_id>",
	Short:   "List steps in a drip campaign.",
	Long:    "List all steps for the specified drip campaign.",
	Example: `  mio email steps list dc_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, stepsPath(hubID, args[0], ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var emailStepsUpdateCmd = &cobra.Command{
	Use:     "update <campaign_id> <step_id>",
	Short:   "Update a drip campaign step.",
	Long:    "Update a step's attributes within a drip campaign. Only the flags you set are changed.",
	Example: `  mio email steps update dc_abc123 step_456 --delay-days=5`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "template-id")
		setIntFlag(cmd, attrs, "delay-days")
		setIntFlag(cmd, attrs, "position")
		setStringFlag(cmd, attrs, "subject")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, stepsPath(hubID, args[0], args[1]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailStepsDeleteCmd = &cobra.Command{
	Use:     "delete <campaign_id> <step_id>",
	Short:   "Delete a drip campaign step.",
	Long:    "Permanently delete a step from a drip campaign. Requires --yes in non-interactive shells.",
	Example: `  mio email steps delete dc_abc123 step_456 --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete step %s from campaign %s?", args[1], args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, stepsPath(hubID, args[0], args[1])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted step %s from campaign %s.\n", args[1], args[0])
		return nil
	},
}

func init() {
	for _, cmd := range []*cobra.Command{emailStepsCreateCmd, emailStepsUpdateCmd} {
		cmd.Flags().String("template-id", "", "ID of the email template to use for this step.")
		cmd.Flags().Int("delay-days", 0, "Number of days after the previous step (or enrollment) to send this step.")
		cmd.Flags().Int("position", 0, "Ordinal position of this step within the campaign.")
		cmd.Flags().String("subject", "", "Email subject line override for this step.")
	}
	addPaginationFlags(emailStepsListCmd)
}

// ---- templates --------------------------------------------------------------

var emailTemplatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "Manage email templates.",
	Long:  "Create, list, retrieve, update, delete, and preview email templates for the active hub.",
}

// templatesPath returns /v1/hubs/{hub_id}/email-templates[/{id}].
func templatesPath(hubID, id string) string {
	if id != "" {
		return emailHubPath(hubID, "email-templates/"+id)
	}
	return emailHubPath(hubID, "email-templates")
}

var emailTemplatesCreateCmd = &cobra.Command{
	Use:     "create",
	Short:   "Create an email template.",
	Long:    "Create a new email template for the active hub.",
	Example: `  mio email templates create --name="Welcome" --subject="Welcome!" --body="<mjml><mj-body><mj-section><mj-column><mj-text>Hello!</mj-text></mj-column></mj-section></mj-body></mjml>" --plain-text="Hello!"`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "subject")
		// Content fields: the backend email_templates schema is mjml_source +
		// plain_text (NOT a "body" attribute). --body therefore maps to
		// mjml_source so it actually sets the rendered content; a bare "body"
		// attribute is silently dropped by the backend (MIO-1238).
		setMappedString(cmd, attrs, "body", "mjml_source")
		setMappedString(cmd, attrs, "plain-text", "plain_text")
		setStringFlag(cmd, attrs, "from-name")
		setStringFlag(cmd, attrs, "from-email")
		setStringFlag(cmd, attrs, "reply-to")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name and --subject")
		}

		res, err := c.client.Create(c.ctx, templatesPath(hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailTemplatesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List email templates.",
	Long:  "List all email templates for the active hub.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, templatesPath(hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var emailTemplatesRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve an email template by id.",
	Long:    "Retrieve a single email template by its id for the active hub.",
	Example: `  mio email templates retrieve tmpl_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, templatesPath(hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailTemplatesUpdateCmd = &cobra.Command{
	Use:     "update <id>",
	Short:   "Update an email template by id.",
	Long:    "Update an email template's attributes. Only the flags you set are changed.",
	Example: `  mio email templates update tmpl_abc123 --subject="New Subject"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "subject")
		// Content fields: the backend email_templates schema is mjml_source +
		// plain_text (NOT a "body" attribute). --body therefore maps to
		// mjml_source so it actually sets the rendered content; a bare "body"
		// attribute is silently dropped by the backend (MIO-1238).
		setMappedString(cmd, attrs, "body", "mjml_source")
		setMappedString(cmd, attrs, "plain-text", "plain_text")
		setStringFlag(cmd, attrs, "from-name")
		setStringFlag(cmd, attrs, "from-email")
		setStringFlag(cmd, attrs, "reply-to")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, templatesPath(hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailTemplatesDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete an email template by id.",
	Long:    "Permanently delete an email template. Requires --yes in non-interactive shells.",
	Example: `  mio email templates delete tmpl_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete email template %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, templatesPath(hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted email template %s.\n", args[0])
		return nil
	},
}

var emailTemplatesPreviewCmd = &cobra.Command{
	Use:     "preview <id>",
	Short:   "Preview an email template.",
	Long:    "Render a sandbox preview of an email template. The backend preview endpoint takes NO request body.",
	Example: `  mio email templates preview tmpl_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		// The backend preview_email_template route binds no request body; it
		// renders the stored template in a sandbox. Send no payload.
		path := emailHubPath(hubID, "email-templates/"+args[0]+"/preview")
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Preview generated for template %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	for _, cmd := range []*cobra.Command{emailTemplatesCreateCmd, emailTemplatesUpdateCmd} {
		cmd.Flags().String("name", "", "Template name.")
		cmd.Flags().String("subject", "", "Email subject line.")
		cmd.Flags().String("body", "", "Email body as MJML source (sets the template's mjml_source).")
		cmd.Flags().String("plain-text", "", "Plain-text fallback body (sets plain_text).")
		cmd.Flags().String("from-name", "", "Sender display name.")
		cmd.Flags().String("from-email", "", "Sender email address.")
		cmd.Flags().String("reply-to", "", "Reply-to email address.")
	}
	addPaginationFlags(emailTemplatesListCmd)
}

// ---- config -----------------------------------------------------------------

var emailConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage hub email configuration.",
	Long:  "Set, get, delete, and test the email sending configuration for the active hub.",
}

// configPath returns /v1/hubs/{hub_id}/email-config.
func configPath(hubID string) string {
	return emailHubPath(hubID, "email-config")
}

var emailConfigSetCmd = &cobra.Command{
	Use:     "set",
	Short:   "Set (create or replace) the hub email configuration.",
	Long:    "Set the email sending configuration for the active hub using a PUT. All provided fields replace the existing config.",
	Example: `  mio email config set --mail-host=smtp.example.com --mail-port=587 --mail-username=user --mail-password=secret --from-email=hello@example.com --from-name="My Community"`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		// Flat body with the backend EmailConfigRequest field names (mail_*),
		// NOT a JSON:API envelope. The route binds a flat pydantic model.
		attrs := map[string]any{}
		setMappedString(cmd, attrs, "mail-host", "mail_host")
		setMappedInt(cmd, attrs, "mail-port", "mail_port")
		setMappedString(cmd, attrs, "mail-username", "mail_username")
		setMappedString(cmd, attrs, "mail-password", "mail_password")
		setMappedString(cmd, attrs, "mail-encryption", "mail_encryption")
		setMappedString(cmd, attrs, "from-name", "mail_from_name")
		setMappedString(cmd, attrs, "from-email", "mail_from_email")
		setMappedString(cmd, attrs, "reply-to", "reply_to")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to set: supply at least one configuration flag")
		}

		res, err := c.client.ActionWith(c.ctx, client.StyleFlat, "PUT", configPath(hubID), attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "Email configuration saved.")
			return nil
		}
		return c.render(cmd, res)
	},
}

var emailConfigGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get the hub email configuration.",
	Long:  "Retrieve the current email sending configuration for the active hub.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, configPath(hubID))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailConfigDeleteCmd = &cobra.Command{
	Use:     "delete",
	Short:   "Delete the hub email configuration.",
	Long:    "Remove the email sending configuration for the active hub. Requires --yes in non-interactive shells.",
	Example: `  mio email config delete --yes`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, "Delete the hub email configuration?"); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, configPath(hubID)); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Deleted hub email configuration.")
		return nil
	},
}

var emailConfigTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Re-test the hub's stored email configuration.",
	Long: `Re-run the SMTP test against the hub's stored BYO-SMTP credentials. The backend
re-reads the saved credentials and sends a test message to the authenticated
user's own email address — this endpoint takes NO request body and no recipient
flag. Returns a JSON:API email_test_results resource on success, or a 422 with
the SMTP error on failure.`,
	Example: `  mio email config test`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		// The backend test_email_config route binds no request body; sending one
		// (let alone an enveloped {to:…} with a path-derived type) is rejected.
		path := emailHubPath(hubID, "email-config/test")
		res, err := c.client.Action(c.ctx, "POST", path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "Test email sent.")
			return nil
		}
		return c.render(cmd, res)
	},
}

func init() {
	for _, cmd := range []*cobra.Command{emailConfigSetCmd} {
		// Flag names map to the backend EmailConfigRequest fields (mail_*).
		cmd.Flags().String("mail-host", "", "SMTP server hostname.")
		cmd.Flags().Int("mail-port", 0, "SMTP server port (e.g. 587, 465).")
		cmd.Flags().String("mail-username", "", "SMTP authentication username.")
		cmd.Flags().String("mail-password", "", "SMTP authentication password. Required on first set; omit on update to keep the existing one.")
		cmd.Flags().String("mail-encryption", "", "Encryption mode: tls (STARTTLS), ssl (implicit TLS), or none.")
		cmd.Flags().String("from-name", "", "Default sender display name (mail_from_name).")
		cmd.Flags().String("from-email", "", "Default sender email address (mail_from_email).")
		cmd.Flags().String("reply-to", "", "Optional reply-to address; omit to use the from address.")
	}
}

// ---- enrollments ------------------------------------------------------------

var emailEnrollmentsCmd = &cobra.Command{
	Use:   "enrollments",
	Short: "Manage drip campaign enrollments.",
	Long:  "List and exit enrollments for contacts in a drip campaign.",
}

// enrollmentsPath returns /v1/hubs/{hub_id}/drip-campaigns/{campaign_id}/enrollments[/{enrollment_id}].
func enrollmentsPath(hubID, campaignID, enrollmentID string) string {
	base := emailHubPath(hubID, "drip-campaigns/"+campaignID+"/enrollments")
	if enrollmentID != "" {
		return base + "/" + enrollmentID
	}
	return base
}

var emailEnrollmentsCreateCmd = &cobra.Command{
	Use:     "create <campaign_id>",
	Short:   "Manually enroll a contact in a drip campaign.",
	Long:    "Manually enroll a contact in the specified drip campaign. The contact must be a member of the hub.",
	Example: `  mio email enrollments create dc_abc123 --contact-id ctt_xyz789`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "contact-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set --contact-id")
		}

		res, err := c.client.Create(c.ctx, enrollmentsPath(hubID, args[0], ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailEnrollmentsListCmd = &cobra.Command{
	Use:     "list <campaign_id>",
	Short:   "List enrollments for a drip campaign.",
	Long:    "List all contact enrollments for the specified drip campaign.",
	Example: `  mio email enrollments list dc_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, enrollmentsPath(hubID, args[0], ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var emailEnrollmentsExitCmd = &cobra.Command{
	Use:     "exit <campaign_id> <enrollment_id>",
	Short:   "Remove a contact from a drip campaign.",
	Long:    "Exit (unenroll) a contact from a drip campaign. Requires --yes in non-interactive shells.",
	Example: `  mio email enrollments exit dc_abc123 enr_xyz789 --yes`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Exit enrollment %s from campaign %s?", args[1], args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, enrollmentsPath(hubID, args[0], args[1])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Exited enrollment %s from campaign %s.\n", args[1], args[0])
		return nil
	},
}

// contactDripEnrollmentsPath returns /v1/contacts/{contact_id}/drip-enrollments.
// This is a contact-scoped read endpoint — not hub-scoped.
func contactDripEnrollmentsPath(contactID string) string {
	return fmt.Sprintf("/v1/contacts/%s/drip-enrollments", contactID)
}

var emailEnrollmentsListByContactCmd = &cobra.Command{
	Use:     "list-by-contact <contact_id>",
	Short:   "List all drip enrollments for a contact.",
	Long:    "List every drip campaign enrollment for a given contact across all campaigns.",
	Example: `  mio email enrollments list-by-contact ctt_xyz789`,
	Args:    cobra.ExactArgs(1),
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

		col, err := c.client.List(c.ctx, contactDripEnrollmentsPath(args[0]), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

func init() {
	emailEnrollmentsCreateCmd.Flags().String("contact-id", "", "ID of the contact to enroll in the drip campaign.")
	addPaginationFlags(emailEnrollmentsListCmd)
	addPaginationFlags(emailEnrollmentsListByContactCmd)
}

// ---- stats ------------------------------------------------------------------

var emailStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "View email statistics.",
	Long:  "View email sending and engagement statistics for the active hub.",
}

var emailStatsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Get hub email statistics.",
	Long:  "Retrieve email sending and engagement statistics for the active hub.",
	Example: `  mio email stats get
  mio email stats get --from=2026-01-01 --to=2026-06-01`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		if cmd.Flags().Changed("from") {
			if v, err := cmd.Flags().GetString("from"); err == nil && v != "" {
				query.Set("filter[from]", v)
			}
		}
		if cmd.Flags().Changed("to") {
			if v, err := cmd.Flags().GetString("to"); err == nil && v != "" {
				query.Set("filter[to]", v)
			}
		}

		col, err := c.client.List(c.ctx, emailHubPath(hubID, "email-stats"), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

func init() {
	emailStatsGetCmd.Flags().String("from", "", "Start date for the stats window (ISO 8601, e.g. 2026-01-01).")
	emailStatsGetCmd.Flags().String("to", "", "End date for the stats window (ISO 8601, e.g. 2026-06-01).")
}

// ---- suppressions -----------------------------------------------------------

var emailSuppressionsCmd = &cobra.Command{
	Use:   "suppressions",
	Short: "Manage the hub email-suppression list.",
	Long:  "List, create (admin_block), and lift email suppressions for the active hub.",
}

// suppressionsPath returns /v1/hubs/{hub_id}/email-suppressions[/{id}].
func suppressionsPath(hubID, id string) string {
	if id != "" {
		return emailHubPath(hubID, "email-suppressions/"+id)
	}
	return emailHubPath(hubID, "email-suppressions")
}

var emailSuppressionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List email suppressions for the active hub.",
	Long:  "List the active email suppressions scoped to the active hub.",
	Example: `  mio email suppressions list
  mio email suppressions list --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, suppressionsPath(hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var emailSuppressionsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Suppress (admin_block) an email address for the active hub.",
	Long: `Create a manual admin_block suppression for an email address in the active hub.

Suppressed addresses are skipped by all hub email sends until the suppression
is lifted. The scope is always the hub and the reason is always admin_block.`,
	Example: `  mio email suppressions create --email blocked@example.com`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate the required flag BEFORE resolving auth so a usage error
		// fires no HTTP request.
		if !cmd.Flags().Changed("email") {
			return errs.New(errs.ExitUsage, "missing required flag: --email")
		}

		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setMappedString(cmd, attrs, "email", "email_address")

		// Enveloped POST: the backend HubCreateSuppressionData type is
		// "email_suppressions" (derived from the email-suppressions tail).
		res, err := c.client.Create(c.ctx, suppressionsPath(hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var emailSuppressionsLiftCmd = &cobra.Command{
	Use:   "lift <suppression_id>",
	Short: "Lift (un-suppress) a hub email suppression.",
	Long: `Lift a suppression for the active hub, re-enabling email delivery to that
address. Lifting an address that hard-bounced or complained can resume sending
to a bad recipient, so this requires --yes in non-interactive shells.`,
	Example: `  mio email suppressions lift esp_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, hubID, err := emailContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Lift suppression %s (re-enable email delivery)?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, suppressionsPath(hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Lifted suppression %s.\n", args[0])
		return nil
	},
}

func init() {
	addPaginationFlags(emailSuppressionsListCmd)
	emailSuppressionsCreateCmd.Flags().String("email", "", "Email address to suppress (admin_block). Required.")
}
