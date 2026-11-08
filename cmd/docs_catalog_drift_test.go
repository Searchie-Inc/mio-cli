package cmd

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/docsgen"
)

const skillDocPath = "skills/content/mio-skill.md"

func TestSkillDocIsGeneratedFromCatalog(t *testing.T) {
	onDisk, err := os.ReadFile(skillDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", skillDocPath, err)
	}

	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	blocks, err := docsgen.Render(cat)
	if err != nil {
		t.Fatalf("docsgen.Render against embedded catalog %s: %v", cat.Meta.CatalogVersion, err)
	}
	want, err := docsgen.Apply(string(onDisk), blocks)
	if err != nil {
		t.Fatalf("docsgen.Apply: %v", err)
	}

	if want == string(onDisk) {
		return
	}
	t.Errorf("%s is STALE against the embedded catalog (%s).\n"+
		"Fix: go generate ./...\n"+
		"Stale generated block(s): %s\n"+
		"This is not cosmetic — an agent that authors a property or enum value the "+
		"renderer does not know gets a 200 and a blank page.",
		skillDocPath, cat.Meta.CatalogVersion, strings.Join(staleBlocks(string(onDisk), want), ", "))
}

func staleBlocks(got, want string) []string {
	gotB, wantB := generatedBlockBodies(got), generatedBlockBodies(want)
	var stale []string
	for _, name := range docsgen.BlockNames {
		if gotB[name] != wantB[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) == 0 {
		stale = []string{"(none — the difference is outside a generated block, which should be impossible)"}
	}
	return stale
}

var genBlockRe = regexp.MustCompile(`(?s)<!-- catalog-gen:([a-z-]+) -->\n(.*?)<!-- /catalog-gen -->`)

func generatedBlockBodies(doc string) map[string]string {
	out := map[string]string{}
	for _, m := range genBlockRe.FindAllStringSubmatch(doc, -1) {
		out[m[1]] = m[2]
	}
	return out
}

var valueBearingKinds = []string{
	"headline", "text", "image", "video", "button", "icon", "divider",
	"progress-ring", "quote",
	"countdown", "logo", "theme-toggle", "plan-card", "plan-price",
	"doodle", "featured-icon",
}

func TestSkillDocValueBearingKinds(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	kinds, ok := cat.Raw()["nodeKinds"].(map[string]any)
	if !ok || len(kinds) == 0 {
		t.Fatalf("catalog: nodeKinds is missing or empty (%T)", cat.Raw()["nodeKinds"])
	}

	documented := map[string]bool{}
	for _, k := range valueBearingKinds {
		documented[k] = true
	}

	for _, kind := range valueBearingKinds {
		spec, present := kinds[kind]
		if !present {
			t.Errorf("the skill documents %q as carrying a top-level `value`, but the catalog (%s) has no such node kind — it was renamed or removed", kind, cat.Meta.CatalogVersion)
			continue
		}
		m, _ := spec.(map[string]any)
		if rules, _ := m["childRules"].(string); rules != "none" {
			t.Errorf("node kind %q is documented as value-bearing but the catalog declares childRules=%q; a container does not carry a `value`", kind, rules)
		}
	}

	observed := map[string]bool{}
	collectValueBearing(cat.Raw(), observed)
	if len(observed) == 0 {
		t.Errorf("no node in the catalog carries a top-level `value` — the recipe scan is broken, so check (3) is guarding nothing")
	}
	for _, kind := range sortedSet(observed) {
		if !documented[kind] {
			t.Errorf("catalog recipes put a top-level `value` on a %q node, but the skill does not list %q as value-bearing — re-read LeafKind in mio-hub src/lib/page-tree/types.ts and update the table", kind, kind)
		}
	}
}

func collectValueBearing(v any, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		if _, hasValue := t["value"]; hasValue {
			if kind, ok := t["kind"].(string); ok && kind != "" {
				out[kind] = true
			}
		}
		for _, child := range t {
			collectValueBearing(child, out)
		}
	case []any:
		for _, child := range t {
			collectValueBearing(child, out)
		}
	case json.Number, string, bool, nil:
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
