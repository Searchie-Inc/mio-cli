package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func validatePageTree(root map[string]any) error {
	node, ok := root["root"].(map[string]any)
	if !ok {
		return nil
	}
	return validatePageNode(node, "root")
}

type pageTreeRule struct {
	name  string
	check func(node map[string]any, where string) error
}

var pageTreeRules = []pageTreeRule{
	{name: "weight", check: func(node map[string]any, where string) error {
		if settings, ok := node["settings"].(map[string]any); ok {
			if w, present := settings["weight"]; present && !isJSONNumber(w) {
				return errs.New(errs.ExitUsage,
					"%s: settings.weight must be a number like 700, not %s — a non-numeric weight is accepted by the API (200) and then DISCARDED by the renderer, which falls back per kind (headline -> 400/normal, text -> no weight class at all). The node still renders; the weight you authored does not",
					where, describeJSONValue(w))
			}
		}

		return nil
	}},
	{name: "template", check: func(node map[string]any, where string) error {
		if tmpl, present := node["template"]; present {
			s, ok := tmpl.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return errs.New(errs.ExitUsage,
					"%s: template must be a non-empty string like \"hero\", \"carousel\" or \"row\", not %s — a section without a resolvable template is accepted by the API (200) but SILENTLY DROPPED by the renderer",
					where, describeJSONValue(tmpl))
			}
		}

		return nil
	}},
	{name: "misplaced-value", check: func(node map[string]any, where string) error {
		if settings, ok := node["settings"].(map[string]any); ok {
			if _, misplaced := settings["value"]; misplaced && readsTopLevelValue(node) {
				if tv, topLevel := node["value"]; !topLevel || tv == nil {
					return errs.New(errs.ExitUsage,
						"%s: content value must be TOP-LEVEL on the node, not settings.value — the API accepts settings.value (200) and the renderer never reads it, so the value you authored never appears. What you see instead depends on the kind: headline/text render empty, image renders the grey fallback TILE (not nothing), video renders an empty player — or, with embed_type \"iframe\", a visible red \"source not allowed\" box — icon falls back to the \"star\" glyph, quote renders NOTHING AT ALL, button renders with a blank label. Move it to the node's \"value\" key. (progress-ring is the sole kind that reads settings.value)",
						where)
				}
			}
		}

		return nil
	}},
}

func validatePageNode(node map[string]any, path string) error {
	where := nodeLabel(node, path)

	for _, rule := range pageTreeRules {
		if err := rule.check(node, where); err != nil {
			return err
		}
	}

	if children, ok := node["children"].([]any); ok {
		for i, c := range children {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if err := validatePageNode(cm, fmt.Sprintf("%s.children[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

var topLevelValueKinds = map[string]bool{
	"headline": true,
	"text":     true,
	"image":    true,
	"video":    true,
	"button":   true,
	"icon":     true,
	"quote":    true,
}

func readsTopLevelValue(node map[string]any) bool {
	kind, ok := node["kind"].(string)
	if !ok {
		return false
	}
	return topLevelValueKinds[strings.TrimSpace(kind)]
}

func nodeLabel(node map[string]any, path string) string {
	if id, ok := node["id"].(string); ok && id != "" {
		return fmt.Sprintf("node %q", id)
	}
	if kind, ok := node["kind"].(string); ok && kind != "" {
		return fmt.Sprintf("%s node (at %s)", kind, path)
	}
	return "node at " + path
}

func isJSONNumber(v any) bool {
	switch v.(type) {
	case float64, float32, json.Number, int, int64:
		return true
	default:
		return false
	}
}

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
