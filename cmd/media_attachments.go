package cmd

// media_attachments.go — `mio media attachments` admin command group (MIO-2289).
//
// Team-scoped attachment admin surface (backend app/media/router.py
// attachments_admin_router, guard _require_team_member; requires the media
// feature flag enabled):
//
//	list   GET    /api/teams/{team}/attachments   [?media_id&target_type&target_id&page[...]]
//	show   GET    /api/teams/{team}/attachments/{id}
//	update PATCH  /api/teams/{team}/attachments/{id}   (type attachments; position/role)
//	delete DELETE /api/teams/{team}/attachments/{id}   (204)
//
// An attachment binds a media file to a target (playlist cover, page section,
// content node, hub branding, ...). Team-scoped; requires a team-member JWT.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// attachmentRoles is the AttachmentRole enum the backend accepts.
var attachmentRoles = map[string]bool{
	"main": true, "thumbnail": true, "logo": true, "favicon": true,
	"attachment": true, "social_image": true, "background_image": true,
	"widget_logo": true, "widget_background": true,
}

func init() {
	mediaAttachmentsCmd.AddCommand(
		mediaAttachmentsListCmd,
		mediaAttachmentsShowCmd,
		mediaAttachmentsUpdateCmd,
		mediaAttachmentsDeleteCmd,
	)
	mediaCmd.AddCommand(mediaAttachmentsCmd)

	mediaAttachmentsListCmd.Flags().String("media-id", "", "Filter by media file id.")
	mediaAttachmentsListCmd.Flags().String("target-type", "", "Filter by target type (e.g. playlist, content_node, page_section, hub_branding).")
	mediaAttachmentsListCmd.Flags().String("target-id", "", "Filter by target id.")
	addPaginationFlags(mediaAttachmentsListCmd)

	mediaAttachmentsUpdateCmd.Flags().Int("position", 0, "New display position (>= 0).")
	mediaAttachmentsUpdateCmd.Flags().String("role", "", "New attachment role: main, thumbnail, logo, favicon, attachment, social_image, background_image, widget_logo, widget_background.")
}

// attachmentsPath returns /api/teams/{team}/attachments[/{id}].
func attachmentsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/attachments", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

var mediaAttachmentsCmd = &cobra.Command{
	Use:   "attachments",
	Short: "Manage media attachments.",
	Long:  "List, inspect, update, and delete attachments — the rows that bind a media file to a target (playlist cover, page section, content node, hub branding, ...).",
}

// ---- list -------------------------------------------------------------------

var mediaAttachmentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List media attachments.",
	Long:  "List attachments for the active team, optionally filtered by media id and/or target. Cursor-paginated.",
	Example: `  mio media attachments list
  mio media attachments list --media-id file_abc --target-type playlist
  mio media attachments list --target-type playlist --target-id pl_1 --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		addPageFlags(cmd, query)
		for _, f := range []struct{ flag, param string }{
			{"media-id", "media_id"},
			{"target-type", "target_type"},
			{"target-id", "target_id"},
		} {
			if v := flagValue(cmd, f.flag); v != "" {
				query.Set(f.param, v)
			}
		}
		col, err := c.client.List(c.ctx, attachmentsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- show -------------------------------------------------------------------

var mediaAttachmentsShowCmd = &cobra.Command{
	Use:     "show <attachment_id>",
	Short:   "Show a media attachment by id.",
	Long:    "Retrieve a single attachment by its id.",
	Example: `  mio media attachments show att_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Retrieve(c.ctx, attachmentsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- update -----------------------------------------------------------------

var mediaAttachmentsUpdateCmd = &cobra.Command{
	Use:   "update <attachment_id>",
	Short: "Update a media attachment's position or role.",
	Long:  "Update an attachment's display position and/or role. Only the flags you provide are changed; at least one of --position or --role is required.",
	Example: `  mio media attachments update att_abc123 --position 2
  mio media attachments update att_abc123 --role thumbnail`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate before resolving auth/team so a no-op / bad flag fires no request.
		attrs := map[string]any{}
		setIntFlag(cmd, attrs, "position")
		if cmd.Flags().Changed("role") {
			role, _ := cmd.Flags().GetString("role")
			if !attachmentRoles[role] {
				return errs.New(errs.ExitUsage, "invalid --role %q: must be one of main, thumbnail, logo, favicon, attachment, social_image, background_image, widget_logo, widget_background", role)
			}
			attrs["role"] = role
		}
		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set --position and/or --role")
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Update(c.ctx, attachmentsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- delete -----------------------------------------------------------------

var mediaAttachmentsDeleteCmd = &cobra.Command{
	Use:     "delete <attachment_id>",
	Short:   "Delete a media attachment by id.",
	Long:    "Delete an attachment, unbinding the media file from its target. Pass --yes to skip the confirmation prompt.",
	Example: `  mio media attachments delete att_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Delete attachment %s?", args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, attachmentsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted attachment %s.\n", args[0])
		return nil
	},
}
