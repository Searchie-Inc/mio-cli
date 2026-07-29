package cmd

// hubs_scaffold_branding.go — scaffold-time branding overrides for
// `mio hubs scaffold` (MIO-2604, epic MIO-2572).
//
// THE BUG THIS CLOSES: scaffold shipped --favicon-url/--logo-url but nothing for
// the PALETTE, so every hub scaffolded from `community` went live with the
// template's indigo primary + green secondary no matter whose brand it was.
// Recoloring meant a SECOND command and a hand-authored blob:
//
//	mio hubs scaffold --template community --name Acme --slug acme
//	mio hubs update <id> --branding-json '{"primary":"#B91C1C", …}'
//
// — two commands and a JSON literal for what the epic calls the "one-command
// shareable hub" story.
//
// WHAT IS ADDED (ticket Option A, its stated preference — "friendlier for CLI
// use; matches the existing --favicon-url/--logo-url pattern"): one scalar flag
// per branding key. Option B (--branding-json) rides along because it fell out
// of the existing `hubs update --branding-json` path for ~10 lines, and an
// operator who already HAS a branding blob should not have to decompose it into
// seven flags.
//
// PRECEDENCE — three layers, lowest wins first (this is exactly the order
// `hubs update` already applies, verified in applyHubBlobs: --*-json deep-merge
// first, then the scalar --logo-url/--favicon-url overrides on top):
//
//	1. the hub template's `branding` block (the default the catalog ships);
//	2. --branding-json, deep-merged OVER it;
//	3. the scalar palette flags, written OVER that.
//
// The whole three-layer result is then handed to applyHubBlobs as the branding
// patch, which deep-merges it onto the hub's CURRENT branding (so a resume never
// clobbers sibling keys) and applies --logo-url/--favicon-url last. The key sets
// are disjoint, so those two land in the same place they always did.
//
// NO CLIENT-SIDE VALUE VALIDATION (deliberate; see registerScaffoldBrandingFlags).

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// Branding blob keys the cascade below reasons about by name, and the flags it
// keys off. Named constants because three call sites have to agree on them.
const (
	scaffoldPrimaryFlag     = "primary-color"
	scaffoldHeaderColorFlag = "header-color"

	brandingPrimaryKey     = "primary"
	brandingHeaderColorKey = "header_color"
)

// scaffoldBrandingFlag binds one scaffold palette/imagery flag to the branding
// blob KEY it writes.
//
// FLAG NAME vs BLOB KEY — they are deliberately NOT the same spelling:
//
//   - the flag names are the ticket's (--primary-color, --text-color, …), because
//     that is how an operator talks about a palette;
//   - the keys are the ones the TEMPLATE actually sets. The catalog's `community`
//     hubTemplate branding block (verified against the embedded catalog 0.12.0 and
//     internal/catalog/testdata/catalog-2.1.json) carries the FE Epic 2 SHORT-form
//     palette — primary/secondary/background/text — not the legacy long form
//     (primary_color/secondary_color/background_color, still on the MIO-2515
//     allowlist for the legacy render path in app/pages/service.py). A flag that
//     wrote the legacy spelling would land NEXT TO the template's value instead of
//     overriding it: the hub would keep its indigo primary and the flag would look
//     like it had done nothing.
//
// Each flag therefore writes the one key the template itself uses. Nothing is
// double-written into both spellings: the CLI is a conduit, and emitting a key
// the template never sets would be the CLI guessing at the hub frontend's render
// contract (which, per hubs_blob_keys.go, it explicitly cannot enumerate).
//
// Every key here is on the MIO-2515 `brandingKeys` allowlist by construction —
// TestScaffoldBrandingFlags_KeysAreOnTheAllowlist pins that, so a future flag
// cannot be added that the strict-key check would then reject at apply time.
type scaffoldBrandingFlag struct {
	flag string // CLI flag name (no leading --)
	key  string // branding blob key it overrides
	help string
}

// scaffoldBrandingFlags is the palette surface, in the order --help lists it and
// the order the resume command reconstructs it.
var scaffoldBrandingFlags = []scaffoldBrandingFlag{
	{scaffoldPrimaryFlag, brandingPrimaryKey,
		"Override the template's branding.primary (the hub's primary brand color). Unless a header color is given (--header-color, or a header_color key in --branding-json) this ALSO fills branding.header_color, so the header matches the brand."},
	{"secondary-color", "secondary",
		"Override the template's branding.secondary (the accent color)."},
	{"text-color", "text",
		"Override the template's branding.text (body text color)."},
	{"background-color", "background",
		"Override the template's branding.background (page background color)."},
	{scaffoldHeaderColorFlag, brandingHeaderColorKey,
		"Override the template's branding.header_color (header chrome). Setting it also switches OFF the --primary-color cascade."},
	{"header-accent", "header_accent",
		"Override the template's branding.header_accent (the header's accent color)."},
	{"social-image-url", "social_image_url",
		"Override the template's branding.social_image_url (the Open Graph / social share image)."},
}

// scaffoldBranding is the scaffold's branding OVERRIDE LAYER: everything the
// OPERATOR asked for on top of the hub template's own branding block. The zero
// value is a valid "no overrides" layer (every method is nil-map safe), so the
// unit-style step tests that hand-build a scaffoldContext keep working untouched.
type scaffoldBranding struct {
	// jsonBlob is the parsed --branding-json object (nil = flag not given). Named
	// jsonBlob, not json, so it never reads as the encoding/json package this
	// file also uses.
	jsonBlob map[string]any
	// scalars is branding key → value for the palette flags that were passed,
	// PLUS any key the cascade filled in.
	scalars map[string]string
	// cascaded names the scalars that came from a CASCADE rather than from their
	// own flag (today: header_color from --primary-color). It exists so the
	// human-facing lines can say WHY a key the operator never typed is being set,
	// and so the resume command re-derives the cascade instead of hard-coding its
	// result.
	cascaded map[string]bool
}

// resolveScaffoldBranding reads the branding override flags off cmd and applies
// the cascade. It fires NO HTTP and is called BEFORE auth/team resolution, so a
// malformed --branding-json or an unknown branding key exits ExitUsage without
// touching the network — the same pre-auth discipline `hubs update` follows.
func resolveScaffoldBranding(cmd *cobra.Command) (scaffoldBranding, error) {
	b := scaffoldBranding{scalars: map[string]string{}, cascaded: map[string]bool{}}

	// --branding-json (ticket Option B). parseJSONObjectFlag is the SAME parser
	// `hubs update --branding-json` uses (inline JSON or @file, ExitUsage on
	// malformed input or a non-object), and validateBlobKeys is the SAME MIO-2515
	// allowlist — this file adds no second validator.
	obj, err := parseJSONObjectFlag(cmd, "branding-json")
	if err != nil {
		return scaffoldBranding{}, err
	}
	if obj != nil {
		// STRICT (an unknown key errors, it does not warn), because the whole
		// scaffold runs in strict key mode — stepBlobs passes Strict:true so a
		// malformed TEMPLATE key is caught rather than silently dropped, and an
		// operator's typo deserves the same treatment. Checking it HERE, rather
		// than leaving it to applyHubBlobs, means the check also fires under
		// --dry-run (which never reaches applyHubBlobs at all) and before any
		// request. io.Discard for warnW: strict mode returns the error instead of
		// writing a warning, so nothing is ever written there.
		if verr := validateBlobKeys(io.Discard, "branding", obj, brandingKeys, nil, true); verr != nil {
			return scaffoldBranding{}, scaffoldStrictKeyErr(verr, scaffoldFlagStrictKeyHint)
		}
		b.jsonBlob = obj
	}

	for _, f := range scaffoldBrandingFlags {
		if v := changedString(cmd, f.flag); v != nil {
			b.scalars[f.key] = *v
		}
	}

	// THE CASCADE (required by MIO-2604): --primary-color fills header_color when
	// no header color was given.
	//
	// "Given" means BY THE OPERATOR — --header-color, or a header_color key in
	// --branding-json. The TEMPLATE's own header_color deliberately does NOT count,
	// and that is the whole point: the catalog's `community` template sets
	// header_color to the SAME value as primary (#4F46E5 in catalog 0.12.0), so a
	// cascade that yielded to it would never fire for the only shipped template and
	// `--primary-color '#B91C1C'` would produce a red-branded hub with an indigo
	// header — precisely the mismatch this ticket reports. A template value is a
	// DEFAULT; the override layer exists to beat defaults, exactly as
	// --favicon-url/--logo-url already beat the template's favicon_url/logo_url.
	//
	// Escape hatch: pass --header-color explicitly (even with the template's own
	// value) to decouple the two.
	if primary, ok := b.scalars[brandingPrimaryKey]; ok && !b.headerColorGiven() {
		b.scalars[brandingHeaderColorKey] = primary
		b.cascaded[brandingHeaderColorKey] = true
	}

	return b, nil
}

// The two replacements for strictKeyDropHint (hubs_blob_keys.go) on the scaffold
// path. The shared text tells the operator to "drop --strict-keys" — sound advice
// on `hubs create`/`hubs update`, a dead end here: `hubs scaffold` has no such
// flag and checks blob keys strictly and unconditionally, because a key that
// silently does nothing is exactly how you end up with an unbranded hub and no
// error.
//
// There are TWO of them because a strict rejection on a scaffold has two
// possible ORIGINS, and each needs different advice. Worse, the shared message
// opens by naming a flag (`--<blob>-json`, built by validateBlobKeys) that is
// right for the operator's own --branding-json and simply wrong for a template
// key — `hubs scaffold` has no --settings-json or --meta-json at all — so the
// template hint has to say where the key really came from.
const (
	// scaffoldFlagStrictKeyHint: the key came from the operator's own
	// --branding-json, so the named flag is real and the fix is theirs.
	scaffoldFlagStrictKeyHint = "These blobs are stored verbatim by the API with no server-side validation, so a misspelled key silently has no effect — and `hubs scaffold` always checks them strictly (it has no --strict-keys to drop). Fix the key; to send one this best-effort allowlist does not know, scaffold first and then apply it with `mio hubs update <hub-id> --branding-json` (the hub frontend is the authoritative render schema)."

	// scaffoldTemplateStrictKeyHint: the key came from the HUB TEMPLATE, so the
	// operator passed no such flag at all and cannot fix it by editing one.
	scaffoldTemplateStrictKeyHint = "These blobs are stored verbatim by the API with no server-side validation, so a misspelled key silently has no effect — which is why `hubs scaffold` checks them strictly and has no --strict-keys to drop. This key came from the HUB TEMPLATE, not from a flag you passed (the flag named above is the equivalent `hubs create`/`hubs update` one): fix the template in the page-builder catalog, or scaffold from a corrected copy with --catalog <file>. The hub frontend is the authoritative render schema."
)

// scaffoldStrictKeyErr re-points a strict blob-key rejection at guidance that
// applies on the scaffold, using the hint for the offending key's ORIGIN.
//
// Two safety properties matter more than the wording:
//
//   - it is a NO-OP for anything that is not a strict blob-key rejection. The
//     blobs step's error channel also carries the PATCH's own failures, and
//     rewriting those would be a real bug — so a message that does not contain
//     the shared hint is returned completely untouched, wrapping and all;
//   - it PRESERVES the original exit code rather than asserting ExitUsage, so a
//     5xx can never be relabelled as the operator's mistake even if the match
//     logic is ever loosened.
//
// The swap is anchored on the shared const, so if that wording moves this stops
// matching (and the message stands as-is) rather than silently emitting advice
// that no longer fits.
func scaffoldStrictKeyErr(err error, hint string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, strictKeyDropHint) {
		return err // not a strict blob-key rejection — leave it exactly as it is
	}
	return errs.New(errs.CodeOf(err), "%s", strings.Replace(msg, strictKeyDropHint, hint, 1))
}

// headerColorGiven reports whether the operator expressed a header color — via
// --header-color (already in scalars by the time the cascade asks) or a
// header_color key in --branding-json.
func (b scaffoldBranding) headerColorGiven() bool {
	if _, ok := b.scalars[brandingHeaderColorKey]; ok {
		return true
	}
	_, ok := b.jsonBlob[brandingHeaderColorKey] // indexing a nil map is fine
	return ok
}

// empty reports whether the operator asked for no branding override at all
// (including `--branding-json '{}'`, which is a well-formed no-op).
func (b scaffoldBranding) empty() bool {
	return len(b.jsonBlob) == 0 && len(b.scalars) == 0
}

// applyTo layers the override onto the hub template's branding block and returns
// the branding patch stepBlobs hands to applyHubBlobs.
//
// It MERGES over the template rather than replacing it: a template branding key
// the operator did not name (logo_url, header_accent, …) must survive, which is
// what makes `--primary-color` a one-key edit instead of a wholesale palette
// rewrite. deepMergeMap returns a fresh top-level map, so the preflight-resolved
// template — shared state — is never mutated.
//
// A no-op override returns the template block UNCHANGED, nil included: a nil
// branding means "not given" to applyHubBlobs, and turning it into an empty
// object would start PATCHing a branding key onto templates that have none.
func (b scaffoldBranding) applyTo(tmplBranding map[string]any) map[string]any {
	if b.empty() {
		return tmplBranding
	}
	out := deepMergeMap(tmplBranding, b.jsonBlob)
	for k, v := range b.scalars {
		out[k] = v // scalars WIN over --branding-json and over the template
	}
	return out
}

// resolved returns the override layer as one map — the --branding-json keys with
// the scalars written on top. It is what the machine-readable result reports and
// what the human-facing lines render, so "what did the CLI actually override"
// has ONE answer.
func (b scaffoldBranding) resolved() map[string]any {
	out := make(map[string]any, len(b.jsonBlob)+len(b.scalars))
	for k, v := range b.jsonBlob {
		out[k] = v
	}
	for k, v := range b.scalars {
		out[k] = v
	}
	return out
}

// describe renders the override layer as a sorted, comma-separated
// "key=value" list, annotating any key the cascade filled in. Sorted so the
// dry-run plan and the summary are deterministic (Go map iteration is not).
func (b scaffoldBranding) describe() string {
	res := b.resolved()
	keys := make([]string, 0, len(res))
	for k := range res {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		part := k + "=" + formatBrandingValue(res[k])
		if b.cascaded[k] {
			part += " (cascaded from --" + scaffoldPrimaryFlag + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// planDetail is the suffix stepBlobs appends to its --dry-run plan entry, so a
// dry run SHOWS the palette it would apply instead of only naming the step
// (MIO-2604 acceptance). Empty when there is nothing to report, which keeps the
// plan of an override-free run byte-identical to what it always was.
func (b scaffoldBranding) planDetail() string {
	if b.empty() {
		return ""
	}
	return " [branding overrides: " + b.describe() + "]"
}

// flagArgs reconstructs the branding flags the OPERATOR passed, for the resume
// command printScaffoldRecovery prints after a mid-pipeline failure — following
// it verbatim must not silently revert the hub to the template's palette.
//
// Cascaded keys are omitted on purpose: --primary-color is echoed, and a resume
// re-derives header_color from it, so the resume command stays the command the
// operator actually ran.
func (b scaffoldBranding) flagArgs() []string {
	var parts []string
	if len(b.jsonBlob) > 0 {
		// Re-marshalled rather than echoed verbatim: json.Marshal sorts object
		// keys, so the reconstructed command is deterministic, and an @file
		// argument becomes a self-contained literal.
		if enc, err := json.Marshal(b.jsonBlob); err == nil {
			parts = append(parts, fmt.Sprintf("--branding-json %q", string(enc)))
		}
	}
	for _, f := range scaffoldBrandingFlags { // table order — stable
		v, ok := b.scalars[f.key]
		if !ok || b.cascaded[f.key] {
			continue
		}
		parts = append(parts, fmt.Sprintf("--%s %q", f.flag, v))
	}
	return parts
}

// formatBrandingValue renders one override value for the human-facing plan and
// summary lines. Every palette value is a string and prints bare; a structured
// value that can only arrive via --branding-json (e.g. the `labels` object)
// prints as compact JSON rather than Go's map[…] syntax.
func formatBrandingValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if enc, err := json.Marshal(v); err == nil {
		return string(enc)
	}
	return fmt.Sprintf("%v", v)
}

// registerScaffoldBrandingFlags registers the branding override surface on the
// scaffold command.
//
// NO CLIENT-SIDE VALUE VALIDATION — no hex check, no CSS-color check, no URL
// scheme check. The house rule is that the CLI is a faithful CONDUIT, not a
// validation layer: if the API accepts a value, so does the CLI. Concretely:
//
//   - the backend stores `branding` as opaque JSONB (app/hubs/models.py types it
//     `dict[str, Any] | None`; HubCreate/UpdateAttributes accept arbitrary keys),
//     so a color value has NO server-side format contract to mirror;
//   - the authoritative reader is the hub frontend, and its contract is NOT ours
//     to enforce. (Correction, MIO-2576: this comment used to claim the frontend
//     "accepts far more than `#rrggbb` — named CSS colors, rgb()/hsl(), gradients".
//     It does not: `src/lib/hub-shape/branding.ts` tests every color key against
//     /^#[0-9a-fA-F]{6}$/ and silently falls back otherwise. The conclusion still
//     holds, for the stronger reason — that regex lives in another repo and applies
//     only to the six color keys, so mirroring it here would reject values the API
//     accepts and pin this CLI to a render contract it does not own. The docs say
//     "pass 6-digit hex"; the code does not enforce it.)
//   - the CLI already ships ZERO validation on the sibling --logo-url/
//     --favicon-url overrides (there is no URL-scheme check anywhere in cmd/), so
//     validating colors while waving URLs through would be incoherent.
//
// What IS validated is the CLI's own interface, where a mistake is invisible
// rather than rejected: the MIO-2515 KEY allowlist. Every flag here writes an
// allowlisted key by construction, and --branding-json is key-checked in strict
// mode by resolveScaffoldBranding — because an unrecognized branding KEY is
// stored verbatim and silently does nothing, which is not a conduit question but
// a "this flag lied to you" question.
func registerScaffoldBrandingFlags(cmd *cobra.Command) {
	for _, f := range scaffoldBrandingFlags {
		cmd.Flags().String(f.flag, "", f.help)
	}
	// --branding-json (ticket Option B) rides alongside the scalars for the
	// operator who already has a blob. Precedence is explicit in the help text
	// because a silent one would be a trap.
	cmd.Flags().String("branding-json", "",
		"Branding keys to merge OVER the template's branding block, as a JSON object (inline or @file). The scalar branding flags WIN over the same key here. Accepted keys: "+
			brandingKeysHelp+". Unknown keys are an ERROR (scaffold runs in strict key mode).")
}
