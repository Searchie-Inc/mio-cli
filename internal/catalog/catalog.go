// Package catalog is the CLI's consumer of the page-builder catalog
// (mio-page-catalog — the cross-repo source of truth for the page-builder
// vocabulary: author templates, compiled section types, page types, and each
// template's declarative starter recipe). It carries a digest-pinned VENDORED
// copy of catalog.json (embedded below) as the offline/air-gapped fallback, a
// Go port of the reference applier (applier.go) that scaffolds real node-trees
// from template recipes, and typed accessors the CLI commands use instead of
// hardcoded lists: the writable section-type allow-list (imperative door),
// template-id + variant validation (tree door), and recommended templates per
// page type.
//
// Live fetching of the freshest catalog over HTTP (with the vendored copy as
// the fail-safe) lives in the client layer; this package owns the vendored
// artifact, the applier, and the parse/lookup logic that runs over either
// source.
package catalog

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// vendoredCatalogJSON is the digest-pinned copy of mio-page-catalog/catalog.json
// at revision f75ddf4 (catalogVersion 0.3.1, meta.digest
// sha256:faae8f12…). It is byte-identical to upstream so its embedded digest
// verifies (see parity_test.go). Refresh it and the golden fixtures together
// when bumping the pinned catalog.
//
//go:embed catalog.json
var vendoredCatalogJSON []byte

// Node is a page-builder recipe/tree node. Unknown fields round-trip untouched;
// numbers are decoded as json.Number so canonicalization is byte-faithful to
// the TS reference.
type Node = map[string]any

// Recommendation is a template's picker placement (charter §5.2.3).
type Recommendation struct {
	Tier  string // baseline | addition | forbidden
	Order int    // picker sort key (ascending)
}

// Template is a catalog author template — either a section template
// (Category "section", carries CompiledSectionType) or a page template
// (from pageTemplates[], carries PageType). Starter is the base recipe subtree;
// Variants are keyed alternative subtrees (data-source type or layout preset).
type Template struct {
	ID                  string
	Label               string
	Category            string // "section" for templates[]; "" for pageTemplates[]
	Lifecycle           string
	CompiledSectionType string   // section templates only
	PageType            string   // page templates only
	ApplicablePageTypes []string // section templates only
	Recommendation      *Recommendation
	Starter             Node
	Variants            map[string]Node
	IsPage              bool // true for pageTemplates[] entries
}

// SectionType is a compiled section.type registry entry (charter §5.0). Writable
// marks the imperative-door (`sections create --type`) allow-list.
type SectionType struct {
	ID           string
	Lifecycle    string
	AnonSafe     bool
	Writable     bool
	CompiledFrom []string
}

// Meta mirrors catalog.json's meta block (the fields the CLI surfaces).
type Meta struct {
	SchemaVersion  string
	CatalogVersion string
	Revision       int
	Digest         string
	CreatedAt      string
}

// Catalog is a parsed page-builder catalog.
type Catalog struct {
	Meta          Meta
	PageTypes     []string
	SectionTypes  []SectionType
	Templates     []Template // section templates (templates[])
	PageTemplates []Template // page templates (pageTemplates[])
	raw           Node       // full parsed catalog (json.Number-bearing) for Digest
}

// Load parses the embedded, digest-pinned vendored catalog. Use Parse to load a
// live-fetched or overridden catalog body.
func Load() (*Catalog, error) {
	return Parse(vendoredCatalogJSON)
}

// Parse decodes a raw catalog.json body into a Catalog, preserving numeric
// literals (UseNumber) so Digest matches the cross-repo canonicalizer.
func Parse(data []byte) (*Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw Node
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("catalog: parse: %w", err)
	}

	c := &Catalog{raw: raw}
	if meta, ok := raw["meta"].(map[string]any); ok {
		c.Meta = Meta{
			SchemaVersion:  str(meta["schemaVersion"]),
			CatalogVersion: str(meta["catalogVersion"]),
			Revision:       numInt(meta["revision"]),
			Digest:         str(meta["digest"]),
			CreatedAt:      str(meta["createdAt"]),
		}
	}
	c.PageTypes = strSlice(raw["pageTypes"])

	for _, v := range slice(raw["sectionTypes"]) {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		c.SectionTypes = append(c.SectionTypes, SectionType{
			ID:           str(m["id"]),
			Lifecycle:    str(m["lifecycle"]),
			AnonSafe:     boolVal(m["anonSafe"]),
			Writable:     boolVal(m["writable"]),
			CompiledFrom: strSlice(m["compiledFrom"]),
		})
	}
	for _, v := range slice(raw["templates"]) {
		if m, ok := v.(map[string]any); ok {
			c.Templates = append(c.Templates, templateFromRaw(m, false))
		}
	}
	for _, v := range slice(raw["pageTemplates"]) {
		if m, ok := v.(map[string]any); ok {
			c.PageTemplates = append(c.PageTemplates, templateFromRaw(m, true))
		}
	}
	return c, nil
}

func templateFromRaw(m map[string]any, isPage bool) Template {
	t := Template{
		ID:                  str(m["id"]),
		Label:               str(m["label"]),
		Category:            str(m["category"]),
		Lifecycle:           str(m["lifecycle"]),
		CompiledSectionType: str(m["compiledSectionType"]),
		PageType:            str(m["pageType"]),
		ApplicablePageTypes: strSlice(m["applicablePageTypes"]),
		Starter:             asNode(m["starter"]),
		IsPage:              isPage,
	}
	if rec, ok := m["recommendation"].(map[string]any); ok {
		t.Recommendation = &Recommendation{Tier: str(rec["tier"]), Order: numInt(rec["order"])}
	}
	if vs, ok := m["variants"].(map[string]any); ok && len(vs) > 0 {
		t.Variants = make(map[string]Node, len(vs))
		for k, node := range vs {
			t.Variants[k] = asNode(node)
		}
	}
	return t
}

// Raw returns the full parsed catalog (for Digest verification).
func (c *Catalog) Raw() Node { return c.raw }

// DigestPinned returns the vendored catalog's declared meta.digest.
func (c *Catalog) DigestPinned() string { return c.Meta.Digest }

// TemplateByID looks a template up by id across BOTH registries — section
// templates (tree/section vocabulary) and page templates. Page templates win
// ties only in theory; ids are unique across both per the catalog invariants.
func (c *Catalog) TemplateByID(id string) (Template, bool) {
	for _, t := range c.Templates {
		if t.ID == id {
			return t, true
		}
	}
	for _, t := range c.PageTemplates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// TemplateIDs returns every scaffoldable template id (section + page), sorted.
func (c *Catalog) TemplateIDs() []string {
	ids := make([]string, 0, len(c.Templates)+len(c.PageTemplates))
	for _, t := range c.Templates {
		ids = append(ids, t.ID)
	}
	for _, t := range c.PageTemplates {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	return ids
}

// WritableSectionTypes returns the sorted ids of section types that accept a
// direct imperative write (`sections create --type`) — the catalog-derived
// replacement for the hardcoded 9-type list.
func (c *Catalog) WritableSectionTypes() []string {
	var out []string
	for _, s := range c.SectionTypes {
		if s.Writable {
			out = append(out, s.ID)
		}
	}
	sort.Strings(out)
	return out
}

// IsWritableSectionType reports whether id is an allowed imperative-door type.
func (c *Catalog) IsWritableSectionType(id string) bool {
	for _, s := range c.SectionTypes {
		if s.ID == id {
			return s.Writable
		}
	}
	return false
}

// RecommendedTemplates returns the section templates applicable to pageType
// (pageType ∈ applicablePageTypes), ordered by recommendation.order ascending.
func (c *Catalog) RecommendedTemplates(pageType string) []Template {
	var out []Template
	for _, t := range c.Templates {
		if contains(t.ApplicablePageTypes, pageType) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return recOrder(out[i]) < recOrder(out[j])
	})
	return out
}

// PageTemplateForType returns the page template whose pageType matches, if any.
func (c *Catalog) PageTemplateForType(pageType string) (Template, bool) {
	for _, t := range c.PageTemplates {
		if t.PageType == pageType {
			return t, true
		}
	}
	return Template{}, false
}

// VariantKeys returns a template's variant keys, sorted (stable help output).
func (t Template) VariantKeys() []string {
	keys := make([]string, 0, len(t.Variants))
	for k := range t.Variants {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func recOrder(t Template) int {
	if t.Recommendation != nil {
		return t.Recommendation.Order
	}
	return 1 << 30 // no recommendation → sort last
}

// CanonicalJSON returns deterministic JSON: object keys recursively sorted
// (Go's encoder sorts map keys), no HTML escaping (TS JSON.stringify does not
// escape <, >, &), and numbers emitted verbatim (json.Number). This is the
// RFC 8785-ish JCS approximation shared across repos (canonical.ts) — matching
// it is what lets the Go digest equal the TS digest.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Digest computes "sha256:<hex>" over the canonical catalog with meta.digest
// removed (charter §5.2.1). Ports mio-page-catalog src/canonical.ts.
func Digest(cat Node) (string, error) {
	clone, ok := deepClone(cat).(map[string]any)
	if !ok {
		return "", fmt.Errorf("catalog: digest: root is not an object")
	}
	if meta, ok := clone["meta"].(map[string]any); ok {
		delete(meta, "digest")
	}
	b, err := CanonicalJSON(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ---- small typed-access helpers over json.Number-bearing maps ----

func str(v any) string {
	s, _ := v.(string)
	return s
}

func boolVal(v any) bool {
	b, _ := v.(bool)
	return b
}

func numInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func slice(v any) []any {
	s, _ := v.([]any)
	return s
}

func strSlice(v any) []string {
	items := slice(v)
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asNode(v any) Node {
	m, _ := v.(map[string]any)
	return m
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
