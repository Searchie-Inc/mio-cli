package cmd

// hubs_blob_keys.go — best-effort client-side key validation for the hub's
// opaque JSONB presentation blobs authored via `mio hubs create` /
// `mio hubs update` --branding-json / --settings-json / --meta-json (MIO-2515).
//
// WHY A CLIENT-SIDE ALLOWLIST (and not a hard server-enforced schema):
// The backend stores branding/settings/meta as opaque JSONB — app/hubs/models.py
// types them `dict[str, Any] | None` and app/hubs/schemas.py HubCreate/
// UpdateAttributes accept arbitrary keys — so there is NO machine-readable
// schema to publish or enforce, and a bogus key round-trips as success. The
// authoritative RENDER contract for these blobs is the hub frontend (mio-hub, a
// separate repo), whose accepted keys the CLI cannot enumerate, and the blob
// shape is explicitly still evolving (legacy branding keys are being retired
// once FE Epic 2 ships). A HARD reject of every unlisted key would therefore
// false-positive on legitimate FE keys. So the CLI WARNS by default (naming the
// offending key + the accepted set, on stderr so it never corrupts --output
// json/yaml) and ERRORS only behind --strict-keys. The allowlist is a curated
// set of keys we can cite to a real backend read or the demo-hub seeder — see
// each map's provenance comment. Nothing here is invented.

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
	"logo_url":              true,
	"favicon_url":           true,
	"social_image_url":      true,
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

// settingsKeys is the accepted TOP-LEVEL key set for --settings-json.
// Provenance: app/seeders/hub_seeder.py _DEMO_HUB_SETTINGS (customCss/menu/
// header/footer/background/appearance/policies) plus the backend-read sections
// registration (app/hubs/registration.py, MIO-761), email (app/hubs/service.py
// settings.email.{from_name,reply_to}, MIO-1229) and auth (app/hubs/service.py
// settings.auth.allowed_redirect_origins, MIO-616).
var settingsKeys = map[string]bool{
	"customCss":    true,
	"menu":         true,
	"header":       true,
	"footer":       true,
	"background":   true,
	"appearance":   true,
	"policies":     true,
	"registration": true,
	"email":        true,
	"auth":         true,
}

// metaKeys is the accepted TOP-LEVEL key set for --meta-json (feature guards).
// Provenance: app/seeders/hub_seeder.py _DEMO_HUB_META.
var metaKeys = map[string]bool{
	"memberDirectory": true,
	"discussions":     true,
	"directMessages":  true,
	"moderation":      true,
}

// settingsNestedKeys deep-validates ONLY the settings sections whose sub-key
// schema is backend-COMPLETE and stable — safe to check one level deeper.
// Provenance:
//   - policies{enabled,show,tos,privacy_policy} — app/hubs/service.py (MIO-2020);
//     tos/privacy_policy documents are stripped on write and managed via
//     `hubs policies update`, but remain valid keys.
//   - registration{enabled} — app/hubs/registration.py (MIO-761).
//   - email{from_name,reply_to} — app/hubs/service.py (MIO-1229), mirrors
//     `hubs email-settings update`.
//   - auth{allowed_redirect_origins} — app/hubs/service.py (MIO-616); stripped on
//     write and managed via `hubs redirect-origins set`, but a valid key.
//
// Branding, meta, and the other settings sections are FE-owned / still evolving,
// so they are validated only at the top level (no nested map here).
var settingsNestedKeys = map[string]map[string]bool{
	"policies":     {"enabled": true, "show": true, "tos": true, "privacy_policy": true},
	"registration": {"enabled": true},
	"email":        {"from_name": true, "reply_to": true},
	"auth":         {"allowed_redirect_origins": true},
}

// *KeysHelp are the accepted top-level key sets rendered as sorted,
// comma-separated strings for --help text (so `mio hubs create --help` surfaces
// the best-effort schema — MIO-2515 acceptance clause 1). Derived from the maps
// above; Go initializes these after the maps they depend on.
var (
	brandingKeysHelp = strings.Join(sortedKeySet(brandingKeys), ", ")
	settingsKeysHelp = strings.Join(sortedKeySet(settingsKeys), ", ")
	metaKeysHelp     = strings.Join(sortedKeySet(metaKeys), ", ")
)

// strictKeyDropHint is the tail of the strict-mode rejection message: what to do
// about an unrecognized key on a command that HAS --strict-keys (hubs
// create/update).
//
// It is a named const, not an inline literal, because a caller with no
// --strict-keys flag to drop must be able to swap it for guidance that actually
// applies — `hubs scaffold` validates branding keys strictly and
// unconditionally, so telling its user to drop a flag that does not exist there
// would be a dead end (see scaffoldStrictKeyErr, MIO-2604). Referencing the same
// const on both ends keeps the swap from silently no-op'ing if this wording ever
// changes.
const strictKeyDropHint = "These blobs are stored verbatim by the API with no server-side validation, so a misspelled key silently has no effect. Fix the key, or drop --strict-keys to send unrecognized keys anyway (the hub frontend is the authoritative render schema)."

// unknownBlobKey records one key that is not on the allowlist, with the accepted
// key set at the same level so the error/warning can suggest the right spelling.
type unknownBlobKey struct {
	path    string   // fully-qualified, e.g. "settings.registraton"
	level   string   // where it lives, e.g. "settings" or "settings.registration"
	allowed []string // sorted accepted keys at that level
}

// validateBlobKeys checks the user-supplied keys of one presentation blob
// against a curated allowlist. blobName is the top-level attribute name
// ("branding"/"settings"/"meta"); allow is the accepted top-level key set;
// nested (may be nil) deep-validates the sub-keys of the sections it names.
//
// It follows validateNavigationBlob's shape: collect the violations, then in
// strict mode return errs.ExitUsage naming the first offender (+ the accepted
// set, + a hint that dropping --strict-keys allows it), else write a
// "Warning: …" line to warnW (the caller passes cmd.ErrOrStderr()) so it never
// corrupts --output json/yaml on stdout. warnW rather than a *cobra.Command
// keeps this callable from the cobra-free applyHubBlobs builder (MIO-2543).
// Only the KEYS the caller passed are inspected — on update this must be the
// incoming object, never the retrieved/merged blob, so pre-existing keys on
// older hubs are not flagged.
func validateBlobKeys(warnW io.Writer, blobName string, obj map[string]any, allow map[string]bool, nested map[string]map[string]bool, strict bool) error {
	if obj == nil {
		return nil
	}

	var unknown []unknownBlobKey
	// Unknown top-level keys.
	for k := range obj {
		if !allow[k] {
			unknown = append(unknown, unknownBlobKey{
				path:    blobName + "." + k,
				level:   blobName,
				allowed: sortedKeySet(allow),
			})
		}
	}
	// Unknown sub-keys, but only for the stable sections we deep-validate and
	// only when the section is present as an object.
	for section, sub := range nested {
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
		return errs.New(errs.ExitUsage, "%s. %s", detail, strictKeyDropHint)
	}
	fmt.Fprintf(warnW,
		"Warning: %s. It is stored verbatim (a typo silently has no effect); pass --strict-keys to make this an error. This allowlist is best-effort — the hub frontend is the authoritative render schema.\n",
		detail)
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
// `policies` is a LEGITIMATE settings key — it is on the allowlist, it is real on
// create, and validateBlobKeys is right not to flag it. This is a different
// failure: a known key on the wrong verb. Without this, `hubs update
// --settings-json '{"policies":{"enabled":true}}'` prints success, exits 0 and
// changes nothing, and the only way to discover that is to read backend source.
func checkPoliciesOnUpdate(warnW io.Writer, settings map[string]any, strict bool) error {
	if settings == nil {
		return nil
	}
	if _, present := settings["policies"]; !present {
		return nil
	}
	if strict {
		return errs.New(errs.ExitUsage, "%s", policiesUnwritableOnUpdateMsg)
	}
	fmt.Fprintf(warnW, "warning: %s\n", policiesUnwritableOnUpdateMsg)
	return nil
}
