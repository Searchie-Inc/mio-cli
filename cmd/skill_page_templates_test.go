package cmd

// skill_page_templates_test.go — two claims the agent skill makes about what
// `mio pages catalog scaffold` hands an agent, checked against the scaffold
// output itself rather than against the generator or a second list.
//
//   - TestSkillPageTemplateKinds_MatchScaffoldOutput: the skill used to call
//     EVERY page template "an outline, not finished sections" (skill and
//     llms.txt), which is wrong for page-sales — 17 finished sections with
//     placeholder copy. The split is now a generated block
//     (catalog-gen:page-template-kinds). This test scaffolds every page
//     template through the real command and requires each to sit in the list
//     its OUTPUT puts it in: no non-empty `value` anywhere → Outlines,
//     otherwise → Complete. It also pins the two templates the hand-written
//     prose (skill and llms.txt) names as examples.
//   - TestSkillDataSourceIDs_CoverEveryBoundType: the skill's "Which id each
//     `dataSource` takes" table must have exactly one row per dataSource type
//     the scaffolded recipes bind (MIO-4176: a `file` binding given a playlist
//     id publishes cleanly and 404s on every view). A catalog bump that ships a
//     new bound type fails here until someone documents its id namespace.

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// scaffoldOffline runs `mio pages catalog scaffold` against the embedded
// catalog and returns the emitted tree.
func scaffoldOffline(t *testing.T, template, variant string) map[string]any {
	t.Helper()
	args := []string{"pages", "catalog", "scaffold", "--template", template, "--offline"}
	if variant != "" {
		args = append(args, "--variant", variant)
	}
	res := runContract(t, offlineEnv(), args...)
	if res.Code != errs.ExitOK {
		t.Fatalf("scaffold %s %s: exit %d, stderr=%q", template, variant, res.Code, res.Stderr)
	}
	var tree map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &tree); err != nil {
		t.Fatalf("scaffold %s: stdout is not JSON: %v", template, err)
	}
	return tree
}

// catalogTemplateRow is one row of `mio pages catalog templates -o json`.
type catalogTemplateRow struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Variants string `json:"variants"`
}

func offlineTemplateRows(t *testing.T) []catalogTemplateRow {
	t.Helper()
	res := runContract(t, offlineEnv(), "pages", "catalog", "templates", "--offline", "-o", "json")
	if res.Code != errs.ExitOK {
		t.Fatalf("pages catalog templates: exit %d, stderr=%q", res.Code, res.Stderr)
	}
	var rows []catalogTemplateRow
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("pages catalog templates: stdout is not a JSON array: %v", err)
	}
	return rows
}

// walkTree calls fn on every node of an emitted tree (the {"root":…} wrapper
// of a page template, or the bare node of a section template).
func walkTree(v any, fn func(map[string]any)) {
	n, ok := v.(map[string]any)
	if !ok {
		return
	}
	if root, ok := n["root"]; ok && n["kind"] == nil {
		walkTree(root, fn)
		return
	}
	fn(n)
	if kids, ok := n["children"].([]any); ok {
		for _, k := range kids {
			walkTree(k, fn)
		}
	}
}

// hasCopy reports whether a node's `value` is copy an author would edit: not
// absent, null, "" or an empty object/array.
func hasCopy(n map[string]any) bool {
	switch v := n["value"].(type) {
	case nil:
		return false
	case string:
		return v != ""
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	default:
		return true
	}
}

var pageKindsBlockRE = regexp.MustCompile(`(?s)<!-- catalog-gen:page-template-kinds -->\n(.*?)<!-- /catalog-gen -->`)

// skillPageKinds reads the Outlines/Complete lists out of the skill's block.
func skillPageKinds(t *testing.T) (outlines, complete map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(skillDocPath)
	if err != nil {
		t.Fatal(err)
	}
	m := pageKindsBlockRE.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("%s has no catalog-gen:page-template-kinds block", skillDocPath)
	}
	parts := strings.SplitN(m[1], "Complete", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "Outlines") {
		t.Fatalf("page-template-kinds block has no Outlines/Complete sections:\n%s", m[1])
	}
	ids := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, id := range regexp.MustCompile("`(page-[a-z0-9-]+)`").FindAllStringSubmatch(s, -1) {
			out[id[1]] = true
		}
		return out
	}
	return ids(parts[0]), ids(parts[1])
}

func TestSkillPageTemplateKinds_MatchScaffoldOutput(t *testing.T) {
	outlines, complete := skillPageKinds(t)
	pages := 0
	for _, row := range offlineTemplateRows(t) {
		if row.Kind != "page" {
			continue
		}
		pages++
		copyNodes := 0
		walkTree(scaffoldOffline(t, row.ID, ""), func(n map[string]any) {
			if hasCopy(n) {
				copyNodes++
			}
		})
		inOutlines, inComplete := outlines[row.ID], complete[row.ID]
		switch {
		case !inOutlines && !inComplete:
			t.Errorf("page template %s is in neither list of the skill's page-template-kinds block (run go generate ./...)", row.ID)
		case copyNodes == 0 && !inOutlines:
			t.Errorf("the skill calls %s complete, but its scaffold carries no value anywhere — it is an outline", row.ID)
		case copyNodes > 0 && !inComplete:
			t.Errorf("the skill calls %s an outline, but its scaffold ships %d nodes with a value — it is complete", row.ID, copyNodes)
		}
	}
	if pages == 0 {
		t.Fatal("pages catalog templates listed no page template; nothing was checked")
	}
	if n := len(outlines) + len(complete); n != pages {
		t.Errorf("the skill's lists name %d page templates, the catalog has %d", n, pages)
	}
	// The hand-written prose names these two as its examples (the skill's
	// "Composing sections" and the `pages catalog scaffold` line of llms.txt).
	// If a catalog bump moves either, that prose must change with it.
	if !outlines["page-homepage"] {
		t.Error("the prose's outline example, page-homepage, is no longer an outline — update the skill and llms.txt")
	}
	if !complete["page-sales"] {
		t.Error("the prose's complete example and the sales recipe, page-sales, is no longer complete — update the skill and llms.txt")
	}
}

var dataSourceRowRE = regexp.MustCompile("^\\| `([a-z_-]+)` \\|")

// skillDataSourceRows returns the dataSource types the skill's id table
// documents, one per row.
func skillDataSourceRows(t *testing.T) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(skillDocPath)
	if err != nil {
		t.Fatal(err)
	}
	const heading = "#### Which id each `dataSource` takes"
	_, section, ok := strings.Cut(string(raw), heading+"\n")
	if !ok {
		t.Fatalf("%s has no %q section", skillDocPath, heading)
	}
	rows := map[string]int{}
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "#") {
			break
		}
		if m := dataSourceRowRE.FindStringSubmatch(line); m != nil {
			rows[m[1]]++
		}
	}
	return rows
}

func TestSkillDataSourceIDs_CoverEveryBoundType(t *testing.T) {
	bound := map[string][]string{} // dataSource.type → where a recipe binds it
	for _, row := range offlineTemplateRows(t) {
		variants := []string{""}
		if row.Variants != "" {
			variants = append(variants, strings.Split(row.Variants, ",")...)
		}
		for _, v := range variants {
			walkTree(scaffoldOffline(t, row.ID, v), func(n map[string]any) {
				if ds, ok := n["dataSource"].(map[string]any); ok {
					if typ, _ := ds["type"].(string); typ != "" {
						where := row.ID
						if v != "" {
							where += " --variant " + v
						}
						bound[typ] = append(bound[typ], where)
					}
				}
			})
		}
	}
	if len(bound) == 0 {
		t.Fatal("no scaffolded recipe binds a dataSource; the walk is broken")
	}

	rows := skillDataSourceRows(t)
	var types []string
	for typ := range bound {
		types = append(types, typ)
	}
	sort.Strings(types)
	for _, typ := range types {
		switch rows[typ] {
		case 0:
			t.Errorf("recipes bind dataSource type %q (%s) but the skill's id table has no row saying which id it takes",
				typ, strings.Join(bound[typ], ", "))
		case 1:
		default:
			t.Errorf("the skill's id table has %d rows for %q", rows[typ], typ)
		}
	}
	for typ := range rows {
		if _, ok := bound[typ]; !ok {
			t.Errorf("the skill's id table documents %q, which no scaffolded recipe binds — verify it against the renderer or drop the row", typ)
		}
	}
}
