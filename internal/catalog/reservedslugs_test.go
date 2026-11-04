package catalog

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestReservedPageSlugs_MatchBackendSource(t *testing.T) {
	path := os.Getenv("MIO_BACKEND_PAGES_SERVICE_PY")
	if path == "" {
		t.Skip("run: git -C ../mio-backend show origin/main:app/pages/service.py > /tmp/service.py; MIO_BACKEND_PAGES_SERVICE_PY=/tmp/service.py go test ./internal/catalog -run ReservedPageSlugs")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assignment := regexp.MustCompile(`(?s)\bRESERVED_SLUGS\s*:[^=\n]+\s*=\s*frozenset\s*\(\s*\{(.*?)\}`)
	var source []string
	for _, line := range strings.Split(string(body), "\n") {
		source = append(source, strings.SplitN(line, "#", 2)[0])
	}
	match := assignment.FindStringSubmatch(strings.Join(source, "\n"))
	if match == nil {
		t.Fatal("RESERVED_SLUGS frozenset assignment not found")
	}
	quoted := regexp.MustCompile(`["']([^"']+)["']`)
	backend := map[string]bool{}
	for _, line := range strings.Split(match[1], "\n") {
		line = strings.SplitN(line, "#", 2)[0]
		for _, m := range quoted.FindAllStringSubmatch(line, -1) {
			backend[m[1]] = true
		}
	}
	if len(backend) == 0 {
		t.Fatal("RESERVED_SLUGS parsed empty")
	}
	mirror := map[string]bool{}
	for _, slug := range ReservedPageSlugs() {
		mirror[slug] = true
	}
	var missing, extra []string
	for slug := range backend {
		if !mirror[slug] {
			missing = append(missing, slug)
		}
	}
	for slug := range mirror {
		if !backend[slug] {
			extra = append(extra, slug)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Errorf("backend slugs missing from mirror: %v; extra in mirror: %v", missing, extra)
	}
}

func TestHubTemplateValidate_ReservedPageSlugs(t *testing.T) {
	for _, slug := range ReservedPageSlugs() {
		t.Run(slug, func(t *testing.T) {
			c := load21ForTest(t)
			h, _ := c.HubTemplateByID("community")
			h.Pages[1].Slug = slug
			err := h.Validate(c)
			if err == nil || !strings.Contains(err.Error(), `slug "`+slug+`" is backend-reserved`) {
				t.Errorf("slug %q: got %v, want backend-reserved error", slug, err)
			}
		})
	}
	for _, slug := range []string{"content", "ordinary-page"} {
		t.Run("accept_"+slug, func(t *testing.T) {
			c := load21ForTest(t)
			h, _ := c.HubTemplateByID("community")
			h.Pages[1].Slug = slug
			if err := h.Validate(c); err != nil {
				t.Errorf("slug %q: %v", slug, err)
			}
		})
	}
}
