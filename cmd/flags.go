package cmd

// flags.go holds small flag-to-attribute helpers shared by all resource command
// files. Resource commands declare their --field flags, then call setStringFlag/
// setIntFlag/setBoolFlag to copy ONLY the flags the user actually changed into
// the attributes map. This preserves partial-update (PATCH) semantics: an unset
// flag is never sent, so it is never overwritten server-side.

import (
	"net/url"

	"github.com/spf13/cobra"
)

// setStringFlag copies a string flag into attrs[name] iff it was set by the user.
func setStringFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetString(name); err == nil {
		attrs[name] = v
	}
}

// setIntFlag copies an int flag into attrs[name] iff it was set by the user.
func setIntFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetInt(name); err == nil {
		attrs[name] = v
	}
}

// setBoolFlag copies a bool flag into attrs[name] iff it was set by the user.
func setBoolFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetBool(name); err == nil {
		attrs[name] = v
	}
}

// addPaginationFlags registers the JSON:API pagination flags on a list command.
func addPaginationFlags(cmd *cobra.Command) {
	cmd.Flags().Int("limit", 0, "Page size (page[size]).")
	cmd.Flags().String("after", "", "Pagination cursor for the next page (page[after]).")
}

// addPageFlags translates the pagination flags into JSON:API query params.
func addPageFlags(cmd *cobra.Command, query url.Values) {
	if cmd.Flags().Changed("limit") {
		if limit, err := cmd.Flags().GetInt("limit"); err == nil && limit > 0 {
			query.Set("page[size]", itoa(limit))
		}
	}
	if cmd.Flags().Changed("after") {
		if after, err := cmd.Flags().GetString("after"); err == nil && after != "" {
			query.Set("page[after]", after)
		}
	}
}

// firstNonEmpty returns the first non-empty string (cmd-package local helper;
// mirrors the config-package one used during resolution).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func itoa(n int) string {
	// Small dependency-free int→string for query params.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
