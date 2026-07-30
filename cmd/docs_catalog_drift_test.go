package cmd

// docs_catalog_drift_test.go — pins the embedded agent skill
// (cmd/skills/content/mio-skill.md) to the embedded page-builder catalog
// (MIO-2539, MIO-2663, MIO-2664, MIO-2685).
//
// WHY
// `mio skills install` ships that markdown verbatim into Claude Code / Codex, so a
// stale sentence there is a stale instruction executed against a live API — and the
// failure mode it documents is silent: the API accepts an unknown node kind or
// settings property with a 200 and the renderer drops the node with no error.
//
// The catalog moved three minors in about a day (0.12.0 → 0.13.0 → 0.14.0 →
// 0.14.1), and the FIRST re-pin this repo took (MIO-2685) immediately falsified two
// hand-written lists: `quote` (a new node kind) and `testimonials` (a new section
// template). Anything transcribed by hand here rots in days. So since 0.14.1 —
// the first catalog to ship a populated `settingsSchema` — the doc's vocabulary and
// settings sections are GENERATED (internal/docsgen) and this test is a
// byte-comparison against a fresh render.
//
// TestSkillDocIsGeneratedFromCatalog is therefore the real guard: it fails on any
// divergence in a documented property name, enum value, default, node kind, section
// type, template id or row variant. TestSkillDocValueBearingKinds covers the one
// list that CANNOT be generated — which kinds carry a top-level `value` is mio-hub's
// `LeafKind`, in another repo — with the strongest cross-checks the catalog allows.

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

// skillDocPath is the on-disk source of the //go:embed'd skillBody. The test reads
// the FILE rather than skillBody so its failure message can name the path to
// regenerate.
//
// It deliberately does NOT assert `onDisk == skillBody`: //go:embed is
// content-addressed, so the toolchain cannot hand a test a skillBody that disagrees
// with the file it just compiled from. A previous version of this test made that
// assertion and documented it as guarding "a stale build cache" — it guarded
// nothing, which is precisely the manufactured confidence this file exists to
// prevent. Reading the file is still the right call (the failure can then say "run
// go generate ./..." against a real path), just not for that reason.
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
		// Render fails loudly on an empty or missing nodeKinds / settingsSchema
		// rather than generating empty documentation — that is the vacuous-pass
		// hole this design closes structurally.
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

// staleBlocks names the generated blocks whose bodies differ, so the failure points
// at the section instead of dumping a whole-file diff.
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

// valueBearingKinds is the doc's hand-written claim: the node kinds whose top-level
// `value` the renderer reads. It mirrors mio-hub's LeafKind
// (src/lib/page-tree/types.ts), which the catalog does NOT encode — there is no
// `value` flag anywhere in catalog.json — so this list cannot be generated, and it
// is the least trustworthy table in the skill.
//
// The three checks below are the strongest the catalog permits. The honest
// limitation: none of them detects mio-hub ADDING a tenth leaf kind that the catalog
// also gains, because at this layer a new leaf is indistinguishable from a new
// container. That is exactly how `quote` slipped through at 0.14.0 — it reached the
// catalog's nodeKinds (which the generated block does catch) while this list stayed
// at eight (which nothing caught). Re-read LeafKind on every catalog re-pin.
var valueBearingKinds = []string{
	"headline", "text", "image", "video", "button", "icon", "divider",
	"progress-ring", "quote",
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

	// (1) Every kind the doc calls value-bearing must still exist in the catalog,
	// and (2) must accept no children — a value-bearing container is a
	// contradiction and would mean the frontend reclassified the kind under us.
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

	// (3) Any kind the catalog's own recipes show carrying a top-level `value` must
	// be documented as value-bearing. A genuine one-directional guard: it catches a
	// kind that starts inlining values in a starter/variant without the doc keeping
	// up. LOWER bound only — the shipped recipes exercise just a few kinds (page
	// templates are outlines with no values at all), so silence is not proof the
	// list is complete.
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

// collectValueBearing walks any decoded catalog fragment and records the `kind` of
// every object carrying both a `kind` and a top-level `value`.
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
		// scalars carry nothing to walk
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
