package cmd

// docs_catalog_drift_test.go — keeps the page-builder VOCABULARY documented in
// the embedded agent skill (`cmd/skills/content/mio-skill.md`) in lockstep with
// the embedded page-builder catalog (MIO-2664, MIO-2539).
//
// WHY THIS TEST EXISTS
// The skill has to name the node kinds, compiled section types, section
// templates and `row` layout variants an author can use — an agent cannot
// discover them from a 200 (an unknown kind is accepted by the API and then
// silently dropped by the renderer). Those four lists are the ONLY parts of the
// render contract that are machine-derivable: `catalog.json` ships them, but
// carries no settings schema at all (`settingsSchema` is empty and 0 of the
// nodeKinds entries declare settings), so everything else in the doc is
// hand-written from verified frontend behaviour and cannot be checked here.
//
// The catalog is re-pinned automatically (`internal/catalog/CATALOG_REF` + the
// catalog-pin-staleness workflow), and a pin bump is exactly how a hand-copied
// list rots — MIO-2741 was a stale-pin bug of that shape. So the doc marks each
// list with an HTML comment marker and this test fails the build when the
// marked list and the embedded catalog disagree in either direction. The fix is
// always to edit the doc; never to relax the test.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// catalogSyncBlockRe matches one marked list in the skill:
//
//	<!-- catalog-sync:node-kinds -->
//	`accordion` · `button` · …
//	<!-- /catalog-sync -->
//
// The block's items are every backtick-quoted token inside it, so the prose
// around them (separators, line breaks, a trailing sentence) is free-form.
var catalogSyncBlockRe = regexp.MustCompile(`(?s)<!-- catalog-sync:([a-z-]+) -->(.*?)<!-- /catalog-sync -->`)

var catalogSyncItemRe = regexp.MustCompile("`([^`]+)`")

// catalogSyncBlocks extracts every marked list from the skill body, keyed by
// marker name.
func catalogSyncBlocks(t *testing.T, body string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, m := range catalogSyncBlockRe.FindAllStringSubmatch(body, -1) {
		name := m[1]
		if _, dup := out[name]; dup {
			t.Fatalf("catalog-sync marker %q appears twice in mio-skill.md; each list must be marked exactly once", name)
		}
		var items []string
		for _, im := range catalogSyncItemRe.FindAllStringSubmatch(m[2], -1) {
			items = append(items, strings.TrimSpace(im[1]))
		}
		sort.Strings(items)
		out[name] = items
	}
	return out
}

// diffSets reports the symmetric difference between the documented and the
// catalog-derived list, formatted for a failure message.
func diffSets(documented, actual []string) (missing, extra []string) {
	have := map[string]bool{}
	for _, d := range documented {
		have[d] = true
	}
	want := map[string]bool{}
	for _, a := range actual {
		want[a] = true
		if !have[a] {
			missing = append(missing, a)
		}
	}
	for _, d := range documented {
		if !want[d] {
			extra = append(extra, d)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func TestSkillDoc_CatalogVocabularyMatchesEmbeddedCatalog(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}

	// nodeKinds is not projected onto the Catalog struct (the CLI never needs it
	// at runtime), so read it off the raw document.
	rawKinds, ok := cat.Raw()["nodeKinds"].(map[string]any)
	if !ok {
		t.Fatalf("catalog: nodeKinds is not an object (got %T)", cat.Raw()["nodeKinds"])
	}
	nodeKinds := make([]string, 0, len(rawKinds))
	for k := range rawKinds {
		nodeKinds = append(nodeKinds, k)
	}

	sectionTypes := make([]string, 0, len(cat.SectionTypes))
	for _, st := range cat.SectionTypes {
		sectionTypes = append(sectionTypes, st.ID)
	}

	sectionTemplates := make([]string, 0, len(cat.Templates))
	for _, tpl := range cat.Templates {
		sectionTemplates = append(sectionTemplates, tpl.ID)
	}

	pageTemplates := make([]string, 0, len(cat.PageTemplates))
	for _, tpl := range cat.PageTemplates {
		pageTemplates = append(pageTemplates, tpl.ID)
	}

	rowTpl, ok := cat.TemplateByID("row")
	if !ok {
		t.Fatalf("catalog: no 'row' template — the skill documents its layout variants")
	}

	want := map[string][]string{
		"node-kinds":        nodeKinds,
		"section-types":     sectionTypes,
		"section-templates": sectionTemplates,
		"page-templates":    pageTemplates,
		"row-variants":      rowTpl.VariantKeys(),
	}

	blocks := catalogSyncBlocks(t, skillBody)

	for name, actual := range want {
		documented, present := blocks[name]
		if !present {
			t.Errorf("mio-skill.md has no <!-- catalog-sync:%s --> block; the %s list must stay marked so this test can guard it", name, name)
			continue
		}
		missing, extra := diffSets(documented, actual)
		if len(missing) == 0 && len(extra) == 0 {
			continue
		}
		var detail []string
		if len(missing) > 0 {
			detail = append(detail, fmt.Sprintf("in the catalog but NOT documented: %v", missing))
		}
		if len(extra) > 0 {
			detail = append(detail, fmt.Sprintf("documented but NOT in the catalog: %v", extra))
		}
		t.Errorf("mio-skill.md catalog-sync:%s is stale against the embedded catalog (%s): %s\n"+
			"Fix the doc list (an agent that authors a kind the renderer does not know gets a 200 and a blank page).",
			name, cat.Meta.CatalogVersion, strings.Join(detail, "; "))
	}

	// A marker the test does not know about is a doc-side typo (e.g.
	// `catalog-sync:nodekinds`) that would otherwise guard nothing.
	for name := range blocks {
		if _, known := want[name]; !known {
			t.Errorf("mio-skill.md carries an unknown catalog-sync marker %q; known markers: node-kinds, section-types, section-templates, page-templates, row-variants", name)
		}
	}
}
