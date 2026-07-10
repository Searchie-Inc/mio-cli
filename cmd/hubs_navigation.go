package cmd

// hubs_navigation.go — client-side validation for the hub navigation menu blob
// authored via `mio hubs create --navigation-json` and `mio hubs update
// --navigation-json` (MIO-2255).

import (
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
