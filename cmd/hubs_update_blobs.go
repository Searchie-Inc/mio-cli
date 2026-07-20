package cmd

// hubs_update_blobs.go — read-modify-write helpers for updating the hub's
// whole-blob JSONB fields (branding / settings / meta) via `mio hubs update`
// without clobbering sibling keys (MIO-2256).

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// unsetBlobs is the set of whole-blob JSONB attributes a --unset path may target.
// The first segment of a dotted path selects the blob; the remaining segments are
// the key path within it. Only these three are read-modify-write blobs the CLI
// owns; navigation is a whole-blob REPLACE (use --navigation-json) and typed
// columns are set with their own flags, so they are rejected as unset targets.
var unsetBlobs = map[string]bool{
	"branding": true,
	"settings": true,
	"meta":     true,
}

// unsetPath is one parsed --unset target: the blob it lives in and the key path
// WITHIN that blob (never empty).
type unsetPath struct {
	blob     string   // branding | settings | meta
	segments []string // key path within the blob, e.g. ["registration","enabled"]
	raw      string   // original dotted path, for error/message context
}

// parseUnsetFlag collects the repeatable --unset values (each also comma-split),
// trims and validates them, and returns the parsed paths. Returns (nil, nil) when
// --unset was not given. Validation is pre-auth so a bad path exits ExitUsage and
// fires NO HTTP request:
//   - an --unset with only blank/empty entries is a usage error;
//   - a path with an empty segment (e.g. "settings..enabled") is a usage error;
//   - a bare blob name with no key (e.g. "settings") is a usage error — deleting a
//     whole blob is not supported (there is nothing to remove within it);
//   - a path whose first segment is not branding/settings/meta is a usage error.
func parseUnsetFlag(cmd *cobra.Command) ([]unsetPath, error) {
	if !cmd.Flags().Changed("unset") {
		return nil, nil
	}
	raw, err := cmd.Flags().GetStringArray("unset")
	if err != nil {
		return nil, errs.New(errs.ExitUsage, "--unset: %s", err)
	}
	var paths []unsetPath
	for _, entry := range raw {
		for _, part := range strings.Split(entry, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				return nil, errs.New(errs.ExitUsage,
					"--unset %q has a blank entry (stray comma?); every comma-separated entry must be a dotted path", entry)
			}
			segs := strings.Split(p, ".")
			for i := range segs {
				segs[i] = strings.TrimSpace(segs[i])
				if segs[i] == "" {
					return nil, errs.New(errs.ExitUsage,
						"--unset %q has an empty path segment", p)
				}
			}
			if len(segs) < 2 {
				return nil, errs.New(errs.ExitUsage,
					"--unset %q must be a dotted path into a blob, e.g. settings.registration.enabled (the first segment selects the blob: branding, settings, meta)", p)
			}
			if !unsetBlobs[segs[0]] {
				return nil, errs.New(errs.ExitUsage,
					"--unset %q: unknown blob %q; the first path segment must be one of branding, settings, meta", p, segs[0])
			}
			paths = append(paths, unsetPath{blob: segs[0], segments: segs[1:], raw: p})
		}
	}
	if len(paths) == 0 {
		return nil, errs.New(errs.ExitUsage,
			"--unset is empty: pass at least one dotted path (e.g. settings.registration.enabled)")
	}
	return paths, nil
}

// deleteAtPath returns a COPY of blob with the key at the dotted segments path
// removed. It copies each map node it descends through (copy-on-write) so the
// retrieved resource — potentially shared with a same-command merge — is never
// mutated in place. A path that does not exist, or that descends into a
// non-object node, is an idempotent no-op (the returned copy is unchanged).
// segments must be non-empty (guaranteed by parseUnsetFlag).
func deleteAtPath(blob map[string]any, segments []string) map[string]any {
	out := make(map[string]any, len(blob))
	for k, v := range blob {
		out[k] = v
	}
	if len(segments) == 0 {
		return out
	}
	head := segments[0]
	if len(segments) == 1 {
		delete(out, head)
		return out
	}
	child, ok := out[head].(map[string]any)
	if !ok {
		// Missing key or a non-object along the path: nothing to delete.
		return out
	}
	out[head] = deleteAtPath(child, segments[1:])
	return out
}

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
