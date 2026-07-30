// Package docsgen renders the machine-derivable parts of the agent skill
// (cmd/skills/content/mio-skill.md) from the embedded page-builder catalog.
//
// WHY THIS EXISTS
// The skill has to tell an authoring agent the node-kind settings vocabulary and
// the surface/background/gradient enums, because the API accepts an unknown
// property with a 200 and the renderer then drops the node silently — there is no
// error to debug from. Until catalog 0.14.0 that content had no machine-readable
// source (`settingsSchema` was empty and 0 of the nodeKinds declared settings), so
// it was hand-written prose. MIO-2685 re-pinned the embedded catalog to 0.14.1,
// which ships a populated `settingsSchema`: 24 `kind:<nodeKind>` entries plus the
// shared `surface`/`background`/`gradient`/`structural` shapes, each property typed
// with its enum and default.
//
// Hand-maintaining a transcription of that is not viable — the catalog moved three
// minor versions in about a day (0.12.0 → 0.13.0 → 0.14.0 → 0.14.1), and the first
// bump this repo took immediately falsified two hand-written lists (`quote` and the
// `testimonials` template). So the doc carries GENERATED blocks, delimited by
//
//	<!-- catalog-gen:<name> -->
//	…generated markdown…
//	<!-- /catalog-gen -->
//
// `go generate ./...` rewrites every block from the embedded catalog, and
// TestSkillDocIsGeneratedFromCatalog byte-compares the checked-in doc against a
// fresh render, so a catalog bump that changes a property, enum value or default
// fails the build with "run go generate ./..." instead of rotting quietly.
//
// WHAT IS DELIBERATELY *NOT* GENERATED
// Anything the catalog does not know: which kinds carry a top-level `value` (that
// is mio-hub's `LeafKind`, in another repo), the nav-icon sprite names, the hub
// branding key map, and the whole workflow/trap narrative. Those stay hand-written
// and are called out as such in the doc.
package docsgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// BlockNames are the generated block names, in the order a reader meets them in
// the skill. Render always returns exactly this set; the drift test uses it to
// detect a block that was deleted from (or invented in) the markdown.
var BlockNames = []string{
	"node-kinds",
	"section-types",
	"section-templates",
	"surface-templates",
	"page-templates",
	"row-variants",
	"node-settings",
	"surface-properties",
	"surface-background",
	"surface-gradient",
}

// The vocabulary this renderer knows how to read.
//
// WHY: the drift test's oracle IS this generator, so anything Render silently
// ignores is invisible to the byte-comparison — `go generate` writes the lossy
// output and the test still passes. Every map below therefore backs an assertion
// (assertSchemaFullyConsumed), not just a lookup.
//
// EXACTLY WHAT IS AND IS NOT COVERED — an earlier version of this comment claimed
// Render "asserts it consumed every key it was handed", which was itself an
// overclaim of the kind this file exists to prevent. The truth:
//
//	COVERED (unknown value ⇒ generation fails):
//	  - settingsSchema top-level keys (must be kind:* or shared:*)
//	  - a kind:* entry's TIERS (knownKindTiers — `_note` is accepted here and not
//	    rendered; it is a tier key, not a property key)
//	  - a shared:* entry's own TOP-LEVEL fields (knownSharedEntryKeys)
//	  - a property spec's keys, recursively through `properties` AND `items`
//	  - a `shape` REFERENCE target (shapeRefTargets) and, separately, every
//	    shared:* shape's own NAME (documentedShapes)
//	  - nodeKinds entry FIELDS (knownNodeKindKeys — `renderFallback` is accepted
//	    here and not rendered; it is a nodeKinds key, not a property key)
//
//	RENDERED (appears in the docs): type, enum, default, properties, shape,
//	  freeform, items, deprecated. TestRenderedPropKeysActuallyRender probes EVERY
//	  member of renderedPropKeys, so a key cannot be whitelisted without a
//	  rendering — that gap is what let `items` and `deprecated` sit accepted but
//	  invisible.
//
//	DELIBERATELY IGNORED — PROPERTY keys accepted and never rendered, listed
//	separately so the distinction is visible rather than buried in one permissive
//	whitelist:
//	  - description: catalog prose. The generated blocks carry names, types, enums
//	    and defaults; the prose is long, reworded often, and would triple the doc.
//	    The catalog remains the place to read it.
//	  - tier: only meaningful inside shared:structural, whose rendering already
//	    groups by the equivalent sections.
//
//	NOT COVERED — stated so the list above is not read as a blanket guarantee:
//	  - the catalog outside nodeKinds/settingsSchema: templates[],
//	    pageTemplates[], sectionTypes[], nestingRules, profiles. Those feed the id
//	    lists and the surface-declaring set; a new FIELD on them is unused rather
//	    than mis-documented.
//	  - the VALUES inside a rendered key (an enum member's spelling, a default's
//	    type). Those flow through to the output, so the byte-comparison catches a
//	    change; nothing asserts they are sane.
var (
	knownKindTiers = map[string]bool{"core": true, "presentational": true, "_note": true}

	// renderedPropKeys are emitted into the docs by specSuffix / valuesCell.
	renderedPropKeys = map[string]bool{
		"type": true, "enum": true, "default": true, "properties": true,
		"shape": true, "freeform": true, "items": true, "deprecated": true,
	}
	// ignoredPropKeys are accepted and deliberately not rendered (see above).
	ignoredPropKeys = map[string]bool{"description": true, "tier": true}

	// TWO SHAPE CONTRACTS, deliberately separate. Collapsing them into one map (as
	// an earlier version did) relaxed the reference check the moment `structural`
	// was added for the other purpose: `shape:"structural"` started printing
	// `*object → shared:structural*`, a pointer to a section that does not exist —
	// the exact dangling pointer that check was written to prevent.
	//
	// documentedShapes: shared:* entries this generator documents SOMEWHERE, so a
	// brand-new shape nobody references is not dropped in silence. `structural` is
	// here because it is rendered inside surface-properties.
	documentedShapes = map[string]bool{
		"surface": true, "background": true, "gradient": true, "structural": true,
	}
	// shapeRefTargets: shapes a `shape:` reference may point AT, i.e. those with a
	// section a reader can actually follow. `structural` has none of its own.
	shapeRefTargets = map[string]bool{
		"surface": true, "background": true, "gradient": true,
	}

	// knownNodeKindKeys are the nodeKinds entry fields this generator understands.
	knownNodeKindKeys = map[string]bool{"childRules": true, "renderFallback": true}

	// knownSharedEntryKeys are the TOP-LEVEL fields of a shared:* entry. Without
	// this the shared branch read only `properties`, so a future
	// `shared:surface.variants` would be dropped with no error — the same silent
	// path already closed for nodeKinds, one level up.
	knownSharedEntryKeys = map[string]bool{
		"type": true, "description": true, "properties": true,
	}
)

// blockRe matches one generated block and captures its name and current body.
var blockRe = regexp.MustCompile(`(?s)<!-- catalog-gen:([a-z-]+) -->\n(.*?)<!-- /catalog-gen -->`)

// Render builds every generated block body from cat. Bodies end with a newline so
// the closing marker always starts its own line.
func Render(cat *catalog.Catalog) (map[string]string, error) {
	raw := cat.Raw()

	kinds, err := objectOf(raw, "nodeKinds")
	if err != nil {
		return nil, err
	}
	schema, err := objectOf(raw, "settingsSchema")
	if err != nil {
		return nil, err
	}

	// Fail before rendering anything if the catalog carries settings vocabulary this
	// generator would silently drop (see knownKindTiers above).
	if err := assertSchemaFullyConsumed(kinds, schema); err != nil {
		return nil, err
	}

	out := map[string]string{}

	out["node-kinds"] = renderNodeKinds(kinds)

	sectionTypes := make([]string, 0, len(cat.SectionTypes))
	for _, st := range cat.SectionTypes {
		sectionTypes = append(sectionTypes, st.ID)
	}
	out["section-types"] = inlineList(sectionTypes)

	sectionTemplates := make([]string, 0, len(cat.Templates))
	for _, t := range cat.Templates {
		sectionTemplates = append(sectionTemplates, t.ID)
	}
	out["section-templates"] = inlineList(sectionTemplates)

	surfaceTemplates, err := surfaceDeclaringTemplates(raw)
	if err != nil {
		return nil, err
	}
	out["surface-templates"] = inlineList(surfaceTemplates)

	pageTemplates := make([]string, 0, len(cat.PageTemplates))
	for _, t := range cat.PageTemplates {
		pageTemplates = append(pageTemplates, t.ID)
	}
	out["page-templates"] = inlineList(pageTemplates)

	rowTpl, ok := cat.TemplateByID("row")
	if !ok {
		return nil, fmt.Errorf("docsgen: catalog has no 'row' template (the skill documents its layout variants)")
	}
	out["row-variants"] = inlineList(rowTpl.VariantKeys())

	out["node-settings"] = renderNodeSettings(kinds, schema)

	surfaceBlock, err := renderSurfaceProperties(schema)
	if err != nil {
		return nil, err
	}
	out["surface-properties"] = surfaceBlock

	bg, err := renderSharedShape(schema, "shared:background")
	if err != nil {
		return nil, err
	}
	out["surface-background"] = bg

	grad, err := renderSharedShape(schema, "shared:gradient")
	if err != nil {
		return nil, err
	}
	out["surface-gradient"] = grad

	for _, name := range BlockNames {
		if _, present := out[name]; !present {
			return nil, fmt.Errorf("docsgen: internal error — block %q declared in BlockNames but not rendered", name)
		}
	}
	return out, nil
}

// Apply replaces the body of every generated block in doc with the rendered one,
// leaving all hand-written prose untouched. It errors when the markdown and
// BlockNames disagree, so a renamed or dropped marker is a hard failure rather
// than a silently un-generated section.
func Apply(doc string, blocks map[string]string) (string, error) {
	seen := map[string]bool{}
	var applyErr error

	out := blockRe.ReplaceAllStringFunc(doc, func(m string) string {
		sub := blockRe.FindStringSubmatch(m)
		name := sub[1]
		body, known := blocks[name]
		if !known {
			applyErr = fmt.Errorf("docsgen: markdown carries unknown catalog-gen block %q; known blocks: %s", name, strings.Join(BlockNames, ", "))
			return m
		}
		if seen[name] {
			applyErr = fmt.Errorf("docsgen: catalog-gen block %q appears more than once", name)
			return m
		}
		seen[name] = true
		return fmt.Sprintf("<!-- catalog-gen:%s -->\n%s<!-- /catalog-gen -->", name, body)
	})
	if applyErr != nil {
		return "", applyErr
	}
	for _, name := range BlockNames {
		if !seen[name] {
			return "", fmt.Errorf("docsgen: markdown has no <!-- catalog-gen:%s --> block; every generated section must stay marked", name)
		}
	}
	return out, nil
}

// assertSchemaFullyConsumed refuses to generate when the catalog declares settings
// vocabulary this renderer does not read. Without it, a new tier / property key /
// shared shape is dropped from the docs with no error and the drift test still
// passes, because the test compares the doc against THIS renderer's output.
func assertSchemaFullyConsumed(kinds, schema map[string]any) error {
	// nodeKinds first — renderNodeKinds reads only childRules, so a new field there
	// would change nothing in the docs and raise nothing.
	for _, kind := range sortedKeys(kinds) {
		m, ok := kinds[kind].(map[string]any)
		if !ok {
			return fmt.Errorf("docsgen: nodeKinds[%q] is not an object (%T)", kind, kinds[kind])
		}
		for _, k := range sortedKeys(m) {
			if !knownNodeKindKeys[k] {
				return fmt.Errorf("docsgen: nodeKinds[%q] declares unknown field %q — this generator reads only childRules, so it would be silently dropped from the docs", kind, k)
			}
		}
	}

	for _, key := range sortedKeys(schema) {
		entry, ok := schema[key].(map[string]any)
		if !ok {
			return fmt.Errorf("docsgen: settingsSchema[%q] is not an object (%T)", key, schema[key])
		}
		switch {
		case strings.HasPrefix(key, "kind:"):
			kind := strings.TrimPrefix(key, "kind:")
			if _, declared := kinds[kind]; !declared {
				return fmt.Errorf("docsgen: settingsSchema has %q but nodeKinds does not declare %q; the doc would document settings for a kind it never lists", key, kind)
			}
			for _, tier := range sortedKeys(entry) {
				if !knownKindTiers[tier] {
					return fmt.Errorf("docsgen: settingsSchema[%q] declares unknown settings tier %q — this generator renders only core and presentational, so those properties would be silently omitted. Teach renderNodeSettings about it, then regenerate", key, tier)
				}
				if tier == "_note" {
					continue
				}
				props, ok := entry[tier].(map[string]any)
				if !ok {
					return fmt.Errorf("docsgen: settingsSchema[%q].%s is not an object (%T)", key, tier, entry[tier])
				}
				if err := assertPropsConsumed(key+"."+tier, props); err != nil {
					return err
				}
			}
		case strings.HasPrefix(key, "shared:"):
			// The reference direction alone is not enough: a brand-new shared shape
			// that nothing references yet would render no block and raise nothing.
			shape := strings.TrimPrefix(key, "shared:")
			if !documentedShapes[shape] {
				return fmt.Errorf("docsgen: settingsSchema declares shared shape %q, which this generator documents nowhere — it would be dropped in silence. Add a block for it (BlockNames + Render) or render it inside an existing one, then list it in documentedShapes", key)
			}
			for _, k := range sortedKeys(entry) {
				if !knownSharedEntryKeys[k] {
					return fmt.Errorf("docsgen: settingsSchema[%q] declares unknown top-level field %q — the shared branch reads only `properties`, so it would be silently dropped from the docs", key, k)
				}
			}
			props, ok := entry["properties"].(map[string]any)
			if !ok {
				return fmt.Errorf("docsgen: settingsSchema[%q] has no properties object", key)
			}
			if err := assertPropsConsumed(key, props); err != nil {
				return err
			}
		default:
			return fmt.Errorf("docsgen: settingsSchema key %q is neither kind:* nor shared:* — this generator would ignore it entirely", key)
		}
	}
	return nil
}

// assertPropsConsumed checks one property map: every property key must be one this
// renderer reads, and a `shape` reference must point at a shape we actually render.
func assertPropsConsumed(where string, props map[string]any) error {
	for _, name := range sortedKeys(props) {
		spec, ok := props[name].(map[string]any)
		if !ok {
			return fmt.Errorf("docsgen: %s.%s is not an object (%T)", where, name, props[name])
		}
		for _, k := range sortedKeys(spec) {
			if renderedPropKeys[k] || ignoredPropKeys[k] {
				continue
			}
			return fmt.Errorf("docsgen: %s.%s declares unknown property key %q — this generator neither renders nor knowingly ignores it, so it would be silently dropped from the docs. Render it (specSuffix/valuesCell + renderedPropKeys) or add it to ignoredPropKeys with a reason", where, name, k)
		}
		if shape, has := spec["shape"].(string); has && !shapeRefTargets[shape] {
			return fmt.Errorf("docsgen: %s.%s references shared shape %q, which this generator renders no section a reader can follow — the doc would print a pointer to nothing. Add a block for it to BlockNames and list it in shapeRefTargets", where, name, shape)
		}
		if nested, has := spec["properties"].(map[string]any); has {
			if err := assertPropsConsumed(where+"."+name, nested); err != nil {
				return err
			}
		}
		// `items` is a property spec in its own right and is now RENDERED
		// (itemsWord), so its keys need the same consumption check as any other.
		if items, has := spec["items"].(map[string]any); has {
			if err := assertPropsConsumed(where+"."+name, map[string]any{"items": items}); err != nil {
				return err
			}
		}
	}
	return nil
}

// surfaceDeclaringTemplates returns the ids of every templates[]/pageTemplates[]
// entry that DECLARES a `surface` property. Presence opts in — even `{}` — and
// absence opts out. This mirrors mio-hub's generated
// src/lib/page-tree/catalog/surface-manifest.ts (SURFACE_TEMPLATE_IDS), which is
// derived from this same catalog by the same rule, so the set is machine-derivable
// rather than a hand-counted list. Order follows the catalog's own array order.
func surfaceDeclaringTemplates(raw map[string]any) ([]string, error) {
	var out []string
	for _, group := range []string{"templates", "pageTemplates"} {
		entries, ok := raw[group].([]any)
		if !ok {
			return nil, fmt.Errorf("docsgen: catalog %q is not an array (%T)", group, raw[group])
		}
		for _, e := range entries {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if _, declares := m["surface"]; !declares {
				continue
			}
			id, _ := m["id"].(string)
			if id == "" {
				return nil, fmt.Errorf("docsgen: a %s entry declares `surface` but has no id", group)
			}
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("docsgen: no template declares a `surface` property; the surface-wrapping set would be documented as empty")
	}
	return out, nil
}

// renderNodeKinds lists every node kind, split by childRules so the
// container/childless divide is derived rather than asserted.
func renderNodeKinds(kinds map[string]any) string {
	var containers, childless []string
	for name, v := range kinds {
		rules, _ := objectField(v, "childRules")
		if rules == "none" {
			childless = append(childless, name)
			continue
		}
		containers = append(containers, name)
	}
	sort.Strings(containers)
	sort.Strings(childless)

	var b strings.Builder
	b.WriteString("Containers (`childRules` accepts children):\n\n")
	b.WriteString(inlineList(containers))
	b.WriteString("\nChildless (`childRules: \"none\"`):\n\n")
	b.WriteString(inlineList(childless))
	return b.String()
}

// renderNodeSettings emits one entry per kind: its child rule, then its `core` and
// `presentational` settings with type, enum and default. Properties are listed
// alphabetically — Go maps lose the catalog's declaration order, so alphabetical
// is the only stable choice.
func renderNodeSettings(kinds, schema map[string]any) string {
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		entry, _ := schema["kind:"+name].(map[string]any)
		rules, _ := objectField(kinds[name], "childRules")

		child := "accepts children"
		if rules == "none" {
			child = "no children"
		}
		fmt.Fprintf(&b, "**`%s`** — %s\n", name, child)

		if entry == nil {
			b.WriteString("- *no settings declared in the catalog*\n\n")
			continue
		}
		wrote := false
		for _, tier := range []string{"core", "presentational"} {
			props, _ := entry[tier].(map[string]any)
			if len(props) == 0 {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", tier, propsInline(props))
			wrote = true
		}
		if !wrote {
			b.WriteString("- *no settings — presentation is fully derived*\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderSurfaceProperties emits the shared `surface` shape plus the additive
// `structural` keys the validator unions onto every kind.
func renderSurfaceProperties(schema map[string]any) (string, error) {
	surface, err := shapeProperties(schema, "shared:surface")
	if err != nil {
		return "", err
	}
	structural, err := shapeProperties(schema, "shared:structural")
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("`settings.surface` accepts these keys, all optional — an omitted key means\n\"no override\", and this is the complete set the resolver reads:\n\n")
	b.WriteString(propsBullets(surface))
	// The count is DERIVED, not spelled: a hardcoded "three" beside a generated
	// list is the same rot this file exists to prevent.
	fmt.Fprintf(&b, "\nPlus %d key(s) the validator unions onto **every** node's settings, whatever its kind:\n\n", len(structural))
	b.WriteString(propsBullets(structural))
	return b.String(), nil
}

// renderSharedShape emits one shared shape (background, gradient) as a property
// table: the discriminant enum first, then the per-variant fields.
func renderSharedShape(schema map[string]any, key string) (string, error) {
	props, err := shapeProperties(schema, key)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("| property | type | values / default |\n|---|---|---|\n")
	for _, name := range sortedKeys(props) {
		spec, _ := props[name].(map[string]any)
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", name, typeWord(spec), valuesCell(spec))
	}
	return b.String(), nil
}

// propsInline renders a property map as a single `·`-separated line.
func propsInline(props map[string]any) string {
	names := sortedKeys(props)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		spec, _ := props[name].(map[string]any)
		parts = append(parts, "`"+name+"` "+specSuffix(spec))
	}
	return strings.Join(parts, " · ")
}

// propsBullets renders a property map as one bullet per property.
func propsBullets(props map[string]any) string {
	var b strings.Builder
	for _, name := range sortedKeys(props) {
		spec, _ := props[name].(map[string]any)
		fmt.Fprintf(&b, "- `%s` %s\n", name, specSuffix(spec))
	}
	return b.String()
}

// specSuffix renders a property's type, enum and default compactly.
func specSuffix(spec map[string]any) string {
	parts := []string{typeWord(spec)}
	if items := itemsWord(spec); items != "" {
		parts = append(parts, items)
	}
	if vals := enumValues(spec); vals != "" {
		parts = append(parts, "`"+vals+"`")
	}
	if nested := nestedKeys(spec); nested != "" {
		parts = append(parts, "{"+nested+"}")
	}
	if d, ok := spec["default"]; ok {
		parts = append(parts, "default `"+scalar(d)+"`")
	}
	if b, _ := spec["freeform"].(bool); b {
		parts = append(parts, "*(freeform — any other string is legal too)*")
	}
	if dep := deprecatedWord(spec); dep != "" {
		parts = append(parts, dep)
	}
	return strings.Join(parts, " ")
}

// itemsWord renders an array property's element type, so an array does not
// document as a bare *array* (catalog 0.14.1's `accordion.defaultExpanded` is the
// live case).
func itemsWord(spec map[string]any) string {
	items, ok := spec["items"].(map[string]any)
	if !ok {
		return ""
	}
	inner, _ := items["type"].(string)
	if inner == "" {
		return "of *unspecified* items"
	}
	if vals := enumValues(items); vals != "" {
		return "of *" + inner + "* `" + vals + "`"
	}
	return "of *" + inner + "*"
}

// deprecatedWord flags a deprecated property. Documenting one as live is a real
// harm — an author wires it up and it stops working on the next frontend release.
func deprecatedWord(spec map[string]any) string {
	switch t := spec["deprecated"].(type) {
	case bool:
		if t {
			return "**DEPRECATED**"
		}
	case string:
		if strings.TrimSpace(t) != "" {
			return "**DEPRECATED** (" + t + ")"
		}
		return "**DEPRECATED**"
	}
	return ""
}

// typeWord renders a property's declared type, resolving a `shape` reference.
func typeWord(spec map[string]any) string {
	t, _ := spec["type"].(string)
	if shape, ok := spec["shape"].(string); ok && shape != "" {
		return fmt.Sprintf("*%s → shared:%s*", t, shape)
	}
	if t == "" {
		return "*untyped*"
	}
	return "*" + t + "*"
}

// enumValues joins a property's enum with `|`.
func enumValues(spec map[string]any) string {
	vals, ok := spec["enum"].([]any)
	if !ok || len(vals) == 0 {
		return ""
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, scalar(v))
	}
	return strings.Join(out, "|")
}

// nestedKeys lists an inline object property's own keys.
func nestedKeys(spec map[string]any) string {
	props, ok := spec["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return ""
	}
	return strings.Join(sortedKeys(props), ", ")
}

// valuesCell is the table cell for a shared-shape property: enum and/or default.
// Enum members are joined with `|`, which is the GFM column separator, so the
// separators are escaped — an unescaped pipe inside a backtick span still splits
// the cell and silently mangles the table.
func valuesCell(spec map[string]any) string {
	var parts []string
	if items := itemsWord(spec); items != "" {
		parts = append(parts, items)
	}
	if vals := enumValues(spec); vals != "" {
		parts = append(parts, "`"+strings.ReplaceAll(vals, "|", `\|`)+"`")
	}
	if nested := nestedKeys(spec); nested != "" {
		parts = append(parts, "keys: "+nested)
	}
	if d, ok := spec["default"]; ok {
		parts = append(parts, "default `"+scalar(d)+"`")
	}
	if b, _ := spec["freeform"].(bool); b {
		parts = append(parts, "freeform")
	}
	if dep := deprecatedWord(spec); dep != "" {
		parts = append(parts, dep)
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

// inlineList renders ids as a wrapped `·`-separated backticked list.
func inlineList(ids []string) string {
	if len(ids) == 0 {
		return "*(none)*\n"
	}
	var b strings.Builder
	lineLen := 0
	for i, id := range ids {
		tok := "`" + id + "`"
		switch {
		case i == 0:
			b.WriteString(tok)
			lineLen = len(tok)
		case lineLen+len(tok)+3 > 78:
			b.WriteString(" ·\n" + tok)
			lineLen = len(tok)
		default:
			b.WriteString(" · " + tok)
			lineLen += len(tok) + 3
		}
	}
	b.WriteString("\n")
	return b.String()
}

// scalar renders a JSON scalar the way the catalog spells it (json.Number keeps
// `2` from becoming `2.0`).
func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", t)
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// objectOf reads a required top-level catalog object.
func objectOf(raw map[string]any, key string) (map[string]any, error) {
	v, ok := raw[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("docsgen: catalog %q is not an object (got %T) — the doc cannot be generated from it", key, raw[key])
	}
	if len(v) == 0 {
		return nil, fmt.Errorf("docsgen: catalog %q is EMPTY; generating from it would silently produce empty documentation", key)
	}
	return v, nil
}

// shapeProperties reads a shared shape's `properties` map.
func shapeProperties(schema map[string]any, key string) (map[string]any, error) {
	entry, ok := schema[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("docsgen: settingsSchema has no %q object", key)
	}
	props, ok := entry["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil, fmt.Errorf("docsgen: settingsSchema[%q].properties is missing or empty", key)
	}
	return props, nil
}

// objectField reads a string field off an `any` that should be an object.
func objectField(v any, key string) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok
}
