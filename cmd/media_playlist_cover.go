package cmd

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
