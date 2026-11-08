package catalog

import "slices"

func ReservedPageSlugs() []string {
	return []string{
		"access-denied", "account", "banned", "discussions", "editor-fixture",
		"forgot", "forgot-password", "history", "home", "legal", "login",
		"members", "messages", "moderation", "my-list", "notifications",
		"onboarding", "payment", "playlists", "register", "reset-password", "search",
	}
}

func IsReservedPageSlug(slug string) bool {
	return slices.Contains(ReservedPageSlugs(), slug)
}
