package hubtemplate

import (
	"strings"
	"testing"
)

// validTemplate returns a fully valid *Template that Validate() accepts. Rejection
// cases clone-and-mutate a single field so only the field under test is bad.
func validTemplate() *Template {
	return &Template{
		ID: "x",
		Spaces: []Space{
			{Name: "General", Slug: "general", AccessLevel: "public", PostingPermission: "any_member"},
		},
		Onboarding: []AttrDef{
			{Name: "Company", Slug: "company", FieldType: "text"},
		},
		Playlists: []Playlist{
			{Title: "Welcome", Key: "welcome", Visibility: "public"},
		},
		Homepage: &HomepageRef{Template: "home-static-cards"},
	}
}

func TestLoad_Community(t *testing.T) {
	tmpl, err := Load("community")
	if err != nil {
		t.Fatalf("Load(community): %v", err)
	}
	if tmpl.ID == "" {
		t.Errorf("ID is empty")
	}
	if len(tmpl.Spaces) == 0 {
		t.Errorf("want >=1 space, got 0")
	}
	if tmpl.Homepage == nil {
		t.Errorf("Homepage is nil")
	}
}

func TestLoad_Unknown(t *testing.T) {
	_, err := Load("nope")
	if err == nil {
		t.Fatal("want error for unknown template")
	}
	if !strings.Contains(err.Error(), "unknown hub template") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "unknown hub template")
	}
}

func TestList(t *testing.T) {
	got := List()
	if len(got) != 1 || got[0] != "community" {
		t.Errorf("List() = %v, want [community]", got)
	}
}

func TestValidate_Accepts(t *testing.T) {
	if err := validTemplate().Validate(); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
}

// TestValidate_Rejects covers every rejection path. Each case asserts the error
// MESSAGE contains an expected substring — not merely that some error occurred —
// so a case cannot pass for the wrong reason (a refactor rejecting for an
// unrelated cause would fail here instead of staying spuriously green).
func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
		mut     func(*Template)
	}{
		{"missing id", "missing id", func(tm *Template) { tm.ID = "" }},
		{"space missing slug", "spaces[0] missing slug", func(tm *Template) { tm.Spaces[0].Slug = "" }},
		{"bad access_level", "invalid access_level", func(tm *Template) { tm.Spaces[0].AccessLevel = "bogus" }},
		{"bad posting_permission", "invalid posting_permission", func(tm *Template) { tm.Spaces[0].PostingPermission = "bogus" }},
		{"onboarding missing slug", "onboarding[0] missing slug", func(tm *Template) { tm.Onboarding[0].Slug = "" }},
		{"onboarding missing field_type", "onboarding[0] missing field_type", func(tm *Template) { tm.Onboarding[0].FieldType = "" }},
		{"onboarding bad field_type", "onboarding[0] invalid field_type", func(tm *Template) { tm.Onboarding[0].FieldType = "bogus" }},
		{"playlist missing title", "playlists[0] missing title", func(tm *Template) { tm.Playlists[0].Title = "" }},
		{"playlist missing key", "playlists[0] missing key", func(tm *Template) { tm.Playlists[0].Key = "" }},
		{"playlist bad visibility", "invalid visibility", func(tm *Template) { tm.Playlists[0].Visibility = "bogus" }},
		{"homepage empty template", "homepage template is required", func(tm *Template) { tm.Homepage.Template = "" }},
		{"homepage nil", "homepage template is required", func(tm *Template) { tm.Homepage = nil }},
		{"homepage bad privacy", "homepage invalid privacy", func(tm *Template) { tm.Homepage.Privacy = "bogus" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tm := validTemplate()
			c.mut(tm)
			err := tm.Validate()
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, c.wantErr)
			}
		})
	}
}
