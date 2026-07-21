package cmd

// pages_tree.go — `mio pages tree` sub-group: author (PUT) and read (GET) a
// page's draft node-tree (the page-builder tree). This is how the homepage
// sections are built from the CLI (MIO-2258). Tree shape follows mio-hub's
// DS-conformed section recipes (mio-backend docs/specs seeder homepage guide).
//
// Routes:
//
//	set  PUT  /api/teams/{team_id}/hubs/{hub_id}/pages/{page_id}/tree   (If-Match OCC)
//	get  GET  /api/teams/{team_id}/hubs/{hub_id}/pages/{page_id}/tree?audience=author&resolve=true
//
// The write derives JSON:API type "page_draft_trees" via the pages/tree
// typeOverride in internal/client.

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var pagesTreeCmd = &cobra.Command{
	Use:   "tree",
	Short: "Author and read a page's draft node-tree.",
	Long: `Get and set the page-builder draft node-tree for a page.

'set' replaces the draft (optimistic concurrency via --if-match) and bumps
draft_version; 'get' returns the resolved author draft.`,
}

// pagesTreePath returns …/pages/{page_id}/tree.
func pagesTreePath(teamID, hubID, pageID string) string {
	return pagesPath(teamID, hubID, pageID) + "/tree"
}

var pagesTreeGetCmd = &cobra.Command{
	Use:     "get <page_id>",
	Short:   "Get a page's author draft node-tree.",
	Long:    "Retrieve the resolved draft node-tree for the page-builder editor (audience=author, resolve=true). The draft_version in the response is the OCC token for 'pages tree set' and 'pages publish'.",
	Example: `  mio pages tree get page_abc123 --hub hub_123`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}
		q := url.Values{}
		q.Set("audience", "author")
		q.Set("resolve", "true")
		res, err := c.client.RetrieveWithQuery(c.ctx, pagesTreePath(teamID, hubID, args[0]), q)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}

var pagesTreeSetCmd = &cobra.Command{
	Use:   "set <page_id>",
	Short: "Author a page's draft node-tree.",
	Long: `Replace a page's draft node-tree (the page-builder tree) and bump its
draft_version. The tree JSON is read from --file (a path, optionally @-prefixed).

--if-match is OPTIONAL and defaults to 0. Omit it for the FIRST tree on a
draft-less page: 'pages tree get' 404s until a draft exists, so there is no
draft_version to read back for the very first set — a fresh page starts at
draft_version 0 and the backend accepts If-Match 0 only while the page has never
had a draft. For every subsequent set, pass the draft_version from a prior
'pages tree get' (or 'pages retrieve') — optimistic concurrency control. The
default of 0 does NOT bypass that guard: sending 0 (or any stale value) against a
page that already has a draft is rejected as a conflict, so you can never
silently clobber an existing draft. On success the response carries the new
draft_version; pass it to 'pages publish --if-match <new>'.

Tree shape follows mio-hub's DS-conformed section recipes — see the guide at
mio-backend docs/specs/2026-07-10-seeder-homepage-recipes-guide.md (MIO-2280).
Every node needs an "id" and inlines its values (the defaults cascade is a no-op
in prod), and there is exactly one level:1 headline per page.

For the HOMEPAGE, use STATIC cards, NOT a data-source binding: the homepage route
prefetches only type:"playlist" sources and runs "hub_playlists" with
enabled:false, so a content-grid bound to dataSource:{type:"hub_playlists"}
renders EMPTY there. (hub_playlists feeds the /content browse page, not the
homepage.)`,
	Example: `  mio pages tree set page_abc123 --hub hub_123 --if-match 0 --file tree.json`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// --if-match is optional (defaults to 0). Omitting it sends If-Match: 0,
		// the first-set sentinel: the backend's atomic OCC update matches only a
		// page still at draft_version 0 (never had a draft). A page that already
		// has a draft (draft_version >= 1) is a conflict, not a clobber — so the
		// default is safe and optimistic concurrency stays intact for later sets.
		file, ferr := cmd.Flags().GetString("file")
		if ferr != nil {
			return errs.New(errs.ExitUsage, "--file: %s", ferr)
		}
		file = strings.TrimSpace(file)
		if file == "" {
			return errs.New(errs.ExitUsage, "--file is required: path to the tree JSON file")
		}
		// parseJSONFlag reads an @-prefixed path; force the prefix so --file is
		// always treated as a file path (trees are large — always from a file).
		v, terr := parseJSONFlag("@" + strings.TrimPrefix(file, "@"))
		if terr != nil {
			return errs.New(errs.ExitUsage, "--file: %s", terr)
		}
		treeObj, ok := v.(map[string]any)
		if !ok {
			return errs.New(errs.ExitUsage, "--file must contain a JSON object (the tree)")
		}
		// Pre-flight: reject the well-defined malformed node settings the API
		// accepts (200) but the renderer silently drops, so the author gets a clear
		// error here rather than a phantom success (MIO-2537). Runs before any HTTP.
		if verr := validatePageTree(treeObj); verr != nil {
			return verr
		}

		ifMatch, ierr := cmd.Flags().GetInt("if-match")
		if ierr != nil {
			return errs.New(errs.ExitUsage, "--if-match: %s", ierr)
		}

		c, teamID, hubID, err := pagesContext(cmd)
		if err != nil {
			return err
		}
		// PUT …/pages/{id}/tree with {data:{type:page_draft_trees,attributes:{tree}}}
		// and the If-Match OCC header (mirrors 'pages publish').
		res, err := c.client.ActionWithHeaders(
			c.ctx, client.StyleEnvelope, "PUT",
			pagesTreePath(teamID, hubID, args[0]),
			map[string]any{"tree": treeObj},
			map[string]string{"If-Match": strconv.Itoa(ifMatch)},
		)
		if err != nil {
			return err
		}
		return c.render(cmd, res)
	},
}
