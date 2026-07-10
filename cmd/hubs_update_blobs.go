package cmd

// hubs_update_blobs.go — read-modify-write helpers for updating the hub's
// whole-blob JSONB fields (branding / settings / meta) via `mio hubs update`
// without clobbering sibling keys (MIO-2256).

import (
	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// parseJSONObjectFlag parses a string flag as a JSON object (inline or @file),
// returning nil when the flag is unset. Returns ExitUsage on malformed JSON or
// a non-object value. Unlike setMappedJSONObjectFlag it RETURNS the object so
// the caller can read-modify-write it against the hub's current blob rather
// than setting it directly.
func parseJSONObjectFlag(cmd *cobra.Command, name string) (map[string]any, error) {
	if !cmd.Flags().Changed(name) {
		return nil, nil
	}
	raw, err := cmd.Flags().GetString(name)
	if err != nil {
		return nil, errs.New(errs.ExitUsage, "--%s: %s", name, err)
	}
	v, err := parseJSONFlag(raw)
	if err != nil {
		return nil, errs.New(errs.ExitUsage, "--%s must be valid JSON: %s", name, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errs.New(errs.ExitUsage, "--%s must be a JSON object", name)
	}
	return obj, nil
}

// deepMergeMap returns a new map that is dst with src merged on top: nested
// objects merge recursively; scalars and arrays are overwritten by src. Used so
// a partial --branding-json/--settings-json/--meta-json edit preserves the
// untouched sibling keys of a whole-blob JSONB field.
func deepMergeMap(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := out[k].(map[string]any); ok {
				out[k] = deepMergeMap(dv, sv)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// attrMap returns a shallow COPY of v as a map[string]any, or a fresh empty map
// when v is nil or not an object (e.g. a hub whose branding is currently null).
// Copying keeps callers from mutating the retrieved resource's attributes in
// place (e.g. the logo-only branding path).
func attrMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = val
	}
	return out
}
