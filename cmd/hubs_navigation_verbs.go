package cmd

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

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

	hubsNavAddCmd.Flags().String("item-json", "", "Full menu item as a JSON object (any bucket/type). Inline JSON or @file. NOT fully verbatim: a position key is normalized to the item's resulting array index (see --position).")
	hubsNavAddCmd.Flags().String("type", "", "Convenience for a url item: url (page/playlist/discussions and mobile items use --item-json).")
	hubsNavAddCmd.Flags().String("href", "", "url item href (with --type url). A hub-relative href must start with /{slug}.")
	hubsNavAddCmd.Flags().String("label", "", "url item label (with --type url).")
	hubsNavAddCmd.Flags().Int("position", 0, "Insert at this zero-based index (default: append to the end). The item's own position field is set from its resulting index, so a position inside --item-json is normalized to that index rather than honoured verbatim. NOTE no backticks here: pflag's UnquoteUsage takes the first backquoted word as the flag's value name, which would replace the 'int' placeholder.")

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

func requireNavBucket(bucket string) error {
	if !isNavBucket(bucket) {
		return errs.New(errs.ExitUsage, "invalid bucket %q: must be header, footer, or mobile", bucket)
	}
	return nil
}

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

func fetchHubNav(c *cmdContext, teamID, hubID string) (map[string]any, string, error) {
	res, err := c.client.Retrieve(c.ctx, hubsPath(teamID, hubID))
	if err != nil {
		return nil, "", err
	}
	slug, _ := res.Attributes["slug"].(string)
	nav := map[string]any{}
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

func writeHubNav(c *cmdContext, teamID, hubID string, nav map[string]any, slug string, editedBucket string) error {
	if err := validateNavigationBlob(nav); err != nil {
		return err
	}
	renumberNavPositions(nav, editedBucket)
	if err := validateNavigationHrefs(nav, slug); err != nil {
		return err
	}
	_, err := c.client.Update(c.ctx, hubsPath(teamID, hubID), map[string]any{"navigation": nav})
	return err
}

var knownNavItemTypes = map[string]bool{
	"url":         true,
	"discussions": true,
	"page":        true,
}

func renumberNavPositions(nav map[string]any, bucket string) {
	{
		items, ok := nav[bucket].([]any)
		if !ok {
			return
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
		row["index"] = i
		out[i] = row
	}
	return out
}

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

		at := len(items)
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

		if err := writeHubNav(c, teamID, hubID, nav, slug, bucket); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}

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

		if err := writeHubNav(c, teamID, hubID, nav, slug, bucket); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}

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

		if err := writeHubNav(c, teamID, hubID, nav, slug, bucket); err != nil {
			return err
		}
		return c.render(cmd, map[string]any{bucket: indexedBucket(nav, bucket)})
	},
}
