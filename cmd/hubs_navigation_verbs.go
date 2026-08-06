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
//
// The hub id positional is OPTIONAL on every verb (MIO-2732): omit it and the hub
// comes from --hub / config current_hub like every other hub-scoped verb. Because
// it shares the positional slot with the bucket, the two are told apart by value
// — see splitNavArgs.

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

// splitNavArgs separates the navigation verbs' positionals into the (optional)
// hub id and the bucket, so these verbs honour --hub/current_hub like every
// other hub-scoped verb (MIO-2732).
//
// The hub id shares the positional slot with the bucket, so the two are told
// apart by VALUE: the buckets are a closed three-name set (header|footer|mobile)
// and a hub id positional is passed through verbatim — it is never resolved from
// a name or slug — so a bare "header" could never have addressed a hub before
// this change either. There is therefore no invocation whose meaning shifts:
//
//	<hub_id> <bucket>   → explicit hub, explicit bucket   (unchanged)
//	<bucket>            → ambient hub, explicit bucket    (new)
//	<hub_id>            → explicit hub, no bucket         (unchanged; `list` only)
//	(nothing)           → ambient hub, no bucket          (new; `list` only)
//
// An empty bucket return means "not supplied": `navigation list` reads it as all
// buckets, while add/remove/reorder reject it via requireNavBucket.
//
// hubGiven reports whether a hub positional was SUPPLIED (even if blank), which
// hubTargetID needs to keep a blank positional from silently falling through to
// the ambient hub — see hubTargetID's contract.
//
// INVARIANT (test-enforced by TestNavBuckets_AreNeverIDShaped): no bucket name
// may be id-shaped. The whole disambiguation rests on it — if a bucket were ever
// named like a hub id, a one-positional invocation would become genuinely
// ambiguous and could silently address the wrong hub.
func splitNavArgs(args []string) (hubArg string, hubGiven bool, bucket string) {
	switch len(args) {
	case 0:
		return "", false, ""
	case 1:
		if isNavBucket(args[0]) {
			return "", false, args[0]
		}
		return args[0], true, ""
	default:
		return args[0], true, args[1]
	}
}

// hubsNavArgs builds the Args validator for a navigation verb accepting between
// minArgs and maxArgs positionals, rejecting a supplied-but-BLANK hub id in the
// Args phase so it can never reach RunE and fire a resolution request — the same
// no-request-on-usage-error guarantee hubsOptionalIDArgs gives the other hubs
// verbs (Codex R2).
func hubsNavArgs(minArgs, maxArgs int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < minArgs || len(args) > maxArgs {
			return errs.New(errs.ExitUsage,
				"`%s` accepts between %d and %d arguments, got %d",
				cmd.CommandPath(), minArgs, maxArgs, len(args))
		}
		if hubArg, hubGiven, _ := splitNavArgs(args); hubGiven && strings.TrimSpace(hubArg) == "" {
			return errs.New(errs.ExitUsage, "%s", blankHubIDMessage(cmd))
		}
		return nil
	}
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
	// `position` is REQUIRED on every strictly-validated nav item and the CLI
	// never set it — add/remove/reorder all shipped items without it, so a typed
	// add 422s with `NavItemPage.position Field required` (MIO-2990). Renumbering
	// here rather than at the insert covers all three verbs at once, and keeps
	// position consistent with array order after a mid-bucket insert or a remove,
	// which a set-on-insert fix would not.
	renumberNavPositions(nav)
	if err := validateNavigationHrefs(nav, slug); err != nil {
		return err
	}
	_, err := c.client.Update(c.ctx, hubsPath(teamID, hubID), map[string]any{"navigation": nav})
	return err
}

// knownNavItemTypes are the item types the backend validates STRICTLY, each of
// which declares a bare `position: int` with no default (app/hubs/validation.py
// _KNOWN_NAV_ITEM_MODELS -> NavItemUrl / NavItemDiscussions / NavItemPage).
//
// Anything else — a "playlist" item, or the mobile {id,label,route,icon} shape
// which carries no `type` at all — takes the backend's PASSTHROUGH branch and is
// not schema-checked. Those are left untouched: stamping `position` onto a shape
// the CLI does not own would be inventing a field, which the conduit rule cuts
// against, and it is not what makes them fail (they do not fail).
//
// Matched case- and space-insensitively because the backend normalizes the type
// before lookup, so `" Page "` reaches strict validation and would 422 without a
// position just the same.
var knownNavItemTypes = map[string]bool{
	"url":         true,
	"discussions": true,
	"page":        true,
}

// renumberNavPositions sets each strictly-validated item's `position` to its
// zero-based index within its bucket, across every bucket in the blob.
//
// Index, not a counter over known items only: `position` addresses the item's
// place in the rendered menu, and `navigation list` advertises the same array
// index for remove/reorder. A bucket mixing known and passthrough items must
// still agree with what the operator sees.
func renumberNavPositions(nav map[string]any) {
	for _, bucket := range []string{"header", "footer", "mobile"} {
		items, ok := nav[bucket].([]any)
		if !ok {
			continue
		}
		for i, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			t, ok := m["type"].(string)
			if !ok || !knownNavItemTypes[strings.ToLower(strings.TrimSpace(t))] {
				continue
			}
			m["position"] = i
		}
	}
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
	Use:   "list [hub_id] [bucket]",
	Short: "List a hub's navigation items with their indices.",
	Long: `Print the hub navigation menu (header/footer/mobile), each item prefixed with its zero-based index for use with 'navigation remove'/'reorder'.

The hub id may be given positionally; omit it to use the ambient hub (--hub, or
current_hub in config).`,
	Example: `  mio hubs navigation list hub_abc123
  mio hubs navigation list hub_abc123 header
  mio hubs navigation list header          # ambient hub, header bucket
  mio hubs navigation list --hub hub_abc123`,
	Args: hubsNavArgs(0, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		hubArg, hubGiven, bucket := splitNavArgs(args)

		buckets := navBuckets
		if bucket != "" {
			if err := requireNavBucket(bucket); err != nil {
				return err
			}
			buckets = []string{bucket}
		}

		c, teamID, err := hubsContext(cmd)
		if err != nil {
			return err
		}
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}
		nav, _, err := fetchHubNav(c, teamID, hubID)
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
	Use:   "add [hub_id] <bucket>",
	Short: "Add an item to a navigation bucket (header|footer|mobile).",
	Long: `Append (or insert with --position) a menu item into a navigation bucket.

Provide the item as a full JSON object with --item-json (any bucket/type:
url|page|playlist|discussions for header/footer, or the {id,label,route,icon}
shape for mobile), or use the url convenience flags: --type url --href <h> --label <l>.

Hub-relative header/footer hrefs must stay within the hub (start with /{slug}).

The hub id may be given positionally before the bucket; omit it to use the
ambient hub (--hub, or current_hub in config).`,
	Example: `  mio hubs navigation add hub_abc123 header --type url --href /my-hub/about --label About
  mio hubs navigation add hub_abc123 header --item-json '{"type":"page","label":"Guide","page_id":"pg_1"}'
  mio hubs navigation add header --type url --href /my-hub/about --label About`,
	Args: hubsNavArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		hubArg, hubGiven, bucket := splitNavArgs(args)
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
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}
		nav, slug, err := fetchHubNav(c, teamID, hubID)
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

		if err := writeHubNav(c, teamID, hubID, nav, slug); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}

// ── remove ──────────────────────────────────────────────────────────────────

var hubsNavRemoveCmd = &cobra.Command{
	Use:   "remove [hub_id] <bucket>",
	Short: "Remove a navigation item by index.",
	Long: `Remove the item at --index (zero-based, from 'navigation list') from a navigation bucket.

The hub id may be given positionally before the bucket; omit it to use the
ambient hub (--hub, or current_hub in config).`,
	Example: `  mio hubs navigation remove hub_abc123 header --index 2
  mio hubs navigation remove header --index 2`,
	Args: hubsNavArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		hubArg, hubGiven, bucket := splitNavArgs(args)
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
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}
		nav, slug, err := fetchHubNav(c, teamID, hubID)
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

		if err := writeHubNav(c, teamID, hubID, nav, slug); err != nil {
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
	Use:   "reorder [hub_id] <bucket>",
	Short: "Reorder a navigation bucket by an index permutation.",
	Long: `Reorder a navigation bucket by --order, a comma-separated permutation of its current zero-based indices (every index exactly once).

The hub id may be given positionally before the bucket; omit it to use the
ambient hub (--hub, or current_hub in config).`,
	Example: `  mio hubs navigation reorder hub_abc123 header --order 2,0,1
  mio hubs navigation reorder header --order 2,0,1`,
	Args: hubsNavArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		hubArg, hubGiven, bucket := splitNavArgs(args)
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
		hubID, err := c.hubTargetID(cmd, hubArg, hubGiven)
		if err != nil {
			return err
		}
		nav, slug, err := fetchHubNav(c, teamID, hubID)
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

		if err := writeHubNav(c, teamID, hubID, nav, slug); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}
