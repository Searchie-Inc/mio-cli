package catalog

// catalog_test.go — loader + accessor invariants over the vendored catalog
// (mio-page-catalog@45258a1, catalogVersion 0.10.0). These accessors are what the CLI commands consume
// instead of hardcoded lists: the writable section-type allow-list (imperative
// door), template-id validation (tree door), and recommended templates per page
// type.

import (
	"reflect"
	"testing"
)

func TestParse_RejectsTrailingContent(t *testing.T) {
	good := vendoredCatalogJSON
	// A valid catalog followed by a stray JSON value must be rejected (TS
	// JSON.parse would): otherwise a digest-consistent body with appended junk
	// would be silently adopted.
	bad := append(append([]byte{}, good...), []byte("\n{\"junk\":true}")...)
	if _, err := Parse(bad); err == nil {
		t.Error("Parse accepted trailing content after the catalog object")
	}
	// Trailing whitespace only is fine.
	ws := append(append([]byte{}, good...), []byte("\n\n  \t")...)
	if _, err := Parse(ws); err != nil {
		t.Errorf("Parse rejected trailing whitespace: %v", err)
	}
}

func TestSectionType_KnownVsUnknown(t *testing.T) {
	c := loadForTest(t)
	// compact flipped writable false -> true in 0.10.0 (MIO-2681,
	// imperative-door parity with grid).
	if st, ok := c.SectionType("compact"); !ok || !st.Writable {
		t.Errorf("SectionType(compact) = %+v, %v; want known + writable", st, ok)
	}
	if st, ok := c.SectionType("grid"); !ok || !st.Writable {
		t.Errorf("SectionType(grid) = %+v, %v; want known + writable", st, ok)
	}
	if _, ok := c.SectionType("brand-new-type"); ok {
		t.Error("SectionType(brand-new-type) should be unknown")
	}
}

func TestLoad_Counts(t *testing.T) {
	c := loadForTest(t)
	if got := len(c.Templates); got != 8 {
		t.Errorf("section templates = %d, want 8", got)
	}
	if got := len(c.PageTemplates); got != 13 {
		t.Errorf("page templates = %d, want 13", got)
	}
	if got := len(c.SectionTypes); got != 12 {
		t.Errorf("section types = %d, want 12", got)
	}
	if got := len(c.PageTypes); got != 12 {
		t.Errorf("page types = %d, want 12", got)
	}
}

func TestWritableSectionTypes_MatchesCatalog(t *testing.T) {
	c := loadForTest(t)
	// The 10 writable=true section types (the imperative `sections create --type`
	// allow-list), sorted for a stable help string. compact joined the set in
	// 0.10.0 (MIO-2681).
	want := []string{"carousel", "compact", "content-grid", "cta", "feature", "grid", "row", "search", "text", "video"}
	got := c.WritableSectionTypes()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WritableSectionTypes() = %v, want %v", got, want)
	}
}

func TestIsWritableSectionType(t *testing.T) {
	c := loadForTest(t)
	cases := map[string]bool{
		"grid":    true,
		"video":   true,
		"feature": true,
		"compact": true, // writable=true as of 0.10.0 (MIO-2681)
		"unknown": false,
	}
	for id, want := range cases {
		if got := c.IsWritableSectionType(id); got != want {
			t.Errorf("IsWritableSectionType(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestTemplateByID_SectionAndPage(t *testing.T) {
	c := loadForTest(t)
	if tmpl, ok := c.TemplateByID("hero"); !ok || tmpl.Category != "section" {
		t.Errorf("TemplateByID(hero) = %+v, %v; want a section template", tmpl, ok)
	}
	if tmpl, ok := c.TemplateByID("page-homepage"); !ok || tmpl.PageType != "homepage" {
		t.Errorf("TemplateByID(page-homepage) = %+v, %v; want pageType homepage", tmpl, ok)
	}
	if _, ok := c.TemplateByID("does-not-exist"); ok {
		t.Error("TemplateByID(does-not-exist) should return ok=false")
	}
}

func TestRecommendedTemplates_ForHomepage_OrderedByRecommendation(t *testing.T) {
	c := loadForTest(t)
	got := c.RecommendedTemplates("homepage")
	var ids []string
	for _, tmpl := range got {
		ids = append(ids, tmpl.ID)
	}
	// Section templates whose applicablePageTypes include "homepage", ordered by
	// recommendation.order. content-card (applicablePageTypes: []) is excluded.
	want := []string{"hero", "carousel", "grid", "content-grid", "row", "search-bar", "compact"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("RecommendedTemplates(homepage) = %v, want %v", ids, want)
	}
}

func TestRecommendedTemplates_UnknownPageType_Empty(t *testing.T) {
	c := loadForTest(t)
	if got := c.RecommendedTemplates("no-such-page-type"); len(got) != 0 {
		t.Errorf("RecommendedTemplates(unknown) = %v, want empty", got)
	}
}

func TestPageTemplateForType(t *testing.T) {
	c := loadForTest(t)
	if p, ok := c.PageTemplateForType("homepage"); !ok || p.ID != "page-homepage" {
		t.Errorf("PageTemplateForType(homepage) = %+v, %v; want page-homepage", p, ok)
	}
	if p, ok := c.PageTemplateForType("custom"); !ok || p.ID != "page-generic" {
		t.Errorf("PageTemplateForType(custom) = %+v, %v; want page-generic", p, ok)
	}
	if _, ok := c.PageTemplateForType("no-such-type"); ok {
		t.Error("PageTemplateForType(unknown) should return ok=false")
	}
}

func TestValidVariants(t *testing.T) {
	c := loadForTest(t)
	tmpl, ok := c.TemplateByID("row")
	if !ok {
		t.Fatal("no row template")
	}
	want := map[string]bool{
		"1col": true, "2eq": true, "2left": true, "2right": true, "3eq": true, "4eq": true,
		"faq": true, "cta-band": true, "bound-cards": true,
	}
	if len(tmpl.Variants) != len(want) {
		t.Fatalf("row variants = %d, want %d", len(tmpl.Variants), len(want))
	}
	for v := range want {
		if _, ok := tmpl.Variants[v]; !ok {
			t.Errorf("row is missing variant %q", v)
		}
	}
}
