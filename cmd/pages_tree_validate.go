package cmd

// pages_tree_validate.go — CLI-side pre-flight validation of a page-builder
// node-tree, run by `pages tree set` AFTER reading --file and BEFORE the PUT
// (MIO-2537, pairs with backend MIO-2538 in review).
//
// The API validates a tree's STRUCTURE, not its RENDERABILITY: several malformed
// node shapes are accepted (200) and then fail at render time, so an author sees
// a phantom success. The failure is NOT uniform, and the messages below say
// which is which (MIO-2799):
//   - a genuine DROP — content under settings.value is never read, so the node
//     renders EMPTY;
//   - a DISCARD with fallback — a non-numeric weight is ignored and the kind's
//     default applies, so the node renders with the wrong weight, not missing.
// Calling everything a "silent drop" sends the reader hunting for a missing
// node when the node is right there.
// This walker rejects the well-defined malformed cases up front (ExitUsage, no
// HTTP) with a message naming the offending node and field.
//
// It is deliberately CONSERVATIVE — it flags only shapes that can never render,
// so a valid tree (including a catalog-scaffolded one) is never rejected. The
// tree shape is {"root": <node>}; a node is {kind, settings{...}, template?,
// children[...]} (see internal/catalog). The two pinned rules:
//
//   - settings.weight, if present, must be a NUMBER (e.g. 700) — never a CSS
//     keyword string like "bold". The catalog only ever emits numeric weights.
//     NOTE the failure mode is a per-kind FALLBACK, not a drop (MIO-2799): the
//     renderer gates on Object.prototype.hasOwnProperty over a {400,500,600,700}
//     map, so "bold" misses and headline falls back to font-normal while text
//     resolves to no weight class. The node always renders; the authored weight
//     is discarded. (A numeric STRING like "700" actually matches, because
//     object keys are strings — the CLI still rejects it as strictness, but it
//     is not the thing that breaks.) mio-docs says "discarded rather than
//     applied"; keep these two surfaces in agreement.
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
				"%s: settings.weight must be a number like 700, not %s — a non-numeric weight is accepted by the API (200) and then DISCARDED by the renderer, which falls back per kind (headline -> 400/normal, text -> no weight class at all). The node still renders; the weight you authored does not",
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

	// (3) content `value` under settings instead of the node top level (MIO-2575).
	// THE most-hit trap: the API 200s and the renderer never reads settings.value,
	// so the authored value never appears. The VISIBLE result is per-kind and the
	// message says so — headline/text/image/video empty, icon falls back to the
	// "star" glyph (resolveIconName, mio-hub icons.ts: "so the page-tree never
	// renders nothing"), quote returns null so the node is absent entirely, button
	// renders with a blank label. Describing all of them as "renders EMPTY" would
	// repeat exactly the mistake MIO-2799 corrects for weight. Seven leaf kinds read the
	// TOP-LEVEL value — headline, text, image, video, button, icon, quote — and
	// exactly one, progress-ring, legitimately reads settings.value (a number),
	// so it is exempt. Checked only when the top-level value is ABSENT: a node
	// carrying both is not silently dropping anything, and rejecting it would
	// break trees the renderer handles fine.
	if settings, ok := node["settings"].(map[string]any); ok {
		if _, misplaced := settings["value"]; misplaced && readsTopLevelValue(node) {
			if _, topLevel := node["value"]; !topLevel {
				return errs.New(errs.ExitUsage,
					"%s: content value must be TOP-LEVEL on the node, not settings.value — the API accepts settings.value (200) and the renderer never reads it, so the value you authored never appears. What you see instead depends on the kind: headline/text/image/video render empty, icon falls back to the \"star\" glyph, quote renders NOTHING AT ALL, button renders with a blank label. Move it to the node's \"value\" key. (progress-ring is the sole kind that reads settings.value)",
					where)
			}
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

// topLevelValueKinds is the set of node kinds VERIFIED to read the top-level
// node.value in mio-hub (origin/main, src/components/primitives/leaves/*):
// headline, text, image, video, button, icon and quote each reference
// node.value. progress-ring is deliberately ABSENT — it is the sole kind that
// reads settings.value (a number), so flagging it would reject a tree the
// renderer handles correctly.
//
// This is an ALLOWLIST, not an exemption list, and that is the load-bearing
// choice: an unrecognised or future kind must never be rejected on a guess,
// because this walker flags only shapes that CAN NEVER render. A denylist would
// invert that — every kind added to mio-hub would start failing here until
// someone remembered to update the CLI.
var topLevelValueKinds = map[string]bool{
	"headline": true,
	"text":     true,
	"image":    true,
	"video":    true,
	"button":   true,
	"icon":     true,
	"quote":    true,
}

// readsTopLevelValue reports whether a node's kind is known to read node.value,
// i.e. whether a settings.value on it is definitely the misplacement trap.
func readsTopLevelValue(node map[string]any) bool {
	kind, ok := node["kind"].(string)
	if !ok {
		return false
	}
	return topLevelValueKinds[strings.TrimSpace(kind)]
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
