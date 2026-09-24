package docsgen

import (
	"strings"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
)

// pageKindLists splits the rendered page-template-kinds block into its two lists.
func pageKindLists(t *testing.T, body string) (outlines, complete []string) {
	t.Helper()
	parts := strings.SplitN(body, "Complete", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "Outlines") {
		t.Fatalf("page-template-kinds block has no Outlines/Complete sections:\n%s", body)
	}
	ids := func(s string) []string {
		var out []string
		for _, f := range strings.Split(s, "`") {
			if strings.HasPrefix(f, "page-") && !strings.ContainsAny(f, " \n") {
				out = append(out, f)
			}
		}
		return out
	}
	return ids(parts[0]), ids(parts[1])
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestPageTemplateKinds_EmbeddedCatalog pins the split the skill's prose leans
// on: page-homepage is an outline (its hero arrives with `settings:{}` and no
// value), page-sales is complete. Every page template lands in exactly one list.
func TestPageTemplateKinds_EmbeddedCatalog(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := Render(cat)
	if err != nil {
		t.Fatal(err)
	}
	outlines, complete := pageKindLists(t, blocks["page-template-kinds"])
	if !contains(outlines, "page-homepage") || contains(complete, "page-homepage") {
		t.Errorf("page-homepage must be an outline; outlines=%v complete=%v", outlines, complete)
	}
	if !contains(complete, "page-sales") || contains(outlines, "page-sales") {
		t.Errorf("page-sales must be complete; outlines=%v complete=%v", outlines, complete)
	}
	if got, want := len(outlines)+len(complete), len(cat.PageTemplates); got != want {
		t.Errorf("classified %d page templates, the catalog has %d: outlines=%v complete=%v", got, want, outlines, complete)
	}
}

// TestPageTemplateKinds_OneValueMakesItComplete: the classifier reads the
// recipe, not a list. Giving page-homepage's headline copy moves it to
// Complete; an empty-string value does not count as copy.
func TestPageTemplateKinds_OneValueMakesItComplete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    any
		complete bool
	}{
		{"a headline with copy", "Welcome to the hub", true},
		{"an empty-string value", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := catalog.Load()
			if err != nil {
				t.Fatal(err)
			}
			for _, pt := range cat.PageTemplates {
				if pt.ID != "page-homepage" {
					continue
				}
				hero := pt.Starter["children"].([]any)[0].(map[string]any)
				headline := hero["children"].([]any)[0].(map[string]any)
				headline["value"] = tc.value
			}
			blocks, err := Render(cat)
			if err != nil {
				t.Fatal(err)
			}
			outlines, complete := pageKindLists(t, blocks["page-template-kinds"])
			if got := contains(complete, "page-homepage"); got != tc.complete {
				t.Errorf("page-homepage with %s: complete=%v, want %v (outlines=%v)", tc.name, got, tc.complete, outlines)
			}
		})
	}
}
