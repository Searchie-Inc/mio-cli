package cmd

// hubs_scaffold_resume.go — what a `mio hubs scaffold --hub` run may change on
// a hub that already exists (MIO-4166, MIO-2818).
//
// THE RULE (decided 2026-09-24, both tickets together): a --hub run FILLS GAPS.
// It adds what the hub is missing and leaves alone everything the hub already
// has — branding/palette keys, navigation buckets, settings (registration
// included), policy text, the policy gate and onboarding hub-config. That is
// the doctrine the rest of the pipeline already followed (pages are
// provenance-guarded and never overwritten; spaces, onboarding definitions and
// playlists skip if they exist); the blobs, policies and hub-config writes
// were the exceptions, and --hub is also the documented crash-recovery path,
// so a builder who customised a hub before a failure lost the customisation on
// recovery.
//
// Two things still win over the hub:
//
//   - a flag given on THIS invocation (the palette flags, --branding-json,
//     --logo-url, --favicon-url, --registration-enabled) — which is what keeps
//     the printed "Resume with:" command, which repeats them, working;
//   - --reapply-template, the explicit opt-in to the old template-wins apply.
//     It is destructive (confirmDestructive: a prompt, or --yes off a TTY) and
//     a usage error without --hub.
//
// Kept values are reported on stderr and in the --dry-run plan, so "the
// template says X but the hub still shows Y" is never a silent outcome.
//
// The whole-hub server op (MIO-2976) is create-only — hubOpSkipReason sends
// every --hub run client-side — so nothing here touches the op path.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/client"
	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

// ---- the --reapply-template flag ---------------------------------------------

// checkReapplyTemplate validates --reapply-template BEFORE auth, team or hub
// resolution, so both refusals fire no request at all: it is a usage error
// without an explicit --hub (a create has nothing to re-apply onto; a
// configured current_hub does not count, exactly as it does not turn a create
// into a resume), and — outside --dry-run, which writes nothing — it is a
// destructive operation that needs a confirmation or --yes.
func checkReapplyTemplate(cmd *cobra.Command, templateID string) (bool, error) {
	reapply, _ := cmd.Flags().GetBool("reapply-template")
	if !reapply {
		return false, nil
	}
	if flags.hub == "" {
		return false, errs.New(errs.ExitUsage,
			"--reapply-template needs --hub <id>: it overwrites an EXISTING hub's branding, navigation, settings, policy text and onboarding config with the template's values (and turns its policy gate on when the template enables it), and a create has nothing to overwrite")
	}
	if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
		return true, nil
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Overwrite hub %s's branding, navigation, settings (registration included), policy text and onboarding config with template %q, and turn its policy gate on if the template enables it?",
		flags.hub, templateID)); err != nil {
		return false, err
	}
	return true, nil
}

// ---- kept values ---------------------------------------------------------------

// keptValue is one value the template declares that the hub already has a
// DIFFERENT value for, and which this run therefore left alone.
type keptValue struct {
	path     string // dotted, e.g. "branding.primary"
	hub, tpl any    // tpl nil: the template has no value there at all
}

func (k keptValue) String() string {
	if k.tpl == nil {
		return fmt.Sprintf("%s=%s (template: none)", k.path, describeKeptValue(k.hub))
	}
	return fmt.Sprintf("%s=%s (template: %s)", k.path, describeKeptValue(k.hub), describeKeptValue(k.tpl))
}

// describeKeptValue renders one value for a kept-value line: strings quoted,
// arrays (navigation buckets) as an item count, anything else as compact JSON,
// truncated so one odd value cannot swamp the line.
func describeKeptValue(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case []any:
		return fmt.Sprintf("%d item(s)", len(x))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:57]) + "..."
	}
	return s
}

func joinKept(kept []keptValue) string {
	parts := make([]string, len(kept))
	for i, k := range kept {
		parts[i] = k.String()
	}
	return strings.Join(parts, "; ")
}

// sameJSON compares two decoded JSON values by their encoding, so a
// json.Number from the catalog and a float64 from the API compare equal and
// map key order is irrelevant (json.Marshal sorts keys).
func sameJSON(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ab, bb)
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cloneJSONValue deep-copies a decoded JSON value, so a fill patch never
// aliases the preflight-resolved template (shared state).
func cloneJSONValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return catalog.CloneNode(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneJSONValue(e)
		}
		return out
	}
	return v
}

// ---- blobs ---------------------------------------------------------------------

// fillMissing returns the part of tmpl the hub's current blob lacks: every key
// that is absent or null in cur (recursing into objects both sides have), and
// nothing else. A key the hub HAS keeps the hub's value; where that value
// differs from the template's it is appended to kept. nil when nothing is
// missing.
//
// A null counts as missing on purpose: it is what the API stores for "no
// value", and a key the frontend reads as unset is a gap, not a choice. (The
// explicit way to remove a key is `hubs update --unset`, which deletes it —
// and a deleted key is a gap too.)
//
// Arrays and scalars are leaves: the hub's array is the hub's, whatever the
// template's holds.
func fillMissing(prefix string, tmpl, cur map[string]any, kept *[]keptValue) map[string]any {
	var out map[string]any
	for _, k := range sortedMapKeys(tmpl) {
		tv, path := tmpl[k], prefix+"."+k
		cv, present := cur[k]
		if !present || cv == nil {
			if tv == nil {
				continue // a null default fills nothing
			}
			if out == nil {
				out = map[string]any{}
			}
			out[k] = cloneJSONValue(tv)
			continue
		}
		tm, tIsMap := tv.(map[string]any)
		cm, cIsMap := cv.(map[string]any)
		if tIsMap && cIsMap {
			if sub := fillMissing(path, tm, cm, kept); len(sub) > 0 {
				if out == nil {
					out = map[string]any{}
				}
				out[k] = sub
			}
			continue
		}
		if !sameJSON(tv, cv) {
			*kept = append(*kept, keptValue{path: path, hub: cv, tpl: tv})
		}
	}
	return out
}

// navBucketEmpty reports whether a navigation bucket holds nothing: absent,
// null, or an empty array.
func navBucketEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case []any:
		return len(x) == 0
	}
	return false
}

// fillNavigation fills navigation BUCKET BY BUCKET: a bucket the hub lacks (or
// holds empty) takes the template's; a bucket the hub has is kept whole, and a
// bucket the template does not declare at all (mobile, today) is never
// dropped. It returns the complete navigation to send — the hub's own buckets
// plus the filled ones, because the backend stores navigation as a
// full-column overwrite — or nil when no bucket needed filling, in which case
// no navigation is sent. Either way the hub's own buckets are never
// re-validated: stepBlobsFillGaps checks the template's hrefs only.
//
// Why whole buckets and not items: the items of one bucket are an ordered
// menu, and splicing the template's items into a menu someone arranged by
// hand produces a menu nobody designed. A bucket is the unit an operator edits
// as a whole.
func fillNavigation(tmplNav, curNav map[string]any, kept *[]keptValue) map[string]any {
	if tmplNav == nil {
		return nil // the template declares no navigation: never touch the hub's
	}
	filled := false
	out := make(map[string]any, len(curNav)+len(tmplNav))
	for k, v := range curNav {
		out[k] = v
	}
	for _, b := range sortedMapKeys(tmplNav) {
		tv, cv := tmplNav[b], curNav[b]
		switch {
		case navBucketEmpty(cv) && !navBucketEmpty(tv):
			out[b] = tv
			filled = true
		case !navBucketEmpty(cv) && !sameJSON(tv, cv):
			*kept = append(*kept, keptValue{path: "navigation." + b, hub: cv, tpl: tv})
		}
	}
	// Buckets only the hub has: kept, and reported, because the template-wins
	// apply (--reapply-template) replaces the whole blob and would drop them.
	for _, b := range sortedMapKeys(curNav) {
		if _, declared := tmplNav[b]; !declared && !navBucketEmpty(curNav[b]) {
			*kept = append(*kept, keptValue{path: "navigation." + b, hub: curNav[b]})
		}
	}
	if !filled {
		return nil
	}
	return out
}

// blobFill is the resolved fill for one --hub run's blobs step.
type blobFill struct {
	branding, settings, navigation map[string]any
	kept                           []keptValue
}

// planBlobFill computes the blobs fill against the hub's CURRENT attributes:
// the template's branding/settings keys the hub lacks (with this invocation's
// branding override layer on top — flags win over the hub), the navigation
// buckets it lacks, and the kept values. nav is the template navigation
// already interpolated, shape-checked and hub-scoped (prepareTemplateNav).
func (sc *scaffoldContext) planBlobFill(t *catalog.HubTemplate, nav map[string]any, cur map[string]any) blobFill {
	var kept []keptValue
	f := blobFill{
		branding:   sc.branding.applyTo(fillMissing("branding", t.Branding, attrMap(cur["branding"]), &kept)),
		settings:   fillMissing("settings", fillableSettings(t.Settings), attrMap(cur["settings"]), &kept),
		navigation: fillNavigation(nav, attrMap(cur["navigation"]), &kept),
	}
	for _, k := range kept {
		if !sc.overwrittenByInvocation(k.path) {
			f.kept = append(f.kept, k)
		}
	}
	return f
}

// empty reports whether the step has nothing to send at all.
func (f blobFill) empty(sc *scaffoldContext) bool {
	return f.branding == nil && f.settings == nil && f.navigation == nil &&
		sc.logoOverride == nil && sc.faviconOverride == nil && sc.registrationOverride == nil
}

// fillableSettings is the template's settings without `policies`. The backend
// pops settings.policies from every hub PATCH (MIO-497, HubService.update), so
// this step can neither fill it nor keep it: sending it is a request that
// changes nothing, and reporting it as "kept" would be false. The shipped
// templates all declare it, and nothing on the scaffold path ever stores its
// `show`, so without this every --hub run sent a settings PATCH and none was
// ever "nothing to fill". The gate is the policies step's — PATCH
// …/policies/gate is the only writer there is. The strict key check still sees
// the whole template (validateTemplateBlobKeysStrict).
func fillableSettings(s map[string]any) map[string]any {
	if _, ok := s["policies"]; !ok {
		return s
	}
	out := make(map[string]any, len(s))
	for k, v := range s {
		if k != "policies" {
			out[k] = v
		}
	}
	return out
}

// overwrittenByInvocation reports whether a flag given on THIS invocation
// writes path — a value it writes was not kept, whatever the hub held.
func (sc *scaffoldContext) overwrittenByInvocation(path string) bool {
	segs := strings.Split(path, ".")
	switch {
	case path == "branding.logo_url" && sc.logoOverride != nil,
		path == "branding.favicon_url" && sc.faviconOverride != nil,
		path == "settings.registration.enabled" && sc.registrationOverride != nil:
		return true
	case segs[0] == "branding":
		return pathSetIn(sc.branding.resolved(), segs[1:])
	}
	return false
}

// pathSetIn reports whether the override object m writes segs: the full path is
// present, or a non-object value sits at one of its prefixes (which replaces
// the whole subtree).
func pathSetIn(m map[string]any, segs []string) bool {
	for i, s := range segs {
		v, ok := m[s]
		if !ok {
			return false
		}
		if i == len(segs)-1 {
			return true
		}
		sub, isMap := v.(map[string]any)
		if !isMap {
			return true
		}
		m = sub
	}
	return false
}

// validateTemplateBlobKeysStrict runs the strict MIO-2515 key check over the
// WHOLE template branding (with the override layer) and settings. Fill mode
// sends only the keys the hub lacks, and applyHubBlobs checks only the keys it
// is sent — so without this a malformed template key would pass or fail
// depending on what the hub happens to hold.
func validateTemplateBlobKeysStrict(sc *scaffoldContext, t *catalog.HubTemplate) error {
	if err := validateBlobKeys(io.Discard, "branding", sc.branding.applyTo(t.Branding), brandingKeys, nil, true); err != nil {
		return scaffoldStrictKeyErr(err, scaffoldTemplateStrictKeyHint)
	}
	if err := validateBlobKeys(io.Discard, "settings", t.Settings, settingsKeys, settingsNestedKeys, true); err != nil {
		return scaffoldStrictKeyErr(err, scaffoldTemplateStrictKeyHint)
	}
	return nil
}

// stepBlobsFillGaps is stepBlobs for a --hub run: read the hub, fill what it
// lacks, keep what it has, and let this invocation's flags win over both.
func stepBlobsFillGaps(sc *scaffoldContext, t *catalog.HubTemplate) error {
	nav, err := prepareTemplateNav(sc, t)
	if err != nil {
		return err
	}
	if err := validateTemplateBlobKeysStrict(sc, t); err != nil {
		return err
	}
	// The hub-scoped href check (MIO-2270) covers the template's WHOLE
	// navigation, here, whatever the hub holds — and nothing else. The PATCH
	// also carries the hub's own kept buckets (navigation is stored whole),
	// and those are the hub's: the API accepted them, and it accepts
	// root-relative hrefs this check rejects (a "/content" link, a sibling
	// hub, a "/old-slug/…" link a slug rename left behind). Re-judging them
	// failed the run over an item it had just reported as kept.
	if err := validateNavigationHrefs(nav, sc.hubSlug); err != nil {
		return err
	}

	// The PLAN reports against the hub the resume GET read; the real run
	// re-reads it below and decides against that read.
	planned := sc.planBlobFill(t, nav, sc.resumeHub)
	detail := fmt.Sprintf("PATCH %s — fill gaps only: add the branding/settings keys and navigation buckets the hub lacks, keep what it has (flags on this command still win) (strict keys)%s",
		hubsPath(sc.teamID, sc.hubIDOrPlaceholder()), sc.branding.planDetail())
	if planned.empty(sc) {
		detail = fmt.Sprintf("nothing to fill — the hub already has every branding/settings key and navigation bucket the template declares; no PATCH%s", sc.branding.planDetail())
	}
	if len(planned.kept) > 0 {
		detail += "; keeps the hub's own " + joinKept(planned.kept)
	}

	return sc.step("blobs", detail, func() error {
		cur, rerr := sc.cl.Retrieve(sc.ctx, hubsPath(sc.teamID, sc.hubID))
		if rerr != nil {
			return rerr
		}
		fill := sc.planBlobFill(t, nav, cur.Attributes)
		if len(fill.kept) > 0 {
			sc.notef("blobs: kept %d value(s) this hub already has (a --hub run fills gaps only; --reapply-template overwrites them with the template's): %s",
				len(fill.kept), joinKept(fill.kept))
		}
		if fill.empty(sc) {
			sc.notef("blobs: nothing to fill — the hub already has every branding/settings key and navigation bucket the template declares; no PATCH sent.")
			return nil
		}
		_, perr := applyHubBlobs(sc.ctx, sc.cl, sc.teamID, sc.hubID, sc.hubSlug, blobPatches{
			Branding:     fill.branding,
			Settings:     fill.settings,
			Navigation:   fill.navigation,
			SlugKnown:    true,
			Favicon:      sc.faviconOverride,
			Logo:         sc.logoOverride,
			Registration: sc.registrationOverride,
			Strict:       true,
			// Merge onto the SAME read the fill was computed from: a second GET
			// could see a key the fill decided was missing.
			Current: cur,
			// The template's hrefs were checked above; the rest of the blob is
			// the hub's own menu, carried back as stored.
			NavHrefsChecked: true,
		}, io.Discard)
		if fill.navigation != nil {
			perr = explainKeptNavRejection(perr, sc.hubID, attrMap(cur.Attributes["navigation"]))
		}
		return scaffoldStrictKeyErr(perr, scaffoldTemplateStrictKeyHint)
	})
}

// explainKeptNavRejection adds the way out to a navigation_page_invalid 422 on
// a fill-gaps PATCH. To fill one navigation bucket the run must send the hub's
// other buckets back as they are (the API stores navigation whole), and the
// API refuses EVERY navigation write while a type=page item points at a page
// that is not one of this hub's active pages — typically a deleted one, since
// page deletion does not cascade to the menu. The item it names
// is then usually the hub's own, and re-running the printed "Resume with:"
// command would only hit it again. Dropping it here would change a menu the
// run reports as kept, so the run stops, and says where the item lives.
func explainKeptNavRejection(err error, hubID string, curNav map[string]any) error {
	if err == nil || !client.HasAPIErrorCode(err, "navigation_page_invalid") {
		return err
	}
	var kept []string
	for _, b := range sortedMapKeys(curNav) {
		if !navBucketEmpty(curNav[b]) {
			kept = append(kept, b)
		}
	}
	if len(kept) == 0 {
		return err
	}
	return errs.Wrap(errs.CodeOf(err), fmt.Errorf(
		"%w — this run sent the hub's own navigation bucket(s) [%s] back unchanged alongside the one(s) it filled (the API stores navigation whole), and the API refuses every navigation write while a page item points at a page that is not one of this hub's active pages (a deleted page, typically). If the item it names is in one of those buckets it is already on the hub's menu: remove it (`mio hubs navigation list %s`, then `mio hubs navigation remove %s <bucket> --index <n>`) and re-run",
		err, strings.Join(kept, ", "), hubID, hubID))
}

// ---- policies (MIO-2818) -------------------------------------------------------

// storedPolicies returns settings.policies from a hub's attributes, as the
// admin hub GET serves it: RAW, exactly as stored. That is the signal this
// file relies on for "is this policy configured?" — see planPolicyFill.
func storedPolicies(attrs map[string]any) map[string]any {
	settings, _ := attrs["settings"].(map[string]any)
	policies, _ := settings["policies"].(map[string]any)
	return policies
}

// policyFillDoc is the verdict for one template policy on a --hub run.
type policyFillDoc struct {
	pol        hubPolicy
	configured bool // the hub has text of its own for this policy
	hubChars   int  // its length, in characters
	same       bool // …and it is exactly the template's text
}

// write reports whether the policy is written: only when the template has text
// AND the hub has none. content:null is never sent on a --hub run.
func (d policyFillDoc) write() bool { return d.pol.Content != nil && !d.configured }

func (d policyFillDoc) note() string {
	pt := d.pol.PolicyType
	switch {
	case d.write():
		return fmt.Sprintf("policies: wrote the template's %s text — the hub had none of its own (it was serving the platform default).", pt)
	case d.same:
		return fmt.Sprintf("policies: %s already carries the template's text — nothing to write.", pt)
	case d.configured && d.pol.Content == nil:
		return fmt.Sprintf("policies: kept the hub's own %s text (%d characters) — the template declares none, and a --hub run never resets a policy to the platform default (--reapply-template does).", pt, d.hubChars)
	case d.configured:
		return fmt.Sprintf("policies: kept the hub's own %s text (%d characters) — the template's text was not applied (a --hub run fills gaps only; --reapply-template replaces it).", pt, d.hubChars)
	default:
		return fmt.Sprintf("policies: %s — the template declares no text and the hub has none of its own, so the platform default already applies; nothing to write.", pt)
	}
}

// policyFill is the resolved policies step for a --hub run.
type policyFill struct {
	docs []policyFillDoc
	// gateSet: the hub's settings.policies.enabled is a JSON boolean — the
	// owner (or an earlier run) set it — and gateValue is that boolean.
	gateSet, gateValue bool
}

// planPolicyFill decides each template policy against the hub's STORED
// settings.policies.
//
// THE SIGNAL (verified on mio-backend origin/main, app/hubs/service.py): a
// policy is UNCONFIGURED exactly when its stored `content` is absent or null.
// That is the predicate the backend itself uses — _project_policy_documents
// renders the platform default iff `doc.get("content") is None` — and
// update_policy stores None for a reset. The admin hub GET returns `settings`
// raw (_row_to_view → _safe_dict(hub.settings)), so the CLI reads the same
// field the backend branches on.
//
// NOT a signal: anything from `GET …/policies` (the admin policies read). It
// renders the default text into `content` for an unconfigured document and
// projects an absent version as "default-v1" even for hand-written text, so
// neither field can tell the two apart.
func planPolicyFill(res templatePolicies, stored map[string]any) policyFill {
	var out policyFill
	for _, pol := range res.policies {
		doc, _ := stored[pol.PolicyType].(map[string]any)
		text, configured := doc["content"].(string)
		d := policyFillDoc{pol: pol, configured: configured, hubChars: utf8.RuneCountInString(text)}
		d.same = configured && pol.Content != nil && *pol.Content == text
		out.docs = append(out.docs, d)
	}
	// Only a JSON boolean is a set gate — the backend reads it with `is True`,
	// so anything else is as good as unset, and filling it is filling a gap.
	if v, ok := stored["enabled"].(bool); ok {
		out.gateSet, out.gateValue = true, v
	}
	return out
}

// describe summarises the content decisions for the --dry-run plan.
func (f policyFill) describe() string {
	var write, keep, none []string
	for _, d := range f.docs {
		switch {
		case d.write():
			write = append(write, d.pol.PolicyType)
		case d.configured && !d.same:
			keep = append(keep, fmt.Sprintf("%s (%d characters)", d.pol.PolicyType, d.hubChars))
		default:
			none = append(none, d.pol.PolicyType)
		}
	}
	var parts []string
	if len(write) > 0 {
		parts = append(parts, "write the template's text for ["+strings.Join(write, ", ")+"] (the hub has none of its own)")
	}
	if len(keep) > 0 {
		parts = append(parts, "keep the hub's own text for ["+strings.Join(keep, ", ")+"]")
	}
	if len(none) > 0 {
		parts = append(parts, "nothing to write for ["+strings.Join(none, ", ")+"]")
	}
	return strings.Join(parts, "; ")
}

// ---- onboarding hub-config ------------------------------------------------------

// existingHubConfigs returns definition_id → attributes for every hub-config
// row the hub has. The admin list is unpaginated today (meta {}), but the walk
// follows a cursor if one ever appears, with the same stall guard as the other
// lookups, so a missed row can never read as "absent" and be overwritten.
func (sc *scaffoldContext) existingHubConfigs() (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	seen := map[string]bool{}
	query := url.Values{}
	const maxPages = 1000
	for page := 0; page < maxPages; page++ {
		col, err := sc.cl.List(sc.ctx, contactAttributesHubConfigPath(sc.teamID, sc.hubID, ""), query)
		if err != nil {
			return nil, err
		}
		for _, r := range col.Data {
			if id, _ := r.Attributes["definition_id"].(string); id != "" {
				out[id] = r.Attributes
			}
		}
		next := nextPageCursor(col)
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		query = url.Values{}
		query.Set("page[after]", next)
	}
	return out, nil
}

// keptHubConfig describes an existing hub-config row a --hub run leaves alone,
// or "" when the row already matches the template (nothing was withheld).
func keptHubConfig(d catalog.TemplateAttrDef, cfg map[string]any) string {
	inOn, _ := cfg["is_in_onboarding"].(bool)
	req, _ := cfg["is_required"].(bool)
	if inOn == d.InOnboarding && req == d.Required {
		return ""
	}
	return fmt.Sprintf("config for %q (is_in_onboarding=%t, is_required=%t; template: is_in_onboarding=%t, is_required=%t)",
		d.Slug, inOn, req, d.InOnboarding, d.Required)
}

// onboardingFillPlanDetail is the --dry-run suffix for the onboarding step on a
// --hub run. Only a dry run reads the hub here (the real run reads inside the
// step, right before it writes); a create never gets here.
func onboardingFillPlanDetail(sc *scaffoldContext, t *catalog.HubTemplate) (string, error) {
	const base = "; --hub fills gaps only: an attribute the hub already has a config for keeps it"
	if !sc.dryRun {
		return base, nil
	}
	defs, err := sc.existingContactAttrDefs()
	if err != nil {
		return "", err
	}
	configs, err := sc.existingHubConfigs()
	if err != nil {
		return "", err
	}
	var kept []string
	for _, d := range t.Onboarding {
		if cfg, ok := configs[defs[d.Slug]]; ok && defs[d.Slug] != "" {
			if k := keptHubConfig(d, cfg); k != "" {
				kept = append(kept, k)
			}
		}
	}
	if len(kept) == 0 {
		return base, nil
	}
	return base + "; keeps the hub's " + strings.Join(kept, "; "), nil
}

// ---- pages: conflicts are checked before anything is written ---------------------

// preflightResumePages runs the pages step's §5.1 conflict decision for every
// planned page BEFORE the pipeline writes anything, on a --hub run.
//
// It used to be reached only at step 7 of 9, so a run that exited 2 with
// "refusing to overwrite" had already rewritten the blobs and policies and
// written spaces and onboarding — the opposite of what the message implies.
// The decision is the same pure decideRecovery the step uses, over the same
// reads; step 7 still makes it again right before it writes (the hub can
// change in between), so this only moves the failure earlier. Every conflict
// is reported at once, not just the first.
//
// A create never needs it: a hub this run just created has no pages.
func preflightResumePages(sc *scaffoldContext) error {
	if sc.pagePlan == nil || len(sc.pagePlan.pages) == 0 {
		return nil
	}
	ourApp := catalog.ApplicationID(sc.hubID, sc.hubTmpl.ID)
	var conflicts []string
	for _, pp := range sc.pagePlan.pages {
		rp, err := sc.recoverPageAtSlug(pp.ref.Slug, ourApp)
		if err != nil {
			return err
		}
		switch decideRecovery(ourApp, rp) {
		case actionConflict:
			conflicts = append(conflicts, fmt.Sprintf("page %q conflicts with existing page %s (%s)",
				pp.ref.Slug, rp.id, recoveryConflictReason(ourApp, rp)))
		case actionCreate:
			if !pp.ref.IsHomepage {
				continue
			}
			// The homepage hazard, exactly as the step applies it: creating the
			// homepage entry would clear ANY existing homepage server-side.
			hres, found, herr := sc.findHubPage(func(r client.Resource) bool {
				isHome, _ := r.Attributes["is_homepage"].(bool)
				return isHome
			})
			if herr != nil {
				return herr
			}
			if found {
				conflicts = append(conflicts, fmt.Sprintf("page %q would replace the hub's existing homepage %s (creating a homepage clears the current one server-side)",
					pp.ref.Slug, hres.ID))
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return errs.New(errs.ExitUsage,
		"%s; refusing to overwrite, and nothing was written (pages are checked before any write on --hub) — inspect the page(s), or re-run with --hub %s after resolving",
		strings.Join(conflicts, "; "), sc.hubID)
}
