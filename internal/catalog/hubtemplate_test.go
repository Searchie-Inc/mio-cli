package catalog

// hubtemplate_test.go — hubTemplates[] model invariants over the 2.1 artifact
// (testdata/catalog-2.1.json, a byte-copy of mio-page-catalog@rev7). The digest
// test is THE canonicalizer-parity gate for the live-fetch epic: if the Go
// Digest over the 2.1 artifact stops matching its pinned meta.digest, every
// downstream provenance marker (TreeDigest) is suspect — stop and escalate
// rather than adjusting the canonicalizer.

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"testing"
)

// pinned21Digest is meta.digest of the 2.1 artifact (schemaVersion 2.1.0,
// catalogVersion 0.7.0, revision 7).
const pinned21Digest = "sha256:ab30e06a03eb7040b53754d3afac4313872d1c9e8dfc0c5f9cec9d2b6903c5eb"

func load21ForTest(t *testing.T) *Catalog {
	t.Helper()
	b, err := os.ReadFile("testdata/catalog-2.1.json")
	if err != nil {
		t.Fatalf("read catalog-2.1.json: %v", err)
	}
	c, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(catalog-2.1.json): %v", err)
	}
	return c
}

func TestCatalog21_DigestVerifiesAndParses(t *testing.T) {
	c := load21ForTest(t)
	if c.Meta.SchemaVersion != "2.1.0" {
		t.Errorf("meta.schemaVersion = %q, want 2.1.0", c.Meta.SchemaVersion)
	}
	if c.Meta.Revision != 7 {
		t.Errorf("meta.revision = %d, want 7", c.Meta.Revision)
	}
	if c.Meta.Digest != pinned21Digest {
		t.Fatalf("meta.digest = %q, want pinned %q", c.Meta.Digest, pinned21Digest)
	}
	got, err := Digest(c.Raw())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got != pinned21Digest {
		t.Fatalf("canonicalizer parity BROKEN: Digest(raw) = %q, want %q (= meta.digest); do not adjust the canonicalizer — escalate", got, pinned21Digest)
	}
}

func TestHubTemplateByID_Community(t *testing.T) {
	c := load21ForTest(t)
	h, ok := c.HubTemplateByID("community")
	if !ok {
		t.Fatal("HubTemplateByID(community) not found")
	}
	if h.ID != "community" || h.Label == "" || h.Lifecycle == "" {
		t.Errorf("community identity = %q/%q/%q, want id community with label+lifecycle", h.ID, h.Label, h.Lifecycle)
	}
	if len(h.Pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(h.Pages))
	}
	hp := h.HomepagePage()
	if hp == nil {
		t.Fatal("HomepagePage() = nil, want the homepage entry")
	}
	if hp.PageTemplate != "page-homepage-community" || hp.Slug != "homepage" || !hp.IsHomepage {
		t.Errorf("HomepagePage() = %+v, want page-homepage-community/homepage/isHomepage", *hp)
	}
	if len(h.Spaces) != 2 {
		t.Fatalf("spaces = %d, want 2", len(h.Spaces))
	}
	for i, s := range h.Spaces {
		if s.Slug == "" || s.AccessLevel == "" {
			t.Errorf("spaces[%d] = %+v, want populated Slug + AccessLevel", i, s)
		}
	}
	if len(h.Onboarding) != 2 {
		t.Fatalf("onboarding = %d, want 2", len(h.Onboarding))
	}
	for i, d := range h.Onboarding {
		if d.Slug == "" || d.FieldType == "" {
			t.Errorf("onboarding[%d] = %+v, want populated Slug + FieldType", i, d)
		}
	}
	if h.Branding == nil || h.Navigation == nil || h.Settings == nil || h.Policies == nil {
		t.Errorf("blob maps: branding=%v navigation=%v settings=%v policies=%v, want all non-nil",
			h.Branding != nil, h.Navigation != nil, h.Settings != nil, h.Policies != nil)
	}
	if err := h.Validate(c); err != nil {
		t.Errorf("Validate(community) = %v, want nil", err)
	}
	if ids := c.HubTemplateIDs(); !reflect.DeepEqual(ids, []string{"community"}) {
		t.Errorf("HubTemplateIDs() = %v, want [community]", ids)
	}
}

func TestHubTemplateValidate_Invariants(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *HubTemplate)
	}{
		{"pages empty", func(h *HubTemplate) { h.Pages = nil }},
		{"empty page slug", func(h *HubTemplate) { h.Pages[2].Slug = "" }},
		{"reserved page slug home", func(h *HubTemplate) { h.Pages[1].Slug = "home" }},
		{"duplicate page slug", func(h *HubTemplate) { h.Pages[1].Slug = h.Pages[0].Slug }},
		{"bad page privacy", func(h *HubTemplate) { h.Pages[0].Privacy = "everyone" }},
		{"empty page privacy", func(h *HubTemplate) { h.Pages[0].Privacy = "" }},
		{"unknown pageTemplate ref", func(h *HubTemplate) { h.Pages[0].PageTemplate = "no-such-template" }},
		{"pageTemplate is a section template", func(h *HubTemplate) { h.Pages[0].PageTemplate = "hero" }},
		{"zero homepages", func(h *HubTemplate) { h.Pages[0].IsHomepage = false }},
		{"two homepages", func(h *HubTemplate) { h.Pages[1].IsHomepage = true }},
		{"bad space access_level", func(h *HubTemplate) { h.Spaces[0].AccessLevel = "secret" }},
		{"bad space posting_permission", func(h *HubTemplate) { h.Spaces[0].PostingPermission = "nobody" }},
		{"empty space slug", func(h *HubTemplate) { h.Spaces[0].Slug = "" }},
		{"duplicate space slug", func(h *HubTemplate) { h.Spaces[1].Slug = h.Spaces[0].Slug }},
		{"bad onboarding field_type", func(h *HubTemplate) { h.Onboarding[0].FieldType = "email" }},
		{"empty onboarding slug", func(h *HubTemplate) { h.Onboarding[0].Slug = "" }},
		{"duplicate onboarding slug", func(h *HubTemplate) { h.Onboarding[1].Slug = h.Onboarding[0].Slug }},
		// community ships no playlists — construct them in the mutation.
		{"empty playlist key", func(h *HubTemplate) {
			h.Playlists = []TemplatePlaylist{{Title: "Welcome", Key: "", Visibility: "public"}}
		}},
		{"duplicate playlist key", func(h *HubTemplate) {
			h.Playlists = []TemplatePlaylist{
				{Title: "Welcome", Key: "welcome", Visibility: "public"},
				{Title: "Onboarding", Key: "welcome", Visibility: "members"},
			}
		}},
		{"bad playlist visibility", func(h *HubTemplate) {
			h.Playlists = []TemplatePlaylist{{Title: "Welcome", Key: "welcome", Visibility: "everyone"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh catalog per case: the mutations write through the HubTemplate's
			// slices, whose backing arrays are shared with the catalog's copy.
			c := load21ForTest(t)
			h, ok := c.HubTemplateByID("community")
			if !ok {
				t.Fatal("HubTemplateByID(community) not found")
			}
			if err := h.Validate(c); err != nil {
				t.Fatalf("pristine community must validate before mutation: %v", err)
			}
			tc.mutate(&h)
			if err := h.Validate(c); err == nil {
				t.Error("Validate() = nil, want error")
			}
		})
	}
}

func TestApplicationID_MatchesBackendVector(t *testing.T) {
	// Backend op computes sha256hex(hub_id + "\x1f" + hub_template_id); these
	// vectors pin the CLI to the same bytes.
	vectors := map[[2]string]string{
		{"hub_1", "community"}:   "33a81a6cc6b3f247d36d4a2487307fbebbe931f8fdcef17125b86af123b5aee5",
		{"hub_abc", "community"}: "21a8db36b1bb7a6a72926e17c62d40824533c5b9c5cac1b17a8502c8d0feaa7c",
	}
	for in, want := range vectors {
		if got := ApplicationID(in[0], in[1]); got != want {
			t.Errorf("ApplicationID(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestTreeDigest_StableAndPrefixed(t *testing.T) {
	a := map[string]any{
		"kind": "stack",
		"settings": map[string]any{
			"gap":  json.Number("8"),
			"slot": "root",
		},
		"children": []any{
			map[string]any{"kind": "field", "id": "f1"},
		},
	}
	// Same tree, different literal key order.
	b := map[string]any{
		"children": []any{
			map[string]any{"id": "f1", "kind": "field"},
		},
		"settings": map[string]any{
			"slot": "root",
			"gap":  json.Number("8"),
		},
		"kind": "stack",
	}
	da, err := TreeDigest(a)
	if err != nil {
		t.Fatalf("TreeDigest(a): %v", err)
	}
	if ok, _ := regexp.MatchString(`^sha256:[0-9a-f]{64}$`, da); !ok {
		t.Errorf("TreeDigest = %q, want sha256:<64 hex>", da)
	}
	db, err := TreeDigest(b)
	if err != nil {
		t.Fatalf("TreeDigest(b): %v", err)
	}
	if da != db {
		t.Errorf("TreeDigest is key-order sensitive: %q != %q", da, db)
	}
	// And repeated runs over the same value are stable.
	da2, err := TreeDigest(a)
	if err != nil {
		t.Fatalf("TreeDigest(a) rerun: %v", err)
	}
	if da != da2 {
		t.Errorf("TreeDigest not stable across runs: %q != %q", da, da2)
	}
}

func TestCloneNode_DeepCopies(t *testing.T) {
	orig := Node{
		"kind": "stack",
		"settings": map[string]any{
			"slot": "root",
		},
		"children": []any{
			map[string]any{"kind": "field", "id": "f1"},
		},
	}
	clone := CloneNode(orig)
	clone["kind"] = "container"
	clone["settings"].(map[string]any)["slot"] = "MUTATED"
	clone["children"].([]any)[0].(map[string]any)["id"] = "MUTATED"

	if orig["kind"] != "stack" {
		t.Errorf("orig.kind = %v, mutated through clone", orig["kind"])
	}
	if got := orig["settings"].(map[string]any)["slot"]; got != "root" {
		t.Errorf("orig.settings.slot = %v, mutated through clone", got)
	}
	if got := orig["children"].([]any)[0].(map[string]any)["id"]; got != "f1" {
		t.Errorf("orig.children[0].id = %v, mutated through clone", got)
	}
}
