package cmd

// hubs_blob_keys.go — best-effort client-side key validation for the hub's
// JSONB presentation blobs authored via `mio hubs create` / `mio hubs update`
// --branding-json / --settings-json / --meta-json (MIO-2515, MIO-4171).
//
// WHY A CLIENT-SIDE ALLOWLIST AT ALL — and why only where it still earns it:
// the CLI is a conduit, not a validation layer, so it checks keys only where the
// API cannot report a typo because it stores the key and silently does nothing
// with it. That is true, today, of:
//   - branding: validate_branding (mio-backend app/hubs/validation.py) checks
//     the VALUES of keys ending in _url and stores every other key as sent;
//   - meta: no server-side validator at all (app/hubs/schemas.py);
//   - settings.achievements sub-keys: see settingsVerbatimNestedKeys.
//
// It is NOT true of the rest of settings. Since MIO-3334 (mio-backend #757) the
// API enforces its own settings allowlist — an unknown top-level key, or an
// unknown sub-key of policies/registration/email/auth, is a 422 naming the key
// and its JSON pointer. The CLI used to keep a hand-copied mirror of that list;
// it fell behind the server in both directions (warning "stored verbatim" on
// keys the API rejects, and warning on — or, under --strict-keys, blocking —
// keys the API accepts), so it is gone (MIO-4171). Those keys are the API's to
// judge, with or without --strict-keys.
//
// Where the CLI does check, it WARNS by default (naming the offending key + the
// accepted set, on stderr so it never corrupts --output json/yaml) and ERRORS
// only behind --strict-keys, because the authoritative RENDER contract is the
// hub frontend (mio-hub), whose accepted keys the CLI cannot enumerate — a hard
// reject of every unlisted key would false-positive on legitimate FE keys. Each
// blob's message says what the API does with THAT blob's keys (blobKeyCheck).

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// brandingKeys is the accepted TOP-LEVEL key set for --branding-json.
// Provenance: app/seeders/hub_seeder.py _DEMO_HUB_BRANDING (declared the hub
// branding "single source of truth", MIO-351) — the FE Epic 2 short-form
// palette (primary/secondary/background/text) plus the legacy long-form palette
// keys (primary_color/secondary_color/background_color) still read by the legacy
// page-render path in app/pages/service.py and app/email/workers.py, and the
// --logo-url flag target logo_url.
var brandingKeys = map[string]bool{
	// Logo / imagery.
	"logo_url":         true,
	"favicon_url":      true,
	"social_image_url": true,
	// Canonical login/register-panel logo key (MIO-3354); its deprecated
	// alias custom_login_logo_url below stays accepted for back-compat (the
	// backend dual-writes the alias during the MIO-3354 transition).
	"auth_logo_url":         true,
	"custom_login_logo_url": true,
	"custom_font_url":       true,
	// Core palette (FE Epic 2 short-form, canonical going forward).
	"primary":    true,
	"secondary":  true,
	"background": true,
	"text":       true,
	// Legacy long-form palette (still read by app/pages/service.py; retired once
	// FE Epic 2 ships its own branding reader).
	"primary_color":    true,
	"secondary_color":  true,
	"background_color": true,
	// Header chrome.
	"header_color":  true,
	"header_accent": true,
	// Appearance toggles.
	"dark_mode": true,
	"gradient":  true,
	// Typography.
	"font_heading":      true,
	"font_body":         true,
	"heading_font_size": true,
	"body_font_size":    true,
	// UI labels (MIO-77 pattern).
	"labels": true,
}

// metaKeys is the accepted TOP-LEVEL key set for --meta-json (feature guards).
// Provenance: app/seeders/hub_seeder.py _DEMO_HUB_META.
var metaKeys = map[string]bool{
	"memberDirectory": true,
	"discussions":     true,
	"directMessages":  true,
	"moderation":      true,
}

// settingsVerbatimNestedKeys is the ONLY settings check the CLI still makes: the
// sub-keys of the one settings section that the API stores as sent while
// something reads it by exact key.
//
//   - achievements{enabled} — app/achievements/feature_flag.py
//     achievements_enabled() reads exactly settings.achievements.enabled, and
//     opts a hub out only on the literal false (default-on, D-004). The API's
//     own nested allowlist (_SETTINGS_NESTED_KEYS, app/hubs/validation.py)
//     deliberately stops at policies/registration/email/auth and leaves
//     achievements opaque past the top level, so `{"achievements":{"enabld":
//     false}}` is saved and leaves achievements ON — verified against the live
//     API in the MIO-4171 PR.
//
// Do NOT add a section the API validates itself (top-level keys, policies,
// registration, email, auth): that is the stale-mirror bug MIO-4171 removed.
var settingsVerbatimNestedKeys = map[string]map[string]bool{
	"achievements": {"enabled": true},
}

// brandingKeysHelp / metaKeysHelp are the accepted key sets rendered as sorted,
// comma-separated strings for --help text (so `mio hubs create --help` surfaces
// the best-effort schema — MIO-2515 acceptance clause 1). Derived from the maps
// above; Go initializes these after the maps they depend on.
var (
	brandingKeysHelp = strings.Join(sortedKeySet(brandingKeys), ", ")
	metaKeysHelp     = strings.Join(sortedKeySet(metaKeys), ", ")
)

// settingsKeysHelpText is the --settings-json help clause on create and update:
// who checks which settings keys, now that the API has an allowlist of its own.
const settingsKeysHelpText = "Settings keys are left to the API, which rejects an unknown top-level key, or an unknown sub-key of policies/registration/email/auth, with a 422 (exit 2) that names it. " +
	"The CLI checks only the sub-keys of settings.achievements, which the API stores as sent (unknown ones warn; error with --strict-keys)."

// blobKeyCheck is one presentation blob's best-effort key check: the keys the
// CLI can vouch for, and — the reason the check exists at all — what the API
// does with a key outside them. Every message about a blob's keys carries its
// own `stored` sentence, so no blob inherits a claim that is only true of
// another (MIO-4171: one shared "stored verbatim" sentence outlived its truth
// for settings by a month).
type blobKeyCheck struct {
	// name is the attribute name, and --<name>-json the flag.
	name string
	// top is the accepted top-level key set; nil means the CLI does not check
	// this blob's top level (settings: the API does).
	top map[string]bool
	// nested deep-validates the sub-keys of the sections it names, one level
	// down, when the section is present as an object.
	nested map[string]map[string]bool
	// stored says what the API does with an unrecognized key here.
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

// strictKeyDropHint is the tail of the strict-mode rejection message: what to do
// about an unrecognized key on a command that HAS --strict-keys (hubs
// create/update). It follows the blob's own `stored` sentence.
//
// It is a named const, not an inline literal, because a caller with no
// --strict-keys flag to drop must be able to swap it for guidance that actually
// applies — `hubs scaffold` validates branding keys strictly and
// unconditionally, so telling its user to drop a flag that does not exist there
// would be a dead end (see scaffoldStrictKeyErr, MIO-2604). Referencing the same
// const on both ends keeps the swap from silently no-op'ing if this wording ever
// changes.
const strictKeyDropHint = "Fix the key, or drop --strict-keys to send it anyway."

// unknownBlobKey records one key that is not on the allowlist, with the accepted
// key set at the same level so the error/warning can suggest the right spelling.
type unknownBlobKey struct {
	path    string   // fully-qualified, e.g. "settings.registraton"
	level   string   // where it lives, e.g. "settings" or "settings.registration"
	allowed []string // sorted accepted keys at that level
}

// validateBlobKeys checks the user-supplied keys of one presentation blob
// against its blobKeyCheck: c.top (when non-nil) is the accepted top-level key
// set, and c.nested deep-validates the sub-keys of the sections it names.
//
// It follows validateNavigationBlob's shape: collect the violations, then in
// strict mode return errs.ExitUsage naming the first offender (+ the accepted
// set, + what the API does with it, + a hint that dropping --strict-keys sends
// it), else write a "Warning: …" line to warnW (the caller passes
// cmd.ErrOrStderr()) so it never corrupts --output json/yaml on stdout. warnW
// rather than a *cobra.Command keeps this callable from the cobra-free
// applyHubBlobs builder (MIO-2543). Only the KEYS the caller passed are
// inspected — on update this must be the incoming object, never the
// retrieved/merged blob, so pre-existing keys on older hubs are not flagged.
func validateBlobKeys(warnW io.Writer, c blobKeyCheck, obj map[string]any, strict bool) error {
	if obj == nil {
		return nil
	}
	blobName := c.name

	var unknown []unknownBlobKey
	// Unknown top-level keys — only for a blob whose top level the CLI checks.
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
	// Unknown sub-keys, but only for the sections we deep-validate and only when
	// the section is present as an object.
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

	// Deterministic order so the message (and tests) do not depend on Go's
	// randomized map iteration.
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

// sortedKeySet returns the keys of a set map in sorted order, for stable
// suggestion messages.
func sortedKeySet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// policiesUnwritableOnUpdateMsg explains the create/update asymmetry that makes
// `settings.policies` a silent no-op on update.
//
// The backend accepts policies.enabled / policies.show on hub CREATE and pops
// the key outright on UPDATE (app/hubs/service.py, `incoming.pop("policies",
// None)  # client can never write policies here`). The CLI cannot re-route it:
// sending it to a different endpoint would be the CLI second-guessing the API,
// and only `enabled` has another door anyway — `show` has none, so a re-route
// would trade one asymmetry for another. What the CLI CAN do is stop implying a
// write that never happens (MIO-2811).
const policiesUnwritableOnUpdateMsg = "settings.policies cannot be written by `hubs update` — the API accepts the key and discards it. " +
	"Use `mio hubs policies gate <hub_id> --enabled[=false]` for policies.enabled, or `mio hubs policies update` for the document text. " +
	"NOTE this differs from `hubs create`, which DOES accept settings.policies.enabled/show; there is no CLI door for policies.show on an existing hub. " +
	"Read the stored state back with `mio hubs policies get <hub_id>`."

// checkPoliciesOnUpdate warns (or, with --strict-keys, errors) when an update's
// --settings-json carries `policies`.
//
// `policies` is a LEGITIMATE settings key — the API's own allowlist accepts it,
// it is real on create, and no key check flags it. This is a different
// failure: a known key on the wrong verb, which the API cannot report because
// it accepts the key and then discards it. Without this, `hubs update
// --settings-json '{"policies":{"enabled":true}}'` prints success, exits 0 and
// changes nothing, and the only way to discover that is to read backend source.
func checkPoliciesOnUpdate(warnW io.Writer, settings map[string]any, unsetPaths []unsetPath, strict bool) error {
	touched := false
	if settings != nil {
		_, touched = settings["policies"]
	}
	// --unset is the same silent no-op by a different door: the backend restores
	// the stored policies block wholesale on update (`merged["policies"] =
	// current_settings["policies"]`), so deleting a key under it changes nothing.
	// It is documented as "the only real delete", which makes the omission here
	// more misleading than the flag's, not less.
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
