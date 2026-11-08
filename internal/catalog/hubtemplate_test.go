package catalog

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

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
	if hp.Title == "" {
		t.Errorf("HomepagePage().Title = %q, want a non-empty page title", hp.Title)
	}
	if hp.Role != "homepage" {
		t.Errorf("HomepagePage().Role = %q, want %q", hp.Role, "homepage")
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
	if terms, _ := h.Policies["terms"].(map[string]any); terms == nil {
		t.Error("community policies.terms is not an object — policy allow-list positive case is vacuous")
	} else if _, ok := terms["enabled"]; !ok {
		t.Error("fixture drift: community policies.terms no longer carries \"enabled\" — policy allow-list positive case is vacuous")
	}
	if ids := c.HubTemplateIDs(); !reflect.DeepEqual(ids, []string{"community"}) {
		t.Errorf("HubTemplateIDs() = %v, want [community]", ids)
	}
}

func TestHubTemplateValidate_AcceptsAdminsAndModeratorsPosting(t *testing.T) {
	c := load21ForTest(t)
	h, ok := c.HubTemplateByID("community")
	if !ok {
		t.Fatal("HubTemplateByID(community) not found")
	}
	h.Spaces[0].PostingPermission = "admins_and_moderators"
	if err := h.Validate(c); err != nil {
		t.Errorf("Validate with admins_and_moderators = %v, want nil", err)
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
		{"non-object policy value", func(h *HubTemplate) {
			h.Policies["terms"] = "yes"
		}},
		{"unknown policy field", func(h *HubTemplate) {
			h.Policies["terms"] = map[string]any{"require_acceptence": true}
		}},
		{"missing playlist title", func(h *HubTemplate) {
			h.Playlists = []TemplatePlaylist{{Title: "", Key: "welcome", Visibility: "public"}}
		}},
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
		{"welcomePost missing title", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{Space: h.Spaces[0].Slug, Title: ""}
		}},
		{"welcomePost missing space", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{Space: "", Title: "Welcome!"}
		}},
		{"welcomePost space not in template", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{Space: "no-such-space", Title: "Welcome!"}
		}},
		{"welcomePost whitespace-only title", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{Space: h.Spaces[0].Slug, Title: "   "}
		}},
		{"welcomePost title with a NUL byte", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{Space: h.Spaces[0].Slug, Title: "Wel\x00come"}
		}},
		{"welcomePost title over 280 code points", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{
				Space: h.Spaces[0].Slug,
				Title: strings.Repeat("é", DiscussionTitleMaxCP+1),
			}
		}},
		{"welcomePost over-cap title survives padding", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{
				Space: h.Spaces[0].Slug,
				Title: " " + strings.Repeat("a", DiscussionTitleMaxCP+1) + " ",
			}
		}},
		{"welcomePost body with a NUL byte", func(h *HubTemplate) {
			h.WelcomePost = &TemplateWelcomePost{
				Space: h.Spaces[0].Slug, Title: "Welcome!", Body: "hi\x00there",
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

func TestHubTemplateWelcomePost_TitleLengthIsCodePointsNotBytes(t *testing.T) {
	c := load21ForTest(t)
	h, ok := c.HubTemplateByID("community")
	if !ok {
		t.Fatal("HubTemplateByID(community) not found")
	}
	for _, tc := range []struct {
		name  string
		title string
	}{
		{"at the cap in code points, double that in bytes", strings.Repeat("é", DiscussionTitleMaxCP)},
		{"over the cap raw, at it once stripped", strings.Repeat("a", DiscussionTitleMaxCP) + "     "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.WelcomePost = &TemplateWelcomePost{Space: h.Spaces[0].Slug, Title: tc.title}
			if err := h.Validate(c); err != nil {
				t.Errorf("Validate() = %v, want nil (%d code points, %d bytes — the cap is code points, measured stripped)",
					err, utf8.RuneCountInString(strings.TrimSpace(tc.title)), len(tc.title))
			}
		})
	}
}

func TestHubTemplateWelcomePost_ParseAndDefault(t *testing.T) {
	if h, ok := load21ForTest(t).HubTemplateByID("community"); !ok {
		t.Fatal("HubTemplateByID(community) not found")
	} else if h.WelcomePost != nil {
		t.Errorf("community.WelcomePost = %+v, want nil — the shipped catalog declares none", h.WelcomePost)
	}

	for _, tc := range []struct {
		name string
		raw  Node
		want TemplateWelcomePost
	}{
		{
			name: "is_published omitted defaults to true",
			raw:  Node{"space": "general", "title": "Welcome!", "body": "Say hi."},
			want: TemplateWelcomePost{Space: "general", Title: "Welcome!", Body: "Say hi.", Published: true},
		},
		{
			name: "is_published false is honored",
			raw:  Node{"space": "general", "title": "Draft", "is_published": false},
			want: TemplateWelcomePost{Space: "general", Title: "Draft", Published: false},
		},
		{
			name: "explicit null does not become a draft",
			raw:  Node{"space": "general", "title": "Welcome!", "is_published": nil},
			want: TemplateWelcomePost{Space: "general", Title: "Welcome!", Published: true},
		},
		{
			name: "stringly-typed value does not become a draft",
			raw:  Node{"space": "general", "title": "Welcome!", "is_published": "true"},
			want: TemplateWelcomePost{Space: "general", Title: "Welcome!", Published: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := parseHubTemplate(Node{"id": "x", "welcomePost": tc.raw})
			if h.WelcomePost == nil {
				t.Fatal("WelcomePost = nil, want parsed")
			}
			if *h.WelcomePost != tc.want {
				t.Errorf("WelcomePost = %+v, want %+v", *h.WelcomePost, tc.want)
			}
		})
	}
}

func TestApplicationID_MatchesBackendVector(t *testing.T) {
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
	da2, err := TreeDigest(a)
	if err != nil {
		t.Fatalf("TreeDigest(a) rerun: %v", err)
	}
	if da != da2 {
		t.Errorf("TreeDigest not stable across runs: %q != %q", da, da2)
	}
}

func TestCloneNode_DeepCopies(t *testing.T) {
	if CloneNode(nil) != nil {
		t.Error("CloneNode(nil) must be nil, not an allocated empty map")
	}
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

func TestParseHubTemplate_IconAndDocuments(t *testing.T) {
	h := parseHubTemplate(Node{
		"id": "starter",
		"spaces": []any{
			map[string]any{"name": "Announcements", "slug": "announcements", "icon": "megaphone"},
			map[string]any{"name": "General", "slug": "general"},
		},
		"playlists": []any{
			map[string]any{
				"title": "Getting Started", "key": "getting-started", "visibility": "public",
				"documents": []any{
					map[string]any{"title": "Add your first lesson", "description": "A placeholder lesson."},
					map[string]any{"title": "Add another lesson"},
				},
			},
			map[string]any{"title": "Second", "key": "second", "visibility": "public"},
		},
	})
	if got := h.Spaces[0].Icon; got != "megaphone" {
		t.Errorf("spaces[0].Icon = %q, want megaphone", got)
	}
	if got := h.Spaces[1].Icon; got != "" {
		t.Errorf("spaces[1].Icon = %q, want empty (the template declares none)", got)
	}
	want := []TemplateDocument{
		{Title: "Add your first lesson", Description: "A placeholder lesson."},
		{Title: "Add another lesson"},
	}
	if !reflect.DeepEqual(h.Playlists[0].Documents, want) {
		t.Errorf("playlists[0].Documents = %+v, want %+v", h.Playlists[0].Documents, want)
	}
	if h.Playlists[1].Documents != nil {
		t.Errorf("playlists[1].Documents = %+v, want nil (declares none)", h.Playlists[1].Documents)
	}
}

func TestHubTemplateValidate_PlaylistVisibilityIsTheCreateEnum(t *testing.T) {
	c := load21ForTest(t)
	base, ok := c.HubTemplateByID("community")
	if !ok {
		t.Fatal("community hub template missing from the 2.1 fixture")
	}
	for _, tc := range []struct {
		visibility string
		wantErr    bool
	}{
		{"public", false},
		{"private", false},
		{"unlisted", false},
		{"members", true},
		{"everyone", true},
	} {
		h := base
		h.Playlists = []TemplatePlaylist{{Title: "Welcome", Key: "welcome", Visibility: tc.visibility}}
		err := h.Validate(c)
		if tc.wantErr && err == nil {
			t.Errorf("visibility %q: Validate returned nil, want an error (playlist CREATE rejects it)", tc.visibility)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("visibility %q: Validate = %v, want nil (playlist CREATE accepts it)", tc.visibility, err)
		}
	}
}

func TestHubTemplateValidate_DocumentNeedsATitle(t *testing.T) {
	c := load21ForTest(t)
	base, _ := c.HubTemplateByID("community")
	h := base
	h.Playlists = []TemplatePlaylist{{
		Title: "Welcome", Key: "welcome", Visibility: "public",
		Documents: []TemplateDocument{{Title: "Fine"}, {Title: "  ", Description: "blank after strip"}},
	}}
	err := h.Validate(c)
	if err == nil || !strings.Contains(err.Error(), "documents[1]") {
		t.Errorf("Validate = %v, want an error naming documents[1] (the synthetic-file register requires a title)", err)
	}
}

func TestHubTemplateValidate_PlaylistDataSourceKeyMustResolve(t *testing.T) {
	bound := Node{
		"kind": "stack",
		"children": []any{
			map[string]any{
				"kind":       "section",
				"dataSource": map[string]any{"type": "playlist", "id": "", "key": "getting-started"},
			},
		},
	}
	c := &Catalog{PageTemplates: []Template{{ID: "page-band", IsPage: true, Starter: bound}}}
	base := HubTemplate{
		ID: "starter",
		Pages: []PageRef{{
			Role: "homepage", PageTemplate: "page-band", Slug: "homepage",
			Title: "Home", Privacy: "public", IsHomepage: true,
		}},
	}

	h := base
	h.Playlists = []TemplatePlaylist{{Title: "Getting Started", Key: "getting-started", Visibility: "public"}}
	if err := h.Validate(c); err != nil {
		t.Fatalf("a key naming one of the template's own playlists must validate, got %v", err)
	}

	for _, tc := range []struct {
		name      string
		playlists []TemplatePlaylist
	}{
		{"no playlists at all", nil},
		{"a playlist under a different key", []TemplatePlaylist{{Title: "Other", Key: "other", Visibility: "public"}}},
	} {
		h := base
		h.Playlists = tc.playlists
		err := h.Validate(c)
		if err == nil {
			t.Errorf("%s: Validate returned nil, want an error — the key can never be filled", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "getting-started") {
			t.Errorf("%s: error must name the unresolvable key; got %v", tc.name, err)
		}
	}
}
