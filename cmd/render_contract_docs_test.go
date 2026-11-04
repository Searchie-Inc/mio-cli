package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
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
	tests := renderContractFacts
	for _, tc := range tests {
		t.Run(tc.file+"/"+tc.fact, func(t *testing.T) {
			content := renderContractSurface(t, tc.file)
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

var renderContractFacts = []struct{ file, fact, pattern, mode string }{
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
	// Second and third copies of the same count in the same files (review of MIO-2883):
	{"../AGENTS.md", "allowlist size", `(?i)allowlists exactly (\w+) kinds`, "kinds"},
	{"../llms.txt", "allowlist size", `(?i)allowlist has (\w+) entries`, "kinds"},
	{"pages_tree_validate.go", "allowlist size", `(?i)ALLOWLIST of the (\w+) kinds verified`, "kinds"},
}

// renderContractSurface reads a doc surface (or the validator source) with Go
// comment markers stripped and whitespace collapsed, so wrapped prose matches.
func renderContractSurface(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	if strings.HasSuffix(file, ".go") {
		content = regexp.MustCompile(`(?m)^\s*// ?`).ReplaceAllString(content, "")
	}
	return strings.Join(strings.Fields(content), " ")
}

// TestRenderContractDocs_EveryCountMentionIsPinned closes the "probe set smaller
// than the claim" hole (review of MIO-2883): each surface repeats the counts, and
// an anchor per sentence leaves the other copies free to drift. Every
// "<number> kinds|entries|shapes|rules" mention on a covered surface must lie
// inside the span of one of the anchored facts above; a new mention fails until
// it is anchored (or, if it is about something else, named in unrelatedCounts).
func TestRenderContractDocs_EveryCountMentionIsPinned(t *testing.T) {
	mention := regexp.MustCompile(`(?i)\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|\d+)\s+(?:leaf\s+|value-bearing\s+|pinned\s+|render-fatal\s+)?(?:kinds|entries|shapes|rules)\b`)
	// Counts that are about other things than the page-tree render contract.
	unrelatedCounts := []string{"kinds the catalog deliberately omit", "come in three kinds"}
	files := map[string]bool{}
	for _, f := range renderContractFacts {
		files[f.file] = true
	}
	files["skills/content/mio-skill.md"] = true
	for file := range files {
		t.Run(file, func(t *testing.T) {
			content := renderContractSurface(t, file)
			var spans [][2]int
			for _, f := range renderContractFacts {
				if f.file != file {
					continue
				}
				if loc := regexp.MustCompile(f.pattern).FindStringIndex(content); loc != nil {
					spans = append(spans, [2]int{loc[0], loc[1]})
				}
			}
			for _, m := range mention.FindAllStringIndex(content, -1) {
				covered := false
				for _, sp := range spans {
					if m[0] >= sp[0] && m[1] <= sp[1] {
						covered = true
					}
				}
				ctx := content[max(0, m[0]-40):min(len(content), m[1]+40)]
				for _, u := range unrelatedCounts {
					if strings.Contains(ctx, u) {
						covered = true
					}
				}
				if !covered {
					t.Errorf("%s: %q states a count that no anchored fact pins — anchor it in renderContractFacts (or, if it is not about the render contract, add it to unrelatedCounts)", file, ctx)
				}
			}
		})
	}
}

// TestPageTreeRules_AreTheOnlyPlaceRulesLive: the docs' rule count is compared to
// len(pageTreeRules), which is only an oracle if no rejection exists outside it.
// validatePageNode may therefore not construct an error itself — every rejection
// must come from a rule — and the rules' names are tied to what the docs say.
func TestPageTreeRules_AreTheOnlyPlaceRulesLive(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pages_tree_validate.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "validatePageNode" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "errs" {
						t.Errorf("validatePageNode builds its own error (errs.%s at %s) — a rejection outside pageTreeRules makes the docs' rule count wrong; add it as a rule", sel.Sel.Name, fset.Position(call.Pos()))
					}
				}
			}
			return true
		})
	}
	if !found {
		t.Fatal("validatePageNode not found — update this guard")
	}

	// Each rule must be described on the agent-facing surfaces. The keyword is
	// what a reader would search for; a replaced or renamed rule fails here.
	docKeyword := map[string]string{"weight": "weight", "template": "template", "misplaced-value": "settings.value"}
	var names []string
	for _, r := range pageTreeRules {
		names = append(names, r.name)
		kw, ok := docKeyword[r.name]
		if !ok {
			t.Errorf("rule %q has no documented keyword — describe it in AGENTS.md/llms.txt/the skill and add it to docKeyword", r.name)
			continue
		}
		for _, f := range []string{"../AGENTS.md", "../llms.txt", "skills/content/mio-skill.md"} {
			if !strings.Contains(renderContractSurface(t, f), kw) {
				t.Errorf("%s never mentions %q, the keyword of rule %q", f, kw, r.name)
			}
		}
	}
	if len(docKeyword) != len(pageTreeRules) {
		t.Errorf("docKeyword names %d rules, pageTreeRules has %d (%v)", len(docKeyword), len(pageTreeRules), names)
	}
	// Order is part of the contract: the first violation wins.
	if got := strings.Join(names, ","); got != "weight,template,misplaced-value" {
		t.Errorf("rule order = %s; a node that breaks two rules reports the first, so a reorder changes errors — update the docs/tests deliberately", got)
	}
}
