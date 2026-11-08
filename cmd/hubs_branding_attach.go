package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var brandingAttachRoles = map[string]bool{
	"logo": true, "favicon": true, "social_image": true, "auth_logo": true,
}

var brandingRoleURLKey = map[string]string{
	"logo":         "logo_url",
	"favicon":      "favicon_url",
	"social_image": "social_image_url",
	"auth_logo":    "auth_logo_url",
}

var safeBrandingImageMimeTypes = map[string]bool{
	"image/png":                true,
	"image/jpeg":               true,
	"image/jpg":                true,
	"image/webp":               true,
	"image/gif":                true,
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true,
}

func init() {
	hubsBrandingCmd.AddCommand(hubsBrandingAttachCmd)
	hubsCmd.AddCommand(hubsBrandingCmd)

	hubsBrandingAttachCmd.Flags().String("file-id", "", "File id whose media becomes the branding asset (same id as `media files retrieve`). Required.")
	hubsBrandingAttachCmd.Flags().String("role", "", "Branding role: logo, favicon, social_image, auth_logo. Required.")
	hubsBrandingAttachCmd.Flags().Int("position", 0, "Optional position (>= 0) for the attachment.")
}

func hubBrandingAttachmentsPath(teamID string) string {
	return fmt.Sprintf("/api/teams/%s/hub-branding-attachments", teamID)
}

func validateBrandingRole(role string) error {
	if brandingAttachRoles[role] {
		return nil
	}
	roles := make([]string, 0, len(brandingAttachRoles))
	for r := range brandingAttachRoles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	return errs.New(errs.ExitUsage, "invalid --role %q: must be one of %s", role, strings.Join(roles, ", "))
}

func brandingAttachPreflight(fileID string, attrs map[string]any) (string, error) {
	mediaID, _ := attrs["media_id"].(string)
	if mediaID == "" {
		return "", errs.New(errs.ExitUsage,
			"file %s has no media yet (it may still be processing) — cannot attach it as branding", fileID)
	}
	if status, _ := attrs["status_upload"].(string); status != "" && status != "READY" {
		return "", errs.New(errs.ExitUsage,
			"file %s upload status is %s — it must be READY before it can be published as branding", fileID, status)
	}
	if mime, _ := attrs["mime_type"].(string); mime != "" && !safeBrandingImageMimeTypes[mime] {
		switch {
		case strings.Contains(mime, "svg"):
			return "", errs.New(errs.ExitUsage,
				"file %s is an SVG (%s) — SVG is rejected for branding roles (XSS surface); upload a raster image (png, jpeg, webp, gif, ico)", fileID, mime)
		case !strings.HasPrefix(mime, "image/"):
			return "", errs.New(errs.ExitUsage,
				"file %s is not an image (%s) — branding roles require a raster image (png, jpeg, webp, gif, ico)", fileID, mime)
		default:
			return "", errs.New(errs.ExitUsage,
				"file %s has MIME type %s, outside the branding raster allowlist (png, jpeg, webp, gif, ico)", fileID, mime)
		}
	}
	return mediaID, nil
}

var hubsBrandingCmd = &cobra.Command{
	Use:   "branding",
	Short: "Manage a hub's managed branding assets.",
	Long: `Manage a hub's managed branding-image attachments — the upload-and-attach
flow behind branding.logo_url / favicon_url / social_image_url / auth_logo_url.`,
}

var hubsBrandingAttachCmd = &cobra.Command{
	Use:   "attach [hub_id]",
	Short: "Attach an uploaded media file as a hub branding asset.",
	Long: `Attach an uploaded media file to a hub under a managed branding role
(logo, favicon, social_image, auth_logo). Pass the FILE id via --file-id (the
same id you use with 'media files retrieve'); the file's media is resolved
automatically and must be a fully uploaded raster image (SVG is rejected —
the backend never publishes it). Attaching replaces any existing asset for the
same role, and the resolved public CDN URL overlays branding.<role>_url at
read time; the command re-reads the hub and reports it as resolved_public_url.`,
	Example: `  mio hubs branding attach hub_abc123 --file-id file_xyz789 --role auth_logo
  mio hubs branding attach --file-id file_xyz789 --role logo   # uses current_hub`,
	Args: hubsOptionalIDArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		fileID := flagValue(cmd, "file-id")
		if fileID == "" {
			return errs.New(errs.ExitUsage, "--file-id is required (the file whose media becomes the branding asset)")
		}
		role := flagValue(cmd, "role")
		if err := validateBrandingRole(role); err != nil {
			return err
		}
		if cmd.Flags().Changed("position") {
			if pos, _ := cmd.Flags().GetInt("position"); pos < 0 {
				return errs.New(errs.ExitUsage, "invalid --position: must be >= 0")
			}
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubArg, hubGiven := optionalArg(args, 0)
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}

		file, err := c.client.Retrieve(c.ctx, filesPath(teamID, fileID))
		if err != nil {
			return err
		}
		mediaID, preflightErr := brandingAttachPreflight(fileID, file.Attributes)
		if preflightErr != nil {
			return preflightErr
		}

		attrs := map[string]any{
			"media_id":    mediaID,
			"target_type": "hub_branding",
			"target_id":   hubID,
			"role":        role,
		}
		setIntFlag(cmd, attrs, "position")

		res, err := c.client.Create(c.ctx, hubBrandingAttachmentsPath(teamID), attrs)
		if err != nil {
			return err
		}

		urlKey := brandingRoleURLKey[role]
		if hub, hubErr := c.client.Retrieve(c.ctx, hubsPath(teamID, hubID)); hubErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"Warning: attachment created, but re-reading hub %s to resolve branding.%s failed: %v\n", hubID, urlKey, hubErr)
		} else {
			branding, _ := hub.Attributes["branding"].(map[string]any)
			if url, _ := branding[urlKey].(string); url != "" {
				res.Attributes["resolved_public_url"] = url
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"Warning: attachment created, but branding.%s is not resolved on the hub yet — the asset may not be published to the public branding CDN on this environment.\n", urlKey)
			}
		}
		return c.render(cmd, res)
	},
}
