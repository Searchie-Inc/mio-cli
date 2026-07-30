package docsgen

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// TestRenderRefusesUnconsumedVocabulary pins the structural weakness a review found
// in the first version of this generator: the drift test's oracle IS Render, so
// anything Render silently ignores is invisible to the byte-comparison — `go generate`
// writes the lossy output and the test still passes.
//
// Each case below was probed against the pre-fix generator and produced NO error plus
// wrong documentation. They must now all fail generation.
func TestRenderRefusesUnconsumedVocabulary(t *testing.T) {
	cases := []struct {
		name string
		// mutate edits a freshly loaded catalog's raw settingsSchema in place.
		mutate func(schema map[string]any)
		// wantErrSubstring must appear in the returned error.
		wantErrSubstring string
		// wasSilent describes what the pre-fix generator did instead.
		wasSilent string
	}{
		{
			name: "a new settings tier on a kind",
			mutate: func(schema map[string]any) {
				entry := schema["kind:headline"].(map[string]any)
				entry["experimental"] = map[string]any{
					"parallax": map[string]any{"type": "boolean"},
				}
			},
			wantErrSubstring: "unknown settings tier",
			wasSilent:        "the tier's properties were omitted from the docs with no error",
		},
		{
			name: "a shape reference to a shared shape we render no block for",
			mutate: func(schema map[string]any) {
				schema["shared:animation"] = map[string]any{
					"type":       "object",
					"properties": map[string]any{"duration": map[string]any{"type": "number"}},
				}
				props := schema["kind:text"].(map[string]any)["presentational"].(map[string]any)
				props["animation"] = map[string]any{"type": "object", "shape": "animation"}
			},
			wantErrSubstring: "renders no block for",
			wasSilent:        "the doc printed `*object → shared:animation*`, a pointer to nothing",
		},
		{
			name: "a kind whose settings moved under properties",
			mutate: func(schema map[string]any) {
				entry := schema["kind:image"].(map[string]any)
				moved := map[string]any{}
				for k, v := range entry["presentational"].(map[string]any) {
					moved[k] = v
				}
				delete(entry, "core")
				delete(entry, "presentational")
				entry["properties"] = moved
			},
			wantErrSubstring: "unknown settings tier",
			wasSilent:        "the doc affirmatively claimed \"no settings — presentation is fully derived\" for a kind that has settings",
		},
		{
			name: "a new property key on a property spec",
			mutate: func(schema map[string]any) {
				props := schema["kind:headline"].(map[string]any)["core"].(map[string]any)
				spec := props["level"].(map[string]any)
				spec["deprecatedInFavourOf"] = "size"
			},
			wantErrSubstring: "unknown property key",
			wasSilent:        "the key was dropped, so a deprecation notice never reached the docs",
		},
		{
			name: "a settingsSchema entry for a kind nodeKinds does not declare",
			mutate: func(schema map[string]any) {
				schema["kind:parallax-band"] = map[string]any{
					"core": map[string]any{"speed": map[string]any{"type": "number"}},
				}
			},
			wantErrSubstring: "nodeKinds does not declare",
			wasSilent:        "the kind's settings were skipped and it never appeared in any list",
		},
		{
			name: "a top-level settingsSchema key that is neither kind nor shared",
			mutate: func(schema map[string]any) {
				schema["profile:ios-min"] = map[string]any{"properties": map[string]any{}}
			},
			wantErrSubstring: "neither kind:* nor shared:*",
			wasSilent:        "the entry was ignored entirely",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := catalog.Load()
			if err != nil {
				t.Fatalf("catalog.Load: %v", err)
			}
			schema, ok := cat.Raw()["settingsSchema"].(map[string]any)
			if !ok {
				t.Fatalf("catalog settingsSchema is not an object")
			}
			tc.mutate(schema)

			_, err = Render(cat)
			if err == nil {
				t.Fatalf("Render accepted %s — before this guard existed, %s", tc.name, tc.wasSilent)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Fatalf("Render error for %s = %q, want it to contain %q", tc.name, err, tc.wantErrSubstring)
			}
		})
	}
}

// TestSurfaceDeclaringTemplatesMatchesPresenceRule pins the derivation rule the
// mio-hub generated manifest (src/lib/page-tree/catalog/surface-manifest.ts) uses:
// a template opts into surface wrapping by DECLARING a `surface` property, presence
// alone — even `{}`. Absence opts out.
func TestSurfaceDeclaringTemplatesMatchesPresenceRule(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	got, err := surfaceDeclaringTemplates(cat.Raw())
	if err != nil {
		t.Fatalf("surfaceDeclaringTemplates: %v", err)
	}

	// The set mio-hub's generated manifest ships for this catalog digest.
	want := []string{"hero", "carousel", "grid", "content-grid", "row", "search-bar", "compact", "testimonials"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("surface-declaring templates = %v, want %v (mio-hub SURFACE_TEMPLATE_IDS for this catalog)", got, want)
	}

	// An empty `surface: {}` must still opt in — the rule is presence, not truthiness.
	// Flip content-card (the one section template that declares none) and re-derive.
	entries := cat.Raw()["templates"].([]any)
	var flipped bool
	for _, e := range entries {
		m := e.(map[string]any)
		if m["id"] == "content-card" {
			m["surface"] = map[string]any{}
			flipped = true
		}
	}
	if !flipped {
		t.Fatal("no content-card template to flip; the presence-rule assertion did not run")
	}
	got2, err := surfaceDeclaringTemplates(cat.Raw())
	if err != nil {
		t.Fatalf("surfaceDeclaringTemplates after flip: %v", err)
	}
	if len(got2) != len(want)+1 {
		t.Errorf("an empty `surface: {}` must opt IN (presence, not truthiness); got %d ids, want %d", len(got2), len(want)+1)
	}
}
