package cmd

// hubs_navigation.go — client-side validation for the hub navigation menu blob
// authored via `mio hubs create --navigation-json` and `mio hubs update
// --navigation-json` (MIO-2255).

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// validateNavigationBlob enforces the render-safety invariants the mio-hub
// navigation parser requires (MIO-2233): the header/footer/mobile buckets must
// each be arrays, and every header/footer item must be an object carrying a
// non-empty "type" (url|page|playlist|discussions). Untyped items are silently
// dropped by the frontend, so a menu shipped without types renders empty — the
// CLI rejects them up front. The mobile bucket uses a different
// {id,label,route,icon} shape and is only checked for being an array of
// objects. Returns ExitUsage on the first violation.
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

// validateNavigationHrefs enforces hub-scoped href safety for header/footer menu
// items authored via --navigation-json (MIO-2270). Every mio-hub is mounted
// under the path "/{slug}", so a menu item that links to a bare "/about" would
// escape the hub and 404 (or leak into a sibling hub). For each header/footer
// item with type=="url": if its href is a RELATIVE path (a leading "/"), it MUST
// stay within this hub — i.e. equal "/{slug}" or continue past it on a path/
// query/fragment boundary ("/{slug}/…", "/{slug}?…", "/{slug}#…"). Absolute
// http(s):// links — and any non-slash href (mailto:, tel:, "#anchor", a bare
// relative "foo") — are passed through as-is. Returns ExitUsage naming the
// offending item on the first violation.
//
// slug is the hub's own slug: the --slug flag value on create, or the retrieved
// hub's slug on update (the CLI does not otherwise know it client-side). The
// mobile bucket uses a {id,label,route,icon} shape rather than {type,href} and
// is not scoped here. Call after validateNavigationBlob, which pins the buckets
// to arrays of objects.
func validateNavigationHrefs(nav map[string]any, slug string) error {
	slug = strings.TrimSpace(slug)
	for _, bucket := range []string{"header", "footer"} {
		raw, ok := nav[bucket]
		if !ok {
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			// Bucket shape is validated by validateNavigationBlob; nothing to scope.
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
			// Classify on the backslash-folded form: browsers fold "\" to "/", so a
			// leading-backslash href like "\outside" is really the path "/outside",
			// and "\\host\x" is really the origin-escaping "//host/x". Only a href
			// that (after folding) is NOT a leading-slash path — an absolute
			// http(s):// URL, mailto:, an "#anchor", a bare relative "foo", or empty
			// — is passed through as-is; everything else is scoped to the hub.
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

// hrefScopedToHub reports whether a leading-slash href RESOLVES to a path within
// the hub mounted at "/{slug}". The href's query/fragment are ignored and its
// path is dot-segment-resolved (path.Clean), including percent-encoded dot
// segments which url.Parse decodes — so "/{slug}/../escape" (which a browser
// resolves to "/escape") does NOT slip past a naive prefix check. The resolved
// path must equal "/{slug}" exactly or continue past it on a "/" segment
// boundary, so a sibling path like "/{slug}-other" whose slug is merely a
// string prefix is NOT accepted. An unparseable href is treated as unscoped.
//
// Any origin-escaping form is rejected even when its path happens to match the
// slug: a protocol-relative "//host/{slug}", the empty-authority "///​{slug}"
// (browsers resolve to "https://{slug}/"), a backslash variant "/\host/{slug}"
// (browsers fold "\" to "/"), or a scheme/host-bearing URL all leave this
// origin. Only a genuine same-origin path can be hub-scoped.
func hrefScopedToHub(href, slug string) bool {
	if slug == "" {
		return false
	}
	// Browsers fold backslashes to forward slashes when resolving the authority,
	// so normalize before deciding whether this is a same-origin path.
	norm := strings.ReplaceAll(href, `\`, "/")
	// A leading "//" is protocol-relative or an empty-authority form that a
	// browser resolves to a different origin — never a same-origin hub path.
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

// navItemName renders a human-friendly identifier for a menu item in error
// messages: its "label" if present, else its href.
func navItemName(obj map[string]any, href string) string {
	if label, _ := obj["label"].(string); strings.TrimSpace(label) != "" {
		return fmt.Sprintf("%q", label)
	}
	return fmt.Sprintf("href %q", href)
}
