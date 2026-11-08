package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

var brandingKeys = map[string]bool{
	"logo_url":              true,
	"favicon_url":           true,
	"social_image_url":      true,
	"auth_logo_url":         true,
	"custom_login_logo_url": true,
	"custom_font_url":       true,
	"primary":               true,
	"secondary":             true,
	"background":            true,
	"text":                  true,
	"primary_color":         true,
	"secondary_color":       true,
	"background_color":      true,
	"header_color":          true,
	"header_accent":         true,
	"dark_mode":             true,
	"gradient":              true,
	"font_heading":          true,
	"font_body":             true,
	"heading_font_size":     true,
	"body_font_size":        true,
	"labels":                true,
}

var metaKeys = map[string]bool{
	"memberDirectory": true,
	"discussions":     true,
	"directMessages":  true,
	"moderation":      true,
}

var settingsVerbatimNestedKeys = map[string]map[string]bool{
	"achievements": {"enabled": true},
}

var (
	brandingKeysHelp = strings.Join(sortedKeySet(brandingKeys), ", ")
	metaKeysHelp     = strings.Join(sortedKeySet(metaKeys), ", ")
)

const (
	settingsKeysHelpText = "Settings keys are left to the API, which rejects an unknown top-level key, or an unknown sub-key of policies/registration/email/auth, with a 422 (exit 2) that names it. " +
		"The CLI checks only the sub-keys of settings.achievements, which the API stores as sent (unknown ones warn; error with --strict-keys)."
	settingsKeysUpdateHelpText = "Settings keys are left to the API, which rejects an unknown top-level key, or an unknown sub-key of policies/registration/email/auth, with a 422 (exit 2) that names it. " +
		"The CLI checks two things itself, each a warning (error with --strict-keys): the sub-keys of settings.achievements, which the API stores as sent, and settings.policies, which the API accepts on update and discards (use 'mio hubs policies' instead)."

	strictKeysHelpText = "Reject unknown keys with an error instead of a warning, where the API would store them as sent: --branding-json and --meta-json keys, and settings.achievements sub-keys. " +
		"It checks no other --settings-json key: the API rejects an unknown top-level key, or an unknown sub-key of policies/registration/email/auth, with a 422 (exit 2) either way. " +
		"Best-effort allowlist; accepted keys are listed in each *-json flag's help and docs/internal/api-surface.md."
	strictKeysUpdateHelpText = "Reject unknown keys with an error instead of a warning, where the API would store them as sent: --branding-json and --meta-json keys, and settings.achievements sub-keys. " +
		"On update it also turns the settings.policies warning into an error, with no request: the API accepts settings.policies on update and discards it (use 'mio hubs policies' instead). " +
		"It checks no other --settings-json key: the API rejects an unknown top-level key, or an unknown sub-key of registration/email/auth, with a 422 (exit 2) either way. " +
		"Best-effort allowlist; accepted keys are listed in each *-json flag's help and docs/internal/api-surface.md."
)

type blobKeyCheck struct {
	name   string
	top    map[string]bool
	nested map[string]map[string]bool
	stored string
}

var (
	brandingKeyCheck = blobKeyCheck{
		name: "branding",
		top:  brandingKeys,
		stored: "The API stores branding keys as sent (it validates only the values of keys ending in _url), so a misspelled key is saved and silently has no effect. " +
			"This allowlist is best-effort; the hub frontend is the authoritative render schema.",
	}
	settingsKeyCheck = blobKeyCheck{
		name:   "settings",
		nested: settingsVerbatimNestedKeys,
		stored: "The API stores the sub-keys of settings.achievements as sent and reads only settings.achievements.enabled, so a misspelled sub-key is saved and silently has no effect.",
	}
	metaKeyCheck = blobKeyCheck{
		name: "meta",
		top:  metaKeys,
		stored: "The API stores meta keys as sent, with no server-side check, so a misspelled key is saved and silently has no effect. " +
			"This allowlist is best-effort; the hub frontend is the authoritative render schema.",
	}
)

const strictKeyDropHint = "Fix the key, or drop --strict-keys to send it anyway."

type unknownBlobKey struct {
	path    string
	level   string
	allowed []string
}

func validateBlobKeys(warnW io.Writer, c blobKeyCheck, obj map[string]any, strict bool) error {
	if obj == nil {
		return nil
	}
	blobName := c.name

	var unknown []unknownBlobKey
	if c.top != nil {
		for k := range obj {
			if !c.top[k] {
				unknown = append(unknown, unknownBlobKey{
					path:    blobName + "." + k,
					level:   blobName,
					allowed: sortedKeySet(c.top),
				})
			}
		}
	}
	for section, sub := range c.nested {
		m, ok := obj[section].(map[string]any)
		if !ok {
			continue
		}
		for k := range m {
			if !sub[k] {
				unknown = append(unknown, unknownBlobKey{
					path:    blobName + "." + section + "." + k,
					level:   blobName + "." + section,
					allowed: sortedKeySet(sub),
				})
			}
		}
	}

	if len(unknown) == 0 {
		return nil
	}

	sort.Slice(unknown, func(i, j int) bool { return unknown[i].path < unknown[j].path })

	first := unknown[0]
	flag := "--" + blobName + "-json"
	var more string
	if len(unknown) > 1 {
		rest := make([]string, len(unknown)-1)
		for i, u := range unknown[1:] {
			rest[i] = u.path
		}
		more = fmt.Sprintf(" (%d more unrecognized: %s)", len(unknown)-1, strings.Join(rest, ", "))
	}
	detail := fmt.Sprintf("%s: unknown key %q; accepted keys at %q are: %s%s",
		flag, first.path, first.level, strings.Join(first.allowed, ", "), more)

	if strict {
		return errs.New(errs.ExitUsage, "%s. %s %s", detail, c.stored, strictKeyDropHint)
	}
	fmt.Fprintf(warnW, "Warning: %s. %s Pass --strict-keys to make this an error.\n", detail, c.stored)
	return nil
}

func sortedKeySet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const policiesUnwritableOnUpdateMsg = "settings.policies cannot be written by `hubs update` — the API accepts the key and discards it. " +
	"Use `mio hubs policies gate <hub_id> --enabled[=false]` for policies.enabled, or `mio hubs policies update` for the document text. " +
	"NOTE this differs from `hubs create`, which DOES accept settings.policies.enabled/show; there is no CLI door for policies.show on an existing hub. " +
	"Read the stored state back with `mio hubs policies get <hub_id>`."

func checkPoliciesOnUpdate(warnW io.Writer, settings map[string]any, unsetPaths []unsetPath, strict bool) error {
	touched := false
	if settings != nil {
		_, touched = settings["policies"]
	}
	for _, p := range unsetPaths {
		if p.blob == "settings" && len(p.segments) > 0 && p.segments[0] == "policies" {
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}
	if strict {
		return errs.New(errs.ExitUsage, "%s", policiesUnwritableOnUpdateMsg)
	}
	fmt.Fprintf(warnW, "warning: %s\n", policiesUnwritableOnUpdateMsg)
	return nil
}
