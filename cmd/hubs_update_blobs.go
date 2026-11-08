package cmd

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var unsetBlobs = map[string]bool{
	"branding": true,
	"settings": true,
	"meta":     true,
}

type unsetPath struct {
	blob     string
	segments []string
	raw      string
}

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
		return out
	}
	out[head] = deleteAtPath(child, segments[1:])
	return out
}

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
