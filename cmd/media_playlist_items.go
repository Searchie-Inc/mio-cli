package cmd

// media_playlist_items.go — `mio media playlists items {add,list,remove,reorder}`
// (MIO-2513).
//
// Item admin for a media playlist (backend app/media/router.py
// playlists_admin_router, guard _require_team_member; requires the media
// feature flag enabled):
//
//	add     POST   /api/teams/{team}/playlists/{playlist_id}/items            (type playlist_items)
//	list    GET    /api/teams/{team}/playlists/{playlist_id}/items            [?page[...]]
//	reorder PATCH  /api/teams/{team}/playlists/{playlist_id}/items/{item_id}  (type playlist_items; position)
//	remove  DELETE /api/teams/{team}/playlists/{playlist_id}/items/{item_id}  (204)
//
// Before this group, playlists could only be created as empty shells — the only
// way to populate one was a raw POST .../items. `add` closes that gap so a
// playlist can be populated end-to-end from the CLI.
//
// Identifier note: reorder/remove key on the playlist_item ROW id (the `id`
// from `items list`), NOT the file id. `reorder` repositions a single item —
// the backend has no bulk-order route for playlist items — so it PATCHes one
// item's position at a time.

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

	// --playlist-id identifies the parent playlist for every verb.
	for _, cmd := range []*cobra.Command{
		mediaPlaylistsItemsAddCmd,
		mediaPlaylistsItemsListCmd,
		mediaPlaylistsItemsRemoveCmd,
		mediaPlaylistsItemsReorderCmd,
	} {
		cmd.Flags().String("playlist-id", "", "Playlist id to operate on. Required.")
	}

	mediaPlaylistsItemsAddCmd.Flags().String("file-id", "", "Media file id to add to the playlist. Required.")
	mediaPlaylistsItemsAddCmd.Flags().Int("position", 0, "Optional 0-based position for the item; if omitted, the backend inserts it at position 0 (front).")

	mediaPlaylistsItemsReorderCmd.Flags().Int("position", 0, "New position (>= 0) for the item. Required.")

	addPaginationFlags(mediaPlaylistsItemsListCmd)
}

// playlistItemsPath returns
// /api/teams/{team}/playlists/{playlist_id}/items[/{item_id}].
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

// ---- items add --------------------------------------------------------------

var mediaPlaylistsItemsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a media file to a playlist.",
	Long:  "Add a media file to a playlist. Without --position the backend inserts it at position 0 (front); pass --position to place it explicitly. The media file must already exist in the team library.",
	Example: `  mio media playlists items add --playlist-id pl_abc123 --file-id file_xyz789
  mio media playlists items add --playlist-id pl_abc123 --file-id file_xyz789 --position 2`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate before resolving auth/team so a missing flag fires no request.
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --playlist-id")
		}
		fileID := flagValue(cmd, "file-id")
		if fileID == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --file-id")
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

// ---- items list -------------------------------------------------------------

var mediaPlaylistsItemsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the items in a playlist.",
	Long:  "List the items of a playlist in order, cursor-paginated.",
	Example: `  mio media playlists items list --playlist-id pl_abc123
  mio media playlists items list --playlist-id pl_abc123 --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate before resolving auth/team so a missing flag fires no request.
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --playlist-id")
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

// ---- items remove -----------------------------------------------------------

var mediaPlaylistsItemsRemoveCmd = &cobra.Command{
	Use:     "remove <item_id>",
	Short:   "Remove an item from a playlist.",
	Long:    "Remove an item from a playlist by its item id (the `id` from `items list`, not the file id). Pass --yes to skip the confirmation prompt.",
	Example: `  mio media playlists items remove it_abc123 --playlist-id pl_abc123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate before resolving auth/team so a missing flag fires no request.
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --playlist-id")
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

// ---- items reorder ----------------------------------------------------------

var mediaPlaylistsItemsReorderCmd = &cobra.Command{
	Use:     "reorder <item_id>",
	Short:   "Change an item's position in a playlist.",
	Long:    "Move a single item to a new position in the playlist. The item is identified by its item id (the `id` from `items list`, not the file id); --position is required.",
	Example: `  mio media playlists items reorder it_abc123 --playlist-id pl_abc123 --position 3`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate before resolving auth/team so a missing/no-op flag fires no request.
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --playlist-id")
		}
		// The backend treats an omitted position as a no-op, so require it.
		if !cmd.Flags().Changed("position") {
			return errs.New(errs.ExitUsage, "missing required flag: --position")
		}
		pos, _ := cmd.Flags().GetInt("position")
		if pos < 0 {
			return errs.New(errs.ExitUsage, "invalid --position: must be >= 0")
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		// PlaylistItemUpdateData requires data.id in the body (not just the URL).
		res, err := c.client.UpdateWithID(c.ctx, playlistItemsPath(teamID, pid, args[0]), args[0], map[string]any{"position": pos})
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}
