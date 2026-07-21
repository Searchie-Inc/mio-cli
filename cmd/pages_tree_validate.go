package cmd

// pages_tree_validate.go — CLI-side pre-flight validation of a page-builder
// node-tree, run by `pages tree set` AFTER reading --file and BEFORE the PUT
// (MIO-2537, pairs with backend MIO-2538 in review).
//
// The API validates a tree's STRUCTURE, not its RENDERABILITY: several malformed
// node settings are accepted (200) and then SILENTLY DROPPED by the renderer, so
// an author sees a phantom success and a missing section/node at render time.
// This walker rejects the well-defined malformed cases up front (ExitUsage, no
// HTTP) with a message naming the offending node and field.
//
// It is deliberately CONSERVATIVE — it flags only shapes that can never render,
// so a valid tree (including a catalog-scaffolded one) is never rejected. The
// tree shape is {"root": <node>}; a node is {kind, settings{...}, template?,
// children[...]} (see internal/catalog). The two pinned rules:
//
//   - settings.weight, if present, must be a NUMBER (e.g. 700) — never a CSS
//     keyword string like "bold". The catalog only ever emits numeric weights;
//     a string weight is dropped.
//   - template, if present on a node (the section marker), must be a NON-EMPTY
//     STRING ("hero", "carousel", "row", …). A section whose template is blank
//     or non-string has no resolvable section type and will not render. A node
//     WITHOUT a template key is NOT identifiable as a section from the tree
//     alone (inner containers legitimately carry no template), so it is left
//     untouched — matching the existing minimal-tree contract tests.
//
// NOT validated: the button-node `action` shape. The catalog scaffold emits
// valid buttons with an empty settings object (they inherit the action from
// scope, e.g. settings.actionFromScope:"action"), so "missing action" is not
// malformed; and no concrete literal `action` object shape is pinned anywhere in
// the catalog, code, or docs to validate against. Rejecting on a guessed shape
// would drop valid trees, so that case is left to the backend (MIO-2538).

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// validatePageTree walks the tree object read from --file ({"root": <node>}) and
// returns an ExitUsage error naming the first offending node+field, or nil if the
// tree carries no pinned malformed setting. A tree with no walkable "root" node is
// left for the backend to reject (this guards node SETTINGS, not envelope shape).
func validatePageTree(root map[string]any) error {
	node, ok := root["root"].(map[string]any)
	if !ok {
		return nil
	}
	return validatePageNode(node, "root")
}

// validatePageNode checks one node's pinned settings, then recurses into its
// children in order (pre-order DFS — the first violation wins). path is a
// dotted/indexed locator (root.children[0]…) used in the error when the node has
// no id.
func validatePageNode(node map[string]any, path string) error {
	where := nodeLabel(node, path)

	// (1) settings.weight, if present, must be numeric.
	if settings, ok := node["settings"].(map[string]any); ok {
		if w, present := settings["weight"]; present && !isJSONNumber(w) {
			return errs.New(errs.ExitUsage,
				"%s: settings.weight must be a number like 700, not %s — a non-numeric weight is accepted by the API (200) but SILENTLY DROPPED by the renderer",
				where, describeJSONValue(w))
		}
	}

	// (2) template, if present, must be a non-empty string (the section marker).
	if tmpl, present := node["template"]; present {
		s, ok := tmpl.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return errs.New(errs.ExitUsage,
				"%s: template must be a non-empty string like \"hero\", \"carousel\" or \"row\", not %s — a section without a resolvable template is accepted by the API (200) but SILENTLY DROPPED by the renderer",
				where, describeJSONValue(tmpl))
		}
	}

	// Recurse into children in order.
	if children, ok := node["children"].([]any); ok {
		for i, c := range children {
			cm, ok := c.(map[string]any)
			if !ok {
				continue // non-object children are the backend's to reject
			}
			if err := validatePageNode(cm, fmt.Sprintf("%s.children[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// nodeLabel names a node for an error message: its id when it carries one, else
// its kind + tree path, else just the tree path.
func nodeLabel(node map[string]any, path string) string {
	if id, ok := node["id"].(string); ok && id != "" {
		return fmt.Sprintf("node %q", id)
	}
	if kind, ok := node["kind"].(string); ok && kind != "" {
		return fmt.Sprintf("%s node (at %s)", kind, path)
	}
	return "node at " + path
}

// isJSONNumber reports whether v decoded from JSON as a number. parseJSONFlag
// decodes via encoding/json without UseNumber, so numbers arrive as float64;
// json.Number and native ints are accepted too so the check is robust to either
// decoder.
func isJSONNumber(v any) bool {
	switch v.(type) {
	case float64, float32, json.Number, int, int64:
		return true
	default:
		return false
	}
}

// describeJSONValue renders the offending value's JSON type for an error message.
func describeJSONValue(v any) string {
	switch t := v.(type) {
	case string:
		return fmt.Sprintf("the string %q", t)
	case bool:
		return "a boolean"
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	default:
		return "a non-string value"
	}
}
