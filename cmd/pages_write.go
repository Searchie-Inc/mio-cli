package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var pagesPrivacyValues = map[string]bool{"public": true, "members": true, "private": true}

type PageInput struct {
	Title    *string
	Slug     *string
	Type     *string
	IsHome   *bool
	Position *int
	Privacy  *string
	Settings map[string]any
	Meta     map[string]any
}

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

func setPageWriteAttrs(cmd *cobra.Command, attrs map[string]any) error {
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
