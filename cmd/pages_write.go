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

// setPageWriteAttrs copies the shared create/update page flags into attrs using
// the exact backend attribute keys. --is-home maps to is_homepage; --privacy is
// validated against the allowed enum; --settings/--meta accept a JSON object
// (inline or @file). The removed --published/--description/--layout flags are
// intentionally absent — they are not in the schema and would 422.
func setPageWriteAttrs(cmd *cobra.Command, attrs map[string]any) error {
	setStringFlag(cmd, attrs, "title")
	setStringFlag(cmd, attrs, "slug")
	setStringFlag(cmd, attrs, "type")
	setMappedBool(cmd, attrs, "is-home", "is_homepage")

	if cmd.Flags().Changed("position") {
		pos, err := cmd.Flags().GetInt("position")
		if err != nil {
			return errs.New(errs.ExitUsage, "--position: %s", err)
		}
		if pos < 0 {
			return errs.New(errs.ExitUsage, "invalid --position %d: must be >= 0", pos)
		}
		attrs["position"] = pos
	}

	if cmd.Flags().Changed("privacy") {
		p, err := cmd.Flags().GetString("privacy")
		if err != nil {
			return errs.New(errs.ExitUsage, "--privacy: %s", err)
		}
		if !pagesPrivacyValues[p] {
			return errs.New(errs.ExitUsage, "invalid --privacy %q: must be public, members, or private", p)
		}
		attrs["privacy"] = p
	}

	if err := setMappedJSONObjectFlag(cmd, attrs, "settings", "settings"); err != nil {
		return err
	}
	return setMappedJSONObjectFlag(cmd, attrs, "meta", "meta")
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
