package cmd

// content.go implements the `mio content` command group for managing content
// items nested under a hub. Every sub-command is hub-scoped: both {team_id}
// and {hub_id} must be resolved from context (or supplied via --team/--hub).
//
// Routes (see docs/internal/api-surface.md "content"):
//
//	create   POST   /api/teams/{team_id}/hubs/{hub_id}/content
//	list     GET    /api/teams/{team_id}/hubs/{hub_id}/content
//	retrieve GET    /api/teams/{team_id}/hubs/{hub_id}/content/{id}
//	children GET    /api/teams/{team_id}/hubs/{hub_id}/content/{id}/children
//	update   PATCH  /api/teams/{team_id}/hubs/{hub_id}/content/{id}
//	delete   DELETE /api/teams/{team_id}/hubs/{hub_id}/content/{id}
//	restore  POST   /api/teams/{team_id}/hubs/{hub_id}/content/{id}/restore
//	reorder  POST   /api/teams/{team_id}/hubs/{hub_id}/content/reorder
//	reconcile POST  /api/teams/{team_id}/hubs/{hub_id}/content/reconcile

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func init() {
	// content <action>
	contentCmd.AddCommand(
		contentCreateCmd,
		contentListCmd,
		contentRetrieveCmd,
		contentChildrenCmd,
		contentUpdateCmd,
		contentDeleteCmd,
		contentRestoreCmd,
		contentReorderCmd,
		contentReconcileCmd,
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(contentCmd)
}

// ---- content group ----------------------------------------------------------

var contentCmd = &cobra.Command{
	Use:   "content",
	Short: "Manage hub content items.",
	Long:  "Create, list, retrieve, update, delete, restore, reorder, and reconcile content items within a hub.",
}

// contentBasePath returns /api/teams/{team_id}/hubs/{hub_id}/content[/{id}].
func contentBasePath(teamID, hubID, id string) string {
	base := fmt.Sprintf("/api/teams/%s/hubs/%s/content", teamID, hubID)
	if id != "" {
		return base + "/" + id
	}
	return base
}

// contentContext is the shared boilerplate for content sub-commands: builds the
// context, requires auth, and resolves both team id and hub id.
func contentContext(cmd *cobra.Command) (*cmdContext, string, string, error) {
	c, err := newContext(cmd)
	if err != nil {
		return nil, "", "", err
	}
	if err := c.requireAuth(); err != nil {
		return nil, "", "", err
	}
	teamID, err := c.requireTeam()
	if err != nil {
		return nil, "", "", err
	}
	hubID, err := c.requireHub()
	if err != nil {
		return nil, "", "", err
	}
	return c, teamID, hubID, nil
}

// ---- create -----------------------------------------------------------------

var contentCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a content item in a hub.",
	Long: `Create a new content item under the active hub.

--title and --node-type are required. Allowed values for --node-type:
  container  — a folder or module that holds other items
  lesson     — a leaf content item (video, audio, pdf, text, etc.)

--content-type is an optional sub-type for leaf items (e.g. video, audio, pdf, text).

Link an already-uploaded media asset with EITHER --file-id or --media-id
(mutually exclusive):

  --file-id    the FILE id, straight from 'mio media files upload' or
               'mio media files list'. Its media_id is resolved for you.
               PREFER THIS.
  --media-id   the Media PK, from a file's .media_id attribute. The backend
               does NOT validate this value, so a file id passed here is
               stored verbatim and yields a lesson pointing at nothing.

A file that lives only in a media playlist has no content item at all. To give
a whole hub's playlists one each, see 'mio content reconcile'.`,
	Example: `  mio content create --hub hub_abc --title "Module 1" --node-type container
  mio content create --hub hub_abc --title "Welcome Video" --node-type lesson --content-type video --parent-id cnt_xyz
  mio content create --hub hub_abc --title "Workshop Replay" --node-type lesson --content-type video --parent-id cnt_xyz --file-id file_abc123`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Flag-shape validation runs BEFORE contentContext so a contradictory
		// pair fires no request even when --hub is a name (see
		// validateContentMediaFlags).
		if err := validateContentMediaFlags(cmd); err != nil {
			return err
		}

		// Both --title and --node-type are required by the backend
		// ContentNodeCreateAttributes schema; validate client-side so a
		// partial-required body never reaches the API.
		var missing []string
		if !cmd.Flags().Changed("title") {
			missing = append(missing, "--title")
		}
		if !cmd.Flags().Changed("node-type") {
			missing = append(missing, "--node-type")
		}
		if len(missing) > 0 {
			return errs.New(errs.ExitUsage, "missing required flag(s): %s", strings.Join(missing, ", "))
		}

		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setMappedString(cmd, attrs, "node-type", "node_type")
		setStringFlag(cmd, attrs, "content-type")
		setStringFlag(cmd, attrs, "parent-id")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "privacy")
		setMappedString(cmd, attrs, "published-at", "published_at")
		if err := applyContentMediaFlags(cmd, c, teamID, attrs); err != nil {
			return err
		}

		res, err := c.client.Create(c.ctx, contentBasePath(teamID, hubID, ""), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- list -------------------------------------------------------------------

var contentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List root content items in a hub.",
	Long:  "List the top-level (root) content items for the active hub.",
	Example: `  mio content list --hub hub_abc
  mio content list --hub hub_abc --limit 50`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		col, err := c.client.List(c.ctx, contentBasePath(teamID, hubID, ""), query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- retrieve ---------------------------------------------------------------

var contentRetrieveCmd = &cobra.Command{
	Use:     "retrieve <id>",
	Short:   "Retrieve a content item by id.",
	Long:    "Fetch a single content item by its id from the active hub.",
	Example: `  mio content retrieve cnt_abc123 --hub hub_abc`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		res, err := c.client.Retrieve(c.ctx, contentBasePath(teamID, hubID, args[0]))
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- children ---------------------------------------------------------------

var contentChildrenCmd = &cobra.Command{
	Use:   "children <id>",
	Short: "List children of a content item.",
	Long:  "Fetch the direct children of a content item (e.g. videos inside a folder).",
	Example: `  mio content children cnt_folder123 --hub hub_abc
  mio content children cnt_folder123 --hub hub_abc --limit 25`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		query := url.Values{}
		addPageFlags(cmd, query)

		path := contentBasePath(teamID, hubID, args[0]) + "/children"
		col, err := c.client.List(c.ctx, path, query)
		if err != nil {
			return err
		}
		return c.render(cmd, col)
	},
}

// ---- update -----------------------------------------------------------------

var contentUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a content item by id.",
	Long: `Partially update a content item. Only the flags you supply are changed (PATCH semantics).

Media binding takes EITHER --file-id (preferred; its media_id is resolved for
you) OR --media-id. An EMPTY value for either is rejected rather than treated as
"unlink" — 'mio content update $ID --media-id "$MEDIA"' with $MEDIA unset would
otherwise destroy a working link and exit 0. To unlink deliberately, pass
--unset-media, which prompts before unlinking (or needs --yes when not on a
terminal) because the lesson stops playing.

Note: node_type and parent_id are immutable after create and cannot be changed via update.`,
	Example: `  mio content update cnt_abc123 --hub hub_abc --title "New Title"
  mio content update cnt_abc123 --hub hub_abc --content-type audio --privacy members`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateContentMediaFlags(cmd); err != nil {
			return err
		}

		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "content-type")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "privacy")
		setMappedString(cmd, attrs, "published-at", "published_at")
		if err := applyContentMediaFlags(cmd, c, teamID, attrs); err != nil {
			return err
		}

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to update: set at least one field flag")
		}

		res, err := c.client.Update(c.ctx, contentBasePath(teamID, hubID, args[0]), attrs)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

// ---- delete -----------------------------------------------------------------

var contentDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a content item by id.",
	Long:    "Permanently delete a content item from the active hub. Use --restore to undo.",
	Example: `  mio content delete cnt_abc123 --hub hub_abc --yes`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		if err := confirmDestructive(cmd, fmt.Sprintf("Delete content item %s?", args[0])); err != nil {
			return err
		}

		if err := c.client.Delete(c.ctx, contentBasePath(teamID, hubID, args[0])); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted content item %s.\n", args[0])
		return nil
	},
}

// ---- restore ----------------------------------------------------------------

var contentRestoreCmd = &cobra.Command{
	Use:     "restore <id>",
	Short:   "Restore a deleted content item.",
	Long:    "Restore a soft-deleted content item by id.",
	Example: `  mio content restore cnt_abc123 --hub hub_abc`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		path := contentBasePath(teamID, hubID, args[0]) + "/restore"
		res, err := c.client.Action(c.ctx, http.MethodPost, path, nil)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Restored content item %s.\n", args[0])
			return nil
		}
		return c.render(cmd, res)
	},
}

// applyContentMediaFlags resolves the content node's media binding for `create`
// and `update`.
//
// --media-id takes the Media PK directly. --file-id takes the FILE id — the id
// a creator actually holds after `mio media files upload` — and resolves it to
// the Media PK here (MIO-3074). That resolution matters more than convenience:
// the backend stores media_id verbatim WITHOUT validating it (MIO-3432), so a
// file id passed to --media-id yields a lesson silently pointing at nothing
// rather than an error. The two flags are mutually exclusive.
// validateContentMediaFlags checks the --media-id/--file-id pairing with NO
// network access, so it can run BEFORE contentContext.
//
// That ordering is the whole point. contentContext resolves the team and hub,
// and requireTeam/requireHub LIST over HTTP whenever either was given as a name
// or slug rather than an id (internal/client/resolve.go — an id-shaped value
// short-circuits, a name does not). Validating after it means a user who
// addresses their hub by name pays a round trip before being told their flags
// contradict each other. `media playlists set-cover` already establishes the
// rule: "Validate before resolving auth/team so a bad flag fires no request."
func validateContentMediaFlags(cmd *cobra.Command) error {
	hasMedia := cmd.Flags().Changed("media-id")
	hasFile := cmd.Flags().Changed("file-id")

	if hasMedia && hasFile {
		return errs.New(errs.ExitUsage,
			"--media-id and --file-id are mutually exclusive: pass the media PK or the file id, not both")
	}
	if hasFile && flagValue(cmd, "file-id") == "" {
		return errs.New(errs.ExitUsage, "--file-id was set but is empty")
	}
	// --media-id needs the same guard, and for a sharper reason: setStringFlag
	// neither trims nor rejects empty, and the backend stores media_id WITHOUT
	// validating it (MIO-3432) — so `--media-id ""` would create a lesson
	// pointing at nothing, with a 201 and no error.
	//
	// This applies to UPDATE too, and that is a deliberate reversal. Treating an
	// empty value as "clear the link" reads well until you write the shell that
	// most people write:
	//
	//	mio content update $ID --media-id "$MEDIA"    # $MEDIA unset upstream
	//
	// cobra sees the flag as Changed with an empty value, so a Changed-based
	// guard does not catch it, and a silent clear DESTROYS a working link while
	// exiting 0. An empty value is far more often a broken variable than an
	// intent to unlink, so it must fail loudly. Clearing is available, but only
	// through a flag a variable cannot accidentally become: --unset-media.
	if hasMedia && flagValue(cmd, "media-id") == "" {
		return errs.New(errs.ExitUsage,
			"--media-id was set but is empty (pass a media PK, use --file-id to resolve one from a file id, or --unset-media to unlink)")
	}
	if cmd.Flags().Changed("unset-media") {
		if hasMedia || hasFile {
			return errs.New(errs.ExitUsage,
				"--unset-media cannot be combined with --media-id or --file-id: unlink or relink, not both")
		}
	}
	return nil
}

func applyContentMediaFlags(cmd *cobra.Command, c *cmdContext, teamID string, attrs map[string]any) error {
	// Explicit unlink. JSON null is what the backend's `media_id: str | None`
	// clears on under exclude_unset semantics; "" would store an empty string.
	// This is a boolean precisely so no shell variable can expand into it.
	//
	// It is ALSO gated by confirmDestructive, the same bar `content delete` uses
	// two commands over: a live lesson stops playing the moment its media is
	// unlinked, and a non-interactive shell must pass --yes to do that. Being
	// un-typo-able is not the same as being safe to run unattended in a loop.
	if cmd.Flags().Changed("unset-media") {
		if unset, _ := cmd.Flags().GetBool("unset-media"); unset {
			if err := confirmDestructive(cmd, "Unlink this content item's media? The lesson will stop playing"); err != nil {
				return err
			}
			attrs["media_id"] = nil
			return nil
		}
	}
	if cmd.Flags().Changed("media-id") {
		setStringFlag(cmd, attrs, "media-id")
		return nil
	}
	if !cmd.Flags().Changed("file-id") {
		return nil
	}

	// Shape already validated by validateContentMediaFlags before any request.
	fileID := flagValue(cmd, "file-id")
	mediaID, err := resolveFileMediaID(c, teamID, fileID, "this content item's media")
	if err != nil {
		return err
	}
	attrs["media_id"] = mediaID
	return nil
}

// ---- reorder ----------------------------------------------------------------

var contentReorderCmd = &cobra.Command{
	Use:   "reorder",
	Short: "Reorder content items in a hub.",
	Long: `Set the display order of content items. Pass --order as a comma-separated
list of ids in the desired order; each id's position is its 0-based index in
the list.`,
	Example: `  mio content reorder --hub hub_abc --order cnt_1,cnt_2,cnt_3`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		if !cmd.Flags().Changed("order") {
			return errs.New(errs.ExitUsage, "nothing to reorder: set --order with a comma-separated list of ids")
		}
		order, _ := cmd.Flags().GetString("order")

		// The backend ReorderAttributes schema (extra="forbid") requires
		// attributes.items — a LIST of {id, position} objects — and rejects any
		// other field. Split --order into that ordered array, stamping each id
		// with its 0-based position; the parent is determined by item context
		// server-side, so nothing else is sent.
		items := make([]map[string]any, 0)
		for _, raw := range strings.Split(order, ",") {
			id := strings.TrimSpace(raw)
			if id == "" {
				continue
			}
			items = append(items, map[string]any{"id": id, "position": len(items)})
		}
		if len(items) == 0 {
			return errs.New(errs.ExitUsage, "nothing to reorder: --order must contain at least one content id")
		}

		attrs := map[string]any{"items": items}

		path := contentBasePath(teamID, hubID, "") + "/reorder"
		res, err := c.client.Action(c.ctx, http.MethodPost, path, attrs)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Reordered content items.\n")
			return nil
		}
		return c.render(cmd, res)
	},
}

// ---- flag registration ------------------------------------------------------

func init() {
	// Flags for create.
	// NOTE: --status and --published were removed (MIO-942 + Codex R1) — the
	// ContentNodeCreateAttributes schema uses extra="forbid" and has neither a
	// status nor a published field. Publication is controlled by published_at
	// (a nullable timestamp the backend gates visibility on: published =
	// published_at <= now), exposed here as --published-at.
	// --node-type maps to attributes.node_type (required on create; immutable after).
	// --content-type maps to attributes.content_type (optional sub-type for lessons).
	contentCreateCmd.Flags().String("title", "", "Content item title.")
	contentCreateCmd.Flags().String("node-type", "", `Node type: "container" (folder/module) or "lesson" (leaf item). Required on create.`)
	contentCreateCmd.Flags().String("content-type", "", `Optional content sub-type for lesson nodes (e.g. video, audio, pdf, text).`)
	contentCreateCmd.Flags().String("parent-id", "", "Id of the parent content item (nests this item under a folder).")
	contentCreateCmd.Flags().String("description", "", "Content item description.")
	contentCreateCmd.Flags().String("privacy", "", `Privacy setting for the content item (e.g. "members", "public").`)
	contentCreateCmd.Flags().String("published-at", "", "Publish timestamp in RFC 3339 format (e.g. 2026-06-11T00:00:00Z). The item is visible to members once this time has passed.")
	contentCreateCmd.Flags().String("media-id", "", "Id of the media asset backing this content item (the .media_id from 'mio media files retrieve', NOT the file id).")
	contentCreateCmd.Flags().String("file-id", "", "Id of the FILE backing this content item (the .id from 'mio media files list'); its media_id is resolved for you. Mutually exclusive with --media-id.")

	// Flags for update (node_type and parent_id are immutable after create).
	contentUpdateCmd.Flags().String("title", "", "Content item title.")
	contentUpdateCmd.Flags().String("content-type", "", `Optional content sub-type for lesson nodes (e.g. video, audio, pdf, text).`)
	contentUpdateCmd.Flags().String("description", "", "Content item description.")
	contentUpdateCmd.Flags().String("privacy", "", `Privacy setting for the content item (e.g. "members", "public").`)
	contentUpdateCmd.Flags().String("published-at", "", "Publish timestamp in RFC 3339 format (e.g. 2026-06-11T00:00:00Z). The item is visible to members once this time has passed.")
	contentUpdateCmd.Flags().String("media-id", "", "Id of the media asset backing this content item (the .media_id from 'mio media files retrieve', NOT the file id). An empty value is REJECTED, never read as 'unlink' — use --unset-media for that.")
	contentUpdateCmd.Flags().Bool("unset-media", false, "Unlink this content item's media, sending an explicit null — the lesson stops playing. A boolean so no shell variable can expand into it, and destructive, so it prompts (or needs --yes in a non-interactive shell).")
	contentUpdateCmd.Flags().String("file-id", "", "Id of the FILE backing this content item (the .id from 'mio media files list'); its media_id is resolved for you. Mutually exclusive with --media-id.")

	// Reconcile: repeatable --playlist-id. Omitted entirely means "use this
	// hub's scaffold provenance" (the backend derives the set); an empty list
	// is rejected server-side, so the command refuses to send one.
	contentReconcileCmd.Flags().StringSlice("playlist-id", nil, "Playlist id to reconcile; repeatable. Omit to use the playlists this hub was scaffolded with.")

	// Pagination for list and children.
	addPaginationFlags(contentListCmd)
	addPaginationFlags(contentChildrenCmd)

	// Reorder flags. There is no --parent-id: the backend reorder route
	// (ReorderAttributes, extra="forbid") only accepts an items array and
	// determines each node's parent from item context, so a parent flag would
	// be a misleading no-op that the API rejects.
	contentReorderCmd.Flags().String("order", "", "Comma-separated list of content ids in the desired display order (position = 0-based index).")
}

// ---- reconcile --------------------------------------------------------------

var contentReconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Create content items for a hub's playlists so their lessons are trackable.",
	Long: `Materialise content items for a hub's media playlists: one container per
playlist, one lesson per playlist item.

Why this exists: media playlists and content items are two separate surfaces. A
file that lives only in a playlist has no content item, and everything keyed on
one is therefore missing for it — progress and completion tracking, "My List"
saves, comments, and the page builder's single-file feature binding.

This is a HEAL action, not a sync: it is never run for you, so a hub stays
un-reconciled until you call it. It is also additive — existing content items
are adopted rather than duplicated.

With no --playlist-id it reconciles the playlists this hub was scaffolded with.
Pass --playlist-id explicitly for a hub that was not built from a template, or
to reconcile a chosen subset.

Two limits worth knowing before you run it:

  - A playlist must belong to this hub. A team-library playlist that was merely
    published into the hub is rejected with 422 playlist_not_in_hub.
  - A hub that was not built from a template has no scaffold provenance to
    derive from, so a bare run rejects with 422 no_playlist_provenance — pass
    --playlist-id explicitly for those.
  - Lessons are created unpublished unless the file AND its playlist are each
    already published to the hub, so publish first if you want them visible.`,
	Example: `  mio content reconcile --hub hub_abc
  mio content reconcile --hub hub_abc --playlist-id pl_a --playlist-id pl_b`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// Built BEFORE contentContext so a bad --playlist-id fires no request
		// even when --hub is a name that would otherwise be resolved over HTTP
		// (same rule as validateContentMediaFlags).
		//
		// The backend accepts a bodyless POST and derives the playlist set from
		// the hub's scaffold provenance; an explicitly EMPTY list is rejected
		// (min_length=1) rather than read as "no override". So send a body only
		// when the caller actually named playlists.
		var body map[string]any
		if cmd.Flags().Changed("playlist-id") {
			ids, _ := cmd.Flags().GetStringSlice("playlist-id")
			cleaned := make([]string, 0, len(ids))
			for _, raw := range ids {
				id := strings.TrimSpace(raw)
				if id == "" {
					// Dropping a blank silently would reconcile a SHORTER set
					// than the caller named and still report success — the
					// caller would have no way to notice the omission.
					return errs.New(errs.ExitUsage,
						"--playlist-id contains an empty value; remove it or supply a real playlist id")
				}
				cleaned = append(cleaned, id)
			}
			if len(cleaned) == 0 {
				return errs.New(errs.ExitUsage, "--playlist-id was set but no non-empty id was given")
			}
			body = map[string]any{"playlist_ids": cleaned}
		}

		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		path := contentBasePath(teamID, hubID, "") + "/reconcile"
		res, err := c.client.Action(c.ctx, http.MethodPost, path, body)
		if err != nil {
			return err
		}
		if res == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Reconciled content items for hub %s.\n", hubID)
			return nil
		}
		return c.render(cmd, res)
	},
}
