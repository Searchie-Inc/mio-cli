package cmd

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func validateNavigationBlob(nav map[string]any) error {
	for _, bucket := range []string{"header", "footer", "mobile"} {
		raw, ok := nav[bucket]
		if !ok {
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			return errs.New(errs.ExitUsage, "navigation.%s must be an array", bucket)
		}
		for i, it := range items {
			obj, ok := it.(map[string]any)
			if !ok {
				return errs.New(errs.ExitUsage, "navigation.%s[%d] must be an object", bucket, i)
			}
			if bucket == "mobile" {
				continue
			}
			if t, _ := obj["type"].(string); strings.TrimSpace(t) == "" {
				return errs.New(errs.ExitUsage,
					"navigation.%s[%d] must have a non-empty \"type\" (url|page|playlist|discussions); untyped items are dropped by the hub renderer", bucket, i)
			}
		}
	}
	return nil
}

func validateNavigationHrefs(nav map[string]any, slug string) error {
	slug = strings.TrimSpace(slug)
	for _, bucket := range []string{"header", "footer"} {
		raw, ok := nav[bucket]
		if !ok {
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			continue
		}
		for i, it := range items {
			obj, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := obj["type"].(string); t != "url" {
				continue
			}
			href, _ := obj["href"].(string)
			href = strings.TrimSpace(href)
			if !strings.HasPrefix(strings.ReplaceAll(href, `\`, "/"), "/") {
				continue
			}
			if hrefScopedToHub(href, slug) {
				continue
			}
			name := navItemName(obj, href)
			if slug == "" {
				return errs.New(errs.ExitUsage,
					"navigation.%s[%d] (%s) has a hub-relative href %q but the hub slug is unknown; set --slug so the CLI can verify the link stays within this hub, or use an absolute http(s):// URL",
					bucket, i, name, href)
			}
			return errs.New(errs.ExitUsage,
				"navigation.%s[%d] (%s) href %q must stay within this hub: start it with \"/%s\" (or use an absolute http(s):// URL)",
				bucket, i, name, href, slug)
		}
	}
	return nil
}

func hrefScopedToHub(href, slug string) bool {
	if slug == "" {
		return false
	}
	norm := strings.ReplaceAll(href, `\`, "/")
	if !strings.HasPrefix(norm, "/") || strings.HasPrefix(norm, "//") {
		return false
	}
	u, err := url.Parse(norm)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return false
	}
	cleaned := path.Clean(u.Path)
	prefix := "/" + slug
	return cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/")
}

func navItemName(obj map[string]any, href string) string {
	if label, _ := obj["label"].(string); strings.TrimSpace(label) != "" {
		return fmt.Sprintf("%q", label)
	}
	return fmt.Sprintf("href %q", href)
}
