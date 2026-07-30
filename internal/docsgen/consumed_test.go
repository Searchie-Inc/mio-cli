package docsgen

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// TestRenderRefusesUnconsumedVocabulary pins the structural weakness reviews found in
// this generator: the drift test's oracle IS Render, so anything Render silently
// ignores is invisible to the byte-comparison — `go generate` writes the lossy output
// and the test still passes.
//
// Each case below was probed against the generator BEFORE the corresponding guard
// existed and produced NO error plus wrong (or missing) documentation.
func TestRenderRefusesUnconsumedVocabulary(t *testing.T) {
	cases := []struct {
		name string
		// mutate edits a freshly loaded catalog's RAW document in place.
		mutate func(raw map[string]any)
		// wantErrSubstring must appear in the returned error.
		wantErrSubstring string
		// wasSilent describes what the generator did before the guard existed.
		wasSilent string
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
			// The other direction, which the first version of the guard missed: the
			// reference check only fires when something REFERENCES the shape.
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
			// NOTE: an earlier version of this case probed an INVENTED key
			// ("deprecatedInFavourOf") while the REAL `deprecated` key sat
			// whitelisted-but-unrendered — so the test looked like it covered the
			// risk while exercising a key that will never appear. `deprecated` and
			// `items` are now genuinely rendered (TestRenderedPropKeysActuallyRender
			// below); this case probes a key that is neither rendered nor ignored.
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
			// nodeKinds had NO consumption assertion at all until a second review
			// pointed it out — the machinery covered settingsSchema only.
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

// renderProbes supplies, for EVERY key in renderedPropKeys, a mutation that should
// visibly change the generated output. The map is asserted to cover renderedPropKeys
// exactly, so a key cannot be whitelisted without a probe — an earlier version of
// this test hand-listed two of eight cases and left the other six unexercised, which
// is the same "looks covered, isn't" failure it was written to prevent.
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

// TestRenderedPropKeysActuallyRender closes the gap that made the whitelist a lie:
// a key listed in renderedPropKeys but never emitted is indistinguishable, from the
// drift test's point of view, from a key that is silently dropped. `deprecated` and
// `items` were both in that state — whitelisted, never rendered — which is why a
// deprecated property could be advertised as live and an array documented as a bare
// *array*.
func TestRenderedPropKeysActuallyRender(t *testing.T) {
	// The probe table must track the whitelist in BOTH directions.
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

// TestIgnoredPropKeysAreDeliberate documents the other half of the contract: keys we
// accept and knowingly do NOT render. If one of these ever starts mattering, this
// test is where the decision is recorded.
func TestIgnoredPropKeysAreDeliberate(t *testing.T) {
	for key := range ignoredPropKeys {
		if renderedPropKeys[key] {
			t.Errorf("%q is in BOTH renderedPropKeys and ignoredPropKeys — the contract must be unambiguous", key)
		}
	}
	// description is in live use across catalog 0.14.1 and is deliberately omitted
	// (long prose, frequent rewording). Prove omission is intentional, not accidental.
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

	// An empty `surface: {}` must still opt in — the rule is presence, not
	// truthiness. Flip content-card (the one section template that declares none).
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
