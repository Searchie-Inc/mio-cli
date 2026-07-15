package cmd

// media_playlist_cover.go — `mio media playlists set-cover` (MIO-2289).
//
// Sets a playlist's cover image by creating a playlist-cover-attachment
// (backend app/media/router.py create_playlist_cover_attachment):
//
//	POST /api/teams/{team}/playlist-cover-attachments   (type attachments)
//
// The backend pins target_type=playlist and role=thumbnail (422 otherwise) and
// upserts — any existing cover attachment for the playlist is replaced. The
// media file must already exist in the team library.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	mediaPlaylistsCmd.AddCommand(mediaPlaylistsSetCoverCmd)

	mediaPlaylistsSetCoverCmd.Flags().String("media-id", "", "Media file id to use as the cover image. Required.")
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
image. The media file must already exist in the team library; any existing
cover attachment for the playlist is replaced.`,
	Example: `  mio media playlists set-cover pl_abc123 --media-id file_xyz789`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate before resolving auth/team so a missing flag fires no request.
		mediaID := flagValue(cmd, "media-id")
		if mediaID == "" {
			return errs.New(errs.ExitUsage, "--media-id is required (the media file to use as the cover)")
		}
		attrs := map[string]any{
			"media_id":    mediaID,
			"target_type": "playlist",
			"target_id":   args[0],
			"role":        "thumbnail",
		}
		setIntFlag(cmd, attrs, "position")

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, playlistCoverAttachmentsPath(teamID), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}
