package cmd

// media_playlist_cover.go — `mio media playlists set-cover` (MIO-2289, MIO-2519).
//
// Sets a playlist's cover image by creating a playlist-cover-attachment
// (backend app/media/router.py create_playlist_cover_attachment):
//
//	POST /api/teams/{team}/playlist-cover-attachments   (type attachments)
//
// The backend pins target_type=playlist and role=thumbnail (422 otherwise). The
// role IS "thumbnail" on purpose — it is NOT incidental: the whole playlist
// cover mechanism is keyed on role='thumbnail'. PlaylistService.resolve_cover_url
// and PlaylistRepository.find_cover_attachment both read the thumbnail-role
// attachment, the DB CHECK constraint (ck_attachment_role) has no 'cover' value,
// and a partial unique index (uq_playlist_cover_attachment) enforces at most one
// thumbnail-role row per playlist. So "thumbnail" is the backend-correct cover
// role — do NOT send "cover" (it would 422 and violate the CHECK constraint).
//
// Id vocabulary (MIO-2519): the attachment's media_id must be the Media PK, NOT
// the file id — the backend does media_repo.get(media_id) (a Media-table PK
// lookup), so a file id returns 404 "Media '<id>' not found." To stay consistent
// with `media hub-media publish --file-id` and `media playlists items add
// --file-id`, set-cover takes the FILE id via --file-id and resolves the file's
// media_id (exposed on the admin file resource as the `media_id` attribute)
// before POSTing. A missing/unprocessed file therefore fails with a self-naming
// error rather than the opaque backend 404.
//
// The backend upserts — any existing cover attachment for the playlist is
// replaced. The media file must already exist in the team library.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	mediaPlaylistsCmd.AddCommand(mediaPlaylistsSetCoverCmd)

	mediaPlaylistsSetCoverCmd.Flags().String("file-id", "", "File id whose media becomes the cover image (same id as `media files retrieve`). Required.")
	mediaPlaylistsSetCoverCmd.Flags().Int("position", 0, "Optional position (>= 0) for the cover attachment.")
}

// playlistCoverAttachmentsPath returns /api/teams/{team}/playlist-cover-attachments.
func playlistCoverAttachmentsPath(teamID string) string {
	return fmt.Sprintf("/api/teams/%s/playlist-cover-attachments", teamID)
}

var mediaPlaylistsSetCoverCmd = &cobra.Command{
	Use:   "set-cover <playlist_id>",
	Short: "Set a playlist's cover image from a media file.",
	Long: `Set (or replace) a playlist's cover by attaching a media file as its cover
image. Pass the FILE id via --file-id (the same id you use with
'media files retrieve'); the file's media is resolved automatically. The file
must already exist and have finished processing; any existing cover attachment
for the playlist is replaced.`,
	Example: `  mio media playlists set-cover pl_abc123 --file-id file_xyz789`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate before resolving auth/team so a bad flag fires no request.
		fileID := flagValue(cmd, "file-id")
		if fileID == "" {
			return errs.New(errs.ExitUsage, "--file-id is required (the file whose media becomes the cover)")
		}
		if cmd.Flags().Changed("position") {
			if pos, _ := cmd.Flags().GetInt("position"); pos < 0 {
				return errs.New(errs.ExitUsage, "invalid --position: must be >= 0")
			}
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		// Resolve the file's media_id — the cover attachment keys on the Media PK,
		// not the file id. A missing/foreign file 404s here naming the --file-id.
		// Shared with `content create/update` since MIO-3074.
		mediaID, err := resolveFileMediaID(c, teamID, fileID, "a cover")
		if err != nil {
			return err
		}

		attrs := map[string]any{
			"media_id":    mediaID,
			"target_type": "playlist",
			"target_id":   args[0],
			"role":        "thumbnail",
		}
		setIntFlag(cmd, attrs, "position")

		res, err := c.client.Create(c.ctx, playlistCoverAttachmentsPath(teamID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}
