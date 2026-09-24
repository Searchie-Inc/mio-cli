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
//     prose (skill and llms.txt) names as examples, and requires the block to
//     name the catalog version it describes (the backend may serve another).
//   - TestSkillPageTemplateKinds_ClassifyCommandAgrees: the skill's one-line
//     command for classifying a template on whatever backend is live is RUN
//     against every embedded page template and must print 0 for exactly the
//     outlines.
//   - TestSkillDataSourceIDs_CoverEveryBoundType: the skill's "Which id each
//     `dataSource` takes" table must have exactly one row per dataSource type
//     the scaffolded recipes bind (MIO-4176: a `file` binding given a playlist
//     id publishes cleanly and 404s on every view), and each row must name the
//     namespace mio-hub's renderer resolves (pinned in dataSourceIDNamespace).
//     A catalog bump that ships a new bound type fails here until someone
//     documents its id namespace.

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/docexamples"
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

// pageKindHeaders opens each list of the generated block, keyed by the word the
// skill's classify command prints for that kind.
var pageKindHeaders = map[string]string{
	"outline":  "Outlines —",
	"complete": "Complete —",
	"system":   "System pages —",
}

var pageIDRE = regexp.MustCompile("`(page-[a-z0-9-]+)`")

// skillPageKinds reads the skill's generated block: for each page template it
// names, the kinds (outline, complete, system) whose list names it.
func skillPageKinds(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(skillDocPath)
	if err != nil {
		t.Fatal(err)
	}
	m := pageKindsBlockRE.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("%s has no catalog-gen:page-template-kinds block", skillDocPath)
	}
	// The lists are only true of the catalog this binary embeds; the backend an
	// agent talks to may serve another (Codex round 1). The block must say which.
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := "catalog " + cat.Meta.CatalogVersion; !strings.Contains(m[1], want) {
		t.Errorf("the page-template-kinds block does not name the catalog it was generated from (%q):\n%s", want, m[1])
	}
	kinds := map[string][]string{}
	current := ""
	for _, line := range strings.Split(m[1], "\n") {
		header := false
		for kind, prefix := range pageKindHeaders {
			if strings.HasPrefix(line, prefix) {
				current, header = kind, true
			}
		}
		if header || current == "" {
			continue
		}
		for _, id := range pageIDRE.FindAllStringSubmatch(line, -1) {
			kinds[id[1]] = append(kinds[id[1]], current)
		}
	}
	if len(kinds) == 0 {
		t.Fatalf("the page-template-kinds block lists no page template under any of %v:\n%s", pageKindHeaders, m[1])
	}
	return kinds
}

// codeRoutedPageTemplates is mio-hub's own list of the page templates it
// routes in code: "code-routed scaffolds … exempt from the 'every root child
// carries a template' rule — they have fixed editable regions" (origin/main
// 335bf971, src/lib/page-tree/README.md, "The section contract"; the same
// list, plus the retired page-search, is in src/lib/page-tree/dev-warnings.ts).
// The renderer lives in another repo, so this is a pinned reading of it, like
// dataSourceIDNamespace below, and it is the oracle the generator's structural
// rule is checked against: the blind review of #137 found page-file-detail —
// three slot placeholders around a locked file-player — filed under "Complete
// — finished sections", because the only rule was "carries a value".
var codeRoutedPageTemplates = map[string]bool{
	"page-login":             true,
	"page-register":          true,
	"page-onboarding":        true,
	"page-account-activity":  true,
	"page-account-profile":   true,
	"page-members":           true,
	"page-file-detail":       true,
	"page-discussions-index": true,
	"page-search":            true,
}

// rootSections counts the root children of an emitted page tree that carry a
// section `template` — the children the backend compiles into sections.
func rootSections(tree map[string]any) int {
	root, _ := tree["root"].(map[string]any)
	kids, _ := root["children"].([]any)
	n := 0
	for _, k := range kids {
		if child, ok := k.(map[string]any); ok {
			if tpl, _ := child["template"].(string); tpl != "" {
				n++
			}
		}
	}
	return n
}

func TestSkillPageTemplateKinds_MatchScaffoldOutput(t *testing.T) {
	kinds := skillPageKinds(t)
	pages := 0
	for _, row := range offlineTemplateRows(t) {
		if row.Kind != "page" {
			continue
		}
		pages++
		tree := scaffoldOffline(t, row.ID, "")
		copyNodes := 0
		walkTree(tree, func(n map[string]any) {
			if hasCopy(n) {
				copyNodes++
			}
		})
		sections := rootSections(tree)
		listed := kinds[row.ID]
		if len(listed) != 1 {
			t.Errorf("page template %s is listed under %v in the skill's page-template-kinds block; want exactly one list (run go generate ./...)", row.ID, listed)
			continue
		}
		kind := listed[0]
		if codeRoutedPageTemplates[row.ID] && kind != "system" {
			t.Errorf("mio-hub routes %s in code (fixed regions, no sections), but the skill lists it as %s, not as a system page", row.ID, kind)
		}
		switch kind {
		case "system":
			if !codeRoutedPageTemplates[row.ID] {
				t.Errorf("the skill calls %s a system page, but mio-hub's list of code-routed scaffolds does not name it: read mio-hub and add it to codeRoutedPageTemplates, or fix the classifier", row.ID)
			}
			if sections > 0 {
				t.Errorf("the skill calls %s a system page (no sections), but %d of its root children carry a section template", row.ID, sections)
			}
		case "complete":
			if sections == 0 {
				t.Errorf("the skill calls %s complete (\"finished sections\"), but no root child of its scaffold carries a section template", row.ID)
			}
			if copyNodes == 0 {
				t.Errorf("the skill calls %s complete, but its scaffold carries no value anywhere — it is an outline", row.ID)
			}
		case "outline":
			if copyNodes > 0 {
				t.Errorf("the skill calls %s an outline, but its scaffold ships %d nodes with a value — it is complete", row.ID, copyNodes)
			}
		}
	}
	if pages == 0 {
		t.Fatal("pages catalog templates listed no page template; nothing was checked")
	}
	if n := len(kinds); n != pages {
		t.Errorf("the skill's lists name %d page templates, the catalog has %d", n, pages)
	}
	// The hand-written prose names these as its examples (the skill's
	// "Composing sections" and the `pages catalog scaffold` line of llms.txt).
	// If a catalog bump moves one, that prose must change with it.
	for id, want := range map[string]string{
		"page-homepage":    "outline",
		"page-sales":       "complete",
		"page-login":       "system",
		"page-file-detail": "system",
	} {
		if got := kinds[id]; len(got) != 1 || got[0] != want {
			t.Errorf("the prose's %s example, %s, is listed under %v — update the skill and llms.txt", want, id, got)
		}
	}
}

// skillClassifyCommand returns the words of the command the skill gives for
// classifying a template the live backend serves, `<id>` still in place.
func skillClassifyCommand(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(skillDocPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range docexamples.FromMarkdown(skillDocPath, string(raw)).Invocations {
		if len(inv.Args) > 3 && strings.Join(inv.Args[:3], " ") == "pages catalog scaffold" &&
			slices.Contains(inv.Args, "<id>") && slices.Contains(inv.Args, "--jq") {
			return inv.Args
		}
	}
	t.Fatalf("%s gives no `mio pages catalog scaffold --template <id> … --jq '…'` command for classifying a live template", skillDocPath)
	return nil
}

// runClassify runs the skill's classify command for template id, plus extra.
func runClassify(t *testing.T, words []string, id string, extra ...string) contractResult {
	t.Helper()
	args := make([]string, 0, len(words)+len(extra))
	for _, w := range words {
		if w == "<id>" {
			w = id
		}
		args = append(args, w)
	}
	return runContract(t, offlineEnv(), append(args, extra...)...)
}

// classifyProbe is a page template the embedded catalog does not have, built so
// that ONE clause of the classify command decides it: a command missing that
// clause prints another word.
type classifyProbe struct {
	id, desc, want string
	starter        map[string]any
}

func probeNode(id, kind string, extra map[string]any) map[string]any {
	n := map[string]any{"id": id, "kind": kind, "settings": map[string]any{}}
	for k, v := range extra {
		n[k] = v
	}
	return n
}

var classifyProbes = []classifyProbe{
	{
		// The non-empty filter. The review's mutation N3 dropped it and every
		// embedded template kept its verdict, because none carries an empty value.
		id: "page-probe-empty-values", want: "outline",
		desc: "whose only values are null, \"\", {} and []",
		starter: probeNode("root", "stack", map[string]any{"children": []any{
			probeNode("hero", "row", map[string]any{"template": "hero", "children": []any{
				probeNode("h", "headline", map[string]any{"value": ""}),
				probeNode("t", "text", map[string]any{"value": nil}),
				probeNode("q", "quote", map[string]any{"value": map[string]any{}}),
				probeNode("u", "text", map[string]any{"value": []any{}}),
			}}),
		}}),
	},
	{
		// The recursion: the one value sits three levels under the section.
		id: "page-probe-deep-copy", want: "complete",
		desc: "whose one value sits three levels down a section",
		starter: probeNode("root", "stack", map[string]any{"children": []any{
			probeNode("r", "row", map[string]any{"template": "row", "children": []any{
				probeNode("c", "stack", map[string]any{"children": []any{
					probeNode("s", "stack", map[string]any{"children": []any{
						probeNode("t", "text", map[string]any{"value": "Placeholder"}),
					}}),
				}}),
			}}),
		}}),
	},
	{
		// page-file-detail's shape: copy, but in regions rather than sections.
		// "system" must win over "complete".
		id: "page-probe-regions-with-copy", want: "system",
		desc: "with copy in untemplated root regions",
		starter: probeNode("root", "stack", map[string]any{"children": []any{
			probeNode("region", "stack", map[string]any{"children": []any{
				probeNode("t", "headline", map[string]any{"value": "Content title"}),
			}}),
		}}),
	},
	{
		// page-generic's shape: no children at all is a blank content page, not
		// a system page with zero regions.
		id: "page-probe-blank", want: "outline",
		desc:    "with no root children",
		starter: probeNode("root", "stack", map[string]any{"children": []any{}}),
	},
	{
		// page-sales has untemplated banners among its sections: ONE root child
		// without a template does not make a page a system page.
		id: "page-probe-banner-and-section", want: "complete",
		desc: "with an untemplated banner beside a filled section",
		starter: probeNode("root", "stack", map[string]any{"children": []any{
			probeNode("b", "banner", map[string]any{"children": []any{}}),
			probeNode("r", "row", map[string]any{"template": "row", "children": []any{
				probeNode("t", "text", map[string]any{"value": "Placeholder"}),
			}}),
		}}),
	},
}

// writeProbeCatalog writes the embedded catalog plus the probes' page templates
// to a file for --catalog (a read-only command warns on the digest and uses it).
func writeProbeCatalog(t *testing.T, probes []classifyProbe) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "internal", "catalog", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat map[string]any
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatal(err)
	}
	pages, _ := cat["pageTemplates"].([]any)
	for _, p := range probes {
		starter := p.starter
		starter["template"] = p.id
		pages = append(pages, map[string]any{
			"id": p.id, "label": p.id, "category": "page", "lifecycle": "active",
			"pageType": "custom", "intent": "classify probe", "defaults": map[string]any{},
			"starter": starter,
		})
	}
	cat["pageTemplates"] = pages
	out, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "probe-catalog.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSkillPageTemplateKinds_ClassifyCommandAgrees RUNS the command the skill
// hands an agent for classifying any template on any backend. Against every
// embedded page template it must print the kind the generated block lists it
// under. Against probe templates served through --catalog, each built so that
// one clause of the command decides it, it must print the kind the skill's
// prose defines — the embedded templates alone cannot tell a command that
// ignores empty values from one that counts them (review finding, mutation N3).
func TestSkillPageTemplateKinds_ClassifyCommandAgrees(t *testing.T) {
	words := skillClassifyCommand(t)
	checked := 0
	for id, listed := range skillPageKinds(t) {
		if len(listed) != 1 {
			continue // MatchScaffoldOutput reports it
		}
		res := runClassify(t, words, id, "--offline")
		if res.Code != errs.ExitOK {
			t.Errorf("the skill's classify command on %s exited %d: %s", id, res.Code, res.Stderr)
			continue
		}
		checked++
		if got := strings.TrimSpace(res.Stdout); got != strconv.Quote(listed[0]) {
			t.Errorf("the skill's classify command printed %q for %s, which the skill lists as %s (it must print %q)", got, id, listed[0], strconv.Quote(listed[0]))
		}
	}
	if checked == 0 {
		t.Fatal("no page template was classified")
	}
	// The skill says the word keeps its quotes even with -o plain, because a
	// scaffold always renders JSON. If scaffold starts honouring -o plain, that
	// sentence (and the quoted words above it) must change.
	if res := runClassify(t, words, "page-login", "--offline", "-o", "plain"); strings.TrimSpace(res.Stdout) != strconv.Quote("system") {
		t.Errorf("with -o plain the skill's classify command printed %q for page-login; the skill says it prints %q even then", strings.TrimSpace(res.Stdout), strconv.Quote("system"))
	}

	catPath := writeProbeCatalog(t, classifyProbes)
	for _, p := range classifyProbes {
		// A probe proves nothing if the scaffold rewrites the shape it is about.
		emitted := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", p.id, "--catalog", catPath)
		if emitted.Code != errs.ExitOK {
			t.Fatalf("scaffold of probe %s exited %d: %s", p.id, emitted.Code, emitted.Stderr)
		}
		var tree map[string]any
		if err := json.Unmarshal([]byte(emitted.Stdout), &tree); err != nil {
			t.Fatalf("scaffold of probe %s is not JSON: %v", p.id, err)
		}
		if p.id == "page-probe-empty-values" {
			empties := 0
			walkTree(tree, func(n map[string]any) {
				if _, has := n["value"]; has && !hasCopy(n) {
					empties++
				}
			})
			if empties != 4 {
				t.Fatalf("the scaffold emitted %d of the empty-values probe's 4 empty values; the probe no longer tests the non-empty filter", empties)
			}
		}
		res := runClassify(t, words, p.id, "--catalog", catPath)
		if res.Code != errs.ExitOK {
			t.Errorf("the skill's classify command on probe %s exited %d: %s", p.id, res.Code, res.Stderr)
			continue
		}
		if got := strings.TrimSpace(res.Stdout); got != strconv.Quote(p.want) {
			t.Errorf("the skill's classify command printed %q for a page %s; want %q", got, p.desc, strconv.Quote(p.want))
		}
	}
}

var dataSourceRowRE = regexp.MustCompile("^\\| `([a-z_-]+)` \\| ([^|]*) \\|")

// skillDataSourceRows returns, per dataSource type, the "`id` is" cell of each
// row the skill's id table has for it.
func skillDataSourceRows(t *testing.T) map[string][]string {
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
	rows := map[string][]string{}
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "#") {
			break
		}
		if m := dataSourceRowRE.FindStringSubmatch(line); m != nil {
			rows[m[1]] = append(rows[m[1]], m[2])
		}
	}
	return rows
}

// dataSourceIDNamespace is the id each bound type takes, as mio-hub's renderer
// resolves it (origin/main 94d28ef8, src/lib/page-tree/data-binding/
// use-data-source.ts; types.ts documents `file` as "id = content_node UUID").
// The renderer lives in another repo, so this is a pinned reading of it, not
// something the test can derive: it exists so the MIO-4176 answer cannot be
// edited back to "media file id" unnoticed. A newly bound type needs an entry,
// taken from the renderer, before the test passes.
var dataSourceIDNamespace = map[string]string{
	"playlist": "playlist",
	"file":     "content-node",
}

var boldRE = regexp.MustCompile(`\*\*([^*]+)\*\*`)

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
		switch len(rows[typ]) {
		case 0:
			t.Errorf("recipes bind dataSource type %q (%s) but the skill's id table has no row saying which id it takes",
				typ, strings.Join(bound[typ], ", "))
			continue
		case 1:
		default:
			t.Errorf("the skill's id table has %d rows for %q", len(rows[typ]), typ)
			continue
		}
		want, known := dataSourceIDNamespace[typ]
		if !known {
			t.Errorf("recipes bind dataSource type %q; read which id mio-hub's useDataSource resolves it as and add it to dataSourceIDNamespace", typ)
			continue
		}
		m := boldRE.FindStringSubmatch(rows[typ][0])
		if m == nil || m[1] != want {
			t.Errorf("the skill's id table says a %q binding takes %q; the renderer resolves it as a **%s** id (MIO-4176)", typ, rows[typ][0], want)
		}
	}
	for typ := range rows {
		if _, ok := bound[typ]; !ok {
			t.Errorf("the skill's id table documents %q, which no scaffolded recipe binds — verify it against the renderer or drop the row", typ)
		}
	}
}

// skillFileBindingRecipe returns the fenced bash block of the skill's "Which id
// each `dataSource` takes" section: the recipe for a `file` binding's id.
func skillFileBindingRecipe(t *testing.T) string {
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
	var block []string
	in := false
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "#") && !in {
			break
		}
		switch {
		case !in && strings.HasPrefix(line, "```bash"):
			in = true
		case in && strings.HasPrefix(line, "```"):
			script := strings.Join(block, "\n")
			if !strings.Contains(script, "mio content children") {
				t.Fatalf("the first bash block of %q no longer lists `content children`:\n%s", heading, script)
			}
			return script
		case in:
			block = append(block, line)
		}
	}
	t.Fatalf("%q has no fenced bash block", heading)
	return ""
}

// contentPagingStub answers the file-binding recipe's requests the way
// mio-backend does (origin/main 2dc04aab): `content reconcile` returns the
// playlist's container, and `content children` pages like admin_list_children —
// page[size] defaults to "20" and must be an integer 1..100 (422 otherwise,
// ContentService.list_children), and page[after] is the last row's id.
type contentPagingStub struct {
	URL    string
	target string // the content-node id of the lesson whose file is file_intro
}

func newContentPagingStub(t *testing.T, lessons, at int) *contentPagingStub {
	t.Helper()
	const team, hub, container = "team_1", "hub_abc123", "cnt_container"
	base := "/api/v1/teams/" + team + "/hubs/" + hub + "/content"
	ids := make([]string, lessons)
	fileIDs := make([]string, lessons)
	for i := range ids {
		ids[i] = fmt.Sprintf("cnt_lesson_%03d", i+1)
		fileIDs[i] = fmt.Sprintf("file_%03d", i+1)
	}
	fileIDs[at-1] = "file_intro"
	write := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/teams":
			write(w, 200, map[string]any{"data": []any{
				map[string]any{"type": "teams", "id": team, "attributes": map[string]any{"name": "Team"}},
			}})
		case r.Method == http.MethodPost && r.URL.Path == base+"/reconcile":
			results := []any{map[string]any{"legacy_hash": "h", "node_type": "container", "outcome": "created", "node_id": container}}
			for _, id := range ids {
				results = append(results, map[string]any{"legacy_hash": "h" + id, "node_type": "lesson", "outcome": "created", "node_id": id})
			}
			write(w, 200, map[string]any{"data": map[string]any{
				"type": "content_node_reconciliations", "id": hub,
				"attributes": map[string]any{"hub_id": hub, "results": results},
			}})
		case r.Method == http.MethodGet && r.URL.Path == base+"/"+container+"/children":
			size, err := strconv.Atoi(cmp.Or(q.Get("page[size]"), "20"))
			if err != nil || size < 1 || size > 100 {
				write(w, 422, map[string]any{"errors": []any{map[string]any{
					"status": "422", "code": "invalid_query_param", "title": "Invalid query parameter",
					"detail": "page size must be between 1 and 100",
				}}})
				return
			}
			start := 0
			if after := q.Get("page[after]"); after != "" {
				start = slices.Index(ids, after) + 1
			}
			end := min(start+size, lessons)
			data := []any{}
			for i := start; i < end; i++ {
				data = append(data, map[string]any{"type": "content_nodes", "id": ids[i], "attributes": map[string]any{
					"node_type": "lesson", "title": "Lesson " + ids[i], "file_id": fileIDs[i],
				}})
			}
			write(w, 200, map[string]any{"data": data, "meta": map[string]any{"page": map[string]any{"size": size, "has_more": end < lessons}}})
		default:
			t.Errorf("the file-binding recipe sent an unexpected request: %s %s", r.Method, r.URL)
			write(w, 404, map[string]any{"errors": []any{map[string]any{"status": "404", "title": "Not Found"}}})
		}
	}))
	t.Cleanup(srv.Close)
	return &contentPagingStub{URL: srv.URL, target: ids[at-1]}
}

// TestSkillFileBindingRecipe_RunsAsWritten runs the skill's recipe for a
// `file` binding's id — the bash block under "Which id each `dataSource`
// takes", verbatim, through bash and a built mio — against a stub that pages
// `content children` the way the backend does. The blind review of #137 found
// that the recipe read one default page of 20: a lesson past #20 came back as
// an EMPTY NODE_ID with exit 0, and an empty dataSource.id publishes and
// renders an empty section, which is MIO-4176's own symptom. The recipe must
// find a lesson past the default page, and when a lesson is out of its reach
// it must fail rather than hand back "".
func TestSkillFileBindingRecipe_RunsAsWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the recipe is a bash script")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("the recipe is a bash script and there is no bash: %v", err)
	}
	script := skillFileBindingRecipe(t)
	bin := buildBinary(t)

	for _, tc := range []struct {
		name        string
		lessons, at int
		reachable   bool
	}{
		{"lesson 25 of 30, past the default page of 20", 30, 25, true},
		{"lesson 130 of 150, past the largest page of 100", 150, 130, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newContentPagingStub(t, tc.lessons, tc.at)
			home := t.TempDir()
			cmd := exec.Command(bash, "-c", script+"\nprintf 'NODE_ID=%s\\n' \"$NODE_ID\"\n")
			cmd.Env = []string{
				"PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME=" + home,
				"USERPROFILE=" + home,
				"MIO_API_KEY=test-key-contract",
				"MIO_API_BASE_URL=" + stub.URL,
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run()
			code := cmd.ProcessState.ExitCode()
			got := ""
			for _, line := range strings.Split(stdout.String(), "\n") {
				if v, ok := strings.CutPrefix(line, "NODE_ID="); ok {
					got = v
				}
			}
			switch {
			case got == stub.target && code == 0:
				// found it, however far it had to page
			case tc.reachable:
				t.Errorf("the skill's file-binding recipe, run as written against %d lessons with file_intro at #%d: exit %d, NODE_ID=%q; want exit 0 and %q. stderr=%q",
					tc.lessons, tc.at, code, got, stub.target, stderr.String())
			case code == 0:
				t.Errorf("the skill's file-binding recipe, run as written against %d lessons with file_intro at #%d, exited 0 with NODE_ID=%q: a lesson it cannot reach must fail it, not hand back an id that publishes an empty section (MIO-4176). stderr=%q",
					tc.lessons, tc.at, got, stderr.String())
			case !strings.Contains(stderr.String(), "--after"):
				t.Errorf("the skill's file-binding recipe failed for a lesson past its page (exit %d) without saying how to page on with --after. stderr=%q", code, stderr.String())
			}
		})
	}
}
