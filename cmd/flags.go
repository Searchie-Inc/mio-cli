package cmd

// flags.go holds small flag-to-attribute helpers shared by all resource command
// files. Resource commands declare their --field flags, then call setStringFlag/
// setIntFlag/setBoolFlag to copy ONLY the flags the user actually changed into
// the attributes map. This preserves partial-update (PATCH) semantics: an unset
// flag is never sent, so it is never overwritten server-side.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// attrKey converts a user-facing kebab-case flag name into the snake_case
// attribute key the JSON:API backend expects. CLI flags are kebab by
// convention (--user-id, --first-name), but every backend write schema uses
// snake_case attribute names (user_id, first_name), so the key — not the flag —
// must be translated when building the attributes map.
func attrKey(flagName string) string {
	return strings.ReplaceAll(flagName, "-", "_")
}

// setStringFlag copies a string flag into attrs[<snake_case name>] iff it was
// set by the user.
func setStringFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetString(name); err == nil {
		attrs[attrKey(name)] = v
	}
}

// setNullableMappedString copies a string flag into attrs[attrName] iff the user
// set it, sending JSON null when the value is empty so an explicit `--flag ""`
// CLEARS the field rather than storing an empty string. The backend distinguishes
// null (clear) from "" (store empty) under exclude_unset semantics; an unset flag
// is left untouched.
func setNullableMappedString(cmd *cobra.Command, attrs map[string]any, name, attrName string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return
	}
	if v == "" {
		attrs[attrName] = nil
	} else {
		attrs[attrName] = v
	}
}

// setIntFlag copies an int flag into attrs[<snake_case name>] iff it was set by
// the user.
func setIntFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetInt(name); err == nil {
		attrs[attrKey(name)] = v
	}
}

// setBoolFlag copies a bool flag into attrs[<snake_case name>] iff it was set by
// the user.
func setBoolFlag(cmd *cobra.Command, attrs map[string]any, name string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetBool(name); err == nil {
		attrs[attrKey(name)] = v
	}
}

// flagValue returns the trimmed value of a string flag, or "" if the flag is
// absent or unset. Unlike setStringFlag it does not care whether the user
// "Changed" it — an unset string flag defaults to "" — so it is the right tool
// for resolver flags (--tag, --email) whose empty value simply means "not
// provided by this path".
func flagValue(cmd *cobra.Command, name string) string {
	if cmd.Flags().Lookup(name) == nil {
		return ""
	}
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// stringArg returns the trimmed value of a positional argument. Cobra's
// ExactArgs(n) guarantees the slot exists but not that it is non-empty, so
// commands that turn a positional into a path segment or a required body field
// use this to reject an all-whitespace argument up front.
func stringArg(arg string) string {
	return strings.TrimSpace(arg)
}

// setMappedString copies a string flag into attrs[key] under an EXPLICIT backend
// field name, iff the user set it. Use this when the snake_cased flag name does
// not equal the backend attribute name (e.g. --from-email → mail_from_email).
func setMappedString(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetString(name); err == nil {
		attrs[key] = v
	}
}

// setMappedInt copies an int flag into attrs[key] under an EXPLICIT backend
// field name, iff the user set it.
func setMappedInt(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetInt(name); err == nil {
		attrs[key] = v
	}
}

// setMappedBool copies a bool flag into attrs[key] under an EXPLICIT backend
// field name, iff the user set it. Use this when the flag name differs from
// the backend attribute name (e.g. --public → is_public).
func setMappedBool(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetBool(name); err == nil {
		attrs[key] = v
	}
}

// setMappedBoolInverted copies a bool flag into attrs[key] under an EXPLICIT
// backend field name with the value NEGATED, iff the user set it. Use this
// when the user-facing flag has the opposite polarity to the backend attribute
// (e.g. --published=true should send is_private=false).
func setMappedBoolInverted(cmd *cobra.Command, attrs map[string]any, name, key string) {
	if !cmd.Flags().Changed(name) {
		return
	}
	if v, err := cmd.Flags().GetBool(name); err == nil {
		attrs[key] = !v
	}
}

// changedString returns a pointer to a string flag's value iff the user changed
// it, and nil otherwise. It is the pointer-returning counterpart of
// setStringFlag: a builder input struct can then distinguish "flag unset" (nil)
// from "flag set to empty" (non-nil ""), preserving the partial-update semantics
// while decoupling the pure builders (MIO-2543) from *cobra.Command. A GetString
// error on a registered string flag is unreachable, so it maps to nil (unset),
// exactly as setStringFlag silently skips it.
func changedString(cmd *cobra.Command, name string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		return nil
	}
	return &v
}

// changedBool returns a pointer to a bool flag's value iff the user changed it,
// and nil otherwise (the pointer-returning counterpart of setBoolFlag).
func changedBool(cmd *cobra.Command, name string) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v, err := cmd.Flags().GetBool(name)
	if err != nil {
		return nil
	}
	return &v
}

// changedInt returns a pointer to an int flag's value iff the user changed it,
// and nil otherwise (the pointer-returning counterpart of setIntFlag).
func changedInt(cmd *cobra.Command, name string) *int {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v, err := cmd.Flags().GetInt(name)
	if err != nil {
		return nil
	}
	return &v
}

// parseJSONFlag parses a flag value as JSON, returning the decoded value (an
// object, array, or scalar). A value beginning with "@" is treated as a path to
// a file whose contents are the JSON (e.g. --conditions @conditions.json). Used
// by commands that accept a structured payload fragment the backend validates
// strictly, such as segment search's condition tree.
func parseJSONFlag(raw string) (any, error) {
	data := []byte(strings.TrimSpace(raw))
	if after, ok := strings.CutPrefix(string(data), "@"); ok {
		b, err := os.ReadFile(after)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", after, err)
		}
		data = b
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// setMappedJSONObjectFlag parses a string flag as a JSON object and stores it
// at attrs[key] under an EXPLICIT backend attribute name, iff the user set the
// flag. A value beginning with "@" is read from a file (see parseJSONFlag).
// The decoded value MUST be a JSON object: these flags map to backend JSONB
// sub-objects (branding, navigation, settings, meta), so an array, scalar or
// null is a usage error rather than a silent pass-through the backend would
// 422 on. Mirrors setMappedString for the JSON-object case. Returns ExitUsage
// on malformed JSON or a non-object value; leaves attrs untouched when unset.
//
// JSON numbers decode via float64, which is lossless for the small UI-scale
// values these hub-config blobs carry (colors, font sizes, positions, flags).
// Integers needing >2^53 precision are out of scope for hub presentation blobs.
func setMappedJSONObjectFlag(cmd *cobra.Command, attrs map[string]any, name, key string) error {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	raw, err := cmd.Flags().GetString(name)
	if err != nil {
		return errs.New(errs.ExitUsage, "--%s: %s", name, err)
	}
	v, err := parseJSONFlag(raw)
	if err != nil {
		return errs.New(errs.ExitUsage, "--%s must be valid JSON: %s", name, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return errs.New(errs.ExitUsage, "--%s must be a JSON object", name)
	}
	attrs[key] = obj
	return nil
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
