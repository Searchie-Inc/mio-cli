package cmd

// hubs_branding_attach.go — `mio hubs branding attach` (MIO-3465).
//
// Completes the managed branding-image flow from the CLI alone. The flow
// (mio-docs guides/auth-brand-panel.mdx) was: create file → PUT bytes → finalize
// → raw POST /api/teams/{team_id}/hub-branding-attachments, because the CLI had
// no create verb for attachments. This adds the attach step:
//
//	POST /api/teams/{team}/hub-branding-attachments   (type attachments)
//
// target_type is pinned to hub_branding (the endpoint hard-codes
// caller_module='hubs' server-side and rejects any other target_type). The role
// set is the backend's BRANDING_ROLES (app/infrastructure/storage/
// public_asset_url.py): logo, favicon, social_image, auth_logo. Attaching
// replaces any prior attachment for the same (hub, role) in one transaction
// (MIO-2115), and the backend copies the object into the public branding bucket
// so a durable CDN URL overlays branding.<role>_url at read time — the stored
// hubs.branding JSONB is never mutated.
//
// Id vocabulary (MIO-2519, same as `media playlists set-cover`): the attachment
// keys on the Media PK, but the CLI takes the FILE id via --file-id and
// resolves the file's media_id, staying consistent with every other --file-id
// verb and turning the opaque backend 404 into a self-naming error.
//
// ELIGIBILITY PREFLIGHT — the one deliberate exception to "the CLI is a
// conduit, not a validation layer". The backend ACCEPTS an ineligible attach
// (the row is created, 201) but never publishes the asset publicly
// (is_media_eligible_for_public_branding: READY + image + raster MIME; SVG is
// excluded as an XSS surface). Without a preflight the failure mode is a
// silent no-op: the operator sees a 201 and no logo, ever. The checks below
// MIRROR the server's predicate — they do not invent a rule — and each fires
// before the POST with an error naming the offending property. Attributes the
// file read does not carry are skipped (the backend stays the authority).
//
// After a successful attach the hub is re-read and the overlaid
// branding.<role>_url is surfaced as a derived `resolved_public_url` attribute
// on the rendered attachment (same convention as injectHubDerivedState). When
// the URL has not resolved (e.g. no public CDN on the env), a stderr warning
// says so — stdout stays a clean JSON:API resource for --jq pipelines.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// brandingAttachRoles mirrors the backend BRANDING_ROLES frozenset — the roles
// the public-branding publisher and read-time resolver act on. Keep in sync
// with app/infrastructure/storage/public_asset_url.py.
var brandingAttachRoles = map[string]bool{
	"logo": true, "favicon": true, "social_image": true, "auth_logo": true,
}

// brandingRoleURLKey mirrors the resolver's _ROLE_TO_KEY
// (app/hubs/branding_resolver.py): the branding key each role overlays.
var brandingRoleURLKey = map[string]string{
	"logo":         "logo_url",
	"favicon":      "favicon_url",
	"social_image": "social_image_url",
	"auth_logo":    "auth_logo_url",
}

// safeBrandingImageMimeTypes mirrors SAFE_BRANDING_IMAGE_MIME_TYPES
// (app/infrastructure/storage/public_asset_url.py). Raster only — SVG is
// deliberately excluded server-side (embedded-script XSS surface).
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

// hubBrandingAttachmentsPath returns /api/teams/{team}/hub-branding-attachments.
func hubBrandingAttachmentsPath(teamID string) string {
	return fmt.Sprintf("/api/teams/%s/hub-branding-attachments", teamID)
}

// validateBrandingRole rejects a missing or non-branding role with an error
// naming the full backend BRANDING_ROLES set.
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

// brandingAttachPreflight resolves the file's media_id and checks the file
// against the backend's publish-eligibility predicate (see the file-top
// comment). It returns the media_id, or a usage error naming exactly what
// makes the file ineligible. Attributes the file read does not carry are
// skipped — the backend stays the authority.
//
// COVERAGE vs the backend predicate (Codex round 1): the server checks four
// properties — not deleted, READY, asset_kind=="image", safe MIME. The admin
// file resource serializes neither asset_kind nor deleted state, so the two
// "missing" checks are covered structurally instead: a soft-deleted file 404s
// at the GET this preflight reads from, and asset_kind is DERIVED server-side
// from the MIME type (_asset_kind_from_mime: any image/* → "image"), so every
// MIME in the raster allowlist below implies asset_kind=="image" — the MIME
// check subsumes it exactly. Only a file with NO mime_type on the wire skips
// the MIME check, and the backend remains the authority there.
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
		// Validate flags before resolving auth/team so a bad flag fires no request.
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

		// Resolve the file's media_id and preflight publish eligibility (see the
		// file-top comment: the backend accepts an ineligible attach but never
		// publishes it — a silent no-op the CLI turns into a named error).
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

		// Surface the overlaid public URL off a fresh hub read. Failures here
		// only warn — the attachment itself is already created.
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
