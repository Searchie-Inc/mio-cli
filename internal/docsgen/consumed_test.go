package docsgen

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

func TestRenderRefusesUnconsumedVocabulary(t *testing.T) {
	cases := []struct {
		name             string
		mutate           func(raw map[string]any)
		wantErrSubstring string
		wasSilent        string
	}{
		{
			name: "a new settings tier on a kind",
			mutate: func(raw map[string]any) {
				entry := schemaOf(raw)["kind:headline"].(map[string]any)
				entry["experimental"] = map[string]any{
					"parallax": map[string]any{"type": "boolean"},
				}
			},
			wantErrSubstring: "unknown settings tier",
			wasSilent:        "the tier's properties were omitted from the docs with no error",
		},
		{
			name: "a shape REFERENCE to a shared shape we render no block for",
			mutate: func(raw map[string]any) {
				props := schemaOf(raw)["kind:text"].(map[string]any)["presentational"].(map[string]any)
				props["animation"] = map[string]any{"type": "object", "shape": "animation"}
			},
			wantErrSubstring: "no section a reader can follow",
			wasSilent:        "the doc printed `*object → shared:animation*`, a pointer to nothing",
		},
		{
			name: "a brand-new shared shape that nothing references",
			mutate: func(raw map[string]any) {
				schemaOf(raw)["shared:animation"] = map[string]any{
					"type":       "object",
					"properties": map[string]any{"duration": map[string]any{"type": "number"}},
				}
			},
			wantErrSubstring: "documents nowhere",
			wasSilent:        "the shape was dropped entirely — the reference check never fired because nothing referenced it",
		},
		{
			name: "a kind whose settings moved under properties",
			mutate: func(raw map[string]any) {
				entry := schemaOf(raw)["kind:image"].(map[string]any)
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
			name: "a property key that is neither rendered nor knowingly ignored",
			mutate: func(raw map[string]any) {
				props := schemaOf(raw)["kind:headline"].(map[string]any)["core"].(map[string]any)
				props["level"].(map[string]any)["minimum"] = 1
			},
			wantErrSubstring: "unknown property key",
			wasSilent:        "the key was dropped with no error",
		},
		{
			name: "a settingsSchema entry for a kind nodeKinds does not declare",
			mutate: func(raw map[string]any) {
				schemaOf(raw)["kind:parallax-band"] = map[string]any{
					"core": map[string]any{"speed": map[string]any{"type": "number"}},
				}
			},
			wantErrSubstring: "nodeKinds does not declare",
			wasSilent:        "the kind's settings were skipped and it never appeared in any list",
		},
		{
			name: "a top-level settingsSchema key that is neither kind nor shared",
			mutate: func(raw map[string]any) {
				schemaOf(raw)["profile:ios-min"] = map[string]any{"properties": map[string]any{}}
			},
			wantErrSubstring: "neither kind:* nor shared:*",
			wasSilent:        "the entry was ignored entirely",
		},
		{
			name: "a new field on a nodeKinds entry",
			mutate: func(raw map[string]any) {
				kinds := raw["nodeKinds"].(map[string]any)
				kinds["headline"].(map[string]any)["replacedBy"] = "title"
			},
			wantErrSubstring: "declares unknown field",
			wasSilent:        "renderNodeKinds reads only childRules, so the field vanished with no error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := catalog.Load()
			if err != nil {
				t.Fatalf("catalog.Load: %v", err)
			}
			tc.mutate(cat.Raw())

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

func schemaOf(raw map[string]any) map[string]any {
	return raw["settingsSchema"].(map[string]any)
}

var renderProbes = map[string]struct {
	set      func(spec map[string]any)
	wantText string
}{
	"deprecated": {func(s map[string]any) { s["deprecated"] = "use size" }, "use size"},
	"items": {func(s map[string]any) {
		s["type"] = "array"
		delete(s, "enum")
		delete(s, "default")
		s["items"] = map[string]any{"type": "string"}
	}, "of *string*"},
	"type":    {func(s map[string]any) { s["type"] = "string"; delete(s, "enum") }, "*string*"},
	"enum":    {func(s map[string]any) { s["enum"] = []any{"alpha", "beta"} }, "alpha|beta"},
	"default": {func(s map[string]any) { s["default"] = "sentinel-default" }, "sentinel-default"},
	"properties": {func(s map[string]any) {
		s["type"] = "object"
		delete(s, "enum")
		delete(s, "default")
		s["properties"] = map[string]any{"probeKey": map[string]any{"type": "string"}}
	}, "probeKey"},
	"shape": {func(s map[string]any) {
		s["type"] = "object"
		delete(s, "enum")
		delete(s, "default")
		s["shape"] = "surface"
	}, "shared:surface"},
	"freeform": {func(s map[string]any) { s["type"] = "string"; delete(s, "enum"); s["freeform"] = true }, "freeform"},
}

func TestRenderedPropKeysActuallyRender(t *testing.T) {
	for key := range renderedPropKeys {
		if _, probed := renderProbes[key]; !probed {
			t.Errorf("renderedPropKeys contains %q with no probe in renderProbes — add one, or the key can be whitelisted without ever being rendered", key)
		}
	}
	for key := range renderProbes {
		if !renderedPropKeys[key] {
			t.Errorf("renderProbes probes %q, which is not in renderedPropKeys — stale probe", key)
		}
	}

	base, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	before, err := Render(base)
	if err != nil {
		t.Fatalf("Render baseline: %v", err)
	}

	for key, probe := range renderProbes {
		t.Run(key, func(t *testing.T) {
			cat, err := catalog.Load()
			if err != nil {
				t.Fatalf("catalog.Load: %v", err)
			}
			spec := schemaOf(cat.Raw())["kind:headline"].(map[string]any)["core"].(map[string]any)["level"].(map[string]any)
			probe.set(spec)

			after, err := Render(cat)
			if err != nil {
				t.Fatalf("Render after setting %s: %v", key, err)
			}
			if before["node-settings"] == after["node-settings"] {
				t.Fatalf("setting %q produced BYTE-IDENTICAL output — it is whitelisted but never rendered, so the docs would keep describing the property as if it were unset", key)
			}
			if !strings.Contains(after["node-settings"], probe.wantText) {
				t.Errorf("rendered block after setting %q does not contain %q", key, probe.wantText)
			}
		})
	}
}

func TestIgnoredPropKeysAreDeliberate(t *testing.T) {
	for key := range ignoredPropKeys {
		if renderedPropKeys[key] {
			t.Errorf("%q is in BOTH renderedPropKeys and ignoredPropKeys — the contract must be unambiguous", key)
		}
	}
	base, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	blocks, err := Render(base)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	desc := schemaOf(base.Raw())["kind:headline"].(map[string]any)["core"].(map[string]any)["level"].(map[string]any)["description"].(string)
	if strings.Contains(blocks["node-settings"], desc) {
		t.Errorf("catalog `description` prose leaked into the generated block; it is listed as deliberately ignored")
	}
}

func TestSurfaceDeclaringTemplatesMatchesPresenceRule(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	got, err := surfaceDeclaringTemplates(cat.Raw())
	if err != nil {
		t.Fatalf("surfaceDeclaringTemplates: %v", err)
	}

	want := []string{"hero", "carousel", "pinned-posts", "grid", "content-grid", "row", "search-bar", "compact", "testimonials"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("surface-declaring templates = %v, want %v (mio-hub SURFACE_TEMPLATE_IDS for this catalog)", got, want)
	}

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
