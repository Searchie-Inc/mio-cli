package catalog

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const pinnedDigest = "sha256:2e60ed405c1ec38d0073ab0163a412d34e285d25708d93ae6f2b5cdf5a11811b"

func fixtureName(id, variant string) string {
	if variant != "" {
		return id + "." + variant + ".json"
	}
	return id + ".json"
}

func TestDigest_MatchesMetaAndPin(t *testing.T) {
	c := loadForTest(t)
	computed, err := Digest(c.Raw())
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if computed != pinnedDigest {
		t.Errorf("computed digest %s != pinned %s (CanonicalJSON diverges from the TS canonicalizer)", computed, pinnedDigest)
	}
	if c.DigestPinned() != pinnedDigest {
		t.Errorf("vendored meta.digest %s != pinned %s", c.DigestPinned(), pinnedDigest)
	}
	if computed != c.DigestPinned() {
		t.Errorf("computed digest %s != vendored meta.digest %s", computed, c.DigestPinned())
	}
}

func TestPinnedRef_WellFormed(t *testing.T) {
	ref := PinnedRef()
	if len(ref) != 40 {
		t.Fatalf("CATALOG_REF = %q (len %d); want a full 40-char commit SHA", ref, len(ref))
	}
	for _, r := range ref {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("CATALOG_REF = %q; want lowercase hex only", ref)
		}
	}
}

func TestGoldenFixtureSet_ExactlyMatches(t *testing.T) {
	c := loadForTest(t)
	want := map[string]bool{}
	for _, tmpl := range c.Templates {
		want[fixtureName(tmpl.ID, "")] = true
		for v := range tmpl.Variants {
			want[fixtureName(tmpl.ID, v)] = true
		}
	}
	for _, p := range c.PageTemplates {
		want[fixtureName(p.ID, "")] = true
	}

	entries, err := os.ReadDir(filepath.Join("testdata", "fixtures"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			got[e.Name()] = true
		}
	}

	for name := range want {
		if !got[name] {
			t.Errorf("missing golden fixture %q", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("stray golden fixture %q (no matching template/variant/page)", name)
		}
	}
}

func TestGoldenParity_Templates(t *testing.T) {
	c := loadForTest(t)
	for _, tmpl := range c.Templates {
		variants := []string{""}
		vs := make([]string, 0, len(tmpl.Variants))
		for v := range tmpl.Variants {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		variants = append(variants, vs...)

		for _, v := range variants {
			name := tmpl.ID
			if v != "" {
				name += " :: " + v
			}
			t.Run(name, func(t *testing.T) {
				assertGoldenParity(t, tmpl, v)
			})
		}
	}
}

func TestGoldenParity_PageTemplates(t *testing.T) {
	c := loadForTest(t)
	for _, p := range c.PageTemplates {
		t.Run(p.ID, func(t *testing.T) {
			assertGoldenParity(t, p, "")
		})
	}
}

func assertGoldenParity(t *testing.T, tmpl Template, variant string) {
	t.Helper()
	instantiated, err := InstantiateTemplate(tmpl, variant, NewUUIDv7Gen())
	if err != nil {
		t.Fatalf("InstantiateTemplate(%s, %q): %v", tmpl.ID, variant, err)
	}
	got := NormalizeIDs(instantiated)
	golden := readJSONNumber(t, filepath.Join("testdata", "fixtures", fixtureName(tmpl.ID, variant)))

	gotCanon := mustCanonical(t, got)
	goldenCanon := mustCanonical(t, golden)
	if gotCanon != goldenCanon {
		t.Errorf("applier output diverges from golden fixture %s\n got=%s\n want=%s",
			fixtureName(tmpl.ID, variant), gotCanon, goldenCanon)
	}
}
