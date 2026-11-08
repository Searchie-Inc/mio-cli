package cmd

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	mediaPlaylistsItemsCmd.AddCommand(
		mediaPlaylistsItemsAddCmd,
		mediaPlaylistsItemsListCmd,
		mediaPlaylistsItemsRemoveCmd,
		mediaPlaylistsItemsReorderCmd,
	)
	mediaPlaylistsCmd.AddCommand(mediaPlaylistsItemsCmd)

	for _, cmd := range []*cobra.Command{
		mediaPlaylistsItemsAddCmd,
		mediaPlaylistsItemsListCmd,
		mediaPlaylistsItemsRemoveCmd,
		mediaPlaylistsItemsReorderCmd,
	} {
		cmd.Flags().String("playlist-id", "", "Playlist id to operate on. Required.")
		markFlagsRequired(cmd, "playlist-id")
	}

	mediaPlaylistsItemsAddCmd.Flags().String("file-id", "", "Media file id to add to the playlist. Required.")
	markFlagsRequired(mediaPlaylistsItemsAddCmd, "file-id")
	mediaPlaylistsItemsAddCmd.Flags().Int("position", 0, "Optional 0-based position for the item; if omitted, the backend inserts it at position 0 (front).")

	mediaPlaylistsItemsReorderCmd.Flags().Int("position", 0, "New position (>= 0) for the item. Required.")
	markFlagsRequired(mediaPlaylistsItemsReorderCmd, "position")

	addPaginationFlags(mediaPlaylistsItemsListCmd)
}

func playlistItemsPath(teamID, playlistID, itemID string) string {
	base := fmt.Sprintf("/api/teams/%s/playlists/%s/items", teamID, playlistID)
	if itemID != "" {
		return base + "/" + itemID
	}
	return base
}

var mediaPlaylistsItemsCmd = &cobra.Command{
	Use:   "items",
	Short: "Manage the items in a media playlist.",
	Long:  "Add, list, remove, and reorder the media files that make up a playlist.",
}

var mediaPlaylistsItemsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a media file to a playlist.",
	Long:  "Add a media file to a playlist. Without --position the backend inserts it at position 0 (front); pass --position to place it explicitly. The media file must already exist in the team library.",
	Example: `  mio media playlists items add --playlist-id pl_abc123 --file-id file_xyz789
  mio media playlists items add --playlist-id pl_abc123 --file-id file_xyz789 --position 2`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "--playlist-id must not be empty")
		}
		fileID := flagValue(cmd, "file-id")
		if fileID == "" {
			return errs.New(errs.ExitUsage, "--file-id must not be empty")
		}
		if cmd.Flags().Changed("position") {
			if pos, _ := cmd.Flags().GetInt("position"); pos < 0 {
				return errs.New(errs.ExitUsage, "invalid --position: must be >= 0")
			}
		}
		attrs := map[string]any{"file_id": fileID}
		setIntFlag(cmd, attrs, "position")

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, playlistItemsPath(teamID, pid, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var mediaPlaylistsItemsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the items in a playlist.",
	Long:  "List the items of a playlist in order, cursor-paginated.",
	Example: `  mio media playlists items list --playlist-id pl_abc123
  mio media playlists items list --playlist-id pl_abc123 --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "--playlist-id must not be empty")
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		addPageFlags(cmd, query)
		col, err := c.client.List(c.ctx, playlistItemsPath(teamID, pid, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var mediaPlaylistsItemsRemoveCmd = &cobra.Command{
	Use:     "remove <item_id>",
	Short:   "Remove an item from a playlist.",
	Long:    "Remove an item from a playlist by its item id (the `id` from `items list`, not the file id). Pass --yes to skip the confirmation prompt.",
	Example: `  mio media playlists items remove it_abc123 --playlist-id pl_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "--playlist-id must not be empty")
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Remove item %s from the playlist?", args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, playlistItemsPath(teamID, pid, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed item %s from the playlist.\n", args[0])
		return nil
	},
}

var mediaPlaylistsItemsReorderCmd = &cobra.Command{
	Use:     "reorder <item_id>",
	Short:   "Change an item's position in a playlist.",
	Long:    "Move a single item to a new position in the playlist. The item is identified by its item id (the `id` from `items list`, not the file id); --position is required.",
	Example: `  mio media playlists items reorder it_abc123 --playlist-id pl_abc123 --position 3`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "--playlist-id must not be empty")
		}
		pos, _ := cmd.Flags().GetInt("position")
		if pos < 0 {
			return errs.New(errs.ExitUsage, "invalid --position: must be >= 0")
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.UpdateWithID(c.ctx, playlistItemsPath(teamID, pid, args[0]), args[0], map[string]any{"position": pos})
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}
