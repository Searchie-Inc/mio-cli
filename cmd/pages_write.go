package cmd

// pages_write.go — shared flag→attribute mapping for `mio pages create` and
// `mio pages update`, aligned to the backend PageCreate/PageUpdateAttributes
// schema (extra="forbid"), plus small helpers used by the sections reorder
// command (MIO-2257).

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// pagesPrivacyValues is the PagePrivacy enum the backend accepts (schema-
// constrained, MIO-530). Validated client-side so a typo exits ExitUsage
// instead of a 422 round-trip.
var pagesPrivacyValues = map[string]bool{"public": true, "members": true, "private": true}

// PageInput carries the resolved create/update page attributes, decoupled from
// *cobra.Command so both the `pages` commands and the scaffold homepage step
// (MIO-2543) can build the same write body. Each scalar pointer is nil when the
// flag was unset; Settings/Meta are the already-parsed JSON objects (nil = unset).
type PageInput struct {
	Title    *string
	Slug     *string
	Type     *string
	IsHome   *bool          // → is_homepage
	Position *int           // must be >= 0
	Privacy  *string        // validated enum
	Settings map[string]any // → settings (parsed JSON object)
	Meta     map[string]any // → meta (parsed JSON object)
}

// buildPageAttrs assembles the page write body from p using the exact backend
// attribute keys: is_home maps to is_homepage, privacy is validated against the
// allowed enum, position must be >= 0, and settings/meta pass through as JSON
// objects. It is a pure builder (takes data, not flags) so the scaffold gets the
// same privacy/position validation the command does. The removed
// published/description/layout fields are intentionally absent — not in the schema.
func buildPageAttrs(p PageInput) (map[string]any, error) {
	attrs := map[string]any{}
	if p.Title != nil {
		attrs["title"] = *p.Title
	}
	if p.Slug != nil {
		attrs["slug"] = *p.Slug
	}
	if p.Type != nil {
		attrs["type"] = *p.Type
	}
	if p.IsHome != nil {
		attrs["is_homepage"] = *p.IsHome
	}
	if p.Position != nil {
		if *p.Position < 0 {
			return nil, errs.New(errs.ExitUsage, "invalid --position %d: must be >= 0", *p.Position)
		}
		attrs["position"] = *p.Position
	}
	if p.Privacy != nil {
		if !pagesPrivacyValues[*p.Privacy] {
			return nil, errs.New(errs.ExitUsage, "invalid --privacy %q: must be public, members, or private", *p.Privacy)
		}
		attrs["privacy"] = *p.Privacy
	}
	if p.Settings != nil {
		attrs["settings"] = p.Settings
	}
	if p.Meta != nil {
		attrs["meta"] = p.Meta
	}
	return attrs, nil
}

// setPageWriteAttrs reads the shared create/update page flags (parsing the
// --settings/--meta JSON objects), builds the write body with buildPageAttrs (the
// privacy/position validation lives there now), and copies it into attrs. Shared
// by `pages create` and `pages update`; behaviour is identical for valid input.
func setPageWriteAttrs(cmd *cobra.Command, attrs map[string]any) error {
	// Parse the --settings/--meta JSON objects at the flag-reading site; a
	// malformed value is an ExitUsage error that fires no HTTP request.
	parsed := map[string]any{}
	if err := setMappedJSONObjectFlag(cmd, parsed, "settings", "settings"); err != nil {
		return err
	}
	if err := setMappedJSONObjectFlag(cmd, parsed, "meta", "meta"); err != nil {
		return err
	}
	settings, _ := parsed["settings"].(map[string]any)
	meta, _ := parsed["meta"].(map[string]any)

	built, err := buildPageAttrs(PageInput{
		Title:    changedString(cmd, "title"),
		Slug:     changedString(cmd, "slug"),
		Type:     changedString(cmd, "type"),
		IsHome:   changedBool(cmd, "is-home"),
		Position: changedInt(cmd, "position"),
		Privacy:  changedString(cmd, "privacy"),
		Settings: settings,
		Meta:     meta,
	})
	if err != nil {
		return err
	}
	for k, v := range built {
		attrs[k] = v
	}
	return nil
}

// splitCSV splits a comma-separated flag value into trimmed, non-empty tokens.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
