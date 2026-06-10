package cmd

// media.go — `mio media` command group.
//
// Routes (see backend app/media/router.py):
//
// files (team-scoped admin):
//
//	list     GET    /api/teams/{team_id}/files
//	retrieve GET    /api/teams/{team_id}/files/{id}
//	update   PATCH  /api/teams/{team_id}/files/{id}
//	delete   DELETE /api/teams/{team_id}/files/{id}
//
// folders (team-scoped admin):
//
//	list     GET    /api/teams/{team_id}/folders
//	create   POST   /api/teams/{team_id}/folders
//	retrieve GET    /api/teams/{team_id}/folders/{id}
//	update   PATCH  /api/teams/{team_id}/folders/{id}
//	delete   DELETE /api/teams/{team_id}/folders/{id}
//
// playlists (team-scoped admin):
//
//	list     GET    /api/teams/{team_id}/playlists
//	create   POST   /api/teams/{team_id}/playlists
//	retrieve GET    /api/teams/{team_id}/playlists/{id}
//	update   PATCH  /api/teams/{team_id}/playlists/{id}
//	delete   DELETE /api/teams/{team_id}/playlists/{id}
//
// NOTE: file creation (upload) requires a two-step flow (POST + presigned S3 PUT
// + POST /finalize) which cannot be done with a single CLI call today. The `files`
// sub-group therefore exposes list/retrieve/update/delete only. Use the API
// directly or the dashboard for file upload.
//
// All routes are team-scoped. Requires a team-member user JWT.

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// media files <action>
	mediaFilesCmd.AddCommand(
		mediaFilesListCmd,
		mediaFilesRetrieveCmd,
		mediaFilesUpdateCmd,
		mediaFilesDeleteCmd,
	)
	mediaCmd.AddCommand(mediaFilesCmd)

	// media folders <action>
	mediaFoldersCmd.AddCommand(
		mediaFoldersListCmd,
		mediaFoldersCreateCmd,
		mediaFoldersRetrieveCmd,
		mediaFoldersUpdateCmd,
		mediaFoldersDeleteCmd,
	)
	mediaCmd.AddCommand(mediaFoldersCmd)

	// media playlists <action>
	mediaPlaylistsCmd.AddCommand(
		mediaPlaylistsListCmd,
		mediaPlaylistsCreateCmd,
		mediaPlaylistsRetrieveCmd,
		mediaPlaylistsUpdateCmd,
		mediaPlaylistsDeleteCmd,
	)
	mediaCmd.AddCommand(mediaPlaylistsCmd)

	rootCmd.AddCommand(mediaCmd)
}

// ---- media group ------------------------------------------------------------

var mediaCmd = &cobra.Command{
	Use:   "media",
	Short: "Manage media assets.",
	Long: `Manage media files, folders, and playlists for the active team.

Files: list, retrieve, update and delete files in the team media library.
Folders: full CRUD for organising files into folders.
Playlists: full CRUD for curated media playlists.

Note: file upload requires a multi-step presigned URL flow; use the dashboard
or API directly for new file uploads.`,
	Example: `  mio media files list
  mio media folders list
  mio media playlists list`,
}

// mediaContext returns context + team id for media commands (team-scoped).
func mediaContext(cmd *cobra.Command) (*cmdContext, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, "", err
	}
	return c, teamID, nil
}

// ======================================================================
// media files
// ======================================================================

var mediaFilesCmd = &cobra.Command{
	Use:   "files",
	Short: "Manage media files.",
	Long:  "List, retrieve, update and delete files in the team media library.",
}

// filesPath returns /api/teams/{team_id}/files[/{id}].
func filesPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/files", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ---- files list -------------------------------------------------------------

var mediaFilesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List media files.",
	Long:  "List all non-deleted files for the active team, cursor-paginated.",
	Example: `  mio media files list
  mio media files list --limit 50 --after <cursor>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, filesPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- files retrieve ---------------------------------------------------------

var mediaFilesRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a media file by id.",
	Long:    "Retrieve a single media file by its id.",
	Example: `  mio media files retrieve file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, filesPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- files update -----------------------------------------------------------

var mediaFilesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a media file by id.",
	Long:  "Partially update a media file's metadata. Only the flags you provide are changed.",
	Example: `  mio media files update file_abc123 --title "New Title"
  mio media files update file_abc123 --description "Updated description" --visibility public`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "visibility")
		setStringFlag(cmd, attrs, "folder-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, filesPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- files delete -----------------------------------------------------------

var mediaFilesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a media file by id.",
	Long:  "Soft-delete a media file. Pass --yes to skip the confirmation prompt.",
	Example: `  mio media files delete file_abc123
  mio media files delete file_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete file %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, filesPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted file %s.\n", args[0])
		return nil
	},
}

// ---- files flag registration ------------------------------------------------

func init() {
	mediaFilesUpdateCmd.Flags().String("title", "", "File display title.")
	mediaFilesUpdateCmd.Flags().String("description", "", "File description.")
	mediaFilesUpdateCmd.Flags().String("visibility", "", "Visibility: public or private.")
	mediaFilesUpdateCmd.Flags().String("folder-id", "", "Folder id to move the file into.")

	addPaginationFlags(mediaFilesListCmd)
}

// ======================================================================
// media folders
// ======================================================================

var mediaFoldersCmd = &cobra.Command{
	Use:   "folders",
	Short: "Manage media folders.",
	Long:  "Create, list, retrieve, update and delete folders for organising media files.",
}

// foldersPath returns /api/teams/{team_id}/folders[/{id}].
func foldersPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/folders", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ---- folders list -----------------------------------------------------------

var mediaFoldersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List media folders.",
	Long:  "List all non-deleted folders for the active team, cursor-paginated.",
	Example: `  mio media folders list
  mio media folders list --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, foldersPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- folders create ---------------------------------------------------------

var mediaFoldersCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a media folder.",
	Long:  "Create a new folder, optionally nested under a parent folder.",
	Example: `  mio media folders create --name "Videos"
  mio media folders create --name "Q1 Campaign" --parent-id folder_abc123`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")
		setStringFlag(cmd, attrs, "parent-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --name")
		}

		res, err := c.client.Create(c.ctx, foldersPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- folders retrieve -------------------------------------------------------

var mediaFoldersRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a media folder by id.",
	Long:    "Retrieve a single media folder by its id.",
	Example: `  mio media folders retrieve folder_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, foldersPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- folders update ---------------------------------------------------------

var mediaFoldersUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update (rename) a media folder by id.",
	Long:  "Rename a folder. To move a folder to a new parent use the API's POST /{id}/move endpoint directly.",
	Example: `  mio media folders update folder_abc123 --name "Renamed Folder"`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "name")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least --name")
		}

		res, err := c.client.Update(c.ctx, foldersPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- folders delete ---------------------------------------------------------

var mediaFoldersDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a media folder by id.",
	Long:  "Soft-delete a folder. Only allowed when the folder is empty. Pass --yes to skip the confirmation prompt.",
	Example: `  mio media folders delete folder_abc123
  mio media folders delete folder_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete folder %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, foldersPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted folder %s.\n", args[0])
		return nil
	},
}

// ---- folders flag registration ----------------------------------------------

func init() {
	mediaFoldersCreateCmd.Flags().String("name", "", "Folder name.")
	mediaFoldersCreateCmd.Flags().String("parent-id", "", "Parent folder id (optional; creates a nested folder).")

	mediaFoldersUpdateCmd.Flags().String("name", "", "New folder name.")

	addPaginationFlags(mediaFoldersListCmd)
}

// ======================================================================
// media playlists
// ======================================================================

var mediaPlaylistsCmd = &cobra.Command{
	Use:   "playlists",
	Short: "Manage media playlists.",
	Long:  "Create, list, retrieve, update and delete curated media playlists.",
}

// playlistsPath returns /api/teams/{team_id}/playlists[/{id}].
func playlistsPath(teamID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/playlists", teamID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// ---- playlists list ---------------------------------------------------------

var mediaPlaylistsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List media playlists.",
	Long:  "List all non-deleted playlists for the active team, cursor-paginated.",
	Example: `  mio media playlists list
  mio media playlists list --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, playlistsPath(teamID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- playlists create -------------------------------------------------------

var mediaPlaylistsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a media playlist.",
	Long:  "Create a new media playlist for the active team.",
	Example: `  mio media playlists create --title "My Playlist"
  mio media playlists create --title "Course Videos" --description "All course material" --visibility public --hub hub_abc123`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "visibility")
		setStringFlag(cmd, attrs, "hub-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --title")
		}

		res, err := c.client.Create(c.ctx, playlistsPath(teamID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- playlists retrieve -----------------------------------------------------

var mediaPlaylistsRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a media playlist by id.",
	Long:    "Retrieve a single media playlist by its id.",
	Example: `  mio media playlists retrieve pl_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, playlistsPath(teamID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- playlists update -------------------------------------------------------

var mediaPlaylistsUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a media playlist by id.",
	Long:  "Partially update a playlist's metadata. Only the flags you provide are changed.",
	Example: `  mio media playlists update pl_abc123 --title "New Name"
  mio media playlists update pl_abc123 --visibility public --podcast-feed-enabled=true`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "visibility")
		setStringFlag(cmd, attrs, "hub-id")
		setBoolFlag(cmd, attrs, "podcast-feed-enabled")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, playlistsPath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- playlists delete -------------------------------------------------------

var mediaPlaylistsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a media playlist by id.",
	Long:  "Soft-delete a media playlist. Pass --yes to skip the confirmation prompt.",
	Example: `  mio media playlists delete pl_abc123
  mio media playlists delete pl_abc123 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete playlist %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, playlistsPath(teamID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted playlist %s.\n", args[0])
		return nil
	},
}

// ---- playlists flag registration --------------------------------------------

func init() {
	for _, cmd := range []*cobra.Command{mediaPlaylistsCreateCmd, mediaPlaylistsUpdateCmd} {
		cmd.Flags().String("title", "", "Playlist title.")
		cmd.Flags().String("description", "", "Playlist description.")
		cmd.Flags().String("visibility", "", "Visibility: public or private.")
		cmd.Flags().String("hub-id", "", "Hub id to associate the playlist with.")
	}

	mediaPlaylistsUpdateCmd.Flags().Bool("podcast-feed-enabled", false, "Whether to enable the podcast RSS feed for this playlist.")

	addPaginationFlags(mediaPlaylistsListCmd)
}
