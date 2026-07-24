package cmd

// hubs_navigation_verbs.go — `mio hubs navigation` list/add/remove/reorder verbs
// (MIO-2633). Ergonomic, item-by-item editing of the hub navigation menu without
// hand-rebuilding the whole `--navigation-json` blob.
//
// Each mutating verb read-modify-writes the hub's `navigation` blob: retrieve the
// hub, mutate ONE bucket (header|footer|mobile), validate the whole blob
// (validateNavigationBlob + validateNavigationHrefs against the hub's slug), then
// PATCH `navigation` as a whole-blob REPLACE — a partial update, so the hub's
// other fields (branding/settings/…) are untouched. header/footer items carry no
// stable id, so items are addressed by their zero-based INDEX as shown by
// `navigation list`.
//
// Concurrency: this is a read-modify-write with no optimistic-lock guard (the hub
// update route exposes none), so a racing edit can be lost — the same
// last-write-wins window as the existing `--navigation-json` replace.

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// navBuckets are the navigation menu buckets the hub renderer understands. header
// and footer carry {type,href,label} items (typed + hub-scoped hrefs); mobile
// carries {id,label,route,icon} items (validated only as an array of objects).
var navBuckets = []string{"header", "footer", "mobile"}

func isNavBucket(b string) bool {
	for _, x := range navBuckets {
		if x == b {
			return true
		}
	}
	return false
}

func init() {
	hubsNavigationCmd.AddCommand(
		hubsNavListCmd,
		hubsNavAddCmd,
		hubsNavRemoveCmd,
		hubsNavReorderCmd,
	)
	hubsCmd.AddCommand(hubsNavigationCmd)

	hubsNavAddCmd.Flags().String("item-json", "", "Full menu item as a JSON object (any bucket/type). Inline JSON or @file.")
	hubsNavAddCmd.Flags().String("type", "", "Convenience for a url item: url (page/playlist/discussions and mobile items use --item-json).")
	hubsNavAddCmd.Flags().String("href", "", "url item href (with --type url). A hub-relative href must start with /{slug}.")
	hubsNavAddCmd.Flags().String("label", "", "url item label (with --type url).")
	hubsNavAddCmd.Flags().Int("position", 0, "Insert at this zero-based index (default: append to the end).")

	hubsNavRemoveCmd.Flags().Int("index", 0, "Zero-based index of the item to remove (see 'navigation list'). Required.")

	hubsNavReorderCmd.Flags().String("order", "", "Comma-separated permutation of the bucket's current indices, e.g. 2,0,1. Required.")
}

var hubsNavigationCmd = &cobra.Command{
	Use:   "navigation",
	Short: "Edit a hub's navigation menu (header/footer/mobile) item-by-item.",
	Long: `Read-modify-write the hub navigation menu without rebuilding the whole
--navigation-json blob. Items are addressed by their zero-based index (from
'navigation list'); header/footer items carry no stable id.

Each edit re-validates the whole menu (buckets well-formed; header/footer items
carry a non-empty type; hub-relative hrefs stay within /{slug}) before writing.`,
}

// ── shared helpers ──────────────────────────────────────────────────────────

func requireNavBucket(bucket string) error {
	if !isNavBucket(bucket) {
		return errs.New(errs.ExitUsage, "invalid bucket %q: must be header, footer, or mobile", bucket)
	}
	return nil
}

// fetchHubNav retrieves the hub and returns a shallow-copied, mutable navigation
// blob plus the hub's slug (for hub-scoped href validation). A hub with no
// navigation yields an empty map.
func fetchHubNav(c *cmdContext, teamID, hubID string) (map[string]any, string, error) {
	res, err := c.client.Retrieve(c.ctx, hubsPath(teamID, hubID))
	if err != nil {
		return nil, "", err
	}
	slug, _ := res.Attributes["slug"].(string)
	nav := map[string]any{}
	// An absent or null navigation is a fresh menu (empty map). But a present,
	// non-object navigation is malformed data — treating it as {} would let the
	// next write REPLACE (destroy) it, so reject instead (MIO-2633, Codex R1).
	if raw, ok := res.Attributes["navigation"]; ok && raw != nil {
		cur, ok := raw.(map[string]any)
		if !ok {
			return nil, "", errs.New(errs.ExitUsage,
				"the hub's stored navigation is not a JSON object (got %T); repair it with 'hubs update --navigation-json' before editing item-by-item", raw)
		}
		for k, v := range cur {
			nav[k] = v
		}
	}
	return nav, slug, nil
}

// bucketItems returns a bucket's items (empty if the bucket is absent), erroring
// if the stored bucket is present but not an array.
func bucketItems(nav map[string]any, bucket string) ([]any, error) {
	raw, ok := nav[bucket]
	if !ok || raw == nil {
		return []any{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errs.New(errs.ExitUsage, "navigation.%s is not an array; repair it with 'hubs update --navigation-json' first", bucket)
	}
	return items, nil
}

// writeHubNav validates the whole blob (buckets well-formed + header/footer hrefs
// hub-scoped) then PATCHes navigation as a whole-blob REPLACE.
func writeHubNav(c *cmdContext, teamID, hubID string, nav map[string]any, slug string) error {
	if err := validateNavigationBlob(nav); err != nil {
		return err
	}
	if err := validateNavigationHrefs(nav, slug); err != nil {
		return err
	}
	_, err := c.client.Update(c.ctx, hubsPath(teamID, hubID), map[string]any{"navigation": nav})
	return err
}

// indexedBucket renders a bucket's items each prefixed with its zero-based index,
// so the output doubles as the addressing scheme for remove/reorder. Returns
// []any (not []map[string]any) so the value is a generic JSON tree that --jq
// (gojq) can traverse — gojq rejects concrete Go slice types.
func indexedBucket(nav map[string]any, bucket string) []any {
	items, _ := bucketItems(nav, bucket)
	out := make([]any, len(items))
	for i, it := range items {
		row := map[string]any{}
		if m, ok := it.(map[string]any); ok {
			for k, v := range m {
				row[k] = v
			}
		}
		// Assign the generated index LAST so an item that happens to carry its own
		// "index" field can't shadow the real position (which addresses remove/
		// reorder) — Codex R1.
		row["index"] = i
		out[i] = row
	}
	return out
}

// ── list ────────────────────────────────────────────────────────────────────

var hubsNavListCmd = &cobra.Command{
	Use:   "list <hub_id> [bucket]",
	Short: "List a hub's navigation items with their indices.",
	Long:  "Print the hub navigation menu (header/footer/mobile), each item prefixed with its zero-based index for use with 'navigation remove'/'reorder'.",
	Example: `  mio hubs navigation list hub_abc123
  mio hubs navigation list hub_abc123 header`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		buckets := navBuckets
		if len(args) == 2 {
			if err := requireNavBucket(args[1]); err != nil {
				return err
			}
			buckets = []string{args[1]}
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		nav, _, err := fetchHubNav(c, teamID, args[0])
		if err != nil {
			return err
		}
		out := map[string]any{}
		for _, b := range buckets {
			if _, err := bucketItems(nav, b); err != nil {
				return err
			}
			out[b] = indexedBucket(nav, b)
		}
		return c.render(cmd, out)
	},
}

// ── add ─────────────────────────────────────────────────────────────────────

// buildNavItem constructs the item to insert from either --item-json (any
// bucket/type) or the url convenience flags. The two are mutually exclusive; one
// is required. The url convenience builds a {type,href,label} item, which is the
// header/footer shape — the mobile bucket uses {id,label,route,icon}, so mobile
// items MUST come via --item-json. Runs before any HTTP so a bad invocation exits
// 2 with no request.
func buildNavItem(cmd *cobra.Command, bucket string) (map[string]any, error) {
	item, err := parseJSONObjectFlag(cmd, "item-json")
	if err != nil {
		return nil, err
	}
	typeSet := cmd.Flags().Changed("type")
	convenience := typeSet || cmd.Flags().Changed("href") || cmd.Flags().Changed("label")

	if item != nil {
		if convenience {
			return nil, errs.New(errs.ExitUsage, "--item-json cannot be combined with --type/--href/--label")
		}
		return item, nil
	}
	// url-convenience path (no --item-json).
	if bucket == "mobile" {
		return nil, errs.New(errs.ExitUsage, "mobile items must be given as --item-json (the {id,label,route,icon} shape); the --type/--href/--label convenience flags build a header/footer url item")
	}
	if !convenience {
		return nil, errs.New(errs.ExitUsage, "provide --item-json '<obj>' or the url convenience flags --type url --href <h> --label <l>")
	}
	if !typeSet {
		return nil, errs.New(errs.ExitUsage, "--type url is required alongside --href/--label")
	}
	typ, _ := cmd.Flags().GetString("type")
	if typ != "url" {
		return nil, errs.New(errs.ExitUsage, "--type %q: only url is supported via flags; use --item-json for page/playlist/discussions and mobile items", typ)
	}
	href, _ := cmd.Flags().GetString("href")
	if strings.TrimSpace(href) == "" {
		return nil, errs.New(errs.ExitUsage, "--type url requires --href")
	}
	out := map[string]any{"type": "url", "href": href}
	if label, _ := cmd.Flags().GetString("label"); label != "" {
		out["label"] = label
	}
	return out, nil
}

var hubsNavAddCmd = &cobra.Command{
	Use:   "add <hub_id> <bucket>",
	Short: "Add an item to a navigation bucket (header|footer|mobile).",
	Long: `Append (or insert with --position) a menu item into a navigation bucket.

Provide the item as a full JSON object with --item-json (any bucket/type:
url|page|playlist|discussions for header/footer, or the {id,label,route,icon}
shape for mobile), or use the url convenience flags: --type url --href <h> --label <l>.

Hub-relative header/footer hrefs must stay within the hub (start with /{slug}).`,
	Example: `  mio hubs navigation add hub_abc123 header --type url --href /my-hub/about --label About
  mio hubs navigation add hub_abc123 header --item-json '{"type":"page","label":"Guide","page_id":"pg_1"}'`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket := args[1]
		if err := requireNavBucket(bucket); err != nil {
			return err
		}
		item, err := buildNavItem(cmd, bucket)
		if err != nil {
			return err
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		nav, slug, err := fetchHubNav(c, teamID, args[0])
		if err != nil {
			return err
		}
		items, err := bucketItems(nav, bucket)
		if err != nil {
			return err
		}

		at := len(items) // default: append
		if cmd.Flags().Changed("position") {
			pos, _ := cmd.Flags().GetInt("position")
			if pos < 0 || pos > len(items) {
				return errs.New(errs.ExitUsage, "--position %d out of range [0,%d] for navigation.%s", pos, len(items), bucket)
			}
			at = pos
		}
		out := make([]any, 0, len(items)+1)
		out = append(out, items[:at]...)
		out = append(out, item)
		out = append(out, items[at:]...)
		nav[bucket] = out

		if err := writeHubNav(c, teamID, args[0], nav, slug); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}

// ── remove ──────────────────────────────────────────────────────────────────

var hubsNavRemoveCmd = &cobra.Command{
	Use:     "remove <hub_id> <bucket>",
	Short:   "Remove a navigation item by index.",
	Long:    "Remove the item at --index (zero-based, from 'navigation list') from a navigation bucket.",
	Example: `  mio hubs navigation remove hub_abc123 header --index 2`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket := args[1]
		if err := requireNavBucket(bucket); err != nil {
			return err
		}
		if !cmd.Flags().Changed("index") {
			return errs.New(errs.ExitUsage, "--index is required (see 'navigation list')")
		}
		idx, _ := cmd.Flags().GetInt("index")

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		nav, slug, err := fetchHubNav(c, teamID, args[0])
		if err != nil {
			return err
		}
		items, err := bucketItems(nav, bucket)
		if err != nil {
			return err
		}
		if idx < 0 || idx >= len(items) {
			return errs.New(errs.ExitUsage, "--index %d out of range [0,%d) for navigation.%s", idx, len(items), bucket)
		}
		out := make([]any, 0, len(items)-1)
		out = append(out, items[:idx]...)
		out = append(out, items[idx+1:]...)
		nav[bucket] = out

		if err := writeHubNav(c, teamID, args[0], nav, slug); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}

// ── reorder ─────────────────────────────────────────────────────────────────

// parseIndexList parses "2,0,1" into []int{2,0,1}, erroring on a non-integer.
func parseIndexList(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, errs.New(errs.ExitUsage, "--order: %q is not a valid index", strings.TrimSpace(p))
		}
		out = append(out, n)
	}
	return out, nil
}

// applyPermutation returns items reordered by order, which MUST be a permutation
// of [0,len(items)) — every index exactly once — so no item is dropped or dup'd.
func applyPermutation(items []any, order []int) ([]any, error) {
	if len(order) != len(items) {
		return nil, errs.New(errs.ExitUsage, "--order has %d indices but the bucket has %d items; list every index exactly once", len(order), len(items))
	}
	seen := make([]bool, len(items))
	out := make([]any, len(items))
	for i, idx := range order {
		if idx < 0 || idx >= len(items) {
			return nil, errs.New(errs.ExitUsage, "--order index %d out of range [0,%d)", idx, len(items))
		}
		if seen[idx] {
			return nil, errs.New(errs.ExitUsage, "--order lists index %d more than once", idx)
		}
		seen[idx] = true
		out[i] = items[idx]
	}
	return out, nil
}

var hubsNavReorderCmd = &cobra.Command{
	Use:     "reorder <hub_id> <bucket>",
	Short:   "Reorder a navigation bucket by an index permutation.",
	Long:    "Reorder a navigation bucket by --order, a comma-separated permutation of its current zero-based indices (every index exactly once).",
	Example: `  mio hubs navigation reorder hub_abc123 header --order 2,0,1`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		bucket := args[1]
		if err := requireNavBucket(bucket); err != nil {
			return err
		}
		orderStr, _ := cmd.Flags().GetString("order")
		if strings.TrimSpace(orderStr) == "" {
			return errs.New(errs.ExitUsage, "--order is required: a comma-separated permutation of the current indices, e.g. 2,0,1")
		}
		order, err := parseIndexList(orderStr)
		if err != nil {
			return err
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		nav, slug, err := fetchHubNav(c, teamID, args[0])
		if err != nil {
			return err
		}
		items, err := bucketItems(nav, bucket)
		if err != nil {
			return err
		}
		reordered, err := applyPermutation(items, order)
		if err != nil {
			return err
		}
		nav[bucket] = reordered

		if err := writeHubNav(c, teamID, args[0], nav, slug); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}
