package catalog

import "slices"

// The source of truth is mio-backend app/pages/service.py RESERVED_SLUGS,
// itself mirrored from mio-hub reserved-slugs.ts. This is a hand-maintained
// mirror: sync it when the backend set changes. Check parity by setting
// MIO_BACKEND_PAGES_SERVICE_PY to a copy of that file and running
// go test ./internal/catalog -run ReservedPageSlugs.

// ReservedPageSlugs returns the backend-reserved page slugs in sorted order.
func ReservedPageSlugs() []string {
	return []string{
		"access-denied", "account", "banned", "discussions", "editor-fixture",
		"forgot", "forgot-password", "history", "home", "legal", "login",
		"members", "messages", "moderation", "my-list", "notifications",
		"onboarding", "payment", "playlists", "register", "reset-password", "search",
	}
}

// IsReservedPageSlug reports whether slug is reserved by the backend mirror.
func IsReservedPageSlug(slug string) bool {
	return slices.Contains(ReservedPageSlugs(), slug)
}
