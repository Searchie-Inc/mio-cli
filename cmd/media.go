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
//	cards get GET   /api/teams/{team_id}/files/{id}/cards
//	cards set PUT   /api/teams/{team_id}/files/{id}/cards     (type file_cards)
//	chapters get GET /api/teams/{team_id}/files/{id}/chapters
//	chapters set PUT /api/teams/{team_id}/files/{id}/chapters (type file_chapters)
//
// folders (team-scoped admin):
//
//	list     GET    /api/teams/{team_id}/folders
//	create   POST   /api/teams/{team_id}/folders
//	retrieve GET    /api/teams/{team_id}/folders/{id}
//	update   PATCH  /api/teams/{team_id}/folders/{id}
//	delete   DELETE /api/teams/{team_id}/folders/{id}
//	move     POST   /api/teams/{team_id}/folders/{id}/move    (type folders)
//
// search (team-scoped admin):
//
//	search   GET    /api/teams/{team_id}/search/media?q=…
//
// hub-media (hub-scoped admin — standalone files):
//
//	publish   POST   /api/teams/{team_id}/hubs/{hub_id}/media  (type hub_media)
//	list      GET    /api/teams/{team_id}/hubs/{hub_id}/media
//	unpublish DELETE /api/teams/{team_id}/hubs/{hub_id}/media/{file_id}
//
// playlists (team-scoped admin):
//
//	list     GET    /api/teams/{team_id}/playlists
//	create   POST   /api/teams/{team_id}/playlists
//	retrieve GET    /api/teams/{team_id}/playlists/{id}
//	update   PATCH  /api/teams/{team_id}/playlists/{id}
//	delete   DELETE /api/teams/{team_id}/playlists/{id}
//
// files also supports the full ingest lifecycle end-to-end from the CLI (see
// cmd/media_upload.go): `upload` orchestrates create → presigned S3 PUT →
// finalize (auto-multipart for large files), `replace` swaps an existing file's
// asset, `finalize`/`transcode` re-drive processing, and `register-synthetic`
// registers a synthetic (document/pdf) file. No dashboard/API detour is needed.
//
// All routes are team-scoped. Requires a team-member user JWT.

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// media files <action>
	mediaFilesCardsCmd.AddCommand(mediaFilesCardsGetCmd, mediaFilesCardsSetCmd)
	mediaFilesChaptersCmd.AddCommand(mediaFilesChaptersGetCmd, mediaFilesChaptersSetCmd)
	mediaFilesCmd.AddCommand(
		mediaFilesListCmd,
		mediaFilesRetrieveCmd,
		mediaFilesDurableURLCmd,
		mediaFilesUpdateCmd,
		mediaFilesDeleteCmd,
		mediaFilesCardsCmd,
		mediaFilesChaptersCmd,
	)
	mediaCmd.AddCommand(mediaFilesCmd)

	mediaFilesCardsSetCmd.Flags().String("cards", "", "JSON array of cards (or @file.json). Each: {label,start,url?,description?,id?}. Required.")
	mediaFilesChaptersSetCmd.Flags().String("chapters", "", "JSON array of chapters (or @file.json). Each: {title,start,id?}. Required.")

	mediaFilesDurableURLCmd.Flags().String("preset", "", "Image variant preset to emit (e.g. thumbnail-160, medium-720, large-1440, webp-medium). Omit to print every preset.")
	mediaFilesDurableURLCmd.Flags().Bool("publish", false, "Also publish the file to the --hub (visibility public, published now) so the URL resolves for anonymous visitors.")
	mediaFilesDurableURLCmd.Flags().String("visibility", "public", "Visibility used when --publish is set: members, private, or public (default public).")

	// media folders <action>
	mediaFoldersCmd.AddCommand(
		mediaFoldersListCmd,
		mediaFoldersCreateCmd,
		mediaFoldersRetrieveCmd,
		mediaFoldersUpdateCmd,
		mediaFoldersDeleteCmd,
		mediaFoldersMoveCmd,
	)
	mediaCmd.AddCommand(mediaFoldersCmd)

	mediaFoldersMoveCmd.Flags().String("parent-id", "", "Target parent folder id to move under.")
	mediaFoldersMoveCmd.Flags().Bool("to-root", false, "Move the folder to the library root (no parent).")

	// media search
	mediaCmd.AddCommand(mediaSearchCmd)
	mediaSearchCmd.Flags().String("query", "", "Search query string. Required.")
	mediaSearchCmd.Flags().String("hub-id", "", "Optional hub id to scope the search.")
	mediaSearchCmd.Flags().Int("limit", 0, "Max results (page[size], 1-100).")

	// media hub-media <action>  (standalone files — MIO-2266)
	mediaHubMediaCmd.AddCommand(
		mediaHubMediaPublishCmd,
		mediaHubMediaListCmd,
		mediaHubMediaUnpublishCmd,
	)
	mediaCmd.AddCommand(mediaHubMediaCmd)

	mediaHubMediaPublishCmd.Flags().String("file-id", "", "File id to publish to the hub. Required.")
	mediaHubMediaPublishCmd.Flags().String("visibility", "", "Per-hub visibility: members (default), private, or public.")
	mediaHubMediaPublishCmd.Flags().String("published-at", "", "RFC3339 publish timestamp (default: now).")
	mediaHubMediaPublishCmd.Flags().Int("position", 0, "Manual ordering within the hub (>= 0).")
	addPaginationFlags(mediaHubMediaListCmd)

	// media playlists <action>
	mediaPlaylistsCmd.AddCommand(
		mediaPlaylistsListCmd,
		mediaPlaylistsCreateCmd,
		mediaPlaylistsRetrieveCmd,
		mediaPlaylistsUpdateCmd,
		mediaPlaylistsDeleteCmd,
	)
	mediaCmd.AddCommand(mediaPlaylistsCmd)

	// media hub-playlists <action>  (MIO-2259)
	mediaHubPlaylistsCmd.AddCommand(
		mediaHubPlaylistsPublishCmd,
		mediaHubPlaylistsListCmd,
		mediaHubPlaylistsUnpublishCmd,
	)
	mediaCmd.AddCommand(mediaHubPlaylistsCmd)

	mediaHubPlaylistsPublishCmd.Flags().String("playlist-id", "", "Playlist id to publish to the hub. Required.")
	mediaHubPlaylistsPublishCmd.Flags().String("visibility", "", "Per-hub visibility: members (default), private, or public.")
	mediaHubPlaylistsPublishCmd.Flags().String("published-at", "", "RFC3339 publish timestamp (default: now).")
	mediaHubPlaylistsPublishCmd.Flags().Int("position", 0, "Manual ordering within the hub (>= 0).")
	addPaginationFlags(mediaHubPlaylistsListCmd)

	rootCmd.AddCommand(mediaCmd)
}

// ---- media group ------------------------------------------------------------

var mediaCmd = &cobra.Command{
	Use:   "media",
	Short: "Manage media assets.",
	Long: `Manage media files, folders, playlists, and transcripts for the active team.

Files: full library management plus the complete upload lifecycle — list,
  retrieve, update, delete, and ingest with 'files upload' (create → presigned
  S3 PUT → finalize, all from the CLI; large files chunk automatically), plus
  'files replace', 'files finalize', 'files transcode', and
  'files register-synthetic'. See 'mio media files --help'.
Folders: full CRUD (create/list/retrieve/update/delete) plus 'move'.
Search: 'media search' runs hybrid search over the team's transcripts.
Playlists: full CRUD, 'set-cover', and 'playlists items' to curate contents.
Transcripts: get/vtt/content/versions and edit/revert authored transcripts.
Attachments: inspect and manage media attachment rows ('media attachments').
Publishing: 'media hub-media' / 'media hub-playlists' publish to a hub.`,
	Example: `  mio media files upload ./clip.mp4 --title "Intro"
  mio media files list
  mio media folders list
  mio media playlists list`,
}

// ---- media hub-playlists sub-resource (MIO-2259) ----------------------------
//
// Publish a media playlist onto a hub (writes a hub_media row — the record the
// /content browse grid and homepage content-grid join on). Hub-scoped.
//
//	publish   POST   /api/teams/{team_id}/hubs/{hub_id}/playlists
//	list      GET    /api/teams/{team_id}/hubs/{hub_id}/playlists
//	unpublish DELETE /api/teams/{team_id}/hubs/{hub_id}/playlists/{playlist_id}
//
// The write derives JSON:API type "hub_media" (not "playlists") via the
// hubs/playlists typeOverride in internal/client.

var hubMediaVisibility = map[string]bool{"members": true, "private": true, "public": true}

// mediaHubContext resolves team + hub for hub-scoped media commands.
func mediaHubContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
	c, teamID, err := mediaContext(cmd)
	if err != nil {
		return nil, "", "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", "", err
	}
	return c, teamID, hubID, nil
}

// hubPlaylistsPath returns /api/teams/{team}/hubs/{hub}/playlists[/{playlist_id}].
func hubPlaylistsPath(teamID, hubID, playlistID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/playlists", teamID, hubID)
	if playlistID != "" {
		return base + "/" + playlistID
	}
	return base
}

// applyHubMediaOptions parses the optional hub_media publish flags
// (--visibility, --published-at, --position) into attrs, validating each BEFORE
// any HTTP request so a bad flag fires no request. Shared by the hub-playlists
// and hub-media (standalone file) publish commands.
func applyHubMediaOptions(cmd *cobra.Command, attrs map[string]any) error {
	if cmd.Flags().Changed("visibility") {
		v, err := cmd.Flags().GetString("visibility")
		if err != nil {
			return errs.New(errs.ExitUsage, "--visibility: %s", err)
		}
		if !hubMediaVisibility[v] {
			return errs.New(errs.ExitUsage, "invalid --visibility %q: must be members, private, or public", v)
		}
		attrs["visibility"] = v
	}
	if cmd.Flags().Changed("published-at") {
		pa, err := cmd.Flags().GetString("published-at")
		if err != nil {
			return errs.New(errs.ExitUsage, "--published-at: %s", err)
		}
		if _, perr := time.Parse(time.RFC3339, pa); perr != nil {
			return errs.New(errs.ExitUsage, "invalid --published-at %q: must be RFC3339 (e.g. 2026-01-02T15:04:05Z)", pa)
		}
		attrs["published_at"] = pa
	}
	if cmd.Flags().Changed("position") {
		pos, err := cmd.Flags().GetInt("position")
		if err != nil {
			return errs.New(errs.ExitUsage, "--position: %s", err)
		}
		if pos < 0 {
			return errs.New(errs.ExitUsage, "invalid --position %d: must be >= 0", pos)
		}
		attrs["position"] = pos
	}
	return nil
}

var mediaHubPlaylistsCmd = &cobra.Command{
	Use:   "hub-playlists",
	Short: "Publish playlists to a hub.",
	Long: `Publish, list, and unpublish media playlists on the active hub.

Publishing a playlist writes a hub_media row — the record that surfaces the
playlist on the hub's /content browse grid and the homepage content-grid.`,
	Example: `  mio media hub-playlists publish --hub hub_123 --playlist-id pl_abc
  mio media hub-playlists list --hub hub_123`,
}

var mediaHubPlaylistsPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish a playlist to the active hub.",
	Long:  "Publish a media playlist to the active hub, creating (or updating) its hub_media row.",
	Example: `  mio media hub-playlists publish --hub hub_123 --playlist-id pl_abc
  mio media hub-playlists publish --hub hub_123 --playlist-id pl_abc --visibility public --position 0`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate before resolving auth/team/hub so a bad flag fires no request.
		// flagValue trims, so --playlist-id "" / --playlist-id=" " are rejected too.
		pid := flagValue(cmd, "playlist-id")
		if pid == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --playlist-id")
		}
		attrs := map[string]any{"playlist_id": pid}
		if err := applyHubMediaOptions(cmd, attrs); err != nil {
			return err
		}

		c, teamID, hubID, err := mediaHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, hubPlaylistsPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var mediaHubPlaylistsListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List playlists published to the active hub.",
	Long:    "List the media playlists published to the active hub (hub_media rows).",
	Example: `  mio media hub-playlists list --hub hub_123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := mediaHubContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		addPageFlags(cmd, query)
		col, err := c.client.List(c.ctx, hubPlaylistsPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var mediaHubPlaylistsUnpublishCmd = &cobra.Command{
	Use:     "unpublish <playlist_id>",
	Short:   "Unpublish a playlist from the active hub.",
	Long:    "Remove a playlist's hub_media row from the active hub. The playlist itself is not deleted. Pass --yes to skip the confirmation prompt.",
	Example: `  mio media hub-playlists unpublish pl_abc --hub hub_123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := mediaHubContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Unpublish playlist %s from the hub?", args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, hubPlaylistsPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Unpublished playlist %s from the hub.\n", args[0])
		return nil
	},
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
	Long: `Manage files in the team media library — and ingest new ones from the CLI.

Library:  list, retrieve, update, delete.
Ingest:   upload (create → presigned S3 PUT → finalize, auto-multipart for large
          files), replace (swap an existing file's asset), finalize/transcode
          (re-drive processing), register-synthetic (register a document/pdf).
Enrich:   cards (in-video CTAs) and chapters, each get/set.`,
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

// ---- files durable-url ------------------------------------------------------

// mediaFilesDurableURLCmd prints a file's durable (non-expiring) image URL(s),
// each joined with the REQUIRED ?hub_id= param and ready to inline into a
// page-tree image node. Unlike the imgproxy-signed `variants` (baked-in exp,
// rot in ~24-48h), durable_variants are absolute + unsigned and safe to persist.
// The URL only resolves once the file is PUBLISHED public to the hub (--publish).
// Image-only: durable_variants is {} for non-image files.
var mediaFilesDurableURLCmd = &cobra.Command{
	Use:   "durable-url <file_id>",
	Short: "Print a file's durable (non-expiring) hub-scoped image URL(s).",
	Long: `Print a file's durable image URL(s) — safe to inline into a page-tree image
node because they never expire (unlike the imgproxy-signed "variants", which rot
in ~24-48h).

Each URL is the file's durable_variants entry joined with the required
"?hub_id=" param, so it resolves for the active --hub. The URL 404s until the
file is PUBLISHED public to that hub: pass --publish to do it here (visibility
public, published now), or run beforehand:
  mio media hub-media publish --hub <hub> --file-id <file_id> --visibility public

Durable URLs are image-only; a non-image file has no durable variants.`,
	Example: `  mio media files durable-url file_abc --hub hub_123 --preset medium-720
  mio media files durable-url file_abc --hub hub_123 --publish`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fileID := args[0]

		// Validate --visibility before any request (mirrors applyHubMediaOptions).
		visibility := "public"
		if cmd.Flags().Changed("visibility") {
			v, _ := cmd.Flags().GetString("visibility")
			if !hubMediaVisibility[v] {
				return errs.New(errs.ExitUsage, "invalid --visibility %q: must be members, private, or public", v)
			}
			visibility = v
		}

		c, teamID, hubID, err := mediaHubContext(cmd)
		if err != nil {
			return err
		}

		// Read the file first so a non-image (no durable variants) fails BEFORE
		// we publish anything.
		res, err := c.client.Retrieve(c.ctx, filesPath(teamID, fileID))
		if err != nil {
			return err
		}
		variants, err := durableVariants(fileID, res.Attributes)
		if err != nil {
			return err
		}
		urls, err := buildDurableURLs(variants, hubID, flagValue(cmd, "preset"))
		if err != nil {
			return err
		}

		// Publish only after we know it's a real image with the requested preset,
		// so the URL actually resolves for anonymous visitors.
		if pub, _ := cmd.Flags().GetBool("publish"); pub {
			pubAttrs := map[string]any{
				"file_id":      fileID,
				"visibility":   visibility,
				"published_at": time.Now().UTC().Format(time.RFC3339),
			}
			if _, err := c.client.Create(c.ctx, hubMediaPath(teamID, hubID, ""), pubAttrs); err != nil {
				return err
			}
		}

		return c.render(cmd, urls)
	},
}

// durableVariants extracts a file's durable_variants map (preset -> URL),
// erroring clearly for non-image files (durable_variants is {} / absent there).
func durableVariants(fileID string, attrs map[string]any) (map[string]string, error) {
	raw, _ := attrs["durable_variants"].(map[string]any)
	if len(raw) == 0 {
		return nil, errs.New(errs.ExitUsage,
			"file %q has no durable image variants — durable URLs are image-only", fileID)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

// buildDurableURLs appends the REQUIRED ?hub_id= to each durable variant URL
// (the bare durable_variants URL 404s for everyone). When preset is non-empty,
// only that preset is returned; an unknown preset is a usage error.
func buildDurableURLs(variants map[string]string, hubID, preset string) (map[string]any, error) {
	withHub := func(u string) string {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		return u + sep + "hub_id=" + url.QueryEscape(hubID)
	}
	if preset != "" {
		base, ok := variants[preset]
		if !ok {
			keys := make([]string, 0, len(variants))
			for k := range variants {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return nil, errs.New(errs.ExitUsage,
				"unknown --preset %q; available presets: %s", preset, strings.Join(keys, ", "))
		}
		return map[string]any{preset: withHub(base)}, nil
	}
	out := make(map[string]any, len(variants))
	for k, u := range variants {
		out[k] = withHub(u)
	}
	return out, nil
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

		res, err := c.client.UpdateWithID(c.ctx, filesPath(teamID, args[0]), args[0], attrs)
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

		if _, ok := attrs["name"]; !ok {
			return errs.New(errs.ExitUsage, "--name is required to create a folder")
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
	Use:     "update <id>",
	Short:   "Update (rename) a media folder by id.",
	Long:    "Rename a folder. To move a folder to a new parent use the API's POST /{id}/move endpoint directly.",
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
  mio media playlists create --title "Course Videos" --description "All course material" --visibility public --hub-id hub_abc123`,
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

		if _, ok := attrs["title"]; !ok {
			return errs.New(errs.ExitUsage, "--title is required to create a playlist")
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

// ======================================================================
// media files cards / chapters (MIO-2266)
//
// In-video CTA cards and authorable chapters are full-list PUT replaces that
// return a JSON:API collection. `set` derives the JSON:API type "file_cards" /
// "file_chapters" from the .../files/{id}/cards|chapters tail via typeOverrides.
//
//	cards    get GET  /api/teams/{team}/files/{id}/cards
//	         set PUT  /api/teams/{team}/files/{id}/cards      (type file_cards)
//	chapters get GET  /api/teams/{team}/files/{id}/chapters
//	         set PUT  /api/teams/{team}/files/{id}/chapters   (type file_chapters)
// ======================================================================

// fileCardsPath returns /api/teams/{team}/files/{id}/cards.
func fileCardsPath(teamID, fileID string) string {
	return fmt.Sprintf("/api/teams/%s/files/%s/cards", teamID, fileID)
}

// fileChaptersPath returns /api/teams/{team}/files/{id}/chapters.
func fileChaptersPath(teamID, fileID string) string {
	return fmt.Sprintf("/api/teams/%s/files/%s/chapters", teamID, fileID)
}

// parseJSONArrayFlag parses a REQUIRED string flag whose value is a JSON array
// (or @file.json). It validates client-side — an unset/empty flag, malformed
// JSON, or a non-array value is a usage error that fires no HTTP request. The
// per-item shape is validated server-side (the backend forbids unknown keys).
func parseJSONArrayFlag(cmd *cobra.Command, name string) ([]any, error) {
	if !cmd.Flags().Changed(name) {
		return nil, errs.New(errs.ExitUsage, "missing required flag: --%s", name)
	}
	raw, err := cmd.Flags().GetString(name)
	if err != nil {
		return nil, errs.New(errs.ExitUsage, "--%s: %s", name, err)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errs.New(errs.ExitUsage, "--%s must not be empty (use '[]' to clear)", name)
	}
	v, perr := parseJSONFlag(raw)
	if perr != nil {
		return nil, errs.New(errs.ExitUsage, "--%s must be valid JSON array or @file: %s", name, perr)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, errs.New(errs.ExitUsage, "--%s must be a JSON array", name)
	}
	return arr, nil
}

var mediaFilesCardsCmd = &cobra.Command{
	Use:   "cards",
	Short: "Manage a file's in-video CTA cards.",
	Long:  "Get or replace the in-video CTA cards shown on a media file during playback.",
}

var mediaFilesCardsGetCmd = &cobra.Command{
	Use:     "get <file_id>",
	Short:   "List a file's in-video CTA cards.",
	Long:    "List the active in-video CTA cards for a team-owned file, ordered by start time.",
	Example: `  mio media files cards get file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		col, err := c.client.List(c.ctx, fileCardsPath(teamID, args[0]), nil)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var mediaFilesCardsSetCmd = &cobra.Command{
	Use:   "set <file_id>",
	Short: "Replace a file's in-video CTA cards (full-list PUT).",
	Long: `Atomically replace the full list of in-video CTA cards for a file.

--cards takes a JSON array (or @file.json). Each card is an object:
  {"label":"Buy now","start":15000,"url":"https://…","description":"…"}
label and start (milliseconds from the video start) are required; url and
description are optional. Include an existing card's "id" to preserve it; omit
it to create a new card. Pass --cards '[]' to remove all cards.`,
	Example: `  mio media files cards set file_abc123 --cards '[{"label":"Buy","start":15000,"url":"https://x.co"}]'
  mio media files cards set file_abc123 --cards @cards.json
  mio media files cards set file_abc123 --cards '[]'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate the payload before resolving auth/team so a bad flag fires no request.
		cards, err := parseJSONArrayFlag(cmd, "cards")
		if err != nil {
			return err
		}
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		col, err := c.client.ActionCollection(c.ctx, http.MethodPut,
			fileCardsPath(teamID, args[0]), map[string]any{"cards": cards})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var mediaFilesChaptersCmd = &cobra.Command{
	Use:   "chapters",
	Short: "Manage a file's authored chapters.",
	Long:  "Get the effective chapters (authored, else auto) or replace the authored chapter list for a media file.",
}

var mediaFilesChaptersGetCmd = &cobra.Command{
	Use:     "get <file_id>",
	Short:   "List a file's effective chapters.",
	Long:    "List the effective chapters (authored when present, else auto-generated) for a team-owned file.",
	Example: `  mio media files chapters get file_abc123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		col, err := c.client.List(c.ctx, fileChaptersPath(teamID, args[0]), nil)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var mediaFilesChaptersSetCmd = &cobra.Command{
	Use:   "set <file_id>",
	Short: "Replace a file's authored chapters (full-list PUT).",
	Long: `Atomically replace the authored chapter list for a file.

--chapters takes a JSON array (or @file.json). Each chapter is an object:
  {"title":"Intro","start":0}
title and start (milliseconds from the video start) are required; start values
must be unique. Include an existing chapter's "id" to preserve it. Pass
--chapters '[]' to clear the authored list (auto chapters then apply).`,
	Example: `  mio media files chapters set file_abc123 --chapters '[{"title":"Intro","start":0},{"title":"Demo","start":60000}]'
  mio media files chapters set file_abc123 --chapters @chapters.json
  mio media files chapters set file_abc123 --chapters '[]'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate the payload before resolving auth/team so a bad flag fires no request.
		chapters, err := parseJSONArrayFlag(cmd, "chapters")
		if err != nil {
			return err
		}
		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		col, err := c.client.ActionCollection(c.ctx, http.MethodPut,
			fileChaptersPath(teamID, args[0]), map[string]any{"chapters": chapters})
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ======================================================================
// media folders move (MIO-2266)
//
//	move POST /api/teams/{team}/folders/{id}/move   (type folders)
//
// The body is {new_parent_id: <id>|null}; the write derives the JSON:API type
// "folders" (NOT "move") via the folders/move typeOverride.
// ======================================================================

// foldersMovePath returns /api/teams/{team}/folders/{id}/move.
func foldersMovePath(teamID, folderID string) string {
	return fmt.Sprintf("/api/teams/%s/folders/%s/move", teamID, folderID)
}

var mediaFoldersMoveCmd = &cobra.Command{
	Use:   "move <folder_id>",
	Short: "Move a folder (and its subtree) to a new parent.",
	Long: `Move a folder and its entire subtree under a new parent folder, or to the
library root with --to-root.

Exactly one of --parent-id or --to-root is required. The move fails (422) if it
would create a cycle, and is not supported for subtrees larger than 1000 folders.`,
	Example: `  mio media folders move folder_abc123 --parent-id folder_xyz789
  mio media folders move folder_abc123 --to-root`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate the target selection before resolving auth so a bad flag fires no request.
		toRoot, _ := cmd.Flags().GetBool("to-root")
		parentID := flagValue(cmd, "parent-id")
		switch {
		case toRoot && parentID != "":
			return errs.New(errs.ExitUsage, "pass either --parent-id or --to-root, not both")
		case !toRoot && parentID == "":
			return errs.New(errs.ExitUsage, "missing move target: pass --parent-id <id> or --to-root")
		}
		// new_parent_id is required by the backend but nullable: null moves to root.
		var newParent any
		if !toRoot {
			newParent = parentID
		}
		attrs := map[string]any{"new_parent_id": newParent}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Action(c.ctx, http.MethodPost, foldersMovePath(teamID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ======================================================================
// media search (MIO-2266)
//
//	search GET /api/teams/{team}/search/media?q=…&hub_id=…&page[size]=…
//
// Hybrid (RRF) search over a team's media transcripts. Team-admin scope; uses
// top-N pagination (page[size] only — no cursor).
// ======================================================================

// searchMediaPath returns /api/teams/{team}/search/media.
func searchMediaPath(teamID string) string {
	return fmt.Sprintf("/api/teams/%s/search/media", teamID)
}

var mediaSearchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search a team's media transcripts.",
	Long: `Hybrid (lexical + semantic RRF) search over the team's media transcripts.

Returns ranked snippets with timestamps and share URLs. Team-admin scope. Pass
--hub-id to restrict results to a single hub's published media.`,
	Example: `  mio media search --query "onboarding checklist"
  mio media search --query pricing --hub-id hub_123 --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate flags before resolving auth so a bad flag fires no request.
		q := flagValue(cmd, "query")
		if q == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --query")
		}
		pageSize := 0
		if cmd.Flags().Changed("limit") {
			limit, lerr := cmd.Flags().GetInt("limit")
			if lerr != nil {
				return errs.New(errs.ExitUsage, "--limit: %s", lerr)
			}
			// Backend caps page[size] at 1..100 (top-N pagination); reject
			// out-of-range client-side so it fires no request.
			if limit < 1 || limit > 100 {
				return errs.New(errs.ExitUsage, "invalid --limit %d: must be between 1 and 100", limit)
			}
			pageSize = limit
		}

		c, teamID, err := mediaContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		query.Set("q", q)
		if hubID := flagValue(cmd, "hub-id"); hubID != "" {
			query.Set("hub_id", hubID)
		}
		if pageSize > 0 {
			query.Set("page[size]", itoa(pageSize))
		}
		col, err := c.client.List(c.ctx, searchMediaPath(teamID), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ======================================================================
// media hub-media — publish standalone files to a hub (MIO-2266)
//
//	publish   POST   /api/teams/{team}/hubs/{hub}/media
//	list      GET    /api/teams/{team}/hubs/{hub}/media
//	unpublish DELETE /api/teams/{team}/hubs/{hub}/media/{file_id}
//
// Publishing a file writes a hub_media row (same join the /content grid and
// homepage content-grid read). The write derives JSON:API type "hub_media"
// (not "media") via the hubs/media typeOverride.
// ======================================================================

// hubMediaPath returns /api/teams/{team}/hubs/{hub}/media[/{file_id}].
func hubMediaPath(teamID, hubID, fileID string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/media", teamID, hubID)
	if fileID != "" {
		return base + "/" + fileID
	}
	return base
}

var mediaHubMediaCmd = &cobra.Command{
	Use:   "hub-media",
	Short: "Publish standalone files to a hub.",
	Long: `Publish, list, and unpublish standalone media files on the active hub.

Publishing a file writes a hub_media row — the record that surfaces the file on
the hub's /content browse grid and the homepage content-grid. (Use
'media hub-playlists' to publish a playlist instead.)`,
	Example: `  mio media hub-media publish --hub hub_123 --file-id file_abc
  mio media hub-media list --hub hub_123`,
}

var mediaHubMediaPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish a file to the active hub.",
	Long:  "Publish a standalone media file to the active hub, creating (or updating) its hub_media row.",
	Example: `  mio media hub-media publish --hub hub_123 --file-id file_abc
  mio media hub-media publish --hub hub_123 --file-id file_abc --visibility public --position 0`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Validate before resolving auth/team/hub so a bad flag fires no request.
		fid := flagValue(cmd, "file-id")
		if fid == "" {
			return errs.New(errs.ExitUsage, "missing required flag: --file-id")
		}
		attrs := map[string]any{"file_id": fid}
		if err := applyHubMediaOptions(cmd, attrs); err != nil {
			return err
		}

		c, teamID, hubID, err := mediaHubContext(cmd)
		if err != nil {
			return err
		}
		res, err := c.client.Create(c.ctx, hubMediaPath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var mediaHubMediaListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List files published to the active hub.",
	Long:    "List the standalone media files published to the active hub (hub_media rows), in all states (draft/scheduled/live).",
	Example: `  mio media hub-media list --hub hub_123`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := mediaHubContext(cmd)
		if err != nil {
			return err
		}
		query := url.Values{}
		addPageFlags(cmd, query)
		col, err := c.client.List(c.ctx, hubMediaPath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

var mediaHubMediaUnpublishCmd = &cobra.Command{
	Use:     "unpublish <file_id>",
	Short:   "Unpublish a file from the active hub.",
	Long:    "Remove a file's hub_media row from the active hub. The file itself is not deleted. Pass --yes to skip the confirmation prompt.",
	Example: `  mio media hub-media unpublish file_abc --hub hub_123 --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := mediaHubContext(cmd)
		if err != nil {
			return err
		}
		if err := confirmDestructive(cmd, fmt.Sprintf("Unpublish file %s from the hub?", args[0])); err != nil {
			return err
		}
		if err := c.client.Delete(c.ctx, hubMediaPath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Unpublished file %s from the hub.\n", args[0])
		return nil
	},
}
