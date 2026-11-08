package docsgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

var BlockNames = []string{
	"node-kinds",
	"section-types",
	"section-templates",
	"surface-templates",
	"page-template-kinds",
	"page-templates",
	"row-variants",
	"node-settings",
	"node-action",
	"node-text-theme",
	"surface-properties",
	"surface-background",
	"surface-gradient",
	"surface-visibility",
}

var (
	knownKindTiers = map[string]bool{"core": true, "presentational": true, "_note": true}

	renderedPropKeys = map[string]bool{
		"type": true, "enum": true, "default": true, "properties": true,
		"shape": true, "freeform": true, "items": true, "deprecated": true,
	}
	ignoredPropKeys = map[string]bool{"description": true, "tier": true}

	documentedShapes = map[string]bool{
		"surface": true, "background": true, "gradient": true, "structural": true,
		"action": true, "textTheme": true, "visibility": true,
	}
	shapeRefTargets = map[string]bool{
		"surface": true, "background": true, "gradient": true,
		"action": true, "textTheme": true, "visibility": true,
	}

	knownNodeKindKeys = map[string]bool{"childRules": true, "renderFallback": true}

	knownSharedEntryKeys = map[string]bool{
		"type": true, "description": true, "properties": true,
	}
)

var blockRe = regexp.MustCompile(`(?s)<!-- catalog-gen:([a-z-]+) -->\n(.*?)<!-- /catalog-gen -->`)

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

	out["page-template-kinds"] = renderPageTemplateKinds(cat.Meta.CatalogVersion, cat.PageTemplates)

	rowTpl, ok := cat.TemplateByID("row")
	if !ok {
		return nil, fmt.Errorf("docsgen: catalog has no 'row' template (the skill documents its layout variants)")
	}
	out["row-variants"] = inlineList(rowTpl.VariantKeys())

	out["node-settings"] = renderNodeSettings(kinds, schema)

	action, err := renderSharedShape(schema, "shared:action")
	if err != nil {
		return nil, err
	}
	out["node-action"] = action

	textTheme, err := renderSharedShape(schema, "shared:textTheme")
	if err != nil {
		return nil, err
	}
	out["node-text-theme"] = textTheme

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

	vis, err := renderSharedShape(schema, "shared:visibility")
	if err != nil {
		return nil, err
	}
	out["surface-visibility"] = vis

	for _, name := range BlockNames {
		if _, present := out[name]; !present {
			return nil, fmt.Errorf("docsgen: internal error — block %q declared in BlockNames but not rendered", name)
		}
	}
	return out, nil
}

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

func assertSchemaFullyConsumed(kinds, schema map[string]any) error {
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
		if items, has := spec["items"].(map[string]any); has {
			if err := assertPropsConsumed(where+"."+name, map[string]any{"items": items}); err != nil {
				return err
			}
		}
	}
	return nil
}

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
	fmt.Fprintf(&b, "\nPlus %d key(s) the validator unions onto **every** node's settings, whatever its kind:\n\n", len(structural))
	b.WriteString(propsBullets(structural))
	return b.String(), nil
}

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

func propsInline(props map[string]any) string {
	names := sortedKeys(props)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		spec, _ := props[name].(map[string]any)
		parts = append(parts, "`"+name+"` "+specSuffix(spec))
	}
	return strings.Join(parts, " · ")
}

func propsBullets(props map[string]any) string {
	var b strings.Builder
	for _, name := range sortedKeys(props) {
		spec, _ := props[name].(map[string]any)
		fmt.Fprintf(&b, "- `%s` %s\n", name, specSuffix(spec))
	}
	return b.String()
}

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

func nestedKeys(spec map[string]any) string {
	props, ok := spec["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return ""
	}
	return strings.Join(sortedKeys(props), ", ")
}

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

func renderPageTemplateKinds(version string, pages []catalog.Template) string {
	var outlines, complete, system []string
	for _, t := range pages {
		switch {
		case isSystemPage(t.Starter):
			system = append(system, t.ID)
		case nodeCarriesCopy(t.Starter):
			complete = append(complete, t.ID)
		default:
			outlines = append(outlines, t.ID)
		}
	}
	return "In catalog " + version + ", the one this binary embeds:\n\n" +
		"Outlines — content pages with no copy on any node; you build and fill the sections:\n\n" + inlineList(outlines) +
		"\nComplete — finished sections with placeholder copy; edit the values in place:\n\n" + inlineList(complete) +
		"\nSystem pages — routed by the hub itself, with fixed regions instead of sections; fill in their values and add no sections:\n\n" + inlineList(system)
}

func isSystemPage(root map[string]any) bool {
	kids, _ := root["children"].([]any)
	if len(kids) == 0 {
		return false
	}
	for _, k := range kids {
		if child, ok := k.(map[string]any); ok {
			if tpl, _ := child["template"].(string); tpl != "" && tpl != "auth-brand-panel" {
				return false
			}
		}
	}
	return true
}

func nodeCarriesCopy(node map[string]any) bool {
	switch v := node["value"].(type) {
	case nil:
	case string:
		if v != "" {
			return true
		}
	case map[string]any:
		if len(v) > 0 {
			return true
		}
	case []any:
		if len(v) > 0 {
			return true
		}
	default:
		return true
	}
	kids, _ := node["children"].([]any)
	for _, k := range kids {
		if child, ok := k.(map[string]any); ok && nodeCarriesCopy(child) {
			return true
		}
	}
	return false
}

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

func objectField(v any, key string) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok
}
