package cmd

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestRenderContractDocs_AgreeWithTheValidator(t *testing.T) {
	wantKinds := make([]string, 0, len(topLevelValueKinds))
	for kind := range topLevelValueKinds {
		wantKinds = append(wantKinds, kind)
	}
	sort.Strings(wantKinds)
	// README has neither a count nor a kind list; the skill has no kind list.
	tests := []struct{ file, fact, pattern, mode string }{
		{"../AGENTS.md", "rule count", `(?i)rejects (\w+) shapes client-side`, "rules"},
		{"../AGENTS.md", "kind count and set", "(?i)\\*\\*Value-bearing kinds\\*\\* — the (\\w+) the renderer reads a top-level `value` from: (.*?) —", "backticks"},
		{"../llms.txt", "rule count", `(?i)rejects all (\w+) client-side`, "rules"},
		{"../llms.txt", "kind count and set", "(?i)value-bearing kinds \\(the (\\w+) whose top-level `value` the renderer reads\\): (.*?) — divider", "words"},
		{"skills/content/mio-skill.md", "rule count", "(?i)(\\w+) of them `pages tree set` now rejects client-side", "rules"},
		{"../docs/internal/api-surface.md", "rule count", `(?i)rejects the (\w+) render-fatal shapes`, "rules"},
		{"../docs/internal/api-surface.md", "kind count", "(?i)one of the (\\w+) kinds that read the TOP-LEVEL `node.value`", "kinds"},
		{"pages_tree_validate.go", "rule count", `(?i)the (\w+) pinned rules`, "rules"},
		{"pages_tree_validate.go", "header kind count and set", `(?i)(\w+) leaf kinds read the TOP-LEVEL value — (.*?) — and`, "comma"},
		{"pages_tree_validate.go", "map comment kind set", `(?i)node.value in mio-hub \(origin/main, src/components/primitives/leaves/\*\): (.*?) each reference node.value`, "set"},
	}
	for _, tc := range tests {
		t.Run(tc.file+"/"+tc.fact, func(t *testing.T) {
			b, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			// Strip Go comment markers before collapsing whitespace in wrapped comments.
			content := string(b)
			if tc.file == "pages_tree_validate.go" {
				content = regexp.MustCompile(`(?m)^\s*// ?`).ReplaceAllString(content, "")
			}
			content = strings.Join(strings.Fields(content), " ")
			match := regexp.MustCompile(tc.pattern).FindStringSubmatch(content)
			if match == nil {
				t.Fatalf("anchor for %s not found in %s — the sentence was reworded or removed; update the anchor, do not delete the check", tc.fact, tc.file)
			}
			if tc.mode == "rules" || tc.mode == "kinds" || tc.mode == "backticks" || tc.mode == "words" || tc.mode == "comma" {
				n, ok := renderContractNumber(match[1])
				if !ok {
					t.Fatalf("%s: invalid count %q", tc.file, match[1])
				}
				want := len(wantKinds)
				if tc.mode == "rules" {
					want = len(pageTreeRules)
				}
				if n != want {
					t.Errorf("%s: %s = %d, validator = %d", tc.file, tc.fact, n, want)
				}
			}
			if tc.mode == "backticks" || tc.mode == "words" || tc.mode == "comma" || tc.mode == "set" {
				list := match[1]
				if tc.mode != "set" {
					list = match[2]
				}
				var kinds []string
				if tc.mode == "backticks" {
					for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(list, -1) {
						kinds = append(kinds, m[1])
					}
				} else {
					list = strings.ReplaceAll(list, ",", " ")
					for _, word := range strings.Fields(list) {
						if word != "and" {
							kinds = append(kinds, word)
						}
					}
				}
				sort.Strings(kinds)
				listCount := len(kinds)
				kinds = slices.Compact(kinds)
				if strings.Join(kinds, "\x00") != strings.Join(wantKinds, "\x00") {
					t.Errorf("%s: %s kinds = %v, validator = %v", tc.file, tc.fact, kinds, wantKinds)
				}
				if tc.mode != "set" {
					n, _ := renderContractNumber(match[1])
					if n != listCount {
						t.Errorf("%s: %s count = %d, list has %d kinds", tc.file, tc.fact, n, listCount)
					}
				}
			}
		})
	}
}

func renderContractNumber(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	for i, word := range []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"} {
		if strings.EqualFold(s, word) {
			return i + 1, true
		}
	}
	return 0, false
}

func TestValidatePageTree_ValueKindMapIsConsumed(t *testing.T) {
	for kind := range topLevelValueKinds {
		t.Run(kind, func(t *testing.T) {
			if err := validatePageTree(treeWith(map[string]any{"kind": kind, "settings": map[string]any{"value": "x"}})); err == nil {
				t.Errorf("%s in topLevelValueKinds must reject misplaced value", kind)
			}
		})
	}
	for _, kind := range []string{"progress-ring", "divider", "unknown-kind"} {
		t.Run(kind, func(t *testing.T) {
			if err := validatePageTree(treeWith(map[string]any{"kind": kind, "settings": map[string]any{"value": "x"}})); err != nil {
				t.Errorf("%s must allow settings.value: %v", kind, err)
			}
		})
	}
}
