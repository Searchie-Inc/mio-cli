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
	"io"
	"sort"
	"unicode/utf16"
)

// vendoredCatalogJSON is the digest-pinned copy of mio-page-catalog/catalog.json
// at commit 5ffdfaf (catalogVersion 0.8.0, revision 8, meta.digest
// sha256:48148927…). It is byte-identical to upstream so its embedded digest
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
	// Reject trailing content after the top-level object (json.Decoder stops at
	// the first value; TS JSON.parse would reject the rest). A second decode must
	// hit EOF — anything else means junk followed the catalog.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("catalog: parse: unexpected trailing content after the catalog object")
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

// SectionType returns the section type with the given id, and whether it exists
// in the catalog. Used to distinguish a KNOWN non-writable type (reject) from an
// UNKNOWN type (defer to the backend) on the imperative door.
func (c *Catalog) SectionType(id string) (SectionType, bool) {
	for _, s := range c.SectionTypes {
		if s.ID == id {
			return s, true
		}
	}
	return SectionType{}, false
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

// CanonicalJSON returns deterministic JSON byte-identical to the cross-repo TS
// canonicalizer (canonical.ts: JSON.stringify(sortKeys(value))): object keys
// recursively sorted by UTF-16 code-unit order (matching JS String sort),
// arrays in order, numbers emitted verbatim (json.Number), and strings escaped
// exactly as JS JSON.stringify. Matching TS byte-for-byte is what lets the Go
// digest equal the digest shipped in the catalog's meta.digest / HTTP ETag.
//
// Go's encoding/json is NOT usable here: it escapes U+2028/U+2029 (and, unless
// disabled, <>&) where JS emits them raw, and it sorts map keys by UTF-8 bytes
// rather than UTF-16 code units — both would diverge from the TS digest for a
// catalog containing those characters.
func CanonicalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := writeCanonical(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeCanonical(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeJSONString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case map[string]any:
		return writeCanonicalObject(b, t)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		// Non-json.Number scalars (float64/int/…). The catalog is always decoded
		// with UseNumber, so these never occur on the digest path; a plain marshal
		// is sufficient and, being numeric, carries no string-escaping divergence.
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(raw)
	}
	return nil
}

// writeCanonicalObject writes a JSON object with keys sorted by UTF-16 code-unit
// order — the ordering the TS canonicalizer's Object.keys(v).sort() produces.
func writeCanonicalObject(b *bytes.Buffer, m map[string]any) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(b, k)
		b.WriteByte(':')
		if err := writeCanonical(b, m[k]); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

const hexDigits = "0123456789abcdef"

// writeJSONString escapes s exactly as JS JSON.stringify: escape only " \ and
// the C0 controls (< 0x20, using the short forms \b\t\n\f\r, else \u00XX), and
// emit every other byte — <>&, U+2028, U+2029, and all multi-byte UTF-8 —
// verbatim.
func writeJSONString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

// utf16Less reports whether a sorts before b in UTF-16 code-unit order — the
// comparison JS String sort (and thus the TS canonicalizer) uses. For BMP text
// this equals codepoint/byte order; it differs only for astral characters,
// whose lead surrogate (0xD800–0xDBFF) sorts below BMP chars in 0xE000–0xFFFF.
func utf16Less(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
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
