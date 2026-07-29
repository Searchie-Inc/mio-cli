package cmd

// pages_catalog_test.go — the `mio pages catalog` group (MIO-2340): scaffold
// node-trees from catalog template recipes (the tree/publish-door artifact) and
// inspect the catalog (writable section types, recommended templates per page
// type). Tests run with --offline so they exercise the embedded, digest-pinned
// vendored catalog hermetically (no network, no cache writes); the live-fetch /
// cache / digest paths are covered by internal/catalog + internal/client.

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// executeCLI runs the command tree in-process (like runContract) but returns the
// raw error so a test can assert on the error MESSAGE — the SilenceErrors root
// never writes it to the stderr buffer, so res.Stderr cannot carry it.
func executeCLI(t *testing.T, env []string, args ...string) error {
	t.Helper()
	restore := overlayEnv(t, env)
	defer restore()
	resetGlobalFlags()
	root := RootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	defer root.SetArgs(nil)
	return root.Execute()
}

var uuidV7Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// offlineEnv is a minimal env for catalog commands: no server is contacted when
// --offline is passed, so the base URL is a sentinel that must never be dialed.
func offlineEnv() []string {
	return []string{"MIO_API_BASE_URL=http://catalog.invalid"}
}

func parseTreeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var tree map[string]any
	if err := json.Unmarshal([]byte(s), &tree); err != nil {
		t.Fatalf("scaffold stdout is not valid JSON: %v\n%s", err, s)
	}
	return tree
}

// collectIDs walks a scaffolded tree collecting every node id (pre-order).
func collectIDs(n map[string]any) []string {
	var out []string
	if id, ok := n["id"].(string); ok {
		out = append(out, id)
	}
	if children, ok := n["children"].([]any); ok {
		for _, c := range children {
			if cm, ok := c.(map[string]any); ok {
				out = append(out, collectIDs(cm)...)
			}
		}
	}
	return out
}

func TestCatalogScaffold_PageTemplate_EmitsRootWrappedTree(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "page-homepage", "--offline")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	tree := parseTreeJSON(t, res.Stdout)
	root, ok := tree["root"].(map[string]any)
	if !ok {
		t.Fatalf("page scaffold must be wrapped as {\"root\": …}; got keys %v", treeKeys(tree))
	}
	if root["kind"] != "stack" || root["template"] != "page-homepage" {
		t.Errorf("root kind/template = %v/%v, want stack/page-homepage", root["kind"], root["template"])
	}
	ids := collectIDs(root)
	if len(ids) == 0 {
		t.Fatal("no node ids in scaffolded tree")
	}
	for _, id := range ids {
		if !uuidV7Pattern.MatchString(id) {
			t.Errorf("node id %q is not a fresh UUIDv7", id)
		}
	}
}

func TestCatalogScaffold_SectionTemplate_EmitsBareNode(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "hero", "--offline")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	node := parseTreeJSON(t, res.Stdout)
	if _, wrapped := node["root"]; wrapped {
		t.Error("section scaffold must be a bare node, not wrapped in root")
	}
	if node["template"] != "hero" {
		t.Errorf("node template = %v, want hero", node["template"])
	}
	if id, _ := node["id"].(string); !uuidV7Pattern.MatchString(id) {
		t.Errorf("root id %q is not a UUIDv7", id)
	}
}

func TestCatalogScaffold_ValidVariant(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "hero", "--variant", "playlist", "--offline")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	node := parseTreeJSON(t, res.Stdout)
	if node["template"] != "hero" {
		t.Errorf("variant scaffold template = %v, want hero", node["template"])
	}
}

func TestCatalogScaffold_ScrollAliasResolvesToCompact(t *testing.T) {
	// MIO-2681: "scroll" is a CLI-side discoverability alias for the compact
	// template (picker label renamed "Compact" -> "Scroll" at 0.9.1; the id
	// itself is a stored contract and stays "compact"). The scaffolded node's
	// embedded "template" attribute — baked into the catalog recipe itself,
	// not derived from what the caller typed — must read the real id.
	res := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "scroll", "--offline")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	node := parseTreeJSON(t, res.Stdout)
	if node["template"] != "compact" {
		t.Errorf(`scaffold --template scroll: node["template"] = %v, want "compact"`, node["template"])
	}
	// Same recipe as scaffolding by the real id directly.
	direct := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "compact", "--offline")
	if direct.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", direct.Code, direct.Stderr)
	}
	directNode := parseTreeJSON(t, direct.Stdout)
	if node["template"] != directNode["template"] {
		t.Errorf("scroll alias and compact direct scaffold disagree on template: %v vs %v", node["template"], directNode["template"])
	}
}

func TestCatalogTemplates_DoesNotListScrollAlias(t *testing.T) {
	// Listings show only the real catalog id — the alias is a lookup-time
	// convenience, never a first-class id.
	res := runContract(t, offlineEnv(), "pages", "catalog", "templates", "--offline", "--output", "json")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stdout, `"scroll"`) {
		t.Errorf(`templates listing must not include the "scroll" alias as an id; got %s`, res.Stdout)
	}
	if !strings.Contains(res.Stdout, `"compact"`) {
		t.Errorf(`templates listing should still include the real id "compact"; got %s`, res.Stdout)
	}
}

func TestCatalogScaffold_UnknownTemplate_ExitUsage(t *testing.T) {
	err := executeCLI(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "does-not-exist", "--offline")
	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	if err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should name the unknown template; got %v", err)
	}
}

func TestCatalogScaffold_UnknownVariant_ExitUsage(t *testing.T) {
	err := executeCLI(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "hero", "--variant", "nope", "--offline")
	if codeForExecuteErr(err) != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); err=%v", codeForExecuteErr(err), errs.ExitUsage, err)
	}
	// The error should list the valid variants (hero has playlist, file).
	if err == nil || !strings.Contains(err.Error(), "playlist") {
		t.Errorf("error should list valid variants; got %v", err)
	}
}

func TestCatalogScaffold_VariantOnVariantlessTemplate_ExitUsage(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--template", "carousel", "--variant", "x", "--offline")
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestCatalogScaffold_MissingTemplate_ExitUsage(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "scaffold", "--offline")
	if res.Code != errs.ExitUsage {
		t.Fatalf("exit = %d, want %d (ExitUsage); stderr=%q", res.Code, errs.ExitUsage, res.Stderr)
	}
}

func TestCatalogSectionTypes_WritableOnly(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "section-types", "--writable-only", "--offline", "--output", "json")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("stdout not a JSON array: %v\n%s", err, res.Stdout)
	}
	if len(rows) != 7 {
		t.Errorf("writable section types = %d, want 7", len(rows))
	}
	found := false
	for _, r := range rows {
		if id, _ := r["id"].(string); id == "compact" {
			found = true
		}
	}
	if !found {
		t.Error("compact (writable=true as of 0.10.0, MIO-2681) must appear in --writable-only output")
	}
}

func TestCatalogSectionTypes_AllNine(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "section-types", "--offline", "--output", "json")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, `"feature"`) || !strings.Contains(res.Stdout, `"compact"`) {
		t.Errorf("full section-types list should include both writable and non-writable ids; got %s", res.Stdout)
	}
}

func TestCatalogTemplates_ForPageType_RecommendedOrdered(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "templates", "--page-type", "homepage", "--offline", "--output", "json")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
		t.Fatalf("stdout not a JSON array: %v\n%s", err, res.Stdout)
	}
	// First section template recommended for homepage is hero (order 10);
	// content-card (applicablePageTypes: []) must not appear.
	var sectionIDs []string
	for _, r := range rows {
		if r["kind"] == "section" {
			sectionIDs = append(sectionIDs, r["id"].(string))
		}
	}
	if len(sectionIDs) == 0 || sectionIDs[0] != "hero" {
		t.Errorf("first recommended section = %v, want hero first; ids=%v", firstOr(sectionIDs), sectionIDs)
	}
	for _, id := range sectionIDs {
		if id == "content-card" {
			t.Error("content-card (no applicablePageTypes) must not be recommended")
		}
	}
}

func TestCatalogTemplates_All(t *testing.T) {
	res := runContract(t, offlineEnv(), "pages", "catalog", "templates", "--offline", "--output", "json")
	if res.Code != errs.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, `"hero"`) || !strings.Contains(res.Stdout, `"page-homepage"`) {
		t.Errorf("full templates list should include section + page templates; got %s", res.Stdout)
	}
}

func treeKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func firstOr(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	return ss[0]
}
