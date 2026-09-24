package docsgen

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// pageKindLists splits the rendered page-template-kinds block into its lists,
// keyed outline / complete / system.
func pageKindLists(t *testing.T, body string) map[string][]string {
	t.Helper()
	headers := map[string]string{"Outlines —": "outline", "Complete —": "complete", "System pages —": "system"}
	lists := map[string][]string{}
	current := ""
	for _, line := range strings.Split(body, "\n") {
		header := false
		for prefix, kind := range headers {
			if strings.HasPrefix(line, prefix) {
				current, header = kind, true
			}
		}
		if header || current == "" {
			continue
		}
		for _, f := range strings.Split(line, "`") {
			if strings.HasPrefix(f, "page-") && !strings.ContainsAny(f, " \n") {
				lists[current] = append(lists[current], f)
			}
		}
	}
	if len(lists) == 0 {
		t.Fatalf("page-template-kinds block has no Outlines/Complete/System pages lists:\n%s", body)
	}
	return lists
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// kindOf renders the block for cat and returns the lists naming id.
func kindOf(t *testing.T, cat *catalog.Catalog, id string) []string {
	t.Helper()
	blocks, err := Render(cat)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for kind, ids := range pageKindLists(t, blocks["page-template-kinds"]) {
		if contains(ids, id) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// TestPageTemplateKinds_EmbeddedCatalog pins the split the skill's prose leans
// on: page-homepage is an outline (its hero arrives with `settings:{}` and no
// value), page-sales is complete, page-login and page-file-detail are system
// pages. Every page template lands in exactly one list.
func TestPageTemplateKinds_EmbeddedCatalog(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"page-homepage":    "outline",
		"page-generic":     "outline",
		"page-sales":       "complete",
		"page-login":       "system",
		"page-file-detail": "system",
	} {
		if got := kindOf(t, cat, id); len(got) != 1 || got[0] != want {
			t.Errorf("%s is listed under %v, want [%s]", id, got, want)
		}
	}
	blocks, err := Render(cat)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, ids := range pageKindLists(t, blocks["page-template-kinds"]) {
		total += len(ids)
	}
	if total != len(cat.PageTemplates) {
		t.Errorf("classified %d page templates, the catalog has %d", total, len(cat.PageTemplates))
	}
}

// pageStarter returns the starter of page template id in cat, to edit in place.
func pageStarter(t *testing.T, cat *catalog.Catalog, id string) map[string]any {
	t.Helper()
	for _, pt := range cat.PageTemplates {
		if pt.ID == id {
			return pt.Starter
		}
	}
	t.Fatalf("the embedded catalog has no %s", id)
	return nil
}

// TestPageTemplateKinds_OneValueMakesItComplete: the classifier reads the
// recipe, not a list. Giving page-homepage's headline copy moves it to
// Complete; an empty-string value does not count as copy.
func TestPageTemplateKinds_OneValueMakesItComplete(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"a headline with copy", "Welcome to the hub", "complete"},
		{"an empty-string value", "", "outline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := catalog.Load()
			if err != nil {
				t.Fatal(err)
			}
			hero := pageStarter(t, cat, "page-homepage")["children"].([]any)[0].(map[string]any)
			hero["children"].([]any)[0].(map[string]any)["value"] = tc.value
			if got := kindOf(t, cat, "page-homepage"); len(got) != 1 || got[0] != tc.want {
				t.Errorf("page-homepage with %s is listed under %v, want [%s]", tc.name, got, tc.want)
			}
		})
	}
}

// TestPageTemplateKinds_SectionsDecideSystem: a page whose root children carry
// no section template is a system page even when it carries copy (the blind
// review of #137: page-file-detail's three slot placeholders were filed under
// "finished sections"), and ONE templated root child makes it a content page.
func TestPageTemplateKinds_SectionsDecideSystem(t *testing.T) {
	t.Run("page-sales with its section templates removed", func(t *testing.T) {
		cat, err := catalog.Load()
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range pageStarter(t, cat, "page-sales")["children"].([]any) {
			delete(k.(map[string]any), "template")
		}
		if got := kindOf(t, cat, "page-sales"); len(got) != 1 || got[0] != "system" {
			t.Errorf("page-sales with copy but no sections is listed under %v, want [system]", got)
		}
	})
	t.Run("page-file-detail with one region made a section", func(t *testing.T) {
		cat, err := catalog.Load()
		if err != nil {
			t.Fatal(err)
		}
		region := pageStarter(t, cat, "page-file-detail")["children"].([]any)[1].(map[string]any)
		region["template"] = "row"
		if got := kindOf(t, cat, "page-file-detail"); len(got) != 1 || got[0] != "complete" {
			t.Errorf("page-file-detail with a section carrying copy is listed under %v, want [complete]", got)
		}
	})
}
