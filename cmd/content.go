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

import (
	"fmt"
	"net/http"
	"net/url"

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
	)

	// Self-register the whole tree on root.
	rootCmd.AddCommand(contentCmd)
}

// ---- content group ----------------------------------------------------------

var contentCmd = &cobra.Command{
	Use:   "content",
	Short: "Manage hub content items.",
	Long:  "Create, list, retrieve, update, delete, restore, and reorder content items within a hub.",
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

--content-type is an optional sub-type for leaf items (e.g. video, audio, pdf, text).`,
	Example: `  mio content create --hub hub_abc --title "Module 1" --node-type container
  mio content create --hub hub_abc --title "Welcome Video" --node-type lesson --content-type video --parent-id cnt_xyz`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
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
		setBoolFlag(cmd, attrs, "published")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to create: set at least --title and --node-type")
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

Note: node_type and parent_id are immutable after create and cannot be changed via update.`,
	Example: `  mio content update cnt_abc123 --hub hub_abc --title "New Title"
  mio content update cnt_abc123 --hub hub_abc --content-type audio --privacy members`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "title")
		setStringFlag(cmd, attrs, "content-type")
		setStringFlag(cmd, attrs, "description")
		setStringFlag(cmd, attrs, "privacy")
		setBoolFlag(cmd, attrs, "published")

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

// ---- reorder ----------------------------------------------------------------

var contentReorderCmd = &cobra.Command{
	Use:   "reorder",
	Short: "Reorder content items in a hub.",
	Long:  "Set the display order of content items. Pass --order as a comma-separated list of ids.",
	Example: `  mio content reorder --hub hub_abc --order cnt_1,cnt_2,cnt_3
  mio content reorder --hub hub_abc --parent-id cnt_folder --order cnt_a,cnt_b`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c, teamID, hubID, err := contentContext(cmd)
		if err != nil {
			return err
		}

		attrs := map[string]any{}
		setStringFlag(cmd, attrs, "order")
		setStringFlag(cmd, attrs, "parent-id")

		if len(attrs) == 0 {
			return errs.New(errs.ExitUsage, "nothing to reorder: set --order with a comma-separated list of ids")
		}

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
	// NOTE: --status was removed (MIO-942) — ContentNodeCreateAttributes uses
	// extra="forbid" and does not have a status field. Use --privacy instead.
	// --node-type maps to attributes.node_type (required on create; immutable after).
	// --content-type maps to attributes.content_type (optional sub-type for lessons).
	contentCreateCmd.Flags().String("title", "", "Content item title.")
	contentCreateCmd.Flags().String("node-type", "", `Node type: "container" (folder/module) or "lesson" (leaf item). Required on create.`)
	contentCreateCmd.Flags().String("content-type", "", `Optional content sub-type for lesson nodes (e.g. video, audio, pdf, text).`)
	contentCreateCmd.Flags().String("parent-id", "", "Id of the parent content item (nests this item under a folder).")
	contentCreateCmd.Flags().String("description", "", "Content item description.")
	contentCreateCmd.Flags().String("privacy", "", `Privacy setting for the content item (e.g. "members", "public").`)
	contentCreateCmd.Flags().Bool("published", false, "Whether the content item is published.")

	// Flags for update (node_type and parent_id are immutable after create).
	contentUpdateCmd.Flags().String("title", "", "Content item title.")
	contentUpdateCmd.Flags().String("content-type", "", `Optional content sub-type for lesson nodes (e.g. video, audio, pdf, text).`)
	contentUpdateCmd.Flags().String("description", "", "Content item description.")
	contentUpdateCmd.Flags().String("privacy", "", `Privacy setting for the content item (e.g. "members", "public").`)
	contentUpdateCmd.Flags().Bool("published", false, "Whether the content item is published.")

	// Pagination for list and children.
	addPaginationFlags(contentListCmd)
	addPaginationFlags(contentChildrenCmd)

	// Reorder flags.
	contentReorderCmd.Flags().String("order", "", "Comma-separated list of content ids in the desired display order.")
	contentReorderCmd.Flags().String("parent-id", "", "Id of the parent folder whose children are being reordered.")
}
