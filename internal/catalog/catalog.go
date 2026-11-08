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
	"strings"
	"unicode/utf16"
)

//go:embed catalog.json
var vendoredCatalogJSON []byte

//go:embed CATALOG_REF
var pinnedRefRaw string

func PinnedRef() string { return strings.TrimSpace(pinnedRefRaw) }

type Node = map[string]any

type Recommendation struct {
	Tier  string
	Order int
}

type Template struct {
	ID                  string
	Label               string
	Category            string
	Lifecycle           string
	CompiledSectionType string
	PageType            string
	ApplicablePageTypes []string
	Recommendation      *Recommendation
	Starter             Node
	Variants            map[string]Node
	IsPage              bool
}

type SectionType struct {
	ID           string
	Lifecycle    string
	AnonSafe     bool
	Writable     bool
	CompiledFrom []string
}

type Meta struct {
	SchemaVersion  string
	CatalogVersion string
	Revision       int
	Digest         string
	CreatedAt      string
}

type Catalog struct {
	Meta          Meta
	PageTypes     []string
	SectionTypes  []SectionType
	Templates     []Template
	PageTemplates []Template
	HubTemplates  []HubTemplate
	raw           Node
}

func Load() (*Catalog, error) {
	return Parse(vendoredCatalogJSON)
}

func Parse(data []byte) (*Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw Node
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("catalog: parse: %w", err)
	}
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
	for _, v := range slice(raw["hubTemplates"]) {
		if m, ok := v.(map[string]any); ok {
			c.HubTemplates = append(c.HubTemplates, parseHubTemplate(m))
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

func (c *Catalog) Raw() Node { return c.raw }

func (c *Catalog) DigestPinned() string { return c.Meta.Digest }

var templateIDAliases = map[string]string{
	"scroll": "compact",
}

func (c *Catalog) TemplateByID(id string) (Template, bool) {
	if real, ok := templateIDAliases[id]; ok {
		id = real
	}
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

func (c *Catalog) IsWritableSectionType(id string) bool {
	for _, s := range c.SectionTypes {
		if s.ID == id {
			return s.Writable
		}
	}
	return false
}

func (c *Catalog) SectionType(id string) (SectionType, bool) {
	for _, s := range c.SectionTypes {
		if s.ID == id {
			return s, true
		}
	}
	return SectionType{}, false
}

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

func (c *Catalog) PageTemplateForType(pageType string) (Template, bool) {
	for _, t := range c.PageTemplates {
		if t.PageType == pageType {
			return t, true
		}
	}
	return Template{}, false
}

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
	return 1 << 30
}

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
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(raw)
	}
	return nil
}

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
